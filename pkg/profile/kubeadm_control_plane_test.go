package profile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/cni"
	"github.com/jeefy/booty/pkg/hardware"
)

const (
	testCACert = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n"
	testCAKey  = "-----BEGIN RSA PRIVATE KEY-----\nMIIEtest\n-----END RSA PRIVATE KEY-----\n"
)

func cpOptions(t *testing.T, cniName string) *ControlPlaneOptions {
	t.Helper()
	var install *cni.Install
	if cniName != cni.None {
		var err error
		if install, err = cni.Render(cniName, "", "10.244.0.0/16", UnitInit); err != nil {
			t.Fatal(err)
		}
	}
	return &ControlPlaneOptions{
		Server:         "192.168.1.10:8080",
		Endpoint:       "10.77.0.10",
		PodCIDR:        "10.244.0.0/16",
		ServiceCIDR:    "10.96.0.0/12",
		CACert:         []byte(testCACert),
		CAKey:          []byte(testCAKey),
		BootstrapToken: "abcdef.0123456789abcdef",
		CertificateKey: strings.Repeat("ab", 32),
		Disk:           "/dev/vda",
		CNI:            install,
	}
}

func controlPlane(t *testing.T, os string, opts Options) (types.Config, string) {
	t.Helper()
	host := &hardware.Host{MAC: "aa:bb:cc:dd:ee:01", Hostname: "cp1", OS: os, Role: hardware.RoleControlPlane}
	return worker(t, host, opts)
}

func unitsByName(cfg types.Config) (map[string]types.Unit, []string) {
	byName := map[string]types.Unit{}
	var names []string
	for _, u := range cfg.Systemd.Units {
		byName[u.Name] = u
		names = append(names, u.Name)
	}
	return byName, names
}

func fileMode(t *testing.T, cfg types.Config, path string) int {
	t.Helper()
	for _, f := range cfg.Storage.Files {
		if f.Path == path {
			return *f.Mode
		}
	}
	t.Fatalf("file %s missing", path)
	return 0
}

func decodeAnyFile(t *testing.T, cfg types.Config, path string) string {
	t.Helper()
	for i := range cfg.Storage.Files {
		if cfg.Storage.Files[i].Path == path {
			mode := 0o755
			cfg.Storage.Files[i].Mode = &mode
			return decodeFile(t, cfg, path)
		}
	}
	t.Fatalf("file %s missing", path)
	return ""
}

func TestControlPlaneHostWithoutManagedOptionsRendersNothing(t *testing.T) {
	cfg, err := Fragment(&hardware.Host{OS: "flatcar", Role: hardware.RoleControlPlane}, Options{Profile: KubeadmWorker, JoinString: "kubeadm join x"})
	if err != nil || len(cfg.Storage.Files)+len(cfg.Systemd.Units)+len(cfg.Storage.Filesystems) != 0 {
		t.Fatalf("a control-plane host must never get worker join units: %+v err=%v", cfg, err)
	}
}

