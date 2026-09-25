package versions

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func newRegistry(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.WithBlobHandler(registry.NewDiskBlobHandler(t.TempDir()))))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func mustRef(t *testing.T, s string) name.Reference {
	t.Helper()
	ref, err := name.ParseReference(s, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func TestStoreAndReplayImage(t *testing.T) {
	ctx := context.Background()
	_, srcHost := newRegistry(t)
	const image = "ghcr.io/ublue-os/ucore:stable"

	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(mustRef(t, srcHost+"/"+image), img); err != nil {
		t.Fatal(err)
	}
	wantDigest, _ := img.Digest()

	store := t.TempDir()
	if err := StoreManifests(ctx, store, srcHost, image); err != nil {
		t.Fatalf("StoreManifests: %v", err)
	}
	if got := listFiles(t, store); len(got) != 1 || got[0] != "ghcr.io/ublue-os/ucore/stable.json" {
		t.Fatalf("stored files %v", got)
	}

	dst, dstHost := newRegistry(t)
	if _, err := remote.Get(mustRef(t, dstHost+"/"+image)); err == nil {
		t.Fatal("fresh registry should not know the image yet")
	}
	restored, failed, err := ReplayManifests(ctx, store, dst.URL)
	if err != nil || restored != 1 || failed != 0 {
		t.Fatalf("replay: restored=%d failed=%d err=%v", restored, failed, err)
	}
	desc, err := remote.Get(mustRef(t, dstHost+"/"+image))
	if err != nil {
		t.Fatalf("image not resolvable after replay: %v", err)
	}
	if desc.Digest != wantDigest {
		t.Fatalf("digest %s want %s", desc.Digest, wantDigest)
	}
}

func TestStoreAndReplayIndex(t *testing.T) {
	ctx := context.Background()
	_, srcHost := newRegistry(t)
	const image = "ghcr.io/ublue-os/bazzite:41"

	idx, err := random.Index(512, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(mustRef(t, srcHost+"/"+image), idx); err != nil {
		t.Fatal(err)
	}
	wantDigest, _ := idx.Digest()
	im, _ := idx.IndexManifest()

	store := t.TempDir()
	if err := StoreManifests(ctx, store, srcHost, image); err != nil {
		t.Fatalf("StoreManifests: %v", err)
	}
	files := listFiles(t, store)
	if len(files) != 1+len(im.Manifests) {
		t.Fatalf("expected tag + %d children, got %v", len(im.Manifests), files)
	}
	for _, m := range im.Manifests {
		want := "ghcr.io/ublue-os/bazzite/" + childFilePrefix + m.Digest.Hex + ".json"
		if !slices.Contains(files, want) {
			t.Fatalf("child %s not stored; have %v", want, files)
		}
	}

	dst, dstHost := newRegistry(t)
	restored, failed, err := ReplayManifests(ctx, store, dst.URL)
	if err != nil || failed != 0 || restored != len(files) {
		t.Fatalf("replay: restored=%d failed=%d err=%v", restored, failed, err)
	}
	desc, err := remote.Get(mustRef(t, dstHost+"/"+image))
	if err != nil {
		t.Fatalf("index not resolvable after replay: %v", err)
	}
	if desc.Digest != wantDigest || !desc.MediaType.IsIndex() {
		t.Fatalf("got %s (%s) want index %s", desc.Digest, desc.MediaType, wantDigest)
	}
	for _, m := range im.Manifests {
		if _, err := remote.Get(mustRef(t, dstHost+"/ghcr.io/ublue-os/bazzite@"+m.Digest.String())); err != nil {
			t.Fatalf("child %s not resolvable after replay: %v", m.Digest, err)
		}
	}

	if pruned, err := PruneStoredManifests(store, []string{image}); err != nil || pruned != 0 {
		t.Fatalf("pruning with the index kept must remove nothing: pruned=%d err=%v", pruned, err)
	}
	if pruned, err := PruneStoredManifests(store, nil); err != nil || pruned != len(files) {
		t.Fatalf("pruning with nothing kept: pruned=%d want %d err=%v", pruned, len(files), err)
	}
	if got := listFiles(t, store); len(got) != 0 {
		t.Fatalf("store should be empty, got %v", got)
	}
}

func TestReplayMissingDirIsNoop(t *testing.T) {
	restored, failed, err := ReplayManifests(context.Background(), filepath.Join(t.TempDir(), "nope"), "http://127.0.0.1:1")
	if err != nil || restored != 0 || failed != 0 {
		t.Fatalf("restored=%d failed=%d err=%v", restored, failed, err)
	}
}

func TestPruneStoredManifests(t *testing.T) {
	store := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(store, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const keptHex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stableIndex := `{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:1111","raw":` +
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","size":1,"digest":"sha256:` + keptHex + `"}]}}`
	write("ghcr.io/ublue-os/ucore/stable.json", stableIndex)
	write("ghcr.io/ublue-os/ucore/testing.json", `{}`)
	write("ghcr.io/ublue-os/ucore/"+childFilePrefix+keptHex+".json", `{}`)
	write("ghcr.io/ublue-os/ucore/"+childFilePrefix+"stale.json", `{}`)
	write("ghcr.io/ublue-os/bazzite/41.json", `{}`)
	write("ghcr.io/ublue-os/bazzite/"+childFilePrefix+"bbb.json", `{}`)
	write("ubuntu/latest.json", `{}`)

	pruned, err := PruneStoredManifests(store, []string{"ghcr.io/ublue-os/ucore:stable", "not a ref!!"})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 5 {
		t.Fatalf("pruned=%d want 5", pruned)
	}
	got := listFiles(t, store)
	want := []string{"ghcr.io/ublue-os/ucore/" + childFilePrefix + keptHex + ".json", "ghcr.io/ublue-os/ucore/stable.json"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("remaining %v want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(store, "ubuntu")); !os.IsNotExist(err) {
		t.Fatal("empty repo dirs should be removed")
	}

	pruned, err = PruneStoredManifests(filepath.Join(store, "missing"), nil)
	if err != nil || pruned != 0 {
		t.Fatalf("missing dir: pruned=%d err=%v", pruned, err)
	}
}

func TestManifestFilePaths(t *testing.T) {
	for _, tc := range []struct{ image, want string }{
		{"ghcr.io/ublue-os/ucore:stable", "ghcr.io/ublue-os/ucore/stable.json"},
		{"ubuntu:latest", "ubuntu/latest.json"},
		{"quay.io/fedora/fedora-coreos:stable", "quay.io/fedora/fedora-coreos/stable.json"},
		{"ghcr.io/ublue-os/ucore", "ghcr.io/ublue-os/ucore/latest.json"},
	} {
		_, tagFile, err := manifestFilePaths("/store", tc.image)
		if err != nil {
			t.Fatalf("%s: %v", tc.image, err)
		}
		if got := filepath.ToSlash(tagFile); got != "/store/"+tc.want {
			t.Fatalf("%s: got %s want /store/%s", tc.image, got, tc.want)
		}
	}
	if _, _, err := manifestFilePaths("/store", "ghcr.io/x/y@sha256:0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("digest references must be rejected")
	}
}
