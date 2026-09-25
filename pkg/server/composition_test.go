package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v3_5 "github.com/coreos/ignition/v2/config/v3_5"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	ign "github.com/jeefy/booty/pkg/ignition"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

const overridingButane = `variant: fcos
version: 1.5.0
storage:
  files:
    - path: /etc/hostname
      mode: 0644
      contents:
        inline: "custom-{{ .Hostname }}"
    - path: /etc/motd
      mode: 0644
      contents:
        inline: "hello"
systemd:
  units:
    - name: booty-update.timer
      enabled: false
      contents: |
        [Unit]
        Description=user override

        [Timer]
        OnCalendar=hourly

        [Install]
        WantedBy=timers.target
`

func register(t *testing.T, srvURL, body string) {
	t.Helper()
	if r := do(t, http.MethodPost, srvURL+"/register", body); r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
}

func parseIgnition(t *testing.T, body string) map[string]any {
	t.Helper()
	if _, rpt, err := v3_5.ParseCompatibleVersion([]byte(body)); err != nil || rpt.IsFatal() {
		t.Fatalf("ignition rejected config: %v\n%s\n%s", err, rpt.String(), body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestIgnitionChildren(t *testing.T) {
	srv, dir := newTestServer(t)
	keys := filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(keys, []byte("# keys\nssh-ed25519 AAAAone test1\n\nssh-ed25519 AAAAtwo test2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.SSHAuthorizedKeysFl, keys)
	viper.Set(config.SSHAuthorizedKeys, []string{"ssh-rsa BBBB inline"})

	for _, path := range []string{"/ignition/user.json", "/ignition/builtin.json"} {
		assertJSONError(t, do(t, http.MethodGet, srv.URL+path+"?mac=nope", ""), http.StatusBadRequest)
		assertJSONError(t, do(t, http.MethodGet, srv.URL+path+"?mac=aa:bb:cc:dd:ee:01", ""), http.StatusNotFound)
		assertJSONError(t, do(t, http.MethodPost, srv.URL+path+"?mac=aa:bb:cc:dd:ee:01", ""), http.StatusMethodNotAllowed)
	}
	if len(hardware.Snapshot().UnknownHosts) != 0 {
		t.Fatal("child endpoints must not record unknown hosts")
	}

	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","os":"flatcar","doInstall":true}`)

	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=AA-BB-CC-DD-EE-01", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("builtin: %+v", r)
	}
	builtin := parseIgnition(t, r.body)
	if builtin["ignition"].(map[string]any)["version"] != "3.4.0" {
		t.Fatalf("builtin must be spec 3.4.0: %s", r.body)
	}
	for _, want := range []string{
		`"path":"/etc/hostname"`, `data:,n1%0A`,
		`"path":"/usr/local/bin/booty-update-check"`,
		`"name":"booty-update.timer"`, `"name":"booty-update.service"`, `"name":"booty-booted.service"`,
		`http://192.168.1.10:8080/booted?mac=$$MAC`,
		`ssh-ed25519 AAAAone test1`, `ssh-ed25519 AAAAtwo test2`, `ssh-rsa BBBB inline`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("builtin fragment missing %s", want)
		}
	}
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:01"); !h.DoInstall || h.Booted != "" {
		t.Fatalf("child fetch must have no side effects, got %+v", h)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition/user.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || !strings.Contains(r.body, "n1") || strings.Contains(r.body, "booty-update") {
		t.Fatalf("user child must be the plain rendered butane: %+v", r)
	}
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:01"); !h.DoInstall || h.Booted != "" {
		t.Fatalf("child fetch must have no side effects, got %+v", h)
	}

	viper.Set(config.Builtin, "hostname,booted")
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	if strings.Contains(r.body, "booty-update") || strings.Contains(r.body, "sshAuthorizedKeys") || !strings.Contains(r.body, "booty-booted.service") {
		t.Fatalf("--builtin toggles must be honoured: %s", r.body)
	}
}

func TestIgnitionPreviewParts(t *testing.T) {
	srv, dir := newTestServer(t)
	if err := os.WriteFile(filepath.Join(dir, "config", "override.yaml"), []byte(overridingButane), 0o644); err != nil {
		t.Fatal(err)
	}
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","os":"flatcar","ignitionFile":"config/override.yaml"}`)
	base := srv.URL + "/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1"

	r := do(t, http.MethodGet, base, "")
	var w ignitionWrapper
	if err := json.Unmarshal([]byte(r.body), &w); err != nil || len(w.Ignition.Config.Merge) != 2 {
		t.Fatalf("preview without part must be the wrapper: %+v err=%v", r, err)
	}

	r = do(t, http.MethodGet, base+"&part=user", "")
	if r.status != 200 || !strings.Contains(r.body, "custom-n1") || strings.Contains(r.body, "booty-booted") {
		t.Fatalf("part=user: %+v", r)
	}
	r = do(t, http.MethodGet, base+"&part=builtin", "")
	if r.status != 200 || !strings.Contains(r.body, "booty-booted.service") || strings.Contains(r.body, "custom-n1") {
		t.Fatalf("part=builtin: %+v", r)
	}

	r = do(t, http.MethodGet, base+"&part=merged", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("part=merged: %+v", r)
	}
	merged := parseIgnition(t, r.body)
	files := merged["storage"].(map[string]any)["files"].([]any)
	hostnameCount := 0
	for _, f := range files {
		file := f.(map[string]any)
		if file["path"] == "/etc/hostname" {
			hostnameCount++
			src := file["contents"].(map[string]any)["source"].(string)
			if !strings.Contains(src, "custom-n1") {
				t.Fatalf("user config must win for /etc/hostname, got %s", src)
			}
		}
	}
	if hostnameCount != 1 {
		t.Fatalf("/etc/hostname should be merged into one entry, got %d", hostnameCount)
	}
	for _, want := range []string{"/etc/motd", "/usr/local/bin/booty-update-check", "booty-booted.service", "booty-update.service"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("merged config missing %s", want)
		}
	}
	units := merged["systemd"].(map[string]any)["units"].([]any)
	for _, u := range units {
		unit := u.(map[string]any)
		if unit["name"] == "booty-update.timer" {
			if unit["enabled"] != false || !strings.Contains(unit["contents"].(string), "OnCalendar=hourly") {
				t.Fatalf("user unit override must win: %v", unit)
			}
		}
	}
	if strings.Count(r.body, "\n") < 10 {
		t.Fatal("merged preview should be pretty-printed")
	}

	assertJSONError(t, do(t, http.MethodGet, base+"&part=bogus", ""), http.StatusBadRequest)
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:01"); h.Booted != "" {
		t.Fatalf("previews must not record boots, got %+v", h)
	}
}

