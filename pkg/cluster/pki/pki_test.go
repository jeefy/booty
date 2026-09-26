package pki

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadOrCreateGeneratesSixFilesWithTightPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	p, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v err=%v, want 0700", info.Mode(), err)
	}
	for _, name := range append([]string{CACertFile, CAKeyFile}, K0sFiles...) {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", name, fi.Mode())
		}
	}
	if missing := p.MissingK0sFiles(); len(missing) != 0 {
		t.Fatalf("missing k0s files: %v", missing)
	}
	cert := parseCertOrFatal(t, p.CACert())
	if cert.Subject.CommonName != "kubernetes" || !cert.IsCA {
		t.Fatalf("CA subject %v isCA=%v", cert.Subject, cert.IsCA)
	}
	etcd := parseCertOrFatal(t, p.Files()[EtcdCACertFile])
	if etcd.Subject.CommonName != "etcd-ca" || !etcd.IsCA {
		t.Fatalf("etcd CA subject %v", etcd.Subject)
	}
	if !bytes.Contains(p.Files()[SAPubFile], []byte("PUBLIC KEY")) || !bytes.Contains(p.Files()[SAKeyFile], []byte("RSA PRIVATE KEY")) {
		t.Fatal("service account files must be PEM public/private keys")
	}
}

func TestLoadOrCreateIsIdempotentAndCompletesPartialDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, SAKeyFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, SAPubFile)); err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CACert(), second.CACert()) || !bytes.Equal(first.CAKey(), second.CAKey()) {
		t.Fatal("existing CA must never be regenerated")
	}
	if !bytes.Equal(first.Files()[EtcdCACertFile], second.Files()[EtcdCACertFile]) {
		t.Fatal("existing etcd CA must never be regenerated")
	}
	if bytes.Equal(first.Files()[SAKeyFile], second.Files()[SAKeyFile]) {
		t.Fatal("removed service account key must be regenerated")
	}
	if err := os.Remove(filepath.Join(dir, CAKeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil || !strings.Contains(err.Error(), "both exist") {
		t.Fatalf("a lone ca.crt must be an error, got %v", err)
	}
}

func TestLoadIsReadOnlyAndNeedsTheCAPair(t *testing.T) {
	src, err := LoadOrCreate(filepath.Join(t.TempDir(), "src"))
	if err != nil {
		t.Fatal(err)
	}
	byo := t.TempDir()
	if err := os.WriteFile(filepath.Join(byo, CACertFile), src.CACert(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(byo); err == nil || !strings.Contains(err.Error(), "cluster CA") {
		t.Fatalf("missing ca.key must fail mentioning the cluster CA, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(byo, CAKeyFile), src.CAKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(byo)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.MissingK0sFiles()) != len(K0sFiles) {
		t.Fatalf("BYO dir with only the CA pair must report all k0s files missing, got %v", p.MissingK0sFiles())
	}
	entries, err := os.ReadDir(byo)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("Load must not write into a BYO dir, found %d entries", len(entries))
	}
	if p.DiscoveryHash() != src.DiscoveryHash() || p.Fingerprint() != src.Fingerprint() {
		t.Fatal("loaded CA must hash like the source")
	}

	other, err := LoadOrCreate(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(byo, CAKeyFile), other.CAKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(byo); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched key must be rejected, got %v", err)
	}
}

func TestDiscoveryHashMatchesOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	dir := filepath.Join(t.TempDir(), "pki")
	p, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	pubkey, err := exec.Command("openssl", "x509", "-pubkey", "-noout", "-in", filepath.Join(dir, CACertFile)).Output()
	if err != nil {
		t.Fatal(err)
	}
	der := exec.Command("openssl", "pkey", "-pubin", "-outform", "der")
	der.Stdin = bytes.NewReader(pubkey)
	derOut, err := der.Output()
	if err != nil {
		t.Fatal(err)
	}
	sum := exec.Command("sha256sum")
	sum.Stdin = bytes.NewReader(derOut)
	sumOut, err := sum.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "sha256:" + strings.Fields(string(sumOut))[0]
	if got := p.DiscoveryHash(); got != want {
		t.Fatalf("DiscoveryHash %s, openssl %s", got, want)
	}
}

func TestAdminKubeconfig(t *testing.T) {
	p, err := LoadOrCreate(filepath.Join(t.TempDir(), "pki"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.AdminKubeconfig(""); err == nil {
		t.Fatal("empty endpoint must fail")
	}
	out, err := p.AdminKubeconfig("10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var kc struct {
		APIVersion     string `yaml:"apiVersion"`
		Kind           string `yaml:"kind"`
		CurrentContext string `yaml:"current-context"`
		Clusters       []struct {
			Name    string `yaml:"name"`
			Cluster struct {
				Server string `yaml:"server"`
				CAData string `yaml:"certificate-authority-data"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
		Contexts []struct {
			Name    string `yaml:"name"`
			Context struct {
				Cluster string `yaml:"cluster"`
				User    string `yaml:"user"`
			} `yaml:"context"`
		} `yaml:"contexts"`
		Users []struct {
			Name string `yaml:"name"`
			User struct {
				CertData string `yaml:"client-certificate-data"`
				KeyData  string `yaml:"client-key-data"`
			} `yaml:"user"`
		} `yaml:"users"`
	}
	if err := yaml.Unmarshal(out, &kc); err != nil {
		t.Fatal(err)
	}
	if kc.APIVersion != "v1" || kc.Kind != "Config" || len(kc.Clusters) != 1 || len(kc.Users) != 1 || len(kc.Contexts) != 1 {
		t.Fatalf("kubeconfig shape: %s", out)
	}
	if kc.Clusters[0].Cluster.Server != "https://10.0.0.1:6443" {
		t.Fatalf("server %q", kc.Clusters[0].Cluster.Server)
	}
	if kc.Contexts[0].Name != kc.CurrentContext || kc.Contexts[0].Context.User != kc.Users[0].Name || kc.Contexts[0].Context.Cluster != kc.Clusters[0].Name {
		t.Fatalf("context wiring: %s", out)
	}
	ca, err := base64.StdEncoding.DecodeString(kc.Clusters[0].Cluster.CAData)
	if err != nil || !bytes.Equal(ca, p.CACert()) {
		t.Fatal("certificate-authority-data must be the CA PEM")
	}
	certPEM, err := base64.StdEncoding.DecodeString(kc.Users[0].User.CertData)
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCertOrFatal(t, certPEM)
	if cert.Subject.CommonName != "kubernetes-admin" {
		t.Fatalf("CN %q", cert.Subject.CommonName)
	}
	orgs := strings.Join(cert.Subject.Organization, ",")
	if !strings.Contains(orgs, "kubeadm:cluster-admins") || !strings.Contains(orgs, "system:masters") {
		t.Fatalf("organizations %q", orgs)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parseCertOrFatal(t, p.CACert()))
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("admin cert must chain to the CA: %v", err)
	}
	keyPEM, err := base64.StdEncoding.DecodeString(kc.Users[0].User.KeyData)
	if err != nil || !bytes.Contains(keyPEM, []byte("RSA PRIVATE KEY")) {
		t.Fatal("client-key-data must be a PEM RSA key")
	}

	withPort, err := p.AdminKubeconfig("cp.example.org:8443")
	if err != nil || !bytes.Contains(withPort, []byte("server: https://cp.example.org:8443")) {
		t.Fatalf("explicit port must be kept: %v\n%s", err, withPort)
	}
}

func parseCertOrFatal(t *testing.T, pemData []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemData)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
