package server

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/cni"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/kubeadm"
	"github.com/jeefy/booty/pkg/profile"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

const (
	k0sBluefinCP   = "52:54:00:aa:00:40"
	k0sFlatcarW    = "52:54:00:aa:00:41"
	k0sCoreOSW     = "52:54:00:aa:00:42"
	k0sBluefinW    = "52:54:00:aa:00:43"
	k0sFlatcarCP   = "52:54:00:aa:00:44"
	k0sTestCNI     = "none"
	k0sTestEndpt   = "10.77.0.40"
	k0sTestCPDisk  = "/dev/vdb"
	k0sTestCtdDisk = "/dev/vda"
)

// newK0sServer mirrors the live-run command line: --clusterDistribution=k0s
// --controlPlane=<mode> --controlPlaneEndpoint=10.77.0.40
// --containerdDisk=/dev/vda --controlPlaneDisk=/dev/vdb --cni=none.
func newK0sServer(t *testing.T, mode string) (*httptest.Server, *cluster.Manager) {
	t.Helper()
	_, dir := newTestServer(t)
	viper.Set(config.ClusterDistribution, "k0s")
	viper.Set(config.ControlPlane, mode)
	viper.Set(config.ControlPlaneEndpt, k0sTestEndpt)
	viper.Set(config.ControlPlaneDisk, k0sTestCPDisk)
	viper.Set(config.ContainerdDisk, k0sTestCtdDisk)
	viper.Set(config.CNI, k0sTestCNI)
	t.Cleanup(func() { viper.Set(config.ControlPlaneDisk, "") })
	state.ResetClusterReady()
	t.Cleanup(state.ResetClusterReady)
	settings := cluster.FromConfig()
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	m, err := cluster.New(settings)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(Options{WebDir: dir, BootFiles: testBootFiles, Cluster: m}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { setClusterManager(nil); setJoinMinter(nil) })
	return srv, m
}

func registerK0sHosts(t *testing.T, url string) {
	t.Helper()
	register(t, url, `{"mac":"`+k0sBluefinCP+`","hostname":"bluefin-cp","os":"bluefin","role":"control-plane","installDisk":"/dev/vda"}`)
	register(t, url, `{"mac":"`+k0sFlatcarW+`","hostname":"w-flatcar","os":"flatcar"}`)
	register(t, url, `{"mac":"`+k0sCoreOSW+`","hostname":"w-coreos","os":"coreos","role":"worker"}`)
	register(t, url, `{"mac":"`+k0sBluefinW+`","hostname":"w-bluefin","os":"bluefin"}`)
}

func TestK0sManagedWorkers(t *testing.T) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		t.Skip("running inside a cluster")
	}
	srv, m := newK0sServer(t, "managed")
	viper.Set(config.KubeadmJoin, config.KubeadmJoinAuto)
	setJoinMinter(nil)
	registerK0sHosts(t, srv.URL)
	w, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	if w.Token != "" {
		t.Fatal("no k0s token before the first render")
	}

	for _, mac := range []string{k0sFlatcarW, k0sCoreOSW} {
		resp, err := http.Get(srv.URL + "/ignition.json?mac=" + mac)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get(WarningHeader) != "" {
			t.Fatalf("%s: k0s workers never mint kubeadm tokens, so no warning: %d %v", mac, resp.StatusCode, resp.Header)
		}
		r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+mac, "")
		if r.status != 200 {
			t.Fatalf("%s: %+v", mac, r)
		}
		names := strings.Join(ignitionUnitNames(t, r.body), ",")
		if names != "booty-booted.service,booty-update.service,booty-update.timer,booty-health.service,"+profile.K0sDataMountUnit+","+k0s.InstallUnit+","+k0s.WorkerUnit {
			t.Fatalf("%s: worker units: %s", mac, names)
		}
		if strings.Contains(r.body, "JOIN_STRING") || strings.Contains(r.body, "kubeadm") || strings.Contains(r.body, "PRIVATE KEY") || strings.Contains(r.body, "booty-cp") {
			t.Fatalf("%s: a k0s worker gets no kubeadm pieces and no keys", mac)
		}
		files := ignitionFiles(t, r.body)
		for path, content := range files {
			if strings.Contains(content, "PRIVATE KEY") {
				t.Fatalf("%s: %s carries a private key", mac, path)
			}
		}
		join, err := token.ParseK0s(strings.TrimSpace(files[k0s.TokenPath]))
		w, _ := m.Tokens.Peek(token.PurposeK0sWorker)
		if err != nil || join.Server != "https://10.77.0.40:6443" || join.Token != w.Token || join.Role != k0s.Worker {
			t.Fatalf("%s: token %+v %v", mac, join, err)
		}
		if !strings.Contains(r.body, `"device":"/dev/vda"`) || !strings.Contains(r.body, `"path":"/var/lib/k0s"`) || !strings.Contains(r.body, `"wipeFilesystem":true`) {
			t.Fatalf("%s: /var/lib/k0s on the containerd disk", mac)
		}
	}

	r := do(t, http.MethodGet, srv.URL+"/ignition.json?mac="+k0sFlatcarW+"&preview=1&part=merged", "")
	if r.status != 200 || !strings.Contains(r.body, k0s.WorkerUnit) || !strings.Contains(r.body, "/etc/hostname") {
		t.Fatalf("merged preview: %+v", r)
	}
}

