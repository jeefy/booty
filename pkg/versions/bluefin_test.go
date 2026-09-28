package versions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/klauspost/compress/zstd"
	"github.com/spf13/viper"
)

// Shaped like the projectbluefin/server releases API: v<version> netboot
// releases next to the retired tag families.
const releasesFixture = `[
 {"tag_name":"v20260927.123","draft":false,"prerelease":false,"assets":[
   {"name":"bluefin-server-20260927.123.efi"},{"name":"bluefin-server-netboot_20260927.123.efi"},
   {"name":"bluefin-server-netboot_20260927.123.esp.raw"},{"name":"bluefin-server_20260927.123.raw"},
   {"name":"k0s-1.36.4-k0s.0.raw.zst"},{"name":"kubeadm_20260927.123.raw.zst"},{"name":"kubestellar_20260927.123.raw.zst"},{"name":"zfs_20260927.123.raw.zst"},
   {"name":"SHA256SUMS"},{"name":"SHA256SUMS.gpg"}]},
 {"tag_name":"v20260926.200","draft":false,"prerelease":false,"assets":[
   {"name":"bluefin-server-netboot_20260926.200.efi"},{"name":"bluefin-server_20260926.200.raw"},
   {"name":"SHA256SUMS"},{"name":"SHA256SUMS.gpg"}]},
 {"tag_name":"installer-v26.08.0","draft":false,"prerelease":false,"assets":[
   {"name":"bluefin-server-pxe-vmlinuz-26.08.0"},{"name":"bluefin-server-ddi-26.08.0.raw.zst"},{"name":"SHA256SUMS"}]},
 {"tag_name":"bluefin-server-v99999999.1","draft":false,"prerelease":false,"assets":[
   {"name":"bluefin-server-netboot_99999999.1.efi"},{"name":"bluefin-server_99999999.1.raw"},{"name":"SHA256SUMS"},{"name":"SHA256SUMS.gpg"}]}
]`

const sumsFixture = `1b394172ba8919c891b7970ad5ebf95ee45ed31be9bbfe2a2ced287d4626fae2 *bluefin-server-netboot_20260927.123.efi
09014730651107888e521d82e0d7dae463ed396c1aa0c51128aca7b26ef40449 *bluefin-server-netboot_20260927.123.esp.raw
b46f0b26f93335e81b9d332872a31112bf1d6269d301b079029670453a9ed428 *bluefin-server_20260927.123.raw
57cef665ebb243ab51d647898b785f8080dc507d38dfa23c3f80fc4f88990f9f *bluefin-server-20260927.123.efi
aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa *k0s-1.36.4-k0s.0.raw.zst
eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee *kubeadm_20260927.123.raw.zst
bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb *kubestellar_20260927.123.raw.zst
cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc *zfs_20260927.123.raw.zst
dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd *zfs_20260926.200.raw.zst
`

