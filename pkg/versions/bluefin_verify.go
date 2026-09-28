package versions

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/jeefy/booty/pkg/config"
	"github.com/klauspost/compress/zstd"
)

const armorPrefix = "-----BEGIN PGP"

// loadBluefinKeyring reads an OpenPGP public keyring, binary (gpg --export,
// what gpgv --keyring takes) or ASCII-armored.
func loadBluefinKeyring(path string) (openpgp.EntityList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys openpgp.EntityList
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte(armorPrefix)) {
		keys, err = openpgp.ReadArmoredKeyRing(bytes.NewReader(data))
	} else {
		keys, err = openpgp.ReadKeyRing(bytes.NewReader(data))
	}
	if err != nil {
		return nil, fmt.Errorf("%s: not an OpenPGP public keyring: %w", path, err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s: keyring holds no keys", path)
	}
	return keys, nil
}

// verifyBluefinSums checks sig, a detached (binary or armored) signature,
// over sums with the keyring at path and returns the signing key's ID. Any
// failure is an error: with --bluefinKeyring set nothing unverified is
// served.
func verifyBluefinSums(path string, sums, sig []byte) (string, error) {
	keys, err := loadBluefinKeyring(path)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(sig)) == 0 {
		return "", fmt.Errorf("%s is empty", BluefinSigFile)
	}
	var signer *openpgp.Entity
	if bytes.HasPrefix(bytes.TrimSpace(sig), []byte(armorPrefix)) {
		signer, err = openpgp.CheckArmoredDetachedSignature(keys, bytes.NewReader(sums), bytes.NewReader(sig), nil)
	} else {
		signer, err = openpgp.CheckDetachedSignature(keys, bytes.NewReader(sums), bytes.NewReader(sig), nil)
	}
	if err != nil {
		return "", fmt.Errorf("%s does not verify %s with --%s %s: %w", BluefinSigFile, BluefinSumsFile, config.BluefinKeyring, path, err)
	}
	if signer == nil || signer.PrimaryKey == nil {
		return "", fmt.Errorf("%s: no signer", BluefinSigFile)
	}
	return strings.ToUpper(hex.EncodeToString(signer.PrimaryKey.Fingerprint)), nil
}

// decompressZstd writes the zstd stream in src to dst and returns the
// sha256 of the decompressed bytes.
func decompressZstd(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer config.CloseQuietly(in, src)
	dec, err := zstd.NewReader(in)
	if err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	defer dec.Close()
	sum, err := writeHashed(dec, dst, func(string) error { return nil })
	if err != nil {
		return "", fmt.Errorf("decompressing %s: %w", src, err)
	}
	return sum, nil
}