func TestControlPlaneFragment(t *testing.T) {
	for _, os := range []string{"flatcar", "coreos"} {
		t.Run(os, func(t *testing.T) {
			cfg, raw := controlPlane(t, os, Options{ControlPlane: cpOptions(t, cni.Cilium), ContainerdDisk: "/dev/vdb"})
			units, names := unitsByName(cfg)
			want := []string{
				ContainerdMountUnit, "containerd.service",
				ControlPlaneMountUnit, UnitSeed, "etc-kubernetes.mount", "var-lib-etcd.mount", "var-lib-kubelet.mount", "kubelet.service",
				UnitCNIInstall, UnitKubeTools, UnitKubeletSetup, UnitInit, UnitClusterReady, cni.UnitName,
			}
			if strings.Join(names, ",") != strings.Join(want, ",") {
				t.Fatalf("units\n got %v\nwant %v", names, want)
			}
			if _, ok := units[UnitJoin]; ok {
				t.Fatal("control plane must not carry the worker join unit")
			}
			if strings.Contains(raw, "JOIN_STRING") || strings.Contains(raw, "join.sh") {
				t.Fatal("control plane must not carry join material")
			}

			if fileMode(t, cfg, CACertPath) != 0o644 || fileMode(t, cfg, CAKeyPath) != 0o600 || fileMode(t, cfg, KubeadmInitConfig) != 0o600 {
				t.Fatal("CA and init config modes")
			}
			if decodeAnyFile(t, cfg, CAKeyPath) != testCAKey || decodeAnyFile(t, cfg, CACertPath) != testCACert {
				t.Fatal("CA files must be the Manager's PKI")
			}
			initYAML := decodeAnyFile(t, cfg, KubeadmInitConfig)
			for _, want := range []string{
				"kind: InitConfiguration", "apiVersion: kubeadm.k8s.io/v1beta4", "token: abcdef.0123456789abcdef", "ttl: 168h0m0s",
				"certificateKey: " + strings.Repeat("ab", 32), "criSocket: unix:///run/containerd/containerd.sock",
				"kind: ClusterConfiguration", "kubernetesVersion: v1.34.3", "controlPlaneEndpoint: 10.77.0.10",
				"podSubnet: 10.244.0.0/16", "serviceSubnet: 10.96.0.0/12", "clusterName: kubernetes",
				"kind: KubeletConfiguration", "cgroupDriver: systemd",
			} {
				if !strings.Contains(initYAML, want) {
					t.Errorf("kubeadm-init.yaml missing %q:\n%s", want, initYAML)
				}
			}

			if len(cfg.Storage.Filesystems) != 2 {
				t.Fatalf("filesystems %+v", cfg.Storage.Filesystems)
			}
			cp := cfg.Storage.Filesystems[1]
			if cp.Device != "/dev/vda" || *cp.Format != "ext4" || *cp.Label != ControlPlaneDiskLabel || *cp.Path != ControlPlaneMount || *cp.WipeFilesystem {
				t.Fatalf("control-plane filesystem %+v", cp)
			}
			if cfg.Storage.Filesystems[0].Device != "/dev/vdb" {
				t.Fatalf("containerd filesystem %+v", cfg.Storage.Filesystems[0])
			}

			mount := *units[ControlPlaneMountUnit].Contents
			for _, want := range []string{"What=/dev/disk/by-label/booty-cp", "Where=/var/lib/booty-cp", "Type=ext4", "WantedBy=local-fs.target"} {
				if !strings.Contains(mount, want) {
					t.Errorf("disk mount missing %q", want)
				}
			}
			for _, m := range bindMounts {
				c := *units[m.unit].Contents
				for _, want := range []string{"What=/var/lib/booty-cp/" + m.sub, "Where=" + m.where, "Type=none", "Options=bind", "RequiresMountsFor=/var/lib/booty-cp", "Requires=" + UnitSeed, "After=" + UnitSeed} {
					if !strings.Contains(c, want) {
						t.Errorf("%s missing %q:\n%s", m.unit, want, c)
					}
				}
			}
			seed := *units[UnitSeed].Contents
			if !strings.Contains(seed, "DefaultDependencies=no") || !strings.Contains(seed, "RequiresMountsFor=/var/lib/booty-cp") || !strings.Contains(seed, "Before=local-fs.target") {
				t.Errorf("seed unit:\n%s", seed)
			}
			seedSh := decodeAnyFile(t, cfg, SeedScript)
			if !strings.Contains(seedSh, "cp -an /etc/kubernetes/. /var/lib/booty-cp/kubernetes/") || !strings.Contains(seedSh, "mkdir -p /var/lib/booty-cp/etcd /var/lib/etcd") {
				t.Errorf("seed script:\n%s", seedSh)
			}
			kubelet := units["kubelet.service"]
			if len(kubelet.Dropins) != 1 || kubelet.Dropins[0].Name != KubeletDropinName || !strings.Contains(*kubelet.Dropins[0].Contents, "RequiresMountsFor=/etc/kubernetes /var/lib/etcd /var/lib/kubelet") {
				t.Errorf("kubelet drop-in %+v", kubelet)
			}
			if !strings.Contains(*units[UnitKubeletSetup].Contents, "RequiresMountsFor=/etc/kubernetes /var/lib/etcd /var/lib/kubelet") {
				t.Error("kubelet setup must wait for the state mounts")
			}

			initUnitText := *units[UnitInit].Contents
			for _, want := range []string{"Requires=" + UnitKubeletSetup, "After=" + UnitKubeletSetup, "ConditionPathExists=!/etc/kubernetes/kubelet.conf",
				"RequiresMountsFor=/etc/kubernetes", "Type=oneshot", "RemainAfterExit=yes", "Restart=on-failure", "RestartSec=30s", "ExecStart=" + InitScript, "WantedBy=multi-user.target"} {
				if !strings.Contains(initUnitText, want) {
					t.Errorf("init unit missing %q", want)
				}
			}
			initSh := decodeAnyFile(t, cfg, InitScript)
			if !strings.Contains(initSh, "exec kubeadm init --config /etc/booty/kubeadm-init.yaml --upload-certs") || !strings.Contains(initSh, "modprobe br_netfilter") || !strings.Contains(initSh, "net.ipv4.ip_forward=1") {
				t.Errorf("init script:\n%s", initSh)
			}

			ready := *units[UnitClusterReady].Contents
			for _, want := range []string{"Wants=" + UnitInit, "After=" + UnitInit, "Restart=on-failure", "RestartSec=30s", "ExecStart=" + ClusterReadyScript} {
				if !strings.Contains(ready, want) {
					t.Errorf("ready unit missing %q", want)
				}
			}
			if strings.Contains(ready, "Requires="+UnitInit) {
				t.Error("ready unit must only Want the init unit so a restarting init does not cancel it")
			}
			readySh := decodeAnyFile(t, cfg, ClusterReadyScript)
			if !strings.Contains(readySh, `-X POST "http://192.168.1.10:8080/cluster/ready?mac=$MAC"`) || !strings.Contains(readySh, "get --raw /readyz") {
				t.Errorf("ready script:\n%s", readySh)
			}

			cniUnit := *units[cni.UnitName].Contents
			if !strings.Contains(cniUnit, "After="+UnitInit) || !strings.Contains(cniUnit, "ConditionPathExists=!"+cni.MarkerPath) {
				t.Errorf("cni unit:\n%s", cniUnit)
			}
			if sh := decodeAnyFile(t, cfg, cni.ScriptPath); !strings.Contains(sh, "cilium install") {
				t.Errorf("cni script:\n%s", sh)
			}
			if tools := decodeAnyFile(t, cfg, KubeToolsScript); !strings.Contains(tools, `RELEASE="v1.34.3"`) {
				t.Error("tool chain must be shared with the worker")
			}
		})
	}
}

