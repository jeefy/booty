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
	"strings"
	"sync"
	"text/template"
	"time"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	ignTypes "github.com/coreos/ignition/v2/config/v3_4/types"
	v3_5 "github.com/coreos/ignition/v2/config/v3_5"
	coreOSType "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/autopilot/controller"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	ign "github.com/jeefy/booty/pkg/ignition"
	"github.com/jeefy/booty/pkg/power"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

// arpTimeout bounds the ARP fallback of a request without ?mac=, so a
// silent IP cannot hold a handler; arpNegativeTTL is how long a failed
// lookup is remembered per IP, and arpNegativeMax bounds that memory.
const (
	arpTimeout     = 500 * time.Millisecond
	arpNegativeTTL = 30 * time.Second
	arpNegativeMax = 1024
)

func init() { arping.SetTimeout(arpTimeout) }

// arpLookup is the raw lookup, a test seam.
var arpLookup = func(ip net.IP) (net.HardwareAddr, error) {
	hw, _, err := arping.Ping(ip)
	return hw, err
}

var errARPTimeout = errors.New("ARP lookup timed out")

// arpFailures remembers IPs whose lookup failed recently, so a client that
// never answers ARP costs one timeout per arpNegativeTTL, not one per
// request.
var arpFailures = struct {
	sync.Mutex
	m   map[string]time.Time
	now func() time.Time
}{m: map[string]time.Time{}, now: time.Now}

// resolveARP runs arpLookup with arpTimeout as deadline, answering from
// and feeding the negative cache.
func resolveARP(ip net.IP) (net.HardwareAddr, error) {
	key := ip.String()
	arpFailures.Lock()
	now := arpFailures.now()
	if until, ok := arpFailures.m[key]; ok {
		if now.Before(until) {
			arpFailures.Unlock()
			return nil, fmt.Errorf("ARP lookup of %s failed less than %s ago", key, arpNegativeTTL)
		}
		delete(arpFailures.m, key)
	}
	arpFailures.Unlock()

	type result struct {
		hw  net.HardwareAddr
		err error
	}
	done := make(chan result, 1)
	lookup := arpLookup
	go func() {
		hw, err := lookup(ip)
		done <- result{hw, err}
	}()
	var res result
	timer := time.NewTimer(arpTimeout)
	select {
	case res = <-done:
		timer.Stop()
	case <-timer.C:
		res.err = errARPTimeout
	}
	if res.err == nil {
		return res.hw, nil
	}
	arpFailures.Lock()
	defer arpFailures.Unlock()
	now = arpFailures.now()
	if len(arpFailures.m) >= arpNegativeMax {
		for k, until := range arpFailures.m {
			if !now.Before(until) {
				delete(arpFailures.m, k)
			}
		}
		for k := range arpFailures.m {
			if len(arpFailures.m) < arpNegativeMax {
				break
			}
			delete(arpFailures.m, k)
		}
	}
	arpFailures.m[key] = now.Add(arpNegativeTTL)
	return nil, res.err
}

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
	hw, err := resolveARP(ip)
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

// hostOSTreeImage is the image a host's rebase template gets: its
// registered ostreeImage, fetched from its own registry.
func hostOSTreeImage(host *hardware.Host) string {
	if host == nil {
		return ""
	}
	return host.OSTreeImage
}

func (s *Server) handleIPXERequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mac, ok := identifyClient(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid mac address")
		return
	}
	secureBoot := r.URL.Query().Get("sb") == "1"
	host := recordSecureBoot(mac, lookupHost(mac, remoteIP(r)), secureBoot, isPreview(r))

	vars := tftp.TemplateVars{
		Server:                   config.ServerHostPort(),
		MenuDefault:              tftp.MenuDefaultForHost(host),
		CoreOSChannel:            viper.GetString(config.CoreOSChannel),
		CoreOSArch:               viper.GetString(config.CoreOSArchitecture),
		CoreOSVersion:            state.CurrentCoreOSVersion(),
		OSTreeImage:              hostOSTreeImage(host),
		SecureBoot:               secureBoot,
		SecureBootTrustedFlatcar: secureBootTrustedFlatcar(),
	}
	if host != nil {
		vars.Hostname = host.Hostname
		setReleaseVars(host, &vars)
	}
	if ca, ok := versions.CurrentFlatcarCA(); ok {
		vars.FlatcarCASha256 = ca.Sha256
	}
	os := tftp.OSForHost(host)
	if host != nil && !versions.OSTracked(os) {
		vars.UntrackedReason = versions.UntrackedReason(os)
	}
	switch {
	case vars.UntrackedReason != "":
		slog.Warn("Host's OS is not tracked; serving the refusal menu", "mac", mac, "os", os)
	case tftp.SecureBootRefused(os, vars):
		slog.Warn("Secure Boot host cannot boot its OS; serving the refusal menu", "mac", mac, "os", os, "trustedFlatcar", vars.SecureBootTrustedFlatcar, "doInstall", host.DoInstall)
	case os == "bluefin":
		bluefinIPXEVars(mac, host, secureBoot || host.SecureBoot, &vars)
	}
	if host != nil && os != "bluefin" && !isPreview(r) {
		if fromHost(r, host) {
			learnIP(mac, host, remoteIP(r))
			s.powerFetch(mac, power.FetchKernel)
			s.autopilotFetch(mac, controller.FetchKernel)
		} else {
			slog.Info("Boot script fetched from an address that is not the host's; not counting it as a boot", "mac", mac, "ip", remoteIP(r), "hostIP", host.IP)
		}
	}
	slog.Info("Serving iPXE script", "mac", mac, "os", os, "menuDefault", vars.MenuDefault, "secureBoot", vars.SecureBoot)
	writeText(w, http.StatusOK, tftp.IPXEScript(os, vars))
}

