package creds

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// systemd's encrypted credential format (src/shared/creds-util.c, v257):
//
//	struct encrypted_credential_header {   // in the clear, covered by the GCM AAD
//	        sd_id128_t id;                 // which key: CRED_AES256_GCM_BY_NULL here
//	        le32_t key_size, block_size, iv_size, tag_size;
//	        uint8_t iv[];                  // padded with NULs to the next 8 byte boundary
//	};
//	struct metadata_credential_header {    // encrypted, first in the ciphertext
//	        le64_t timestamp, not_after;   // CLOCK_REALTIME usec, UINT64_MAX = none
//	        le32_t name_size;
//	        char name[];                   // padded with NULs to the next 8 byte boundary
//	};
//	// followed by the encrypted payload and the 16 byte GCM tag
//
// With the null key there is no TPM2/host key blob between header and
// ciphertext; the AES-256 key is SHA-256 over the (absent) host and TPM2 key
// material, i.e. SHA-256 of the empty string (sha256_hash_host_and_tpm2_key).
// systemd-creds writes the whole thing base64 encoded, wrapped at 79 columns,
// and every consumer (PID 1's ImportCredential=, generators, systemd-creds
// decrypt) unbase64s when reading.
var (
	// nullKeyID is CRED_AES256_GCM_BY_NULL, SD_ID128_MAKE(05,84,69,da,f6,f5,43,24,80,05,49,da,0f,8e,a2,fb).
	nullKeyID = [16]byte{0x05, 0x84, 0x69, 0xda, 0xf6, 0xf5, 0x43, 0x24, 0x80, 0x05, 0x49, 0xda, 0x0f, 0x8e, 0xa2, 0xfb}
	// nullKey is what sha256_hash_host_and_tpm2_key returns with neither key set.
	nullKey = sha256.Sum256(nil)
	// ivLabel keys the HMAC that derives the deterministic IV.
	ivLabel = []byte("booty systemd-creds null-key iv")
)

const (
	keySize   = 32 // EVP_CIPHER_key_length(EVP_aes_256_gcm())
	blockSize = 1  // EVP_CIPHER_block_size: GCM is a stream mode
	ivSize    = 12 // EVP_CIPHER_iv_length
	tagSize   = 16 // hard-coded in encrypt_credential_and_warn

	// usecInfinity is USEC_INFINITY: "no timestamp"/"no expiry". The
	// decrypt side skips every timestamp check for it.
	usecInfinity = ^uint64(0)

	fixedHeaderSize   = 16 + 4*4  // id + key/block/iv/tag sizes, before iv[]
	metadataFixedSize = 8 + 8 + 4 // timestamp + not_after + name_size, before name[]
	base64LineLength  = 79        // base64mem_full(..., 79, ...) in systemd-creds

	// credentialNameMax is CREDENTIAL_NAME_MAX (FDNAME_MAX).
	credentialNameMax = 255
)

// Encrypt returns plaintext as a systemd credential encrypted with the null
// key, in the base64 text form `systemd-creds --with-key=null encrypt
// --name=<name>` writes. The result carries name, which systemd checks
// against the file name (minus .cred) when it imports the credential.
//
// The null key provides neither confidentiality nor authenticity -- anyone
// can derive it -- so the IV does not need to be unpredictable either. It is
// HMAC-SHA256(name || 0 || plaintext) truncated to 12 bytes, which keeps
// Encrypt a pure function: Bundle stays byte-for-byte identical across
// Booty restarts and Sum, which is embedded in the iPXE script before the
// installer downloads the tar, stays valid.
func Encrypt(name string, plaintext []byte) ([]byte, error) {
	raw, err := encryptRaw(name, plaintext)
	if err != nil {
		return nil, err
	}
	return wrapBase64(raw), nil
}

func encryptRaw(name string, plaintext []byte) ([]byte, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}

	header := make([]byte, align8(fixedHeaderSize+ivSize))
	copy(header, nullKeyID[:])
	binary.LittleEndian.PutUint32(header[16:], keySize)
	binary.LittleEndian.PutUint32(header[20:], blockSize)
	binary.LittleEndian.PutUint32(header[24:], ivSize)
	binary.LittleEndian.PutUint32(header[28:], tagSize)
	copy(header[fixedHeaderSize:], deriveIV(name, plaintext))

	body := make([]byte, align8(metadataFixedSize+len(name)), align8(metadataFixedSize+len(name))+len(plaintext))
	binary.LittleEndian.PutUint64(body[0:], usecInfinity)
	binary.LittleEndian.PutUint64(body[8:], usecInfinity)
	binary.LittleEndian.PutUint32(body[16:], uint32(len(name)))
	copy(body[metadataFixedSize:], name)
	body = append(body, plaintext...)

	aead, err := newGCM()
	if err != nil {
		return nil, err
	}
	return aead.Seal(header, header[fixedHeaderSize:fixedHeaderSize+ivSize], body, header), nil
}