func TestK0sManagedFlatcarController(t *testing.T) {
	srv, m := newK0sServer(t, "managed")
	register(t, srv.URL, `{"mac":"`+k0sFlatcarCP+`","hostname":"flatcar-cp","os":"flatcar","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"`+k0sBluefinW+`","hostname":"w-bluefin","os":"bluefin"}`)

	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sFlatcarCP+"&preview=1", "")
	if r.status != 200 {
		t.Fatalf("%+v", r)
	}
	names := strings.Join(ignitionUnitNames(t, r.body), ",")
	for _, want := range []string{"booty-booted.service", profile.ControlPlaneMountUnit, profile.UnitSeed, profile.K0sDataMountUnit, profile.K0sContainerdMountUnit, k0s.InstallUnit, k0s.ControllerUnit, k0s.UnitClusterReady} {
		if !strings.Contains(names, want) {
			t.Errorf("units missing %s: %s", want, names)
		}
	}
	for _, absent := range []string{profile.UnitInit, profile.UnitJoin, profile.UnitKubeTools, "etc-kubernetes.mount", cni.UnitName, "JOIN_STRING"} {
		if strings.Contains(r.body, absent) {
			t.Errorf("k0s controller must not carry %s", absent)
		}
	}
	files := ignitionFiles(t, r.body)
	if files[k0s.PKIDir+"/ca.key"] != string(m.PKI.CAKey()) || !strings.Contains(files[k0s.PKIDir+"/ca.key"], "PRIVATE KEY") {
		t.Fatal("controller preview carries the CA key (it is the config)")
	}
	if !strings.Contains(files[k0s.ConfigPath], "externalAddress: 10.77.0.40") {
		t.Fatalf("k0s.yaml:\n%s", files[k0s.ConfigPath])
	}
	if !strings.Contains(r.body, `"device":"/dev/vdb"`) || !strings.Contains(r.body, `"label":"booty-cp"`) || !strings.Contains(r.body, `"path":"/var/lib/k0s/containerd"`) {
		t.Fatalf("controller filesystems missing")
	}

	viper.Set(config.ControlPlaneDisk, "")
	clusterManager.Settings.ControlPlaneDisk = ""
	for _, url := range []string{"/ignition.json?mac=" + k0sFlatcarCP + "&preview=1", "/ignition/builtin.json?mac=" + k0sFlatcarCP + "&preview=1", "/ignition/user.json?mac=" + k0sFlatcarCP} {
		r := do(t, http.MethodGet, srv.URL+url, "")
		assertJSONError(t, r, http.StatusBadRequest)
		if r.body != `{"error":"control-plane host needs --controlPlaneDisk on a PXE-booted OS"}` {
			t.Fatalf("%s: %+v", url, r)
		}
	}
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	if !strings.Contains(r.body, "host "+k0sFlatcarCP+" (flatcar): control-plane host needs --controlPlaneDisk on a PXE-booted OS") {
		t.Fatalf("/cluster must warn: %s", r.body)
	}
	register(t, srv.URL, `{"mac":"`+k0sBluefinCP+`","hostname":"bluefin-cp","os":"bluefin","role":"control-plane"}`)
	viper.Set(config.ControlPlaneEndpt, "")
	clusterManager.Settings.Endpoint = ""
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	if !strings.Contains(r.body, "2 control-plane hosts registered but --controlPlaneEndpoint is not set") {
		t.Fatalf("/cluster warns about the endpoint: %s", r.body)
	}
}

func TestK0sExternal(t *testing.T) {
	srv, _ := newK0sServer(t, "external")
	registerK0sHosts(t, srv.URL)
	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sFlatcarW, "")
	if r.status != 200 || strings.Contains(r.body, "k0s") {
		t.Fatalf("external k0s without --k0sTokenFile: plain builtin: %+v", r)
	}

	tokenFile := filepath.Join(t.TempDir(), "token")
	tok, err := token.EncodeK0s(k0s.Worker, "k0s.example.org", []byte("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"), "abcdef.0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clusterManager.Settings.K0sTokenFile = tokenFile
	for _, mac := range []string{k0sFlatcarW, k0sCoreOSW} {
		r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+mac, "")
		if r.status != 200 || !strings.Contains(r.body, k0s.WorkerUnit) || ignitionFiles(t, r.body)[k0s.TokenPath] != tok+"\n" {
			t.Fatalf("%s: external worker carries the file's token: %+v", mac, r)
		}
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sBluefinCP, "")
	if r.status != 200 || strings.Contains(r.body, "k0s") {
		t.Fatalf("an external control-plane host renders the plain builtin: %+v", r)
	}
}

