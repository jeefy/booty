package config

import (
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDownloadServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to not exist, stat err=%v", path, err)
	}
}

func TestDownloadSuccess(t *testing.T) {
	srv := newDownloadServer(t, "hello world", http.StatusOK)
	dest := filepath.Join(t.TempDir(), "sub", "file.bin")

	if err := Download(context.Background(), srv.Client(), srv.URL+"/file.bin", dest, 0, ""); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("unexpected content %q", got)
	}
	assertNoFile(t, dest+".tmp")
}

func TestDownloadWithChecksum(t *testing.T) {
	body := "verified payload"
	sum := sha256.Sum256([]byte(body))
	srv := newDownloadServer(t, body, http.StatusOK)
	dest := filepath.Join(t.TempDir(), "file.bin")

	if err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, strings.ToUpper(hex.EncodeToString(sum[:]))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !FileHashMatches(dest, crypto.SHA256, hex.EncodeToString(sum[:])) {
		t.Fatalf("FileHashMatches should be true for freshly downloaded file")
	}
}

func TestDownloadHTTPError(t *testing.T) {
	srv := newDownloadServer(t, "nope", http.StatusNotFound)
	dest := filepath.Join(t.TempDir(), "file.bin")

	err := Download(context.Background(), srv.Client(), srv.URL, dest, 0, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
	assertNoFile(t, dest)
	assertNoFile(t, dest+".tmp")
}

func TestDownloadChecksumMismatch(t *testing.T) {
	srv := newDownloadServer(t, "tampered", http.StatusOK)
	dest := filepath.Join(t.TempDir(), "file.bin")

	err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, strings.Repeat("00", 32))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	assertNoFile(t, dest)
	assertNoFile(t, dest+".tmp")
}

func TestDownloadRejectsUnsupportedHash(t *testing.T) {
	srv := newDownloadServer(t, "x", http.StatusOK)
	err := Download(context.Background(), srv.Client(), srv.URL, filepath.Join(t.TempDir(), "f"), crypto.MD5, "abcd")
	if err == nil {
		t.Fatal("expected error for unsupported hash")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	if err := WriteFileAtomic(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if err := WriteFileAtomic(path, []byte(`{"a":2}`), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic overwrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != `{"a":2}` {
		t.Fatalf("unexpected content %q err=%v", got, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file, got %d", len(entries))
	}
}

func TestReplaceSymlinkOverRegularFile(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "kernel")
	if err := os.WriteFile(link, []byte("old regular file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "flatcar", "1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flatcar", "1.0.0", "kernel"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceSymlink(filepath.Join("flatcar", "1.0.0", "kernel"), link); err != nil {
		t.Fatalf("ReplaceSymlink: %v", err)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != filepath.Join("flatcar", "1.0.0", "kernel") {
		t.Fatalf("unexpected target %q", target)
	}
	got, err := os.ReadFile(link)
	if err != nil || string(got) != "new" {
		t.Fatalf("unexpected content %q err=%v", got, err)
	}
	assertNoFile(t, link+".tmp")
}

func TestCleanRelPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"config/ignition.yaml", "config/ignition.yaml", true},
		{"./config//ignition.yaml", "config/ignition.yaml", true},
		{"a/b/../c.yaml", "a/c.yaml", true},
		{"", "", false},
		{".", "", false},
		{"..", "", false},
		{"../etc/passwd", "", false},
		{"a/../../x", "", false},
		{"/etc/passwd", "", false},
		{"config/\x00.yaml", "", false},
	}
	for _, tc := range tests {
		got, err := CleanRelPath(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("CleanRelPath(%q): ok=%v err=%v", tc.in, tc.ok, err)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("CleanRelPath(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
