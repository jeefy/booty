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
	if len(got) != 2 {
		t.Fatalf("want firstboot.hostname.cred and tmpfiles.extra.cred, got %v", got)
	}
	hn, ok := got["firstboot.hostname.cred"]
	if !ok || hn.body != "node1\n" || hn.hdr.Mode != 0o600 || hn.hdr.Uid != 0 || hn.hdr.Gid != 0 || hn.hdr.ModTime.Unix() != 0 {
		t.Fatalf("hostname credential wrong: %+v", hn)
	}
	tf, ok := got["tmpfiles.extra.cred"]
	if !ok {
		t.Fatal("tmpfiles.extra.cred missing")
	}
	rules := tf.body
	for _, want := range []string{
		"d /home/core/.ssh 0700 core core -\n",
		"f~ /home/core/.ssh/authorized_keys 0600 core core - " + base64.StdEncoding.EncodeToString([]byte("ssh-ed25519 AAAA1 a\nssh-ed25519 AAAA2 b\n")) + "\n",
		"f~ /etc/systemd/system/booty-booted.service 0644 root root - ",
		"L+ /etc/systemd/system/multi-user.target.wants/booty-booted.service - - - - /etc/systemd/system/booty-booted.service\n",
		"f~ /etc/systemd/system/booty-update.service 0644 root root - ",
		"f~ /etc/systemd/system/booty-update.timer 0644 root root - ",
		"L+ /etc/systemd/system/timers.target.wants/booty-update.timer - - - - /etc/systemd/system/booty-update.timer\n",
		"f~ /opt/booty/update-check 0755 root root - ",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("tmpfiles rules missing %q:\n%s", want, rules)
		}
	}
	if strings.Count(rules, "\n") != 8 {
		t.Fatalf("want exactly 8 rules:\n%s", rules)
	}
	for _, line := range strings.Split(strings.TrimSpace(rules), "\n") {
		if !strings.HasPrefix(line, "f~ ") {
			continue
		}
		fields := strings.Fields(line)
		decoded, err := base64.StdEncoding.DecodeString(fields[len(fields)-1])
		if err != nil {
			t.Fatalf("%s: bad base64: %v", fields[1], err)
		}
		switch fields[1] {
		case "/etc/systemd/system/booty-booted.service":
			if !strings.Contains(string(decoded), `/booted?mac=$$MAC"`) || !strings.Contains(string(decoded), "http://192.168.1.10:8080/booted") {
				t.Fatalf("booted unit must POST /booted?mac= on the server:\n%s", decoded)
			}
		case "/opt/booty/update-check":
			if string(decoded) != ign.UpdateCheckScript(in.Server) {
				t.Fatal("update-check script must be the one the Ignition builtin ships")
			}
		}
	}
}

func TestBundleHonoursFeatures(t *testing.T) {
	tests := []struct {
		features string
		in       Input
		want     []string
		absent   []string
	}{
		{"hostname", in, []string{"firstboot.hostname.cred"}, []string{"tmpfiles.extra.cred"}},
		{"sshkeys", in, []string{"tmpfiles.extra.cred"}, []string{"firstboot.hostname.cred"}},
		{"sshkeys", Input{Hostname: "x"}, nil, []string{"firstboot.hostname.cred", "tmpfiles.extra.cred"}},
		{"hostname", Input{Server: "s"}, nil, []string{"firstboot.hostname.cred"}},
		{"none", in, nil, []string{"firstboot.hostname.cred", "tmpfiles.extra.cred"}},
		{"booted,update", Input{Server: "s"}, []string{"tmpfiles.extra.cred"}, []string{"firstboot.hostname.cred"}},
	}
	for _, tc := range tests {
		t.Run(tc.features, func(t *testing.T) {
			data, err := Bundle(tc.in, features(t, tc.features))
			if err != nil {
				t.Fatal(err)
			}
			got := entries(t, data)
			for _, w := range tc.want {
				if _, ok := got[w]; !ok {
					t.Errorf("missing %s in %v", w, got)
				}
			}
			for _, a := range tc.absent {
				if _, ok := got[a]; ok {
					t.Errorf("unexpected %s", a)
				}
			}
		})
	}

	rules := TmpfilesRules(Input{Server: "s"}, features(t, "booted"))
	if strings.Contains(rules, "authorized_keys") || strings.Contains(rules, "booty-update") || !strings.Contains(rules, "booty-booted.service") {
		t.Fatalf("booted-only rules wrong:\n%s", rules)
	}
	rules = TmpfilesRules(Input{Server: "s"}, features(t, "update"))
	if strings.Contains(rules, "booty-booted") || !strings.Contains(rules, "/opt/booty/update-check") {
		t.Fatalf("update-only rules wrong:\n%s", rules)
	}
}