func TestControlPlaneWithoutCNI(t *testing.T) {
	cfg, raw := controlPlane(t, "flatcar", Options{ControlPlane: cpOptions(t, cni.None)})
	units, _ := unitsByName(cfg)
	if _, ok := units[cni.UnitName]; ok || strings.Contains(raw, "cni-apply") {
		t.Fatal("--cni=none must not render the apply unit")
	}
	if len(cfg.Storage.Filesystems) != 1 {
		t.Fatalf("only the control-plane disk: %+v", cfg.Storage.Filesystems)
	}
	for _, name := range []string{cni.Calico, cni.Flannel} {
		cfg, _ := controlPlane(t, "flatcar", Options{ControlPlane: cpOptions(t, name)})
		if sh := decodeAnyFile(t, cfg, cni.ScriptPath); !strings.Contains(sh, cni.Pin(name)) {
			t.Errorf("%s script must carry its pin", name)
		}
	}
}

func TestControlPlaneOptionsValidation(t *testing.T) {
	host := &hardware.Host{OS: "flatcar", Role: hardware.RoleControlPlane}
	for name, mutate := range map[string]func(*ControlPlaneOptions){
		"disk":     func(o *ControlPlaneOptions) { o.Disk = "" },
		"ca":       func(o *ControlPlaneOptions) { o.CAKey = nil },
		"token":    func(o *ControlPlaneOptions) { o.BootstrapToken = "" },
		"certkey":  func(o *ControlPlaneOptions) { o.CertificateKey = "" },
		"endpoint": func(o *ControlPlaneOptions) { o.Endpoint = "" },
		"server":   func(o *ControlPlaneOptions) { o.Server = "" },
		"quotes":   func(o *ControlPlaneOptions) { o.Endpoint = `10.0.0.1" evil` },
	} {
		o := cpOptions(t, cni.None)
		mutate(o)
		if _, err := Fragment(host, Options{Profile: KubeadmWorker, ControlPlane: o}); err == nil {
			t.Errorf("%s: Fragment must fail", name)
		}
	}
	if _, err := Fragment(host, Options{Profile: KubeadmWorker, ControlPlane: cpOptions(t, cni.None), ContainerdDisk: "/dev/vda"}); err == nil || !strings.Contains(err.Error(), "containerd disk") {
		t.Fatalf("same device for both disks must fail, got %v", err)
	}
	if _, err := Fragment(host, Options{ControlPlane: cpOptions(t, cni.None)}); err != nil {
		t.Fatalf("the control plane renders without --profile: %v", err)
	}
}

func TestControlPlaneSkipsBluefin(t *testing.T) {
	host := &hardware.Host{OS: "bluefin", Role: hardware.RoleControlPlane}
	cfg, err := Fragment(host, Options{Profile: KubeadmWorker, ControlPlane: cpOptions(t, cni.Cilium)})
	if err != nil || len(cfg.Storage.Files)+len(cfg.Systemd.Units) != 0 {
		t.Fatalf("bluefin never runs kubeadm: %+v err=%v", cfg, err)
	}
}

// TestKubeadmInitYAMLValidates runs `kubeadm config validate` on the
// rendered config when a kubeadm binary is available (KUBEADM env or PATH).
func TestKubeadmInitYAMLValidates(t *testing.T) {
	bin := os.Getenv("KUBEADM")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("kubeadm"); err != nil {
			t.Skip("kubeadm not available; set KUBEADM=/path/to/kubeadm")
		}
	}
	y, err := KubeadmInitYAML("v1.34.3", cpOptions(t, cni.None))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kubeadm-init.yaml")
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "config", "validate", "--config", path).CombinedOutput()
	if err != nil {
		t.Fatalf("kubeadm config validate: %v\n%s\n%s", err, out, y)
	}
}
