package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/creds"
	"github.com/jeefy/booty/pkg/hardware"
	ign "github.com/jeefy/booty/pkg/ignition"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

const (
	credsPathPrefix = "/creds/"
	credsTarSuffix  = ".tar"
	credsSumSuffix  = ".tar.sha256"
)

func credsURL(mac string) string {
	return "http://" + config.ServerHostPort() + credsPathPrefix + mac + credsTarSuffix
}

// hostCredentials renders the credentials bundle for host with the current
// --builtin toggles and SSH keys.
func hostCredentials(host *hardware.Host) ([]byte, error) {
	keys, err := ign.LoadSSHKeys(viper.GetString(config.SSHAuthorizedKeysFl), viper.GetStringSlice(config.SSHAuthorizedKeys))
	if err != nil {
		slog.Warn("Could not read SSH authorized keys file", "file", viper.GetString(config.SSHAuthorizedKeysFl), "error", err)
	}
	return creds.Bundle(creds.Input{Hostname: host.Hostname, Server: config.ServerHostPort(), SSHKeys: keys}, builtinFeatures())
}

// handleCredsRequest serves GET /creds/<mac>.tar (the bundle) and
// /creds/<mac>.tar.sha256 (its hex digest) for registered hosts.
func handleCredsRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, credsPathPrefix)
	var rawMAC string
	var wantSum bool
	switch {
	case strings.HasSuffix(name, credsSumSuffix):
		rawMAC, wantSum = strings.TrimSuffix(name, credsSumSuffix), true
	case strings.HasSuffix(name, credsTarSuffix):
		rawMAC = strings.TrimSuffix(name, credsTarSuffix)
	default:
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	mac, err := hardware.NormalizeMAC(rawMAC)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	host, ok := hardware.Get(mac)
	if !ok {
		writeError(w, http.StatusNotFound, "host not registered")
		return
	}
	bundle, err := hostCredentials(host)
	if err != nil {
		slog.Error("Rendering credentials bundle failed", "mac", mac, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to render credentials")
		return
	}
	if wantSum {
		writeText(w, http.StatusOK, creds.Sum(bundle)+"\n")
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", fmt.Sprint(len(bundle)))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(bundle); err != nil {
		slog.Debug("Writing credentials bundle failed", "mac", mac, "error", err)
	}
}

// bluefinVars fills the bluefin.ipxe placeholders from the served release's
// manifest and the host. Without a cached release it is empty, which makes
// tftp serve the pending menu.
func bluefinVars(mac string, host *hardware.Host) tftp.BluefinVars {
	m, ok := versions.CurrentBluefinManifest()
	if !ok {
		return tftp.BluefinVars{}
	}
	v := tftp.BluefinVars{
		Version:     m.Version,
		Vmlinuz:     m.Vmlinuz,
		Initrd:      m.Initrd,
		DDI:         m.DDI,
		DDISha256:   m.DDISha256,
		InstallDisk: host.InstallDisk,
	}
	bundle, err := hostCredentials(host)
	if err != nil {
		slog.Error("Rendering credentials bundle failed; serving install without inst.creds_*", "mac", mac, "error", err)
		return v
	}
	if len(bundle) > 0 && builtinFeatures().Enabled() {
		v.CredsURL = credsURL(mac)
		v.CredsSha256 = creds.Sum(bundle)
	}
	return v
}

// applyNextBootClear implements --doInstallClearOn=next-boot for Bluefin:
// the first /booty.ipxe fetch at least --installMinDuration after the install
// stanza was served clears doInstall (the installer rebooted into the new
// system, which PXEs again); an earlier one means the installer crashed and
// keeps install. It returns the host as it should be rendered.
func applyNextBootClear(mac string, host *hardware.Host, now time.Time) *hardware.Host {
	if host == nil || !host.DoInstall || host.OS != "bluefin" || viper.GetString(config.DoInstallClearOn) != config.ClearOnNextBoot {
		return host
	}
	served, err := time.Parse(time.RFC3339, host.InstallServedAt)
	if err != nil {
		return host
	}
	if elapsed := now.Sub(served); elapsed < viper.GetDuration(config.InstallMinDuration) {
		slog.Info("Re-PXE too soon after install was served; keeping doInstall", "mac", mac, "elapsed", elapsed.Round(time.Second), "min", viper.GetDuration(config.InstallMinDuration))
		return host
	}
	updated, err := hardware.Update(mac, func(h *hardware.Host) {
		h.DoInstall = false
		h.InstallServedAt = ""
	})
	if err != nil {
		slog.Error("Could not clear doInstall", "mac", mac, "error", err)
		return host
	}
	slog.Info("Cleared doInstall", "mac", mac, "trigger", "next boot after install")
	return updated
}

// recordInstallServed stamps installServedAt when the Bluefin install stanza
// is the menu default, so applyNextBootClear can measure from it.
func recordInstallServed(mac string, host *hardware.Host, now time.Time) {
	if host == nil || !host.DoInstall || host.OS != "bluefin" {
		return
	}
	stamp := now.UTC().Format(time.RFC3339)
	if _, err := hardware.Update(mac, func(h *hardware.Host) { h.InstallServedAt = stamp }); err != nil {
		slog.Error("Could not record install served", "mac", mac, "error", err)
	}
}
