package ignition

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v3_4 "github.com/coreos/ignition/v2/config/v3_4"
	v3_5 "github.com/coreos/ignition/v2/config/v3_5"
)

func allFeatures(t *testing.T) Features {
	t.Helper()
	f, err := ParseFeatures("hostname,update,booted,sshkeys")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func marshal(t *testing.T, in Input, f Features) []byte {
	t.Helper()
	raw, err := json.Marshal(Fragment(in, f))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseFeatures(t *testing.T) {
	f, err := ParseFeatures(" Hostname, update ,booted,sshkeys")
	if err != nil || len(f) != 4 || !f.Enabled() {
		t.Fatalf("ParseFeatures: f=%v err=%v", f, err)
	}
	f, err = ParseFeatures("none")
	if err != nil || f.Enabled() {
		t.Fatalf("none should disable everything: f=%v err=%v", f, err)
	}
	f, err = ParseFeatures("hostname,none")
	if err != nil || f.Enabled() {
		t.Fatalf("none anywhere in the list wins: f=%v err=%v", f, err)
	}
	if _, err := ParseFeatures("hostname,bogus"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown feature must be rejected, got %v", err)
	}
	f, err = ParseFeatures("")
	if err != nil || f.Enabled() {
		t.Fatalf("empty list means nothing enabled: f=%v err=%v", f, err)
	}
}

func TestFragmentParsesWithIgnition(t *testing.T) {
	in := Input{Hostname: "node1", Server: "192.168.1.10:8080", SSHKeys: []string{"ssh-ed25519 AAAA1 a", "ssh-ed25519 AAAA2 b"}}
	raw := marshal(t, in, allFeatures(t))

	cfg34, rpt, err := v3_4.Parse(raw)
	if err != nil || rpt.IsFatal() {
		t.Fatalf("v3_4.Parse: %v\n%s\n%s", err, rpt.String(), raw)
	}
	if _, rpt, err := v3_5.ParseCompatibleVersion(raw); err != nil || rpt.IsFatal() {
		t.Fatalf("v3_5.ParseCompatibleVersion: %v\n%s", err, rpt.String())
	}
	if cfg34.Ignition.Version != "3.4.0" {
		t.Fatalf("fragment must be spec 3.4.0, got %q", cfg34.Ignition.Version)
	}

	files := map[string]string{}
	for _, f := range cfg34.Storage.Files {
		files[f.Path] = *f.Contents.Source
	}
	if src := files["/etc/hostname"]; src != "data:,node1%0A" {
		t.Fatalf("/etc/hostname source %q", src)
	}
	if !strings.HasPrefix(files[UpdateCheckScriptPath], "data:text/plain;charset=utf-8;base64,") {
		t.Fatalf("update script should be base64 data URL, got %q", files[UpdateCheckScriptPath])
	}
	for _, f := range cfg34.Storage.Files {
		if f.Path == UpdateCheckScriptPath && (f.Mode == nil || *f.Mode != 0o755) {
			t.Fatalf("update script must be 0755, got %v", f.Mode)
		}
	}

	units := map[string]bool{}
	enabled := map[string]bool{}
	for _, u := range cfg34.Systemd.Units {
		units[u.Name] = true
		enabled[u.Name] = u.Enabled != nil && *u.Enabled
		if !strings.Contains(*u.Contents, "[Unit]") {
			t.Errorf("unit %s has no [Unit] section", u.Name)
		}
	}
	for _, name := range []string{"booty-booted.service", "booty-update.service", "booty-update.timer"} {
		if !units[name] {
			t.Errorf("missing unit %s", name)
		}
	}
	if !enabled["booty-booted.service"] || !enabled["booty-update.timer"] || enabled["booty-update.service"] {
		t.Fatalf("enabled flags wrong: %v", enabled)
	}
	if len(cfg34.Passwd.Users) != 1 || cfg34.Passwd.Users[0].Name != "core" || len(cfg34.Passwd.Users[0].SSHAuthorizedKeys) != 2 {
		t.Fatalf("unexpected passwd %+v", cfg34.Passwd)
	}
	if !strings.Contains(string(raw), `http://192.168.1.10:8080/booted?mac=$$MAC`) {
		t.Fatalf("booted unit must escape $ for systemd:\n%s", raw)
	}
}

func TestUpdateCheckScriptContents(t *testing.T) {
	s := UpdateCheckScript("10.0.0.1:8080")
	for _, want := range []string{
		"#!/bin/bash",
		`"http://10.0.0.1:8080/update-check"`,
		`version=${OSTREE_VERSION:-${VERSION_ID:-${VERSION:-}}}`,
		`touch /var/run/reboot-required`,
		`rm -f /var/run/reboot-required`,
		`leaving reboot state alone`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q", want)
		}
	}
	if strings.Contains(s, "$$") {
		t.Fatal("script is a file, not an ExecStart line: no $$ escaping")
	}
}

func TestFragmentToggles(t *testing.T) {
	in := Input{Hostname: "node1", Server: "s", SSHKeys: []string{"k"}}

	f, _ := ParseFeatures("hostname")
	cfg := Fragment(in, f)
	if len(cfg.Storage.Files) != 1 || cfg.Storage.Files[0].Path != "/etc/hostname" || len(cfg.Systemd.Units) != 0 || len(cfg.Passwd.Users) != 0 {
		t.Fatalf("hostname only: %+v", cfg)
	}
	if cfg := Fragment(Input{Server: "s"}, f); len(cfg.Storage.Files) != 0 {
		t.Fatal("empty hostname must not produce /etc/hostname")
	}

	f, _ = ParseFeatures("update")
	cfg = Fragment(in, f)
	if len(cfg.Storage.Files) != 1 || cfg.Storage.Files[0].Path != UpdateCheckScriptPath || len(cfg.Systemd.Units) != 2 {
		t.Fatalf("update only: %+v", cfg)
	}

	f, _ = ParseFeatures("booted")
	cfg = Fragment(in, f)
	if len(cfg.Storage.Files) != 0 || len(cfg.Systemd.Units) != 1 || cfg.Systemd.Units[0].Name != "booty-booted.service" {
		t.Fatalf("booted only: %+v", cfg)
	}

	f, _ = ParseFeatures("sshkeys")
	cfg = Fragment(in, f)
	if len(cfg.Passwd.Users) != 1 || len(cfg.Storage.Files) != 0 || len(cfg.Systemd.Units) != 0 {
		t.Fatalf("sshkeys only: %+v", cfg)
	}
	if cfg := Fragment(Input{Hostname: "x", Server: "s"}, f); len(cfg.Passwd.Users) != 0 {
		t.Fatal("no keys must not produce a core user entry")
	}

	f, _ = ParseFeatures("none")
	cfg = Fragment(in, f)
	if len(cfg.Storage.Files)+len(cfg.Systemd.Units)+len(cfg.Passwd.Users) != 0 {
		t.Fatalf("none must be empty: %+v", cfg)
	}
	raw, _ := json.Marshal(cfg)
	if !strings.Contains(string(raw), `"version":"3.4.0"`) {
		t.Fatalf("none should still carry the version stanza, got %s", raw)
	}
	if _, rpt, err := v3_4.Parse(raw); err != nil || rpt.IsFatal() {
		t.Fatalf("empty config must still parse: %v %s", err, rpt.String())
	}
}

func TestLoadSSHKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	content := "# comment\n\nssh-ed25519 AAAA1 one\n   ssh-rsa BBBB2 two   \n\n# trailing\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := LoadSSHKeys(path, []string{" ssh-ed25519 CCCC3 inline ", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh-ed25519 CCCC3 inline", "ssh-ed25519 AAAA1 one", "ssh-rsa BBBB2 two"}
	if strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("keys=%q want %q", keys, want)
	}

	keys, err = LoadSSHKeys("", nil)
	if err != nil || len(keys) != 0 {
		t.Fatalf("no file, no inline: keys=%q err=%v", keys, err)
	}
	keys, err = LoadSSHKeys(filepath.Join(t.TempDir(), "missing"), []string{"k"})
	if err == nil || len(keys) != 1 {
		t.Fatalf("missing file must error but keep inline keys: keys=%q err=%v", keys, err)
	}
}
