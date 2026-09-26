package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/kubeadm"
)

// ErrNotK0s is returned by K0sNodeFiles under another distribution.
var ErrNotK0s = errors.New("cluster distribution is not k0s")

// K0sNodeFiles is the single k0s renderer both the Ignition profile
// (Flatcar/CoreOS) and the Bluefin credentials bundle build on: the files,
// units and drop-ins host needs for its role, with contents shared between
// the two delivery paths. It returns nil, nil when the host gets no k0s
// pieces at all: a control-plane host under an external control plane, or
// a worker under an external control plane without a token source. The
// error is a render refusal (unsupported OS, missing --controlPlaneDisk,
// unresolved endpoint, unreadable token file). mint says whether a worker
// may have a fresh join token minted through the API (a real boot);
// previews pass false and only see the cache or the pre-shared token.
func (m *Manager) K0sNodeFiles(ctx context.Context, hosts map[string]*hardware.Host, host *hardware.Host, server string, mint bool) (*k0s.Node, error) {
	if m.Settings.Distribution != K0s {
		return nil, ErrNotK0s
	}
	if err := m.RenderCheck(hosts, host); err != nil {
		return nil, err
	}
	opts := k0s.Options{
		OS:          host.OS,
		Server:      server,
		PodCIDR:     m.Settings.PodCIDR,
		ServiceCIDR: m.Settings.ServiceCIDR,
		CNI:         string(m.Settings.CNI),
		CNIRelease:  m.Settings.CNIRelease,
	}
	if host.IsControlPlane() {
		if !m.Settings.Managed() || m.PKI == nil {
			return nil, nil
		}
		opts.Role = k0s.Controller
		endpoint, err := m.Endpoint(hosts)
		if err != nil {
			return nil, err
		}
		opts.Endpoint = endpoint
		opts.PKI = m.PKI.Files()
		if missing := m.PKI.MissingK0sFiles(); len(missing) > 0 {
			return nil, fmt.Errorf("cluster CA directory %s lacks %v, which k0s needs", m.PKI.Dir(), missing)
		}
		secrets, err := m.k0sBootstrapSecrets()
		if err != nil {
			return nil, err
		}
		opts.Secrets = secrets
		return k0s.Render(opts)
	}
	opts.Role = k0s.Worker
	tok, err := m.K0sWorkerToken(ctx, hosts, host, mint)
	if err != nil || tok == "" {
		return nil, err
	}
	opts.WorkerToken = tok
	return k0s.Render(opts)
}

// K0sWorkerToken is the encoded join token a worker writes to
// /etc/k0s/token. When a Minter is set it mints a one-hour worker
// bootstrap token through the API (or reuses the one cached for the MAC;
// with mint false only the cache is consulted) and wraps it with the
// endpoint and CA the way `k0s token pre-shared` does. When that is not
// possible it falls back like WorkerJoinString: the persisted 7-day
// pre-shared token under a managed control plane, the contents of
// --k0sTokenFile under an external one, "" when neither applies. A failed
// mint is logged at debug level: before the controller is up it is the
// expected state.
func (m *Manager) K0sWorkerToken(ctx context.Context, hosts map[string]*hardware.Host, host *hardware.Host, mint bool) (string, error) {
	if minted := m.mintedK0sWorkerToken(ctx, hosts, host, mint); minted != "" {
		return minted, nil
	}
	return m.preSharedK0sWorkerToken(hosts)
}

func (m *Manager) mintedK0sWorkerToken(ctx context.Context, hosts map[string]*hardware.Host, host *hardware.Host, mint bool) string {
	if m.Minter == nil {
		return ""
	}
	if !mint {
		cached, _ := m.Minter.Cached(host.MAC)
		return cached
	}
	endpoint, caCert, err := m.k0sJoinTarget(hosts)
	if err != nil {
		slog.Debug("Minting a k0s worker token is not possible; serving the pre-shared token", "mac", host.MAC, "error", err)
		return ""
	}
	tok, err := m.Minter.Rendered(ctx, host.MAC, host.Hostname, kubeadm.K0sWorkerSpec, func(t kubeadm.Token) (string, error) {
		return token.EncodeK0s(k0s.Worker, endpoint, caCert, t.String())
	})
	if err != nil {
		slog.Debug("Minting a k0s worker token failed; serving the pre-shared token", "mac", host.MAC, "error", err)
		return ""
	}
	return tok
}

// k0sJoinTarget is the endpoint (host[:port]) and CA a minted worker token
// wraps: Booty's own under a managed control plane; --controlPlaneEndpoint
// or the kubeconfig's server, and the kubeconfig's CA, under an external
// one.
func (m *Manager) k0sJoinTarget(hosts map[string]*hardware.Host) (endpoint string, caCert []byte, err error) {
	if m.Settings.Managed() && m.PKI != nil {
		endpoint, err = m.Endpoint(hosts)
		return endpoint, m.PKI.CACert(), err
	}
	caCert = m.Minter.CACert()
	if len(bytes.TrimSpace(caCert)) == 0 {
		return "", nil, fmt.Errorf("--%s carries no certificate-authority for the worker token", config.Kubeconfig)
	}
	if m.Settings.Endpoint != "" {
		return m.Settings.Endpoint, caCert, nil
	}
	u, err := url.Parse(m.Minter.APIServer())
	if err != nil || u.Host == "" {
		return "", nil, fmt.Errorf("--%s server %q is not a URL", config.Kubeconfig, m.Minter.APIServer())
	}
	return u.Host, caCert, nil
}

func (m *Manager) preSharedK0sWorkerToken(hosts map[string]*hardware.Host) (string, error) {
	if m.Settings.Managed() && m.PKI != nil {
		endpoint, err := m.Endpoint(hosts)
		if err != nil {
			return "", err
		}
		tok, err := m.Tokens.Current(token.PurposeK0sWorker)
		if err != nil {
			return "", err
		}
		return token.EncodeK0s(k0s.Worker, endpoint, m.PKI.CACert(), tok.Token)
	}
	if m.Settings.K0sTokenFile == "" {
		return "", nil
	}
	data, err := os.ReadFile(m.Settings.K0sTokenFile)
	if err != nil {
		return "", fmt.Errorf("reading --%s: %w", config.K0sTokenFile, err)
	}
	tok := strings.TrimSpace(string(data))
	join, err := token.ParseK0s(tok)
	if err != nil {
		return "", fmt.Errorf("--%s: %w", config.K0sTokenFile, err)
	}
	if join.Role != k0s.Worker {
		return "", fmt.Errorf("--%s holds a %s token, not a worker token", config.K0sTokenFile, join.Role)
	}
	return tok, nil
}

// k0sBootstrapSecrets renders the Secret manifest the first controller
// applies from /var/lib/k0s/manifests/booty/tokens.yaml: the worker token
// every worker joins with and the controller token (unused without HA, but
// it makes `k0s controller --token-file` with Booty's token work by hand).
func (m *Manager) k0sBootstrapSecrets() ([]byte, error) {
	var out bytes.Buffer
	for i, purpose := range []struct {
		purpose token.Purpose
		role    k0s.Role
	}{{token.PurposeK0sWorker, k0s.Worker}, {token.PurposeK0sController, k0s.Controller}} {
		e, err := m.Tokens.Current(purpose.purpose)
		if err != nil {
			return nil, err
		}
		secret, err := token.K0sBootstrapSecret(purpose.role, e.Token, e.Expires)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			out.WriteString("---\n")
		}
		out.Write(secret)
	}
	return out.Bytes(), nil
}
