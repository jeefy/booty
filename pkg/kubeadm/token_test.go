package kubeadm

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAPI struct {
	t        *testing.T
	mu       sync.Mutex
	secrets  map[string]secret
	posts    int
	deletes  []string
	infoHits int
	caPEM    []byte
	failPost int
	tokens   []string
}

func newFakeAPI(t *testing.T) (*fakeAPI, *Minter, *httptest.Server) {
	t.Helper()
	caPEM, _ := selfSignedCA(t)
	api := &fakeAPI{t: t, secrets: map[string]secret{}, caPEM: caPEM}
	srv := httptest.NewTLSServer(api)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(tokenFile, []byte("sa-token-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serverCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, serverCert, 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(KubeConfig{APIServer: srv.URL, TokenFile: tokenFile, CAFile: caFile}, time.Hour)
	return api, m, srv
}

func selfSignedCA(t *testing.T) (pemBytes []byte, spkiHash string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kubernetes"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), hex.EncodeToString(sum[:])
}

func (a *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tokens = append(a.tokens, r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == clusterInfoPath:
		a.infoHits++
		kubeconfig := "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: " +
			base64.StdEncoding.EncodeToString(a.caPEM) + "\n    server: https://192.168.1.10:6443\n  name: \"\"\ncontexts: null\nkind: Config\n"
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"kubeconfig": kubeconfig}})
	case r.Method == http.MethodPost && r.URL.Path == secretsPath:
		a.posts++
		if a.failPost != 0 {
			w.WriteHeader(a.failPost)
			_, _ = w.Write([]byte(`{"kind":"Status","message":"secrets is forbidden","reason":"Forbidden"}`))
			return
		}
		var s secret
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		a.secrets[s.Metadata.Name] = s
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(s)
	case r.Method == http.MethodGet && r.URL.Path == secretsPath:
		if r.URL.Query().Get("fieldSelector") != "type="+SecretType {
			a.t.Errorf("list must filter by type, got %q", r.URL.RawQuery)
		}
		items := make([]secret, 0, len(a.secrets))
		for _, s := range a.secrets {
			out := secret{Metadata: s.Metadata, Type: s.Type, Data: map[string]string{}}
			for k, v := range s.StringData {
				out.Data[k] = base64.StdEncoding.EncodeToString([]byte(v))
			}
			items = append(items, out)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, secretsPath+"/"):
		name := strings.TrimPrefix(r.URL.Path, secretsPath+"/")
		if _, ok := a.secrets[name]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(a.secrets, name)
		a.deletes = append(a.deletes, name)
		_, _ = w.Write([]byte(`{"kind":"Status","status":"Success"}`))
	default:
		a.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *fakeAPI) addSecret(s secret) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.secrets[s.Metadata.Name] = s
}

var joinRe = regexp.MustCompile(`^kubeadm join 192\.168\.1\.10:6443 --token ([a-z0-9]{6})\.([a-z0-9]{16}) --discovery-token-ca-cert-hash sha256:([0-9a-f]{64})$`)

