package versions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupLegacyRegistry(t *testing.T) {
	dir := setupVerify(t)
	CleanupLegacyRegistry()
	if _, err := os.Stat(filepath.Join(dir, "registry")); !os.IsNotExist(err) {
		t.Fatal("nothing to do without a registry directory")
	}

	empty := filepath.Join(dir, "registry", "blobs", "sha256")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	CleanupLegacyRegistry()
	if _, err := os.Stat(filepath.Join(dir, "registry")); !os.IsNotExist(err) {
		t.Fatalf("an empty registry tree is removed, got err=%v", err)
	}

	blob := filepath.Join(empty, "abc")
	touch(t, blob)
	CleanupLegacyRegistry()
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("a registry with blobs is left alone: %v", err)
	}
}
