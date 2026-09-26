package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

const redactedValue = "••••"

// secretSettings are shown redacted by GET /config. joinStringFile,
// clusterCADir, k0sTokenFile and kubeconfig are paths, not the secrets
// they point at, and stay visible; the file contents never appear.
var secretSettings = []string{config.JoinString, config.GithubToken}

type settingEntry struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Source   string `json:"source"`
	Redacted bool   `json:"redacted"`
}

type templateInfo struct {
	Name           string `json:"name"`
	Source         string `json:"source"`
	Writable       bool   `json:"writable"`
	ReadOnlyReason string `json:"readOnlyReason,omitempty"`
}

type hostTemplate struct {
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	Name     string `json:"name"`
}

type configResponse struct {
	Settings  []settingEntry `json:"settings"`
	Templates struct {
		Default templateInfo   `json:"default"`
		Hosts   []hostTemplate `json:"hosts"`
	} `json:"templates"`
	ReadOnlyReason string `json:"readOnlyReason,omitempty"`
}

type templateResponse struct {
	templateInfo
	Content string `json:"content"`
}

type validateResponse struct {
	OK       bool          `json:"ok"`
	Ignition string        `json:"ignition"`
	Entries  []reportEntry `json:"entries"`
}

const (
	templateSourceFile     = "file"
	templateSourceEmbedded = "embedded"
)

var templateExtensions = []string{".yaml", ".yml", ".bu"}

func handleConfigRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var resp configResponse
	resp.Settings = effectiveSettings()
	resp.Templates.Default = defaultTemplateInfo()
	resp.Templates.Hosts = hostTemplates()
	resp.ReadOnlyReason = resp.Templates.Default.ReadOnlyReason
	writeJSON(w, http.StatusOK, resp)
}

func effectiveSettings() []settingEntry {
	settings := make([]settingEntry, 0, len(config.Keys))
	for _, key := range config.Keys {
		entry := settingEntry{Key: key, Value: config.SettingValue(key), Source: config.SettingSource(key)}
		if slices.Contains(secretSettings, key) {
			entry.Redacted = true
			if entry.Value != "" {
				entry.Value = redactedValue
			}
		}
		settings = append(settings, entry)
	}
	sort.Slice(settings, func(i, j int) bool { return settings[i].Key < settings[j].Key })
	return settings
}

func defaultTemplateInfo() templateInfo {
	name := viper.GetString(config.IgnitionFile)
	if rel, err := config.CleanRelPath(name); err == nil {
		name = rel
	}
	return newTemplateInfo(name, DefaultTemplateInUse())
}

func newTemplateInfo(rel string, embedded bool) templateInfo {
	info := templateInfo{Name: rel, Source: templateSourceFile}
	if embedded {
		info.Source = templateSourceEmbedded
	}
	info.Writable, info.ReadOnlyReason = config.ProbeWritable(config.DataPath(rel))
	return info
}

func hostTemplates() []hostTemplate {
	hosts := []hostTemplate{}
	for mac, h := range hardware.Snapshot().Hosts {
		if h.IgnitionFile != "" {
			hosts = append(hosts, hostTemplate{MAC: mac, Hostname: h.Hostname, Name: h.IgnitionFile})
		}
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].MAC < hosts[j].MAC })
	return hosts
}

// templateName validates an editor-supplied template name: relative, inside
// DataDir, a Butane extension, and none of the files /data/ hides. Empty
// means the configured default template.
func templateName(name string) (string, error) {
	if name == "" {
		name = viper.GetString(config.IgnitionFile)
	}
	rel, err := config.CleanRelPath(name)
	if err != nil {
		return "", err
	}
	if !slices.Contains(templateExtensions, strings.ToLower(filepath.Ext(rel))) {
		return "", fmt.Errorf("template name must end in %s", strings.Join(templateExtensions, ", "))
	}
	if deniedDataPath(filepath.ToSlash(rel)) {
		return "", fmt.Errorf("%q is not a template", rel)
	}
	return rel, nil
}

func loadTemplate(rel string) (templateResponse, error) {
	src, err := readIgnitionTemplate(rel)
	switch {
	case err == nil:
		return templateResponse{templateInfo: newTemplateInfo(rel, false), Content: src}, nil
	case errors.Is(err, fs.ErrNotExist) && rel == config.DefaultIgnitionFile && DefaultTemplateInUse():
		return templateResponse{templateInfo: newTemplateInfo(rel, true), Content: defaultButane}, nil
	default:
		return templateResponse{}, err
	}
}

func handleConfigTemplateRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rel, err := templateName(r.URL.Query().Get("name"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid template name: "+err.Error())
			return
		}
		resp, err := loadTemplate(rel)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			writeError(w, http.StatusNotFound, "template not found")
		case err != nil:
			slog.Error("Reading ignition template failed", "file", rel, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to read template")
		default:
			writeJSON(w, http.StatusOK, resp)
		}
	case http.MethodPut:
		handleConfigTemplateSave(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func handleConfigTemplateSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	rel, err := templateName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template name: "+err.Error())
		return
	}
	if result := validateTemplate(rel, req.Content, previewHost()); !result.OK {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "template does not validate", "entries": result.Entries})
		return
	}
	path := config.DataPath(rel)
	if writable, reason := config.ProbeWritable(path); !writable {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "template is read-only", "reason": reason})
		return
	}
	if err := config.WriteFileAtomic(path, []byte(req.Content), 0o644); err != nil {
		slog.Error("Writing ignition template failed", "file", rel, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to write template")
		return
	}
	slog.Info("Ignition template saved via API", "file", rel, "size_bytes", len(req.Content), "remote", r.RemoteAddr)
	resp, err := loadTemplate(rel)
	if err != nil {
		slog.Error("Re-reading saved ignition template failed", "file", rel, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read template")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func handleConfigTemplateValidateRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		MAC     string `json:"mac"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	rel, err := templateName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template name: "+err.Error())
		return
	}
	host := previewHost()
	if req.MAC != "" {
		mac, err := hardware.NormalizeMAC(req.MAC)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		registered, ok := hardware.Get(mac)
		if !ok {
			writeError(w, http.StatusNotFound, "host not registered")
			return
		}
		host = registered
	}
	writeJSON(w, http.StatusOK, validateTemplate(rel, req.Content, host))
}

func previewHost() *hardware.Host {
	return &hardware.Host{MAC: "00:00:00:00:00:00", Hostname: "example"}
}

// previewJoinString never mints a token: static mode resolves the configured
// join string, auto mode renders a placeholder.
func previewJoinString() string {
	if viper.GetString(config.KubeadmJoin) == config.KubeadmJoinAuto {
		return "kubeadm join <auto>"
	}
	join, err := config.StaticJoinString()
	if err != nil {
		slog.Warn("Static kubeadm join string unavailable for template preview", "error", err)
		return ""
	}
	return join
}

func validateTemplate(name, content string, host *hardware.Host) validateResponse {
	ignCfg, _, entries, err := renderButane(name, content, templateDataFor(host, previewJoinString()))
	if entries == nil {
		entries = []reportEntry{}
	}
	if err != nil {
		if !hasFatalEntry(entries) {
			entries = append(entries, reportEntry{Kind: "error", Message: err.Error()})
		}
		return validateResponse{OK: false, Entries: entries}
	}
	return validateResponse{OK: true, Ignition: string(ignCfg), Entries: entries}
}

func hasFatalEntry(entries []reportEntry) bool {
	return slices.ContainsFunc(entries, func(e reportEntry) bool { return e.Kind == "error" })
}