func TestJoinStringMintsAndCaches(t *testing.T) {
	api, m, _ := newFakeAPI(t)
	_, wantHash := selfSignedCAFromPEM(t, api.caPEM)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	join, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "node1")
	if err != nil {
		t.Fatal(err)
	}
	parts := joinRe.FindStringSubmatch(join)
	if parts == nil {
		t.Fatalf("join string format: %q", join)
	}
	if parts[3] != wantHash {
		t.Fatalf("discovery hash %s want %s", parts[3], wantHash)
	}
	id := parts[1]
	s, ok := api.secrets["bootstrap-token-"+id]
	if !ok {
		t.Fatalf("secret bootstrap-token-%s not created; have %v", id, api.secrets)
	}
	if s.Type != SecretType || s.Metadata.Namespace != "kube-system" || s.APIVersion != "v1" || s.Kind != "Secret" {
		t.Fatalf("secret envelope %+v", s)
	}
	want := map[string]string{
		"token-id":                       id,
		"token-secret":                   parts[2],
		"expiration":                     "2026-09-25T13:00:00Z",
		"usage-bootstrap-authentication": "true",
		"usage-bootstrap-signing":        "true",
		"auth-extra-groups":              "system:bootstrappers:kubeadm:default-node-token",
		"description":                    "booty: node1 aa:bb:cc:dd:ee:01",
	}
	for k, v := range want {
		if s.StringData[k] != v {
			t.Errorf("stringData[%s] = %q want %q", k, s.StringData[k], v)
		}
	}
	if !strings.Contains(strings.Join(api.tokens, ","), "Bearer sa-token-1") {
		t.Fatalf("requests must carry the service account token: %v", api.tokens)
	}

	again, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "node1")
	if err != nil || again != join {
		t.Fatalf("second call must return the cached string: %q err=%v", again, err)
	}
	if api.posts != 1 {
		t.Fatalf("second call within ttl/2 must not POST, posts=%d", api.posts)
	}
	if cached, ok := m.Cached("aa:bb:cc:dd:ee:01"); !ok || cached != join {
		t.Fatalf("Cached: %q %v", cached, ok)
	}
	if _, ok := m.Cached("aa:bb:cc:dd:ee:02"); ok {
		t.Fatal("other MACs must not hit the cache")
	}

	other, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:02", "node2")
	if err != nil || other == join || api.posts != 2 {
		t.Fatalf("different MAC must mint its own token: %q posts=%d err=%v", other, api.posts, err)
	}
	if api.infoHits != 1 {
		t.Fatalf("cluster-info must be cached, hits=%d", api.infoHits)
	}

	now = now.Add(31 * time.Minute)
	if _, ok := m.Cached("aa:bb:cc:dd:ee:01"); ok {
		t.Fatal("cache must expire after ttl/2")
	}
	third, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "node1")
	if err != nil || third == join || api.posts != 3 {
		t.Fatalf("expired cache must mint again: %q posts=%d err=%v", third, api.posts, err)
	}
	if api.infoHits != 2 {
		t.Fatalf("cluster-info must be refetched after 10 minutes, hits=%d", api.infoHits)
	}
}

func selfSignedCAFromPEM(t *testing.T, pemBytes []byte) ([]byte, string) {
	t.Helper()
	h, err := SPKIHash(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	return pemBytes, h
}

func TestTokenRotationIsPickedUp(t *testing.T) {
	api, m, _ := newFakeAPI(t)
	if _, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "n1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.cfg.TokenFile, []byte("sa-token-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:02", "n2"); err != nil {
		t.Fatal(err)
	}
	if api.tokens[len(api.tokens)-1] != "Bearer sa-token-2" {
		t.Fatalf("token file must be re-read per request: %v", api.tokens)
	}
}

func TestMintFailure(t *testing.T) {
	api, m, _ := newFakeAPI(t)
	api.failPost = http.StatusForbidden
	_, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "n1")
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("RBAC failure must surface: %v", err)
	}
	if _, ok := m.Cached("aa:bb:cc:dd:ee:01"); ok {
		t.Fatal("failures must not be cached")
	}

	outside := New(InClusterConfig(), time.Hour)
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		_, err = outside.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "n1")
		if !errors.Is(err, ErrNotInCluster) {
			t.Fatalf("outside a cluster must return ErrNotInCluster, got %v", err)
		}
	}
}

