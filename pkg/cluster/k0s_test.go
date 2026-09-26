package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func k0sManager(t *testing.T, mode Mode, mutate func(*Settings)) *Manager {
	t.Helper()
	viper.Set(config.DataDir, t.TempDir())
	s := defaults()
	s.Distribution, s.ControlPlane, s.Endpoint, s.ControlPlaneDisk, s.CNI = K0s, mode, "10.77.0.40", "/dev/vdb", NoCNI
	if mutate != nil {
		mutate(&s)
	}
	m, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var k0sHosts = map[string]*hardware.Host{
	"52:54:00:aa:00:40": {MAC: "52:54:00:aa:00:40", Hostname: "bluefin-cp", OS: "bluefin", Role: hardware.RoleControlPlane},
	"52:54:00:aa:00:41": {MAC: "52:54:00:aa:00:41", Hostname: "w-flatcar", OS: "flatcar"},
	"52:54:00:aa:00:42": {MAC: "52:54:00:aa:00:42", Hostname: "w-coreos", OS: "coreos", Role: hardware.RoleWorker},
	"52:54:00:aa:00:43": {MAC: "52:54:00:aa:00:43", Hostname: "w-bluefin", OS: "bluefin"},
	"52:54:00:aa:00:44": {MAC: "52:54:00:aa:00:44", Hostname: "flatcar-cp", OS: "flatcar", Role: hardware.RoleControlPlane},
}

func TestK0sNodeFilesManaged(t *testing.T) {
	m := k0sManager(t, Managed, nil)
	if _, err := (&Manager{Settings: defaults()}).K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:41"], "s", false); err != ErrNotK0s {
		t.Fatalf("kubeadm manager: %v", err)
	}
	workerTok, _ := m.Tokens.Peek(token.PurposeK0sWorker)
	if workerTok.Token != "" {
		t.Fatal("no token before the first render")
	}

	for _, mac := range []string{"52:54:00:aa:00:40", "52:54:00:aa:00:44"} {
		n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts[mac], "192.168.1.10:8080", false)
		if err != nil {
			t.Fatalf("%s: %v", mac, err)
		}
		if n.Role != k0s.Controller || n.OS != k0sHosts[mac].OS {
			t.Fatalf("%s: %+v", mac, n)
		}
		files := map[string]string{}
		for _, f := range n.Files {
			files[f.Path] = f.Contents
		}
		if files[k0s.PKIDir+"/ca.key"] != string(m.PKI.CAKey()) || files[k0s.PKIDir+"/etcd/ca.crt"] != string(m.PKI.Files()["etcd/ca.crt"]) {
			t.Fatalf("%s: PKI files must be the Manager's", mac)
		}
		w, _ := m.Tokens.Peek(token.PurposeK0sWorker)
		c, _ := m.Tokens.Peek(token.PurposeK0sController)
		manifest := files[k0s.TokensManifest]
		if !strings.Contains(manifest, "name: bootstrap-token-"+w.ID()) || !strings.Contains(manifest, "name: bootstrap-token-"+c.ID()) || strings.Count(manifest, "kind: Secret") != 2 || !strings.Contains(manifest, "\n---\n") {
			t.Fatalf("%s: tokens manifest:\n%s", mac, manifest)
		}
		if !strings.Contains(files[k0s.ConfigPath], "externalAddress: 10.77.0.40") || !strings.Contains(files[k0s.ConfigPath], "provider: kuberouter") {
			t.Fatalf("%s: k0s.yaml:\n%s", mac, files[k0s.ConfigPath])
		}
	}

	for _, mac := range []string{"52:54:00:aa:00:41", "52:54:00:aa:00:42", "52:54:00:aa:00:43"} {
		n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts[mac], "192.168.1.10:8080", false)
		if err != nil {
			t.Fatalf("%s: %v", mac, err)
		}
		if n.Role != k0s.Worker || len(n.Files) != 1 || n.Files[0].Path != k0s.TokenPath {
			t.Fatalf("%s: %+v", mac, n)
		}
		join, err := token.ParseK0s(strings.TrimSpace(n.Files[0].Contents))
		w, _ := m.Tokens.Peek(token.PurposeK0sWorker)
		if err != nil || join.Server != "https://10.77.0.40:6443" || join.Token != w.Token || string(join.CACert) != string(m.PKI.CACert()) {
			t.Fatalf("%s: token %+v %v", mac, join, err)
		}
		for _, f := range n.Files {
			if strings.Contains(f.Contents, "PRIVATE KEY") {
				t.Fatalf("%s: worker carries a private key", mac)
			}
		}
	}
	a, _ := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:44"], "s", false)
	b, _ := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:44"], "s", false)
	if len(a.Files) != len(b.Files) || a.Files[0] != b.Files[0] || a.Units[0] != b.Units[0] {
		t.Fatal("renders are deterministic")
	}
}

