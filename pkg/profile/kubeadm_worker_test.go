package profile

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	v3_4 "github.com/coreos/ignition/v2/config/v3_4"
	"github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/hardware"
)

func worker(t *testing.T, host *hardware.Host, opts Options) (types.Config, string) {
	t.Helper()
	opts.Profile = KubeadmWorker
	cfg, err := Fragment(host, opts)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parsed, rpt, err := v3_4.Parse(raw)
	if err != nil || rpt.IsFatal() {
		t.Fatalf("v3_4.Parse: %v\n%s\n%s", err, rpt.String(), raw)
	}
	return parsed, string(raw)
}

func decodeFile(t *testing.T, cfg types.Config, path string) string {
	t.Helper()
	for _, f := range cfg.Storage.Files {
		if f.Path != path {
			continue
		}
		if f.Mode == nil || *f.Mode != 0o755 {
			t.Fatalf("%s must be 0755, got %v", path, f.Mode)
		}
		src := *f.Contents.Source
		if strings.HasPrefix(src, "data:text/plain;charset=utf-8;base64,") {
			b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(src, "data:text/plain;charset=utf-8;base64,"))
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		if strings.HasPrefix(src, "data:,") {
			return strings.TrimPrefix(src, "data:,")
		}
		t.Fatalf("%s is not inline: %s", path, src)
	}
	t.Fatalf("file %s missing", path)
	return ""
}

func TestValidate(t *testing.T) {
	if err := Validate(""); err != nil {
		t.Fatal(err)
	}
	if err := Validate(KubeadmWorker); err != nil {
		t.Fatal(err)
	}
	if err := Validate("bogus"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown profile must be rejected, got %v", err)
	}
	if _, err := Fragment(&hardware.Host{OS: "flatcar"}, Options{Profile: "bogus"}); err == nil {
		t.Fatal("Fragment must reject unknown profiles")
	}
}

func TestNoProfileIsEmpty(t *testing.T) {
	cfg, err := Fragment(&hardware.Host{OS: "flatcar"}, Options{})
	if err != nil || len(cfg.Storage.Files)+len(cfg.Systemd.Units) != 0 || cfg.Ignition.Version != "3.4.0" {
		t.Fatalf("empty profile must be an empty 3.4.0 config: %+v err=%v", cfg, err)
	}
}

func TestSkipsUblue(t *testing.T) {
	cfg, err := Fragment(&hardware.Host{MAC: "aa:bb:cc:dd:ee:01", OS: "ublue"}, Options{Profile: KubeadmWorker, JoinString: "kubeadm join x"})
	if err != nil || len(cfg.Storage.Files)+len(cfg.Systemd.Units)+len(cfg.Storage.Filesystems) != 0 {
		t.Fatalf("ublue hosts must not get the profile: %+v err=%v", cfg, err)
	}
	for _, os := range []string{"", "flatcar", "coreos"} {
		if !AppliesTo(os) {
			t.Errorf("profile should apply to %q", os)
		}
	}
	if AppliesTo("ublue") {
		t.Error("profile must not apply to ublue")
	}
}