func TestIgnitionBuiltinNone(t *testing.T) {
	srv, _ := newTestServer(t)
	viper.Set(config.Builtin, "none")
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","os":"flatcar","doInstall":true}`)

	r := do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || strings.Contains(r.body, "merge") || !strings.Contains(r.body, "n1") {
		t.Fatalf("--builtin=none must serve the plain user config: %+v", r)
	}
	h, _ := hardware.Get("aa:bb:cc:dd:ee:01")
	if h.DoInstall || h.Booted == "" {
		t.Fatalf("side effects must still fire with builtin=none, got %+v", h)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1&part=merged", "")
	if r.status != 200 || strings.Contains(r.body, "merge") || !strings.Contains(r.body, "n1") {
		t.Fatalf("with builtin=none every part is the user config: %+v", r)
	}
}

func TestIgnitionEmbeddedDefaultTemplate(t *testing.T) {
	srv, dir := newTestServer(t)
	if err := os.Remove(filepath.Join(dir, "config", "ignition.yaml")); err != nil {
		t.Fatal(err)
	}
	if !DefaultTemplateInUse() {
		t.Fatal("DefaultTemplateInUse should be true once config/ignition.yaml is gone")
	}
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","os":"flatcar"}`)

	r := do(t, http.MethodGet, srv.URL+"/ignition/user.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 {
		t.Fatalf("embedded default should render: %+v", r)
	}
	parseIgnition(t, r.body)
	if !strings.Contains(r.body, "n1") || !strings.Contains(r.body, `"version": "3.4.0"`) {
		t.Fatalf("embedded default must template the hostname: %s", r.body)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1", "")
	if r.status != 200 || !strings.Contains(r.body, `"version":"3.4.0"`) {
		t.Fatalf("wrapper should pick up the embedded default's version: %+v", r)
	}

	viper.Set(config.IgnitionFile, "config/custom.yaml")
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/ignition/user.json?mac=aa:bb:cc:dd:ee:01", ""), http.StatusInternalServerError)
	if DefaultTemplateInUse() {
		t.Fatal("a custom --ignitionFile never falls back to the embedded template")
	}
	viper.Set(config.IgnitionFile, config.DefaultIgnitionFile)

	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"n2","ignitionFile":"config/ignition.yaml"}`)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/ignition/user.json?mac=aa:bb:cc:dd:ee:02", ""), http.StatusInternalServerError)
}

func statHardwareMap(t *testing.T, dir string) (time.Time, int64) {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, "hardware.json"))
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime(), info.Size()
}

func TestUpdateCheckFlatcar(t *testing.T) {
	srv, dir := newTestServer(t)
	state.SetCurrentFlatcarVersion("")
	t.Cleanup(func() { state.SetCurrentFlatcarVersion("") })
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })

	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/update-check", ""), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/update-check?mac=zz", ""), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/update-check?mac=aa:bb:cc:dd:ee:01&os=flatcar&version=1.0.0", ""), http.StatusNotFound)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/update-check?mac=aa:bb:cc:dd:ee:01", ""), http.StatusMethodNotAllowed)
	if len(hardware.Snapshot().UnknownHosts) != 0 {
		t.Fatal("/update-check must not record unknown hosts")
	}

	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","os":"flatcar"}`)
	url := srv.URL + "/update-check?mac=AA:BB:CC:DD:EE:01&os=flatcar&version=1.0.0"

	var resp updateCheckResponse
	get := func() updateCheckResponse {
		t.Helper()
		r := do(t, http.MethodGet, url, "")
		if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
			t.Fatalf("update-check: %+v", r)
		}
		if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp = get()
	if resp.RebootRequired || resp.Running != "1.0.0" || !strings.Contains(resp.Reason, "no flatcar version") {
		t.Fatalf("unknown target must fail closed: %+v", resp)
	}
	h, _ := hardware.Get("aa:bb:cc:dd:ee:01")
	if h.Running != "1.0.0" || h.LastCheck != "2026-09-25T12:00:00Z" || h.RebootPending {
		t.Fatalf("check must be recorded on the host: %+v", h)
	}
	if h.Booted != "" || h.IP != "" {
		t.Fatalf("update-check must not touch booted/ip: %+v", h)
	}

	state.SetCurrentFlatcarVersion("0.0.0")
	if resp = get(); resp.RebootRequired {
		t.Fatalf("0.0.0 placeholder is not a target: %+v", resp)
	}

	state.SetCurrentFlatcarVersion("2.0.0")
	now = now.Add(30 * time.Second)
	if resp = get(); !resp.RebootRequired || resp.Target != "2.0.0" {
		t.Fatalf("flatcar behind must require a reboot: %+v", resp)
	}
	h, _ = hardware.Get("aa:bb:cc:dd:ee:01")
	if !h.RebootPending || h.LastCheck != "2026-09-25T12:00:30Z" {
		t.Fatalf("state change must persist immediately: %+v", h)
	}
	r := do(t, http.MethodGet, srv.URL+"/info", "")
	if !strings.Contains(r.body, `"fleet":{"hosts":1,"pendingReboots":1}`) {
		t.Fatalf("info.fleet should count the pending reboot: %s", r.body)
	}
	r = do(t, http.MethodGet, srv.URL+"/booty.json", "")
	if !strings.Contains(r.body, `"running":"1.0.0"`) || !strings.Contains(r.body, `"lastCheck":"2026-09-25T12:00:30Z"`) || !strings.Contains(r.body, `"rebootPending":true`) {
		t.Fatalf("booty.json should expose the new fields: %s", r.body)
	}

	modBefore, sizeBefore := statHardwareMap(t, dir)
	now = now.Add(20 * time.Second)
	if resp = get(); !resp.RebootRequired {
		t.Fatalf("still behind: %+v", resp)
	}
	modAfter, sizeAfter := statHardwareMap(t, dir)
	if !modAfter.Equal(modBefore) || sizeAfter != sizeBefore {
		t.Fatal("unchanged result within a minute must not rewrite hardware.json")
	}
	if h, _ = hardware.Get("aa:bb:cc:dd:ee:01"); h.LastCheck != "2026-09-25T12:00:30Z" {
		t.Fatalf("lastCheck must not advance when the write is skipped: %+v", h)
	}

	now = now.Add(time.Minute)
	get()
	if h, _ = hardware.Get("aa:bb:cc:dd:ee:01"); h.LastCheck != "2026-09-25T12:01:50Z" {
		t.Fatalf("lastCheck must refresh once a minute has passed: %+v", h)
	}

	url = srv.URL + "/update-check?mac=aa:bb:cc:dd:ee:01&os=flatcar&version=2.0.0"
	if resp = get(); resp.RebootRequired || resp.Reason != "flatcar up to date" {
		t.Fatalf("equal versions must not reboot: %+v", resp)
	}
	if h, _ = hardware.Get("aa:bb:cc:dd:ee:01"); h.RebootPending || h.Running != "2.0.0" {
		t.Fatalf("clearing the pending flag must persist: %+v", h)
	}

	url = srv.URL + "/update-check?mac=aa:bb:cc:dd:ee:01&os=flatcar"
	if resp = get(); resp.RebootRequired {
		t.Fatalf("missing version must fail closed: %+v", resp)
	}
}

