package server

import (
	"net/http"
	"slices"
	"sort"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

const redactedValue = "••••"

// secretSettings are shown redacted by GET /config. joinStringFile is a
// path, not the secret, and stays visible.
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

const (
	templateSourceFile     = "file"
	templateSourceEmbedded = "embedded"
)

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
