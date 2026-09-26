package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/cni"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/profile"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

// newManagedServer starts a server with --controlPlane=managed
// --controlPlaneEndpoint=10.77.0.10 --controlPlaneDisk=/dev/vda
// --profile=kubeadm-worker and the given --kubeadmJoin mode.
func newManagedServer(t *testing.T, joinMode string) (*httptest.Server, *cluster.Manager) {
	t.Helper()
	_, dir := newTestServer(t)
	viper.Set(config.ControlPlane, "managed")
	viper.Set(config.ControlPlaneEndpt, "10.77.0.10")
	viper.Set(config.ControlPlaneDisk, "/dev/vda")
	viper.Set(config.Profile, "kubeadm-worker")
	viper.Set(config.KubeadmJoin, joinMode)
	t.Cleanup(func() { viper.Set(config.ControlPlaneDisk, "") })
	state.ResetClusterReady()
	t.Cleanup(state.ResetClusterReady)
	m, err := cluster.New(cluster.FromConfig())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(Options{WebDir: dir, BootFiles: testBootFiles, Cluster: m}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { setClusterManager(nil); setJoinMinter(nil) })
	return srv, m
}

func ignitionFiles(t *testing.T, body string) map[string]string {
	t.Helper()
	cfg := parseIgnition(t, body)
	files := map[string]string{}
	storage, _ := cfg["storage"].(map[string]any)
	list, _ := storage["files"].([]any)
	for _, f := range list {
		file := f.(map[string]any)
		src := file["contents"].(map[string]any)["source"].(string)
		if strings.HasPrefix(src, "data:text/plain;charset=utf-8;base64,") {
			b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(src, "data:text/plain;charset=utf-8;base64,"))
			if err != nil {
				t.Fatal(err)
			}
			src = string(b)
		}
		files[file["path"].(string)] = src
	}
	return files
}

func ignitionUnitNames(t *testing.T, body string) []string {
	t.Helper()
	cfg := parseIgnition(t, body)
	var names []string
	systemd, _ := cfg["systemd"].(map[string]any)
	list, _ := systemd["units"].([]any)
	for _, u := range list {
		names = append(names, u.(map[string]any)["name"].(string))
	}
	return names
}

