// Package secureboot holds the pure parsers behind Booty's UEFI Secure Boot
// support: today the shim ".vendor_cert" section, from which the Flatcar
// Secure Boot CA is lifted so users can enroll it in their firmware db.
package secureboot

import (
	"crypto/sha256"
	"crypto/x509"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"strings"
)

// VendorCertSection is the name of the shim section that carries the
// vendor certificate (or a signed db of them) shim trusts in addition to
// the firmware's db.
const VendorCertSection = ".vendor_cert"

// VendorCert is the authorised certificate found in a shim.
type VendorCert struct {
	DER []byte
	// Subject is the certificate's subject as Go prints it, empty when
	// the DER is a db (EFI_SIGNATURE_LIST) rather than a single
	// certificate.
	Subject   string
	NotBefore string
	NotAfter  string
}

// SHA256 is the lowercase hex digest of the DER, the fingerprint firmware
// setup screens and sbctl show.
func (v VendorCert) SHA256() string {
	sum := sha256.Sum256(v.DER)
	return hex.EncodeToString(sum[:])
}

// PEM renders the DER as a CERTIFICATE block.
func (v VendorCert) PEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: v.DER})
}

// ExtractVendorCert reads the PE image (a shim) on r and returns the
// vendor certificate its .vendor_cert section authorises. debug/pe
// resolves the "/N" long section names shim uses through the COFF string
// table, which is why the file is parsed rather than scanned.
func ExtractVendorCert(r io.ReaderAt) (VendorCert, error) {
	f, err := pe.NewFile(r)
	if err != nil {
		return VendorCert{}, fmt.Errorf("not a PE image: %w", err)
	}
	defer func() { _ = f.Close() }()
	sec := f.Section(VendorCertSection)
	if sec == nil {
		names := make([]string, 0, len(f.Sections))
		for _, s := range f.Sections {
			names = append(names, s.Name)
		}
		return VendorCert{}, fmt.Errorf("no %s section (sections: %s)", VendorCertSection, strings.Join(names, ", "))
	}
	data, err := sec.Data()
	if err != nil {
		return VendorCert{}, fmt.Errorf("%s: %w", VendorCertSection, err)
	}
	if int(sec.VirtualSize) < len(data) {
		data = data[:sec.VirtualSize]
	}
	return ParseVendorCertSection(data)
}

// ParseVendorCertSection decodes shim's vendor_cert layout: four little
// endian uint32 (vendor_authorized_size, vendor_deauthorized_size,
// vendor_authorized_offset, vendor_deauthorized_offset) followed by the
// blobs at those offsets, relative to the section start.
func ParseVendorCertSection(data []byte) (VendorCert, error) {
	if len(data) < 16 {
		return VendorCert{}, fmt.Errorf("%s: %d bytes is too short for the header", VendorCertSection, len(data))
	}
	authSize := binary.LittleEndian.Uint32(data[0:4])
	authOff := binary.LittleEndian.Uint32(data[8:12])
	if authSize == 0 {
		return VendorCert{}, fmt.Errorf("%s: no authorised vendor certificate", VendorCertSection)
	}
	end := uint64(authOff) + uint64(authSize)
	if authOff < 16 || end > uint64(len(data)) {
		return VendorCert{}, fmt.Errorf("%s: authorised blob at %d+%d exceeds %d bytes", VendorCertSection, authOff, authSize, len(data))
	}
	v := VendorCert{DER: append([]byte(nil), data[authOff:end]...)}
	if cert, err := x509.ParseCertificate(v.DER); err == nil {
		v.Subject = cert.Subject.String()
		v.NotBefore = cert.NotBefore.UTC().Format("2006-01-02")
		v.NotAfter = cert.NotAfter.UTC().Format("2006-01-02")
	}
	return v, nil
}
