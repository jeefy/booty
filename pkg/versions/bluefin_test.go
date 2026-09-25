package versions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

// Trimmed from https://api.github.com/repos/projectbluefin/server/releases
// as of installer-v26.08.0: two DDIs in the newest release, a bluefin-server-v*
// tag family, and older installer releases without PXE assets.
const releasesFixture = `[
 {"tag_name":"installer-v26.08.0","draft":false,"prerelease":false,"assets":[
   {"name":"bluefin-server-26.08.0.efi"},{"name":"bluefin-server-4593.2.5.efi"},
   {"name":"bluefin-server-ddi-26.08.0.raw.zst"},{"name":"bluefin-server-ddi-4593.2.5.raw.zst"},
   {"name":"bluefin-server-installer-26.08.0.raw.zst"},
   {"name":"bluefin-server-pxe-initrd-26.08.0.cpio.gz"},{"name":"bluefin-server-pxe-vmlinuz-26.08.0"},
   {"name":"k0s-1.36.4-k0s.0.raw.zst"},{"name":"SHA256SUMS"},{"name":"SHA256SUMS.gpg"},{"name":"zfs.raw"}]},
 {"tag_name":"installer-v25.08.15","draft":false,"prerelease":false,"assets":[
   {"name":"bluefin-server-25.08.15.efi"},{"name":"bluefin-server-ddi-25.08.15.raw.zst"},
   {"name":"bluefin-server-installer-25.08.15.raw.zst"},{"name":"SHA256SUMS"},{"name":"SHA256SUMS.gpg"}]},
 {"tag_name":"bluefin-server-v25.08.14","draft":false,"prerelease":false,"assets":[
   {"name":"bluefin-server-pxe-initrd-25.08.14.cpio.gz"},{"name":"bluefin-server-pxe-vmlinuz-25.08.14"},{"name":"SHA256SUMS"}]},
 {"tag_name":"installer-v25.08.13","draft":false,"prerelease":false,"assets":[
   {"name":"bluefin-server-ddi-25.08.13.raw.zst"},{"name":"SHA256SUMS"}]}
]`

const sumsTwoDDI = `d4ff742d69dd11458672c06baf53500ac0d8a0de0433166c910da86db3dd1b87 *bluefin-server-4593.2.5.efi
3498541fce7e25c1bc8eb73bf6ce624af60e0a99d1f0637068284f6b7c8cd8a3 *bluefin-server-ddi-4593.2.5.raw.zst
b320cddd2ac01873a494c9365e4762486f4be0ac4b54122286249523db425ff6 *bluefin-server-installer-26.08.0.raw.zst
fd4c65dd7fdc0ee637fd9b5afe6ee10e133d81f37a986ef133d962e30ce4feec *bluefin-server-pxe-initrd-26.08.0.cpio.gz
fe0b7ef1f9f98acc00e1db22937cdc57456df860a372fdc8eb806298f767b676 *bluefin-server-pxe-vmlinuz-26.08.0
19b49f2f967c0577360f405512d6b90be9278eac48d93f33975135154cc4e780 *k0s-1.36.4-k0s.0.raw.zst
1d486e65ee7aed6ad2dde81d9d265e7a21662a6845be2af0aeec842dcf77f3b7 *zfs.raw
`

const sumsOneDDI = `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  bluefin-server-pxe-vmlinuz-27.01.0
bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  bluefin-server-pxe-initrd-27.01.0.cpio.gz
cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc  bluefin-server-ddi-27.01.0.raw.zst
`

