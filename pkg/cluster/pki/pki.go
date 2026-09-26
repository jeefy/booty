// Package pki generates and loads the cluster certificate authority Booty
// hands to a managed control plane: the Kubernetes CA, the service-account
// signing key pair and the etcd CA (the six files k0s expects under
// /var/lib/k0s/pki; kubeadm only needs ca.crt/ca.key). Everything here is
// cluster-admin material: files are 0600 in a 0700 directory and are never
// regenerated once they exist.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"gopkg.in/yaml.v3"
)

// File names relative to the PKI directory.
const (
	CACertFile     = "ca.crt"
	CAKeyFile      = "ca.key"
	SAKeyFile      = "sa.key"
	SAPubFile      = "sa.pub"
	EtcdCACertFile = "etcd/ca.crt"
	EtcdCAKeyFile  = "etcd/ca.key"

	caCommonName     = "kubernetes"
	etcdCACommonName = "etcd-ca"
	adminCommonName  = "kubernetes-admin"
	caValidity       = 10 * 365 * 24 * time.Hour
	adminValidity    = 365 * 24 * time.Hour
	rsaBits          = 2048
)

// AdminGroups are the organisations on the admin client certificate:
// kubeadm >= 1.29 binds cluster-admin to kubeadm:cluster-admins, k0s and
// older kubeadm rely on system:masters.
var AdminGroups = []string{"kubeadm:cluster-admins", "system:masters"}

// K0sFiles are the files k0s needs besides the CA pair.
var K0sFiles = []string{SAKeyFile, SAPubFile, EtcdCACertFile, EtcdCAKeyFile}

// PKI is a loaded cluster CA directory. The CA pair is always present; the
// k0s files are optional for bring-your-own directories.
type PKI struct {
	dir    string
	caCert *x509.Certificate
	caKey  crypto.Signer
	files  map[string][]byte
}

// Load reads dir without writing anything. It needs at least ca.crt and
// ca.key (whose public keys must match); the k0s files are picked up when
// present.
func Load(dir string) (*PKI, error) {
	files := map[string][]byte{}
	for _, name := range []string{CACertFile, CAKeyFile} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("cluster CA: reading %s: %w", name, err)
		}
		files[name] = data
	}
	for _, name := range K0sFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cluster CA: reading %s: %w", name, err)
		}
		files[name] = data
	}
	return newPKI(dir, files)
}

// LoadOrCreate loads dir, creating it (0700) and any missing file (0600)
// first. Existing files are never overwritten, so a partially populated
// directory is completed rather than replaced.
func LoadOrCreate(dir string) (*PKI, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cluster CA: creating %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cluster CA: securing %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(EtcdCACertFile)), 0o700); err != nil {
		return nil, fmt.Errorf("cluster CA: creating etcd dir: %w", err)
	}
	if err := ensureCA(dir, CACertFile, CAKeyFile, caCommonName); err != nil {
		return nil, err
	}
	if err := ensureCA(dir, EtcdCACertFile, EtcdCAKeyFile, etcdCACommonName); err != nil {
		return nil, err
	}
	if err := ensureServiceAccountKeys(dir); err != nil {
		return nil, err
	}
	return Load(dir)
}

func newPKI(dir string, files map[string][]byte) (*PKI, error) {
	cert, err := parseCertificate(files[CACertFile])
	if err != nil {
		return nil, fmt.Errorf("cluster CA: %s: %w", CACertFile, err)
	}
	key, err := parsePrivateKey(files[CAKeyFile])
	if err != nil {
		return nil, fmt.Errorf("cluster CA: %s: %w", CAKeyFile, err)
	}
	if !publicKeysEqual(cert.PublicKey, key.Public()) {
		return nil, fmt.Errorf("cluster CA: %s does not match %s", CAKeyFile, CACertFile)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("cluster CA: %s is not a CA certificate", CACertFile)
	}
	return &PKI{dir: dir, caCert: cert, caKey: key, files: files}, nil
}

