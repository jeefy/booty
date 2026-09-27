package versions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/rpm/rpmtest"
	"github.com/jeefy/booty/pkg/secureboot/secureboottest"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func tarGz(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type sbFixture struct {
	shim, ipxe, snponly, fedoraShim, fedoraGrub []byte
	vendorCert                                  []byte
	flatcarShim                                 []byte
	hits                                        map[string]*atomic.Int32
}

// fakeSecureBootUpstream serves GitHub (API + downloads), the Fedora mirror
// tree and a Flatcar release directory from one httptest server with
// non-default versions so the pin tables are bypassed and the API digest
// path is exercised.
func fakeSecureBootUpstream(t *testing.T) (*httptest.Server, *sbFixture) {
	t.Helper()
	section, err := os.ReadFile(filepath.Join("..", "secureboot", "testdata", "flatcar-4757.2.0-vendor_cert.bin"))
	if err != nil {
		t.Fatal(err)
	}
	fx := &sbFixture{
		shim:        bytes.Repeat([]byte("MZ-ipxe-shim"), 50),
		ipxe:        bytes.Repeat([]byte("MZ-ipxe-sb"), 40),
		snponly:     bytes.Repeat([]byte("MZ-snponly-sb"), 30),
		fedoraShim:  bytes.Repeat([]byte("MZ-fedora-shim"), 20),
		fedoraGrub:  bytes.Repeat([]byte("MZ-fedora-grub"), 20),
		vendorCert:  section,
		flatcarShim: secureboottest.MinimalShim(section),
		hits:        map[string]*atomic.Int32{},
	}
	tarball := tarGz(t, map[string][]byte{
		"ipxeboot/x86_64-sb/ipxe.efi":    fx.ipxe,
		"ipxeboot/x86_64-sb/snponly.efi": fx.snponly,
		"ipxeboot/arm64-sb/ipxe.efi":     []byte("arm"),
		"ipxeboot/x86_64/ipxe.efi":       []byte("unsigned"),
	})
	shimRPM := rpmtest.Package("zstd",
		rpmtest.Member{Name: "./usr/lib/efi/shim/9.9-1/EFI/fedora/shimx64.efi", Mode: 0o100700, Data: fx.fedoraShim},
		rpmtest.Member{Name: "./usr/lib/efi/shim/9.9-1/EFI/fedora/mmx64.efi", Mode: 0o100700, Data: []byte("mok")})
	grubRPM := rpmtest.Package("zstd",
		rpmtest.Member{Name: "./usr/lib/efi/grub2/1:9.9-1.fc99/EFI/fedora/grubx64.efi", Mode: 0o100700, Data: fx.fedoraGrub})

	flatcarDigests := fmt.Sprintf("# SHA512 HASH\n%x  flatcar_production_image.shim\n", sha512.Sum512(fx.flatcarShim))
	routes := map[string][]byte{
		"/ipxe/shim/releases/download/ipxe-test/ipxe-shimx64.efi":                             fx.shim,
		"/repos/ipxe/shim/releases/tags/ipxe-test":                                            []byte(fmt.Sprintf(`{"assets":[{"name":"ipxe-shimx64.efi","digest":"sha256:%s"},{"name":"mmx64.efi","digest":"sha256:abc"}]}`, sha256Of(fx.shim))),
		"/ipxe/ipxe/releases/download/vtest/ipxeboot.tar.gz":                                  tarball,
		"/repos/ipxe/ipxe/releases/tags/vtest":                                                []byte(fmt.Sprintf(`{"assets":[{"name":"ipxeboot.tar.gz","digest":"sha256:%s"}]}`, sha256Of(tarball))),
		"/fedora/development/45/Everything/x86_64/os/Packages/s/shim-x64-9.9-1.x86_64.rpm":    shimRPM,
		"/fedora/updates/99/Everything/x86_64/Packages/g/grub2-efi-x64-9.9-1.fc99.x86_64.rpm": grubRPM,
		"/stable/amd64-usr/4757.2.0/flatcar_production_image.shim":                            fx.flatcarShim,
		"/stable/amd64-usr/4757.2.0/flatcar_production_image.shim.DIGESTS":                    []byte(flatcarDigests),
	}
	for p := range routes {
		fx.hits[p] = &atomic.Int32{}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fx.hits[r.URL.Path].Add(1)
		if strings.HasPrefix(r.URL.Path, "/repos/") && r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("GitHub API call without Accept header: %s", r.URL.Path)
		}
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, fx
}

func setupSecureBoot(t *testing.T, srvURL string) string {
	t.Helper()
	dir := setupVerify(t)
	viper.Set(config.SecureBoot, true)
	viper.Set(config.SecureBootIPXEShim, "ipxe-test")
	viper.Set(config.SecureBootIPXE, "vtest")
	viper.Set(config.FedoraShimVersion, "9.9-1")
	viper.Set(config.FedoraGrubVersion, "9.9-1.fc99")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarURL, srvURL+"/%s/%s-usr/%s")
	origAPI, origDL, origFedora := githubAPIBase, githubDownloadBase, fedoraMirrorBase
	githubAPIBase, githubDownloadBase, fedoraMirrorBase = srvURL, srvURL, srvURL+"/fedora"
	t.Cleanup(func() { githubAPIBase, githubDownloadBase, fedoraMirrorBase = origAPI, origDL, origFedora })
	return dir
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return data
}

