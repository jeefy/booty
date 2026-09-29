package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

var (
	goldenToken  = regexp.MustCompile(`--token [a-z0-9]{6}\.[a-z0-9]{16}`)
	goldenCAHash = regexp.MustCompile(`sha256:[0-9a-f]{64}`)
)

// TestLiveKubeadmWorkerFlatcarRenderIsByteIdentical pins what a Flatcar
// (and CoreOS) kubeadm worker gets under the live deployment's flags
// (--profile=kubeadm-worker --kubeadmJoin=auto --containerdDisk=/dev/sda,
// SSH keys from a file, the example site template, an external kubeadm
// cluster Manager) for a real boot: wrapper, builtin and user children,
// plus the merged preview. The fixtures were dumped from c9f5fef, before
// Bluefin hosts could join a kubeadm cluster, and refreshed for autopilot
// P1, which adds exactly booty-health.service and its script; a Bluefin
// host registered next to them must not change a byte. Minted tokens and
// the fake CA's discovery hash are random per run and normalised before
// comparing.
func TestLiveKubeadmWorkerFlatcarRenderIsByteIdentical(t *testing.T) {
	srv, dir := newTestServer(t)
	_, minter, _ := newFakeMinter(t)
	setJoinMinter(minter)
	t.Cleanup(func() { setJoinMinter(nil) })
	viper.Set(config.Profile, "kubeadm-worker")
	viper.Set(config.K8sVersion, "v1.34.3")
	viper.Set(config.CNIVersion, "v1.1.1")
	viper.Set(config.ContainerdDisk, "/dev/sda")
	viper.Set(config.KubeadmJoin, config.KubeadmJoinAuto)
	keys := filepath.Join(dir, "config", "authorized_keys")
	if err := os.WriteFile(keys, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGolden golden@example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.SSHAuthorizedKeysFl, keys)
	site, err := os.ReadFile(filepath.Join("..", "..", "examples", "config", "ignition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "ignition.yaml"), site, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := cluster.New(cluster.FromConfig())
	if err != nil {
		t.Fatal(err)
	}
	setClusterManager(m)
	t.Cleanup(func() { setClusterManager(nil) })

	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"aren","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"live-c1","os":"coreos"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:03","hostname":"live-b1","os":"bluefin"}`)

	// Ordered like a boot: the wrapper fetch mints, the children reuse it.
	for _, step := range []struct{ fixture, url string }{
		{"live-kubeadm-worker-flatcar-wrapper.json", "/ignition.json?mac=aa:bb:cc:dd:ee:01"},
		{"live-kubeadm-worker-flatcar-builtin.json", "/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01"},
		{"live-kubeadm-worker-flatcar-user.json", "/ignition/user.json?mac=aa:bb:cc:dd:ee:01"},
		{"live-kubeadm-worker-flatcar-merged.json", "/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1&part=merged"},
		{"live-kubeadm-worker-coreos-builtin.json", "/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02"},
	} {
		r := do(t, http.MethodGet, srv.URL+step.url, "")
		if r.status != 200 {
			t.Fatalf("%s: %+v", step.fixture, r)
		}
		got := goldenCAHash.ReplaceAllString(goldenToken.ReplaceAllString(r.body, "--token <token>"), "sha256:<ca-hash>")
		path := filepath.Join("testdata", step.fixture)
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: rendered Ignition differs from the c9f5fef fixture\n--- want\n%s\n--- got\n%s", step.fixture, want, got)
		}
	}
}
