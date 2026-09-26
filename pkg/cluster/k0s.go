package cluster

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

// ErrNotK0s is returned by K0sNodeFiles under another distribution.
var ErrNotK0s = errors.New("cluster distribution is not k0s")

// K0sNodeFiles is the single k0s renderer both the Ignition profile
// (Flatcar/CoreOS) and the Bluefin credentials bundle build on: the files,
// units and drop-ins host needs for its role, with contents shared between
// the two delivery paths. It returns nil, nil when the host gets no k0s
// pieces at all: a control-plane host under an external control plane, or
// a worker under an external control plane without --k0sTokenFile. The
// error is a render refusal (unsupported OS, missing --controlPlaneDisk,
// unresolved endpoint, unreadable token file).
func (m *Manager) K0sNodeFiles(hosts map[string]*hardware.Host, host *hardware.Host, server string) (*k0s.Node, error) {
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
	tok, err := m.k0sWorkerToken(hosts)
	if err != nil || tok == "" {
		return nil, err
	}
	opts.WorkerToken = tok
	return k0s.Render(opts)
}

// k0sWorkerToken is the encoded join token a worker writes to
// /etc/k0s/token: pre-shared from Booty's CA and persisted bootstrap token
// with a managed control plane, the contents of --k0sTokenFile with an
// external one, "" when neither applies.
func (m *Manager) k0sWorkerToken(hosts map[string]*hardware.Host) (string, error) {
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
