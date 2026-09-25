package server

import (
	"context"
	"log/slog"
	"net/http"

	ignTypes "github.com/coreos/ignition/v2/config/v3_4/types"
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
// the cache. Failures are logged and reported through the warning header,
// never as an HTTP error: a worker that boots without joining beats one
// that does not boot at all.
func resolveJoinString(ctx context.Context, w http.ResponseWriter, mac string, host *hardware.Host, mint bool) string {
	if viper.GetString(config.KubeadmJoin) != config.KubeadmJoinAuto {
		join, err := config.StaticJoinString()
		if err != nil {
			slog.Error("Static kubeadm join string unavailable", "mac", mac, "error", err)
			w.Header().Set(WarningHeader, joinUnavailableWarning)
		}
		return join
	}
	if !profile.AppliesTo(host.OS) {
		return ""
	}
	if !mint {
		join, _ := joinMinter.Cached(mac)
		return join
	}
	join, err := joinMinter.JoinString(ctx, mac, host.Hostname)
	if err != nil {
		slog.Error("Could not mint kubeadm join token; serving empty JOIN_STRING", "mac", mac, "hostname", host.Hostname, "error", err)
		w.Header().Set(WarningHeader, joinUnavailableWarning)
		return ""
	}
	return join
}

func profileOptions(joinString string) profile.Options {
	return profile.Options{
		Profile:         viper.GetString(config.Profile),
		K8sVersion:      viper.GetString(config.K8sVersion),
		CNIVersion:      viper.GetString(config.CNIVersion),
		CrictlVersion:   viper.GetString(config.CrictlVersion),
		ContainerdDisk:  viper.GetString(config.ContainerdDisk),
		KubeletUnitsURL: viper.GetString(config.KubeletUnitsURL),
		JoinString:      joinString,
	}
}

// appendProfile adds the selected profile's files, filesystems and units
// after the builtin ones so the merge order is builtin -> profile -> user.
func appendProfile(cfg ignTypes.Config, host *hardware.Host, joinString string) ignTypes.Config {
	frag, err := profile.Fragment(host, profileOptions(joinString))
	if err != nil {
		slog.Error("Invalid --profile; serving builtin fragment without it", "error", err)
		return cfg
	}
	cfg.Storage.Files = append(cfg.Storage.Files, frag.Storage.Files...)
	cfg.Storage.Filesystems = append(cfg.Storage.Filesystems, frag.Storage.Filesystems...)
	cfg.Systemd.Units = append(cfg.Systemd.Units, frag.Systemd.Units...)
	return cfg
}