func TestSelectBluefinRelease(t *testing.T) {
	releases, err := parseBluefinReleases([]byte(releasesFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 4 || releases[0].Version != "20260927.123" || len(releases[0].Assets) != 10 {
		t.Fatalf("parse: %+v", releases)
	}
	rel, err := selectBluefinRelease(releases)
	if err != nil || rel.Tag != "v20260927.123" || rel.Version != "20260927.123" {
		t.Fatalf("select: %+v err=%v", rel, err)
	}

	complete := func(v string) []string {
		return []string{"bluefin-server-netboot_" + v + ".efi", "bluefin-server_" + v + ".raw", "SHA256SUMS", "SHA256SUMS.gpg"}
	}
	cases := []struct {
		name string
		rel  bluefinRelease
	}{
		{"no signature", bluefinRelease{Tag: "v20270101.1", Version: "20270101.1", Assets: complete("20270101.1")[:3]}},
		{"no DDI", bluefinRelease{Tag: "v20270101.2", Version: "20270101.2", Assets: []string{"bluefin-server-netboot_20270101.2.efi", "SHA256SUMS", "SHA256SUMS.gpg"}}},
		{"assets of another version", bluefinRelease{Tag: "v20270101.3", Version: "20270101.3", Assets: complete("20260927.123")}},
		{"prerelease", bluefinRelease{Tag: "v20270101.4", Version: "20270101.4", Prerelease: true, Assets: complete("20270101.4")}},
		{"draft", bluefinRelease{Tag: "v20270101.5", Version: "20270101.5", Draft: true, Assets: complete("20270101.5")}},
		{"installer tag family", bluefinRelease{Tag: "installer-v20270101.6", Version: "20270101.6", Assets: complete("20270101.6")}},
		{"not a version", bluefinRelease{Tag: "vnext", Version: "next", Assets: complete("next")}},
		{"path in version", bluefinRelease{Tag: "v1/../x", Version: "1/../x", Assets: complete("1/../x")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectBluefinRelease(append([]bluefinRelease{tc.rel}, releases...))
			if err != nil || got.Version != "20260927.123" {
				t.Fatalf("%s must lose to the older complete release: %+v err=%v", tc.name, got, err)
			}
		})
	}

	if _, err := selectBluefinRelease(releases[2:]); err == nil {
		t.Fatal("no complete v<version> release must be an error")
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
		{"20260927.123", "20260926.200", 1}, {"20260927.9", "20260927.10", -1}, {"20260927.123", "20260927.123", 0},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compare(%s, %s)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestValidBluefinVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"20260927.123": true, "2026.09.0": true, "0.42": true,
		"": false, "next": false, "1/2": false, "1..2": false, "1 2": false, strings.Repeat("1", 65): false,
	} {
		if got := ValidBluefinVersion(v); got != want {
			t.Errorf("ValidBluefinVersion(%q)=%v want %v", v, got, want)
		}
	}
}

func TestSelectBluefinArtifacts(t *testing.T) {
	sums, err := parseSHA256SUMS(strings.NewReader(sumsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(sums.order) != 9 {
		t.Fatalf("parse: %+v", sums)
	}

	t.Run("netboot UKI, DDI and the four sysexts", func(t *testing.T) {
		m, sysexts, err := selectBluefinArtifacts("20260927.123", sums)
		if err != nil {
			t.Fatal(err)
		}
		if m.NetbootUKI != "bluefin-server-netboot_20260927.123.efi" || m.DDI != "bluefin-server_20260927.123.raw" {
			t.Fatalf("manifest %+v", m)
		}
		wantSums := map[string]string{
			"bluefin-server-netboot_20260927.123.efi": "1b394172ba8919c891b7970ad5ebf95ee45ed31be9bbfe2a2ced287d4626fae2",
			"bluefin-server_20260927.123.raw":         "b46f0b26f93335e81b9d332872a31112bf1d6269d301b079029670453a9ed428",
		}
		if !reflect.DeepEqual(m.SHA256Sums, wantSums) {
			t.Fatalf("sha256sums %v", m.SHA256Sums)
		}
		got := map[string]string{}
		for _, s := range sysexts {
			got[s.name] = s.asset + " " + s.raw() + " " + s.sha256[:1]
		}
		want := map[string]string{
			"k0s":         "k0s-1.36.4-k0s.0.raw.zst k0s-1.36.4-k0s.0.raw a",
			"kubeadm":     "kubeadm_20260927.123.raw.zst kubeadm_20260927.123.raw e",
			"kubestellar": "kubestellar_20260927.123.raw.zst kubestellar_20260927.123.raw b",
			"zfs":         "zfs_20260927.123.raw.zst zfs_20260927.123.raw c",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sysexts %v", got)
		}
	})

	t.Run("two-space separator and no sysexts", func(t *testing.T) {
		two, err := parseSHA256SUMS(strings.NewReader(strings.Repeat("e", 64) + "  bluefin-server-netboot_0.42.efi\n" + strings.Repeat("f", 64) + "  bluefin-server_0.42.raw\n"))
		if err != nil {
			t.Fatal(err)
		}
		m, sysexts, err := selectBluefinArtifacts("0.42", two)
		if err != nil || len(sysexts) != 0 || m.SHA256Sums[m.DDI] != strings.Repeat("f", 64) {
			t.Fatalf("got %+v %v err=%v", m, sysexts, err)
		}
	})

	t.Run("several k0s sysexts are skipped", func(t *testing.T) {
		more, _ := parseSHA256SUMS(strings.NewReader(sumsFixture + strings.Repeat("9", 64) + " *k0s-1.37.0-k0s.0.raw.zst\n"))
		_, sysexts, err := selectBluefinArtifacts("20260927.123", more)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sysexts {
			if s.name == "k0s" {
				t.Fatal("an ambiguous k0s sysext must be skipped")
			}
		}
	})

	t.Run("missing required files are errors", func(t *testing.T) {
		if _, _, err := selectBluefinArtifacts("20260926.200", sums); err == nil || !strings.Contains(err.Error(), "netboot") {
			t.Fatalf("UKI of another version must be missing: %v", err)
		}
		noDDI, _ := parseSHA256SUMS(strings.NewReader(strings.SplitN(sumsFixture, "\n", 2)[0] + "\n"))
		if _, _, err := selectBluefinArtifacts("20260927.123", noDDI); err == nil || !strings.Contains(err.Error(), "bluefin-server_20260927.123.raw") {
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

func zstdBytes(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// fakeBluefinRelease is the file set of one release as the server publishes
// it: every file listed in SHA256SUMS, sysexts zstd-compressed.
type fakeBluefinRelease struct {
	version string
	files   map[string]string
	sums    string
	sig     string
	// unpublished files are listed in SHA256SUMS but answer 404.
	unpublished map[string]bool
}

func newFakeRelease(t *testing.T, version string, sysexts map[string]string) *fakeBluefinRelease {
	t.Helper()
	r := &fakeBluefinRelease{version: version, files: map[string]string{
		"bluefin-server-netboot_" + version + ".efi": "UKI-" + version,
		"bluefin-server_" + version + ".raw":         "DDI-" + version,
		"bluefin-server-" + version + ".efi":         "DISK-UKI-" + version,
	}, unpublished: map[string]bool{}}
	for name, body := range sysexts {
		r.files[name] = zstdBytes(t, body)
	}
	names := make([]string, 0, len(r.files))
	for n := range r.files {
		names = append(names, n)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, n := range names {
		fmt.Fprintf(&sums, "%s *%s\n", sha(r.files[n]), n)
	}
	r.sums = sums.String()
	r.sig = "unsigned"
	return r
}

func (r *fakeBluefinRelease) serve(name string) (string, bool) {
	switch {
	case r.unpublished[name]:
		return "", false
	case name == BluefinSumsFile:
		return r.sums, true
	case name == BluefinSigFile:
		return r.sig, true
	}
	body, ok := r.files[name]
	return body, ok
}

// fakeGitHub serves the releases API and the assets of releases.
func fakeGitHub(t *testing.T, releasesJSON string, releases ...*fakeBluefinRelease) (*httptest.Server, map[string]int) {
	t.Helper()
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if r.URL.Path == "/repos/acme/server/releases" {
			if r.URL.Query().Get("per_page") != "20" || r.Header.Get("Accept") != "application/vnd.github+json" {
				t.Errorf("bad API request: %s %v", r.URL, r.Header)
			}
			if want := viper.GetString(config.GithubToken); want != "" && r.Header.Get("Authorization") != "Bearer "+want {
				t.Errorf("token not sent: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(releasesJSON))
			return
		}
		for _, rel := range releases {
			prefix := "/acme/server/releases/download/v" + rel.version + "/"
			if !strings.HasPrefix(r.URL.Path, prefix) {
				continue
			}
			if body, ok := rel.serve(strings.TrimPrefix(r.URL.Path, prefix)); ok {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func releasesJSON(versions ...string) string {
	var parts []string
	for _, v := range versions {
		parts = append(parts, fmt.Sprintf(`{"tag_name":"v%[1]s","assets":[{"name":"bluefin-server-netboot_%[1]s.efi"},{"name":"bluefin-server_%[1]s.raw"},{"name":"SHA256SUMS"},{"name":"SHA256SUMS.gpg"}]}`, v))
	}
	return "[" + strings.Join(parts, ",") + "]"
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
		viper.Set(config.BluefinVersion, "")
		state.Init()
	})
	return dir
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBluefinVersionCheckInstallsRelease(t *testing.T) {
	const ver = "20260927.123"
	rel := newFakeRelease(t, ver, map[string]string{
		"zfs_" + ver + ".raw.zst":         "ZFS-SYSEXT",
		"kubestellar_" + ver + ".raw.zst": "KS-SYSEXT",
		"kubeadm_" + ver + ".raw.zst":     "KUBEADM-SYSEXT",
		"k0s-1.36.4-k0s.0.raw.zst":        "K0S-SYSEXT",
	})
	srv, hits := fakeGitHub(t, releasesJSON(ver), rel)
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
	relDir := filepath.Join(dir, "bluefin", ver)
	for name, want := range map[string]string{
		"bluefin-server-netboot_" + ver + ".efi": "UKI-" + ver,
		"bluefin-server_" + ver + ".raw":         "DDI-" + ver,
		"SHA256SUMS":                             rel.sums,
		"SHA256SUMS.gpg":                         rel.sig,
		"zfs_" + ver + ".raw":                    "ZFS-SYSEXT",
		"kubestellar_" + ver + ".raw":            "KS-SYSEXT",
		"kubeadm_" + ver + ".raw":                "KUBEADM-SYSEXT",
		"k0s-1.36.4-k0s.0.raw":                   "K0S-SYSEXT",
	} {
		if got := readText(t, filepath.Join(relDir, name)); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	for _, gone := range []string{"zfs_" + ver + ".raw.zst", "bluefin-server-" + ver + ".efi"} {
		if _, err := os.Stat(filepath.Join(relDir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s must not be kept: %v", gone, err)
		}
	}
	m, err := LoadBluefinManifest(relDir)
	if err != nil {
		t.Fatal(err)
	}
	wantSysexts := map[string]BluefinSysext{
		"zfs":         {File: "zfs_" + ver + ".raw", Sha256: sha("ZFS-SYSEXT")},
		"kubestellar": {File: "kubestellar_" + ver + ".raw", Sha256: sha("KS-SYSEXT")},
		"kubeadm":     {File: "kubeadm_" + ver + ".raw", Sha256: sha("KUBEADM-SYSEXT")},
		"k0s":         {File: "k0s-1.36.4-k0s.0.raw", Sha256: sha("K0S-SYSEXT")},
	}
	if !reflect.DeepEqual(m.Sysexts, wantSysexts) {
		t.Fatalf("sysexts %+v", m.Sysexts)
	}
	if m.SHA256Sums["zfs_"+ver+".raw.zst"] != sha(rel.files["zfs_"+ver+".raw.zst"]) || m.SHA256Sums[m.DDI] != sha("DDI-"+ver) {
		t.Fatalf("sha256sums %v", m.SHA256Sums)
	}
	link, err := os.Readlink(filepath.Join(dir, "bluefin", "current"))
	if err != nil || link != ver {
		t.Fatalf("current -> %q err=%v", link, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bluefin", "1.0.0")); !os.IsNotExist(err) {
		t.Fatal("a release that was never current must be pruned")
	}
	cur, ok := CurrentBluefinManifest()
	if !ok || !reflect.DeepEqual(cur, m) {
		t.Fatalf("CurrentBluefinManifest=%+v ok=%v", cur, ok)
	}
	if _, ok := PreviousBluefinManifest(); ok {
		t.Fatal("no previous release yet")
	}
	if got := state.LoadLocalBluefinVersion(); got != ver {
		t.Fatalf("LoadLocalBluefinVersion=%q", got)
	}
	if missing := MissingBluefinArtifacts(dir); missing != nil {
		t.Fatalf("missing %v", missing)
	}

	BluefinVersionCheck()
	if n := hits["/acme/server/releases/download/v"+ver+"/bluefin-server_"+ver+".raw"]; n != 1 {
		t.Fatalf("second run must not download again; DDI fetched %d times", n)
	}
	if hits["/repos/acme/server/releases"] != 2 {
		t.Fatalf("API hit %d times", hits["/repos/acme/server/releases"])
	}
}

func TestBluefinVersionCheckKeepsPreviousRelease(t *testing.T) {
	versions := []string{"20260925.1", "20260926.2", "20260927.3"}
	var rels []*fakeBluefinRelease
	for _, v := range versions {
		rels = append(rels, newFakeRelease(t, v, nil))
	}
	current := ""
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/server/releases" {
			_, _ = w.Write([]byte(releasesJSON(current)))
			return
		}
		for _, rel := range rels {
			if body, ok := rel.serve(strings.TrimPrefix(r.URL.Path, "/acme/server/releases/download/v"+rel.version+"/")); ok && strings.HasPrefix(r.URL.Path, "/acme/server/releases/download/v"+rel.version+"/") {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	dir := setupBluefin(t, srvURL)

	for i, v := range versions {
		current = v
		BluefinVersionCheck()
		if state.CurrentBluefinVersion() != v {
			t.Fatalf("step %d: current %q", i, state.CurrentBluefinVersion())
		}
		prev, ok := PreviousBluefinManifest()
		if i == 0 && ok {
			t.Fatal("first install has no previous release")
		}
		if i > 0 && (!ok || prev.Version != versions[i-1]) {
			t.Fatalf("step %d: previous %+v ok=%v", i, prev, ok)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "bluefin"))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if !reflect.DeepEqual(dirs, versions[1:]) {
		t.Fatalf("release dirs %v, want current + previous %v", dirs, versions[1:])
	}
}

func TestBluefinVersionCheckPinSkipsAPIAndReusesVerifiedFiles(t *testing.T) {
	const ver = "20270101.7"
	rel := newFakeRelease(t, ver, map[string]string{"zfs_" + ver + ".raw.zst": "ZFS"})
	rel.unpublished["zfs_"+ver+".raw.zst"] = true
	srv, hits := fakeGitHub(t, `[]`, rel)
	dir := setupBluefin(t, srv.URL)
	viper.Set(config.BluefinVersion, ver)
	state.Init()
	if state.BluefinPin() != ver {
		t.Fatalf("pin=%q", state.BluefinPin())
	}
	relDir := filepath.Join(dir, "bluefin", ver)
	if err := os.MkdirAll(relDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relDir, "bluefin-server-netboot_"+ver+".efi"), []byte("UKI-"+ver), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relDir, "bluefin-server_"+ver+".raw"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	BluefinVersionCheck()

	if got := state.CurrentBluefinVersion(); got != ver {
		t.Fatalf("current=%q", got)
	}
	if hits["/repos/acme/server/releases"] != 0 {
		t.Fatal("a pinned version must not consult the releases API")
	}
	base := "/acme/server/releases/download/v" + ver + "/"
	if hits[base+"bluefin-server-netboot_"+ver+".efi"] != 0 {
		t.Fatal("a verified file must not be downloaded again")
	}
	if hits[base+"bluefin-server_"+ver+".raw"] != 1 || readText(t, filepath.Join(relDir, "bluefin-server_"+ver+".raw")) != "DDI-"+ver {
		t.Fatal("a corrupt file must be re-downloaded")
	}
	m, _ := CurrentBluefinManifest()
	if len(m.Sysexts) != 0 {
		t.Fatalf("a listed but unpublished sysext is skipped: %+v", m.Sysexts)
	}
}

func TestBluefinVersionCheckDoesNotAdvanceOnFailure(t *testing.T) {
	const ver = "20260927.123"
	cases := []struct {
		name  string
		setup func(r *fakeBluefinRelease)
	}{
		{"DDI missing", func(r *fakeBluefinRelease) { r.unpublished["bluefin-server_"+ver+".raw"] = true }},
		{"DDI corrupt", func(r *fakeBluefinRelease) { r.files["bluefin-server_"+ver+".raw"] = "tampered" }},
		{"sysext corrupt", func(r *fakeBluefinRelease) { r.files["zfs_"+ver+".raw.zst"] = "tampered" }},
		{"sysext not zstd", func(r *fakeBluefinRelease) {
			r.files["zfs_"+ver+".raw.zst"] = "plain"
			r.sums = strings.ReplaceAll(r.sums, sha(zstdBytes(t, "ZFS")), sha("plain"))
		}},
		{"signature missing", func(r *fakeBluefinRelease) { r.unpublished[BluefinSigFile] = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel := newFakeRelease(t, ver, map[string]string{"zfs_" + ver + ".raw.zst": "ZFS"})
			tc.setup(rel)
			srv, _ := fakeGitHub(t, releasesJSON(ver), rel)
			dir := setupBluefin(t, srv.URL)

			BluefinVersionCheck()

			if got := state.CurrentBluefinVersion(); got != "0.0.0" {
				t.Fatalf("version advanced to %q", got)
			}
			if _, err := os.Lstat(filepath.Join(dir, "bluefin", "current")); !os.IsNotExist(err) {
				t.Fatal("current must not be linked on failure")
			}
			if _, ok := CurrentBluefinManifest(); ok {
				t.Fatal("no manifest expected")
			}
		})
	}

	t.Run("API failure", func(t *testing.T) {
		unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
		t.Cleanup(unreachable.Close)
		setupBluefin(t, unreachable.URL)
		BluefinVersionCheck()
		if got := state.CurrentBluefinVersion(); got != "0.0.0" {
			t.Fatalf("API failure must leave 0.0.0, got %q", got)
		}
	})
}

// testKeyring writes the public half of a fresh signing key to dir (binary
// or armored) and returns its path and the entity that signs.
func testKeyring(t *testing.T, dir string, armored bool) (string, *openpgp.Entity) {
	t.Helper()
	e, err := openpgp.NewEntity("Bluefin Test", "", "test@example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if armored {
		w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Serialize(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	} else if err := e.Serialize(&buf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pubring.pgp")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, e
}

func detachSign(t *testing.T, e *openpgp.Entity, data string, armored bool) string {
	t.Helper()
	var sig bytes.Buffer
	var err error
	if armored {
		err = openpgp.ArmoredDetachSign(&sig, e, strings.NewReader(data), nil)
	} else {
		err = openpgp.DetachSign(&sig, e, strings.NewReader(data), nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return sig.String()
}

func TestBluefinKeyringVerifiesSHA256SUMS(t *testing.T) {
	const ver = "20260927.123"
	for _, armored := range []bool{false, true} {
		t.Run(fmt.Sprintf("armored=%v", armored), func(t *testing.T) {
			rel := newFakeRelease(t, ver, nil)
			srv, _ := fakeGitHub(t, releasesJSON(ver), rel)
			setupBluefin(t, srv.URL)
			keyring, signer := testKeyring(t, t.TempDir(), armored)
			viper.Set(config.BluefinKeyring, keyring)
			if err := ValidateBluefinFlags(); err != nil {
				t.Fatal(err)
			}

			rel.sig = detachSign(t, signer, rel.sums+"\n", armored)
			BluefinVersionCheck()
			if got := state.CurrentBluefinVersion(); got != "0.0.0" {
				t.Fatalf("a signature over other bytes must fail closed, version %q", got)
			}

			_, stranger := testKeyring(t, t.TempDir(), armored)
			rel.sig = detachSign(t, stranger, rel.sums, armored)
			BluefinVersionCheck()
			if got := state.CurrentBluefinVersion(); got != "0.0.0" {
				t.Fatalf("a signature by an unknown key must fail closed, version %q", got)
			}

			rel.sig = detachSign(t, signer, rel.sums, armored)
			BluefinVersionCheck()
			if got := state.CurrentBluefinVersion(); got != ver {
				t.Fatalf("a good signature must install, version %q", got)
			}
			if got := readText(t, config.DataPath("bluefin", ver, BluefinSigFile)); got != rel.sig {
				t.Fatal("the signature must be stored verbatim for the node")
			}
		})
	}
}

func TestValidateBluefinFlags(t *testing.T) {
	setupVerify(t)
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte("not a keyring"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		key   string
		value string
		ok    bool
	}{
		{"no flags", "", "", true},
		{"pin", config.BluefinVersion, "20260927.123", true},
		{"bad pin", config.BluefinVersion, "../x", false},
		{"missing keyring", config.BluefinKeyring, filepath.Join(dir, "absent"), false},
		{"junk keyring", config.BluefinKeyring, junk, false},
		{"oci repository", config.BluefinOCI, "ghcr.io/projectbluefin/bluefin-server", true},
		{"oci plain http", config.BluefinOCI, "http://registry.example:30500/bluefin-server", true},
		{"oci with tag", config.BluefinOCI, "ghcr.io/projectbluefin/bluefin-server:latest", false},
		{"oci garbage", config.BluefinOCI, "not a ref", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{config.BluefinVersion, config.BluefinKeyring, config.BluefinOCI} {
				viper.Set(k, "")
			}
			if tc.key != "" {
				viper.Set(tc.key, tc.value)
			}
			if err := ValidateBluefinFlags(); (err == nil) != tc.ok {
				t.Fatalf("err=%v ok=%v", err, tc.ok)
			}
		})
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
	const ver = "20260927.123"
	m := BluefinManifest{Version: ver, NetbootUKI: BluefinNetbootUKI(ver), DDI: BluefinDDI(ver), Sysexts: map[string]BluefinSysext{"zfs": {File: "zfs_" + ver + ".raw", Sha256: "x"}}}

	t.Run("incomplete resets to 0.0.0 and keeps files", func(t *testing.T) {
		dir := setupVerify(t)
		state.SetCurrentBluefinVersion(ver)
		t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
		rel := writeManifest(t, dir, m)
		for _, f := range []string{m.NetbootUKI, m.DDI, BluefinSumsFile, BluefinSigFile} {
			touch(t, filepath.Join(rel, f))
		}
		if got := MissingBluefinArtifacts(dir); !reflect.DeepEqual(got, []string{"zfs_" + ver + ".raw"}) {
			t.Fatalf("missing=%v", got)
		}
		VerifyLocalArtifacts()
		if got := state.CurrentBluefinVersion(); got != "0.0.0" {
			t.Fatalf("version %q want 0.0.0", got)
		}
		if _, err := os.Stat(filepath.Join(rel, m.DDI)); err != nil {
			t.Fatal("files must be kept:", err)
		}
	})

	t.Run("complete is untouched", func(t *testing.T) {
		dir := setupVerify(t)
		state.SetCurrentBluefinVersion(ver)
		t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
		rel := writeManifest(t, dir, m)
		for _, f := range m.Files() {
			touch(t, filepath.Join(rel, f))
		}
		if got := MissingBluefinArtifacts(dir); got != nil {
			t.Fatalf("missing=%v", got)
		}
		VerifyLocalArtifacts()
		if got := state.CurrentBluefinVersion(); got != ver {
			t.Fatalf("version %q changed", got)
		}
	})

	t.Run("retired installer manifest forces a fresh download", func(t *testing.T) {
		dir := setupVerify(t)
		rel := filepath.Join(dir, "bluefin", "26.08.0")
		if err := os.MkdirAll(rel, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rel, "manifest.json"), []byte(`{"version":"26.08.0","vmlinuz":"k","initrd":"i","ddi":"d","ddiSha256":"x"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		symlink(t, "26.08.0", filepath.Join(dir, "bluefin", "current"))
		state.SetCurrentBluefinVersion("26.08.0")
		t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
		if got := MissingBluefinArtifacts(dir); !reflect.DeepEqual(got, []string{"manifest.json"}) {
			t.Fatalf("missing=%v", got)
		}
		VerifyLocalArtifacts()
		if got := state.CurrentBluefinVersion(); got != "0.0.0" {
			t.Fatalf("version %q want 0.0.0", got)
		}
		if _, ok := CurrentBluefinManifest(); ok {
			t.Fatal("an installer-v* manifest is not a netboot release")
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