func TestInClusterConfig(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	cfg := InClusterConfig()
	if cfg.APIServer != "https://10.96.0.1:443" || cfg.TokenFile != DefaultTokenFile || cfg.CAFile != DefaultCAFile {
		t.Fatalf("%+v", cfg)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "fd00::1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if cfg := InClusterConfig(); cfg.APIServer != "https://[fd00::1]:443" {
		t.Fatalf("IPv6 host must be bracketed: %+v", cfg)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if cfg := InClusterConfig(); cfg.APIServer != "" {
		t.Fatalf("no env must give no API server: %+v", cfg)
	}
}

func TestParseClusterInfoKubeconfig(t *testing.T) {
	caPEM, wantHash := selfSignedCA(t)
	kc := "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: " + base64.StdEncoding.EncodeToString(caPEM) +
		"\n    server: https://cp.example.internal:6443\n  name: \"\"\n"
	endpoint, hash, err := ParseClusterInfoKubeconfig(kc)
	if err != nil || endpoint != "cp.example.internal:6443" || hash != wantHash {
		t.Fatalf("endpoint=%q hash=%q err=%v", endpoint, hash, err)
	}
	for name, bad := range map[string]string{
		"empty":     "",
		"no server": "clusters:\n- cluster:\n    certificate-authority-data: " + base64.StdEncoding.EncodeToString(caPEM) + "\n",
		"bad b64":   "clusters:\n- cluster:\n    server: https://x:6443\n    certificate-authority-data: '!!!'\n",
		"no cert":   "clusters:\n- cluster:\n    server: https://x:6443\n    certificate-authority-data: " + base64.StdEncoding.EncodeToString([]byte("nope")) + "\n",
		"not yaml":  "{{{",
	} {
		if _, _, err := ParseClusterInfoKubeconfig(bad); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
}

func TestCleanupDeletesOnlyExpiredBootyTokens(t *testing.T) {
	api, m, _ := newFakeAPI(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	mk := func(name, desc, exp string) secret {
		return secret{Metadata: secretMeta{Name: name, Namespace: "kube-system"}, Type: SecretType,
			StringData: map[string]string{"description": desc, "expiration": exp}}
	}
	api.addSecret(mk("bootstrap-token-old001", "booty: n1 aa:bb:cc:dd:ee:01", "2026-09-25T11:00:00Z"))
	api.addSecret(mk("bootstrap-token-live01", "booty: n2 aa:bb:cc:dd:ee:02", "2026-09-25T13:00:00Z"))
	api.addSecret(mk("bootstrap-token-edge01", "booty: n3 aa:bb:cc:dd:ee:03", "2026-09-25T12:00:00Z"))
	api.addSecret(mk("bootstrap-token-human1", "created by an operator", "2020-01-01T00:00:00Z"))
	api.addSecret(mk("bootstrap-token-nodesc", "", "2020-01-01T00:00:00Z"))
	api.addSecret(mk("bootstrap-token-badexp", "booty: n4 aa:bb:cc:dd:ee:04", "yesterday"))
	api.addSecret(secret{Metadata: secretMeta{Name: "not-a-token"}, Type: "Opaque", StringData: map[string]string{"description": "booty: x", "expiration": "2020-01-01T00:00:00Z"}})

	m.cache["aa:bb:cc:dd:ee:09"] = cachedJoin{join: "x", expires: now.Add(-time.Second)}
	m.cache["aa:bb:cc:dd:ee:10"] = cachedJoin{join: "y", expires: now.Add(time.Minute)}

	deleted, err := m.Cleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 || len(api.deletes) != 2 {
		t.Fatalf("deleted=%d deletes=%v", deleted, api.deletes)
	}
	got := strings.Join(api.deletes, ",")
	if !strings.Contains(got, "bootstrap-token-old001") || !strings.Contains(got, "bootstrap-token-edge01") {
		t.Fatalf("expired booty tokens (including exactly-now) must go: %v", api.deletes)
	}
	for _, keep := range []string{"bootstrap-token-live01", "bootstrap-token-human1", "bootstrap-token-nodesc", "bootstrap-token-badexp", "not-a-token"} {
		if _, ok := api.secrets[keep]; !ok {
			t.Errorf("%s must be kept", keep)
		}
	}
	if _, ok := m.cache["aa:bb:cc:dd:ee:09"]; ok {
		t.Error("stale cache entry must be dropped")
	}
	if _, ok := m.cache["aa:bb:cc:dd:ee:10"]; !ok {
		t.Error("live cache entry must be kept")
	}
}

func TestRandomToken(t *testing.T) {
	id, sec, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z0-9]{6}$`).MatchString(id) || !regexp.MustCompile(`^[a-z0-9]{16}$`).MatchString(sec) {
		t.Fatalf("token %s.%s", id, sec)
	}
	seen := map[string]bool{}
	for range 50 {
		s, err := randomString(16)
		if err != nil || seen[s] {
			t.Fatalf("random strings must not repeat: %q err=%v", s, err)
		}
		seen[s] = true
	}
}

func TestBadCAFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(KubeConfig{APIServer: "https://127.0.0.1:1", TokenFile: tokenFile, CAFile: filepath.Join(dir, "missing")}, time.Hour)
	if _, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "n"); err == nil || !strings.Contains(err.Error(), "CA") {
		t.Fatalf("missing CA must fail clearly: %v", err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	untrusted := New(KubeConfig{APIServer: srv.URL, TokenFile: tokenFile, CAFile: ""}, time.Hour)
	if _, err := untrusted.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "n"); err == nil {
		t.Fatal("server not signed by the configured CA must be rejected")
	}
}

func TestMintK0sWorkerSpec(t *testing.T) {
	api, m, _ := newFakeAPI(t)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	tok, err := m.Mint(context.Background(), "aa:bb:cc:dd:ee:01", "w1", K0sWorkerSpec)
	if err != nil {
		t.Fatal(err)
	}
	if tok.String() != tok.ID+"."+tok.Secret || !regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`).MatchString(tok.String()) {
		t.Fatalf("token %+v", tok)
	}
	if !tok.Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires %s", tok.Expires)
	}
	s, ok := api.secrets["bootstrap-token-"+tok.ID]
	if !ok {
		t.Fatalf("secret not created; have %v", api.secrets)
	}
	if s.Type != SecretType || s.Metadata.Namespace != "kube-system" || s.Metadata.Name != "bootstrap-token-"+tok.ID {
		t.Fatalf("envelope %+v", s)
	}
	want := map[string]string{
		"token-id":                       tok.ID,
		"token-secret":                   tok.Secret,
		"expiration":                     "2026-09-26T13:00:00Z",
		"usage-bootstrap-authentication": "true",
		"description":                    "booty: w1 aa:bb:cc:dd:ee:01",
	}
	if len(s.StringData) != len(want) {
		t.Fatalf("a k0s worker token has exactly the k0s keys, got %v", s.StringData)
	}
	for k, v := range want {
		if s.StringData[k] != v {
			t.Errorf("stringData[%s] = %q want %q", k, s.StringData[k], v)
		}
	}
	for _, absent := range []string{"auth-extra-groups", "usage-bootstrap-signing"} {
		if _, ok := s.StringData[absent]; ok {
			t.Errorf("k0s worker tokens must not carry %s", absent)
		}
	}
	if api.infoHits != 0 {
		t.Fatal("Mint never reads cluster-info")
	}
	if _, ok := m.Cached("aa:bb:cc:dd:ee:01"); ok {
		t.Fatal("Mint does not populate the cache")
	}
}

