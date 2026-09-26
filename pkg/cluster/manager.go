package cluster

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jeefy/booty/pkg/cluster/pki"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/kubeadm"
)

// ErrNoControlPlane is returned by Endpoint when no endpoint flag is set
// and no single control-plane host can stand in for it.
var ErrNoControlPlane = errors.New("no control-plane host registered")

// ErrNoControlPlaneDisk is the render-time refusal for a managed control
// plane (kubeadm or k0s) on a PXE-booted OS without --controlPlaneDisk: the
// root filesystem is RAM, so etcd and the kubelet state would vanish on
// reboot. Bluefin installs to disk and is exempt.
var ErrNoControlPlaneDisk = fmt.Errorf("control-plane host needs --%s on a PXE-booted OS", config.ControlPlaneDisk)

// Manager ties the settings to the cluster CA (nil unless the control
// plane is managed), the persisted bootstrap tokens and, under k0s, the
// Minter K0sWorkerToken mints per-boot worker tokens with (nil: pre-shared
// tokens only).
type Manager struct {
	Settings Settings
	PKI      *pki.PKI
	Tokens   *token.Store
	Minter   *kubeadm.Minter
}

// New builds the Manager for settings. With a managed control plane it
// loads --clusterCADir read-only (which must then hold everything the
// distribution needs) or creates Booty's own CA under
// --dataDir/cluster/pki; the token store lives next to it either way.
func New(settings Settings) (*Manager, error) {
	m := &Manager{Settings: settings}
	if settings.Managed() {
		p, err := loadPKI(settings)
		if err != nil {
			return nil, err
		}
		m.PKI = p
		slog.Info("Cluster CA ready", "dir", p.Dir(), "fingerprint", p.Fingerprint(), "notAfter", p.NotAfter().Format("2006-01-02"))
	}
	store, err := token.Open(config.ClusterPath(config.ClusterTokensFile))
	if err != nil {
		return nil, fmt.Errorf("cluster tokens: %w", err)
	}
	m.Tokens = store
	return m, nil
}

func loadPKI(settings Settings) (*pki.PKI, error) {
	if settings.CADir == "" {
		return pki.LoadOrCreate(config.ClusterPath(config.ClusterPKIDir))
	}
	p, err := pki.Load(settings.CADir)
	if err != nil {
		return nil, fmt.Errorf("--%s: %w", config.ClusterCADir, err)
	}
	if settings.Distribution == K0s {
		if missing := p.MissingK0sFiles(); len(missing) > 0 {
			return nil, fmt.Errorf("--%s: cluster CA directory %s lacks %v, which k0s needs; Booty never writes into a bring-your-own directory", config.ClusterCADir, settings.CADir, missing)
		}
	}
	return p, nil
}

// ControlPlaneHosts returns the registered control-plane hosts sorted by
// MAC.
func ControlPlaneHosts(hosts map[string]*hardware.Host) []*hardware.Host {
	var cps []*hardware.Host
	for _, h := range hosts {
		if h.IsControlPlane() {
			cps = append(cps, h)
		}
	}
	sort.Slice(cps, func(i, j int) bool { return cps[i].MAC < cps[j].MAC })
	return cps
}

// Endpoint is the host[:port] every node uses for the API server:
// --controlPlaneEndpoint when set, otherwise the IP of the single
// registered control-plane host. It fails when there is none, more than
// one, or the one has no known IP yet.
func (m *Manager) Endpoint(hosts map[string]*hardware.Host) (string, error) {
	if m.Settings.Endpoint != "" {
		return m.Settings.Endpoint, nil
	}
	cps := ControlPlaneHosts(hosts)
	switch len(cps) {
	case 0:
		return "", ErrNoControlPlane
	case 1:
		if cps[0].IP == "" {
			return "", fmt.Errorf("control-plane host %s has no IP yet; set --%s", cps[0].MAC, config.ControlPlaneEndpt)
		}
		return cps[0].IP, nil
	}
	return "", fmt.Errorf("%d control-plane hosts registered; set --%s to the address they share", len(cps), config.ControlPlaneEndpt)
}

// NeedsControlPlaneDisk reports whether rendering a control plane for a
// host running os requires --controlPlaneDisk. Flatcar and Fedora CoreOS
// run from RAM when PXE-booted; Bluefin installs to disk.
func NeedsControlPlaneDisk(os string) bool {
	switch os {
	case "", "flatcar", "coreos":
		return true
	}
	return false
}

// Warnings lists configuration problems that are not startup errors
// because hosts can be registered later: a managed control plane without
// a control-plane host, more than one, hosts whose OS cannot run the
// chosen distribution and a PXE control plane without a disk.
func (m *Manager) Warnings(hosts map[string]*hardware.Host) []string {
	warnings := []string{}
	if m.Settings.Managed() {
		cps := ControlPlaneHosts(hosts)
		switch {
		case len(cps) == 0:
			warnings = append(warnings, ErrNoControlPlane.Error())
		case len(cps) > 1 && m.Settings.Endpoint == "":
			warnings = append(warnings, fmt.Sprintf("%d control-plane hosts registered but --%s is not set", len(cps), config.ControlPlaneEndpt))
		}
		if m.Settings.ControlPlaneDisk == "" {
			for _, cp := range cps {
				if NeedsControlPlaneDisk(cp.OS) {
					warnings = append(warnings, fmt.Sprintf("host %s (%s): %s", cp.MAC, cp.OS, ErrNoControlPlaneDisk))
				}
			}
		}
	}
	macs := make([]string, 0, len(hosts))
	for mac := range hosts {
		macs = append(macs, mac)
	}
	sort.Strings(macs)
	for _, mac := range macs {
		h := hosts[mac]
		if !Supports(h.OS, m.Settings.Distribution) {
			warnings = append(warnings, fmt.Sprintf("host %s (%s) unsupported under %s", mac, h.OS, m.Settings.Distribution))
		}
	}
	return warnings
}
