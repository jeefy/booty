package cluster

import (
	"errors"
	"fmt"

	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/cni"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/profile"
)

// ErrNotManaged is returned by the managed-only helpers in external mode.
var ErrNotManaged = errors.New("control plane is not managed by Booty")

// ErrUnsupportedOS is wrapped by RenderCheck for a host whose OS cannot run
// the chosen distribution.
var ErrUnsupportedOS = errors.New("unsupported distribution")

// RenderCheck is the per-request refusal for hosts Booty cannot render
// under the current cluster settings; the message is meant for an HTTP 400.
// It only applies with a managed control plane: in external mode Booty
// renders whatever it did before pkg/cluster existed.
func (m *Manager) RenderCheck(hosts map[string]*hardware.Host, host *hardware.Host) error {
	if !m.Settings.Managed() {
		return nil
	}
	if !Supports(host.OS, m.Settings.Distribution) {
		return fmt.Errorf("%w %s for os %s", ErrUnsupportedOS, m.Settings.Distribution, host.OS)
	}
	if !host.IsControlPlane() || m.Settings.Distribution != Kubeadm {
		return nil
	}
	if m.Settings.ControlPlaneDisk == "" && NeedsControlPlaneDisk(host.OS) {
		return ErrNoControlPlaneDisk
	}
	if _, err := m.Endpoint(hosts); err != nil {
		return err
	}
	return nil
}

// KubeadmEndpoint is the control-plane endpoint with kubeadm's default
// port filled in.
func (m *Manager) KubeadmEndpoint(hosts map[string]*hardware.Host) (string, error) {
	endpoint, err := m.Endpoint(hosts)
	if err != nil {
		return "", err
	}
	return config.WithDefaultPort(endpoint, 6443), nil
}

// WorkerJoinString is the pre-generated `kubeadm join` for a managed
// kubeadm control plane: the persisted bootstrap token the control plane
// was initialised with and the discovery hash of Booty's CA. It needs no
// API server, so workers can render before the control plane is up.
func (m *Manager) WorkerJoinString(hosts map[string]*hardware.Host) (string, error) {
	if !m.Settings.ManagedKubeadm() || m.PKI == nil {
		return "", ErrNotManaged
	}
	endpoint, err := m.KubeadmEndpoint(hosts)
	if err != nil {
		return "", err
	}
	tok, err := m.Tokens.Current(token.PurposeKubeadmWorker)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("kubeadm join %s --token %s --discovery-token-ca-cert-hash %s", endpoint, tok.Token, m.PKI.DiscoveryHash()), nil
}

// ControlPlaneOptions assembles what the kubeadm control-plane fragment
// renders for host: the CA pair, the persisted bootstrap token and
// certificate key, the endpoint, networks, disk and the CNI install. server
// is Booty's client-facing host[:port].
func (m *Manager) ControlPlaneOptions(hosts map[string]*hardware.Host, host *hardware.Host, server string) (*profile.ControlPlaneOptions, error) {
	if !m.Settings.ManagedKubeadm() || m.PKI == nil {
		return nil, ErrNotManaged
	}
	if err := m.RenderCheck(hosts, host); err != nil {
		return nil, err
	}
	endpoint, err := m.Endpoint(hosts)
	if err != nil {
		return nil, err
	}
	tok, err := m.Tokens.Current(token.PurposeKubeadmWorker)
	if err != nil {
		return nil, err
	}
	certKey, err := m.Tokens.Current(token.PurposeKubeadmCertKey)
	if err != nil {
		return nil, err
	}
	install, err := cni.Render(string(m.Settings.CNI), m.Settings.CNIRelease, m.Settings.PodCIDR, cni.Kubeadm(profile.UnitInit))
	if err != nil {
		return nil, err
	}
	return &profile.ControlPlaneOptions{
		Server:         server,
		Endpoint:       endpoint,
		PodCIDR:        m.Settings.PodCIDR,
		ServiceCIDR:    m.Settings.ServiceCIDR,
		CACert:         m.PKI.CACert(),
		CAKey:          m.PKI.CAKey(),
		BootstrapToken: tok.Token,
		CertificateKey: certKey.Token,
		Disk:           m.Settings.ControlPlaneDisk,
		CNI:            install,
	}, nil
}
