package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

// addKubeadmSysextToFixture adds kubeadm_<version>.raw to the served
// fixture release, as a release carrying the kubeadm sysext has it.
func addKubeadmSysextToFixture(t *testing.T, dir string) {
	t.Helper()
	rel := filepath.Join(dir, "bluefin", bluefinTestVersion)
	file := "kubeadm_" + bluefinTestVersion + ".raw"
	if err := os.WriteFile(filepath.Join(rel, file), []byte("KUBEADM-"+bluefinTestVersion), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rel, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m versions.BluefinManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.Sysexts["kubeadm"] = versions.BluefinSysext{File: file, Sha256: sha256Hex("KUBEADM-" + bluefinTestVersion)}
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBluefinNodeWithoutKubeadmJoinIsByteIdentical pins the node config of
// Bluefin hosts that do not join the kubeadm cluster, with the kubeadm
// sysext in the release: no --profile, and under --profile=kubeadm-worker
// a control-plane host, a k0s host and an installing host. The fixtures
// were dumped from c9f5fef, before Bluefin kubeadm workers existed.
func TestBluefinNodeWithoutKubeadmJoinIsByteIdentical(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	addKubeadmSysextToFixture(t, dir)
	viper.Set(config.SSHAuthorizedKeys, []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGolden golden@example"})
	viper.Set(config.ContainerdDisk, "/dev/sda")
	viper.Set(config.KubeadmJoin, config.KubeadmJoinStatic)
	viper.Set(config.JoinString, "kubeadm join 10.0.0.1:6443 --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c1","hostname":"plain","os":"bluefin","stateDisk":"/dev/vdb","extensions":["zfs"]}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c2","hostname":"cp","os":"bluefin","role":"control-plane"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c3","hostname":"k0s","os":"bluefin","extensions":["k0s"]}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c4","hostname":"inst","os":"bluefin","doInstall":true,"installDisk":"/dev/nvme0n1"}`)

	for _, c := range []struct {
		fixture, profile, mac string
	}{
		{"bluefin-node-no-profile.json", "", "aa:bb:cc:dd:ee:c1"},
		{"bluefin-node-kubeadm-profile-control-plane.json", "kubeadm-worker", "aa:bb:cc:dd:ee:c2"},
		{"bluefin-node-kubeadm-profile-k0s.json", "kubeadm-worker", "aa:bb:cc:dd:ee:c3"},
		{"bluefin-node-kubeadm-profile-installing.json", "kubeadm-worker", "aa:bb:cc:dd:ee:c4"},
	} {
		viper.Set(config.Profile, c.profile)
		r := do(t, http.MethodGet, srv.URL+"/bluefin/"+strings.ReplaceAll(c.mac, ":", "-")+"/bluefin-node.ign?preview=1", "")
		if r.status != 200 {
			t.Fatalf("%s: %+v", c.fixture, r)
		}
		path := filepath.Join("testdata", c.fixture)
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(path, []byte(r.body), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if r.body != string(want) {
			t.Errorf("%s: node config differs from the c9f5fef fixture\n--- want\n%s\n--- got\n%s", c.fixture, want, r.body)
		}
	}
}
