package cluster

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/kubeadm"
)

// fakeSecretsAPI accepts bootstrap-token Secret creations and remembers
// the decoded stringData of the last one.
type fakeSecretsAPI struct {
	posts atomic.Int32
	fail  atomic.Bool
	last  atomic.Pointer[map[string]string]
}

func (a *fakeSecretsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/namespaces/kube-system/secrets") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	a.posts.Add(1)
	if a.fail.Load() {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","message":"secrets is forbidden","reason":"Forbidden"}`))
		return
	}
	var body struct {
		StringData map[string]string `json:"stringData"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	a.last.Store(&body.StringData)
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{}`))
}

// newFakeMinter returns a Minter that talks to a fake API server with a
// static bearer token and the server's own certificate as its CA, which is
// what --kubeconfig would have carried.
func newFakeMinter(t *testing.T) (*fakeSecretsAPI, *kubeadm.Minter, []byte) {
	t.Helper()
	api := &fakeSecretsAPI{}
	srv := httptest.NewTLSServer(api)
	t.Cleanup(srv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	return api, kubeadm.New(kubeadm.KubeConfig{APIServer: srv.URL, Token: "sa", CAData: caPEM}, time.Hour), caPEM
}

func TestK0sWorkerTokenManagedMints(t *testing.T) {
	m := k0sManager(t, Managed, nil)
	api, minter, _ := newFakeMinter(t)
	m.Minter = minter
	worker := k0sHosts["52:54:00:aa:00:41"]

	tok, err := m.K0sWorkerToken(t.Context(), k0sHosts, worker, true)
	if err != nil {
		t.Fatal(err)
	}
	join, err := token.ParseK0s(tok)
	if err != nil {
		t.Fatal(err)
	}
	pre, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	if join.Role != k0s.Worker || join.User != "kubelet-bootstrap" || join.Server != "https://10.77.0.40:6443" || string(join.CACert) != string(m.PKI.CACert()) {
		t.Fatalf("minted token wraps Booty's endpoint and CA: %+v", join)
	}
	if pre.Token != "" && join.Token == pre.Token {
		t.Fatal("a minted token is not the pre-shared one")
	}
	if api.posts.Load() != 1 {
		t.Fatalf("posts=%d", api.posts.Load())
	}
	data := *api.last.Load()
	if data["token-id"]+"."+data["token-secret"] != join.Token || data["usage-bootstrap-authentication"] != "true" || data["description"] != "booty: w-flatcar 52:54:00:aa:00:41" {
		t.Fatalf("secret data %v", data)
	}
	for _, absent := range []string{"auth-extra-groups", "usage-bootstrap-signing"} {
		if _, ok := data[absent]; ok {
			t.Fatalf("k0s worker Secret must not carry %s: %v", absent, data)
		}
	}

	again, err := m.K0sWorkerToken(t.Context(), k0sHosts, worker, true)
	if err != nil || again != tok || api.posts.Load() != 1 {
		t.Fatalf("the second boot fetch within ttl/2 reuses the token: posts=%d err=%v", api.posts.Load(), err)
	}
	preview, err := m.K0sWorkerToken(t.Context(), k0sHosts, worker, false)
	if err != nil || preview != tok {
		t.Fatalf("a preview sees the cached token: %v", err)
	}

	n, err := m.K0sNodeFiles(t.Context(), k0sHosts, worker, "s", true)
	if err != nil || n == nil || n.Files[0].Contents != tok+"\n" || api.posts.Load() != 1 {
		t.Fatalf("K0sNodeFiles writes the minted token to /etc/k0s/token: %v posts=%d", err, api.posts.Load())
	}
	n, err = m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:44"], "s", true)
	if err != nil || n == nil || api.posts.Load() != 1 {
		t.Fatalf("controllers never mint: %v posts=%d", err, api.posts.Load())
	}
}

func TestK0sWorkerTokenFallsBackToPreShared(t *testing.T) {
	m := k0sManager(t, Managed, nil)
	api, minter, _ := newFakeMinter(t)
	m.Minter = minter
	worker := k0sHosts["52:54:00:aa:00:43"]

	preview, err := m.K0sWorkerToken(t.Context(), k0sHosts, worker, false)
	if err != nil {
		t.Fatal(err)
	}
	pre, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	if join, err := token.ParseK0s(preview); err != nil || join.Token != pre.Token || api.posts.Load() != 0 {
		t.Fatalf("a preview with a cold cache never mints and shows the pre-shared token: %+v %v posts=%d", join, err, api.posts.Load())
	}

	api.fail.Store(true)
	tok, err := m.K0sWorkerToken(t.Context(), k0sHosts, worker, true)
	if err != nil {
		t.Fatal(err)
	}
	if join, err := token.ParseK0s(tok); err != nil || join.Token != pre.Token || api.posts.Load() != 1 {
		t.Fatalf("a failed mint falls back to the pre-shared token: %+v %v posts=%d", join, err, api.posts.Load())
	}

	m.Minter = kubeadm.New(kubeadm.KubeConfig{}, time.Hour)
	tok, err = m.K0sWorkerToken(t.Context(), k0sHosts, worker, true)
	if join, perr := token.ParseK0s(tok); err != nil || perr != nil || join.Token != pre.Token {
		t.Fatalf("a minter without an API server falls back: %v %v", err, perr)
	}
	m.Minter = nil
	tok, err = m.K0sWorkerToken(t.Context(), k0sHosts, worker, true)
	if join, perr := token.ParseK0s(tok); err != nil || perr != nil || join.Token != pre.Token {
		t.Fatalf("no minter: pre-shared: %v %v", err, perr)
	}

	m.Settings.Endpoint = ""
	m.Minter = minter
	api.fail.Store(false)
	if _, err := m.K0sWorkerToken(t.Context(), k0sHosts, worker, true); err == nil || !strings.Contains(err.Error(), "control-plane hosts") || api.posts.Load() != 1 {
		t.Fatalf("no endpoint: nothing minted, the render refusal surfaces: %v posts=%d", err, api.posts.Load())
	}
}

func TestK0sWorkerTokenExternalKubeconfig(t *testing.T) {
	m := k0sManager(t, External, func(s *Settings) { s.Endpoint = "" })
	api, minter, caPEM := newFakeMinter(t)
	m.Minter = minter
	worker := k0sHosts["52:54:00:aa:00:41"]

	tok, err := m.K0sWorkerToken(t.Context(), k0sHosts, worker, true)
	if err != nil {
		t.Fatal(err)
	}
	join, err := token.ParseK0s(tok)
	if err != nil {
		t.Fatal(err)
	}
	apiURL, _ := url.Parse(minter.APIServer())
	if join.Server != "https://"+apiURL.Host || string(join.CACert) != string(caPEM) || join.Role != k0s.Worker {
		t.Fatalf("external tokens wrap the kubeconfig's server and CA: %+v", join)
	}
	data := *api.last.Load()
	if data["auth-extra-groups"] != "" || data["usage-bootstrap-authentication"] != "true" {
		t.Fatalf("secret data %v", data)
	}
	n, err := m.K0sNodeFiles(t.Context(), k0sHosts, worker, "s", true)
	if err != nil || n == nil || n.Role != k0s.Worker || n.Files[0].Contents != tok+"\n" {
		t.Fatalf("external worker without --k0sTokenFile still gets k0s units when a token was minted: %+v %v", n, err)
	}
	if n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:40"], "s", true); err != nil || n != nil {
		t.Fatalf("an external control-plane host still renders nothing: %+v %v", n, err)
	}

	m.Settings.Endpoint = "k0s.example.org:16443"
	other := k0sHosts["52:54:00:aa:00:42"]
	tok, err = m.K0sWorkerToken(t.Context(), k0sHosts, other, true)
	if err != nil {
		t.Fatal(err)
	}
	if join, err := token.ParseK0s(tok); err != nil || join.Server != "https://k0s.example.org:16443" {
		t.Fatalf("--controlPlaneEndpoint wins over the kubeconfig's server: %+v %v", join, err)
	}

	m.Minter = kubeadm.New(kubeadm.KubeConfig{APIServer: minter.APIServer(), Token: "sa"}, time.Hour)
	if tok, err := m.K0sWorkerToken(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:43"], true); err != nil || tok != "" {
		t.Fatalf("a kubeconfig without a CA cannot build a worker token and falls through to nothing: %q %v", tok, err)
	}
	if n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:43"], "s", false); err != nil || n != nil {
		t.Fatalf("preview of a worker with no token source: %+v %v", n, err)
	}
}
