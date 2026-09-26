package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestRegisterValidatesRole(t *testing.T) {
	srv, _ := newTestServer(t)
	r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp","os":"flatcar","role":"master"}`)
	assertJSONError(t, r, http.StatusBadRequest)
	if !strings.Contains(r.body, "invalid role") {
		t.Fatalf("error must say invalid role: %s", r.body)
	}
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp","os":"flatcar","role":" control-plane "}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"flatcar","role":"worker"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:03","hostname":"w2","os":"coreos"}`)

	r = do(t, http.MethodGet, srv.URL+"/hosts?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || !strings.Contains(r.body, `"role":"control-plane"`) {
		t.Fatalf("/hosts must carry the trimmed role: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/hosts?mac=aa:bb:cc:dd:ee:03", "")
	if r.status != 200 || strings.Contains(r.body, `"role"`) {
		t.Fatalf("a role-less host must not gain a role field: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/booty.json", "")
	if r.status != 200 || !strings.Contains(r.body, `"role":"control-plane"`) || !strings.Contains(r.body, `"role":"worker"`) {
		t.Fatalf("/booty.json must carry roles: %+v", r)
	}
}

func TestClusterEndpointExternalDefaults(t *testing.T) {
	srv, _ := newTestServer(t)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"desk","os":"bluefin","role":"control-plane"}`)

	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/cluster", ""), http.StatusMethodNotAllowed)
	r := do(t, http.MethodGet, srv.URL+"/cluster", "")
	if r.status != 200 {
		t.Fatalf("%+v", r)
	}
	var resp clusterResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Distribution != "kubeadm" || resp.ControlPlane != "external" || resp.CNI != "cilium" || resp.Ready || resp.CAFingerprint != "" || resp.Endpoint != "" {
		t.Fatalf("external defaults: %+v", resp)
	}
	if len(resp.Hosts) != 2 || resp.Hosts[0].MAC != "aa:bb:cc:dd:ee:01" || resp.Hosts[0].Role != "control-plane" || resp.Hosts[1].Role != "worker" || resp.Hosts[1].OS != "flatcar" {
		t.Fatalf("hosts: %+v", resp.Hosts)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0] != "host aa:bb:cc:dd:ee:01 (bluefin) unsupported under kubeadm" {
		t.Fatalf("warnings: %v", resp.Warnings)
	}
	if strings.Contains(r.body, "PRIVATE KEY") || strings.Contains(r.body, "BEGIN CERTIFICATE") {
		t.Fatal("/cluster must never carry key material")
	}
}

func TestClusterEndpointManaged(t *testing.T) {
	_, dir := newTestServer(t)
	viper.Set(config.ControlPlane, "managed")
	viper.Set(config.ClusterDistribution, "k0s")
	m, err := cluster.New(cluster.FromConfig())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(Options{WebDir: dir, BootFiles: testBootFiles, Cluster: m}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { setClusterManager(nil) })

	r := do(t, http.MethodGet, srv.URL+"/cluster", "")
	var resp clusterResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ControlPlane != "managed" || resp.Distribution != "k0s" || resp.CAFingerprint != m.PKI.Fingerprint() || !strings.HasPrefix(resp.CAFingerprint, "sha256:") {
		t.Fatalf("managed: %+v", resp)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0] != "no control-plane host registered" || resp.Endpoint != "" {
		t.Fatalf("no CP yet: %+v", resp)
	}
	if strings.Contains(r.body, "PRIVATE KEY") || strings.Contains(r.body, "BEGIN CERTIFICATE") {
		t.Fatal("/cluster must never carry key material")
	}

	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp","os":"bluefin","role":"control-plane","ip":"192.168.1.30"}`)
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Endpoint != "192.168.1.30" || len(resp.Warnings) != 0 {
		t.Fatalf("single CP resolves the endpoint: %+v", resp)
	}

	viper.Set(config.ControlPlaneEndpt, "vip.example.org:6443")
	m.Settings.Endpoint = "vip.example.org:6443"
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	if !strings.Contains(r.body, `"endpoint":"vip.example.org:6443"`) {
		t.Fatalf("flag endpoint wins: %s", r.body)
	}

	r = do(t, http.MethodGet, srv.URL+"/config", "")
	if r.status != 200 || strings.Contains(r.body, "PRIVATE KEY") {
		t.Fatalf("/config must not leak keys: %+v", r)
	}
	for _, want := range []string{`"key":"clusterDistribution","value":"k0s"`, `"key":"controlPlane","value":"managed"`, `"key":"controlPlaneEndpoint","value":"vip.example.org:6443"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("/config missing %s", want)
		}
	}
}

func TestClusterDirIsNeverServed(t *testing.T) {
	srv, dir := newTestServer(t)
	pkiDir := filepath.Join(dir, "cluster", "pki")
	if err := os.MkdirAll(pkiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cluster/pki/ca.key", "cluster/pki/ca.crt", "cluster/tokens.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("-----BEGIN RSA PRIVATE KEY-----\nsecret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/data/cluster/pki/ca.key", "/data/cluster/pki/ca.crt", "/data/cluster/tokens.json", "/data/cluster", "/data/cluster/", "/data/./cluster/pki/ca.key", "/data/config/../cluster/pki/ca.key"} {
		r := do(t, http.MethodGet, srv.URL+path, "")
		if r.status != http.StatusNotFound || strings.Contains(r.body, "PRIVATE KEY") {
			t.Errorf("%s: status %d body %q", path, r.status, r.body)
		}
	}
	r := do(t, http.MethodGet, srv.URL+"/config/template?name=cluster/tokens.yaml", "")
	assertJSONError(t, r, http.StatusBadRequest)
	if !strings.Contains(r.body, "not a template") {
		t.Fatalf("template names under cluster/ must be rejected: %s", r.body)
	}
	if !deniedDataPath("cluster/pki/etcd/ca.key") || deniedDataPath("clusters/foo.yaml") {
		t.Fatal("deniedDataPath must cover exactly the cluster/ tree")
	}
}

func TestConfigShowsClusterPathsUnredacted(t *testing.T) {
	srv, dir := newTestServer(t)
	viper.Set(config.ClusterCADir, filepath.Join(dir, "byo"))
	viper.Set(config.K0sTokenFile, filepath.Join(dir, "k0s.token"))
	viper.Set(config.Kubeconfig, filepath.Join(dir, "kubeconfig"))
	r := do(t, http.MethodGet, srv.URL+"/config", "")
	var resp configResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	seen := map[string]settingEntry{}
	for _, s := range resp.Settings {
		seen[s.Key] = s
	}
	for key, want := range map[string]string{"clusterCADir": filepath.Join(dir, "byo"), "k0sTokenFile": filepath.Join(dir, "k0s.token"), "kubeconfig": filepath.Join(dir, "kubeconfig"), "podCIDR": "10.244.0.0/16", "serviceCIDR": "10.96.0.0/12", "cni": "cilium", "cniRelease": ""} {
		s, ok := seen[key]
		if !ok {
			t.Errorf("setting %s missing from /config", key)
			continue
		}
		if s.Redacted || s.Value != want {
			t.Errorf("%s = %+v, want unredacted %q", key, s, want)
		}
	}
}