func TestManagedControlPlaneRender(t *testing.T) {
	srv, m := newManagedServer(t, config.KubeadmJoinAuto)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp1","os":"flatcar","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"cp2","os":"coreos","role":"control-plane"}`)

	for _, mac := range []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"} {
		r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+mac+"&preview=1", "")
		if r.status != 200 {
			t.Fatalf("%s: %+v", mac, r)
		}
		files := ignitionFiles(t, r.body)
		if files[profile.CAKeyPath] != string(m.PKI.CAKey()) || files[profile.CACertPath] != string(m.PKI.CACert()) {
			t.Fatalf("%s: control plane must carry the Manager's CA pair", mac)
		}
		if !strings.Contains(r.body, `"path":"/etc/kubernetes/pki/ca.key"`) || !strings.Contains(files[profile.CAKeyPath], "PRIVATE KEY") {
			t.Fatal("control-plane preview must contain the CA key (it is the config)")
		}
		tok, _ := m.Tokens.Peek(token.PurposeKubeadmWorker)
		certKey, _ := m.Tokens.Peek(token.PurposeKubeadmCertKey)
		initYAML := files[profile.KubeadmInitConfig]
		for _, want := range []string{"token: " + tok.Token, "certificateKey: " + certKey.Token, "controlPlaneEndpoint: 10.77.0.10", "podSubnet: 10.244.0.0/16", "serviceSubnet: 10.96.0.0/12", "kubernetesVersion: v1.34.3"} {
			if !strings.Contains(initYAML, want) {
				t.Errorf("%s: kubeadm-init.yaml missing %q:\n%s", mac, want, initYAML)
			}
		}
		if !strings.Contains(files[cni.ScriptPath], "cilium install --version") {
			t.Errorf("%s: default CNI is cilium", mac)
		}
		names := strings.Join(ignitionUnitNames(t, r.body), ",")
		for _, want := range []string{"booty-booted.service", profile.ControlPlaneMountUnit, profile.UnitSeed, "etc-kubernetes.mount", profile.UnitInit, profile.UnitClusterReady, cni.UnitName} {
			if !strings.Contains(names, want) {
				t.Errorf("%s: units missing %s: %s", mac, want, names)
			}
		}
		if strings.Contains(names, profile.UnitJoin) || strings.Contains(r.body, "JOIN_STRING") {
			t.Fatalf("%s: control plane must not carry the worker join: %s", mac, names)
		}
		if !strings.Contains(r.body, `"device":"/dev/vda"`) || !strings.Contains(r.body, `"label":"booty-cp"`) || !strings.Contains(r.body, `"wipeFilesystem":false`) {
			t.Fatalf("%s: control-plane filesystem missing", mac)
		}
	}

	r := do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1&part=merged", "")
	if r.status != 200 || !strings.Contains(r.body, "booty-k8s-init.service") || !strings.Contains(r.body, "/etc/hostname") {
		t.Fatalf("merged preview: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	if r.status != 200 || strings.Contains(r.body, "PRIVATE KEY") || strings.Contains(r.body, certKeyOf(t, m)) {
		t.Fatalf("/cluster must not leak key material: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/config", "")
	if strings.Contains(r.body, "PRIVATE KEY") || strings.Contains(r.body, certKeyOf(t, m)) {
		t.Fatal("/config must not leak key material")
	}
}

func certKeyOf(t *testing.T, m *cluster.Manager) string {
	t.Helper()
	e, err := m.Tokens.Current(token.PurposeKubeadmCertKey)
	if err != nil {
		t.Fatal(err)
	}
	return e.Token
}

func TestManagedWorkerGetsPreGeneratedJoin(t *testing.T) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		t.Skip("running inside a cluster")
	}
	srv, m := newManagedServer(t, config.KubeadmJoinAuto)
	setJoinMinter(nil)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp1","os":"flatcar","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:03","hostname":"w2","os":"coreos","role":"worker"}`)

	resp, err := http.Get(srv.URL + "/ignition.json?mac=aa:bb:cc:dd:ee:02")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get(WarningHeader) != "" {
		t.Fatalf("the pre-generated join is the expected pre-API state, not a warning: %d %v", resp.StatusCode, resp.Header)
	}
	tok, _ := m.Tokens.Peek(token.PurposeKubeadmWorker)
	want := "kubeadm join 10.77.0.10:6443 --token " + tok.Token + " --discovery-token-ca-cert-hash " + m.PKI.DiscoveryHash()
	for _, mac := range []string{"aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"} {
		r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+mac, "")
		if r.status != 200 || !strings.Contains(r.body, `JOIN_STRING=`+want+`\"`) {
			t.Fatalf("%s: worker must carry the pre-generated join string %q: %+v", mac, want, r)
		}
		if strings.Contains(r.body, "PRIVATE KEY") || strings.Contains(r.body, "ca.key") || strings.Contains(r.body, "kubeadm-init.yaml") {
			t.Fatalf("%s: worker must never see key material", mac)
		}
		for path, content := range ignitionFiles(t, r.body) {
			if strings.Contains(content, "PRIVATE KEY") || strings.Contains(content, certKeyOf(t, m)) {
				t.Fatalf("%s: worker file %s carries key material", mac, path)
			}
		}
		names := strings.Join(ignitionUnitNames(t, r.body), ",")
		if !strings.Contains(names, profile.UnitJoin) || strings.Contains(names, profile.UnitInit) || strings.Contains(names, "etc-kubernetes.mount") {
			t.Fatalf("%s: worker units: %s", mac, names)
		}
		if !strings.Contains(r.body, `Restart=on-failure\nRestartSec=30s`) {
			t.Fatalf("%s: worker join unit must retry", mac)
		}
	}
	r := do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:02&preview=1&part=builtin", "")
	if !strings.Contains(r.body, `JOIN_STRING=`+want) {
		t.Fatalf("previews show the pre-generated join string too: %+v", r)
	}

	viper.Set(config.Profile, "")
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02", "")
	if !strings.Contains(r.body, profile.UnitJoin) {
		t.Fatalf("a managed kubeadm cluster implies the kubeadm-worker profile: %+v", r)
	}
}