// setReleaseVars points the Flatcar and CoreOS boot scripts at the release
// the host boots (its targetVersion, else the fleet target), in its
// data/<os>/<version>/ directory.
func setReleaseVars(host *hardware.Host, vars *tftp.TemplateVars) {
	target := versions.EffectiveTarget(host)
	switch tftp.OSForHost(host) {
	case versions.OSFlatcar:
		if versions.ReleaseCached(versions.OSFlatcar, target) {
			vars.FlatcarDir = versions.FlatcarDataDir(target)
		}
	case versions.OSCoreOS:
		if target != "" {
			vars.CoreOSVersion = target
		}
		if versions.ReleaseCached(versions.OSCoreOS, target) {
			vars.CoreOSDir = versions.CoreOSDataDir(target)
		}
	}
}

func readIgnitionTemplate(name string) (string, error) {
	t, err := cachedIgnitionTemplate(name)
	if err != nil {
		return "", err
	}
	return t.src, nil
}

// parsedTemplate is a Butane template's source and its parsed Go template
// (or the parse error). It is host-independent and safe to share: executing
// a text/template concurrently is safe.
type parsedTemplate struct {
	name  string
	src   string
	tmpl  *template.Template
	parse error
}

func parseTemplate(name, src string) *parsedTemplate {
	t, err := template.New(name).Parse(src)
	return &parsedTemplate{name: name, src: src, tmpl: t, parse: err}
}

type templateCacheEntry struct {
	size    int64
	modTime time.Time
	inode   uint64
	t       *parsedTemplate
}

// maxCachedTemplates bounds templateCache: the default template plus the
// per-host ignitionFiles in use.
const maxCachedTemplates = 64

// templateCache holds the parsed Butane templates by data directory and
// cleaned path; an entry is reused only while the file's size, mtime and
// inode are unchanged, so edits on disk are picked up on the next render.
var templateCache = struct {
	sync.Mutex
	m map[string]templateCacheEntry
}{m: map[string]templateCacheEntry{}}

