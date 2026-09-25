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

const warningButane = `variant: fcos
version: 1.5.0
systemd:
  units:
    - name: example.service
      enabled: true
      contents: |
        [Service]
        ExecStart=/bin/true
`

var fatalButane = strings.Replace(minimalButane, "/etc/hostname", "etc/hostname", 1)

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

func decodeTemplate(t *testing.T, r response) templateResponse {
	t.Helper()
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("template response: %+v", r)
	}
	var resp templateResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatalf("decode template: %v\n%s", err, r.body)
	}
	return resp
}

func decodeValidate(t *testing.T, r response) validateResponse {
	t.Helper()
	if r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") {
		t.Fatalf("validate response: %+v", r)
	}
	var resp validateResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatalf("decode validate: %v\n%s", err, r.body)
	}
	return resp
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

	tpl := decodeTemplate(t, do(t, http.MethodGet, srv.URL+"/config/template", ""))
	if tpl.Source != templateSourceEmbedded || tpl.Content != defaultButane || tpl.Name != "config/ignition.yaml" || !tpl.Writable {
		t.Fatalf("default template must fall back to the embedded butane: %+v", tpl)
	}
}

func TestConfigTemplateGet(t *testing.T) {
	srv, dir := newTestServer(t)

	tpl := decodeTemplate(t, do(t, http.MethodGet, srv.URL+"/config/template", ""))
	if tpl.Name != "config/ignition.yaml" || tpl.Source != templateSourceFile || tpl.Content != minimalButane || !tpl.Writable {
		t.Fatalf("default template: %+v", tpl)
	}

	if err := os.WriteFile(filepath.Join(dir, "config", "n1.bu"), []byte(warningButane), 0o644); err != nil {
		t.Fatal(err)
	}
	tpl = decodeTemplate(t, do(t, http.MethodGet, srv.URL+"/config/template?name=./config/n1.bu", ""))
	if tpl.Name != "config/n1.bu" || tpl.Content != warningButane {
		t.Fatalf("named template: %+v", tpl)
	}

	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/config/template?name=config/missing.yaml", ""), http.StatusNotFound)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/config/template", "{}"), http.StatusMethodNotAllowed)

	for _, name := range []string{
		"../ignition.yaml",
		"config/../../etc/passwd.yaml",
		"/etc/ignition.yaml",
		"hardware.json",
		"flatcar_pin.txt",
		"config/ignition.txt",
		"config/ignition.yaml.tmp",
		"registry/blobs/x.yaml",
		"config",
		".",
	} {
		r := do(t, http.MethodGet, srv.URL+"/config/template?name="+name, "")
		if r.status != http.StatusBadRequest {
			t.Errorf("%q: status %d want 400 (body=%s)", name, r.status, r.body)
		}
	}
}