func TestRenderedCachesPerMAC(t *testing.T) {
	api, m, _ := newFakeAPI(t)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	render := func(tok Token) (string, error) { return "k0s:" + tok.String(), nil }

	first, err := m.Rendered(context.Background(), "aa:bb:cc:dd:ee:01", "w1", K0sWorkerSpec, render)
	if err != nil || !strings.HasPrefix(first, "k0s:") {
		t.Fatalf("%q %v", first, err)
	}
	again, err := m.Rendered(context.Background(), "aa:bb:cc:dd:ee:01", "w1", K0sWorkerSpec, render)
	if err != nil || again != first || api.posts != 1 {
		t.Fatalf("second call within ttl/2 is served from the cache: %q posts=%d err=%v", again, api.posts, err)
	}
	if cached, ok := m.Cached("aa:bb:cc:dd:ee:01"); !ok || cached != first {
		t.Fatalf("Cached: %q %v", cached, ok)
	}
	other, err := m.Rendered(context.Background(), "aa:bb:cc:dd:ee:02", "w2", K0sWorkerSpec, render)
	if err != nil || other == first || api.posts != 2 {
		t.Fatalf("another MAC mints its own: %q posts=%d err=%v", other, api.posts, err)
	}
	now = now.Add(31 * time.Minute)
	third, err := m.Rendered(context.Background(), "aa:bb:cc:dd:ee:01", "w1", K0sWorkerSpec, render)
	if err != nil || third == first || api.posts != 3 {
		t.Fatalf("expired cache mints again: %q posts=%d err=%v", third, api.posts, err)
	}

	failing := func(Token) (string, error) { return "", errors.New("encode failed") }
	if _, err := m.Rendered(context.Background(), "aa:bb:cc:dd:ee:03", "w3", K0sWorkerSpec, failing); err == nil || !strings.Contains(err.Error(), "encode failed") {
		t.Fatalf("render errors surface: %v", err)
	}
	if _, ok := m.Cached("aa:bb:cc:dd:ee:03"); ok {
		t.Fatal("a failed render caches nothing")
	}
	api.failPost = http.StatusForbidden
	if _, err := m.Rendered(context.Background(), "aa:bb:cc:dd:ee:04", "w4", K0sWorkerSpec, render); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("API failures surface: %v", err)
	}
}