func TestUpdateCheckOSTree(t *testing.T) {
	srv, _ := newTestServer(t)
	state.SetCurrentCoreOSVersion("")
	t.Cleanup(func() { state.SetCurrentCoreOSVersion("") })
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","os":"ublue","ostreeImage":"ghcr.io/ublue-os/bazzite:stable"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"n2","os":"coreos"}`)

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

	resp := get("mac=aa:bb:cc:dd:ee:01&os=coreos&version=42.20250101.0.0&image=ostree-unverified-registry:ghcr.io/ublue-os/bazzite:stable&digest=sha256:old")
	if resp.RebootRequired || resp.Reason != "target image not cached yet" {
		t.Fatalf("uncached target must fail closed: %+v", resp)
	}
	if resp.Running != "ghcr.io/ublue-os/bazzite:stable@sha256:old" {
		t.Fatalf("running should be image@digest with transport stripped: %+v", resp)
	}

	digestLookup = func(ref string, _ ...crane.Option) (string, error) {
		if ref != "127.0.0.1:18099/ghcr.io/ublue-os/bazzite:stable" {
			t.Errorf("digest lookup must target the local registry, got %q", ref)
		}
		return "sha256:new", nil
	}
	resp = get("mac=aa:bb:cc:dd:ee:01&os=coreos&image=ostree-image-signed:docker://192.168.1.10:8080/ghcr.io/ublue-os/bazzite:stable&digest=sha256:old")
	if !resp.RebootRequired || resp.Target != "ghcr.io/ublue-os/bazzite:stable@sha256:new" {
		t.Fatalf("digest mismatch against cached image must reboot: %+v", resp)
	}
	resp = get("mac=aa:bb:cc:dd:ee:01&os=coreos&image=ghcr.io/ublue-os/bazzite:stable&digest=sha256:new")
	if resp.RebootRequired || resp.Reason != "image up to date" {
		t.Fatalf("matching digest must not reboot: %+v", resp)
	}
	resp = get("mac=aa:bb:cc:dd:ee:01&os=coreos&image=ghcr.io/ublue-os/bazzite:stable")
	if resp.RebootRequired {
		t.Fatalf("missing digest must fail closed: %+v", resp)
	}
	digestLookup = func(string, ...crane.Option) (string, error) { return "", os.ErrNotExist }

	resp = get("mac=aa:bb:cc:dd:ee:02&os=coreos&version=42.20250101.0.0")
	if resp.RebootRequired || !strings.Contains(resp.Reason, "no coreos version") {
		t.Fatalf("unknown coreos target must fail closed: %+v", resp)
	}
	state.SetCurrentCoreOSVersion("42.20250202.0.0")
	resp = get("mac=aa:bb:cc:dd:ee:02&os=coreos&version=42.20250101.0.0")
	if !resp.RebootRequired || resp.Target != "42.20250202.0.0" {
		t.Fatalf("coreos behind must reboot: %+v", resp)
	}
	resp = get("mac=aa:bb:cc:dd:ee:02&os=coreos&version=42.20250202.0.0")
	if resp.RebootRequired {
		t.Fatalf("coreos equal must not reboot: %+v", resp)
	}
	resp = get("mac=aa:bb:cc:dd:ee:02&os=coreos&version=42")
	if resp.RebootRequired || !strings.Contains(resp.Reason, "cannot compare") {
		t.Fatalf("non-coreos version string must fail closed: %+v", resp)
	}
	resp = get("mac=aa:bb:cc:dd:ee:02&os=haiku&version=1")
	if resp.RebootRequired || !strings.Contains(resp.Reason, "unknown os") {
		t.Fatalf("unknown os must fail closed: %+v", resp)
	}
}

