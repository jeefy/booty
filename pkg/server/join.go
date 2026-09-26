package server

import (
	"context"
	"log/slog"
	"net/http"

	ignTypes "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/kubeadm"
	"github.com/jeefy/booty/pkg/profile"
	"github.com/spf13/viper"
)

// WarningHeader is set on Ignition responses when Booty could not provide a
// kubeadm join token; the boot proceeds with an empty JOIN_STRING.
const WarningHeader = "X-Booty-Warning"

const joinUnavailableWarning = "kubeadm join token unavailable"

var joinMinter *kubeadm.Minter

func setJoinMinter(m *kubeadm.Minter) {
	if m == nil {
		m = kubeadm.New(kubeadm.InClusterConfig(), viper.GetDuration(config.JoinTokenTTL))
	}
	joinMinter = m
}

// resolveJoinString picks the kubeadm join string for host. In static mode
// it is --joinStringFile/--joinString. In auto mode a token is minted (or
// taken from the per-MAC cache) when mint is true; previews only ever see
// the cache. With a managed kubeadm control plane the fallback is the
// pre-generated join string (persisted bootstrap token + Booty's CA hash)
// instead of an empty one: that is the expected state before the API
// server exists, so it is not a warning. Failures are logged and reported
// through the warning header, never as an HTTP error: a worker that boots
// without joining beats one that does not boot at all. A managed
// control-plane host gets "" without minting: it initialises, it never
// joins. Under k0s nothing is minted either and no warning is raised: k0s
// workers join with /etc/k0s/token, so only an explicit static
// --joinString (a template variable the operator asked for) passes
// through.
func resolveJoinString(ctx context.Context, w http.ResponseWriter, mac string, host *hardware.Host, mint bool) string {
	if host.IsControlPlane() && clusterManager != nil && clusterManager.Settings.Managed() {
		return ""
	}
	if clusterManager != nil && clusterManager.Settings.Distribution == cluster.K0s {
		if viper.GetString(config.KubeadmJoin) == config.KubeadmJoinAuto {
			return ""
		}
		join, err := config.StaticJoinString()
		if err != nil {
			slog.Error("Static kubeadm join string unavailable", "mac", mac, "error", err)
			w.Header().Set(WarningHeader, joinUnavailableWarning)
		}
		return join
	}
	if viper.GetString(config.KubeadmJoin) != config.KubeadmJoinAuto {
		join, err := config.StaticJoinString()
		if err != nil {
			slog.Error("Static kubeadm join string unavailable", "mac", mac, "error", err)
			w.Header().Set(WarningHeader, joinUnavailableWarning)
		}
		if join == "" {
			join = managedJoinString(mac)
		}
		return join
	}
	if !profile.AppliesTo(host.OS) {
		return ""
	}
	if !mint {
		join, ok := joinMinter.Cached(mac)
		if !ok {
			join = managedJoinString(mac)
		}
		return join
	}
	join, err := joinMinter.JoinString(ctx, mac, host.Hostname)
	if err != nil {
		if pre := managedJoinString(mac); pre != "" {
			slog.Debug("Minting a kubeadm join token failed; serving the pre-generated join string", "mac", mac, "error", err)
			return pre
		}
		slog.Error("Could not mint kubeadm join token; serving empty JOIN_STRING", "mac", mac, "hostname", host.Hostname, "error", err)
		w.Header().Set(WarningHeader, joinUnavailableWarning)
		return ""
	}
	return join
}

// managedJoinString is the pre-generated join string of a managed kubeadm
// control plane, or "" when the control plane is external or the endpoint
// is not known yet.
func managedJoinString(mac string) string {
	m := clusterManager
	if m == nil || !m.Settings.ManagedKubeadm() {
		return ""
	}
	join, err := m.WorkerJoinString(hardware.Snapshot().Hosts)
	if err != nil {
		slog.Debug("Pre-generated kubeadm join string unavailable", "mac", mac, "error", err)
		return ""
	}
	return join
}

// profileOptions builds the profile input for host. Under k0s the host
// becomes a k0s node (controller or worker, from the cluster Manager) and
// no kubeadm profile applies. A managed kubeadm cluster implies the
// kubeadm-worker profile for its workers, and a role: control-plane host
// gets the control-plane options. The error is a render refusal (400):
// unsupported OS, missing --controlPlaneDisk, no endpoint. mint lets a
// k0s worker's join token be minted through the API (a real boot);
// previews pass false.
func profileOptions(ctx context.Context, host *hardware.Host, joinString string, mint bool) (profile.Options, error) {
	opts := profile.Options{
		Profile:         viper.GetString(config.Profile),
		K8sVersion:      viper.GetString(config.K8sVersion),
		CNIVersion:      viper.GetString(config.CNIVersion),
		CrictlVersion:   viper.GetString(config.CrictlVersion),
		ContainerdDisk:  viper.GetString(config.ContainerdDisk),
		KubeletUnitsURL: viper.GetString(config.KubeletUnitsURL),
		JoinString:      joinString,
	}
	m := clusterManager
	if m != nil && m.Settings.Distribution == cluster.K0s {
		opts.Profile = ""
		if !profile.AppliesTo(host.OS) {
			return opts, nil
		}
		node, err := m.K0sNodeFiles(ctx, hardware.Snapshot().Hosts, host, config.ServerHostPort(), mint)
		if err != nil || node == nil {
			return opts, err
		}
		opts.K0s = &profile.K0sOptions{
			Node:             node,
			Version:          m.Settings.K0sVersion,
			SHA256:           k0s.PinnedSHA256(m.Settings.K0sVersion),
			ControlPlaneDisk: m.Settings.ControlPlaneDisk,
		}
		return opts, nil
	}
	if m == nil || !m.Settings.ManagedKubeadm() {
		return opts, nil
	}
	if opts.Profile == "" {
		opts.Profile = profile.KubeadmWorker
	}
	if !host.IsControlPlane() {
		return opts, nil
	}
	cp, err := m.ControlPlaneOptions(hardware.Snapshot().Hosts, host, config.ServerHostPort())
	if err != nil {
		return opts, err
	}
	opts.ControlPlane = cp
	return opts, nil
}

// renderCheck refuses hosts the cluster settings cannot render (HTTP 400)
// and returns false after writing the response.
func renderCheck(w http.ResponseWriter, host *hardware.Host) bool {
	m := clusterManager
	if m == nil {
		return true
	}
	if err := m.RenderCheck(hardware.Snapshot().Hosts, host); err != nil {
		slog.Warn("Refusing to render host under the cluster settings", "mac", host.MAC, "os", host.OS, "role", cluster.RoleOf(host), "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// appendProfile adds the selected profile's files, filesystems and units
// after the builtin ones so the merge order is builtin -> profile -> user.
func appendProfile(ctx context.Context, cfg ignTypes.Config, host *hardware.Host, joinString string, mint bool) ignTypes.Config {
	opts, err := profileOptions(ctx, host, joinString, mint)
	if err != nil {
		slog.Error("Cluster options unavailable; serving builtin fragment without the profile", "mac", host.MAC, "error", err)
		return cfg
	}
	frag, err := profile.Fragment(host, opts)
	if err != nil {
		slog.Error("Invalid profile; serving builtin fragment without it", "mac", host.MAC, "error", err)
		return cfg
	}
	cfg.Storage.Files = append(cfg.Storage.Files, frag.Storage.Files...)
	cfg.Storage.Filesystems = append(cfg.Storage.Filesystems, frag.Storage.Filesystems...)
	cfg.Systemd.Units = append(cfg.Systemd.Units, frag.Systemd.Units...)
	return cfg
}
