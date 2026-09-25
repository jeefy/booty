package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"text/template"
	"time"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	ignTypes "github.com/coreos/ignition/v2/config/v3_4/types"
	v3_5 "github.com/coreos/ignition/v2/config/v3_5"
	coreOSType "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	ign "github.com/jeefy/booty/pkg/ignition"
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

// lookupHost resolves a booting client to a registered host. Unknown MACs
// are auto-registered when --autoRegister is set, otherwise recorded as
// unknown hosts. Only the boot path (/booty.ipxe, /ignition.json) calls it.
func lookupHost(mac, ip string) *hardware.Host {
	if mac == "" {
		return nil
	}
	host, found := hardware.Get(mac)
	if found {
		return host
	}
	if host := autoRegister(mac, ip); host != nil {
		return host
	}
	hardware.Observe(mac, ip)
	slog.Warn("Unknown host detected", "mac", mac, "ip", ip)
	return nil
}

func autoRegister(mac, ip string) *hardware.Host {
	os := viper.GetString(config.AutoRegister)
	if os == "" {
		return nil
	}
	if !hardware.IsValidOS(os) {
		slog.Error("Invalid --autoRegister value; not registering unknown host", "os", os, "mac", mac)
		return nil
	}
	tmpl, err := hardware.ParseHostnameTemplate(viper.GetString(config.HostnameTemplate))
	if err != nil {
		slog.Error("Invalid --hostnameTemplate; not registering unknown host", "mac", mac, "error", err)
		return nil
	}
	hostname, err := tmpl.Render(mac, ip)
	if err != nil {
		slog.Error("Hostname template did not render a valid hostname; not registering unknown host", "mac", mac, "error", err)
		return nil
	}
	for otherMAC, other := range hardware.Snapshot().Hosts {
		if other.Hostname == hostname && otherMAC != mac {
			slog.Warn("Auto-registered hostname collides with an existing host", "hostname", hostname, "mac", mac, "existing", otherMAC)
			break
		}
	}
	host, err := hardware.Put(hardware.Host{MAC: mac, Hostname: hostname, IP: ip, OS: os})
	if err != nil {
		slog.Error("Auto-registering unknown host failed", "mac", mac, "error", err)
		return nil
	}
	slog.Info("Auto-registered unknown host", "mac", mac, "ip", ip, "hostname", hostname, "os", os)
	return host
}

// resolveOSTreeImage prefers the locally cached copy of the host's OSTree
// image when the embedded registry already has it.
func resolveOSTreeImage(host *hardware.Host) string {
	if host == nil || host.OSTreeImage == "" {
		return ""
	}
	local := versions.LocalImageRef(host.OSTreeImage)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	digest, err := digestLookup(local, versions.LocalOptions(ctx)...)
	if err != nil || digest == "" {
		slog.Warn("OSTree image not in local cache yet, using upstream", "image", local, "error", err)
		return host.OSTreeImage
	}
	return versions.ClientImageRef(host.OSTreeImage)
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
	now := time.Now()
	host := applyNextBootClear(mac, lookupHost(mac, remoteIP(r)), now)

	vars := tftp.TemplateVars{
		Server:        config.ServerHostPort(),
		MenuDefault:   tftp.MenuDefaultForHost(host),
		CoreOSChannel: viper.GetString(config.CoreOSChannel),
		CoreOSArch:    viper.GetString(config.CoreOSArchitecture),
		CoreOSVersion: state.CurrentCoreOSVersion(),
		OSTreeImage:   resolveOSTreeImage(host),
	}
	if host != nil {
		vars.Hostname = host.Hostname
	}
	os := tftp.OSForHost(host)
	if os == "bluefin" {
		vars.Bluefin = bluefinVars(mac, host)
		if vars.Bluefin.Vmlinuz != "" {
			recordInstallServed(mac, host, now)
		}
	}
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

// defaultButane is rendered when the operator never created
// config/ignition.yaml. The builtin fragment supplies hostname, keys and
// units, so this only needs to be a valid, near-empty config.
const defaultButane = `variant: fcos
version: 1.5.0
storage:
  files:
    - path: /etc/hostname
      mode: 0644
      contents:
        inline: "{{ .Hostname }}\n"
`

// DefaultTemplateInUse reports whether Booty will fall back to the embedded
// Butane template because the default config/ignition.yaml is absent.
func DefaultTemplateInUse() bool {
	if viper.GetString(config.IgnitionFile) != config.DefaultIgnitionFile {
		return false
	}
	_, err := os.Stat(config.DataPath(config.DefaultIgnitionFile))
	return errors.Is(err, fs.ErrNotExist)
}

func loadIgnitionTemplate(host *hardware.Host) (name, src string, err error) {
	name = viper.GetString(config.IgnitionFile)
	if host.IgnitionFile != "" {
		name = host.IgnitionFile
	}
	src, err = readIgnitionTemplate(name)
	if err != nil && host.IgnitionFile == "" && name == config.DefaultIgnitionFile && errors.Is(err, fs.ErrNotExist) {
		return "embedded-default", defaultButane, nil
	}
	return name, src, err
}

type ignitionPart string

const (
	partWrapper ignitionPart = ""
	partUser    ignitionPart = "user"
	partBuiltin ignitionPart = "builtin"
	partMerged  ignitionPart = "merged"
)

func builtinFeatures() ign.Features {
	f, err := ign.ParseFeatures(viper.GetString(config.Builtin))
	if err != nil {
		slog.Error("Invalid --builtin value; disabling builtin fragment", "error", err)
		return ign.Features{}
	}
	return f
}

// identifyRegisteredClient resolves the request to a registered host. It
// writes 400 for an invalid MAC and 404 when the host is unknown, returning
// ok=false in both cases. Unknown hosts are not recorded as unknownHosts:
// only /ignition.json and /booty.ipxe do that.
func identifyRegisteredClient(w http.ResponseWriter, r *http.Request) (mac string, host *hardware.Host, ok bool) {
	mac, ok = identifyClient(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid mac address")
		return "", nil, false
	}
	if mac == "" {
		writeError(w, http.StatusNotFound, "host not registered")
		return "", nil, false
	}
	host, found := hardware.Get(mac)
	if !found {
		writeError(w, http.StatusNotFound, "host not registered")
		return mac, nil, false
	}
	return mac, host, true
}

func writeRawJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		slog.Debug("Writing ignition response failed", "error", err)
	}
}

