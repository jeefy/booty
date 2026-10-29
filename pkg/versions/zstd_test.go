package versions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func writeZstd(t *testing.T, path string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDecompressZstdCap(t *testing.T) {
	old := maxZstdOutput
	maxZstdOutput = 1024
	t.Cleanup(func() { maxZstdOutput = old })
	dir := t.TempDir()

	src := filepath.Join(dir, "ok.zst")
	writeZstd(t, src, bytes.Repeat([]byte("a"), 1024))
	if _, err := decompressZstd(src, filepath.Join(dir, "ok.raw")); err != nil {
		t.Fatalf("at cap: %v", err)
	}

	big := filepath.Join(dir, "big.zst")
	writeZstd(t, big, bytes.Repeat([]byte("a"), 1025))
	dst := filepath.Join(dir, "big.raw")
	_, err := decompressZstd(big, dst)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected overflow error, got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("overflowing output left behind")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp left: %s", e.Name())
		}
	}
}