func TestK0sRenderCheck(t *testing.T) {
	m := k0sManager(t, Managed, func(s *Settings) { s.ControlPlaneDisk = "" })
	if _, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:44"], "s", false); err != ErrNoControlPlaneDisk {
		t.Fatalf("flatcar controller without a disk: %v", err)
	}
	if err := m.RenderCheck(k0sHosts, k0sHosts["52:54:00:aa:00:44"]); err != ErrNoControlPlaneDisk {
		t.Fatalf("RenderCheck: %v", err)
	}
	if n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:40"], "s", false); err != nil || n == nil {
		t.Fatalf("bluefin controller needs no disk: %v", err)
	}
	if n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:41"], "s", false); err != nil || n == nil {
		t.Fatalf("workers never need the disk: %v", err)
	}
	if w := m.Warnings(k0sHosts); len(w) != 1 || !strings.Contains(w[0], "52:54:00:aa:00:44 (flatcar): control-plane host needs --controlPlaneDisk") {
		t.Fatalf("warnings: %v", w)
	}
	m.Settings.Endpoint = ""
	if _, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:40"], "s", false); err == nil || !strings.Contains(err.Error(), "control-plane hosts") {
		t.Fatalf("two control planes without an endpoint: %v", err)
	}
}

func TestK0sNodeFilesExternal(t *testing.T) {
	m := k0sManager(t, External, nil)
	for _, mac := range []string{"52:54:00:aa:00:40", "52:54:00:aa:00:41"} {
		if n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts[mac], "s", false); err != nil || n != nil {
			t.Fatalf("%s: external without --k0sTokenFile renders nothing: %+v %v", mac, n, err)
		}
	}
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	tok, err := token.EncodeK0s(k0s.Worker, "k0s.example.org:6443", []byte("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"), "abcdef.0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Settings.K0sTokenFile = tokenFile
	for _, mac := range []string{"52:54:00:aa:00:41", "52:54:00:aa:00:43"} {
		n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts[mac], "s", false)
		if err != nil || n == nil || n.Role != k0s.Worker || n.Files[0].Contents != tok+"\n" {
			t.Fatalf("%s: external worker gets the file's token: %+v %v", mac, n, err)
		}
	}
	if n, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:40"], "s", false); err != nil || n != nil {
		t.Fatalf("an external control-plane host renders nothing: %+v %v", n, err)
	}
	ctl, _ := token.EncodeK0s(k0s.Controller, "k0s.example.org", []byte("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"), "abcdef.0123456789abcdef")
	if err := os.WriteFile(tokenFile, []byte(ctl), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:41"], "s", false); err == nil || !strings.Contains(err.Error(), "controller token") {
		t.Fatalf("a controller token in --k0sTokenFile is refused: %v", err)
	}
	m.Settings.K0sTokenFile = filepath.Join(dir, "missing")
	if _, err := m.K0sNodeFiles(t.Context(), k0sHosts, k0sHosts["52:54:00:aa:00:41"], "s", false); err == nil || !strings.Contains(err.Error(), "--k0sTokenFile") {
		t.Fatalf("unreadable token file: %v", err)
	}
}
