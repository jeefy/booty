// Package kubeadm mints short-lived bootstrap tokens through the Kubernetes
// API and turns them into `kubeadm join` command lines, so Booty never has
// to hand out a long-lived token over the unauthenticated boot network. The
// same Secret, shaped by a different Spec, is what a k0s worker join token
// wraps, so pkg/cluster mints those through the Minter too.
package kubeadm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/cluster/pki"
	"gopkg.in/yaml.v3"
)

const (
	DefaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	DefaultCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

	SecretType        = "bootstrap.kubernetes.io/token"
	DescriptionPrefix = "booty:"

	tokenAlphabet   = "abcdefghijklmnopqrstuvwxyz0123456789"
	tokenIDLen      = 6
	tokenSecretLen  = 16
	requestTimeout  = 10 * time.Second
	clusterInfoTTL  = 10 * time.Minute
	secretsPath     = "/api/v1/namespaces/kube-system/secrets"
	clusterInfoPath = "/api/v1/namespaces/kube-public/configmaps/cluster-info"
)

// ErrNotInCluster is returned when no API server address is configured
// (KUBERNETES_SERVICE_HOST unset and no explicit KubeConfig.APIServer).
var ErrNotInCluster = errors.New("not running inside a Kubernetes cluster (KUBERNETES_SERVICE_HOST unset)")

// KubeConfig is how the minter reaches the API server. Credentials are
// either a bearer token (TokenFile is re-read on every request because
// projected service account tokens rotate; Token is static) or a client
// certificate. The CA comes from CAData (PEM) or CAFile.
type KubeConfig struct {
	APIServer      string
	TokenFile      string
	Token          string
	CAFile         string
	CAData         []byte
	ClientCertData []byte
	ClientKeyData  []byte
}

// InClusterConfig builds a KubeConfig from the standard in-cluster
// environment and mount paths. APIServer is empty outside a cluster.
func InClusterConfig() KubeConfig {
	cfg := KubeConfig{TokenFile: DefaultTokenFile, CAFile: DefaultCAFile}
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host != "" {
		if port == "" {
			port = "443"
		}
		cfg.APIServer = "https://" + net.JoinHostPort(host, port)
	}
	return cfg
}

// InCluster returns a Minter for the cluster Booty runs in; outside a
// cluster every mint fails with ErrNotInCluster.
func InCluster(ttl time.Duration) *Minter {
	return New(InClusterConfig(), ttl)
}

// FromKubeconfig returns a Minter authenticating with the current context
// of the kubeconfig file at path (client certificate or bearer token).
func FromKubeconfig(path string, ttl time.Duration) (*Minter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig: %w", err)
	}
	cfg, err := ParseKubeconfig(data, filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", path, err)
	}
	return New(cfg, ttl), nil
}

// FromCA returns a Minter for a Booty-managed control plane: it
// authenticates with an admin client certificate signed by the cluster CA
// and, since it knows the CA, never has to read kube-public/cluster-info
// for the discovery hash. endpoint is host[:port] (default :6443).
func FromCA(p *pki.PKI, endpoint string, ttl time.Duration) (*Minter, error) {
	kubeconfig, err := p.AdminKubeconfig(endpoint)
	if err != nil {
		return nil, err
	}
	cfg, err := ParseKubeconfig(kubeconfig, "")
	if err != nil {
		return nil, fmt.Errorf("admin kubeconfig: %w", err)
	}
	m := New(cfg, ttl)
	u, err := url.Parse(cfg.APIServer)
	if err != nil {
		return nil, err
	}
	m.info = clusterInfo{endpoint: u.Host, caHash: strings.TrimPrefix(p.DiscoveryHash(), "sha256:"), static: true}
	return m, nil
}