// handleIgnitionRequest serves the config a booting machine fetches. For a
// registered host it is a wrapper that tells Ignition to merge Booty's
// builtin fragment and then the user's rendered config (later entries win),
// unless --builtin=none in which case the user config is returned directly.
// Unregistered hosts get the brig. Boot side effects fire here only.
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

	features := builtinFeatures()
	preview := isPreview(r)
	part := ignitionPart(r.URL.Query().Get("part"))
	if !preview {
		part = partWrapper
		recordBoot(mac, ip, host)
	} else {
		slog.Debug("Ignition preview; not recording boot", "mac", mac, "ip", ip, "part", part)
	}
	joinString := resolveJoinString(r.Context(), w, mac, host, !preview)

	user, err := renderUserIgnition(mac, host, joinString)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to render ignition config")
		return
	}

	if !features.Enabled() {
		writeRawJSON(w, user)
		return
	}

	switch part {
	case partWrapper:
		version, err := ignitionVersion(user)
		if err != nil {
			slog.Error("Rendered ignition has no version", "mac", mac, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to render ignition config")
			return
		}
		writeJSON(w, http.StatusOK, mergeWrapper(version, mac))
	case partUser:
		writeRawJSON(w, user)
	case partBuiltin:
		writeJSON(w, http.StatusOK, builtinFragment(host, features, joinString))
	case partMerged:
		builtin, err := json.Marshal(builtinFragment(host, features, joinString))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode builtin fragment")
			return
		}
		merged, err := mergePreview(builtin, user)
		if err != nil {
			slog.Error("Merging ignition preview failed", "mac", mac, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to merge ignition configs: "+err.Error())
			return
		}
		writeRawJSON(w, merged)
	default:
		writeError(w, http.StatusBadRequest, "part must be user, builtin or merged")
	}
}

func handleIgnitionUserRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mac, host, ok := identifyRegisteredClient(w, r)
	if !ok {
		return
	}
	user, err := renderUserIgnition(mac, host, resolveJoinString(r.Context(), w, mac, host, true))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to render ignition config")
		return
	}
	writeRawJSON(w, user)
}

func handleIgnitionBuiltinRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mac, host, ok := identifyRegisteredClient(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, builtinFragment(host, builtinFeatures(), resolveJoinString(r.Context(), w, mac, host, true)))
}