func TestCACert(t *testing.T) {
	_, m, _ := newFakeAPI(t)
	fromFile, err := os.ReadFile(m.cfg.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.CACert(); string(got) != string(fromFile) {
		t.Fatalf("CACert must read CAFile: %q", got)
	}
	inline := New(KubeConfig{APIServer: "https://x", CAData: []byte("pem")}, time.Hour)
	got := inline.CACert()
	if string(got) != "pem" {
		t.Fatalf("CACert must prefer CAData: %q", got)
	}
	got[0] = 'x'
	if string(inline.cfg.CAData) != "pem" {
		t.Fatal("CACert returns a copy")
	}
	if New(KubeConfig{APIServer: "https://x"}, time.Hour).CACert() != nil {
		t.Fatal("no CA configured yields nil")
	}
	if New(KubeConfig{APIServer: "https://x", CAFile: filepath.Join(t.TempDir(), "missing")}, time.Hour).CACert() != nil {
		t.Fatal("an unreadable CAFile yields nil")
	}
}

func TestCleanupCoversK0sMintedTokens(t *testing.T) {
	api, m, _ := newFakeAPI(t)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	if _, err := m.Mint(context.Background(), "aa:bb:cc:dd:ee:01", "w1", K0sWorkerSpec); err != nil {
		t.Fatal(err)
	}
	api.addSecret(secret{Metadata: secretMeta{Name: "bootstrap-token-k0spre", Namespace: "kube-system"}, Type: SecretType,
		StringData: map[string]string{"description": "Worker bootstrap token generated by k0s", "expiration": "2020-01-01T00:00:00Z", "usage-bootstrap-authentication": "true"}})
	now = now.Add(61 * time.Minute)
	deleted, err := m.Cleanup(context.Background())
	if err != nil || deleted != 1 || len(api.deletes) != 1 || !strings.HasPrefix(api.deletes[0], "bootstrap-token-") || api.deletes[0] == "bootstrap-token-k0spre" {
		t.Fatalf("the expired booty k0s token goes, k0s's own pre-shared token stays: deleted=%d %v err=%v", deleted, api.deletes, err)
	}
}
