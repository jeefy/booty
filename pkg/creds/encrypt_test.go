package creds

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptLayoutMatchesSystemd(t *testing.T) {
	raw, err := encryptRaw("sample", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := append(append([]byte{}, nullKeyID[:]...),
		0x20, 0, 0, 0, // key_size 32
		0x01, 0, 0, 0, // block_size 1
		0x0c, 0, 0, 0, // iv_size 12
		0x10, 0, 0, 0, // tag_size 16
	)
	if !bytes.Equal(raw[:32], wantHeader) {
		t.Fatalf("header mismatch:\n%x\n%x", raw[:32], wantHeader)
	}
	if !bytes.Equal(raw[44:48], []byte{0, 0, 0, 0}) {
		t.Fatalf("iv must be padded to 8 bytes with NULs, got %x", raw[44:48])
	}
	// 48 header + ALIGN8(20+6)=32 metadata + 5 payload + 16 tag
	if len(raw) != 48+32+5+16 {
		t.Fatalf("unexpected length %d", len(raw))
	}

	enc, err := Encrypt("sample", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(enc, []byte("BYRp2vb1QySABUnaD46i")) {
		t.Fatalf("base64 output must start with the null key id: %q", enc)
	}
	if !bytes.HasSuffix(enc, []byte("\n")) || bytes.Contains(enc, []byte("\n\n")) {
		t.Fatalf("expected single trailing newline: %q", enc)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(enc), "\n"), "\n") {
		if len(line) > base64LineLength || len(line) == 0 {
			t.Fatalf("line length %d out of range: %q", len(line), line)
		}
	}
	long, err := Encrypt("sample", bytes.Repeat([]byte("x"), 300))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(long), "\n"), "\n")
	if len(lines) < 3 || len(lines[0]) != base64LineLength || len(lines[1]) != base64LineLength {
		t.Fatalf("long output must wrap at %d columns: %q", base64LineLength, long)
	}
}

func TestEncryptIsDeterministicAndRoundTrips(t *testing.T) {
	a, err := Encrypt("firstboot.hostname", []byte("node1\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encrypt("firstboot.hostname", []byte("node1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("encryption must be deterministic")
	}
	c, err := Encrypt("firstboot.hostname", []byte("node2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("different plaintext must give different output")
	}

	got, err := Decrypt("firstboot.hostname", a)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "node1\n" {
		t.Fatalf("round trip gave %q", got)
	}
	if _, err := Decrypt("other.name", a); err == nil {
		t.Fatal("embedded name mismatch must be rejected")
	}
	if _, err := Decrypt("", a); err != nil {
		t.Fatalf("empty name must skip the name check: %v", err)
	}
	tampered := append([]byte{}, a...)
	tampered[len(tampered)/2] ^= 0x01
	if _, err := Decrypt("firstboot.hostname", tampered); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}

	empty, err := Encrypt("empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Decrypt("empty", empty); err != nil || len(got) != 0 {
		t.Fatalf("empty payload round trip: %q %v", got, err)
	}
}

func TestEncryptRejectsInvalidNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", "a:b", "a\nb", strings.Repeat("x", 256)} {
		if _, err := Encrypt(name, []byte("x")); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
	for _, name := range []string{"tmpfiles.extra", "systemd.unit-dropin.multi-user.target~booty", "systemd.extra-unit.booty-update.timer"} {
		if _, err := Encrypt(name, []byte("x")); err != nil {
			t.Errorf("name %q should be accepted: %v", name, err)
		}
	}
}

// systemdCreds returns how to run systemd-creds decrypt on this host, or
// skips: it needs the binary and, when running unprivileged, the
// io.systemd.Credentials varlink service (systemd-creds.socket) which
// refuses null-key operations, so root (via passwordless sudo) is needed.
func systemdCreds(t *testing.T) []string {
	t.Helper()
	bin, err := exec.LookPath("systemd-creds")
	if err != nil {
		t.Skip("systemd-creds not installed")
	}
	if os.Geteuid() == 0 {
		return []string{bin}
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		t.Skip("not root and no sudo")
	}
	if err := exec.Command(sudo, "-n", "true").Run(); err != nil {
		t.Skip("not root and sudo needs a password")
	}
	return []string{sudo, "-n", bin}
}

func runCreds(cmd []string, args ...string) ([]byte, error) {
	c := exec.Command(cmd[0], append(cmd[1:], args...)...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", strings.Join(append(cmd, args...), " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func TestSystemdCredsDecryptsGoOutput(t *testing.T) {
	cmd := systemdCreds(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	f := features(t, "hostname,update,booted,sshkeys")
	for name, plaintext := range Credentials(in, f) {
		enc, err := Encrypt(name, []byte(plaintext))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name+credSuffix)
		if err := os.WriteFile(path, enc, 0o644); err != nil {
			t.Fatal(err)
		}
		// --name= is what ImportCredential= enforces: the embedded name must
		// equal the file name without .cred. --allow-null skips the
		// TPM2+SecureBoot refusal that applies to the host running the test,
		// not to the format.
		out, err := runCreds(cmd, "--with-key=null", "--allow-null", "decrypt", "--name="+name, path, "-")
		if err != nil {
			t.Fatalf("systemd-creds could not decrypt %s: %v", name, err)
		}
		if string(out) != plaintext {
			t.Fatalf("%s: systemd-creds decrypted to %q, want %q", name, out, plaintext)
		}
		if _, err := runCreds(cmd, "--with-key=null", "--allow-null", "decrypt", "--name=wrong", path, "-"); err == nil {
			t.Fatalf("%s: systemd-creds must reject a mismatching --name", name)
		}
	}
}

func TestGoDecryptsSystemdCredsOutput(t *testing.T) {
	cmd := systemdCreds(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plaintext := "hello from systemd-creds\n" + strings.Repeat("0123456789", 20)
	src := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(src, []byte(plaintext), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "oracle.cred")
	if _, err := runCreds(cmd, "--with-key=null", "encrypt", "--name=oracle", src, dst); err != nil {
		t.Fatalf("systemd-creds encrypt failed: %v", err)
	}
	enc, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(enc, []byte("BYRp2vb1")) {
		t.Fatalf("expected base64 null-key credential, got %q", enc[:20])
	}
	got, err := Decrypt("oracle", enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != plaintext {
		t.Fatalf("Go decrypt gave %q", got)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(enc), "\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Decrypt("oracle", raw); err != nil || string(got) != plaintext {
		t.Fatalf("Go decrypt of raw bytes: %q %v", got, err)
	}
	if _, err := Decrypt("other", enc); err == nil {
		t.Fatal("name mismatch must be rejected")
	}
}