// Decrypt is the inverse of Encrypt for null-key credentials, accepting the
// base64 text or the raw binary form. When name is not empty the embedded
// name must match it, like systemd does for credential files.
func Decrypt(name string, data []byte) ([]byte, error) {
	raw, err := unwrapBase64(data)
	if err != nil {
		return nil, err
	}
	if len(raw) < fixedHeaderSize {
		return nil, errors.New("encrypted credential too short")
	}
	if !bytes.Equal(raw[:16], nullKeyID[:]) {
		return nil, errors.New("credential is not encrypted with the null key")
	}
	ks := binary.LittleEndian.Uint32(raw[16:])
	bs := binary.LittleEndian.Uint32(raw[20:])
	ivs := binary.LittleEndian.Uint32(raw[24:])
	ts := binary.LittleEndian.Uint32(raw[28:])
	if ks != keySize || bs != blockSize || ivs != ivSize || ts != tagSize {
		return nil, fmt.Errorf("unexpected cipher parameters in header: key %d block %d iv %d tag %d", ks, bs, ivs, ts)
	}
	headerLen := align8(fixedHeaderSize + ivSize)
	if len(raw) < headerLen+align8(metadataFixedSize)+tagSize {
		return nil, errors.New("encrypted credential too short")
	}
	aead, err := newGCM()
	if err != nil {
		return nil, err
	}
	body, err := aead.Open(nil, raw[fixedHeaderSize:fixedHeaderSize+ivSize], raw[headerLen:], raw[:headerLen])
	if err != nil {
		return nil, fmt.Errorf("decrypting credential: %w", err)
	}
	if len(body) < align8(metadataFixedSize) {
		return nil, errors.New("metadata header incomplete")
	}
	nameLen := binary.LittleEndian.Uint32(body[16:])
	if nameLen > credentialNameMax {
		return nil, errors.New("embedded credential name too long")
	}
	metaLen := align8(metadataFixedSize + int(nameLen))
	if len(body) < metaLen {
		return nil, errors.New("metadata header incomplete")
	}
	embedded := string(body[metadataFixedSize : metadataFixedSize+int(nameLen)])
	if name != "" && embedded != name {
		return nil, fmt.Errorf("embedded credential name %q does not match %q", embedded, name)
	}
	return body[metaLen:], nil
}

func newGCM() (cipher.AEAD, error) {
	block, err := aes.NewCipher(nullKey[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func deriveIV(name string, plaintext []byte) []byte {
	mac := hmac.New(sha256.New, ivLabel)
	mac.Write([]byte(name))
	mac.Write([]byte{0})
	mac.Write(plaintext)
	return mac.Sum(nil)[:ivSize]
}

// validateName mirrors credential_name_valid: a plain file name that is also
// usable as a file descriptor name (no '/', ':' or control characters, not
// "." or "..", at most 255 bytes).
func validateName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > credentialNameMax {
		return fmt.Errorf("invalid credential name %q", name)
	}
	if strings.ContainsAny(name, "/:") {
		return fmt.Errorf("invalid credential name %q", name)
	}
	for _, c := range []byte(name) {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("invalid credential name %q", name)
		}
	}
	return nil
}

func align8(n int) int {
	return (n + 7) &^ 7
}

// wrapBase64 encodes raw the way base64mem_full(raw, len, 79, ...) followed
// by write_string_file does: standard alphabet with padding, a newline after
// every 79 characters and one at the end.
func wrapBase64(raw []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(raw)
	var out bytes.Buffer
	out.Grow(len(enc) + len(enc)/base64LineLength + 2)
	for len(enc) > base64LineLength {
		out.WriteString(enc[:base64LineLength])
		out.WriteByte('\n')
		enc = enc[base64LineLength:]
	}
	out.WriteString(enc)
	out.WriteByte('\n')
	return out.Bytes()
}

// unwrapBase64 accepts the base64 text form (whitespace ignored, as
// READ_FULL_FILE_UNBASE64 does) and passes raw binary through.
func unwrapBase64(data []byte) ([]byte, error) {
	if len(data) >= 16 && bytes.Equal(data[:16], nullKeyID[:]) {
		return data, nil
	}
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, string(data))
	raw, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return nil, fmt.Errorf("credential is neither base64 nor a raw null-key credential: %w", err)
	}
	return raw, nil
}