// builtinFragment is the /ignition/builtin.json child: Booty's builtin
// pieces followed by the selected --profile.
func builtinFragment(host *hardware.Host, features ign.Features, joinString string) ignTypes.Config {
	keys, err := ign.LoadSSHKeys(viper.GetString(config.SSHAuthorizedKeysFl), viper.GetStringSlice(config.SSHAuthorizedKeys))
	if err != nil {
		slog.Warn("Could not read SSH authorized keys file", "file", viper.GetString(config.SSHAuthorizedKeysFl), "error", err)
	}
	cfg := ign.Fragment(ign.Input{Hostname: host.Hostname, Server: config.ServerHostPort(), SSHKeys: keys}, features)
	return appendProfile(cfg, host, joinString)
}

type mergeSource struct {
	Source string `json:"source"`
}

type ignitionWrapper struct {
	Ignition struct {
		Version string `json:"version"`
		Config  struct {
			Merge []mergeSource `json:"merge"`
		} `json:"config"`
	} `json:"ignition"`
}

func mergeWrapper(version, mac string) ignitionWrapper {
	base := "http://" + config.ServerHostPort() + "/ignition/"
	q := "?mac=" + url.QueryEscape(mac)
	var w ignitionWrapper
	w.Ignition.Version = version
	w.Ignition.Config.Merge = []mergeSource{
		{Source: base + "builtin.json" + q},
		{Source: base + "user.json" + q},
	}
	return w
}

func ignitionVersion(rendered []byte) (string, error) {
	var probe struct {
		Ignition struct {
			Version string `json:"version"`
		} `json:"ignition"`
	}
	if err := json.Unmarshal(rendered, &probe); err != nil {
		return "", err
	}
	if probe.Ignition.Version == "" {
		return "", errors.New("ignition.version missing")
	}
	return probe.Ignition.Version, nil
}

// mergePreview is a display-only approximation of what Ignition does on the
// node: both children are upconverted to spec 3.5 and merged with the
// builtin fragment as parent so the user's config wins on conflict.
func mergePreview(builtin, user []byte) ([]byte, error) {
	parent, rpt, err := v3_5.ParseCompatibleVersion(builtin)
	if err != nil {
		return nil, fmt.Errorf("builtin fragment: %w (%s)", err, rpt.String())
	}
	child, rpt, err := v3_5.ParseCompatibleVersion(user)
	if err != nil {
		return nil, fmt.Errorf("user config: %w (%s)", err, rpt.String())
	}
	return json.MarshalIndent(v3_5.Merge(parent, child), "", "  ")
}

func renderUserIgnition(mac string, host *hardware.Host, joinString string) ([]byte, error) {
	name, src, err := loadIgnitionTemplate(host)
	if err != nil {
		slog.Error("Reading ignition template failed", "mac", mac, "file", name, "error", err)
		return nil, err
	}
	rendered, err := renderIgnition(name, src, host, joinString)
	if err != nil {
		slog.Error("Rendering ignition failed", "mac", mac, "file", name, "error", err)
		return nil, err
	}
	return rendered, nil
}

func renderIgnition(name, src string, host *hardware.Host, joinString string) ([]byte, error) {
	t, err := template.New(name).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	templateData := struct {
		JoinString  string
		ServerIP    string
		OSTreeImage string
		Hostname    string
	}{
		JoinString:  joinString,
		ServerIP:    config.ClientRegistry(),
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

// isPreview reports whether the request is an operator looking at a rendered
// config (UI link, curl) rather than a machine booting. Previews must not
// stamp booted/ip or clear a pending doInstall.
func isPreview(r *http.Request) bool {
	return r.URL.Query().Get("preview") != ""
}

func recordBoot(mac, ip string, host *hardware.Host) {
	if err := hardware.MarkBooted(mac, ip, time.Now()); err != nil {
		slog.Error("Could not record boot", "mac", mac, "error", err)
	}
	if !host.DoInstall {
		return
	}
	if viper.GetString(config.DoInstallClearOn) != config.ClearOnIgnition {
		slog.Debug("Leaving doInstall set until POST /booted", "mac", mac)
		return
	}
	clearDoInstall(mac, "ignition fetch")
}

func clearDoInstall(mac, trigger string) {
	if _, err := hardware.Update(mac, func(h *hardware.Host) { h.DoInstall, h.InstallServedAt = false, "" }); err != nil {
		slog.Error("Could not clear doInstall", "mac", mac, "error", err)
		return
	}
	slog.Info("Cleared doInstall", "mac", mac, "trigger", trigger)
}
