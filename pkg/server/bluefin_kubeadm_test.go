package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ign36 "github.com/coreos/ignition/v2/config/v3_6/types"
	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/profile"
	"github.com/spf13/viper"
)

const (
	arenMAC     = "aa:bb:cc:dd:ee:a1"
	arenNodeIgn = "/bluefin/aa-bb-cc-dd-ee-a1/bluefin-node.ign"
)

func liveKubeadmFlags(t *testing.T, dir string) {
	t.Helper()
	viper.Set(config.Profile, profile.KubeadmWorker)
	viper.Set(config.K8sVersion, "v1.34.3")
	viper.Set(config.CNIVersion, "v1.1.1")
	viper.Set(config.ContainerdDisk, "/dev/sda")
	viper.Set(config.KubeadmJoin, config.KubeadmJoinAuto)
	keys := filepath.Join(dir, "config", "authorized_keys")
	if err := os.WriteFile(keys, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAren aren@example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.SSHAuthorizedKeysFl, keys)
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestBluefinKubeadmWorkerNodeConfig(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	addKubeadmSysextToFixture(t, dir)
	liveKubeadmFlags(t, dir)
	api, minter, hash := newFakeMinter(t)
	setJoinMinter(minter)
	t.Cleanup(func() { setJoinMinter(nil) })
	register(t, srv.URL, `{"mac":"`+arenMAC+`","hostname":"aren","os":"bluefin"}`)

	if resp := head(t, srv.URL+arenNodeIgn); resp.StatusCode != 200 {
		t.Fatalf("HEAD: %d", resp.StatusCode)
	}
	cfg, files, units := bluefinNode(t, srv.URL, arenMAC, "")
	if api.posts.Load() != 1 {
		t.Fatalf("the initrd's HEAD and GET share one minted token: posts=%d", api.posts.Load())
	}

	sysext := "/etc/extensions/kubeadm_" + bluefinTestVersion + ".raw"
	want := bluefinNodeFile{mode: 0o644, source: "http://192.168.1.10:8080/data/bluefin/" + bluefinTestVersion + "/kubeadm_" + bluefinTestVersion + ".raw",
		hash: "sha256-" + sha256Hex("KUBEADM-"+bluefinTestVersion), overwrite: true, remote: true}
	if got := files[sysext]; got != want {
		t.Fatalf("kubeadm sysext:\n got %+v\nwant %+v", got, want)
	}
	if r := do(t, http.MethodGet, strings.Replace(want.source, "http://192.168.1.10:8080", srv.URL, 1), ""); r.status != 200 || "sha256-"+sha256Hex(r.body) != want.hash {
		t.Fatalf("the sysext source serves the verified bytes: %+v", r)
	}
	for p := range files {
		if strings.HasPrefix(p, "/opt") {
			t.Errorf("/opt stays a plain writable directory for the CNI: %s", p)
		}
	}
	if f := files["/etc/default/kubelet"]; f.contents != "KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false\n" || !f.overwrite {
		t.Fatalf("/etc/default/kubelet: %+v", f)
	}
	if f := files[profile.BluefinJoinScript]; f.mode != 0o755 || !strings.Contains(f.contents, "export PATH=/usr/bin:/usr/sbin") {
		t.Fatalf("join script: %+v", f)
	}

	if len(cfg.Storage.Disks) != 0 || len(cfg.Storage.Filesystems) != 0 {
		t.Fatalf("the containerd disk is never partitioned, formatted or wiped by Ignition: %+v %+v", cfg.Storage.Disks, cfg.Storage.Filesystems)
	}
	enabled := func(u ign36.Unit) bool { return u.Enabled != nil && *u.Enabled }
	for _, name := range []string{"sshd.service", "containerd.service", "kubelet.service", profile.BluefinUnitJoin} {
		if !enabled(units[name]) {
			t.Errorf("%s must be enabled: %+v", name, units[name])
		}
	}
	for _, name := range []string{`var-lib-containerd\x2ddisk.mount`, profile.BluefinContainerdDirUnit, "var-lib-containerd.mount"} {
		u, ok := units[name]
		if !ok || u.Contents == nil || enabled(u) {
			t.Errorf("%s is written and pulled in by containerd, not enabled: %+v", name, u)
		}
	}
	if u := units[`var-lib-containerd\x2ddisk.mount`]; u.Contents == nil || !strings.Contains(*u.Contents, "What=/dev/sda\n") || !strings.Contains(*u.Contents, "Where=/var/lib/containerd-disk\n") {
		t.Fatalf("disk mount: %+v", u)
	}
	if u := units["var-lib-containerd.mount"]; u.Contents == nil || !strings.Contains(*u.Contents, "What=/var/lib/containerd-disk/bluefin\n") || !strings.Contains(*u.Contents, "Options=bind,nofail\n") {
		t.Fatalf("bind mount: %+v", u)
	}
	ctd := units["containerd.service"]
	if ctd.Contents != nil || len(ctd.Dropins) != 1 || ctd.Dropins[0].Contents == nil || !strings.Contains(*ctd.Dropins[0].Contents, "Requires=var-lib-containerd.mount\nAfter=var-lib-containerd.mount\n") {
		t.Fatalf("containerd.service keeps the sysext's unit, plus a drop-in on the bind mount: %+v", ctd)
	}
	join := units[profile.BluefinUnitJoin]
	if join.Contents == nil {
		t.Fatal("join unit has no contents")
	}
	for _, s := range []string{`Environment="JOIN_STRING=kubeadm join 192.168.1.10:6443 --token `, "--discovery-token-ca-cert-hash sha256:" + hash + `"`, `Environment="NODE_NAME=aren"`, "Restart=on-failure\n", "After=network-online.target systemd-sysext.service containerd.service\n"} {
		if !strings.Contains(*join.Contents, s) {
			t.Errorf("join unit missing %q:\n%s", s, *join.Contents)
		}
	}

	if _, _, units := bluefinNode(t, srv.URL, arenMAC, "?preview=1"); api.posts.Load() != 1 || !strings.Contains(*units[profile.BluefinUnitJoin].Contents, "--token ") {
		t.Fatalf("a preview shows the cached token and never mints: posts=%d", api.posts.Load())
	}

	viper.Set(config.ContainerdDisk, "")
	_, _, units = bluefinNode(t, srv.URL, arenMAC, "?preview=1")
	if _, ok := units["var-lib-containerd.mount"]; ok || len(units["containerd.service"].Dropins) != 0 {
		t.Fatalf("without --containerdDisk containerd keeps its root in RAM: %v", units)
	}
}

func TestBluefinKubeadmWorkerStaticJoin(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	addKubeadmSysextToFixture(t, dir)
	liveKubeadmFlags(t, dir)
	viper.Set(config.KubeadmJoin, config.KubeadmJoinStatic)
	viper.Set(config.JoinString, "kubeadm join 10.0.0.1:6443 --token t.s --discovery-token-ca-cert-hash sha256:abc")
	register(t, srv.URL, `{"mac":"`+arenMAC+`","hostname":"aren","os":"bluefin"}`)

	_, _, units := bluefinNode(t, srv.URL, arenMAC, "?preview=1")
	if u := units[profile.BluefinUnitJoin]; u.Contents == nil || !strings.Contains(*u.Contents, `Environment="JOIN_STRING=kubeadm join 10.0.0.1:6443 --token t.s --discovery-token-ca-cert-hash sha256:abc"`) {
		t.Fatalf("static join string: %+v", u)
	}
}

func TestBluefinKubeadmWorkerFailsClosedWithoutSysext(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	liveKubeadmFlags(t, dir)
	api, minter, _ := newFakeMinter(t)
	setJoinMinter(minter)
	t.Cleanup(func() { setJoinMinter(nil) })
	register(t, srv.URL, `{"mac":"`+arenMAC+`","hostname":"aren","os":"bluefin"}`)
	logs := captureLogs(t)

	cfg, files, units := bluefinNode(t, srv.URL, arenMAC, "")
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "Bluefin release has no kubeadm sysext; serving the node config without the kubeadm join") {
		t.Fatalf("a release without the kubeadm sysext is logged as an error:\n%s", logs)
	}
	if len(units) != 1 || !*units["sshd.service"].Enabled || len(files) != 1 || files["/etc/hostname"].contents != "aren\n" || len(cfg.Passwd.Users) != 1 {
		t.Fatalf("the host still boots diskless with hostname and SSH, nothing kubeadm: %v %v", files, units)
	}
	if api.posts.Load() != 0 {
		t.Fatalf("no join, no token: posts=%d", api.posts.Load())
	}

	state := filepath.Join(dir, "bluefin", "current")
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if _, _, units := bluefinNode(t, srv.URL, arenMAC, "?preview=1"); len(units) != 1 || !strings.Contains(logs.String(), "No Bluefin release cached; serving the node config without the kubeadm join") {
		t.Fatalf("no release at all: %v\n%s", units, logs)
	}
}

func TestBluefinKubeadmWorkerClusterWarnings(t *testing.T) {
	srv, _ := newTestServer(t)
	register(t, srv.URL, `{"mac":"`+arenMAC+`","hostname":"aren","os":"bluefin"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:a2","hostname":"cp","os":"bluefin","role":"control-plane"}`)
	for _, c := range []struct {
		profile string
		want    []string
	}{
		{"", []string{"host " + arenMAC + " (bluefin) unsupported under kubeadm", "host aa:bb:cc:dd:ee:a2 (bluefin) unsupported under kubeadm"}},
		{profile.KubeadmWorker, []string{"host aa:bb:cc:dd:ee:a2 (bluefin) unsupported under kubeadm"}},
	} {
		viper.Set(config.Profile, c.profile)
		m := &cluster.Manager{Settings: cluster.FromConfig()}
		if got := m.Warnings(hardware.Snapshot().Hosts); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("profile %q: warnings %v, want %v", c.profile, got, c.want)
		}
	}
}
