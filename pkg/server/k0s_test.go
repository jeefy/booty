package server

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/cni"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/creds"
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

// credsRules fetches and decrypts the Bluefin bundle for mac, asserting
// the two-credential contract, and returns the decoded tmpfiles files.
func credsRules(t *testing.T, srvURL, mac string) (string, map[string][2]string) {
	t.Helper()
	resp, err := http.Get(srvURL + "/creds/" + mac + ".tar")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("creds: %d %v", resp.StatusCode, err)
	}
	names := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(body))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(tr)
		plain, err := creds.Decrypt(strings.TrimSuffix(hdr.Name, ".cred"), content)
		if err != nil {
			t.Fatalf("%s: %v", hdr.Name, err)
		}
		names[hdr.Name] = string(plain)
	}
	if len(names) != 2 {
		t.Fatalf("bundle must hold exactly firstboot.hostname and tmpfiles.extra, got %v", names)
	}
	rules := names["tmpfiles.extra.cred"]
	files := map[string][2]string{}
	for _, line := range strings.Split(strings.TrimSpace(rules), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 7 || fields[0] != "f~" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(fields[6])
		if err != nil {
			t.Fatal(err)
		}
		files[fields[1]] = [2]string{fields[2], string(b)}
	}
	return rules, files
}

func TestK0sManagedBluefinController(t *testing.T) {
	srv, m := newK0sServer(t, "managed")
	registerK0sHosts(t, srv.URL)

	rules, files := credsRules(t, srv.URL, k0sBluefinCP)
	dropIn := files["/etc/systemd/system/k0scontroller.service.d/booty-role.conf"][1]
	if strings.Count(dropIn, "ExecStart=") != 2 || !strings.Contains(dropIn, "ExecStart=/usr/bin/k0s controller -c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot") || strings.Contains(dropIn, "--single") {
		t.Fatalf("drop-in:\n%s", dropIn)
	}
	for _, p := range []string{"/var/lib/k0s/pki/ca.crt", "/var/lib/k0s/pki/ca.key", "/var/lib/k0s/pki/sa.key", "/var/lib/k0s/pki/sa.pub", "/var/lib/k0s/pki/etcd/ca.crt", "/var/lib/k0s/pki/etcd/ca.key"} {
		if files[p][0] != "0600" {
			t.Errorf("%s: %+v", p, files[p])
		}
	}
	if files["/var/lib/k0s/pki/ca.key"][1] != string(m.PKI.CAKey()) {
		t.Fatal("controller bundle carries the Manager's CA key")
	}
	if !strings.Contains(files["/etc/k0s/k0s.yaml"][1], "externalAddress: 10.77.0.40") || !strings.Contains(files["/etc/k0s/k0s.yaml"][1], "provider: kuberouter") {
		t.Fatalf("k0s.yaml:\n%s", files["/etc/k0s/k0s.yaml"][1])
	}
	w, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	if !strings.Contains(files["/var/lib/k0s/manifests/booty/tokens.yaml"][1], "bootstrap-token-"+w.ID()) {
		t.Fatal("tokens manifest")
	}
	if !strings.Contains(rules, "booty-cluster-ready.service") || strings.Contains(rules, "booty-cni-apply") || strings.Contains(rules, "booty-cp") || strings.Contains(rules, "/opt/bin/k0s") {
		t.Fatalf("bluefin controller units:\n%s", rules)
	}
	if strings.Contains(files["/etc/systemd/system/k0scontroller.service.d/booty.conf"][1], "booty-cluster-ready.service") == false {
		t.Fatal("first-boot hook pulls the ready unit in")
	}

	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sBluefinCP+"&preview=1", "")
	if r.status != 200 || strings.Contains(r.body, "k0s") {
		t.Fatalf("bluefin is provisioned through creds; Ignition stays the plain builtin: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	var resp clusterResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Distribution != "k0s" || resp.Endpoint != k0sTestEndpt || len(resp.Warnings) != 0 || strings.Contains(r.body, "PRIVATE KEY") {
		t.Fatalf("/cluster: %s", r.body)
	}
	if r := do(t, http.MethodPost, srv.URL+"/cluster/ready?mac="+k0sBluefinCP, ""); r.status != 200 || !strings.Contains(r.body, `"ready":true`) {
		t.Fatalf("bluefin controller reports ready: %+v", r)
	}
	if r := do(t, http.MethodGet, srv.URL+"/config", ""); strings.Contains(r.body, "PRIVATE KEY") {
		t.Fatal("/config leaks")
	}
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
		if names != "booty-booted.service,booty-update.service,booty-update.timer,"+profile.K0sDataMountUnit+","+k0s.InstallUnit+","+k0s.WorkerUnit {
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

	rules, files := credsRules(t, srv.URL, k0sBluefinW)
	if strings.Contains(rules, "PRIVATE KEY") || strings.Contains(rules, "pki") || strings.Contains(rules, "k0s.yaml") {
		t.Fatalf("bluefin worker rules:\n%s", rules)
	}
	if files["/etc/systemd/system/k0scontroller.service.d/booty-role.conf"][1] != "[Service]\nExecStart=\nExecStart=/usr/bin/k0s worker --token-file /etc/k0s/token\n" {
		t.Fatalf("bluefin worker drop-in: %+v", files["/etc/systemd/system/k0scontroller.service.d/booty-role.conf"])
	}
	join, err := token.ParseK0s(strings.TrimSpace(files["/etc/k0s/token"][1]))
	if err != nil || join.Server != "https://10.77.0.40:6443" || join.Token != w.Token && w.Token != "" {
		t.Fatalf("bluefin worker token: %+v %v", join, err)
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
	if _, files := credsRules(t, srv.URL, k0sBluefinW); files["/etc/k0s/token"][0] != "0600" {
		t.Fatal("bluefin workers are unaffected by the disk rule")
	}
	register(t, srv.URL, `{"mac":"`+k0sBluefinCP+`","hostname":"bluefin-cp","os":"bluefin","role":"control-plane"}`)
	viper.Set(config.ControlPlaneEndpt, "")
	clusterManager.Settings.Endpoint = ""
	rules, _ := credsRules(t, srv.URL, k0sBluefinCP)
	if strings.Contains(rules, "k0s.yaml") {
		t.Fatal("two control planes without an endpoint: the bluefin bundle is served without k0s pieces")
	}
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
	rules, _ := credsRules(t, srv.URL, k0sBluefinW)
	if strings.Contains(rules, "booty-role.conf") {
		t.Fatalf("no token file, no drop-in:\n%s", rules)
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
	_, files := credsRules(t, srv.URL, k0sBluefinW)
	if files["/etc/k0s/token"][1] != tok+"\n" || !strings.Contains(files["/etc/systemd/system/k0scontroller.service.d/booty-role.conf"][1], "k0s worker --token-file") {
		t.Fatal("bluefin external worker")
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac="+k0sBluefinCP, "")
	if r.status != 200 || strings.Contains(r.body, "k0s") {
		t.Fatalf("an external control-plane host renders the plain builtin: %+v", r)
	}
	rules, _ = credsRules(t, srv.URL, k0sBluefinCP)
	if strings.Contains(rules, "booty-role.conf") || strings.Contains(rules, "k0s.yaml") {
		t.Fatalf("an external control-plane host gets no k0s pieces:\n%s", rules)
	}
}
