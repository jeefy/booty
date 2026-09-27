package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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
	viper.Set(config.HttpPort, 18099)
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.DoInstallClearOn, config.ClearOnIgnition)
	viper.Set(config.Builtin, config.DefaultBuiltin)
	viper.Set(config.SSHAuthorizedKeysFl, "")
	viper.Set(config.SSHAuthorizedKeys, []string{})
	viper.Set(config.AutoRegister, "")
	viper.Set(config.HostnameTemplate, config.DefaultHostnameTemplate)
	viper.Set(config.Profile, "")
	viper.Set(config.KubeadmJoin, config.KubeadmJoinStatic)
	viper.Set(config.JoinString, "")
	viper.Set(config.JoinStringFile, "")
	viper.Set(config.JoinTokenTTL, time.Hour)
	viper.Set(config.K8sVersion, config.DefaultK8sVersion)
	viper.Set(config.CNIVersion, config.DefaultCNIVersion)
	viper.Set(config.CrictlVersion, "")
	viper.Set(config.ContainerdDisk, "")
	viper.Set(config.KubeletUnitsURL, config.DefaultKubeletUnitsURL)
	viper.Set(config.ClusterDistribution, config.DefaultClusterDistribution)
	viper.Set(config.ControlPlane, config.DefaultControlPlane)
	viper.Set(config.ControlPlaneEndpt, "")
	viper.Set(config.ClusterCADir, "")
	viper.Set(config.CNI, config.DefaultCNI)
	viper.Set(config.CNIRelease, "")
	viper.Set(config.PodCIDR, config.DefaultPodCIDR)
	viper.Set(config.ServiceCIDR, config.DefaultServiceCIDR)
	viper.Set(config.K0sTokenFile, "")
	viper.Set(config.K0sVersion, config.DefaultK0sVersion)
	viper.Set(config.Kubeconfig, "")

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

	origARP, origDigest, origPull := arpLookup, digestLookup, pullImage
	arpLookup = func(ip net.IP) (net.HardwareAddr, error) {
		return net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0xaa, 0xaa}, nil
	}
	digestLookup = func(string, ...crane.Option) (string, error) { return "", os.ErrNotExist }
	pullImage = func(string) {}
	t.Cleanup(func() { arpLookup, digestLookup, pullImage = origARP, origDigest, origPull })

	srv := httptest.NewServer(NewHandler(Options{WebDir: dir, BootFiles: testBootFiles}))
	t.Cleanup(srv.Close)
	return srv, dir
}