// ParseKubeconfig extracts the API server, CA and credentials of the
// current context from kubeconfig YAML. File references (certificate-
// authority, client-certificate, client-key, tokenFile) are resolved
// relative to dir.
func ParseKubeconfig(data []byte, dir string) (KubeConfig, error) {
	var kc struct {
		CurrentContext string `yaml:"current-context"`
		Clusters       []struct {
			Name    string `yaml:"name"`
			Cluster struct {
				Server   string `yaml:"server"`
				CAData   string `yaml:"certificate-authority-data"`
				CAFile   string `yaml:"certificate-authority"`
				Insecure bool   `yaml:"insecure-skip-tls-verify"`
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
				Token     string `yaml:"token"`
				TokenFile string `yaml:"tokenFile"`
				CertData  string `yaml:"client-certificate-data"`
				KeyData   string `yaml:"client-key-data"`
				CertFile  string `yaml:"client-certificate"`
				KeyFile   string `yaml:"client-key"`
			} `yaml:"user"`
		} `yaml:"users"`
	}
	if err := yaml.Unmarshal(data, &kc); err != nil {
		return KubeConfig{}, err
	}
	if len(kc.Contexts) == 0 {
		return KubeConfig{}, errors.New("no contexts")
	}
	ctxName := kc.CurrentContext
	if ctxName == "" {
		ctxName = kc.Contexts[0].Name
	}
	clusterName, userName := "", ""
	for _, c := range kc.Contexts {
		if c.Name == ctxName {
			clusterName, userName = c.Context.Cluster, c.Context.User
		}
	}
	if clusterName == "" {
		return KubeConfig{}, fmt.Errorf("context %q not found", ctxName)
	}
	var cfg KubeConfig
	found := false
	for _, c := range kc.Clusters {
		if c.Name != clusterName {
			continue
		}
		found = true
		cfg.APIServer = c.Cluster.Server
		if c.Cluster.Insecure {
			return KubeConfig{}, errors.New("insecure-skip-tls-verify is not supported")
		}
		var err error
		if cfg.CAData, err = pemField(c.Cluster.CAData, c.Cluster.CAFile, dir); err != nil {
			return KubeConfig{}, fmt.Errorf("cluster %s CA: %w", clusterName, err)
		}
	}
	if !found || cfg.APIServer == "" {
		return KubeConfig{}, fmt.Errorf("cluster %q has no server", clusterName)
	}
	if u, err := url.Parse(cfg.APIServer); err != nil || u.Scheme != "https" || u.Host == "" {
		return KubeConfig{}, fmt.Errorf("server %q must be an https URL", cfg.APIServer)
	}
	for _, u := range kc.Users {
		if u.Name != userName {
			continue
		}
		var err error
		cfg.Token = u.User.Token
		if u.User.TokenFile != "" {
			cfg.TokenFile = resolvePath(u.User.TokenFile, dir)
		}
		if cfg.ClientCertData, err = pemField(u.User.CertData, u.User.CertFile, dir); err != nil {
			return KubeConfig{}, fmt.Errorf("user %s client certificate: %w", userName, err)
		}
		if cfg.ClientKeyData, err = pemField(u.User.KeyData, u.User.KeyFile, dir); err != nil {
			return KubeConfig{}, fmt.Errorf("user %s client key: %w", userName, err)
		}
	}
	if cfg.Token == "" && cfg.TokenFile == "" && (cfg.ClientCertData == nil || cfg.ClientKeyData == nil) {
		return KubeConfig{}, fmt.Errorf("user %q has neither a token nor a client certificate and key", userName)
	}
	return cfg, nil
}

func pemField(b64, file, dir string) ([]byte, error) {
	switch {
	case b64 != "":
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("decoding base64: %w", err)
		}
		return data, nil
	case file != "":
		return os.ReadFile(resolvePath(file, dir))
	}
	return nil, nil
}

func resolvePath(p, dir string) string {
	if filepath.IsAbs(p) || dir == "" {
		return p
	}
	return filepath.Join(dir, p)
}

type cachedJoin struct {
	join    string
	expires time.Time
}

// Spec is the part of a bootstrap-token Secret that differs between the
// flavours Booty mints. Usages become `usage-bootstrap-<usage>: "true"`;
// ExtraGroups is `auth-extra-groups` and is left out when empty. Token id,
// secret, expiration and the `booty:` description are the same for all.
type Spec struct {
	Usages      []string
	ExtraGroups string
}

var (
	// KubeadmSpec is what `kubeadm token create` writes: authentication
	// and signing usages, and the group kubeadm's node RBAC is bound to.
	KubeadmSpec = Spec{Usages: []string{"authentication", "signing"}, ExtraGroups: "system:bootstrappers:kubeadm:default-node-token"}
	// K0sWorkerSpec is what `k0s token pre-shared --role worker` writes
	// (k0s pkg/token/manager.go): authentication only and no extra groups,
	// since k0s binds the implicit system:bootstrappers group.
	K0sWorkerSpec = Spec{Usages: []string{"authentication"}}
)

// Token is a minted bootstrap token.
type Token struct {
	ID      string
	Secret  string
	Expires time.Time
}

// String is the <id>.<secret> form nodes present as a bearer token.
func (t Token) String() string { return t.ID + "." + t.Secret }

type clusterInfo struct {
	endpoint string
	caHash   string
	fetched  time.Time
	static   bool
}