func TestStarterButaneRenders(t *testing.T) {
	newTestServer(t)
	host := &hardware.Host{MAC: "aa:bb:cc:dd:ee:01", Hostname: "n1"}
	rendered, err := renderIgnition("starter", ign.StarterButane, host, "")
	if err != nil {
		t.Fatal(err)
	}
	parseIgnition(t, string(rendered))
	if !strings.Contains(string(rendered), `"version": "3.4.0"`) {
		t.Fatalf("starter must translate to spec 3.4.0:\n%s", rendered)
	}
	for _, v := range []string{"{{ .Hostname }}", "{{ .ServerIP }}", "{{ .JoinString }}", "{{ .OSTreeImage }}"} {
		if !strings.Contains(ign.StarterButane, v) {
			t.Errorf("starter header must document %s", v)
		}
	}
}

func TestExampleTemplatesRender(t *testing.T) {
	newTestServer(t)
	host := &hardware.Host{MAC: "aa:bb:cc:dd:ee:01", Hostname: "n1", OSTreeImage: "ghcr.io/ublue-os/bazzite:stable"}
	for _, name := range []string{"config/ignition.yaml", "ucore.but", "bazzite.but"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := renderIgnition(name, string(src), host, "kubeadm join 10.0.0.1:6443 --token t.s")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		parseIgnition(t, string(rendered))
		for _, gone := range []string{"/etc/hostname", "version-check", "booty-booted.service", `"name": "update.timer"`} {
			if strings.Contains(string(rendered), gone) {
				t.Errorf("%s still carries %q, which the builtin fragment provides", name, gone)
			}
		}
	}
}
