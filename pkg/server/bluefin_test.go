package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v3_6 "github.com/coreos/ignition/v2/config/v3_6"
	ign36 "github.com/coreos/ignition/v2/config/v3_6/types"
	"github.com/jeefy/booty/pkg/config"
	ign "github.com/jeefy/booty/pkg/ignition"
	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/versions"
)

const bluefinMAC = "aa:bb:cc:dd:ee:b1"

const bluefinTestVersion = "20260927.123"

// installBluefinFixture lays out a served release the way
// versions.BluefinVersionCheck leaves it.
func installBluefinFixture(t *testing.T, dir string) {
	t.Helper()
	writeBluefinRelease(t, dir, bluefinTestVersion, "current")
	state.SetCurrentBluefinVersion(bluefinTestVersion)
	t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
}

func writeBluefinRelease(t *testing.T, dir, version, link string) {
	t.Helper()
	rel := filepath.Join(dir, "bluefin", version)
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"bluefin-server-netboot_" + version + ".efi": "UKI-" + version,
		"bluefin-server_" + version + ".raw":         "DDI-" + version,
		"SHA256SUMS":                                 "SUMS-" + version,
		"SHA256SUMS.gpg":                             "SIG-" + version,
		"zfs_" + version + ".raw":                    "ZFS-" + version,
		"kubestellar_" + version + ".raw":            "KS-" + version,
		"k0s-1.36.4-k0s.0.raw":                       "K0S",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(rel, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := versions.BluefinManifest{
		Version:    version,
		NetbootUKI: "bluefin-server-netboot_" + version + ".efi",
		DDI:        "bluefin-server_" + version + ".raw",
		Sysexts: map[string]versions.BluefinSysext{
			"zfs":         {File: "zfs_" + version + ".raw", Sha256: sha256Hex("ZFS-" + version)},
			"kubestellar": {File: "kubestellar_" + version + ".raw", Sha256: sha256Hex("KS-" + version)},
			"k0s":         {File: "k0s-1.36.4-k0s.0.raw", Sha256: sha256Hex("K0S")},
		},
		SHA256Sums: map[string]string{},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "bluefin", link)
	_ = os.Remove(linkPath)
	if err := os.Symlink(version, linkPath); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestBluefinIPXEMenu(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true,"installDisk":"/dev/vda"}`)

	for _, q := range []string{"", "&sb=1"} {
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC+q, "")
		if r.status != 200 || !strings.Contains(r.body, "switch to UEFI HTTP Boot - srv1") || !strings.Contains(r.body, "echo   http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-b1/bluefin-server-netboot.efi\n") {
			t.Fatalf("%q: %+v", q, r)
		}
		if strings.Contains(r.body, "\nkernel ") || strings.Contains(r.body, "inst.") || strings.Contains(r.body, "[[") {
			t.Fatalf("%q: iPXE never boots Bluefin: %s", q, r.body)
		}
	}
	if h, _ := hardware.Get(bluefinMAC); !h.DoInstall || h.InstallServedAt != "" {
		t.Fatalf("the iPXE menu leaves doInstall alone: %+v", h)
	}
	if r := do(t, http.MethodGet, srv.URL+"/creds/"+bluefinMAC+".tar", ""); r.status != http.StatusNotFound {
		t.Fatalf("the installer credentials bundle is gone: %+v", r)
	}
}

func TestUpdateCheckBluefin(t *testing.T) {
	srv, dir := newTestServer(t)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c1","hostname":"fc","os":"flatcar"}`)
	state.SetCurrentFlatcarVersion("2.0.0")
	t.Cleanup(func() { state.SetCurrentFlatcarVersion("") })

	get := func(query string) updateCheckResponse {
		t.Helper()
		r := do(t, http.MethodGet, srv.URL+"/update-check?"+query, "")
		if r.status != 200 {
			t.Fatalf("update-check: %+v", r)
		}
		var resp updateCheckResponse
		if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := get("mac=" + bluefinMAC + "&os=bluefin-server&version=26.08.0")
	if resp.RebootRequired || resp.Reason != "server has no bluefin release yet" || resp.Running != "26.08.0" || resp.Target != "" {
		t.Fatalf("no release cached: never reboot: %+v", resp)
	}
	h, _ := hardware.Get(bluefinMAC)
	if h.Running != "26.08.0" || h.LastCheck == "" || h.RebootPending {
		t.Fatalf("check must still be recorded: %+v", h)
	}

	installBluefinFixture(t, dir)
	resp = get("mac=" + bluefinMAC + "&os=bluefin-server&version=" + bluefinTestVersion)
	if resp.RebootRequired || resp.Reason != "bluefin up to date" || resp.Target != bluefinTestVersion {
		t.Fatalf("running the target: %+v", resp)
	}
	resp = get("mac=" + bluefinMAC + "&os=bluefin-server&version=26.08.0")
	if !resp.RebootRequired || resp.Reason != bluefinUpdateReason+bluefinTestVersion || resp.Target != bluefinTestVersion {
		t.Fatalf("a diskless host behind the target re-images on reboot: %+v", resp)
	}
	resp = get("mac=" + bluefinMAC + "&os=bluefin-server")
	if resp.RebootRequired || resp.Reason != "host reported no version" {
		t.Fatalf("unknown running version fails closed: %+v", resp)
	}

	resp = get("mac=" + bluefinMAC + "&os=flatcar&version=1.0.0")
	if !resp.RebootRequired || !strings.HasPrefix(resp.Reason, bluefinUpdateReason) {
		t.Fatalf("a bluefin host reporting os=flatcar (Flatcar-based os-release) is still bluefin: %+v", resp)
	}

	resp = get("mac=aa:bb:cc:dd:ee:c1&os=bluefin&version=1.0.0")
	if !resp.RebootRequired || !strings.HasPrefix(resp.Reason, bluefinUpdateReason) {
		t.Fatalf("os=bluefin from the client wins over the registered os: %+v", resp)
	}
	resp = get("mac=aa:bb:cc:dd:ee:c1&os=flatcar&version=1.0.0")
	if !resp.RebootRequired || resp.Reason != "flatcar 1.0.0 differs from served 2.0.0" {
		t.Fatalf("flatcar logic untouched: %+v", resp)
	}
}

func TestBluefinManifestIsServedFromData(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	if m, ok := versions.CurrentBluefinManifest(); !ok || m.Version != bluefinTestVersion {
		t.Fatalf("manifest: %+v %v", m, ok)
	}
	r := do(t, http.MethodGet, srv.URL+"/data/bluefin/current/manifest.json", "")
	if r.status != 200 || !strings.Contains(r.body, `"netbootUKI"`) {
		t.Fatalf("manifest is public: %+v", r)
	}
}

const (
	bluefinPrevVersion = "20260926.200"
	bluefinDashMAC     = "aa-bb-cc-dd-ee-b1"
	bluefinBootPath    = "/bluefin/" + bluefinDashMAC + "/bluefin-server-netboot.efi"
	bluefinNodePath    = "/bluefin/" + bluefinDashMAC + "/bluefin-node.ign"
)

func head(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestBluefinHTTPBootDecision(t *testing.T) {
	newTestServer(t)
	for _, h := range []string{
		`{"mac":"aa:bb:cc:dd:ee:01","hostname":"d","os":"bluefin"}`,
		`{"mac":"aa:bb:cc:dd:ee:02","hostname":"i","os":"bluefin","mode":"installed"}`,
		`{"mac":"aa:bb:cc:dd:ee:03","hostname":"r","os":"bluefin","mode":"installed","doInstall":true,"installDisk":"/dev/vda"}`,
		`{"mac":"aa:bb:cc:dd:ee:04","hostname":"f","os":"flatcar"}`,
	} {
		var host hardware.Host
		if err := json.Unmarshal([]byte(h), &host); err != nil {
			t.Fatal(err)
		}
		if _, err := hardware.Put(host); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, mac, autoRegister string
		url                     string
		handled                 bool
	}{
		{"diskless bluefin", "aa:bb:cc:dd:ee:01", "", "http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-01/bluefin-server-netboot.efi", true},
		{"installed bluefin boots its disk", "aa:bb:cc:dd:ee:02", "", "", true},
		{"installed bluefin being reinstalled", "aa:bb:cc:dd:ee:03", "", "http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-03/bluefin-server-netboot.efi", true},
		{"flatcar keeps the default", "aa:bb:cc:dd:ee:04", "", "", false},
		{"unknown keeps the default", "aa:bb:cc:dd:ee:05", "", "", false},
		{"unknown under --autoRegister=flatcar", "aa:bb:cc:dd:ee:05", "flatcar", "", false},
		{"unknown under --autoRegister=bluefin", "aa:bb:cc:dd:ee:05", "bluefin", "http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-05/bluefin-server-netboot.efi", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viper.Set(config.AutoRegister, tc.autoRegister)
			hw, _ := net.ParseMAC(tc.mac)
			url, handled := BluefinHTTPBoot(hw)
			if url != tc.url || handled != tc.handled {
				t.Fatalf("got (%q, %v), want (%q, %v)", url, handled, tc.url, tc.handled)
			}
		})
	}
	if _, ok := hardware.Get("aa:bb:cc:dd:ee:05"); ok {
		t.Fatal("the DHCP decision never registers a host")
	}
}

func TestBluefinRoutes(t *testing.T) {
	srv, dir := newTestServer(t)
	writeBluefinRelease(t, dir, bluefinPrevVersion, "previous")
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c1","hostname":"fc","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c2","hostname":"inst","os":"bluefin","mode":"installed"}`)

	cases := []struct {
		name, method, path string
		status             int
		body, ctype        string
	}{
		{"netboot UKI", http.MethodGet, bluefinBootPath, 200, "UKI-" + bluefinTestVersion, "application/efi"},
		{"any efi name, colon MAC", http.MethodGet, "/bluefin/" + bluefinMAC + "/BOOTX64.EFI", 200, "UKI-" + bluefinTestVersion, "application/efi"},
		{"current DDI", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/bluefin-server_" + bluefinTestVersion + ".raw", 200, "DDI-" + bluefinTestVersion, "application/octet-stream"},
		{"previous DDI for hosts mid-boot", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/bluefin-server_" + bluefinPrevVersion + ".raw", 200, "DDI-" + bluefinPrevVersion, ""},
		{"unknown DDI version", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/bluefin-server_20250101.1.raw", 404, "", ""},
		{"SHA256SUMS", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/SHA256SUMS", 200, "SUMS-" + bluefinTestVersion, ""},
		{"SHA256SUMS.gpg", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/SHA256SUMS.gpg", 200, "SIG-" + bluefinTestVersion, ""},
		{"a sysext is not served here", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/zfs_" + bluefinTestVersion + ".raw", 404, "", ""},
		{"manifest is not served here", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/manifest.json", 404, "", ""},
		{"flatcar host", http.MethodGet, "/bluefin/aa-bb-cc-dd-ee-c1/bluefin-server-netboot.efi", 404, "", ""},
		{"flatcar host DDI", http.MethodGet, "/bluefin/aa-bb-cc-dd-ee-c1/bluefin-server_" + bluefinTestVersion + ".raw", 404, "", ""},
		{"flatcar host config", http.MethodGet, "/bluefin/aa-bb-cc-dd-ee-c1/bluefin-node.ign", 404, "", ""},
		{"installed host falls through to disk", http.MethodGet, "/bluefin/aa-bb-cc-dd-ee-c2/bluefin-server-netboot.efi", 404, "", ""},
		{"unknown host", http.MethodGet, "/bluefin/aa-bb-cc-dd-ee-99/bluefin-server-netboot.efi", 404, "", ""},
		{"not a MAC", http.MethodGet, "/bluefin/nope/bluefin-server-netboot.efi", 404, "", ""},
		{"no file", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/", 404, "", ""},
		{"nested path", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/x/SHA256SUMS", 404, "", ""},
		{"traversal", http.MethodGet, "/bluefin/" + bluefinDashMAC + "/../../hardware.json", 404, "", ""},
		{"POST", http.MethodPost, bluefinBootPath, 405, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := do(t, tc.method, srv.URL+tc.path, "")
			if r.status != tc.status {
				t.Fatalf("status %d want %d: %s", r.status, tc.status, r.body)
			}
			if tc.body != "" && r.body != tc.body {
				t.Fatalf("body %q want %q", r.body, tc.body)
			}
			if tc.ctype != "" && r.contentType != tc.ctype {
				t.Fatalf("content-type %q want %q", r.contentType, tc.ctype)
			}
		})
	}

	t.Run("HEAD answers like GET", func(t *testing.T) {
		for _, p := range []string{bluefinBootPath, "/bluefin/" + bluefinDashMAC + "/bluefin-server_" + bluefinTestVersion + ".raw", "/bluefin/" + bluefinDashMAC + "/SHA256SUMS"} {
			resp := head(t, srv.URL+p)
			if resp.StatusCode != 200 || resp.ContentLength <= 0 {
				t.Fatalf("%s: HEAD %d length %d", p, resp.StatusCode, resp.ContentLength)
			}
		}
	})

	t.Run("no release cached", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, "bluefin", "current")); err != nil {
			t.Fatal(err)
		}
		if r := do(t, http.MethodGet, srv.URL+bluefinBootPath, ""); r.status != 404 {
			t.Fatalf("%s: without a current release there is no target: %+v", bluefinBootPath, r)
		}
		ddi := "/bluefin/" + bluefinDashMAC + "/bluefin-server_" + bluefinTestVersion + ".raw"
		sums := "/bluefin/" + bluefinDashMAC + "/SHA256SUMS"
		for _, p := range []string{ddi, sums} {
			if r := do(t, http.MethodGet, srv.URL+p, ""); r.status != 200 {
				t.Fatalf("%s: a release still on disk that the host netbooted serves it mid-boot: %+v", p, r)
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, "bluefin", bluefinTestVersion)); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{ddi, sums} {
			if r := do(t, http.MethodGet, srv.URL+p, ""); r.status != 404 {
				t.Fatalf("%s: pruned release: %+v", p, r)
			}
		}
	})
}

func TestBluefinUKIFetchRecordsBoot(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)

	head(t, srv.URL+bluefinBootPath)
	do(t, http.MethodGet, srv.URL+bluefinBootPath+"?preview=1", "")
	if h, _ := hardware.Get(bluefinMAC); h.Booted != "" {
		t.Fatalf("HEAD and previews record nothing: %+v", h)
	}
	do(t, http.MethodGet, srv.URL+bluefinBootPath, "")
	if h, _ := hardware.Get(bluefinMAC); h.Booted == "" || h.IP != "127.0.0.1" {
		t.Fatalf("the firmware's GET records boot and IP: %+v", h)
	}

	const unknown = "aa:bb:cc:dd:ee:77"
	do(t, http.MethodGet, srv.URL+"/bluefin/aa-bb-cc-dd-ee-77/bluefin-server-netboot.efi", "")
	if _, ok := hardware.Snapshot().UnknownHosts[unknown]; !ok {
		t.Fatal("an unknown MAC is recorded for the fleet view")
	}
	viper.Set(config.AutoRegister, "flatcar")
	if r := do(t, http.MethodGet, srv.URL+"/bluefin/aa-bb-cc-dd-ee-77/bluefin-server-netboot.efi", ""); r.status != 404 {
		t.Fatalf("%+v", r)
	}
	if _, ok := hardware.Get(unknown); ok {
		t.Fatal("--autoRegister=flatcar never registers through /bluefin/")
	}
	viper.Set(config.AutoRegister, "bluefin")
	if r := do(t, http.MethodGet, srv.URL+"/bluefin/aa-bb-cc-dd-ee-77/bluefin-server-netboot.efi", ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	if h, ok := hardware.Get(unknown); !ok || h.OS != "bluefin" || h.Booted == "" {
		t.Fatalf("--autoRegister=bluefin registers on the UKI fetch: %+v", h)
	}
}

// bluefinNodeFile is one storage.files entry of a node config, contents
// decoded when inline.
type bluefinNodeFile struct {
	mode              int
	contents, source  string
	hash              string
	overwrite, remote bool
}

func decodeDataURL(t *testing.T, src string) string {
	t.Helper()
	switch {
	case strings.HasPrefix(src, "data:text/plain;charset=utf-8;base64,"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(src, "data:text/plain;charset=utf-8;base64,"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	case strings.HasPrefix(src, "data:,"):
		s, err := url.PathUnescape(strings.TrimPrefix(src, "data:,"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Fatalf("not a data URL: %q", src)
	return ""
}

// bluefinBootyUnits and bluefinBootyFiles are what every Bluefin node
// config carries under the default --builtin (see
// TestBluefinNodeCarriesBootyUnits); bluefinNode leaves them out of its
// maps so the other tests can count what is specific to the host.
var (
	bluefinBootyUnits = []string{ign.BootedUnitName, ign.UpdateServiceName, ign.UpdateTimerName, ign.HealthUnitName}
	bluefinBootyFiles = []string{bluefinUpdateCheckScript, bluefinHealthReportScript}
)

// bluefinNode fetches mac's node config and checks it is a valid spec 3.6.0
// config. The Booty units and scripts every node gets are left out.
func bluefinNode(t *testing.T, srvURL, mac, query string) (ign36.Config, map[string]bluefinNodeFile, map[string]ign36.Unit) {
	t.Helper()
	cfg, files, units := bluefinNodeAll(t, srvURL, mac, query)
	for _, name := range bluefinBootyUnits {
		delete(units, name)
	}
	for _, path := range bluefinBootyFiles {
		delete(files, path)
	}
	return cfg, files, units
}

func bluefinNodeAll(t *testing.T, srvURL, mac, query string) (ign36.Config, map[string]bluefinNodeFile, map[string]ign36.Unit) {
	t.Helper()
	r := do(t, http.MethodGet, srvURL+"/bluefin/"+strings.ReplaceAll(mac, ":", "-")+"/bluefin-node.ign"+query, "")
	if r.status != 200 || r.contentType != "application/json" {
		t.Fatalf("node config: %+v", r)
	}
	cfg, rpt, err := v3_6.Parse([]byte(r.body))
	if err != nil || rpt.IsFatal() {
		t.Fatalf("Ignition 3.6 rejects the node config: %v %s\n%s", err, rpt.String(), r.body)
	}
	if cfg.Ignition.Version != "3.6.0" {
		t.Fatalf("version %q", cfg.Ignition.Version)
	}
	files := map[string]bluefinNodeFile{}
	for _, f := range cfg.Storage.Files {
		nf := bluefinNodeFile{overwrite: f.Overwrite != nil && *f.Overwrite}
		if f.Mode != nil {
			nf.mode = *f.Mode
		}
		if f.Contents.Source != nil {
			nf.source = *f.Contents.Source
			if strings.HasPrefix(nf.source, "data:") {
				nf.contents = decodeDataURL(t, nf.source)
			} else {
				nf.remote = true
			}
		}
		if f.Contents.Verification.Hash != nil {
			nf.hash = *f.Contents.Verification.Hash
		}
		files[f.Path] = nf
	}
	units := map[string]ign36.Unit{}
	for _, u := range cfg.Systemd.Units {
		units[u.Name] = u
	}
	return cfg, files, units
}

func TestBluefinNodeIgnitionBasics(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	viper.Set(config.SSHAuthorizedKeys, []string{"ssh-ed25519 AAAA... dogfood"})
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)

	cfg, files, units := bluefinNode(t, srv.URL, bluefinMAC, "")
	if f := files["/etc/hostname"]; f.contents != "srv1\n" || f.mode != 0o644 || !f.overwrite {
		t.Fatalf("hostname: %+v", f)
	}
	if len(cfg.Passwd.Users) != 1 || cfg.Passwd.Users[0].Name != "root" || len(cfg.Passwd.Users[0].SSHAuthorizedKeys) != 1 || cfg.Passwd.Users[0].SSHAuthorizedKeys[0] != "ssh-ed25519 AAAA... dogfood" {
		t.Fatalf("root keys: %+v", cfg.Passwd.Users)
	}
	if u, ok := units["sshd.service"]; !ok || u.Enabled == nil || !*u.Enabled || u.Contents != nil {
		t.Fatalf("SSH keys enable the image's sshd.service: %v", units)
	}
	if len(cfg.Storage.Disks) != 0 || len(cfg.Storage.Filesystems) != 0 || len(units) != 1 || len(files) != 1 {
		t.Fatalf("a plain diskless host touches no disk and adds only sshd: %+v %v", cfg.Storage, units)
	}
	if _, err := os.Stat(filepath.Join(dir, "bluefin", "current")); err != nil {
		t.Fatal(err)
	}

	resp := head(t, srv.URL+bluefinNodePath)
	get := do(t, http.MethodGet, srv.URL+bluefinNodePath, "")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || resp.ContentLength != int64(len(get.body)) {
		t.Fatalf("HEAD must answer like GET: %d %q %d vs %d", resp.StatusCode, resp.Header.Get("Content-Type"), resp.ContentLength, len(get.body))
	}
	if h, _ := hardware.Get(bluefinMAC); h.Booted == "" {
		t.Fatalf("the config GET records the boot: %+v", h)
	}

	viper.Set(config.Builtin, "none")
	if r := do(t, http.MethodGet, srv.URL+bluefinNodePath, ""); r.status != 404 {
		t.Fatalf("nothing to configure is 404: %+v", r)
	}
	if resp := head(t, srv.URL+bluefinNodePath); resp.StatusCode != 404 {
		t.Fatalf("HEAD too: %d", resp.StatusCode)
	}
	viper.Set(config.Builtin, "hostname")
	if cfg, files, units := bluefinNode(t, srv.URL, bluefinMAC, ""); len(files) != 1 || len(cfg.Passwd.Users) != 0 || len(units) != 0 {
		t.Fatalf("--builtin=hostname drops the keys and sshd: %v %v", files, units)
	}
	for _, p := range []string{"/bluefin/aa-bb-cc-dd-ee-99/bluefin-node.ign", "/bluefin/" + bluefinDashMAC + "/other.ign"} {
		if r := do(t, http.MethodGet, srv.URL+p, ""); r.status != 404 {
			t.Fatalf("%s: %+v", p, r)
		}
	}
}

func TestBluefinNodeIgnitionStateDiskMatchesUpstreamFixture(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	viper.Set(config.Builtin, "sshkeys")
	viper.Set(config.SSHAuthorizedKeys, []string{"ssh-ed25519 AAAA... dogfood"})
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","stateDisk":"/dev/vdb"}`)

	got, _, _ := bluefinNode(t, srv.URL, bluefinMAC, "")
	raw, err := os.ReadFile(filepath.Join("testdata", "var-on-disk.ign"))
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := v3_6.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Storage.Filesystems) != 1 || got.Storage.Filesystems[0].Path == nil || *got.Storage.Filesystems[0].Path != "/var" {
		t.Fatalf("the state filesystem names path /var so Ignition writes /var files onto it: %+v", got.Storage.Filesystems)
	}
	got.Storage.Filesystems[0].Path = nil
	if len(got.Systemd.Units) == 0 || got.Systemd.Units[0].Name != "sshd.service" {
		t.Fatalf("SSH keys enable sshd ahead of the fixture's units: %+v", got.Systemd.Units)
	}
	got.Systemd.Units = got.Systemd.Units[1:]
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("state disk config differs from projectbluefin/server's var-on-disk.ign:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestBluefinNodeIgnitionExtensions(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","extensions":["zfs","kubestellar","k0s"]}`)

	_, files, units := bluefinNode(t, srv.URL, bluefinMAC, "")
	base := "http://192.168.1.10:8080/data/bluefin/" + bluefinTestVersion + "/"
	for p, want := range map[string]bluefinNodeFile{
		"/etc/extensions/zfs_" + bluefinTestVersion + ".raw":         {mode: 0o644, source: base + "zfs_" + bluefinTestVersion + ".raw", hash: "sha256-" + sha256Hex("ZFS-"+bluefinTestVersion), overwrite: true, remote: true},
		"/etc/extensions/kubestellar_" + bluefinTestVersion + ".raw": {mode: 0o644, source: base + "kubestellar_" + bluefinTestVersion + ".raw", hash: "sha256-" + sha256Hex("KS-"+bluefinTestVersion), overwrite: true, remote: true},
		"/var/lib/k0s/k0s.raw": {mode: 0o644, source: base + "k0s-1.36.4-k0s.0.raw", hash: "sha256-" + sha256Hex("K0S"), overwrite: true, remote: true},
	} {
		if got := files[p]; got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", p, got, want)
		}
		r := do(t, http.MethodGet, strings.Replace(want.source, "http://192.168.1.10:8080", srv.URL, 1), "")
		if r.status != 200 || "sha256-"+sha256Hex(r.body) != want.hash {
			t.Errorf("%s: the source must serve the verified bytes: %+v", p, r)
		}
	}
	fb, ok := units["k0s-first-boot.service"]
	if !ok || fb.Enabled == nil || !*fb.Enabled || fb.Contents != nil {
		t.Fatalf("k0s-first-boot.service is only enabled: %+v", fb)
	}

	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","extensions":["zfs"]}`)
	_, files, units = bluefinNode(t, srv.URL, bluefinMAC, "")
	if _, ok := files["/var/lib/k0s/k0s.raw"]; ok || len(units) != 0 {
		t.Fatalf("only zfs: %v %v", files, units)
	}

	if r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"`+bluefinMAC+`","os":"bluefin","extensions":["kubestellar"]}`); r.status != 400 || !strings.Contains(r.body, "kubestellar requires k0s") {
		t.Fatalf("kubestellar without k0s is refused: %+v", r)
	}
	if r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"`+bluefinMAC+`","os":"flatcar","stateDisk":"/dev/sdb"}`); r.status != 400 {
		t.Fatalf("stateDisk on flatcar is refused: %+v", r)
	}
}

func TestBluefinNodeIgnitionInstall(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true,"installDisk":"/dev/nvme0n1"}`)

	_, _, units := bluefinNode(t, srv.URL, bluefinMAC, "?preview=1")
	u, ok := units["booty-install.service"]
	if !ok || u.Enabled == nil || !*u.Enabled || u.Contents == nil {
		t.Fatalf("install unit: %+v", u)
	}
	for _, want := range []string{
		"After=network-online.target run-bluefin-boot.mount\n",
		"Requires=run-bluefin-boot.mount\n",
		"Type=oneshot\n",
		"ExecStart=/usr/bin/systemd-sysinstall --erase=yes --confirm=no --variables=yes --reboot=yes --definitions=/run/bluefin/boot/bluefin/repart.d --kernel=/run/bluefin/boot/EFI/Linux/bluefin-server-" + bluefinTestVersion + ".efi /dev/nvme0n1\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(*u.Contents, want) {
			t.Errorf("install unit missing %q:\n%s", want, *u.Contents)
		}
	}
	head(t, srv.URL+bluefinNodePath)
	if h, _ := hardware.Get(bluefinMAC); !h.DoInstall || h.Installed() {
		t.Fatalf("preview and HEAD keep doInstall: %+v", h)
	}

	_, _, units = bluefinNode(t, srv.URL, bluefinMAC, "")
	if _, ok := units["booty-install.service"]; !ok {
		t.Fatal("the real fetch carries the install unit")
	}
	h, _ := hardware.Get(bluefinMAC)
	if h.DoInstall || !h.Installed() {
		t.Fatalf("--doInstallClearOn=ignition: the fetch that carries the install marks the host installed: %+v", h)
	}
	if _, _, units := bluefinNode(t, srv.URL, bluefinMAC, "?preview=1"); len(units) != 0 {
		t.Fatalf("installed host without doInstall gets no install unit: %v", units)
	}
	if r := do(t, http.MethodGet, srv.URL+bluefinBootPath, ""); r.status != 404 {
		t.Fatalf("installed host gets no UKI: %+v", r)
	}

	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true}`)
	if _, _, units := bluefinNode(t, srv.URL, bluefinMAC, "?preview=1"); len(units) != 0 {
		t.Fatalf("doInstall without installDisk never installs: %v", units)
	}
}

func TestBluefinInstallClearsOnNextBoot(t *testing.T) {
	for _, mode := range []string{config.ClearOnNextBoot, config.ClearOnBooted} {
		t.Run(mode, func(t *testing.T) {
			srv, dir := newTestServer(t)
			installBluefinFixture(t, dir)
			viper.Set(config.DoInstallClearOn, mode)
			viper.Set(config.InstallMinDuration, 3*time.Minute)
			register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true,"installDisk":"/dev/vda"}`)
			hw, _ := net.ParseMAC(bluefinMAC)

			if r := do(t, http.MethodGet, srv.URL+bluefinBootPath, ""); r.status != 200 {
				t.Fatalf("install boot: %+v", r)
			}
			h, _ := hardware.Get(bluefinMAC)
			if !h.DoInstall || h.InstallServedAt == "" {
				t.Fatalf("the install boot stamps installServedAt: %+v", h)
			}
			if _, _, units := bluefinNode(t, srv.URL, bluefinMAC, ""); units["booty-install.service"].Name == "" {
				t.Fatal("install unit served")
			}
			if h, _ := hardware.Get(bluefinMAC); !h.DoInstall {
				t.Fatalf("%s: the config fetch does not clear: %+v", mode, h)
			}
			if r := do(t, http.MethodGet, srv.URL+bluefinBootPath, ""); r.status != 200 {
				t.Fatalf("a netboot within installMinDuration (install failed) installs again: %+v", r)
			}

			old := time.Now().Add(-4 * time.Minute).UTC().Format(time.RFC3339)
			if _, err := hardware.Update(bluefinMAC, func(h *hardware.Host) { h.InstallServedAt = old }); err != nil {
				t.Fatal(err)
			}
			if url, handled := BluefinHTTPBoot(hw); url == "" || !handled {
				t.Fatal("DHCP still offers the UKI until the fetch confirms the install")
			}
			if r := do(t, http.MethodGet, srv.URL+bluefinBootPath, ""); r.status != 404 {
				t.Fatalf("the netboot after installMinDuration finds the host installed and sends it to disk: %+v", r)
			}
			h, _ = hardware.Get(bluefinMAC)
			if h.DoInstall || h.InstallServedAt != "" || !h.Installed() {
				t.Fatalf("installed: %+v", h)
			}
			if url, handled := BluefinHTTPBoot(hw); url != "" || !handled {
				t.Fatal("DHCP now stays silent")
			}
		})
	}

	t.Run("POST /booted", func(t *testing.T) {
		srv, _ := newTestServer(t)
		viper.Set(config.DoInstallClearOn, config.ClearOnBooted)
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true,"installDisk":"/dev/vda"}`)
		if r := do(t, http.MethodPost, srv.URL+"/booted?mac="+bluefinMAC, ""); r.status != 200 {
			t.Fatalf("%+v", r)
		}
		if h, _ := hardware.Get(bluefinMAC); h.DoInstall || !h.Installed() {
			t.Fatalf("POST /booted marks an installing bluefin host installed: %+v", h)
		}
	})
}

func TestBluefinNodeIgnitionMergesHostTemplate(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	butane := "variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/motd\n      mode: 0644\n      overwrite: true\n      contents:\n        inline: \"hello {{ .Hostname }}\\n\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config", "bluefin.yaml"), []byte(butane), 0o644); err != nil {
		t.Fatal(err)
	}
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","ignitionFile":"config/bluefin.yaml"}`)
	_, files, _ := bluefinNode(t, srv.URL, bluefinMAC, "")
	if files["/etc/motd"].contents != "hello srv1\n" || files["/etc/hostname"].contents != "srv1\n" {
		t.Fatalf("the host template is merged over the builtin pieces: %+v", files)
	}

	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
	if _, files, _ := bluefinNode(t, srv.URL, bluefinMAC, ""); len(files) != 1 {
		t.Fatalf("the global Flatcar/CoreOS template is not merged into Bluefin hosts: %+v", files)
	}

	if err := os.WriteFile(filepath.Join(dir, "config", "broken.yaml"), []byte("variant: fcos\nversion: 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","ignitionFile":"config/broken.yaml"}`)
	if r := do(t, http.MethodGet, srv.URL+bluefinNodePath, ""); r.status != 500 {
		t.Fatalf("a broken host template is an error, not a silently different config: %+v", r)
	}
}
