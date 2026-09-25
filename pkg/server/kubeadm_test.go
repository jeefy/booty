package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/kubeadm"
	"github.com/spf13/viper"
)

func TestKubeadmWorkerProfileStatic(t *testing.T) {
	srv, _ := newTestServer(t)
	viper.Set(config.Profile, "kubeadm-worker")
	viper.Set(config.ContainerdDisk, "/dev/sda")
	viper.Set(config.JoinString, "kubeadm join 10.0.0.1:6443 --token t.s --discovery-token-ca-cert-hash sha256:abc")
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"w1","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"desk","os":"bluefin"}`)

	r := do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1&part=merged", "")
	if r.status != 200 {
		t.Fatalf("merged preview: %+v", r)
	}
	merged := parseIgnition(t, r.body)
	files := map[string]string{}
	for _, f := range merged["storage"].(map[string]any)["files"].([]any) {
		file := f.(map[string]any)
		files[file["path"].(string)] = file["contents"].(map[string]any)["source"].(string)
	}
	tools, ok := files["/opt/booty/kube-tools.sh"]
	if !ok {
		t.Fatalf("kube-tools.sh missing from merged config; files=%v", files)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(tools, "data:text/plain;charset=utf-8;base64,"))
	if err != nil || !strings.Contains(string(decoded), `RELEASE="v1.34.3"`) {
		t.Fatalf("kube-tools.sh must carry the k8s version: %s err=%v", decoded, err)
	}
	cni, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(files["/opt/booty/cni.sh"], "data:text/plain;charset=utf-8;base64,"))
	if !strings.Contains(string(cni), `CNI_VERSION="v1.1.1"`) {
		t.Fatalf("cni.sh must carry the CNI version: %s", cni)
	}
	if _, ok := files["/etc/hostname"]; !ok {
		t.Fatal("builtin hostname must still be present")
	}

	var names []string
	for _, u := range merged["systemd"].(map[string]any)["units"].([]any) {
		names = append(names, u.(map[string]any)["name"].(string))
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"booty-booted.service", "var-lib-containerd.mount", "containerd.service",
		"booty-cni-install.service", "booty-kube-tools.service", "booty-kubelet-setup.service", "booty-k8s-join.service"} {
		if !strings.Contains(joined, want) {
			t.Errorf("merged units missing %s: %s", want, joined)
		}
	}
	if strings.Index(joined, "booty-cni-install") > strings.Index(joined, "booty-k8s-join") || strings.Index(joined, "booty-booted") > strings.Index(joined, "booty-cni-install") {
		t.Fatalf("order must be builtin then profile chain: %s", joined)
	}
	if !strings.Contains(r.body, `JOIN_STRING=kubeadm join 10.0.0.1:6443 --token t.s --discovery-token-ca-cert-hash sha256:abc`) {
		t.Fatalf("join unit must carry the static join string:\n%s", r.body)
	}
	if !strings.Contains(r.body, `"device": "/dev/sda"`) || !strings.Contains(r.body, `"wipeFilesystem": true`) {
		t.Fatalf("containerd filesystem missing:\n%s", r.body)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || !strings.Contains(r.body, "booty-k8s-join.service") || r.contentType == "" {
		t.Fatalf("builtin child must include the profile: %+v", r)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02", "")
	if r.status != 200 || strings.Contains(r.body, "booty-k8s-join") || strings.Contains(r.body, "/opt/booty/kube-tools.sh") || !strings.Contains(r.body, "booty-booted.service") {
		t.Fatalf("bluefin hosts must not get the profile: %+v", r)
	}

	viper.Set(config.Profile, "")
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	if strings.Contains(r.body, "booty-k8s-join") {
		t.Fatalf("no profile means no profile units: %s", r.body)
	}
}

func TestJoinStringFileWinsOverFlag(t *testing.T) {
	srv, dir := newTestServer(t)
	viper.Set(config.Profile, "kubeadm-worker")
	viper.Set(config.JoinString, "kubeadm join flag:6443 --token a.b")
	file := filepath.Join(dir, "join.txt")
	if err := os.WriteFile(file, []byte("  kubeadm join file:6443 --token c.d \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.JoinStringFile, file)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"w1","os":"flatcar"}`)

	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	if !strings.Contains(r.body, `JOIN_STRING=kubeadm join file:6443 --token c.d\"`) || strings.Contains(r.body, "flag:6443") {
		t.Fatalf("file must win and be trimmed: %s", r.body)
	}
	if err := os.WriteFile(file, []byte("kubeadm join rotated:6443 --token e.f"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	if !strings.Contains(r.body, "rotated:6443") {
		t.Fatalf("file must be re-read per render: %s", r.body)
	}

	viper.Set(config.JoinStringFile, filepath.Join(dir, "missing"))
	resp, err := http.Get(srv.URL + "/ignition.json?mac=aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get(WarningHeader) == "" {
		t.Fatalf("unreadable join file must still boot with a warning: %d %v", resp.StatusCode, resp.Header)
	}
}

type fakeKubeAPI struct {
	posts atomic.Int32
	caPEM []byte
}

func (a *fakeKubeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/configmaps/cluster-info"):
		kubeconfig := "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: " +
			base64.StdEncoding.EncodeToString(a.caPEM) + "\n    server: https://192.168.1.10:6443\n  name: \"\"\n"
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"kubeconfig": kubeconfig}})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/namespaces/kube-system/secrets"):
		a.posts.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newFakeMinter(t *testing.T) (*fakeKubeAPI, *kubeadm.Minter, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kubernetes"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	hash, err := kubeadm.SPKIHash(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeKubeAPI{caPEM: caPEM}
	apiSrv := httptest.NewTLSServer(api)
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	tokenFile, caFile := filepath.Join(dir, "token"), filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(tokenFile, []byte("sa"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: apiSrv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return api, kubeadm.New(kubeadm.KubeConfig{APIServer: apiSrv.URL, TokenFile: tokenFile, CAFile: caFile}, time.Hour), hash
}

func TestKubeadmJoinAuto(t *testing.T) {
	srv, _ := newTestServer(t)
	api, minter, hash := newFakeMinter(t)
	setJoinMinter(minter)
	t.Cleanup(func() { setJoinMinter(nil) })
	viper.Set(config.Profile, "kubeadm-worker")
	viper.Set(config.KubeadmJoin, config.KubeadmJoinAuto)
	viper.Set(config.JoinString, "kubeadm join static:6443 --token a.b")
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"w1","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"desk","os":"bluefin"}`)

	r := do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1&part=builtin", "")
	if r.status != 200 || !strings.Contains(r.body, `JOIN_STRING=\"`) || api.posts.Load() != 0 {
		t.Fatalf("preview must not mint and sees an empty join string: posts=%d %+v", api.posts.Load(), r)
	}

	resp, err := http.Get(srv.URL + "/ignition.json?mac=aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get(WarningHeader) != "" {
		t.Fatalf("wrapper fetch: %d %v", resp.StatusCode, resp.Header)
	}
	if api.posts.Load() != 1 {
		t.Fatalf("wrapper fetch must mint exactly one token, posts=%d", api.posts.Load())
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	want := "JOIN_STRING=kubeadm join 192.168.1.10:6443 --token "
	if r.status != 200 || !strings.Contains(r.body, want) || !strings.Contains(r.body, "--discovery-token-ca-cert-hash sha256:"+hash) || strings.Contains(r.body, "static:6443") {
		t.Fatalf("builtin child must carry the minted join string: %+v", r)
	}
	if api.posts.Load() != 1 {
		t.Fatalf("child fetch must reuse the cached token, posts=%d", api.posts.Load())
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition/user.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || api.posts.Load() != 1 {
		t.Fatalf("user child must reuse the cached token too, posts=%d %+v", api.posts.Load(), r)
	}
	r = do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1&part=builtin", "")
	if !strings.Contains(r.body, want) {
		t.Fatalf("preview after a boot shows the cached join string: %+v", r)
	}

	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02", "")
	if r.status != 200 || api.posts.Load() != 1 || strings.Contains(r.body, "JOIN_STRING") {
		t.Fatalf("bluefin host must not mint or get the profile: posts=%d %+v", api.posts.Load(), r)
	}

	setJoinMinter(kubeadm.New(kubeadm.KubeConfig{APIServer: srv.URL, TokenFile: "/nonexistent", CAFile: ""}, time.Hour))
	r = do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || !strings.Contains(r.body, `JOIN_STRING=\"`) {
		t.Fatalf("cold cache + failing mint must still emit the unit with an empty JOIN_STRING: %+v", r)
	}
}

func TestKubeadmJoinAutoFailure(t *testing.T) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		t.Skip("running inside a cluster")
	}
	srv, _ := newTestServer(t)
	setJoinMinter(nil)
	viper.Set(config.Profile, "kubeadm-worker")
	viper.Set(config.KubeadmJoin, config.KubeadmJoinAuto)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"w1","os":"flatcar"}`)

	resp, err := http.Get(srv.URL + "/ignition.json?mac=aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("a failed mint must not fail the boot: %d", resp.StatusCode)
	}
	if got := resp.Header.Get(WarningHeader); got != "kubeadm join token unavailable" {
		t.Fatalf("warning header %q", got)
	}
	r := do(t, http.MethodGet, srv.URL+"/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || !strings.Contains(r.body, `Environment=\"JOIN_STRING=\"`) || !strings.Contains(r.body, "booty-k8s-join.service") {
		t.Fatalf("join unit must be emitted with an empty JOIN_STRING: %+v", r)
	}

	resp, err = http.Get(srv.URL + "/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get(WarningHeader) != "" {
		t.Fatal("previews do not mint, so they must not warn")
	}
}
