package kubeadm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ErrUnreachable wraps every transport-level failure of Client.Do (DNS,
// connect, TLS, timeout): the API server did not answer at all, as opposed
// to answering with a status the caller must interpret (StatusError).
var ErrUnreachable = errors.New("API server unreachable")

// StatusError is a non-2xx answer from the API server, with the reason and
// message of its Status body when it sent one. It matches
// ErrForbidden/ErrNotFound/ErrTooManyRequests through errors.Is so callers
// can branch on RBAC, missing objects and PodDisruptionBudget back-off
// without parsing messages.
type StatusError struct {
	Code    int
	Reason  string
	Message string
	// RetryAfter is the Retry-After header in seconds (0 when absent), which
	// the eviction API sets on a 429 caused by a PodDisruptionBudget.
	RetryAfter int
}

// Sentinel errors StatusError matches by status code.
var (
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not found")
	ErrTooManyRequests = errors.New("too many requests")
)

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("API server returned %d %s: %s", e.Code, e.Reason, e.Message)
	}
	return fmt.Sprintf("API server returned %d", e.Code)
}

// Is lets errors.Is(err, ErrForbidden) and friends work on a StatusError.
func (e *StatusError) Is(target error) bool {
	switch target {
	case ErrForbidden:
		return e.Code == http.StatusForbidden || e.Code == http.StatusUnauthorized
	case ErrNotFound:
		return e.Code == http.StatusNotFound
	case ErrTooManyRequests:
		return e.Code == http.StatusTooManyRequests
	}
	return false
}

// Client is the plain net/http Kubernetes API client Booty shares between
// the token minter and the autopilot's cluster client: one TLS transport
// built lazily from a KubeConfig, the bearer token re-read per request
// (projected service account tokens rotate), 10 s timeouts, no client-go.
type Client struct {
	cfg KubeConfig

	once   sync.Once
	client *http.Client
	err    error
}

// NewClient returns a Client for cfg. Nothing is dialled until the first
// request; an empty APIServer makes every request fail with ErrNotInCluster.
func NewClient(cfg KubeConfig) *Client {
	return &Client{cfg: cfg}
}

// Config is the KubeConfig the client was built from.
func (c *Client) Config() KubeConfig { return c.cfg }

// APIServer is the URL the client talks to; empty outside a cluster.
func (c *Client) APIServer() string { return c.cfg.APIServer }

// CACert is the PEM CA the client verifies the API server with (CAData,
// else the contents of CAFile); nil when none is configured.
func (c *Client) CACert() []byte {
	if c.cfg.CAData != nil {
		return bytes.Clone(c.cfg.CAData)
	}
	if c.cfg.CAFile == "" {
		return nil
	}
	data, err := os.ReadFile(c.cfg.CAFile)
	if err != nil {
		slog.Debug("Reading API server CA failed", "file", c.cfg.CAFile, "error", err)
		return nil
	}
	return data
}

func (c *Client) httpClient() (*http.Client, error) {
	c.once.Do(func() {
		var pool *x509.CertPool
		ca := c.cfg.CAData
		if ca == nil && c.cfg.CAFile != "" {
			data, err := os.ReadFile(c.cfg.CAFile)
			if err != nil {
				c.err = fmt.Errorf("reading API server CA: %w", err)
				return
			}
			ca = data
		}
		if ca != nil {
			pool = x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ca) {
				c.err = errors.New("API server CA holds no certificate")
				return
			}
		}
		tlsCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		if c.cfg.ClientCertData != nil || c.cfg.ClientKeyData != nil {
			cert, err := tls.X509KeyPair(c.cfg.ClientCertData, c.cfg.ClientKeyData)
			if err != nil {
				c.err = fmt.Errorf("loading client certificate: %w", err)
				return
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		c.client = &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				Proxy:               nil,
				TLSClientConfig:     tlsCfg,
				TLSHandshakeTimeout: requestTimeout,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	})
	return c.client, c.err
}

func (c *Client) bearerToken() (string, error) {
	if c.cfg.TokenFile == "" {
		return c.cfg.Token, nil
	}
	token, err := os.ReadFile(c.cfg.TokenFile)
	if err != nil {
		return "", fmt.Errorf("reading service account token: %w", err)
	}
	return strings.TrimSpace(string(token)), nil
}

// Do sends method path (relative to the API server, query string included)
// with an optional JSON body and returns the response for the caller to
// read and close. contentType overrides application/json for the body
// (strategic-merge patches). Transport failures wrap ErrUnreachable; a
// status is never turned into an error here, see Drain and Decode.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, contentType ...string) (*http.Response, error) {
	if c.cfg.APIServer == "" {
		return nil, ErrNotInCluster
	}
	client, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	token, err := c.bearerToken()
	if err != nil {
		return nil, err
	}
	// The timeout must outlive this function: callers stream the body after
	// we return, and cancelling here would abort that read mid-way.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.APIServer, "/")+path, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		ct := "application/json"
		if len(contentType) > 0 && contentType[0] != "" {
			ct = contentType[0]
		}
		req.Header.Set("Content-Type", ct)
	}
	resp, err := client.Do(req) //nolint:bodyclose // callers close via Drain/Decode/CloseBody
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s %s: %w: %w", method, path, ErrUnreachable, err)
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// Decode sends the request and unmarshals a 2xx JSON answer of at most
// limit bytes into v; any other status is a *StatusError.
func (c *Client) Decode(ctx context.Context, method, path string, body []byte, v any, limit int64) error {
	resp, err := c.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer CloseBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StatusFromResponse(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(v); err != nil {
		return fmt.Errorf("decoding %s %s: %w", method, path, err)
	}
	return nil
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

// Drain discards a 2xx body and closes it; any other status becomes a
// *StatusError.
func Drain(resp *http.Response) error {
	defer CloseBody(resp)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return StatusFromResponse(resp)
}

// StatusFromResponse reads the Status body of a failed answer into a
// StatusError. It does not close the body.
func StatusFromResponse(resp *http.Response) *StatusError {
	var status struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(raw, &status)
	e := &StatusError{Code: resp.StatusCode, Reason: status.Reason, Message: status.Message}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		_, _ = fmt.Sscanf(ra, "%d", &e.RetryAfter)
	}
	return e
}

// CloseBody closes resp.Body, logging a failure at debug level.
func CloseBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		slog.Debug("Closing API response failed", "error", err)
	}
}
