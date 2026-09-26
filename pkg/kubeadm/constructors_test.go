package kubeadm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/cluster/pki"
)

func writeKubeconfig(t *testing.T, dir, server string, caPEM []byte, user string) string {
	t.Helper()
	kc := "apiVersion: v1\nkind: Config\ncurrent-context: booty\nclusters:\n- name: c\n  cluster:\n    server: " + server +
		"\n    certificate-authority-data: " + base64.StdEncoding.EncodeToString(caPEM) +
		"\ncontexts:\n- name: booty\n  context:\n    cluster: c\n    user: u\nusers:\n- name: u\n  user:\n" + user
	path := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(path, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFromKubeconfigWithToken(t *testing.T) {
	api, _, srv := newFakeAPI(t)
	serverCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, srv.URL, serverCert, "    token: static-token\n")

	m, err := FromKubeconfig(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if m.APIServer() != srv.URL {
		t.Fatalf("APIServer %q", m.APIServer())
	}
	join, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "n1")
	if err != nil || !joinRe.MatchString(join) {
		t.Fatalf("join %q err=%v", join, err)
	}
	if !strings.Contains(strings.Join(api.tokens, ","), "Bearer static-token") {
		t.Fatalf("requests must carry the kubeconfig token: %v", api.tokens)
	}
}

func TestFromKubeconfigWithFileReferences(t *testing.T) {
	_, _, srv := newFakeAPI(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kc := "apiVersion: v1\nclusters:\n- name: c\n  cluster:\n    server: " + srv.URL + "\n    certificate-authority: ca.crt\ncontexts:\n- name: x\n  context:\n    cluster: c\n    user: u\nusers:\n- name: u\n  user:\n    tokenFile: token\n"
	path := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(path, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := FromKubeconfig(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if m.cfg.TokenFile != filepath.Join(dir, "token") || m.cfg.CAData == nil {
		t.Fatalf("relative references must resolve against the kubeconfig dir: %+v", m.cfg)
	}
	if _, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "n1"); err != nil {
		t.Fatal(err)
	}
}

func TestFromKubeconfigRejectsBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := FromKubeconfig(filepath.Join(dir, "missing"), time.Hour); err == nil {
		t.Fatal("missing file must fail")
	}
	for name, body := range map[string]string{
		"no contexts":   "apiVersion: v1\nclusters:\n- name: c\n  cluster:\n    server: https://x:6443\n",
		"no creds":      "contexts:\n- name: a\n  context: {cluster: c, user: u}\nclusters:\n- name: c\n  cluster: {server: https://x:6443}\nusers:\n- name: u\n  user: {}\n",
		"http server":   "contexts:\n- name: a\n  context: {cluster: c, user: u}\nclusters:\n- name: c\n  cluster: {server: http://x:8080}\nusers:\n- name: u\n  user: {token: t}\n",
		"insecure":      "contexts:\n- name: a\n  context: {cluster: c, user: u}\nclusters:\n- name: c\n  cluster: {server: https://x:6443, insecure-skip-tls-verify: true}\nusers:\n- name: u\n  user: {token: t}\n",
		"unknown ctx":   "current-context: nope\ncontexts:\n- name: a\n  context: {cluster: c, user: u}\nclusters:\n- name: c\n  cluster: {server: https://x:6443}\nusers:\n- name: u\n  user: {token: t}\n",
		"bad base64 ca": "contexts:\n- name: a\n  context: {cluster: c, user: u}\nclusters:\n- name: c\n  cluster: {server: https://x:6443, certificate-authority-data: '!!'}\nusers:\n- name: u\n  user: {token: t}\n",
	} {
		if _, err := ParseKubeconfig([]byte(body), dir); err == nil {
			t.Errorf("%s: must fail", name)
		}
	}
}

func TestFromCA(t *testing.T) {
	p, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"))
	if err != nil {
		t.Fatal(err)
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(p.CACert())
	api := &fakeAPI{t: t, secrets: map[string]secret{}, caPEM: p.CACert()}
	var seenCN string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) > 0 {
			seenCN = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		api.ServeHTTP(w, r)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: caPool, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	m, err := FromCA(p, "10.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if m.APIServer() != "https://10.0.0.1:6443" {
		t.Fatalf("APIServer %q", m.APIServer())
	}
	if m.cfg.Token != "" || m.cfg.TokenFile != "" || m.cfg.ClientCertData == nil {
		t.Fatalf("FromCA must authenticate with a client certificate: %+v", m.cfg)
	}
	// The test server presents its own self-signed certificate, so point the
	// minter at it while keeping the client certificate; the API server CA
	// check is covered by the other tests.
	m.cfg.APIServer = srv.URL
	m.cfg.CAData = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	join, err := m.JoinString(context.Background(), "aa:bb:cc:dd:ee:01", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if seenCN != "kubernetes-admin" {
		t.Fatalf("client certificate CN %q", seenCN)
	}
	if api.infoHits != 0 {
		t.Fatal("FromCA knows endpoint and CA hash and must not read cluster-info")
	}
	wantHash := strings.TrimPrefix(p.DiscoveryHash(), "sha256:")
	if !strings.HasPrefix(join, "kubeadm join 10.0.0.1:6443 --token ") || !strings.HasSuffix(join, "--discovery-token-ca-cert-hash sha256:"+wantHash) {
		t.Fatalf("join %q", join)
	}
	if strings.Contains(strings.Join(api.tokens, ","), "Bearer") {
		t.Fatalf("no bearer header with client certificates: %v", api.tokens)
	}

	if _, err := FromCA(p, "", time.Hour); err == nil {
		t.Fatal("empty endpoint must fail")
	}
}