func TestManagedStaticModeUsesPreGeneratedJoinWhenUnset(t *testing.T) {
	srv, m := newManagedServer(t, config.KubeadmJoinStatic)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"flatcar"}`)
	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02", "")
	tok, _ := m.Tokens.Peek(token.PurposeKubeadmWorker)
	if !strings.Contains(r.body, "JOIN_STRING=kubeadm join 10.77.0.10:6443 --token "+tok.Token) {
		t.Fatalf("static mode without --joinString falls back to the pre-generated join: %+v", r)
	}
	viper.Set(config.JoinString, "kubeadm join static:6443 --token a.b")
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02", "")
	if !strings.Contains(r.body, "JOIN_STRING=kubeadm join static:6443") {
		t.Fatalf("an explicit --joinString still wins: %+v", r)
	}
}

func TestManagedMinterWinsOverPreGenerated(t *testing.T) {
	srv, _ := newManagedServer(t, config.KubeadmJoinAuto)
	api, minter, hash := newFakeMinter(t)
	setJoinMinter(minter)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp1","os":"flatcar","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"flatcar"}`)
	resp, err := http.Get(srv.URL + "/ignition.json?mac=aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if api.posts.Load() != 0 {
		t.Fatal("a control-plane host must not mint a join token")
	}
	resp, err = http.Get(srv.URL + "/ignition.json?mac=aa:bb:cc:dd:ee:02")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02", "")
	if api.posts.Load() != 1 || !strings.Contains(r.body, "--discovery-token-ca-cert-hash sha256:"+hash) || strings.Contains(r.body, "10.77.0.10:6443") {
		t.Fatalf("once the API answers, minted tokens win: posts=%d %+v", api.posts.Load(), r)
	}
}

func TestManagedRenderRefusals(t *testing.T) {
	srv, _ := newManagedServer(t, config.KubeadmJoinStatic)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp1","os":"flatcar","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:04","hostname":"desk","os":"bluefin"}`)

	for _, url := range []string{"/ignition.json?mac=aa:bb:cc:dd:ee:04&preview=1", "/ignition/builtin.json?mac=aa:bb:cc:dd:ee:04", "/ignition/user.json?mac=aa:bb:cc:dd:ee:04"} {
		r := do(t, http.MethodGet, srv.URL+url, "")
		assertJSONError(t, r, http.StatusBadRequest)
		if !strings.Contains(r.body, "unsupported distribution kubeadm for os bluefin") {
			t.Fatalf("%s: %+v", url, r)
		}
	}
	r := do(t, http.MethodGet, srv.URL+"/cluster", "")
	if !strings.Contains(r.body, "host aa:bb:cc:dd:ee:04 (bluefin) unsupported under kubeadm") {
		t.Fatalf("/cluster must warn about the bluefin host: %s", r.body)
	}

	viper.Set(config.ControlPlaneDisk, "")
	clusterManager.Settings.ControlPlaneDisk = ""
	for _, url := range []string{"/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1", "/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01&preview=1", "/ignition/user.json?mac=aa:bb:cc:dd:ee:01"} {
		r := do(t, http.MethodGet, srv.URL+url, "")
		assertJSONError(t, r, http.StatusBadRequest)
		if r.body != `{"error":"control-plane host needs --controlPlaneDisk on a PXE-booted OS"}` {
			t.Fatalf("%s: %+v", url, r)
		}
	}
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	if !strings.Contains(r.body, "host aa:bb:cc:dd:ee:01 (flatcar): control-plane host needs --controlPlaneDisk on a PXE-booted OS") {
		t.Fatalf("/cluster must warn about the missing disk: %s", r.body)
	}
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"flatcar"}`)
	if r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02", ""); r.status != 200 {
		t.Fatalf("workers render without the disk: %+v", r)
	}
}

func TestExternalModeIgnoresRolesAndBluefin(t *testing.T) {
	srv, _ := newTestServer(t)
	viper.Set(config.Profile, "kubeadm-worker")
	viper.Set(config.JoinString, "kubeadm join 10.0.0.1:6443 --token t.s")
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp1","os":"flatcar","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:04","hostname":"desk","os":"bluefin"}`)
	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:04", "")
	if r.status != 200 || strings.Contains(r.body, "JOIN_STRING") {
		t.Fatalf("external mode keeps serving bluefin hosts the plain builtin fragment: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || strings.Contains(r.body, "JOIN_STRING") || strings.Contains(r.body, profile.UnitInit) || !strings.Contains(r.body, "booty-booted.service") {
		t.Fatalf("a control-plane host never gets worker join units, and external mode renders no control plane: %+v", r)
	}
}