func cachedIgnitionTemplate(name string) (*parsedTemplate, error) {
	rel, err := config.CleanRelPath(name)
	if err != nil {
		return nil, fmt.Errorf("ignition file %q: %w", name, err)
	}
	dataDir := viper.GetString(config.DataDir)
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	defer config.CloseQuietly(root, "data root")
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer config.CloseQuietly(f, rel)
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	key := templateCacheKey(dataDir, rel)
	templateCache.Lock()
	e, ok := templateCache.m[key]
	templateCache.Unlock()
	if ok && e.size == info.Size() && e.modTime.Equal(info.ModTime()) && e.inode == fileInode(info) {
		return e.t, nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	t := parseTemplate(name, string(data))
	templateCache.Lock()
	if len(templateCache.m) >= maxCachedTemplates {
		clear(templateCache.m)
	}
	templateCache.m[key] = templateCacheEntry{size: info.Size(), modTime: info.ModTime(), inode: fileInode(info), t: t}
	templateCache.Unlock()
	return t, nil
}

func templateCacheKey(dataDir, rel string) string {
	return dataDir + "\x00" + rel
}

// forgetCachedTemplate drops the cache entry of rel (a cleaned path under
// the data directory), so the next render rereads the file.
func forgetCachedTemplate(rel string) {
	templateCache.Lock()
	delete(templateCache.m, templateCacheKey(viper.GetString(config.DataDir), rel))
	templateCache.Unlock()
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

// embeddedDefaultTemplate is defaultButane, parsed once.
var embeddedDefaultTemplate = parseTemplate("embedded-default", defaultButane)

func loadIgnitionTemplate(host *hardware.Host) (name string, t *parsedTemplate, err error) {
	name = viper.GetString(config.IgnitionFile)
	if host.IgnitionFile != "" {
		name = host.IgnitionFile
	}
	t, err = cachedIgnitionTemplate(name)
	if err != nil && host.IgnitionFile == "" && name == config.DefaultIgnitionFile && errors.Is(err, fs.ErrNotExist) {
		return embeddedDefaultTemplate.name, embeddedDefaultTemplate, nil
	}
	return name, t, err
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

// handleIgnitionRequest serves the config a booting machine fetches. For a
// registered host it is a wrapper that tells Ignition to merge Booty's
// builtin fragment and then the user's rendered config (later entries win),
// unless --builtin=none in which case the user config is returned directly.
// Unregistered hosts get the brig. Boot side effects fire here only.
func (s *Server) handleIgnitionRequest(w http.ResponseWriter, r *http.Request) {
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

	if !s.renderCheck(w, host) {
		return
	}
	features := builtinFeatures()
	preview := isPreview(r)
	part := ignitionPart(r.URL.Query().Get("part"))
	if !preview {
		part = partWrapper
		s.recordBoot(mac, ip, host)
	} else {
		slog.Debug("Ignition preview; not recording boot", "mac", mac, "ip", ip, "part", part)
	}
	joinString := s.resolveJoinString(r.Context(), w, mac, host, !preview)

	user, err := renderUserIgnition(mac, host, joinString)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to render ignition config")
		return
	}

	if !features.Enabled() {
		writeJSONBody(w, r, http.StatusOK, user)
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
		writeJSONBody(w, r, http.StatusOK, user)
	case partBuiltin:
		writeJSON(w, http.StatusOK, s.builtinFragment(r.Context(), host, features, joinString, !preview))
	case partMerged:
		builtin, err := json.Marshal(s.builtinFragment(r.Context(), host, features, joinString, !preview))
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
		writeJSONBody(w, r, http.StatusOK, merged)
	default:
		writeError(w, http.StatusBadRequest, "part must be user, builtin or merged")
	}
}

func (s *Server) handleIgnitionUserRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mac, host, ok := identifyRegisteredClient(w, r)
	if !ok || !s.renderCheck(w, host) {
		return
	}
	user, err := renderUserIgnition(mac, host, s.resolveJoinString(r.Context(), w, mac, host, true))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to render ignition config")
		return
	}
	writeJSONBody(w, r, http.StatusOK, user)
}

func (s *Server) handleIgnitionBuiltinRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mac, host, ok := identifyRegisteredClient(w, r)
	if !ok || !s.renderCheck(w, host) {
		return
	}
	mint := !isPreview(r)
	writeJSON(w, http.StatusOK, s.builtinFragment(r.Context(), host, builtinFeatures(), s.resolveJoinString(r.Context(), w, mac, host, mint), mint))
}

// builtinFragment is the /ignition/builtin.json child: Booty's builtin
// pieces followed by the selected --profile. mint is false for previews,
// which must not create k0s join tokens.
func (s *Server) builtinFragment(ctx context.Context, host *hardware.Host, features ign.Features, joinString string, mint bool) ignTypes.Config {
	keys, err := ign.LoadSSHKeys(viper.GetString(config.SSHAuthorizedKeysFl), viper.GetStringSlice(config.SSHAuthorizedKeys))
	if err != nil {
		slog.Warn("Could not read SSH authorized keys file", "file", viper.GetString(config.SSHAuthorizedKeysFl), "error", err)
	}
	cfg := ign.Fragment(ign.Input{Hostname: host.Hostname, Server: config.ServerHostPort(), SSHKeys: keys, MAC: host.MAC}, features)
	return s.appendProfile(ctx, cfg, host, joinString, mint)
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
	name, t, err := loadIgnitionTemplate(host)
	if err != nil {
		slog.Error("Reading ignition template failed", "mac", mac, "file", name, "error", err)
		return nil, err
	}
	rendered, err := renderParsedIgnition(t, host, joinString)
	if err != nil {
		slog.Error("Rendering ignition failed", "mac", mac, "file", name, "error", err)
		return nil, err
	}
	return rendered, nil
}

func renderIgnition(name, src string, host *hardware.Host, joinString string) ([]byte, error) {
	return renderParsedIgnition(parseTemplate(name, src), host, joinString)
}

func renderParsedIgnition(t *parsedTemplate, host *hardware.Host, joinString string) ([]byte, error) {
	ignCfg, rendered, entries, err := renderParsedButane(t, templateDataFor(host, joinString))
	if err != nil {
		if rendered != "" {
			slog.Error("Butane template contents", "template", rendered)
			for _, entry := range entries {
				slog.Error("Butane report entry", "entry", entry.text)
			}
		}
		return nil, err
	}
	if len(entries) > 0 {
		slog.Warn("Problems translating butane config", "report", reportText(entries))
	}
	return ignCfg, nil
}