// newK0sFakeMinter is the pkg/kubeadm fake API as a Minter for the k0s
// paths: a static bearer token and the server's own certificate as CA.
func newK0sFakeMinter(t *testing.T) (*fakeKubeAPI, *kubeadm.Minter) {
	t.Helper()
	api := &fakeKubeAPI{}
	apiSrv := httptest.NewTLSServer(api)
	t.Cleanup(apiSrv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: apiSrv.Certificate().Raw})
	return api, kubeadm.New(kubeadm.KubeConfig{APIServer: apiSrv.URL, Token: "sa", CAData: caPEM}, time.Hour)
}

func TestK0sManagedWorkersMintJoinTokens(t *testing.T) {
	srv, m := newK0sServer(t, "managed")
	api, minter := newK0sFakeMinter(t)
	m.Minter = minter
	registerK0sHosts(t, srv.URL)

	r := do(t, http.MethodGet, srv.URL+"/ignition.json?mac="+k0sFlatcarW+"&preview=1&part=builtin", "")
	pre, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	join, err := token.ParseK0s(strings.TrimSpace(ignitionFiles(t, r.body)[k0s.TokenPath]))
	if err != nil || join.Token != pre.Token || api.posts.Load() != 0 {
		t.Fatalf("a preview with a cold cache never mints and shows the pre-shared token: %+v %v posts=%d", join, err, api.posts.Load())
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sFlatcarW+"&preview=1", "")
	if api.posts.Load() != 0 {
		t.Fatal("the builtin child with preview=1 does not mint either")
	}

	resp, err := http.Get(srv.URL + "/ignition.json?mac=" + k0sFlatcarW)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get(WarningHeader) != "" || api.posts.Load() != 0 {
		t.Fatalf("the wrapper carries no k0s files and mints nothing: %d %v posts=%d", resp.StatusCode, resp.Header, api.posts.Load())
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sFlatcarW, "")
	if r.status != 200 || api.posts.Load() != 1 {
		t.Fatalf("the real builtin fetch mints exactly one token: posts=%d %+v", api.posts.Load(), r)
	}
	minted, err := token.ParseK0s(strings.TrimSpace(ignitionFiles(t, r.body)[k0s.TokenPath]))
	if err != nil || minted.Token == pre.Token || minted.Role != k0s.Worker || minted.User != "kubelet-bootstrap" || minted.Server != "https://10.77.0.40:6443" || string(minted.CACert) != string(m.PKI.CACert()) {
		t.Fatalf("minted worker token: %+v %v", minted, err)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sFlatcarW, "")
	again, _ := token.ParseK0s(strings.TrimSpace(ignitionFiles(t, r.body)[k0s.TokenPath]))
	if again.Token != minted.Token || api.posts.Load() != 1 {
		t.Fatalf("a retry within the cache window reuses the token: posts=%d", api.posts.Load())
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac="+k0sFlatcarW+"&preview=1&part=builtin", "")
	if p, _ := token.ParseK0s(strings.TrimSpace(ignitionFiles(t, r.body)[k0s.TokenPath])); p.Token != minted.Token || api.posts.Load() != 1 {
		t.Fatal("a preview after the boot shows the cached minted token")
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sCoreOSW, "")
	other, _ := token.ParseK0s(strings.TrimSpace(ignitionFiles(t, r.body)[k0s.TokenPath]))
	if other.Token == minted.Token || api.posts.Load() != 2 {
		t.Fatalf("each MAC gets its own token: posts=%d", api.posts.Load())
	}
	if r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sBluefinCP+"&preview=1", ""); r.status != 200 || api.posts.Load() != 2 {
		t.Fatal("a controller never mints")
	}
}

func TestK0sExternalKubeconfigMintsWithoutTokenFile(t *testing.T) {
	srv, m := newK0sServer(t, "external")
	api, minter := newK0sFakeMinter(t)
	m.Minter = minter
	registerK0sHosts(t, srv.URL)

	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sFlatcarW, "")
	if r.status != 200 || !strings.Contains(r.body, k0s.WorkerUnit) || api.posts.Load() != 1 {
		t.Fatalf("external worker with --kubeconfig gets k0s units and a minted token: posts=%d %+v", api.posts.Load(), r)
	}
	join, err := token.ParseK0s(strings.TrimSpace(ignitionFiles(t, r.body)[k0s.TokenPath]))
	if err != nil || join.Role != k0s.Worker || join.Server != "https://10.77.0.40:6443" || string(join.CACert) != string(minter.CACert()) {
		t.Fatalf("external token wraps --controlPlaneEndpoint and the kubeconfig's CA: %+v %v", join, err)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sBluefinCP, "")
	if r.status != 200 || strings.Contains(r.body, "k0s") || api.posts.Load() != 1 {
		t.Fatalf("an external control-plane host still renders the plain builtin: %+v", r)
	}
}
