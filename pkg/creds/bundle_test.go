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

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

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
		t.Fatalf("want exactly firstboot.hostname.cred and tmpfiles.extra.cred (PID 1 and generators refuse null-key credentials), got %v", got)
	}
	hn, ok := got["firstboot.hostname.cred"]
	if !ok || hn.body != "node1\n" || hn.hdr.Mode != 0o600 || hn.hdr.Uid != 0 || hn.hdr.Gid != 0 || hn.hdr.ModTime.Unix() != 0 {
		t.Fatalf("hostname credential wrong: %+v", hn)
	}
	tf, ok := got["tmpfiles.extra.cred"]
	if !ok {
		t.Fatal("tmpfiles.extra.cred missing")
	}

	want := "f+ /etc/hostname 0644 root root - node1\n" +
		"f~ /etc/systemd/system/booty-hostname.service 0644 root root - " + b64(hostnameUnit) + "\n" +
		"L+ /etc/systemd/system/sysinit.target.wants/booty-hostname.service - - - - /etc/systemd/system/booty-hostname.service\n" +
		"d /home/core/.ssh 0700 core core -\n" +
		"f~ /home/core/.ssh/authorized_keys 0600 core core - " + b64("ssh-ed25519 AAAA1 a\nssh-ed25519 AAAA2 b\n") + "\n" +
		"f~ /etc/systemd/system/booty-booted.service 0644 root root - " + b64(ign.BootedUnit(in.Server)) + "\n" +
		"L+ /etc/systemd/system/multi-user.target.wants/booty-booted.service - - - - /etc/systemd/system/booty-booted.service\n" +
		"f~ /etc/systemd/system/booty-update.service 0644 root root - " + b64(ign.UpdateService) + "\n" +
		"f~ /etc/systemd/system/booty-update.timer 0644 root root - " + b64(ign.UpdateTimer) + "\n" +
		"L+ /etc/systemd/system/timers.target.wants/booty-update.timer - - - - /etc/systemd/system/booty-update.timer\n" +
		"f~ /opt/booty/update-check 0755 root root - " + b64(ign.UpdateCheckScript(in.Server)) + "\n"
	if tf.body != want {
		t.Fatalf("tmpfiles rules:\n%s\nwant:\n%s", tf.body, want)
	}
	if !strings.Contains(ign.BootedUnit(in.Server), `/booted?mac=$$MAC"`) || !strings.Contains(ign.BootedUnit(in.Server), "http://192.168.1.10:8080/booted") {
		t.Fatalf("booted unit must POST /booted?mac= on the server:\n%s", ign.BootedUnit(in.Server))
	}
	for _, s := range []string{"DefaultDependencies=no", "Before=network-pre.target", "ConditionPathExists=/etc/hostname", "WantedBy=sysinit.target", "> /proc/sys/kernel/hostname"} {
		if !strings.Contains(hostnameUnit, s) {
			t.Errorf("hostname unit missing %q:\n%s", s, hostnameUnit)
		}
	}
}

func TestBundleHonoursFeatures(t *testing.T) {
	const (
		hostname = "firstboot.hostname.cred"
		tmpfiles = "tmpfiles.extra.cred"
	)
	tests := []struct {
		features string
		in       Input
		want     []string
		rules    []string
		noRules  []string
	}{
		{"hostname", in, []string{hostname, tmpfiles}, []string{"/etc/hostname", "booty-hostname.service"}, []string{"authorized_keys", "booty-booted", "booty-update"}},
		{"sshkeys", in, []string{tmpfiles}, []string{"authorized_keys"}, []string{"/etc/hostname", "booty-hostname", "booty-booted", "booty-update"}},
		{"sshkeys", Input{Hostname: "x"}, nil, nil, nil},
		{"hostname", Input{Server: "s"}, nil, nil, nil},
		{"none", in, nil, nil, nil},
		{"booted", Input{Server: "s"}, []string{tmpfiles}, []string{"booty-booted.service"}, []string{"/etc/hostname", "authorized_keys", "booty-update"}},
		{"update", Input{Server: "s"}, []string{tmpfiles}, []string{"booty-update.service", "booty-update.timer", "/opt/booty/update-check"}, []string{"/etc/hostname", "authorized_keys", "booty-booted"}},
	}
	for _, tc := range tests {
		t.Run(tc.features, func(t *testing.T) {
			data, err := Bundle(tc.in, features(t, tc.features))
			if err != nil {
				t.Fatal(err)
			}
			got := entries(t, data)
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			for _, w := range tc.want {
				if _, ok := got[w]; !ok {
					t.Errorf("missing %s in %v", w, got)
				}
			}
			rules := got[tmpfiles].body
			for _, s := range tc.rules {
				if !strings.Contains(rules, s) {
					t.Errorf("rules missing %q:\n%s", s, rules)
				}
			}
			for _, s := range tc.noRules {
				if strings.Contains(rules, s) {
					t.Errorf("rules must not contain %q:\n%s", s, rules)
				}
			}
		})
	}
}
