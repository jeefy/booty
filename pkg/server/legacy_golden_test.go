package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// TestLegacyRenderIsByteIdentical guards the compatibility promise of the
// cluster work: a host without a role under --profile=kubeadm-worker with a
// static join string renders exactly as it did on main before pkg/cluster
// existed. The fixtures were dumped from main (84b48bb) with the same
// settings and refreshed once in H2 for the worker join unit's
// Restart=on-failure and join.sh's already-joined guard; regenerate them
// (UPDATE_GOLDEN=1) only for an intentional rendering change.
func TestLegacyRenderIsByteIdentical(t *testing.T) {
	srv, _ := newTestServer(t)
	viper.Set(config.Profile, "kubeadm-worker")
	viper.Set(config.ContainerdDisk, "/dev/sda")
	viper.Set(config.JoinString, "kubeadm join 10.0.0.1:6443 --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"legacy-w1","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"legacy-c1","os":"coreos"}`)

	for fixture, url := range map[string]string{
		"legacy-kubeadm-worker-flatcar-builtin.json": "/ignition/builtin.json?mac=aa:bb:cc:dd:ee:01&preview=1",
		"legacy-kubeadm-worker-coreos-builtin.json":  "/ignition/builtin.json?mac=aa:bb:cc:dd:ee:02&preview=1",
		"legacy-kubeadm-worker-flatcar-merged.json":  "/ignition.json?mac=aa:bb:cc:dd:ee:01&preview=1&part=merged",
	} {
		want, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		r := do(t, http.MethodGet, srv.URL+url, "")
		if r.status != 200 {
			t.Fatalf("%s: %+v", fixture, r)
		}
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(filepath.Join("testdata", fixture), []byte(r.body), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if r.body != string(want) {
			t.Errorf("%s: rendered Ignition differs from the main fixture\n--- want\n%s\n--- got\n%s", fixture, want, r.body)
		}
	}
}