func TestSelectBluefinRelease(t *testing.T) {
	releases, err := parseBluefinReleases([]byte(releasesFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 4 || releases[0].Version != "26.08.0" || len(releases[0].Assets) != 11 {
		t.Fatalf("parse: %+v", releases)
	}
	rel, err := selectBluefinRelease(releases)
	if err != nil || rel.Tag != "installer-v26.08.0" || rel.Version != "26.08.0" {
		t.Fatalf("select: %+v err=%v", rel, err)
	}

	extra := append([]bluefinRelease{
		{Tag: "installer-v27.00.0", Version: "27.00.0", Assets: []string{"SHA256SUMS", "bluefin-server-ddi-27.00.0.raw.zst"}},
		{Tag: "installer-v27.01.0", Version: "27.01.0", Prerelease: true, Assets: releases[0].Assets},
		{Tag: "installer-v27.02.0", Version: "27.02.0", Draft: true, Assets: releases[0].Assets},
		{Tag: "installer-v9.9.9", Version: "9.9.9", Assets: releases[0].Assets},
	}, releases...)
	rel, err = selectBluefinRelease(extra)
	if err != nil || rel.Version != "26.08.0" {
		t.Fatalf("incomplete, prerelease and draft releases must lose to the older complete one: %+v err=%v", rel, err)
	}

	if _, err := selectBluefinRelease(releases[1:]); err == nil {
		t.Fatal("no complete installer release must be an error")
	}
	if _, err := parseBluefinReleases([]byte(`{"message":"rate limited"}`)); err == nil {
		t.Fatal("non-array body must be an error")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"26.08.0", "25.08.15", 1}, {"25.08.15", "26.08.0", -1}, {"26.08.0", "26.08.0", 0},
		{"26.08.0", "26.8.0", 0}, {"26.08.1", "26.08", 1}, {"9.9.9", "26.08.0", -1}, {"26.08.0-rc1", "26.08.0", 1},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compare(%s, %s)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSelectBluefinArtifacts(t *testing.T) {
	t.Run("two DDIs: the one in SHA256SUMS matching the Flatcar base is taken (last listed)", func(t *testing.T) {
		sums, err := parseSHA256SUMS(strings.NewReader(sumsTwoDDI))
		if err != nil {
			t.Fatal(err)
		}
		if len(sums.order) != 7 || sums.hashes["zfs.raw"] != "1d486e65ee7aed6ad2dde81d9d265e7a21662a6845be2af0aeec842dcf77f3b7" {
			t.Fatalf("parse: %+v", sums)
		}
		m, err := selectBluefinArtifacts("26.08.0", sums)
		if err != nil {
			t.Fatal(err)
		}
		want := BluefinManifest{
			Version:   "26.08.0",
			Vmlinuz:   "bluefin-server-pxe-vmlinuz-26.08.0",
			Initrd:    "bluefin-server-pxe-initrd-26.08.0.cpio.gz",
			DDI:       "bluefin-server-ddi-4593.2.5.raw.zst",
			DDISha256: "3498541fce7e25c1bc8eb73bf6ce624af60e0a99d1f0637068284f6b7c8cd8a3",
		}
		if m != want {
			t.Fatalf("got %+v\nwant %+v", m, want)
		}
	})

	t.Run("several DDIs in SHA256SUMS: the one matching the version wins", func(t *testing.T) {
		sums, err := parseSHA256SUMS(strings.NewReader(sumsTwoDDI + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd *bluefin-server-ddi-26.08.0.raw.zst\n"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := selectBluefinArtifacts("26.08.0", sums)
		if err != nil || m.DDI != "bluefin-server-ddi-26.08.0.raw.zst" || m.DDISha256 != strings.Repeat("d", 64) {
			t.Fatalf("got %+v err=%v", m, err)
		}
	})

	t.Run("one DDI, two-space separator", func(t *testing.T) {
		sums, err := parseSHA256SUMS(strings.NewReader(sumsOneDDI))
		if err != nil {
			t.Fatal(err)
		}
		m, err := selectBluefinArtifacts("27.01.0", sums)
		if err != nil || m.DDI != "bluefin-server-ddi-27.01.0.raw.zst" || m.DDISha256 != strings.Repeat("c", 64) || m.Vmlinuz != "bluefin-server-pxe-vmlinuz-27.01.0" {
			t.Fatalf("got %+v err=%v", m, err)
		}
	})

	t.Run("missing files are errors", func(t *testing.T) {
		sums, _ := parseSHA256SUMS(strings.NewReader(sumsOneDDI))
		if _, err := selectBluefinArtifacts("26.08.0", sums); err == nil || !strings.Contains(err.Error(), "vmlinuz") {
			t.Fatalf("kernel of another version must be missing: %v", err)
		}
		noDDI, _ := parseSHA256SUMS(strings.NewReader(strings.Join(strings.Split(sumsOneDDI, "\n")[:2], "\n") + "\n"))
		if _, err := selectBluefinArtifacts("27.01.0", noDDI); err == nil || !strings.Contains(err.Error(), "ddi") {
			t.Fatalf("no DDI must be an error: %v", err)
		}
	})

	t.Run("malformed SHA256SUMS", func(t *testing.T) {
		if _, err := parseSHA256SUMS(strings.NewReader("")); err == nil {
			t.Error("empty must fail")
		}
		if _, err := parseSHA256SUMS(strings.NewReader("abc  file\n")); err == nil {
			t.Error("short hash must fail")
		}
	})
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// fakeGitHub serves the releases API and release assets for one release.
func fakeGitHub(t *testing.T, version string, files map[string]string, releases string) (*httptest.Server, map[string]int) {
	t.Helper()
	hits := map[string]int{}
	var sums strings.Builder
	for _, name := range []string{"bluefin-server-pxe-vmlinuz-" + version, "bluefin-server-pxe-initrd-" + version + ".cpio.gz", "bluefin-server-ddi-legacy.raw.zst", "bluefin-server-ddi-" + version + ".raw.zst"} {
		if body, ok := files[name]; ok {
			fmt.Fprintf(&sums, "%s *%s\n", sha(body), name)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch {
		case r.URL.Path == "/repos/acme/server/releases":
			if r.URL.Query().Get("per_page") != "20" || r.Header.Get("Accept") != "application/vnd.github+json" {
				t.Errorf("bad API request: %s %v", r.URL, r.Header)
			}
			if want := viper.GetString(config.GithubToken); want != "" && r.Header.Get("Authorization") != "Bearer "+want {
				t.Errorf("token not sent: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(releases))
		case strings.HasPrefix(r.URL.Path, "/acme/server/releases/download/installer-v"+version+"/"):
			name := strings.TrimPrefix(r.URL.Path, "/acme/server/releases/download/installer-v"+version+"/")
			if name == "SHA256SUMS" {
				_, _ = w.Write([]byte(sums.String()))
				return
			}
			body, ok := files[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func setupBluefin(t *testing.T, srvURL string) string {
	t.Helper()
	dir := setupVerify(t)
	viper.Set(config.BluefinRepo, "acme/server")
	origAPI, origDL := githubAPIBase, githubDownloadBase
	githubAPIBase, githubDownloadBase = srvURL, srvURL
	state.SetCurrentBluefinVersion("")
	state.SetRemoteBluefinVersion("")
	t.Cleanup(func() {
		githubAPIBase, githubDownloadBase = origAPI, origDL
		state.SetCurrentBluefinVersion("")
		state.SetRemoteBluefinVersion("")
	})
	return dir
}

func TestBluefinVersionCheckInstallsRelease(t *testing.T) {
	const ver = "26.08.0"
	files := map[string]string{
		"bluefin-server-pxe-vmlinuz-" + ver:             "KERNEL",
		"bluefin-server-pxe-initrd-" + ver + ".cpio.gz": "INITRD",
		"bluefin-server-ddi-legacy.raw.zst":             "LEGACY",
		"bluefin-server-ddi-" + ver + ".raw.zst":        "DDI",
	}
	releases := `[{"tag_name":"installer-v26.08.0","assets":[{"name":"bluefin-server-pxe-vmlinuz-26.08.0"},{"name":"bluefin-server-pxe-initrd-26.08.0.cpio.gz"},{"name":"SHA256SUMS"}]}]`
	srv, hits := fakeGitHub(t, ver, files, releases)
	dir := setupBluefin(t, srv.URL)
	viper.Set(config.GithubToken, "ghp_test")
	touch(t, filepath.Join(dir, "bluefin", "1.0.0", "manifest.json"))

	BluefinVersionCheck()

	if got := state.CurrentBluefinVersion(); got != ver {
		t.Fatalf("current=%q want %s", got, ver)
	}
	if got := state.RemoteBluefinVersion(); got != ver {
		t.Fatalf("remote=%q", got)
	}
	rel := filepath.Join(dir, "bluefin", ver)
	for name, body := range files {
		data, err := os.ReadFile(filepath.Join(rel, name))
		if name == "bluefin-server-ddi-legacy.raw.zst" {
			if err == nil {
				t.Fatal("the non-selected DDI must not be downloaded")
			}
			continue
		}
		if err != nil || string(data) != body {
			t.Fatalf("%s: %v %q", name, err, data)
		}
	}
	m, err := LoadBluefinManifest(rel)
	if err != nil || m.DDI != "bluefin-server-ddi-26.08.0.raw.zst" || m.DDISha256 != sha("DDI") || m.Version != ver {
		t.Fatalf("manifest: %+v err=%v", m, err)
	}
	if _, err := os.Stat(filepath.Join(rel, "SHA256SUMS")); err != nil {
		t.Fatal("SHA256SUMS must be kept:", err)
	}
	link, err := os.Readlink(filepath.Join(dir, "bluefin", "current"))
	if err != nil || link != ver {
		t.Fatalf("current -> %q err=%v", link, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bluefin", "1.0.0")); !os.IsNotExist(err) {
		t.Fatal("old release must be pruned")
	}
	cur, ok := CurrentBluefinManifest()
	if !ok || cur != m {
		t.Fatalf("CurrentBluefinManifest=%+v ok=%v", cur, ok)
	}
	if got := state.LoadLocalBluefinVersion(); got != ver {
		t.Fatalf("LoadLocalBluefinVersion=%q", got)
	}

	BluefinVersionCheck()
	if hits["/acme/server/releases/download/installer-v26.08.0/bluefin-server-ddi-26.08.0.raw.zst"] != 1 {
		t.Fatalf("second run must not download again; DDI fetched %d times", hits["/acme/server/releases/download/installer-v26.08.0/bluefin-server-ddi-26.08.0.raw.zst"])
	}
	if hits["/repos/acme/server/releases"] != 2 {
		t.Fatalf("API hit %d times", hits["/repos/acme/server/releases"])
	}
}

func TestBluefinVersionCheckPinSkipsAPIAndReusesVerifiedFiles(t *testing.T) {
	const ver = "27.01.0"
	files := map[string]string{
		"bluefin-server-pxe-vmlinuz-" + ver:             "K2",
		"bluefin-server-pxe-initrd-" + ver + ".cpio.gz": "I2",
		"bluefin-server-ddi-" + ver + ".raw.zst":        "D2",
	}
	srv, hits := fakeGitHub(t, ver, files, `[]`)
	dir := setupBluefin(t, srv.URL)
	viper.Set(config.BluefinVersion, ver)
	state.Init()
	if state.BluefinPin() != ver {
		t.Fatalf("pin=%q", state.BluefinPin())
	}
	rel := filepath.Join(dir, "bluefin", ver)
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "bluefin-server-pxe-vmlinuz-"+ver), []byte("K2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "bluefin-server-ddi-"+ver+".raw.zst"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	BluefinVersionCheck()

	if got := state.CurrentBluefinVersion(); got != ver {
		t.Fatalf("current=%q", got)
	}
	if hits["/repos/acme/server/releases"] != 0 {
		t.Fatal("a pinned version must not consult the releases API")
	}
	if hits["/acme/server/releases/download/installer-v27.01.0/bluefin-server-pxe-vmlinuz-27.01.0"] != 0 {
		t.Fatal("a verified file must not be downloaded again")
	}
	if hits["/acme/server/releases/download/installer-v27.01.0/bluefin-server-ddi-27.01.0.raw.zst"] != 1 {
		t.Fatal("a corrupt file must be re-downloaded")
	}
	if data, _ := os.ReadFile(filepath.Join(rel, "bluefin-server-ddi-"+ver+".raw.zst")); string(data) != "D2" {
		t.Fatalf("DDI content %q", data)
	}
}

func TestBluefinVersionCheckDoesNotAdvanceOnFailure(t *testing.T) {
	const ver = "26.08.0"
	files := map[string]string{
		"bluefin-server-pxe-vmlinuz-" + ver:             "K",
		"bluefin-server-pxe-initrd-" + ver + ".cpio.gz": "I",
	}
	srv, _ := fakeGitHub(t, ver, files, `[{"tag_name":"installer-v26.08.0","assets":[{"name":"bluefin-server-pxe-vmlinuz-26.08.0"},{"name":"bluefin-server-pxe-initrd-26.08.0.cpio.gz"},{"name":"SHA256SUMS"}]}]`)
	dir := setupBluefin(t, srv.URL)

	BluefinVersionCheck()

	if got := state.CurrentBluefinVersion(); got != "0.0.0" {
		t.Fatalf("no DDI in SHA256SUMS must leave the version unset, got %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "bluefin", "current")); !os.IsNotExist(err) {
		t.Fatal("current must not be linked on failure")
	}
	if _, ok := CurrentBluefinManifest(); ok {
		t.Fatal("no manifest expected")
	}

	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	t.Cleanup(unreachable.Close)
	githubAPIBase, githubDownloadBase = unreachable.URL, unreachable.URL
	state.SetCurrentBluefinVersion("")
	BluefinVersionCheck()
	if got := state.CurrentBluefinVersion(); got != "0.0.0" {
		t.Fatalf("API failure must leave 0.0.0, got %q", got)
	}
}

func TestVerifyLocalArtifactsBluefin(t *testing.T) {
	writeManifest := func(t *testing.T, dir string, m BluefinManifest) string {
		t.Helper()
		rel := filepath.Join(dir, "bluefin", m.Version)
		if err := os.MkdirAll(rel, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeBluefinManifest(rel, m); err != nil {
			t.Fatal(err)
		}
		symlink(t, m.Version, filepath.Join(dir, "bluefin", "current"))
		return rel
	}
	m := BluefinManifest{Version: "26.08.0", Vmlinuz: "k", Initrd: "i", DDI: "d", DDISha256: "x"}

	t.Run("incomplete resets to 0.0.0 and keeps files", func(t *testing.T) {
		dir := setupVerify(t)
		state.SetCurrentBluefinVersion(m.Version)
		t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
		rel := writeManifest(t, dir, m)
		touch(t, filepath.Join(rel, "k"))
		touch(t, filepath.Join(rel, "i"))
		if got := MissingBluefinArtifacts(dir); !reflect.DeepEqual(got, []string{"d"}) {
			t.Fatalf("missing=%v", got)
		}
		VerifyLocalArtifacts()
		if got := state.CurrentBluefinVersion(); got != "0.0.0" {
			t.Fatalf("version %q want 0.0.0", got)
		}
		if _, err := os.Stat(filepath.Join(rel, "k")); err != nil {
			t.Fatal("files must be kept:", err)
		}
	})

	t.Run("complete is untouched", func(t *testing.T) {
		dir := setupVerify(t)
		state.SetCurrentBluefinVersion(m.Version)
		t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
		rel := writeManifest(t, dir, m)
		for _, f := range m.Files() {
			touch(t, filepath.Join(rel, f))
		}
		if got := MissingBluefinArtifacts(dir); got != nil {
			t.Fatalf("missing=%v", got)
		}
		VerifyLocalArtifacts()
		if got := state.CurrentBluefinVersion(); got != m.Version {
			t.Fatalf("version %q changed", got)
		}
	})

	t.Run("no manifest is a no-op", func(t *testing.T) {
		dir := setupVerify(t)
		if got := MissingBluefinArtifacts(dir); got != nil {
			t.Fatalf("missing=%v", got)
		}
		state.SetCurrentBluefinVersion("0.0.0")
		t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
		VerifyLocalArtifacts()
		if got := state.CurrentBluefinVersion(); got != "0.0.0" {
			t.Fatalf("version %q", got)
		}
	})
}