// Minter creates bootstrap tokens and caches the resulting join strings per
// MAC for half the token TTL, so the wrapper fetch and the child fetches of
// one boot (and quick retries) share a single token.
type Minter struct {
	cfg KubeConfig
	ttl time.Duration
	now func() time.Time

	clientOnce sync.Once
	client     *http.Client
	clientErr  error

	mu    sync.Mutex
	cache map[string]cachedJoin
	locks map[string]*sync.Mutex
	info  clusterInfo
}

// New returns a Minter; ttl is the bootstrap token lifetime.
func New(cfg KubeConfig, ttl time.Duration) *Minter {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Minter{cfg: cfg, ttl: ttl, now: time.Now, cache: map[string]cachedJoin{}, locks: map[string]*sync.Mutex{}}
}

// TTL is the token lifetime the minter writes into each Secret.
func (m *Minter) TTL() time.Duration { return m.ttl }

// APIServer is the URL the minter talks to; empty outside a cluster.
func (m *Minter) APIServer() string { return m.cfg.APIServer }

// CACert is the PEM CA the minter verifies the API server with: the
// kubeconfig's certificate-authority(-data), Booty's cluster CA under
// FromCA, or the service account's ca.crt in-cluster. It is what a k0s
// join token embeds for the worker. nil when none is configured.
func (m *Minter) CACert() []byte {
	if m.cfg.CAData != nil {
		return bytes.Clone(m.cfg.CAData)
	}
	if m.cfg.CAFile == "" {
		return nil
	}
	data, err := os.ReadFile(m.cfg.CAFile)
	if err != nil {
		slog.Debug("Reading API server CA failed", "file", m.cfg.CAFile, "error", err)
		return nil
	}
	return data
}

// Cached returns the string minted earlier for mac (a kubeadm join string
// or a rendered k0s token) if it is still within the cache window. It
// never talks to the API server.
func (m *Minter) Cached(mac string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cache[mac]
	if !ok || !m.now().Before(e.expires) {
		delete(m.cache, mac)
		return "", false
	}
	return e.join, true
}

// JoinString returns the cached join string for mac or mints a new kubeadm
// token. hostname only ends up in the Secret's description.
func (m *Minter) JoinString(ctx context.Context, mac, hostname string) (string, error) {
	return m.cachedOr(mac, func() (string, error) { return m.mint(ctx, mac, hostname) })
}

// Rendered returns the cached string for mac or mints a token shaped by
// spec, hands it to render and caches the result for ttl/2, exactly as
// JoinString does for kubeadm. The render error is returned as-is and
// nothing is cached then; the Secret it minted expires on its own.
func (m *Minter) Rendered(ctx context.Context, mac, hostname string, spec Spec, render func(Token) (string, error)) (string, error) {
	return m.cachedOr(mac, func() (string, error) {
		tok, err := m.Mint(ctx, mac, hostname, spec)
		if err != nil {
			return "", err
		}
		return render(tok)
	})
}

func (m *Minter) cachedOr(mac string, mint func() (string, error)) (string, error) {
	lock := m.macLock(mac)
	lock.Lock()
	defer lock.Unlock()
	if join, ok := m.Cached(mac); ok {
		return join, nil
	}
	join, err := mint()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.cache[mac] = cachedJoin{join: join, expires: m.now().Add(m.ttl / 2)}
	m.mu.Unlock()
	return join, nil
}

func (m *Minter) macLock(mac string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[mac]
	if !ok {
		l = &sync.Mutex{}
		m.locks[mac] = l
	}
	return l
}

func (m *Minter) mint(ctx context.Context, mac, hostname string) (string, error) {
	info, err := m.clusterInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("cluster-info: %w", err)
	}
	tok, err := m.Mint(ctx, mac, hostname, KubeadmSpec)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("kubeadm join %s --token %s --discovery-token-ca-cert-hash sha256:%s", info.endpoint, tok, info.caHash), nil
}

// Mint creates the kube-system Secret for a fresh bootstrap token shaped by
// spec, expiring after the minter's TTL, with the description
// `booty: <hostname> <mac>` that Cleanup recognises. It never consults the
// cache.
func (m *Minter) Mint(ctx context.Context, mac, hostname string, spec Spec) (Token, error) {
	id, secret, err := newToken()
	if err != nil {
		return Token{}, err
	}
	tok := Token{ID: id, Secret: secret, Expires: m.now().Add(m.ttl).UTC().Truncate(time.Second)}
	if err := m.createTokenSecret(ctx, tok, spec, hostname, mac); err != nil {
		return Token{}, fmt.Errorf("creating bootstrap token: %w", err)
	}
	slog.Info("Minted bootstrap token", "mac", mac, "hostname", hostname, "id", id, "usages", spec.Usages, "expiration", tok.Expires.Format(time.RFC3339))
	return tok, nil
}