func TestSecureBootVersionCheckInstallsBundleAndFlatcarCA(t *testing.T) {
	srv, fx := fakeSecureBootUpstream(t)
	dir := setupSecureBoot(t, srv.URL)
	state.SetCurrentFlatcarVersion("4757.2.0")
	if err := os.MkdirAll(filepath.Join(dir, "secureboot", "old-bundle"), 0o755); err != nil {
		t.Fatal(err)
	}

	SecureBootVersionCheck()

	bundle := SecureBootBundleVersion()
	if bundle != "ipxe-test_vtest_shim-9.9-1_grub-9.9-1.fc99" {
		t.Fatalf("bundle = %q", bundle)
	}
	cur := filepath.Join(dir, "secureboot", "current")
	for name, want := range map[string][]byte{
		"ipxe-shimx64.efi":    fx.shim,
		"snponly-shimx64.efi": fx.shim,
		"ipxe.efi":            fx.ipxe,
		"snponly.efi":         fx.snponly,
		"fedora/shimx64.efi":  fx.fedoraShim,
		"fedora/grubx64.efi":  fx.fedoraGrub,
	} {
		if got := readFile(t, filepath.Join(cur, name)); !bytes.Equal(got, want) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if link, err := os.Readlink(cur); err != nil || link != bundle {
		t.Errorf("current -> %q (%v)", link, err)
	}
	if link, err := os.Readlink(filepath.Join(cur, "snponly-shimx64.efi")); err != nil || link != "ipxe-shimx64.efi" {
		t.Errorf("snponly-shimx64.efi -> %q (%v)", link, err)
	}
	for _, leftover := range []string{"ipxeboot.tar.gz", "shim-x64-9.9-1.x86_64.rpm", "grub2-efi-x64-9.9-1.fc99.x86_64.rpm"} {
		if _, err := os.Stat(filepath.Join(cur, leftover)); err == nil {
			t.Errorf("%s left behind in the bundle", leftover)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "secureboot", "old-bundle")); !os.IsNotExist(err) {
		t.Error("old bundle not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "secureboot", "flatcar_production_image.shim")); !os.IsNotExist(err) {
		t.Error("downloaded Flatcar shim left behind")
	}

	m, ok := CurrentSecureBootManifest()
	if !ok || m.BundleVersion != bundle || m.IPXEShimVersion != "ipxe-test" || m.FedoraGrubVersion != "9.9-1.fc99" {
		t.Fatalf("manifest = %+v ok=%v", m, ok)
	}
	sums := map[string]string{}
	sources := map[string]string{}
	for _, f := range m.Files {
		sums[f.Name] = f.Sha256
		sources[f.Name] = f.Source
	}
	if sums["ipxe.efi"] != sha256Of(fx.ipxe) || sums["fedora/grubx64.efi"] != sha256Of(fx.fedoraGrub) || sums["snponly-shimx64.efi"] != sha256Of(fx.shim) {
		t.Errorf("manifest digests wrong: %v", sums)
	}
	if !strings.HasSuffix(sources["ipxe.efi"], "ipxeboot.tar.gz#ipxeboot/x86_64-sb/ipxe.efi") {
		t.Errorf("ipxe.efi source = %q", sources["ipxe.efi"])
	}
	if !strings.Contains(sources["fedora/shimx64.efi"], "/development/45/") || !strings.HasSuffix(sources["fedora/shimx64.efi"], "#usr/lib/efi/shim/9.9-1/EFI/fedora/shimx64.efi") {
		t.Errorf("fedora shim source = %q", sources["fedora/shimx64.efi"])
	}
	if !strings.Contains(sources["fedora/grubx64.efi"], "/updates/99/") {
		t.Errorf("fedora grub source = %q", sources["fedora/grubx64.efi"])
	}
	if missing := MissingSecureBootArtifacts(dir); len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	if !SecureBootReady() {
		t.Error("SecureBootReady = false after a full sync")
	}

	ca, ok := CurrentFlatcarCA()
	if !ok || ca.FlatcarVersion != "4757.2.0" || ca.Sha256 != "ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2" {
		t.Fatalf("flatcar CA = %+v ok=%v", ca, ok)
	}
	if !strings.Contains(ca.Subject, "Flatcar Container Linux Secure Boot Development CA") || ca.NotAfter != "2037-01-19" {
		t.Errorf("flatcar CA metadata = %+v", ca)
	}
	der := readFile(t, filepath.Join(dir, "secureboot", "flatcar-ca.der"))
	if sha256Of(der) != ca.Sha256 {
		t.Error("flatcar-ca.der does not match the sidecar digest")
	}
	if pemBytes := readFile(t, filepath.Join(dir, "secureboot", "flatcar-ca.pem")); !bytes.HasPrefix(pemBytes, []byte("-----BEGIN CERTIFICATE-----")) {
		t.Errorf("flatcar-ca.pem = %q", pemBytes[:40])
	}

	before := map[string]int32{}
	for p, h := range fx.hits {
		before[p] = h.Load()
	}
	SecureBootVersionCheck()
	for p, h := range fx.hits {
		if h.Load() != before[p] {
			t.Errorf("second check re-fetched %s", p)
		}
	}
}

func TestSecureBootVersionCheckOffDoesNothing(t *testing.T) {
	srv, fx := fakeSecureBootUpstream(t)
	dir := setupSecureBoot(t, srv.URL)
	viper.Set(config.SecureBoot, false)
	state.SetCurrentFlatcarVersion("4757.2.0")

	SecureBootVersionCheck()

	if _, err := os.Stat(filepath.Join(dir, "secureboot")); !os.IsNotExist(err) {
		t.Error("secureboot directory created while --secureBoot is off")
	}
	for p, h := range fx.hits {
		if h.Load() != 0 {
			t.Errorf("fetched %s while off", p)
		}
	}
	if SecureBootReady() {
		t.Error("ready while off")
	}
}

func TestSecureBootBundleFailureLeavesNoCurrent(t *testing.T) {
	srv, _ := fakeSecureBootUpstream(t)
	dir := setupSecureBoot(t, srv.URL)
	viper.Set(config.FedoraGrubVersion, "0.0-0.fc1")

	SecureBootVersionCheck()

	if _, err := os.Stat(filepath.Join(dir, "secureboot", "current")); !os.IsNotExist(err) {
		t.Error("current symlink created although the grub package was missing")
	}
	if SecureBootReady() {
		t.Error("ready after a failed sync")
	}
	if _, ok := CurrentFlatcarCA(); ok {
		t.Error("Flatcar CA extracted without a Flatcar release")
	}
}

func TestSecureBootDigestMismatchIsRejected(t *testing.T) {
	srv, fx := fakeSecureBootUpstream(t)
	dir := setupSecureBoot(t, srv.URL)
	fx.shim[0] ^= 0xff

	SecureBootVersionCheck()

	if _, err := os.Stat(filepath.Join(dir, "secureboot", "current")); !os.IsNotExist(err) {
		t.Error("bundle installed despite a shim digest mismatch")
	}
	if _, err := os.Stat(filepath.Join(dir, "secureboot", SecureBootBundleVersion(), "ipxe-shimx64.efi")); err == nil {
		t.Error("mismatching shim kept on disk")
	}
}

func TestSecureBootFlatcarCARotationWarnsAndRewrites(t *testing.T) {
	srv, _ := fakeSecureBootUpstream(t)
	dir := setupSecureBoot(t, srv.URL)
	state.SetCurrentFlatcarVersion("4757.2.0")
	stale := `{"flatcarVersion":"4593.2.5","sha256":"0000","subject":"old","source":"x"}` + "\n"
	if err := os.MkdirAll(filepath.Join(dir, "secureboot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secureboot", "flatcar-ca.json"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	SecureBootVersionCheck()

	ca, ok := CurrentFlatcarCA()
	if !ok || ca.FlatcarVersion != "4757.2.0" || ca.Sha256 == "0000" {
		t.Fatalf("flatcar CA not refreshed: %+v ok=%v", ca, ok)
	}
}

func TestFedoraRPMURLsAndReleaseOf(t *testing.T) {
	if got := fedoraReleaseOf("2.12-64.fc44"); got != "44" {
		t.Errorf("fedoraReleaseOf = %q", got)
	}
	for _, v := range []string{"16.1-7", "1.0.fc", "1.0.fcx"} {
		if got := fedoraReleaseOf(v); got != "" {
			t.Errorf("fedoraReleaseOf(%q) = %q", v, got)
		}
	}
	urls := fedoraRPMURLs("grub2-efi-x64-2.12-64.fc44.x86_64.rpm", "44")
	if len(urls) != 6 || !strings.HasSuffix(urls[0], "/updates/44/Everything/x86_64/Packages/g/grub2-efi-x64-2.12-64.fc44.x86_64.rpm") || !strings.Contains(urls[3], "/updates/45/") {
		t.Errorf("urls = %v", urls)
	}
	urls = fedoraRPMURLs("shim-x64-16.1-7.x86_64.rpm", "")
	if len(urls) != 6 || !strings.Contains(urls[0], "/updates/45/Everything/x86_64/Packages/s/") || !strings.Contains(urls[2], "/development/45/Everything/x86_64/os/Packages/s/") {
		t.Errorf("urls = %v", urls)
	}
}

func TestValidateSecureBootFlags(t *testing.T) {
	setupVerify(t)
	if err := ValidateSecureBootFlags(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	viper.Set(config.SecureBootTrusted, "flatcar, Microsoft")
	if err := ValidateSecureBootFlags(); err != nil {
		t.Fatalf("flatcar rejected: %v", err)
	}
	for key, bad := range map[string]string{
		config.SecureBootIPXEShim: "../etc",
		config.SecureBootIPXE:     "v2.0.0/x",
		config.FedoraShimVersion:  "",
		config.FedoraGrubVersion:  "2.12 64",
		config.SecureBootTrusted:  "redhat",
	} {
		setupVerify(t)
		viper.Set(key, bad)
		if err := ValidateSecureBootFlags(); err == nil {
			t.Errorf("--%s=%q accepted", key, bad)
		}
	}
}
