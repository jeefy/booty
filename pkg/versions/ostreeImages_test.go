package versions

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestDeleteUnreferencedBlobs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sha256")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"aaa", "bbb", "ccc", "ddd"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	referenced := map[string]bool{"aaa": true, "ccc": true, "zzz-not-on-disk": true}
	deleted, kept, err := deleteUnreferencedBlobs(dir, referenced)
	if err != nil {
		t.Fatalf("deleteUnreferencedBlobs: %v", err)
	}
	if deleted != 2 || kept != 2 {
		t.Fatalf("deleted=%d kept=%d, want 2/2", deleted, kept)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	want := []string{"aaa", "ccc", "subdir"}
	if len(names) != len(want) {
		t.Fatalf("remaining %v want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("remaining %v want %v", names, want)
		}
	}

	deleted, kept, err = deleteUnreferencedBlobs(dir, referenced)
	if err != nil || deleted != 0 || kept != 2 {
		t.Fatalf("second pass: deleted=%d kept=%d err=%v", deleted, kept, err)
	}

	deleted, kept, err = deleteUnreferencedBlobs(filepath.Join(dir, "missing"), referenced)
	if err != nil || deleted != 0 || kept != 0 {
		t.Fatalf("missing dir: deleted=%d kept=%d err=%v", deleted, kept, err)
	}
}

func TestDeleteUnreferencedBlobsEmptySetDeletesEverything(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orphan"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deleted, kept, err := deleteUnreferencedBlobs(dir, map[string]bool{})
	if err != nil || deleted != 1 || kept != 0 {
		t.Fatalf("deleted=%d kept=%d err=%v", deleted, kept, err)
	}
}