// Dir is the directory the PKI was loaded from.
func (p *PKI) Dir() string { return p.dir }

// CACert is the PEM-encoded CA certificate.
func (p *PKI) CACert() []byte { return cloneBytes(p.files[CACertFile]) }

// CAKey is the PEM-encoded CA private key. Only control-plane renders may
// ever see it.
func (p *PKI) CAKey() []byte { return cloneBytes(p.files[CAKeyFile]) }

// Files returns every loaded file keyed by its relative path, PEM-encoded.
func (p *PKI) Files() map[string][]byte {
	out := make(map[string][]byte, len(p.files))
	for name, data := range p.files {
		out[name] = cloneBytes(data)
	}
	return out
}

// MissingK0sFiles lists the k0s files (sa.key, sa.pub, etcd/ca.*) the
// directory does not contain.
func (p *PKI) MissingK0sFiles() []string {
	var missing []string
	for _, name := range K0sFiles {
		if _, ok := p.files[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// DiscoveryHash is the kubeadm --discovery-token-ca-cert-hash value:
// "sha256:" followed by the hex SHA-256 of the CA certificate's
// SubjectPublicKeyInfo.
func (p *PKI) DiscoveryHash() string {
	sum := sha256.Sum256(p.caCert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Fingerprint is the SHA-256 of the DER-encoded CA certificate, for display.
func (p *PKI) Fingerprint() string {
	sum := sha256.Sum256(p.caCert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NotAfter is the CA certificate's expiry.
func (p *PKI) NotAfter() time.Time { return p.caCert.NotAfter }

// AdminKubeconfig mints a one-year cluster-admin client certificate signed
// by the CA and returns a self-contained kubeconfig for
// https://<endpoint>. endpoint is host[:port]; a bare host gets :6443.
func (p *PKI) AdminKubeconfig(endpoint string) ([]byte, error) {
	if endpoint == "" {
		return nil, errors.New("admin kubeconfig: endpoint is empty")
	}
	server := "https://" + config.WithDefaultPort(endpoint, 6443)
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil, fmt.Errorf("admin kubeconfig: generating key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: adminCommonName, Organization: AdminGroups},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(adminValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, fmt.Errorf("admin kubeconfig: signing client certificate: %w", err)
	}
	return kubeconfigYAML(server, p.files[CACertFile], encodePEM("CERTIFICATE", der), encodePEM("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)))
}

type kubeconfig struct {
	APIVersion     string              `yaml:"apiVersion"`
	Kind           string              `yaml:"kind"`
	Clusters       []kubeconfigCluster `yaml:"clusters"`
	Contexts       []kubeconfigContext `yaml:"contexts"`
	CurrentContext string              `yaml:"current-context"`
	Users          []kubeconfigUser    `yaml:"users"`
}

type kubeconfigCluster struct {
	Name    string `yaml:"name"`
	Cluster struct {
		Server string `yaml:"server"`
		CAData string `yaml:"certificate-authority-data"`
	} `yaml:"cluster"`
}

type kubeconfigContext struct {
	Name    string `yaml:"name"`
	Context struct {
		Cluster string `yaml:"cluster"`
		User    string `yaml:"user"`
	} `yaml:"context"`
}

type kubeconfigUser struct {
	Name string `yaml:"name"`
	User struct {
		CertData string `yaml:"client-certificate-data"`
		KeyData  string `yaml:"client-key-data"`
	} `yaml:"user"`
}

func kubeconfigYAML(server string, caPEM, certPEM, keyPEM []byte) ([]byte, error) {
	const cluster, user, context = "kubernetes", adminCommonName, adminCommonName + "@kubernetes"
	kc := kubeconfig{APIVersion: "v1", Kind: "Config", CurrentContext: context}
	var c kubeconfigCluster
	c.Name = cluster
	c.Cluster.Server = server
	c.Cluster.CAData = base64.StdEncoding.EncodeToString(caPEM)
	var ctx kubeconfigContext
	ctx.Name = context
	ctx.Context.Cluster = cluster
	ctx.Context.User = user
	var u kubeconfigUser
	u.Name = user
	u.User.CertData = base64.StdEncoding.EncodeToString(certPEM)
	u.User.KeyData = base64.StdEncoding.EncodeToString(keyPEM)
	kc.Clusters, kc.Contexts, kc.Users = []kubeconfigCluster{c}, []kubeconfigContext{ctx}, []kubeconfigUser{u}
	out, err := yaml.Marshal(kc)
	if err != nil {
		return nil, fmt.Errorf("admin kubeconfig: %w", err)
	}
	return out, nil
}

func ensureCA(dir, certName, keyName, commonName string) error {
	certPath, keyPath := filepath.Join(dir, certName), filepath.Join(dir, keyName)
	certExists, keyExists := fileExists(certPath), fileExists(keyPath)
	switch {
	case certExists && keyExists:
		return nil
	case certExists != keyExists:
		return fmt.Errorf("cluster CA: %s and %s must both exist or both be absent in %s", certName, keyName, dir)
	}
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return fmt.Errorf("cluster CA: generating %s: %w", keyName, err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("cluster CA: self-signing %s: %w", certName, err)
	}
	if err := writeSecret(keyPath, encodePEM("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))); err != nil {
		return err
	}
	return writeSecret(certPath, encodePEM("CERTIFICATE", der))
}

func ensureServiceAccountKeys(dir string) error {
	keyPath, pubPath := filepath.Join(dir, SAKeyFile), filepath.Join(dir, SAPubFile)
	keyExists, pubExists := fileExists(keyPath), fileExists(pubPath)
	switch {
	case keyExists && pubExists:
		return nil
	case keyExists != pubExists:
		return fmt.Errorf("cluster CA: %s and %s must both exist or both be absent in %s", SAKeyFile, SAPubFile, dir)
	}
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return fmt.Errorf("cluster CA: generating %s: %w", SAKeyFile, err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("cluster CA: encoding %s: %w", SAPubFile, err)
	}
	if err := writeSecret(keyPath, encodePEM("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))); err != nil {
		return err
	}
	return writeSecret(pubPath, encodePEM("PUBLIC KEY", pub))
}

func writeSecret(path string, data []byte) error {
	if err := config.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("cluster CA: writing %s: %w", path, err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("cluster CA: generating serial: %w", err)
	}
	return serial, nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	if e, ok := a.(equaler); ok {
		return e.Equal(b)
	}
	return false
}

func parseCertificate(pemData []byte) (*x509.Certificate, error) {
	block, _ := decodePEM(pemData, "CERTIFICATE")
	if block == nil {
		return nil, errors.New("no CERTIFICATE block")
	}
	return x509.ParseCertificate(block)
}

func parsePrivateKey(pemData []byte) (crypto.Signer, error) {
	for _, typ := range []string{"RSA PRIVATE KEY", "EC PRIVATE KEY", "PRIVATE KEY"} {
		der, _ := decodePEM(pemData, typ)
		if der == nil {
			continue
		}
		switch typ {
		case "RSA PRIVATE KEY":
			return x509.ParsePKCS1PrivateKey(der)
		case "EC PRIVATE KEY":
			return x509.ParseECPrivateKey(der)
		default:
			key, err := x509.ParsePKCS8PrivateKey(der)
			if err != nil {
				return nil, err
			}
			switch k := key.(type) {
			case *rsa.PrivateKey:
				return k, nil
			case *ecdsa.PrivateKey:
				return k, nil
			case ed25519.PrivateKey:
				return k, nil
			}
			return nil, fmt.Errorf("unsupported key type %T", key)
		}
	}
	return nil, errors.New("no private key block")
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}
