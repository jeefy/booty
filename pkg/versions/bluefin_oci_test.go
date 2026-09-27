package versions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/viper"
	"oras.land/oras-go/v2/registry/remote"
)

// pushBluefinArtifact publishes rel the way the release workflow's ORAS push
// does: one layer per file titled with its name, tagged with the version
// and each extra tag.
func pushBluefinArtifact(t *testing.T, host string, rel *fakeBluefinRelease, tags ...string) {
	t.Helper()
	ctx := context.Background()
	repo, err := remote.NewRepository(host + "/projectbluefin/bluefin-server")
	if err != nil {
		t.Fatal(err)
	}
	repo.PlainHTTP = true
	files := map[string]string{BluefinSumsFile: rel.sums, BluefinSigFile: rel.sig}
	for name, body := range rel.files {
		files[name] = body
	}
	var layers []ocispec.Descriptor
	for name, body := range files {
		desc := ocispec.Descriptor{
			MediaType:   "application/octet-stream",
			Digest:      digest.FromString(body),
			Size:        int64(len(body)),
			Annotations: map[string]string{ocispec.AnnotationTitle: name},
		}
		if err := repo.Push(ctx, desc, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		layers = append(layers, desc)
	}
	cfg := []byte("{}")
	cfgDesc := ocispec.Descriptor{MediaType: "application/vnd.oci.empty.v1+json", Digest: digest.FromBytes(cfg), Size: int64(len(cfg))}
	if err := repo.Push(ctx, cfgDesc, bytes.NewReader(cfg)); err != nil {
		t.Fatal(err)
	}
	manifest := ocispec.Manifest{MediaType: ocispec.MediaTypeImageManifest, ArtifactType: "application/vnd.projectbluefin.server.release", Config: cfgDesc, Layers: layers}
	manifest.SchemaVersion = 2
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	desc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromBytes(body), Size: int64(len(body))}
	for _, tag := range append([]string{rel.version}, tags...) {
		if err := repo.PushReference(ctx, desc, bytes.NewReader(body), tag); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBluefinVersionCheckFromOCI(t *testing.T) {
	reg := httptest.NewServer(registry.New())
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "http://")

	old := newFakeRelease(t, "20260926.200", nil)
	pushBluefinArtifact(t, host, old)
	const ver = "20260927.123"
	rel := newFakeRelease(t, ver, map[string]string{"kubestellar_" + ver + ".raw.zst": "KS", "k0s-1.36.4-k0s.0.raw.zst": "K0S"})
	pushBluefinArtifact(t, host, rel, "latest")

	github, hits := fakeGitHub(t, `[]`)
	dir := setupBluefin(t, github.URL)
	viper.Set(config.BluefinOCI, "http://"+host+"/projectbluefin/bluefin-server")

	BluefinVersionCheck()

	if got := state.CurrentBluefinVersion(); got != ver {
		t.Fatalf("current=%q want %s (latest)", got, ver)
	}
	if state.RemoteBluefinVersion() != ver {
		t.Fatalf("remote=%q", state.RemoteBluefinVersion())
	}
	if len(hits) != 0 {
		t.Fatalf("--bluefinOCI must not touch GitHub: %v", hits)
	}
	relDir := filepath.Join(dir, "bluefin", ver)
	for name, want := range map[string]string{
		BluefinNetbootUKI(ver):        "UKI-" + ver,
		BluefinDDI(ver):               "DDI-" + ver,
		BluefinSumsFile:               rel.sums,
		"kubestellar_" + ver + ".raw": "KS",
		"k0s-1.36.4-k0s.0.raw":        "K0S",
	} {
		if got := readText(t, filepath.Join(relDir, name)); got != want {
			t.Fatalf("%s=%q want %q", name, got, want)
		}
	}
	m, ok := CurrentBluefinManifest()
	if !ok || m.Sysexts["k0s"].Sha256 != sha("K0S") || m.Sysexts["kubestellar"].File != "kubestellar_"+ver+".raw" {
		t.Fatalf("manifest %+v ok=%v", m, ok)
	}

	t.Run("pin resolves the version tag", func(t *testing.T) {
		viper.Set(config.BluefinVersion, old.version)
		state.Init()
		BluefinVersionCheck()
		if got := state.CurrentBluefinVersion(); got != old.version {
			t.Fatalf("current=%q want pinned %s", got, old.version)
		}
		if prev, ok := PreviousBluefinManifest(); !ok || prev.Version != ver {
			t.Fatalf("previous %+v ok=%v", prev, ok)
		}
	})

	t.Run("tampered layer fails closed", func(t *testing.T) {
		bad := newFakeRelease(t, "20260928.1", nil)
		bad.sums = strings.ReplaceAll(bad.sums, sha("DDI-20260928.1"), strings.Repeat("0", 64))
		pushBluefinArtifact(t, host, bad)
		viper.Set(config.BluefinVersion, bad.version)
		state.Init()
		BluefinVersionCheck()
		if got := state.CurrentBluefinVersion(); got != old.version {
			t.Fatalf("version advanced to %q", got)
		}
		if _, err := os.Stat(filepath.Join(dir, "bluefin", bad.version, BluefinDDI(bad.version))); !os.IsNotExist(err) {
			t.Fatal("an unverified DDI must not be kept")
		}
	})
}

func TestOCIBluefinVersion(t *testing.T) {
	layer := func(title string) ocispec.Descriptor {
		return ocispec.Descriptor{Annotations: map[string]string{ocispec.AnnotationTitle: title}}
	}
	if v, err := ociBluefinVersion(ocispec.Manifest{Layers: []ocispec.Descriptor{layer("SHA256SUMS"), layer("bluefin-server-netboot_20260927.123.efi"), layer("bluefin-server-20260927.123.efi")}}); err != nil || v != "20260927.123" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	for _, m := range []ocispec.Manifest{
		{Layers: []ocispec.Descriptor{layer("SHA256SUMS")}},
		{Layers: []ocispec.Descriptor{layer("bluefin-server-netboot_1.efi"), layer("bluefin-server-netboot_2.efi")}},
		{Layers: []ocispec.Descriptor{layer("bluefin-server-netboot_..efi")}},
	} {
		if _, err := ociBluefinVersion(m); err == nil {
			t.Fatalf("%+v must be refused", m.Layers)
		}
	}
}