func newToken() (id, secret string, err error) {
	id, err = randomString(tokenIDLen)
	if err != nil {
		return "", "", err
	}
	secret, err = randomString(tokenSecretLen)
	return id, secret, err
}

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = tokenAlphabet[int(b)%len(tokenAlphabet)]
	}
	return string(out), nil
}

type secretMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type secret struct {
	APIVersion string            `json:"apiVersion,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Metadata   secretMeta        `json:"metadata"`
	Type       string            `json:"type"`
	StringData map[string]string `json:"stringData,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
}

func (m *Minter) createTokenSecret(ctx context.Context, tok Token, spec Spec, hostname, mac string) error {
	data := map[string]string{
		"token-id":     tok.ID,
		"token-secret": tok.Secret,
		"expiration":   tok.Expires.UTC().Format(time.RFC3339),
		"description":  fmt.Sprintf("%s %s %s", DescriptionPrefix, hostname, mac),
	}
	for _, usage := range spec.Usages {
		data["usage-bootstrap-"+usage] = "true"
	}
	if spec.ExtraGroups != "" {
		data["auth-extra-groups"] = spec.ExtraGroups
	}
	body := secret{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata:   secretMeta{Name: "bootstrap-token-" + tok.ID, Namespace: "kube-system"},
		Type:       SecretType,
		StringData: data,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := m.do(ctx, http.MethodPost, secretsPath, raw)
	if err != nil {
		return err
	}
	return drain(resp)
}

func (m *Minter) clusterInfo(ctx context.Context) (clusterInfo, error) {
	m.mu.Lock()
	info := m.info
	m.mu.Unlock()
	if info.static || (info.endpoint != "" && m.now().Sub(info.fetched) < clusterInfoTTL) {
		return info, nil
	}
	resp, err := m.do(ctx, http.MethodGet, clusterInfoPath, nil)
	if err != nil {
		return clusterInfo{}, err
	}
	defer closeBody(resp)
	if resp.StatusCode != http.StatusOK {
		return clusterInfo{}, apiError(resp)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cm); err != nil {
		return clusterInfo{}, fmt.Errorf("decoding cluster-info: %w", err)
	}
	endpoint, caHash, err := ParseClusterInfoKubeconfig(cm.Data["kubeconfig"])
	if err != nil {
		return clusterInfo{}, err
	}
	info = clusterInfo{endpoint: endpoint, caHash: caHash, fetched: m.now()}
	m.mu.Lock()
	m.info = info
	m.mu.Unlock()
	return info, nil
}

// ParseClusterInfoKubeconfig extracts the external API endpoint (host:port)
// and the kubeadm discovery hash, sha256 of the CA certificate's
// SubjectPublicKeyInfo, from the kubeconfig published in kube-public.
func ParseClusterInfoKubeconfig(kubeconfig string) (endpoint, caHash string, err error) {
	var kc struct {
		Clusters []struct {
			Cluster struct {
				Server string `yaml:"server"`
				CAData string `yaml:"certificate-authority-data"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if kubeconfig == "" {
		return "", "", errors.New("cluster-info has no kubeconfig")
	}
	if err := yaml.Unmarshal([]byte(kubeconfig), &kc); err != nil {
		return "", "", fmt.Errorf("parsing cluster-info kubeconfig: %w", err)
	}
	if len(kc.Clusters) == 0 || kc.Clusters[0].Cluster.Server == "" {
		return "", "", errors.New("cluster-info kubeconfig has no cluster server")
	}
	u, err := url.Parse(kc.Clusters[0].Cluster.Server)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("cluster-info server %q is not a URL", kc.Clusters[0].Cluster.Server)
	}
	der, err := base64.StdEncoding.DecodeString(kc.Clusters[0].Cluster.CAData)
	if err != nil {
		return "", "", fmt.Errorf("decoding certificate-authority-data: %w", err)
	}
	caHash, err = SPKIHash(der)
	if err != nil {
		return "", "", err
	}
	return u.Host, caHash, nil
}

// SPKIHash returns the hex sha256 of the SubjectPublicKeyInfo of the first
// certificate in pem, which is what `--discovery-token-ca-cert-hash` checks.
func SPKIHash(pemData []byte) (string, error) {
	certs, err := parseCertificates(pemData)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(certs[0].RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:]), nil
}

func parseCertificates(pemData []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for rest := pemData; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing CA certificate: %w", err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, errors.New("certificate-authority-data holds no certificate")
	}
	return certs, nil
}

// Cleanup deletes bootstrap tokens Booty created (kubeadm and k0s flavours
// alike, they share the description) whose expiration has passed. Tokens
// without the "booty:" description are never touched. It also drops stale
// cache entries.
func (m *Minter) Cleanup(ctx context.Context) (deleted int, err error) {
	m.mu.Lock()
	for mac, e := range m.cache {
		if !m.now().Before(e.expires) {
			delete(m.cache, mac)
		}
	}
	m.mu.Unlock()

	resp, err := m.do(ctx, http.MethodGet, secretsPath+"?fieldSelector="+url.QueryEscape("type="+SecretType), nil)
	if err != nil {
		return 0, err
	}
	defer closeBody(resp)
	if resp.StatusCode != http.StatusOK {
		return 0, apiError(resp)
	}
	var list struct {
		Items []secret `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return 0, fmt.Errorf("decoding secret list: %w", err)
	}
	now := m.now()
	var errs []error
	for _, s := range list.Items {
		if !m.expiredBootyToken(s, now) {
			continue
		}
		del, err := m.do(ctx, http.MethodDelete, secretsPath+"/"+url.PathEscape(s.Metadata.Name), nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := drain(del); err != nil && del.StatusCode != http.StatusNotFound {
			errs = append(errs, fmt.Errorf("%s: %w", s.Metadata.Name, err))
			continue
		}
		deleted++
		slog.Info("Deleted expired bootstrap token", "secret", s.Metadata.Name)
	}
	return deleted, errors.Join(errs...)
}

func (m *Minter) expiredBootyToken(s secret, now time.Time) bool {
	if s.Type != SecretType {
		return false
	}
	desc := secretField(s, "description")
	if !strings.HasPrefix(desc, DescriptionPrefix) {
		return false
	}
	exp, err := time.Parse(time.RFC3339, secretField(s, "expiration"))
	if err != nil {
		slog.Warn("Bootstrap token has an unparseable expiration; leaving it alone", "secret", s.Metadata.Name, "error", err)
		return false
	}
	return !exp.After(now)
}

func secretField(s secret, key string) string {
	if v, ok := s.StringData[key]; ok {
		return v
	}
	raw, ok := s.Data[key]
	if !ok {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return ""
	}
	return string(b)
}

func (m *Minter) httpClient() (*http.Client, error) {
	m.clientOnce.Do(func() {
		var pool *x509.CertPool
		ca := m.cfg.CAData
		if ca == nil && m.cfg.CAFile != "" {
			data, err := os.ReadFile(m.cfg.CAFile)
			if err != nil {
				m.clientErr = fmt.Errorf("reading API server CA: %w", err)
				return
			}
			ca = data
		}
		if ca != nil {
			pool = x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ca) {
				m.clientErr = errors.New("API server CA holds no certificate")
				return
			}
		}
		tlsCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		if m.cfg.ClientCertData != nil || m.cfg.ClientKeyData != nil {
			cert, err := tls.X509KeyPair(m.cfg.ClientCertData, m.cfg.ClientKeyData)
			if err != nil {
				m.clientErr = fmt.Errorf("loading client certificate: %w", err)
				return
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		m.client = &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				Proxy:               nil,
				TLSClientConfig:     tlsCfg,
				TLSHandshakeTimeout: requestTimeout,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	})
	return m.client, m.clientErr
}

func (m *Minter) bearerToken() (string, error) {
	if m.cfg.TokenFile == "" {
		return m.cfg.Token, nil
	}
	token, err := os.ReadFile(m.cfg.TokenFile)
	if err != nil {
		return "", fmt.Errorf("reading service account token: %w", err)
	}
	return strings.TrimSpace(string(token)), nil
}

func (m *Minter) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	if m.cfg.APIServer == "" {
		return nil, ErrNotInCluster
	}
	client, err := m.httpClient()
	if err != nil {
		return nil, err
	}
	token, err := m.bearerToken()
	if err != nil {
		return nil, err
	}
	// The timeout must outlive this function: callers stream the body after
	// we return, and cancelling here would abort that read mid-way.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.cfg.APIServer, "/")+path, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req) //nolint:bodyclose // callers close via drain/closeBody
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func drain(resp *http.Response) error {
	defer closeBody(resp)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return apiError(resp)
}

func apiError(resp *http.Response) error {
	var status struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(raw, &status)
	if status.Message != "" {
		return fmt.Errorf("API server returned %d %s: %s", resp.StatusCode, status.Reason, status.Message)
	}
	return fmt.Errorf("API server returned %d", resp.StatusCode)
}

func closeBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		slog.Debug("Closing API response failed", "error", err)
	}
}
