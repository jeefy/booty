package http

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

const minimalButane = `variant: fcos
version: 1.5.0
storage:
  files:
    - path: /etc/hostname
      mode: 0644
      contents:
        inline: "{{ .Hostname }}"
`

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	viper.Set(config.ServerIP, "192.168.1.10")
	viper.Set(config.ServerHttpPort, 8080)
	viper.Set(config.HttpPort, 8080)
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")

	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "ignition.yaml"), []byte(minimalButane), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "join.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flatcar_pin.txt"), []byte("1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kernel.tmp"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "registry", "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry", "blobs", "x"), []byte("blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := hardware.Load(); err != nil {
		t.Fatalf("hardware.Load: %v", err)
	}

	origARP, origDigest := arpLookup, digestLookup
	arpLookup = func(ip net.IP) (net.HardwareAddr, error) {
		return net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0xaa, 0xaa}, nil
	}
	digestLookup = func(string, ...crane.Option) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { arpLookup, digestLookup = origARP, origDigest })

	srv := httptest.NewServer(NewHandler(Options{WebDir: dir}))
	t.Cleanup(srv.Close)
	return srv, dir
}

type response struct {
	status      int
	contentType string
	body        string
}

func do(t *testing.T, method, url, body string) response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return response{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: sb.String()}
}

func assertJSONError(t *testing.T, r response, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d want %d; body=%s", r.status, status, r.body)
	}
	if !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("content-type %q should be JSON", r.contentType)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.body), &e); err != nil || e.Error == "" {
		t.Fatalf("body %q should be {\"error\":...}: %v", r.body, err)
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	r := do(t, http.MethodGet, srv.URL+"/healthz", "")
	if r.status != 200 || r.body != `{"status":"ok"}` || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("unexpected healthz response %+v", r)
	}
}

func TestRegisterValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	tests := []struct {
		name string
		body string
	}{
		{"invalid mac", `{"mac":"nope","hostname":"x"}`},
		{"bad os", `{"mac":"aa:bb:cc:dd:ee:01","os":"windows"}`},
		{"traversal ignition", `{"mac":"aa:bb:cc:dd:ee:01","ignitionFile":"../../etc/passwd"}`},
		{"absolute ignition", `{"mac":"aa:bb:cc:dd:ee:01","ignitionFile":"/etc/passwd"}`},
		{"malformed json", `{"mac":`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertJSONError(t, do(t, http.MethodPost, srv.URL+"/register", tc.body), http.StatusBadRequest)
		})
	}
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/register", ""), http.StatusMethodNotAllowed)
	if len(hardware.Snapshot().Hosts) != 0 {
		t.Fatal("no host should have been registered")
	}
}

func TestRegisterUnregisterAndHosts(t *testing.T) {
	srv, _ := newTestServer(t)

	r := do(t, http.MethodGet, srv.URL+"/hosts?mac=aa:bb:cc:dd:ee:01", "")
	assertJSONError(t, r, http.StatusNotFound)
	if len(hardware.Snapshot().UnknownHosts) != 0 {
		t.Fatal("/hosts lookup must not create unknownHosts entries")
	}
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/hosts", ""), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/hosts?mac=zz", ""), http.StatusBadRequest)

	r = do(t, http.MethodPost, srv.URL+"/register", `{"mac":"AA-BB-CC-DD-EE-01","hostname":"node1","os":"flatcar","ignitionFile":"./config/ignition.yaml"}`)
	if r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
	var reg struct {
		Status string        `json:"status"`
		Host   hardware.Host `json:"host"`
	}
	if err := json.Unmarshal([]byte(r.body), &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Status != "ok" || reg.Host.MAC != "aa:bb:cc:dd:ee:01" || reg.Host.IgnitionFile != "config/ignition.yaml" {
		t.Fatalf("unexpected register response %+v", reg)
	}

	r = do(t, http.MethodGet, srv.URL+"/hosts?mac=AA:BB:CC:DD:EE:01", "")
	if r.status != 200 || !strings.Contains(r.body, `"hostname":"node1"`) {
		t.Fatalf("hosts: %+v", r)
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.json", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("booty.json: %+v", r)
	}
	var data hardware.BootyData
	if err := json.Unmarshal([]byte(r.body), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Hosts) != 1 || data.UnknownHosts == nil {
		t.Fatalf("unexpected booty.json %+v", data)
	}

	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/unregister", `{"mac":"aa:bb:cc:dd:ee:99"}`), http.StatusNotFound)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/unregister", `{"mac":"garbage"}`), http.StatusBadRequest)
	r = do(t, http.MethodPost, srv.URL+"/unregister", `{"mac":"aa:bb:cc:dd:ee:01"}`)
	if r.status != 200 || r.body != `{"status":"ok"}` {
		t.Fatalf("unregister: %+v", r)
	}
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/hosts?mac=aa:bb:cc:dd:ee:01", ""), http.StatusNotFound)
}