var testBootFiles = fstest.MapFS{
	"undionly.kpxe": {Data: []byte("BIOS-IPXE")},
	"ipxe.efi":      {Data: []byte("EFI-IPXE")},
	"snponly.efi":   {Data: []byte("SNP-IPXE")},
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
		{"ublue is gone", `{"mac":"aa:bb:cc:dd:ee:01","os":"ublue"}`},
		{"relative install disk", `{"mac":"aa:bb:cc:dd:ee:01","os":"bluefin","installDisk":"sda"}`},
		{"install disk with whitespace", `{"mac":"aa:bb:cc:dd:ee:01","os":"bluefin","installDisk":"/dev/sda inst.x=1"}`},
		{"install disk traversal", `{"mac":"aa:bb:cc:dd:ee:01","os":"bluefin","installDisk":"/dev/../etc"}`},
		{"bad hostname", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"Bad_Name!"}`},
		{"leading hyphen hostname", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"-node"}`},
		{"too long hostname", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"` + strings.Repeat("a", 254) + `"}`},
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

	for _, body := range []string{
		`{"mac":"aa:bb:cc:dd:ee:01","hostname":"Node-1.Example.COM"}`,
		`{"mac":"aa:bb:cc:dd:ee:02","hostname":""}`,
		`{"mac":"aa:bb:cc:dd:ee:03"}`,
		`{"mac":"aa:bb:cc:dd:ee:04","os":"bluefin","installDisk":" /dev/nvme0n1 "}`,
	} {
		if r := do(t, http.MethodPost, srv.URL+"/register", body); r.status != 200 {
			t.Fatalf("register %s: %+v", body, r)
		}
	}
}

func TestAutoRegister(t *testing.T) {
	srv, _ := newTestServer(t)
	viper.Set(config.AutoRegister, "flatcar")

	r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=AA:BB:CC:DD:EE:42", "")
	if r.status != 200 || strings.Contains(r.body, "Unknown Host") || !strings.Contains(r.body, "flatcar_production_pxe.vmlinuz") {
		t.Fatalf("auto-registered host must get the flatcar script: %+v", r)
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.json", "")
	var data hardware.BootyData
	if err := json.Unmarshal([]byte(r.body), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.UnknownHosts) != 0 {
		t.Fatalf("auto-registered host must not be observed as unknown: %+v", data.UnknownHosts)
	}
	h := data.Hosts["aa:bb:cc:dd:ee:42"]
	if h == nil || h.Hostname != "node-ddee42" || h.OS != "flatcar" || h.IP != "127.0.0.1" || h.DoInstall || h.Booted != "" {
		t.Fatalf("unexpected auto-registered host %+v", h)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:43", "")
	if r.status != 200 || strings.Contains(r.body, "booty-brig-reboot.service") || !strings.Contains(r.body, "/ignition/user.json?mac=aa%3Abb%3Acc%3Add%3Aee%3A43") {
		t.Fatalf("auto-registered host must get the merge wrapper, not the brig: %+v", r)
	}
	h, _ = hardware.Get("aa:bb:cc:dd:ee:43")
	if h == nil || h.Hostname != "node-ddee43" || h.Booted == "" {
		t.Fatalf("ignition fetch must auto-register and record the boot: %+v", h)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition/user.json?mac=aa:bb:cc:dd:ee:44", "")
	assertJSONError(t, r, http.StatusNotFound)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/hosts?mac=aa:bb:cc:dd:ee:44", ""), http.StatusNotFound)
	if _, ok := hardware.Get("aa:bb:cc:dd:ee:44"); ok {
		t.Fatal("child and /hosts lookups must never auto-register")
	}

	viper.Set(config.HostnameTemplate, "pxe-{{ .MACFlat }}.lab")
	do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:45", "")
	if h, _ = hardware.Get("aa:bb:cc:dd:ee:45"); h == nil || h.Hostname != "pxe-aabbccddee45.lab" {
		t.Fatalf("hostname template must be honoured: %+v", h)
	}

	viper.Set(config.HostnameTemplate, "static-name")
	do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:46", "")
	do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:47", "")
	a, _ := hardware.Get("aa:bb:cc:dd:ee:46")
	b, _ := hardware.Get("aa:bb:cc:dd:ee:47")
	if a == nil || b == nil || a.Hostname != "static-name" || b.Hostname != "static-name" {
		t.Fatalf("hostname collisions are warned about but still registered: %+v %+v", a, b)
	}

	viper.Set(config.HostnameTemplate, "{{ .Nope }}")
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:48", "")
	if !strings.Contains(r.body, "Unknown Host") {
		t.Fatalf("a broken template must fall back to the unknown-host menu: %+v", r)
	}
	if _, ok := hardware.Snapshot().UnknownHosts["aa:bb:cc:dd:ee:48"]; !ok {
		t.Fatal("a broken template must still observe the unknown host")
	}
}

func TestAutoRegisterOffKeepsBrig(t *testing.T) {
	srv, _ := newTestServer(t)

	r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:42", "")
	if !strings.Contains(r.body, "Unknown Host") {
		t.Fatalf("without --autoRegister unknown hosts get the menu: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:42", "")
	if !strings.Contains(r.body, "booty-brig-reboot.service") {
		t.Fatalf("without --autoRegister unknown hosts get the brig: %+v", r)
	}
	data := hardware.Snapshot()
	if len(data.Hosts) != 0 || data.UnknownHosts["aa:bb:cc:dd:ee:42"] == nil || data.UnknownHosts["aa:bb:cc:dd:ee:42"].Count != 2 {
		t.Fatalf("unknown host must be observed, not registered: %+v", data)
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

	r = do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:ff","hostname":"node1","os":"coreos","doInstall":true,"ostreeImage":"ghcr.io/ublue-os/bazzite:stable"}`)
	if r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
	if _, still := hardware.Snapshot().UnknownHosts["aa:bb:cc:dd:ee:ff"]; still {
		t.Fatal("registering must clear the unknown-host entry")
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=AA:BB:CC:DD:EE:FF", "")
	if r.status != 200 || !strings.Contains(r.body, "fedora-coreos-${VERSION}-live-kernel") || !strings.Contains(r.body, "set OSTREE_IMAGE ghcr.io/ublue-os/bazzite:stable") {
		t.Fatalf("coreos ipxe: %+v", r)
	}
	if strings.Contains(r.body, "[[") {
		t.Fatalf("placeholders left: %s", r.body)
	}
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:ff"); !h.DoInstall {
		t.Fatal("doInstall must not flip on the iPXE fetch")
	}

	digestLookup = func(ref string, _ ...crane.Option) (string, error) {
		if ref != "127.0.0.1:18099/ghcr.io/ublue-os/bazzite:stable" {
			t.Errorf("digest lookup must target the loopback registry, got %q", ref)
		}
		return "sha256:abc", nil
	}
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:ff", "")
	if !strings.Contains(r.body, "set OSTREE_IMAGE 192.168.1.10:8080/ghcr.io/ublue-os/bazzite:stable") {
		t.Fatalf("cached image must be rendered with the client-facing registry:\n%s", r.body)
	}
	digestLookup = func(string, ...crane.Option) (string, error) { return "", os.ErrNotExist }

	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:ff&preview=1", "")
	if r.status != 200 || !strings.Contains(r.body, "/ignition/user.json?mac=aa%3Abb%3Acc%3Add%3Aee%3Aff") {
		t.Fatalf("ignition preview should be the merge wrapper: %+v", r)
	}
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:ff"); !h.DoInstall || h.Booted != "" {
		t.Fatalf("a preview must not flip doInstall or stamp booted, got %+v", h)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:ff", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("ignition: %+v", r)
	}
	var wrapper ignitionWrapper
	if err := json.Unmarshal([]byte(r.body), &wrapper); err != nil {
		t.Fatalf("ignition output is not JSON: %v\n%s", err, r.body)
	}
	if wrapper.Ignition.Version != "3.4.0" || len(wrapper.Ignition.Config.Merge) != 2 {
		t.Fatalf("wrapper must carry the user config's version and two merge entries:\n%s", r.body)
	}
	if wrapper.Ignition.Config.Merge[0].Source != "http://192.168.1.10:8080/ignition/builtin.json?mac=aa%3Abb%3Acc%3Add%3Aee%3Aff" ||
		wrapper.Ignition.Config.Merge[1].Source != "http://192.168.1.10:8080/ignition/user.json?mac=aa%3Abb%3Acc%3Add%3Aee%3Aff" {
		t.Fatalf("merge order must be builtin then user, on the client-facing address:\n%s", r.body)
	}
	h, _ := hardware.Get("aa:bb:cc:dd:ee:ff")
	if h.DoInstall {
		t.Fatal("doInstall should flip to false after the ignition fetch")
	}
	if h.Booted == "" || h.IP != "127.0.0.1" {
		t.Fatalf("booted/ip should be recorded, got %+v", h)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition/user.json?mac=aa:bb:cc:dd:ee:ff", "")
	if r.status != 200 || !strings.Contains(r.body, "node1") || !strings.Contains(r.body, `"version": "3.4.0"`) {
		t.Fatalf("user child must be the rendered butane: %+v", r)
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

func TestBootedEndpointValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/booted?mac=aa:bb:cc:dd:ee:01", ""), http.StatusMethodNotAllowed)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/booted", ""), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/booted?mac=not-a-mac", ""), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/booted?mac=aa:bb:cc:dd:ee:01", ""), http.StatusNotFound)
	if len(hardware.Snapshot().UnknownHosts) != 0 {
		t.Fatal("/booted must not create unknownHosts entries")
	}
}