func TestConfigTemplateValidate(t *testing.T) {
	srv, _ := newTestServer(t)
	viper.Set(config.JoinString, "kubeadm join 10.0.0.1:6443 --token t")
	if r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"node1"}`); r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
	body := func(name, content, mac string) string {
		b, err := json.Marshal(map[string]string{"name": name, "content": content, "mac": mac})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	validate := srv.URL + "/config/template/validate"

	assertJSONError(t, do(t, http.MethodGet, validate, ""), http.StatusMethodNotAllowed)
	assertJSONError(t, do(t, http.MethodPost, validate, `{"name":`), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, validate, body("../x.yaml", minimalButane, "")), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, validate, body("", minimalButane, "nope")), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, validate, body("", minimalButane, "aa:bb:cc:dd:ee:99")), http.StatusNotFound)

	res := decodeValidate(t, do(t, http.MethodPost, validate, body("", minimalButane, "")))
	if !res.OK || len(res.Entries) != 0 || !strings.Contains(res.Ignition, `"version": "3.4.0"`) {
		t.Fatalf("minimal butane must validate cleanly: %+v", res)
	}
	if !strings.Contains(res.Ignition, "data:,example") {
		t.Fatalf("dummy host renders as 'example':\n%s", res.Ignition)
	}

	res = decodeValidate(t, do(t, http.MethodPost, validate, body("", minimalButane, "AA:BB:CC:DD:EE:01")))
	if !res.OK || !strings.Contains(res.Ignition, "data:,node1") {
		t.Fatalf("registered host renders with its hostname: %+v", res)
	}

	joinTemplate := strings.Replace(minimalButane, "{{ .Hostname }}", "{{ .JoinString }}", 1)
	res = decodeValidate(t, do(t, http.MethodPost, validate, body("", joinTemplate, "")))
	if !res.OK || !strings.Contains(res.Ignition, "--token%20t") {
		t.Fatalf("static join string is rendered: %+v", res)
	}
	viper.Set(config.KubeadmJoin, config.KubeadmJoinAuto)
	res = decodeValidate(t, do(t, http.MethodPost, validate, body("", joinTemplate, "")))
	if !res.OK || !strings.Contains(res.Ignition, "kubeadm%20join%20%3Cauto%3E") {
		t.Fatalf("auto mode renders a placeholder, never mints: %+v", res)
	}
	viper.Set(config.KubeadmJoin, config.KubeadmJoinStatic)

	res = decodeValidate(t, do(t, http.MethodPost, validate, body("", warningButane, "")))
	if !res.OK || len(res.Entries) != 1 || res.Entries[0].Kind != "warning" || !strings.Contains(res.Entries[0].Message, "example.service") {
		t.Fatalf("non-fatal warnings keep ok=true: %+v", res)
	}
	if res.Ignition == "" {
		t.Fatal("warnings must still return the rendered ignition")
	}

	res = decodeValidate(t, do(t, http.MethodPost, validate, body("", minimalButane+"bogus: true\n", "")))
	if !res.OK || len(res.Entries) != 1 || res.Entries[0].Kind != "warning" || !strings.Contains(res.Entries[0].Message, "unused key bogus") {
		t.Fatalf("unused keys are warnings in butane: %+v", res)
	}

	res = decodeValidate(t, do(t, http.MethodPost, validate, body("", fatalButane, "")))
	if res.OK || res.Ignition != "" || len(res.Entries) == 0 || res.Entries[0].Kind != "error" || !strings.Contains(res.Entries[0].Message, "absolute") {
		t.Fatalf("fatal butane reports set ok=false: %+v", res)
	}

	res = decodeValidate(t, do(t, http.MethodPost, validate, body("", "hostname: {{ .Hostname", "")))
	if res.OK || len(res.Entries) != 1 || res.Entries[0].Kind != "error" || !strings.Contains(res.Entries[0].Message, "parsing template") {
		t.Fatalf("template parse errors are reported as entries: %+v", res)
	}

	res = decodeValidate(t, do(t, http.MethodPost, validate, body("", "hostname: {{ .Nope }}", "")))
	if res.OK || len(res.Entries) != 1 || !strings.Contains(res.Entries[0].Message, "executing template") {
		t.Fatalf("template execution errors are reported as entries: %+v", res)
	}
}

func TestConfigTemplateSave(t *testing.T) {
	srv, dir := newTestServer(t)
	if r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"node1"}`); r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
	hwBefore, err := os.ReadFile(filepath.Join(dir, "hardware.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := func(name, content string) string {
		b, err := json.Marshal(map[string]string{"name": name, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	put := func(name, content string) response {
		return do(t, http.MethodPut, srv.URL+"/config/template", body(name, content))
	}

	updated := strings.Replace(minimalButane, "/etc/hostname", "/etc/booty-hostname", 1)
	tpl := decodeTemplate(t, put("", updated))
	if tpl.Name != "config/ignition.yaml" || tpl.Source != templateSourceFile || tpl.Content != updated || !tpl.Writable {
		t.Fatalf("PUT must answer like GET: %+v", tpl)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "config", "ignition.yaml"))
	if err != nil || string(onDisk) != updated {
		t.Fatalf("saved content mismatch: %q %v", onDisk, err)
	}
	r := do(t, http.MethodGet, srv.URL+"/ignition/user.json?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || !strings.Contains(r.body, "/etc/booty-hostname") {
		t.Fatalf("boot path must serve the saved template: %+v", r)
	}

	tpl = decodeTemplate(t, put("config/new-host.yml", minimalButane))
	if tpl.Name != "config/new-host.yml" || tpl.Content != minimalButane {
		t.Fatalf("PUT creates new templates: %+v", tpl)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "new-host.yml")); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "config")); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Fatalf("probe/temp file left behind: %s", e.Name())
			}
		}
	}

	r = put("", fatalButane)
	if r.status != http.StatusBadRequest {
		t.Fatalf("invalid template must be rejected: %+v", r)
	}
	var rejected struct {
		Error   string        `json:"error"`
		Entries []reportEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(r.body), &rejected); err != nil || rejected.Error != "template does not validate" || len(rejected.Entries) == 0 {
		t.Fatalf("400 body must carry the entries: %s (%v)", r.body, err)
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "config", "ignition.yaml")); string(onDisk) != updated {
		t.Fatal("rejected PUT must not touch the file")
	}

	assertJSONError(t, put("../escape.yaml", minimalButane), http.StatusBadRequest)
	assertJSONError(t, put("hardware.json", minimalButane), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPut, srv.URL+"/config/template", `{"name":`), http.StatusBadRequest)
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.yaml")); err == nil {
		t.Fatal("PUT must never write outside dataDir")
	}

	hwAfter, err := os.ReadFile(filepath.Join(dir, "hardware.json"))
	if err != nil || string(hwAfter) != string(hwBefore) {
		t.Fatalf("hardware.json must be untouched: %v", err)
	}
}

func TestConfigTemplateReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode bits")
	}
	srv, dir := newTestServer(t)
	configDir := filepath.Join(dir, "config")
	file := filepath.Join(configDir, "ignition.yaml")
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(configDir, 0o755); err != nil {
			t.Error(err)
		}
		if err := os.Chmod(file, 0o644); err != nil {
			t.Error(err)
		}
	})

	cfg := decodeConfig(t, do(t, http.MethodGet, srv.URL+"/config", ""))
	if cfg.Templates.Default.Writable || cfg.ReadOnlyReason == "" || !strings.Contains(cfg.ReadOnlyReason, "permission denied") {
		t.Fatalf("read-only template must be reported: %+v", cfg)
	}
	tpl := decodeTemplate(t, do(t, http.MethodGet, srv.URL+"/config/template", ""))
	if tpl.Writable || tpl.ReadOnlyReason == "" || tpl.Content != minimalButane {
		t.Fatalf("read-only template still readable: %+v", tpl)
	}

	r := do(t, http.MethodPut, srv.URL+"/config/template", `{"name":"config/ignition.yaml","content":"variant: fcos\nversion: 1.5.0\n"}`)
	if r.status != http.StatusConflict {
		t.Fatalf("PUT on a read-only template: %+v", r)
	}
	var conflict struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(r.body), &conflict); err != nil || conflict.Error != "template is read-only" || !strings.Contains(conflict.Reason, "permission denied") {
		t.Fatalf("409 body: %s (%v)", r.body, err)
	}
	if onDisk, _ := os.ReadFile(file); string(onDisk) != minimalButane {
		t.Fatal("read-only file must be untouched")
	}

	r = do(t, http.MethodPut, srv.URL+"/config/template", `{"name":"config/other.yaml","content":"variant: fcos\nversion: 1.5.0\n"}`)
	if r.status != http.StatusConflict {
		t.Fatalf("PUT of a new file into a read-only directory: %+v", r)
	}
}
