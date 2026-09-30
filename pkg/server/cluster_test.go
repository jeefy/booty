package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/cluster/k8s/fake"
	"github.com/jeefy/booty/pkg/cluster/pki"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/kubeadm"
	"github.com/jeefy/booty/pkg/state"
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
	if resp.Source != clusterSourceExternal || resp.Connected || resp.Nodes != 0 || resp.APIServer != "" {
		t.Fatalf("external without a client: %+v", resp)
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
	if resp.Source != clusterSourceManaged || resp.Connected || resp.Ready || resp.Nodes != 0 || resp.APIServer != "" {
		t.Fatalf("managed without a client: %+v", resp)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0] != "no control-plane host registered" || resp.Endpoint != "" {
		t.Fatalf("no CP yet: %+v", resp)
	}
	if strings.Contains(r.body, "PRIVATE KEY") || strings.Contains(r.body, "BEGIN CERTIFICATE") {
		t.Fatal("/cluster must never carry key material")
	}

	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp","os":"bluefin","role":"control-plane","ip":"192.168.1.30","stateDisk":"/dev/sdb"}`)
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

func getCluster(t *testing.T, srvURL string) clusterResponse {
	t.Helper()
	r := do(t, http.MethodGet, srvURL+"/cluster", "")
	if r.status != 200 {
		t.Fatalf("%+v", r)
	}
	var resp clusterResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	if strings.Contains(r.body, "PRIVATE KEY") || strings.Contains(r.body, "BEGIN CERTIFICATE") {
		t.Fatal("/cluster must never carry key material")
	}
	return resp
}

func count(seen []string, want string) int {
	n := 0
	for _, s := range seen {
		if s == want {
			n++
		}
	}
	return n
}

func apiWarnings(warnings []string) []string {
	var out []string
	for _, w := range warnings {
		if strings.HasPrefix(w, "API server ") {
			out = append(out, w)
		}
	}
	return out
}

// TestClusterExternalThroughAutopilot is the homelab shape: an external
// control plane, the autopilot on, Booty reaching the API through its
// client. /cluster reports the live connection and reuses the chooser's
// cached Inspect instead of listing the nodes itself.
func TestClusterExternalThroughAutopilot(t *testing.T) {
	srv, _ := newTestServer(t)
	t.Cleanup(func() { setAutopilot(nil) })
	api, client, apiSrv := fake.New(t)
	api.Load(t, "../cluster/k8s/testdata")
	setAutopilot(&autopilot.Autopilot{
		Settings: autopilot.Settings{Mode: config.AutopilotGuard, Namespace: "kube-system"},
		Client:   client,
		Chooser:  actuator.NewChooser(actuator.Options{Client: client, Namespace: "kube-system"}),
	})
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"flatcar"}`)

	resp := getCluster(t, srv.URL)
	apiURL, _ := url.Parse(apiSrv.URL)
	if !resp.Connected || !resp.Ready || resp.ReadyAt != "" || resp.Nodes != 3 || resp.Source != clusterSourceExternal || resp.ControlPlane != "external" {
		t.Fatalf("external, reachable: %+v", resp)
	}
	if resp.APIServer != apiSrv.URL || resp.Endpoint != apiURL.Host {
		t.Fatalf("endpoint must be the API server's host:port: %+v", resp)
	}
	if want := pki.CertFingerprint(apiSrv.Certificate()); resp.CAFingerprint != want {
		t.Fatalf("caFingerprint %q, want the API server CA %q", resp.CAFingerprint, want)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("a reachable cluster needs no warning: %v", resp.Warnings)
	}

	before := len(api.Seen())
	if again := getCluster(t, srv.URL); again.Nodes != 3 || !again.Connected {
		t.Fatalf("second call: %+v", again)
	}
	if after := len(api.Seen()); after != before {
		t.Fatalf("/cluster must reuse the cached probe, saw %d new requests: %v", after-before, api.Seen()[before:])
	}
	if n := count(api.Seen(), "GET /api/v1/nodes"); n != 1 {
		t.Fatalf("the nodes must be listed once, not %d times: %v", n, api.Seen())
	}
	if got := api.Writes(); len(got) != 0 {
		t.Fatalf("GET /cluster must never write to the cluster: %v", got)
	}

	viper.Set(config.ControlPlaneEndpt, "vip.example.org:6443")
	if resp := getCluster(t, srv.URL); resp.Endpoint != "vip.example.org:6443" || resp.APIServer != apiSrv.URL {
		t.Fatalf("--controlPlaneEndpoint wins over the API server host: %+v", resp)
	}
}

// TestClusterExternalThroughMinter is --autopilot=off --kubeadmJoin=auto:
// the only client is the token minter's, and /cluster still knows whether
// the API answers (one cached GET /version), where it is and which CA
// Booty trusts; the node count stays unknown.
func TestClusterExternalThroughMinter(t *testing.T) {
	srv, _ := newTestServer(t)
	t.Cleanup(func() { setJoinMinter(nil) })
	api, client, apiSrv := fake.New(t)
	setJoinMinter(kubeadm.New(client.API().Config(), time.Hour))

	resp := getCluster(t, srv.URL)
	apiURL, _ := url.Parse(apiSrv.URL)
	if !resp.Connected || !resp.Ready || resp.Nodes != 0 || resp.APIServer != apiSrv.URL || resp.Endpoint != apiURL.Host || resp.Source != clusterSourceExternal {
		t.Fatalf("minter only: %+v", resp)
	}
	if want := pki.CertFingerprint(apiSrv.Certificate()); resp.CAFingerprint != want {
		t.Fatalf("caFingerprint %q, want %q", resp.CAFingerprint, want)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("warnings: %v", resp.Warnings)
	}
	getCluster(t, srv.URL)
	if seen := api.Seen(); len(seen) != 1 || seen[0] != "GET /version" {
		t.Fatalf("two /cluster calls must cost one GET /version: %v", seen)
	}

	api.Fail = http.StatusUnauthorized
	setJoinMinter(kubeadm.New(client.API().Config(), time.Hour))
	resp = getCluster(t, srv.URL)
	if !resp.Connected || len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "API server "+apiSrv.URL+": ") || !strings.Contains(resp.Warnings[0], "401") {
		t.Fatalf("a server that answers 401 is reachable, with the answer as a warning: %+v", resp)
	}
}

func TestClusterExternalUnreachable(t *testing.T) {
	srv, _ := newTestServer(t)
	t.Cleanup(func() { setAutopilot(nil); setJoinMinter(nil) })
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp","os":"flatcar","role":"control-plane","ip":"192.168.1.30"}`)

	dead, err := autopilot.Setup(autopilot.Settings{Mode: config.AutopilotGuard, Kubeconfig: writeBogusKubeconfig(t), HealthWindow: time.Minute, RetryAfter: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	setAutopilot(dead)
	start := time.Now()
	resp := getCluster(t, srv.URL)
	if resp.Connected || resp.Ready || resp.Nodes != 0 || resp.APIServer != "https://127.0.0.1:1" || resp.Endpoint != "127.0.0.1:1" || resp.Source != clusterSourceExternal {
		t.Fatalf("unreachable: %+v", resp)
	}
	if resp.CAFingerprint != "" {
		t.Fatalf("a kubeconfig without a CA yields no fingerprint: %+v", resp)
	}
	if len(resp.Warnings) != 1 || !strings.HasPrefix(resp.Warnings[0], "API server https://127.0.0.1:1 unreachable: ") {
		t.Fatalf("warnings: %v", resp.Warnings)
	}
	if elapsed := time.Since(start); elapsed > clusterProbeTimeout+time.Second {
		t.Fatalf("/cluster took %s against a dead API server", elapsed)
	}

	setAutopilot(nil)
	setJoinMinter(kubeadm.New(kubeadm.KubeConfig{APIServer: "https://127.0.0.1:1", Token: "nope"}, time.Hour))
	resp = getCluster(t, srv.URL)
	if resp.Connected || resp.Ready || resp.APIServer != "https://127.0.0.1:1" || resp.Endpoint != "127.0.0.1:1" || len(resp.Warnings) != 1 {
		t.Fatalf("unreachable through the minter: %+v", resp)
	}
}

// TestClusterManagedWithClient keeps the managed semantics: ready is the
// control-plane host's report, the fingerprint and endpoint are Booty's,
// and the live connection is reported alongside without a warning while
// the cluster is still bootstrapping.
func TestClusterManagedWithClient(t *testing.T) {
	_, dir := newTestServer(t)
	viper.Set(config.ControlPlane, "managed")
	viper.Set(config.KubeadmJoin, config.KubeadmJoinAuto)
	m, err := cluster.New(cluster.FromConfig())
	if err != nil {
		t.Fatal(err)
	}
	api, client, apiSrv := fake.New(t)
	api.Load(t, "../cluster/k8s/testdata")
	pilot := &autopilot.Autopilot{
		Settings: autopilot.Settings{Mode: config.AutopilotGuard, Namespace: "kube-system"},
		Client:   client,
		Chooser:  actuator.NewChooser(actuator.Options{Client: client, Namespace: "kube-system"}),
	}
	srv := httptest.NewServer(NewHandler(Options{WebDir: dir, BootFiles: testBootFiles, Cluster: m, Autopilot: pilot}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { setClusterManager(nil); setAutopilot(nil); state.ResetClusterReady() })
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp","os":"flatcar","role":"control-plane","ip":"192.168.1.30"}`)

	resp := getCluster(t, srv.URL)
	if resp.Source != clusterSourceManaged || !resp.Connected || resp.Nodes != 3 || resp.APIServer != apiSrv.URL {
		t.Fatalf("managed with a client: %+v", resp)
	}
	if resp.Ready || resp.Endpoint != "192.168.1.30" || resp.CAFingerprint != m.PKI.Fingerprint() {
		t.Fatalf("managed facts must stay Booty's: %+v", resp)
	}
	if got := apiWarnings(resp.Warnings); len(got) != 0 {
		t.Fatalf("a connected managed cluster needs no API warning: %v", got)
	}

	pilot.Client = k8s.FromConfig(kubeadm.KubeConfig{APIServer: "https://127.0.0.1:1", Token: "nope"})
	pilot.Chooser = actuator.NewChooser(actuator.Options{Client: pilot.Client})
	resp = getCluster(t, srv.URL)
	if resp.Connected {
		t.Fatalf("dead API: %+v", resp)
	}
	if got := apiWarnings(resp.Warnings); len(got) != 0 {
		t.Fatalf("an unreachable API before the control plane reported ready is expected, not a warning: %v", got)
	}
	if _, err := state.MarkClusterReady("aa:bb:cc:dd:ee:01", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp = getCluster(t, srv.URL)
	if !resp.Ready || resp.Connected || resp.ReadyAt == "" {
		t.Fatalf("ready is the control-plane report even while the API is down: %+v", resp)
	}
	if got := apiWarnings(resp.Warnings); len(got) != 1 || !strings.HasPrefix(got[0], "API server https://127.0.0.1:1 unreachable: ") {
		t.Fatalf("once ready, an unreachable API is a warning: %v", resp.Warnings)
	}
}