func TestDoInstallClearOnBooted(t *testing.T) {
	srv, _ := newTestServer(t)
	viper.Set(config.DoInstallClearOn, config.ClearOnBooted)

	r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:02","hostname":"n2","os":"coreos","doInstall":true}`)
	if r.status != 200 {
		t.Fatalf("register: %+v", r)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:02", "")
	if r.status != 200 {
		t.Fatalf("ignition: %+v", r)
	}
	h, _ := hardware.Get("aa:bb:cc:dd:ee:02")
	if !h.DoInstall {
		t.Fatal("ignition fetch must leave doInstall set when clearing on booted")
	}
	if h.Booted == "" || h.IP != "127.0.0.1" {
		t.Fatalf("ignition fetch must still stamp booted/ip, got %+v", h)
	}
	firstBoot := h.Booted

	r = do(t, http.MethodPost, srv.URL+"/booted?mac=AA-BB-CC-DD-EE-02", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("booted: %+v", r)
	}
	var resp struct {
		Status string        `json:"status"`
		Host   hardware.Host `json:"host"`
	}
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" || resp.Host.MAC != "aa:bb:cc:dd:ee:02" || resp.Host.DoInstall {
		t.Fatalf("unexpected booted response %+v", resp)
	}
	h, _ = hardware.Get("aa:bb:cc:dd:ee:02")
	if h.DoInstall || h.Booted == "" || h.Booted < firstBoot || h.IP != "127.0.0.1" {
		t.Fatalf("POST /booted must clear doInstall and stamp booted/ip, got %+v", h)
	}

	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/booted?mac=aa:bb:cc:dd:ee:99", ""), http.StatusNotFound)
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

func TestInfoSecureBootBlock(t *testing.T) {
	srv, dir := newTestServer(t)
	viper.Set(config.SecureBoot, true)
	viper.Set(config.SecureBootTrusted, "flatcar")
	viper.Set(config.SecureBootIPXEShim, "ipxe-16.1")
	viper.Set(config.SecureBootIPXE, "v2.0.0")
	viper.Set(config.FedoraShimVersion, "16.1-7")
	viper.Set(config.FedoraGrubVersion, "2.12-64.fc44")
	t.Cleanup(func() {
		viper.Set(config.SecureBoot, false)
		viper.Set(config.SecureBootTrusted, "")
	})
	bundle := "ipxe-16.1_v2.0.0_shim-16.1-7_grub-2.12-64.fc44"
	sbDir := filepath.Join(dir, "secureboot")
	if err := os.MkdirAll(filepath.Join(sbDir, bundle, "fedora"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"bundleVersion":"` + bundle + `","files":[{"name":"ipxe-shimx64.efi","sha256":"00"},{"name":"fedora/shimx64.efi","sha256":"00"}]}`
	for name, content := range map[string]string{
		filepath.Join(bundle, "manifest.json"):      manifest,
		filepath.Join(bundle, "ipxe-shimx64.efi"):   "SHIM",
		filepath.Join(bundle, "fedora/shimx64.efi"): "FEDORA",
		"flatcar-ca.json":                           `{"flatcarVersion":"4757.2.0","sha256":"ebb170da","subject":"CN=Flatcar CA","notAfter":"2037-01-19","source":"x"}`,
	} {
		if err := os.WriteFile(filepath.Join(sbDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(bundle, filepath.Join(sbDir, "current")); err != nil {
		t.Fatal(err)
	}

	r := do(t, http.MethodGet, srv.URL+"/info", "")
	var info struct {
		SecureBoot struct {
			Enabled   bool     `json:"enabled"`
			Ready     bool     `json:"ready"`
			Bundle    string   `json:"bundleVersion"`
			Trusted   []string `json:"trusted"`
			BootURL   string   `json:"bootURL"`
			FlatcarCA *struct {
				FlatcarVersion string `json:"flatcarVersion"`
				Sha256         string `json:"sha256"`
				URL            string `json:"url"`
			} `json:"flatcarCA"`
		} `json:"secureBoot"`
	}
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	sb := info.SecureBoot
	if !sb.Enabled || !sb.Ready || sb.Bundle != bundle || sb.BootURL != "http://192.168.1.10:8080/boot/sb" {
		t.Fatalf("secureBoot = %+v", sb)
	}
	if len(sb.Trusted) != 2 || sb.Trusted[0] != "microsoft" || sb.Trusted[1] != "flatcar" {
		t.Errorf("trusted = %v", sb.Trusted)
	}
	if sb.FlatcarCA == nil || sb.FlatcarCA.Sha256 != "ebb170da" || sb.FlatcarCA.FlatcarVersion != "4757.2.0" || sb.FlatcarCA.URL != "http://192.168.1.10:8080/boot/secureboot/flatcar-ca.der" {
		t.Errorf("flatcarCA = %+v", sb.FlatcarCA)
	}

	if err := os.Remove(filepath.Join(sbDir, bundle, "fedora/shimx64.efi")); err != nil {
		t.Fatal(err)
	}
	r = do(t, http.MethodGet, srv.URL+"/info", "")
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	if info.SecureBoot.Ready {
		t.Error("ready must drop when a bundle member is missing")
	}
}

func TestInfoAndVersion(t *testing.T) {
	srv, _ := newTestServer(t)
	r := do(t, http.MethodGet, srv.URL+"/info", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("info: %+v", r)
	}
	var info map[string]map[string]any
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"flatcar", "coreos", "bluefin", "booty", "fleet", "secureBoot"} {
		if _, ok := info[key]; !ok {
			t.Fatalf("info missing %q: %s", key, r.body)
		}
	}
	if sb := info["secureBoot"]; sb["enabled"] != false || sb["ready"] != false || sb["bundleVersion"] != "" || sb["flatcarCA"] != nil {
		t.Fatalf("info.secureBoot must be off by default: %v", sb)
	}
	if _, ok := info["flatcar"]["pinnedVersion"]; !ok {
		t.Fatalf("info.flatcar missing pinnedVersion: %s", r.body)
	}
	if _, ok := info["bluefin"]["pinnedVersion"]; !ok || info["bluefin"]["version"] == nil {
		t.Fatalf("info.bluefin must carry version and pinnedVersion: %s", r.body)
	}
	if info["fleet"]["hosts"] != float64(0) || info["fleet"]["pendingReboots"] != float64(0) {
		t.Fatalf("info.fleet should count zero hosts: %s", r.body)
	}

	r = do(t, http.MethodGet, srv.URL+"/version.json", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") || !strings.Contains(r.body, `"flatcar"`) || !strings.Contains(r.body, `"bluefin"`) {
		t.Fatalf("version.json: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/version.txt", "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "text/plain") || !strings.HasPrefix(r.body, "FLATCAR_VERSION=") || !strings.Contains(r.body, "\nCOREOS_VERSION=") || !strings.Contains(r.body, "\nBLUEFIN_VERSION=") {
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

func TestBootHandler(t *testing.T) {
	srv, _ := newTestServer(t)

	for name, want := range map[string][2]string{
		"undionly.kpxe": {"BIOS-IPXE", "application/octet-stream"},
		"ipxe.efi":      {"EFI-IPXE", "application/efi"},
		"snponly.efi":   {"SNP-IPXE", "application/efi"},
	} {
		r := do(t, http.MethodGet, srv.URL+"/boot/"+name, "")
		if r.status != http.StatusOK || r.body != want[0] {
			t.Errorf("/boot/%s: status %d body %q", name, r.status, r.body)
		}
		if r.contentType != want[1] {
			t.Errorf("/boot/%s: content-type %q, want %q", name, r.contentType, want[1])
		}
	}

	head := doHead(t, srv.URL+"/boot/ipxe.efi")
	if head.StatusCode != http.StatusOK || head.ContentLength != int64(len("EFI-IPXE")) {
		t.Errorf("HEAD /boot/ipxe.efi: status %d length %d", head.StatusCode, head.ContentLength)
	}
	if got := head.Header.Get("Content-Type"); got != "application/efi" {
		t.Errorf("HEAD content-type %q", got)
	}
	if got := head.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("HEAD cache-control %q", got)
	}

	for _, p := range []string{"/boot/", "/boot/pxelinux.0", "/boot/../boot/ipxe.efi/x", "/boot/ipxe.efi/", "/boot/booty.ipxe", "/boot/sb/ipxe.efi", "/boot/secureboot/flatcar-ca.der", "/boot/sb/", "/boot/sb/../ipxe.efi", "/boot/../ignition.json", "/boot/sb/../../config/ignition.yaml"} {
		assertJSONError(t, do(t, http.MethodGet, srv.URL+p, ""), http.StatusNotFound)
	}
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/boot/ipxe.efi", ""), http.StatusMethodNotAllowed)

	noFiles := httptest.NewServer(NewHandler(Options{WebDir: t.TempDir()}))
	t.Cleanup(noFiles.Close)
	assertJSONError(t, do(t, http.MethodGet, noFiles.URL+"/boot/ipxe.efi", ""), http.StatusNotFound)
}

// doHead issues a HEAD without following redirects, so a 301 from the mux
// would surface as a failure instead of being followed.
func doHead(t *testing.T, url string) *http.Response {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	return resp
}

func TestBootHandlerCollapsesSlashesWithoutRedirect(t *testing.T) {
	srv, _ := newTestServer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"/boot//ipxe.efi", "/boot///ipxe.efi", "/boot/./ipxe.efi"} {
		resp, err := client.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
		if resp.StatusCode != http.StatusOK || string(body) != "EFI-IPXE" {
			t.Errorf("GET %s: status %d body %q (location %q)", p, resp.StatusCode, body, resp.Header.Get("Location"))
		}
	}
	if head := doHead(t, srv.URL+"/boot//ipxe.efi"); head.StatusCode != http.StatusOK || head.ContentLength != int64(len("EFI-IPXE")) {
		t.Errorf("HEAD /boot//ipxe.efi: status %d length %d", head.StatusCode, head.ContentLength)
	}
}

func TestBootHandlerServesSecureBootBundle(t *testing.T) {
	srv, dir := newTestServer(t)
	bundle := filepath.Join(dir, "secureboot", "bundle-1")
	for name, content := range map[string]string{
		"ipxe-shimx64.efi":    "SHIM",
		"ipxe.efi":            "SB-IPXE",
		"fedora/shimx64.efi":  "FEDORA-SHIM",
		"fedora/grubx64.efi":  "FEDORA-GRUB",
		"manifest.json":       "{}",
		"../flatcar-ca.der":   "DER",
		"../flatcar-ca.pem":   "PEM",
		"../bundle-0/old.efi": "OLD",
	} {
		full := filepath.Join(bundle, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("bundle-1", filepath.Join(dir, "secureboot", "current")); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string][2]string{
		"/boot/sb/ipxe-shimx64.efi":           {"SHIM", "application/efi"},
		"/boot/sb//ipxe.efi":                  {"SB-IPXE", "application/efi"},
		"/boot/sb/fedora/shimx64.efi":         {"FEDORA-SHIM", "application/efi"},
		"/boot/secureboot/fedora/shimx64.efi": {"FEDORA-SHIM", "application/efi"},
		"/boot/secureboot/fedora/grubx64.efi": {"FEDORA-GRUB", "application/efi"},
		"/boot/secureboot/ipxe-shimx64.efi":   {"SHIM", "application/efi"},
		"/boot/secureboot/flatcar-ca.der":     {"DER", "application/octet-stream"},
		"/boot/secureboot/flatcar-ca.pem":     {"PEM", "application/octet-stream"},
		"/boot/secureboot//flatcar-ca.der":    {"DER", "application/octet-stream"},
		"/boot/secureboot/manifest.json":      {"{}", "application/octet-stream"},
		"/boot/ipxe.efi":                      {"EFI-IPXE", "application/efi"},
	} {
		r := do(t, http.MethodGet, srv.URL+p, "")
		if r.status != http.StatusOK || r.body != want[0] || r.contentType != want[1] {
			t.Errorf("GET %s: %+v, want %v", p, r, want)
		}
	}
	head := doHead(t, srv.URL+"/boot/sb/ipxe-shimx64.efi")
	if head.StatusCode != http.StatusOK || head.ContentLength != 4 || head.Header.Get("Content-Type") != "application/efi" {
		t.Errorf("HEAD /boot/sb/ipxe-shimx64.efi: status %d length %d type %q", head.StatusCode, head.ContentLength, head.Header.Get("Content-Type"))
	}

	for _, p := range []string{"/boot/sb/", "/boot/sb/fedora", "/boot/sb/fedora/", "/boot/sb/missing.efi", "/boot/secureboot/bundle-0/old.efi", "/boot/secureboot/current/ipxe.efi/", "/boot/sb/../../config/ignition.yaml", "/boot/sb/..%2F..%2Fhardware.json", "/boot/secureboot/../hardware.json", "/boot/secureboot/flatcar-ca.txt"} {
		assertJSONError(t, do(t, http.MethodGet, srv.URL+p, ""), http.StatusNotFound)
	}
}

func TestBootSecureBootAutoexecScript(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/boot/sb/autoexec.ipxe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body := string(raw)
	for _, want := range []string{"#!ipxe", "dhcp || goto retry", "/booty.ipxe?mac=${mac}", "shell"} {
		if !strings.Contains(body, want) {
			t.Errorf("autoexec.ipxe missing %q:\n%s", want, body)
		}
	}
	if resp.Header.Get("Content-Length") != strconv.Itoa(len(raw)) {
		t.Errorf("Content-Length %q for %d bytes", resp.Header.Get("Content-Length"), len(raw))
	}
}