func TestIPXEAndIgnitionFlow(t *testing.T) {
	srv, _ := newTestServer(t)

	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=bogus", ""), http.StatusBadRequest)

	r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:ff", "")
	if r.status != 200 || !strings.Contains(r.body, "Unknown Host") || !strings.HasPrefix(r.contentType, "text/plain") {
		t.Fatalf("unknown ipxe: %+v", r)
	}
	if u := hardware.Snapshot().UnknownHosts["aa:bb:cc:dd:ee:ff"]; u == nil || u.Count != 1 {
		t.Fatalf("unknown host should be observed once, got %+v", u)
	}

	r = do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:ff","hostname":"node1","os":"ublue","doInstall":true,"ostreeImage":"ghcr.io/ublue-os/bazzite:stable"}`)
	if r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
	if _, still := hardware.Snapshot().UnknownHosts["aa:bb:cc:dd:ee:ff"]; still {
		t.Fatal("registering must clear the unknown-host entry")
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=AA:BB:CC:DD:EE:FF", "")
	if r.status != 200 || !strings.Contains(r.body, "set menu-default install") || !strings.Contains(r.body, "set OSTREE_IMAGE ghcr.io/ublue-os/bazzite:stable") {
		t.Fatalf("ublue ipxe: %+v", r)
	}
	if strings.Contains(r.body, "[[") {
		t.Fatalf("placeholders left: %s", r.body)
	}
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:ff"); !h.DoInstall {
		t.Fatal("doInstall must not flip on the iPXE fetch")
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:ff", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("ignition: %+v", r)
	}
	var ign map[string]any
	if err := json.Unmarshal([]byte(r.body), &ign); err != nil {
		t.Fatalf("ignition output is not JSON: %v\n%s", err, r.body)
	}
	if !strings.Contains(r.body, "node1") {
		t.Fatalf("hostname not templated into ignition:\n%s", r.body)
	}
	h, _ := hardware.Get("aa:bb:cc:dd:ee:ff")
	if h.DoInstall {
		t.Fatal("doInstall should flip to false after the ignition fetch")
	}
	if h.Booted == "" || h.IP != "127.0.0.1" {
		t.Fatalf("booted/ip should be recorded, got %+v", h)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:00", "")
	if r.status != 200 || !strings.Contains(r.body, "booty-brig-reboot.service") {
		t.Fatalf("unknown host should get the brig config: %+v", r)
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe", "")
	if r.status != 200 || !strings.Contains(r.body, "Unknown Host") {
		t.Fatalf("ARP fallback should identify 02:00:00:00:aa:aa as unknown: %+v", r)
	}
	if _, ok := hardware.Snapshot().UnknownHosts["02:00:00:00:aa:aa"]; !ok {
		t.Fatal("ARP-identified unknown host should be observed")
	}
}

func TestIgnitionBadTemplateIs500(t *testing.T) {
	srv, _ := newTestServer(t)
	r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:01","ignitionFile":"config/missing.yaml"}`)
	if r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:01", "")
	assertJSONError(t, r, http.StatusInternalServerError)
	if strings.Contains(r.body, "missing.yaml") {
		t.Fatalf("error body must not leak paths: %s", r.body)
	}
}

func TestFlatcarPin(t *testing.T) {
	srv, _ := newTestServer(t)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/flatcar/pin", `{"version":"latest"}`), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/flatcar/pin", `{"version":"1.2"}`), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodDelete, srv.URL+"/flatcar/pin", ""), http.StatusMethodNotAllowed)

	r := do(t, http.MethodGet, srv.URL+"/flatcar/pin", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("pin GET: %+v", r)
	}
	var pin struct {
		Pinned  bool   `json:"pinned"`
		Version string `json:"version"`
		Current string `json:"current"`
	}
	if err := json.Unmarshal([]byte(r.body), &pin); err != nil {
		t.Fatal(err)
	}
}

func TestInfoAndVersion(t *testing.T) {
	srv, _ := newTestServer(t)
	r := do(t, http.MethodGet, srv.URL+"/info", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("info: %+v", r)
	}
	var info map[string]map[string]string
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"flatcar", "coreos", "booty"} {
		if _, ok := info[key]; !ok {
			t.Fatalf("info missing %q: %s", key, r.body)
		}
	}
	if _, ok := info["flatcar"]["pinnedVersion"]; !ok {
		t.Fatalf("info.flatcar missing pinnedVersion: %s", r.body)
	}

	r = do(t, http.MethodGet, srv.URL+"/version.json", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") || !strings.Contains(r.body, `"flatcar"`) {
		t.Fatalf("version.json: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/version.txt", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "text/plain") || !strings.HasPrefix(r.body, "FLATCAR_VERSION=") || !strings.Contains(r.body, "\nCOREOS_VERSION=") {
		t.Fatalf("version.txt: %+v", r)
	}
}

func TestDataHandler(t *testing.T) {
	srv, dir := newTestServer(t)
	if err := os.WriteFile(filepath.Join(dir, "kernel.bin"), []byte("KERNEL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("kernel.bin", filepath.Join(dir, "linked.bin")); err != nil {
		t.Fatal(err)
	}

	for _, denied := range []string{
		"/data/hardware.json",
		"/data/flatcar_pin.txt",
		"/data/kernel.tmp",
		"/data/registry/blobs/x",
		"/data/registry/",
		"/data/",
		"/data/config/",
		"/data/config",
		"/data/../hardware.json",
		"/data/config/../hardware.json",
	} {
		r := do(t, http.MethodGet, srv.URL+denied, "")
		if r.status != http.StatusNotFound {
			t.Errorf("%s: status %d want 404 (body=%q)", denied, r.status, r.body)
		}
	}

	for path, want := range map[string]string{
		"/data/kernel.bin":     "KERNEL",
		"/data/linked.bin":     "KERNEL",
		"/data/config/join.sh": "#!/bin/sh\n",
	} {
		r := do(t, http.MethodGet, srv.URL+path, "")
		if r.status != 200 || r.body != want {
			t.Errorf("%s: %+v", path, r)
		}
	}
}
