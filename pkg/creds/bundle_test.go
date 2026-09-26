package creds

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/pki"
	"github.com/jeefy/booty/pkg/cluster/token"
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
		"f~ /opt/booty/update-check 0755 root root - " + b64(ign.UpdateCheckScript(in.Server)) + "\n" +
		"d /etc/systemd/system/k0scontroller.service.d 0755 root root -\n" +
		"f~ /etc/systemd/system/k0scontroller.service.d/booty.conf 0644 root root - " + b64("[Unit]\nWants=booty-hostname.service booty-booted.service booty-update.timer\n") + "\n"
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
		{"sshkeys", in, []string{tmpfiles}, []string{"authorized_keys"}, []string{"/etc/hostname", "booty-hostname", "booty-booted", "booty-update", "k0scontroller"}},
		{"sshkeys", Input{Hostname: "x"}, nil, nil, nil},
		{"hostname", Input{Server: "s"}, nil, nil, nil},
		{"none", in, nil, nil, nil},
		{"booted", Input{Server: "s"}, []string{tmpfiles}, []string{"booty-booted.service", "k0scontroller.service.d/booty.conf"}, []string{"/etc/hostname", "authorized_keys", "booty-update"}},
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

func k0sNode(t *testing.T, role k0s.Role, cniName string) *k0s.Node {
	t.Helper()
	p, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"))
	if err != nil {
		t.Fatal(err)
	}
	o := k0s.Options{Role: role, OS: "bluefin", Server: in.Server, Endpoint: "10.77.0.40", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", CNI: cniName}
	if role == k0s.Controller {
		o.PKI = p.Files()
		if o.Secrets, err = token.K0sBootstrapSecret(k0s.Worker, "abcdef.0123456789abcdef", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	} else if o.WorkerToken, err = token.EncodeK0s(k0s.Worker, "10.77.0.40", p.CACert(), "abcdef.0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	n, err := k0s.Render(o)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// tmpfilesFiles decodes every f~ rule into path -> (mode, contents).
func tmpfilesFiles(t *testing.T, rules string) map[string][2]string {
	t.Helper()
	out := map[string][2]string{}
	for _, line := range strings.Split(strings.TrimSpace(rules), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 7 || fields[0] != "f~" {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(fields[6])
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		out[fields[1]] = [2]string{fields[2], string(body)}
	}
	return out
}

func TestBundleBluefinK0sController(t *testing.T) {
	f := features(t, "hostname,update,booted,sshkeys")
	input := in
	input.K0s = k0sNode(t, k0s.Controller, "cilium")
	data, err := Bundle(input, f)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, data)
	if len(got) != 2 {
		t.Fatalf("still exactly two credentials, got %v", got)
	}
	rules := got["tmpfiles.extra.cred"].body
	legacy := TmpfilesRules(in, f)
	legacyPrefix := strings.TrimSuffix(legacy, "d /etc/systemd/system/k0scontroller.service.d 0755 root root -\n"+
		"f~ /etc/systemd/system/k0scontroller.service.d/booty.conf 0644 root root - "+b64("[Unit]\nWants=booty-hostname.service booty-booted.service booty-update.timer\n")+"\n")
	if !strings.HasPrefix(rules, legacyPrefix) {
		t.Fatalf("k0s rules must extend the legacy ones:\n%s", rules)
	}
	for _, want := range []string{
		"d /etc/k0s 0755 root root -\n", "d /var/lib/k0s 0755 root root -\n", "d /var/lib/k0s/manifests 0755 root root -\n", "d /var/lib/k0s/manifests/booty 0755 root root -\n", "d /var/lib/k0s/pki 0755 root root -\n", "d /var/lib/k0s/pki/etcd 0755 root root -\n",
		"d /etc/systemd/system/k0scontroller.service.d 0755 root root -\n",
		"L+ /etc/systemd/system/multi-user.target.wants/booty-cluster-ready.service - - - - /etc/systemd/system/booty-cluster-ready.service\n",
		"L+ /etc/systemd/system/multi-user.target.wants/booty-cni-apply.service - - - - /etc/systemd/system/booty-cni-apply.service\n",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules missing %q:\n%s", want, rules)
		}
	}
	if strings.Count(rules, "d /etc/systemd/system/k0scontroller.service.d ") != 1 {
		t.Fatalf("the drop-in directory is created once:\n%s", rules)
	}
	files := tmpfilesFiles(t, rules)
	for _, p := range []string{"/var/lib/k0s/pki/ca.crt", "/var/lib/k0s/pki/ca.key", "/var/lib/k0s/pki/sa.key", "/var/lib/k0s/pki/sa.pub", "/var/lib/k0s/pki/etcd/ca.crt", "/var/lib/k0s/pki/etcd/ca.key", "/etc/k0s/k0s.yaml", "/var/lib/k0s/manifests/booty/tokens.yaml"} {
		if files[p][0] != "0600" || files[p][1] == "" {
			t.Errorf("%s: %+v", p, files[p])
		}
	}
	if !strings.Contains(files["/var/lib/k0s/pki/ca.key"][1], "PRIVATE KEY") {
		t.Fatal("controller bundle carries the CA key")
	}
	if !strings.Contains(files["/etc/k0s/k0s.yaml"][1], "externalAddress: 10.77.0.40") || !strings.Contains(files["/etc/k0s/k0s.yaml"][1], "provider: custom") {
		t.Fatalf("k0s.yaml:\n%s", files["/etc/k0s/k0s.yaml"][1])
	}
	if files["/opt/booty/cluster-ready.sh"][0] != "0755" || files["/opt/booty/cni-apply.sh"][0] != "0755" {
		t.Fatal("scripts are 0755")
	}
	dropIn := files["/etc/systemd/system/k0scontroller.service.d/booty-role.conf"][1]
	if strings.Count(dropIn, "ExecStart=") != 2 || !strings.Contains(dropIn, "ExecStart=\nExecStart=/usr/bin/k0s controller -c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot\n") || strings.Contains(dropIn, "--single") {
		t.Fatalf("role drop-in:\n%s", dropIn)
	}
	wants := files["/etc/systemd/system/k0scontroller.service.d/booty.conf"][1]
	if wants != "[Unit]\nWants=booty-hostname.service booty-booted.service booty-update.timer booty-cluster-ready.service booty-cni-apply.service\n" {
		t.Fatalf("first-boot Wants= must pull the k0s units in too:\n%s", wants)
	}
	if !strings.Contains(files["/etc/systemd/system/booty-cluster-ready.service"][1], "Wants=k0scontroller.service") {
		t.Fatal("ready unit")
	}
	if strings.Contains(rules, "k0sworker.service") || strings.Contains(rules, "/opt/bin/k0s") || strings.Contains(rules, "booty-k0s-install") {
		t.Fatalf("bluefin keeps the image's unit name and binary:\n%s", rules)
	}
	if _, ok := files["/etc/k0s/token"]; ok {
		t.Fatal("a controller has no worker token file")
	}
}

func TestBundleBluefinK0sWorker(t *testing.T) {
	f := features(t, "hostname,update,booted,sshkeys")
	input := in
	input.K0s = k0sNode(t, k0s.Worker, "cilium")
	data, err := Bundle(input, f)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, data)
	if len(got) != 2 {
		t.Fatalf("still exactly two credentials, got %v", got)
	}
	rules := got["tmpfiles.extra.cred"].body
	if strings.Contains(rules, "PRIVATE KEY") || strings.Contains(rules, b64("-----BEGIN RSA PRIVATE KEY")) {
		t.Fatal("worker bundle must carry no key")
	}
	files := tmpfilesFiles(t, rules)
	for p, body := range files {
		if strings.Contains(body[1], "PRIVATE KEY") {
			t.Fatalf("%s carries a private key", p)
		}
	}
	for _, absent := range []string{"/var/lib/k0s/pki", "k0s.yaml", "tokens.yaml", "booty-cluster-ready", "booty-cni-apply"} {
		if strings.Contains(rules, absent) {
			t.Fatalf("worker rules must not carry %s:\n%s", absent, rules)
		}
	}
	if !strings.Contains(rules, "d /etc/k0s 0755 root root -\n") || files["/etc/k0s/token"][0] != "0600" {
		t.Fatalf("token file:\n%s", rules)
	}
	join, err := token.ParseK0s(strings.TrimSpace(files["/etc/k0s/token"][1]))
	if err != nil || join.Server != "https://10.77.0.40:6443" || join.Role != k0s.Worker {
		t.Fatalf("token: %+v %v", join, err)
	}
	dropIn := files["/etc/systemd/system/k0scontroller.service.d/booty-role.conf"][1]
	if dropIn != "[Service]\nExecStart=\nExecStart=/usr/bin/k0s worker --token-file /etc/k0s/token\n" {
		t.Fatalf("worker drop-in:\n%s", dropIn)
	}
	if files["/etc/systemd/system/k0scontroller.service.d/booty.conf"][1] != "[Unit]\nWants=booty-hostname.service booty-booted.service booty-update.timer\n" {
		t.Fatalf("a worker adds no units to the first-boot hook:\n%s", rules)
	}

	minimal, err := Bundle(Input{Server: "s", K0s: input.K0s}, features(t, "none"))
	if err != nil {
		t.Fatal(err)
	}
	if got := entries(t, minimal); len(got) != 0 {
		t.Fatalf("--builtin=none disables the bundle, k0s included: %v", got)
	}
	only, err := Bundle(Input{Server: "s", K0s: input.K0s}, features(t, "booted"))
	if err != nil {
		t.Fatal(err)
	}
	rules = entries(t, only)["tmpfiles.extra.cred"].body
	files = tmpfilesFiles(t, rules)
	if _, ok := files["/etc/k0s/token"]; !ok || files["/etc/systemd/system/k0scontroller.service.d/booty-role.conf"][1] != dropIn || files["/etc/systemd/system/k0scontroller.service.d/booty.conf"][1] != "[Unit]\nWants=booty-booted.service\n" {
		t.Fatalf("k0s rides along with any enabled feature:\n%s", rules)
	}
}
