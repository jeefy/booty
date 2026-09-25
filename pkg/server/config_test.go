package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func decodeConfig(t *testing.T, r response) configResponse {
	t.Helper()
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("GET /config: %+v", r)
	}
	var cfg configResponse
	if err := json.Unmarshal([]byte(r.body), &cfg); err != nil {
		t.Fatalf("decode /config: %v\n%s", err, r.body)
	}
	return cfg
}

func settingByKey(t *testing.T, cfg configResponse, key string) settingEntry {
	t.Helper()
	for _, s := range cfg.Settings {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("setting %q missing from %+v", key, cfg.Settings)
	return settingEntry{}
}

func TestConfigView(t *testing.T) {
	srv, _ := newTestServer(t)
	viper.Set(config.JoinString, "kubeadm join 10.0.0.1:6443 --token secret")
	viper.Set(config.JoinStringFile, "/run/secrets/join")
	viper.Set(config.GithubToken, "")
	viper.Set(config.FlatcarChannel, "beta")
	viper.Set(config.SSHAuthorizedKeys, []string{"ssh-ed25519 AAA", "ssh-ed25519 BBB"})
	t.Setenv("BOOTY_COREOSCHANNEL", "testing")
	t.Setenv("IGNITION_FILE", "config/ignition.yaml")
	config.FlagChanged = func(key string) bool { return key == config.FlatcarChannel }
	t.Cleanup(func() { config.FlagChanged = nil })

	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/config", "{}"), http.StatusMethodNotAllowed)

	if r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","ignitionFile":"config/n1.yaml"}`); r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
	if r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:02","hostname":"n2"}`); r.status != 200 {
		t.Fatalf("register: %+v", r)
	}

	cfg := decodeConfig(t, do(t, http.MethodGet, srv.URL+"/config", ""))

	if len(cfg.Settings) != len(config.Keys) {
		t.Fatalf("expected %d settings, got %d", len(config.Keys), len(cfg.Settings))
	}
	for i := 1; i < len(cfg.Settings); i++ {
		if cfg.Settings[i-1].Key >= cfg.Settings[i].Key {
			t.Fatalf("settings must be sorted by key: %q before %q", cfg.Settings[i-1].Key, cfg.Settings[i].Key)
		}
	}
	if s := settingByKey(t, cfg, config.JoinString); !s.Redacted || s.Value != redactedValue {
		t.Fatalf("joinString must be redacted: %+v", s)
	}
	if s := settingByKey(t, cfg, config.GithubToken); !s.Redacted || s.Value != "" {
		t.Fatalf("empty githubToken must stay empty but redacted: %+v", s)
	}
	if s := settingByKey(t, cfg, config.JoinStringFile); s.Redacted || s.Value != "/run/secrets/join" {
		t.Fatalf("joinStringFile is a path and must be visible: %+v", s)
	}
	if s := settingByKey(t, cfg, config.FlatcarChannel); s.Source != config.SourceFlag || s.Value != "beta" {
		t.Fatalf("flatcarChannel should come from a flag: %+v", s)
	}
	if s := settingByKey(t, cfg, config.CoreOSChannel); s.Source != config.SourceEnv {
		t.Fatalf("coreOSChannel should come from BOOTY_COREOSCHANNEL: %+v", s)
	}
	if s := settingByKey(t, cfg, config.IgnitionFile); s.Source != config.SourceEnv {
		t.Fatalf("legacy IGNITION_FILE must count as env: %+v", s)
	}
	if s := settingByKey(t, cfg, config.ServerIP); s.Source != config.SourceDefault || s.Value != "192.168.1.10" {
		t.Fatalf("serverIP set programmatically counts as default: %+v", s)
	}
	if s := settingByKey(t, cfg, config.HttpPort); s.Value != "18099" {
		t.Fatalf("ints render as strings: %+v", s)
	}
	if s := settingByKey(t, cfg, config.JoinTokenTTL); s.Value != "1h0m0s" {
		t.Fatalf("durations render in Go notation: %+v", s)
	}
	if s := settingByKey(t, cfg, config.SSHAuthorizedKeys); s.Value != "ssh-ed25519 AAA, ssh-ed25519 BBB" {
		t.Fatalf("slices are comma-joined: %+v", s)
	}

	if d := cfg.Templates.Default; d.Name != "config/ignition.yaml" || d.Source != templateSourceFile || !d.Writable || d.ReadOnlyReason != "" {
		t.Fatalf("unexpected default template %+v", d)
	}
	if cfg.ReadOnlyReason != "" {
		t.Fatalf("readOnlyReason must be omitted when writable: %q", cfg.ReadOnlyReason)
	}
	if len(cfg.Templates.Hosts) != 1 || cfg.Templates.Hosts[0] != (hostTemplate{MAC: "aa:bb:cc:dd:ee:01", Hostname: "n1", Name: "config/n1.yaml"}) {
		t.Fatalf("only hosts with an ignitionFile are listed: %+v", cfg.Templates.Hosts)
	}
}

func TestConfigViewEmbeddedTemplate(t *testing.T) {
	srv, dir := newTestServer(t)
	if err := os.Remove(filepath.Join(dir, "config", "ignition.yaml")); err != nil {
		t.Fatal(err)
	}
	cfg := decodeConfig(t, do(t, http.MethodGet, srv.URL+"/config", ""))
	if d := cfg.Templates.Default; d.Source != templateSourceEmbedded || !d.Writable {
		t.Fatalf("missing default template is embedded but savable: %+v", d)
	}
}
