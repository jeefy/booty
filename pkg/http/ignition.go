package http

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"text/template"
	"time"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	coreOSType "github.com/coreos/ignition/v2/config/v3_5_experimental/types"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

var arpLookup = func(ip net.IP) (net.HardwareAddr, error) {
	hw, _, err := arping.Ping(ip)
	return hw, err
}

var digestLookup = crane.Digest

// identifyClient returns the normalized MAC for the request, preferring the
// ?mac= query parameter and falling back to an ARP lookup of RemoteAddr.
// ok is false when the query parameter is present but invalid.
func identifyClient(r *http.Request) (mac string, ok bool) {
	mac, err := hostFromQuery(r)
	if err != nil {
		return "", false
	}
	if mac != "" {
		slog.Debug("MAC address from query", "mac", mac)
		return mac, true
	}
	ip := net.ParseIP(remoteIP(r))
	if ip == nil {
		slog.Warn("Cannot determine client IP for ARP lookup", "remote", r.RemoteAddr)
		return "", true
	}
	hw, err := arpLookup(ip)
	if err != nil {
		slog.Error("ARP lookup failed", "ip", ip, "error", err)
		return "", true
	}
	slog.Debug("MAC address from ARP", "mac", hw.String())
	return hw.String(), true
}

func lookupHost(mac, ip string) *hardware.Host {
	if mac == "" {
		return nil
	}
	host, found := hardware.Get(mac)
	if !found {
		hardware.Observe(mac, ip)
		slog.Warn("Unknown host detected", "mac", mac, "ip", ip)
		return nil
	}
	return host
}

// resolveOSTreeImage prefers the locally cached copy of the host's OSTree
// image when the embedded registry already has it.
func resolveOSTreeImage(host *hardware.Host) string {
	if host == nil || host.OSTreeImage == "" {
		return ""
	}
	local := versions.LocalImageRef(host.OSTreeImage)
	digest, err := digestLookup(local)
	if err != nil {
		slog.Warn("OSTree image not available from local cache, using upstream", "image", local, "error", err)
		return host.OSTreeImage
	}
	if digest == "" {
		slog.Warn("OSTree image not in local cache yet, using upstream", "image", local)
		return host.OSTreeImage
	}
	return local
}

func handleIPXERequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mac, ok := identifyClient(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid mac address")
		return
	}
	host := lookupHost(mac, remoteIP(r))

	vars := tftp.TemplateVars{
		Server:        config.ServerHostPort(),
		MenuDefault:   tftp.MenuDefaultForHost(host),
		CoreOSChannel: viper.GetString(config.CoreOSChannel),
		CoreOSArch:    viper.GetString(config.CoreOSArchitecture),
		CoreOSVersion: state.CurrentCoreOSVersion(),
		OSTreeImage:   resolveOSTreeImage(host),
	}
	os := tftp.OSForHost(host)
	slog.Info("Serving iPXE script", "mac", mac, "os", os, "menuDefault", vars.MenuDefault)
	writeText(w, http.StatusOK, tftp.IPXEScript(os, vars))
}

func readIgnitionTemplate(name string) (string, error) {
	rel, err := config.CleanRelPath(name)
	if err != nil {
		return "", fmt.Errorf("ignition file %q: %w", name, err)
	}
	root, err := os.OpenRoot(viper.GetString(config.DataDir))
	if err != nil {
		return "", err
	}
	defer config.CloseQuietly(root, "data root")
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer config.CloseQuietly(f, rel)
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func handleIgnitionRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mac, ok := identifyClient(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid mac address")
		return
	}
	ip := remoteIP(r)
	host := lookupHost(mac, ip)
	if host == nil {
		slog.Info("Serving brig ignition to unregistered host", "mac", mac, "ip", ip)
		writeJSON(w, http.StatusOK, brigIgnitionConfig())
		return
	}

	ignitionFile := viper.GetString(config.IgnitionFile)
	if host.IgnitionFile != "" {
		ignitionFile = host.IgnitionFile
	}
	rendered, err := renderIgnition(ignitionFile, host)
	if err != nil {
		slog.Error("Rendering ignition failed", "mac", mac, "file", ignitionFile, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to render ignition config")
		return
	}

	if err := hardware.MarkBooted(mac, ip, time.Now()); err != nil {
		slog.Error("Could not record boot", "mac", mac, "error", err)
	}
	if host.DoInstall {
		if _, err := hardware.Update(mac, func(h *hardware.Host) { h.DoInstall = false }); err != nil {
			slog.Error("Could not clear doInstall", "mac", mac, "error", err)
		} else {
			slog.Info("Cleared doInstall after ignition fetch", "mac", mac)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(rendered); err != nil {
		slog.Debug("Writing ignition response failed", "error", err)
	}
}

func renderIgnition(ignitionFile string, host *hardware.Host) ([]byte, error) {
	src, err := readIgnitionTemplate(ignitionFile)
	if err != nil {
		return nil, err
	}
	t, err := template.New(ignitionFile).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	templateData := struct {
		JoinString  string
		ServerIP    string
		OSTreeImage string
		Hostname    string
	}{
		JoinString:  viper.GetString(config.JoinString),
		ServerIP:    fmt.Sprintf("%s:%d", viper.GetString(config.ServerIP), viper.GetInt(config.ServerHttpPort)),
		Hostname:    host.Hostname,
		OSTreeImage: resolveOSTreeImage(host),
	}

	var tpl bytes.Buffer
	if err := t.Execute(&tpl, templateData); err != nil {
		return nil, fmt.Errorf("executing template: %w", err)
	}

	ignCfg, report, err := butaneConfig.TranslateBytes(tpl.Bytes(), butaneCommon.TranslateBytesOptions{Pretty: true})
	if err != nil {
		slog.Error("Butane template contents", "template", tpl.String())
		for _, entry := range report.Entries {
			slog.Error("Butane report entry", "entry", entry.String())
		}
		return nil, fmt.Errorf("translating butane: %w", err)
	}
	if len(report.Entries) > 0 {
		slog.Warn("Problems translating butane config", "report", report.String())
		if report.IsFatal() {
			return nil, fmt.Errorf("butane report is fatal: %s", report.String())
		}
	}
	return ignCfg, nil
}

// brigIgnitionConfig is served to unregistered hosts: a single systemd unit
// that reboots the machine so it keeps returning to PXE until an operator
// registers its MAC. Ignition rejects unit names without a systemd extension
// (.service, .timer, ...), so the name must keep its suffix.
func brigIgnitionConfig() *coreOSType.Config {
	enabled := true
	contents := `[Unit]
Description=Booty brig: reboot until this host is registered

[Service]
Type=oneshot
ExecStart=/usr/bin/systemctl reboot

[Install]
WantedBy=multi-user.target
`
	cfg := &coreOSType.Config{}
	cfg.Ignition.Version = coreOSType.MaxVersion.String()
	cfg.Systemd.Units = append(cfg.Systemd.Units, coreOSType.Unit{
		Name:     "booty-brig-reboot.service",
		Enabled:  &enabled,
		Contents: &contents,
	})
	return cfg
}
