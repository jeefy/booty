package server

import (
	"context"
	"log/slog"

	ign36 "github.com/coreos/ignition/v2/config/v3_6/types"
	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/profile"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

func clusterSettings() cluster.Settings {
	if m := clusterManager; m != nil {
		return m.Settings
	}
	return cluster.FromConfig()
}

// bluefinJoinsKubeadm reports whether host's node config joins the
// external kubeadm cluster: a worker under --profile=kubeadm-worker, as for
// Flatcar, unless it runs k0s or this boot installs it to disk.
func bluefinJoinsKubeadm(mac string, host *hardware.Host) bool {
	switch {
	case !clusterSettings().BluefinKubeadmWorkers():
		return false
	case host.IsControlPlane():
		slog.Debug("kubeadm worker skipped: control-plane host of an external cluster", "mac", mac)
		return false
	case host.HasExtension(hardware.ExtensionK0s):
		slog.Warn("Bluefin host runs the k0s sysext; not joining it to the kubeadm cluster", "mac", mac)
		return false
	case bluefinInstallsNow(host):
		slog.Info("Bluefin host installs to disk on this boot; not joining it to the kubeadm cluster", "mac", mac)
		return false
	}
	return true
}

// addBluefinKubeadmWorker activates the release's kubeadm sysext and adds
// the worker units. Without that sysext it adds nothing and logs an error:
// the host still boots diskless, it just does not join.
func addBluefinKubeadmWorker(ctx context.Context, cfg *ign36.Config, mac string, host *hardware.Host, release versions.BluefinManifest, haveRelease, mint bool) {
	if !haveRelease {
		slog.Error("No Bluefin release cached; serving the node config without the kubeadm join", "mac", mac)
		return
	}
	if _, ok := release.Sysexts[versions.BluefinSysextKubeadm]; !ok {
		slog.Error("Bluefin release has no kubeadm sysext; serving the node config without the kubeadm join", "mac", mac, "version", release.Version)
		return
	}
	addBluefinExtensions(cfg, mac, release, []string{versions.BluefinSysextKubeadm})
	join, _ := kubeadmJoinString(ctx, mac, host, mint)
	worker := profile.BluefinKubeadmWorker(profile.BluefinWorkerOptions{
		Hostname:       host.Hostname,
		JoinString:     join,
		ContainerdDisk: viper.GetString(config.ContainerdDisk),
	})
	for _, f := range worker.Files {
		cfg.Storage.Files = append(cfg.Storage.Files, bluefinInlineFile(f.Path, f.Contents, f.Mode))
	}
	for _, u := range worker.Units {
		unit := ign36.Unit{Name: u.Name}
		if u.Enabled {
			unit.Enabled = boolPtr(true)
		}
		if u.Contents != "" {
			unit.Contents = strPtr(u.Contents)
		}
		for _, d := range u.Dropins {
			unit.Dropins = append(unit.Dropins, ign36.Dropin{Name: d.Name, Contents: strPtr(d.Contents)})
		}
		cfg.Systemd.Units = append(cfg.Systemd.Units, unit)
	}
}