type templateData struct {
	JoinString  string
	ServerIP    string
	OSTreeImage string
	Hostname    string
}

func templateDataFor(host *hardware.Host, joinString string) templateData {
	return templateData{
		JoinString:  joinString,
		ServerIP:    config.ClientRegistry(),
		Hostname:    host.Hostname,
		OSTreeImage: hostOSTreeImage(host),
	}
}

// reportEntry is one Butane validation message. Message carries the
// location ("at $.storage.files.0, line 6 col 9: ...") so an editor can
// point at it; text is Butane's own rendering, kept for the boot-path log.
type reportEntry struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	text    string
}

func reportText(entries []reportEntry) string {
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e.text)
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderButane executes the Go template in src and translates the result
// with Butane. rendered is the executed template (empty when the template
// itself failed to parse or execute); err covers template errors,
// translation errors and fatal reports, with the entries explaining why.
func renderButane(name, src string, data templateData) (ignCfg []byte, rendered string, entries []reportEntry, err error) {
	return renderParsedButane(parseTemplate(name, src), data)
}

// renderParsedButane is renderButane for an already parsed template; only
// the execution and the Butane translation are per host.
func renderParsedButane(t *parsedTemplate, data templateData) (ignCfg []byte, rendered string, entries []reportEntry, err error) {
	if t.parse != nil {
		return nil, "", nil, fmt.Errorf("parsing template: %w", t.parse)
	}
	var tpl bytes.Buffer
	if err := t.tmpl.Execute(&tpl, data); err != nil {
		return nil, "", nil, fmt.Errorf("executing template: %w", err)
	}
	rendered = tpl.String()

	ignCfg, report, err := butaneConfig.TranslateBytes(tpl.Bytes(), butaneCommon.TranslateBytesOptions{Pretty: true})
	for _, e := range report.Entries {
		text := e.String()
		msg := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, e.Kind.String()), ":"))
		entries = append(entries, reportEntry{Kind: e.Kind.String(), Message: msg, text: text})
	}
	if err != nil {
		return nil, rendered, entries, fmt.Errorf("translating butane: %w", err)
	}
	if report.IsFatal() {
		return nil, rendered, entries, fmt.Errorf("butane report is fatal: %s", report.String())
	}
	return ignCfg, rendered, entries, nil
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

// fromHost reports whether a kernel/UKI fetch came from the host it names:
// from the host's recorded IP, or from anywhere while Booty knows no IP
// for the host yet (its first boot, which then teaches it one through
// learnIP). Only such a fetch counts as the host booting, L0 for the
// autopilot's health gate and the power tracker; an operator's curl of
// /booty.ipxe?mac=… or of the Bluefin UKI from a workstation is served but
// observed by nobody and leaves the host's IP alone. Two limitations
// follow: while the IP is unknown any address passes, and a host whose
// address changed between boots has that boot's kernel fetch ignored until
// its Ignition fetch or POST /booted relearns the address (the next boot
// is gated as usual). Previews never count.
func fromHost(r *http.Request, host *hardware.Host) bool {
	if host == nil || isPreview(r) {
		return false
	}
	return host.IP == "" || sameIP(remoteIP(r), host.IP)
}

// sameIP compares two textual addresses as IPs (so an IPv4-mapped IPv6
// form equals its IPv4), falling back to string equality.
func sameIP(a, b string) bool {
	if a == b {
		return true
	}
	ia, ib := net.ParseIP(a), net.ParseIP(b)
	return ia != nil && ib != nil && ia.Equal(ib)
}

// learnIP records the address a host without one fetched its kernel from,
// so the rest of its boot (and its next ones) can be matched against it.
// A host with an IP is left alone: only its Ignition fetch and POST
// /booted move it.
func learnIP(mac string, host *hardware.Host, ip string) {
	if host.IP != "" || ip == "" {
		return
	}
	if _, err := hardware.Update(mac, func(h *hardware.Host) {
		if h.IP == "" {
			h.IP = ip
		}
	}); err != nil {
		slog.Error("Could not record the host's IP", "mac", mac, "error", err)
		return
	}
	host.IP = ip
	slog.Info("Learned the host's IP from its kernel fetch", "mac", mac, "ip", ip)
}

func (s *Server) recordBoot(mac, ip string, host *hardware.Host) {
	if err := hardware.MarkBooted(mac, ip, time.Now()); err != nil {
		slog.Error("Could not record boot", "mac", mac, "error", err)
	}
	s.powerFetch(mac, power.FetchIgnition)
	s.autopilotFetch(mac, controller.FetchIgnition)
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