func TestScriptsCarryVersions(t *testing.T) {
	cfg, raw := worker(t, &hardware.Host{OS: "flatcar", Hostname: "n1"}, Options{
		K8sVersion:     "v1.34.3",
		CNIVersion:     "v1.1.1",
		JoinString:     "kubeadm join 10.0.0.1:6443 --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash sha256:deadbeef",
		ContainerdDisk: "",
	})
	if len(cfg.Storage.Filesystems) != 0 {
		t.Fatalf("no containerd disk requested, got %+v", cfg.Storage.Filesystems)
	}
	cni := decodeFile(t, cfg, CNIScript)
	if !strings.Contains(cni, `CNI_VERSION="v1.1.1"`) || !strings.Contains(cni, "containernetworking/plugins/releases/download/${CNI_VERSION}") {
		t.Fatalf("cni.sh:\n%s", cni)
	}
	tools := decodeFile(t, cfg, KubeToolsScript)
	for _, want := range []string{
		`RELEASE="v1.34.3"`, `CRICTL="v1.34.0"`,
		"pkgs.k8s.io/core:/stable:/${KUBE_MINOR}/rpm/", "dnf install -y",
		"https://dl.k8s.io/${RELEASE}/bin/linux/amd64/{kubeadm,kubelet,kubectl}",
		"github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL}/crictl-${CRICTL}-linux-amd64.tar.gz",
		"mkdir -p /etc/kubernetes/manifests",
	} {
		if !strings.Contains(tools, want) {
			t.Errorf("kube-tools.sh missing %q", want)
		}
	}
	if strings.Contains(tools, "kubernetes-incubator") {
		t.Error("kube-tools.sh must use the kubernetes-sigs cri-tools URL")
	}
	sysd := decodeFile(t, cfg, SystemdScript)
	for _, want := range []string{
		`UNITS="https://raw.githubusercontent.com/kubernetes/release/master/cmd/krel/templates/latest"`,
		"${UNITS}/kubelet/kubelet.service", "${UNITS}/kubeadm/10-kubeadm.conf",
		"/etc/default/kubelet", "/etc/sysconfig/kubelet", "--fail-swap-on=false",
	} {
		if !strings.Contains(sysd, want) {
			t.Errorf("systemd.sh missing %q", want)
		}
	}
	join := decodeFile(t, cfg, JoinScript)
	for _, want := range []string{"modprobe br_netfilter", "kubeadm reset -f || true", "exec ${JOIN_STRING}", `-z "${JOIN_STRING:-}"`} {
		if !strings.Contains(join, want) {
			t.Errorf("join.sh missing %q", want)
		}
	}
	if !strings.Contains(raw, `Environment=\"JOIN_STRING=kubeadm join 10.0.0.1:6443 --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash sha256:deadbeef\"`) {
		t.Fatalf("join unit must carry the join string:\n%s", raw)
	}
	if !strings.Contains(raw, `PATH=/opt/bin:$$PATH exec /opt/booty/join.sh`) {
		t.Fatalf("join unit must escape $PATH for systemd:\n%s", raw)
	}
	if strings.Contains(raw, "http://") {
		t.Fatalf("scripts must be inline, nothing fetched from Booty:\n%s", raw)
	}
}

func TestCustomVersionsAndUnitsURL(t *testing.T) {
	cfg, _ := worker(t, &hardware.Host{OS: "coreos"}, Options{
		K8sVersion: "v1.35.0", CNIVersion: "v1.9.0", CrictlVersion: "v1.35.1",
		KubeletUnitsURL: "https://mirror.example/krel/",
	})
	tools := decodeFile(t, cfg, KubeToolsScript)
	if !strings.Contains(tools, `RELEASE="v1.35.0"`) || !strings.Contains(tools, `CRICTL="v1.35.1"`) {
		t.Fatalf("versions not applied:\n%s", tools)
	}
	if cni := decodeFile(t, cfg, CNIScript); !strings.Contains(cni, `CNI_VERSION="v1.9.0"`) {
		t.Fatalf("cni version not applied:\n%s", cni)
	}
	if sysd := decodeFile(t, cfg, SystemdScript); !strings.Contains(sysd, `UNITS="https://mirror.example/krel"`) {
		t.Fatalf("units URL not applied (trailing slash must be trimmed):\n%s", sysd)
	}
}

