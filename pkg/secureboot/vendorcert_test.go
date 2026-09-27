package secureboot

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/secureboot/secureboottest"
)

const flatcarCASHA256 = "ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2"

func TestParseFlatcarVendorCertSection(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "flatcar-4757.2.0-vendor_cert.bin"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVendorCertSection(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.SHA256(); got != flatcarCASHA256 {
		t.Errorf("sha256 = %s, want %s", got, flatcarCASHA256)
	}
	if len(v.DER) != 876 {
		t.Errorf("DER is %d bytes", len(v.DER))
	}
	if !strings.Contains(v.Subject, "Flatcar Container Linux Secure Boot Development CA") {
		t.Errorf("subject = %q", v.Subject)
	}
	if v.NotBefore != "2024-11-07" || v.NotAfter != "2037-01-19" {
		t.Errorf("validity = %s .. %s", v.NotBefore, v.NotAfter)
	}
	block, _ := pem.Decode(v.PEM())
	if block == nil || block.Type != "CERTIFICATE" || !bytes.Equal(block.Bytes, v.DER) {
		t.Fatal("PEM does not round-trip the DER")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.IsCA || cert.Subject.String() != cert.Issuer.String() {
		t.Errorf("expected a self-signed CA, got subject %q issuer %q", cert.Subject, cert.Issuer)
	}
}

func TestParseVendorCertSectionRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"short":            {1, 2, 3},
		"zero size":        {0, 0, 0, 0, 0, 0, 0, 0, 16, 0, 0, 0, 16, 0, 0, 0},
		"offset in header": {4, 0, 0, 0, 0, 0, 0, 0, 4, 0, 0, 0, 16, 0, 0, 0, 1, 2, 3, 4},
		"blob past end":    {100, 0, 0, 0, 0, 0, 0, 0, 16, 0, 0, 0, 16, 0, 0, 0, 1, 2, 3, 4},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseVendorCertSection(data); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	opaque := append([]byte{4, 0, 0, 0, 0, 0, 0, 0, 16, 0, 0, 0, 20, 0, 0, 0}, 0xde, 0xad, 0xbe, 0xef)
	v, err := ParseVendorCertSection(opaque)
	if err != nil || !bytes.Equal(v.DER, []byte{0xde, 0xad, 0xbe, 0xef}) || v.Subject != "" {
		t.Fatalf("opaque blob: %+v, %v", v, err)
	}
}

func TestExtractVendorCertFromRealShim(t *testing.T) {
	f, err := os.Open("/tmp/opencode/sb/4757_flatcar_production_image.shim")
	if err != nil {
		t.Skipf("real Flatcar shim not available: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	v, err := ExtractVendorCert(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.SHA256(); got != flatcarCASHA256 {
		t.Errorf("sha256 = %s, want %s", got, flatcarCASHA256)
	}
}

func TestExtractVendorCertRejectsNonPE(t *testing.T) {
	if _, err := ExtractVendorCert(bytes.NewReader([]byte("not a PE file at all, just text"))); err == nil {
		t.Fatal("expected an error")
	}
}

func TestExtractVendorCertFromSyntheticPE(t *testing.T) {
	section, err := os.ReadFile(filepath.Join("testdata", "flatcar-4757.2.0-vendor_cert.bin"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ExtractVendorCert(bytes.NewReader(secureboottest.MinimalShim(section)))
	if err != nil {
		t.Fatal(err)
	}
	if got := v.SHA256(); got != flatcarCASHA256 {
		t.Errorf("sha256 = %s, want %s", got, flatcarCASHA256)
	}
	if _, err := ExtractVendorCert(bytes.NewReader(secureboottest.MinimalShim(nil))); err == nil {
		t.Fatal("empty section must fail")
	}
}