func TestClusterReadyEndpoint(t *testing.T) {
	srv, _ := newManagedServer(t, config.KubeadmJoinStatic)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp1","os":"flatcar","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"flatcar"}`)

	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/cluster/ready?mac=aa:bb:cc:dd:ee:01", ""), http.StatusMethodNotAllowed)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/cluster/ready", ""), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/cluster/ready?mac=nope", ""), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/cluster/ready?mac=aa:bb:cc:dd:ee:02", ""), http.StatusNotFound)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/cluster/ready?mac=aa:bb:cc:dd:ee:09", ""), http.StatusNotFound)

	r := do(t, http.MethodGet, srv.URL+"/cluster", "")
	var resp clusterResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Ready || resp.ReadyAt != "" || strings.Contains(r.body, "readyAt") {
		t.Fatalf("not ready yet: %+v", resp)
	}

	r = do(t, http.MethodPost, srv.URL+"/cluster/ready?mac=AA:BB:CC:DD:EE:01", "")
	if r.status != 200 || !strings.Contains(r.body, `"ready":true`) {
		t.Fatalf("ready: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Ready || resp.ReadyAt == "" {
		t.Fatalf("/cluster must reflect ready: %+v", resp)
	}
	first := resp.ReadyAt
	if r := do(t, http.MethodPost, srv.URL+"/cluster/ready?mac=aa:bb:cc:dd:ee:01", ""); r.status != 200 || !strings.Contains(r.body, `"readyAt":"`+first+`"`) {
		t.Fatalf("second POST is idempotent: %+v", r)
	}
	data, err := os.ReadFile(filepath.Join(viper.GetString(config.DataDir), "cluster", "ready.json"))
	if err != nil || !strings.Contains(string(data), `"ready":true`) {
		t.Fatalf("ready must be persisted under cluster/: %s %v", data, err)
	}
	if r := do(t, http.MethodGet, srv.URL+"/data/cluster/ready.json", ""); r.status != http.StatusNotFound {
		t.Fatalf("cluster/ stays hidden from /data/: %+v", r)
	}
}

// TestManagedRenderValidates runs ignition-validate and kubeadm config
// validate on a real control-plane render when the binaries are around.
func TestManagedRenderValidates(t *testing.T) {
	ignitionValidate, ignErr := exec.LookPath("ignition-validate")
	kubeadmBin := os.Getenv("KUBEADM")
	if kubeadmBin == "" {
		kubeadmBin, _ = exec.LookPath("kubeadm")
	}
	if ignErr != nil && kubeadmBin == "" {
		t.Skip("neither ignition-validate nor kubeadm available")
	}
	srv, _ := newManagedServer(t, config.KubeadmJoinStatic)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"cp1","os":"flatcar","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"w1","os":"coreos"}`)
	dir := t.TempDir()
	for name, url := range map[string]string{
		"cp-builtin.json":     "/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01&preview=1",
		"cp-merged.json":      "/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1&part=merged",
		"worker-builtin.json": "/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02&preview=1",
	} {
		r := do(t, http.MethodGet, srv.URL+url, "")
		if r.status != 200 {
			t.Fatalf("%s: %+v", name, r)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(r.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if ignErr == nil {
			if out, err := exec.Command(ignitionValidate, path).CombinedOutput(); err != nil {
				t.Errorf("ignition-validate %s: %v\n%s", name, err, out)
			}
		}
		if name == "cp-builtin.json" && kubeadmBin != "" {
			initPath := filepath.Join(dir, "kubeadm-init.yaml")
			if err := os.WriteFile(initPath, []byte(ignitionFiles(t, r.body)[profile.KubeadmInitConfig]), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(kubeadmBin, "config", "validate", "--config", initPath).CombinedOutput(); err != nil {
				t.Errorf("kubeadm config validate: %v\n%s", err, out)
			}
		}
	}
}