func TestUnitChainOrdering(t *testing.T) {
	cfg, _ := worker(t, &hardware.Host{OS: "flatcar"}, Options{JoinString: "kubeadm join x"})
	var names []string
	byName := map[string]types.Unit{}
	for _, u := range cfg.Systemd.Units {
		names = append(names, u.Name)
		byName[u.Name] = u
	}
	want := []string{UnitCNIInstall, UnitKubeTools, UnitKubeletSetup, UnitJoin}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("units %v want %v", names, want)
	}
	for i, name := range want {
		u := byName[name]
		if u.Enabled == nil || !*u.Enabled {
			t.Errorf("%s must be enabled", name)
		}
		c := *u.Contents
		if !strings.Contains(c, "Type=oneshot") || !strings.Contains(c, "WantedBy=multi-user.target") {
			t.Errorf("%s must be an enabled oneshot:\n%s", name, c)
		}
		if i == 0 {
			if !strings.Contains(c, "Wants=network-online.target") || !strings.Contains(c, "After=network-online.target") {
				t.Errorf("first unit must wait for the network:\n%s", c)
			}
			continue
		}
		prev := want[i-1]
		if !strings.Contains(c, "Requires="+prev+"\n") || !strings.Contains(c, "After="+prev+"\n") {
			t.Errorf("%s must Requires/After %s:\n%s", name, prev, c)
		}
	}
}

func TestContainerdDisk(t *testing.T) {
	cfg, _ := worker(t, &hardware.Host{OS: "flatcar"}, Options{ContainerdDisk: "/dev/sda"})
	if len(cfg.Storage.Filesystems) != 1 {
		t.Fatalf("expected one filesystem, got %+v", cfg.Storage.Filesystems)
	}
	fs := cfg.Storage.Filesystems[0]
	if fs.Device != "/dev/sda" || *fs.Format != "ext4" || *fs.Label != "ssd" || *fs.Path != "/var/lib/containerd" || fs.WipeFilesystem == nil || !*fs.WipeFilesystem {
		t.Fatalf("filesystem %+v", fs)
	}
	units := map[string]types.Unit{}
	for _, u := range cfg.Systemd.Units {
		units[u.Name] = u
	}
	mount, ok := units[ContainerdMountUnit]
	if !ok || mount.Enabled == nil || !*mount.Enabled {
		t.Fatalf("mount unit missing or disabled: %+v", mount)
	}
	for _, want := range []string{"What=/dev/disk/by-label/ssd", "Where=/var/lib/containerd", "Type=ext4", "WantedBy=local-fs.target"} {
		if !strings.Contains(*mount.Contents, want) {
			t.Errorf("mount unit missing %q", want)
		}
	}
	containerd, ok := units["containerd.service"]
	if !ok || len(containerd.Dropins) != 1 || containerd.Dropins[0].Name != "10-wait-containerd.conf" {
		t.Fatalf("containerd dropin missing: %+v", containerd)
	}
	if d := *containerd.Dropins[0].Contents; !strings.Contains(d, "After=var-lib-containerd.mount") || !strings.Contains(d, "Requires=var-lib-containerd.mount") {
		t.Fatalf("dropin must order containerd after the mount:\n%s", d)
	}
	if len(cfg.Systemd.Units) != 6 {
		t.Fatalf("expected mount + dropin + 4 chain units, got %d", len(cfg.Systemd.Units))
	}
}

func TestEmptyJoinStringStillEmitsUnit(t *testing.T) {
	_, raw := worker(t, &hardware.Host{OS: "flatcar"}, Options{})
	if !strings.Contains(raw, `Environment=\"JOIN_STRING=\"`) {
		t.Fatalf("join unit must be emitted with an empty JOIN_STRING:\n%s", raw)
	}
}

func TestSystemdQuote(t *testing.T) {
	got := systemdQuote("kubeadm join a \"b\" c\\d\n --x")
	if got != `kubeadm join a \"b\" c\\d --x` {
		t.Fatalf("systemdQuote = %q", got)
	}
}

func TestCrictlVersionFor(t *testing.T) {
	for in, want := range map[string]string{"v1.34.3": "v1.34.0", "v1.35.0": "v1.35.0", "1.33.7": "v1.33.0", "weird": "weird"} {
		if got := crictlVersionFor(in); got != want {
			t.Errorf("crictlVersionFor(%q) = %q, want %q", in, got, want)
		}
	}
}
