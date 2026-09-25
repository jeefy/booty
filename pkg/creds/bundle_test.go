package creds

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	ign "github.com/jeefy/booty/pkg/ignition"
)

func features(t *testing.T, list string) ign.Features {
	t.Helper()
	f, err := ign.ParseFeatures(list)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type entry struct {
	hdr  *tar.Header
	body string
}

// entries decrypts every member and fails on any that is not a null-key
// credential whose embedded name equals the file name minus .cred.
func entries(t *testing.T, data []byte) map[string]entry {
	t.Helper()
	out := map[string]entry{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag != tar.TypeReg || strings.Contains(hdr.Name, "/") || !strings.HasSuffix(hdr.Name, credSuffix) || len(hdr.Name) == len(credSuffix) {
			t.Fatalf("member %q must be a flat regular ?*.cred file", hdr.Name)
		}
		if !bytes.HasPrefix(body, []byte("BYRp2vb1")) {
			t.Fatalf("%s is not a base64 null-key credential: %q", hdr.Name, body)
		}
		plain, err := Decrypt(strings.TrimSuffix(hdr.Name, credSuffix), body)
		if err != nil {
			t.Fatalf("%s: %v", hdr.Name, err)
		}
		out[hdr.Name] = entry{hdr: hdr, body: string(plain)}
	}
}

var in = Input{Hostname: "node1", Server: "192.168.1.10:8080", SSHKeys: []string{"ssh-ed25519 AAAA1 a", "ssh-ed25519 AAAA2 b"}}

func TestBundleIsDeterministicAndComplete(t *testing.T) {
	f := features(t, "hostname,update,booted,sshkeys")
	a, err := Bundle(in, f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Bundle(in, f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || Sum(a) != Sum(b) {
		t.Fatal("two renders of the same input must be identical")
	}
	if len(Sum(a)) != 64 {
		t.Fatalf("Sum should be hex sha256, got %q", Sum(a))
	}

	got := entries(t, a)
	want := []string{
		"firstboot.hostname.cred",
		"tmpfiles.extra.cred",
		"systemd.extra-unit.booty-booted.service.cred",
		"systemd.extra-unit.booty-update.service.cred",
		"systemd.extra-unit.booty-update.timer.cred",
		"systemd.unit-dropin.multi-user.target~booty.cred",
		"systemd.unit-dropin.timers.target~booty.cred",
	}
	if len(got) != len(want) {
		t.Fatalf("want %d members %v, got %v", len(want), want, got)
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Fatalf("missing %s in %v", w, got)
		}
	}
	hn := got["firstboot.hostname.cred"]
	if hn.body != "node1\n" || hn.hdr.Mode != 0o600 || hn.hdr.Uid != 0 || hn.hdr.Gid != 0 || hn.hdr.ModTime.Unix() != 0 {
		t.Fatalf("hostname credential wrong: %+v", hn)
	}

	rules := got["tmpfiles.extra.cred"].body
	for _, want := range []string{
		"d /home/core/.ssh 0700 core core -\n",
		"f~ /home/core/.ssh/authorized_keys 0600 core core - " + base64.StdEncoding.EncodeToString([]byte("ssh-ed25519 AAAA1 a\nssh-ed25519 AAAA2 b\n")) + "\n",
		"f~ /opt/booty/update-check 0755 root root - " + base64.StdEncoding.EncodeToString([]byte(ign.UpdateCheckScript(in.Server))) + "\n",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("tmpfiles rules missing %q:\n%s", want, rules)
		}
	}
	if strings.Count(rules, "\n") != 3 {
		t.Fatalf("want exactly 3 rules (units are not delivered via tmpfiles):\n%s", rules)
	}
	if strings.Contains(rules, "/etc/systemd") {
		t.Fatalf("tmpfiles must not write units, they are never loaded:\n%s", rules)
	}

	booted := got["systemd.extra-unit.booty-booted.service.cred"].body
	if booted != ign.BootedUnit(in.Server) || !strings.Contains(booted, `/booted?mac=$$MAC"`) || !strings.Contains(booted, "http://192.168.1.10:8080/booted") {
		t.Fatalf("booted unit must be the Ignition builtin's text:\n%s", booted)
	}
	if got["systemd.extra-unit.booty-update.service.cred"].body != ign.UpdateService || got["systemd.extra-unit.booty-update.timer.cred"].body != ign.UpdateTimer {
		t.Fatal("update units must be the Ignition builtin's text")
	}
	if d := got["systemd.unit-dropin.multi-user.target~booty.cred"].body; d != "[Unit]\nWants=booty-booted.service\n" {
		t.Fatalf("multi-user.target drop-in wrong: %q", d)
	}
	if d := got["systemd.unit-dropin.timers.target~booty.cred"].body; d != "[Unit]\nWants=booty-update.timer\n" {
		t.Fatalf("timers.target drop-in wrong: %q", d)
	}
}

func TestBundleHonoursFeatures(t *testing.T) {
	const (
		hostname   = "firstboot.hostname.cred"
		tmpfiles   = "tmpfiles.extra.cred"
		bootedUnit = "systemd.extra-unit.booty-booted.service.cred"
		bootedWant = "systemd.unit-dropin.multi-user.target~booty.cred"
		updateSvc  = "systemd.extra-unit.booty-update.service.cred"
		updateTmr  = "systemd.extra-unit.booty-update.timer.cred"
		updateWant = "systemd.unit-dropin.timers.target~booty.cred"
	)
	all := []string{hostname, tmpfiles, bootedUnit, bootedWant, updateSvc, updateTmr, updateWant}
	tests := []struct {
		features string
		in       Input
		want     []string
	}{
		{"hostname", in, []string{hostname}},
		{"sshkeys", in, []string{tmpfiles}},
		{"sshkeys", Input{Hostname: "x"}, nil},
		{"hostname", Input{Server: "s"}, nil},
		{"none", in, nil},
		{"booted", Input{Server: "s"}, []string{bootedUnit, bootedWant}},
		{"update", Input{Server: "s"}, []string{tmpfiles, updateSvc, updateTmr, updateWant}},
		{"booted,update", Input{Server: "s"}, []string{tmpfiles, bootedUnit, bootedWant, updateSvc, updateTmr, updateWant}},
	}
	for _, tc := range tests {
		t.Run(tc.features, func(t *testing.T) {
			data, err := Bundle(tc.in, features(t, tc.features))
			if err != nil {
				t.Fatal(err)
			}
			got := entries(t, data)
			wanted := map[string]bool{}
			for _, w := range tc.want {
				wanted[w] = true
				if _, ok := got[w]; !ok {
					t.Errorf("missing %s in %v", w, got)
				}
			}
			for _, a := range all {
				if _, ok := got[a]; ok && !wanted[a] {
					t.Errorf("unexpected %s", a)
				}
			}
		})
	}

	if rules := TmpfilesRules(Input{Server: "s"}, features(t, "booted")); rules != "" {
		t.Fatalf("booted alone needs no tmpfiles rules:\n%s", rules)
	}
	rules := TmpfilesRules(Input{Server: "s"}, features(t, "update"))
	if strings.Contains(rules, "booty-update") || !strings.Contains(rules, "/opt/booty/update-check") {
		t.Fatalf("update-only rules wrong:\n%s", rules)
	}
}
