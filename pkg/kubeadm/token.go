// Package kubeadm mints short-lived bootstrap tokens through the Kubernetes
// API and turns them into `kubeadm join` command lines, so Booty never has
// to hand out a long-lived token over the unauthenticated boot network.
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
	"strings"
	"sync"
	"time"

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

// KubeConfig is how the minter reaches the API server. The bearer token is
// re-read from TokenFile on every request because projected service account
// tokens rotate.
type KubeConfig struct {
	APIServer string
	TokenFile string
	CAFile    string
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

type cachedJoin struct {
	join    string
	expires time.Time
}

type clusterInfo struct {
	endpoint string
	caHash   string
	fetched  time.Time
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

// Cached returns the join string minted earlier for mac if it is still
// within the cache window. It never talks to the API server.
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

// JoinString returns the cached join string for mac or mints a new token.
// hostname only ends up in the Secret's description.
func (m *Minter) JoinString(ctx context.Context, mac, hostname string) (string, error) {
	lock := m.macLock(mac)
	lock.Lock()
	defer lock.Unlock()
	if join, ok := m.Cached(mac); ok {
		return join, nil
	}
	join, err := m.mint(ctx, mac, hostname)
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
	id, secret, err := newToken()
	if err != nil {
		return "", err
	}
	expiration := m.now().Add(m.ttl).UTC().Format(time.RFC3339)
	if err := m.createTokenSecret(ctx, id, secret, expiration, hostname, mac); err != nil {
		return "", fmt.Errorf("creating bootstrap token: %w", err)
	}
	slog.Info("Minted kubeadm bootstrap token", "mac", mac, "hostname", hostname, "id", id, "expiration", expiration)
	return fmt.Sprintf("kubeadm join %s --token %s.%s --discovery-token-ca-cert-hash sha256:%s", info.endpoint, id, secret, info.caHash), nil
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

func (m *Minter) createTokenSecret(ctx context.Context, id, secretValue, expiration, hostname, mac string) error {
	body := secret{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata:   secretMeta{Name: "bootstrap-token-" + id, Namespace: "kube-system"},
		Type:       SecretType,
		StringData: map[string]string{
			"token-id":                       id,
			"token-secret":                   secretValue,
			"expiration":                     expiration,
			"usage-bootstrap-authentication": "true",
			"usage-bootstrap-signing":        "true",
			"auth-extra-groups":              "system:bootstrappers:kubeadm:default-node-token",
			"description":                    fmt.Sprintf("%s %s %s", DescriptionPrefix, hostname, mac),
		},
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
	if info.endpoint != "" && m.now().Sub(info.fetched) < clusterInfoTTL {
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

// Cleanup deletes bootstrap tokens Booty created whose expiration has
// passed. Tokens without the "booty:" description are never touched. It
// also drops stale cache entries.
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
		slog.Info("Deleted expired kubeadm bootstrap token", "secret", s.Metadata.Name)
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
		pool := x509.NewCertPool()
		if m.cfg.CAFile != "" {
			ca, err := os.ReadFile(m.cfg.CAFile)
			if err != nil {
				m.clientErr = fmt.Errorf("reading API server CA: %w", err)
				return
			}
			if !pool.AppendCertsFromPEM(ca) {
				m.clientErr = fmt.Errorf("API server CA %s holds no certificate", m.cfg.CAFile)
				return
			}
		}
		m.client = &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				Proxy:               nil,
				TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
				TLSHandshakeTimeout: requestTimeout,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	})
	return m.client, m.clientErr
}

func (m *Minter) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	if m.cfg.APIServer == "" {
		return nil, ErrNotInCluster
	}
	client, err := m.httpClient()
	if err != nil {
		return nil, err
	}
	token, err := os.ReadFile(m.cfg.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("reading service account token: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.cfg.APIServer, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req) //nolint:bodyclose // callers close via drain/closeBody
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
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
