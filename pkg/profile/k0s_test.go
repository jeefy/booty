package profile

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v3_4 "github.com/coreos/ignition/v2/config/v3_4"
	"github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/cluster/pki"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/cni"
	"github.com/jeefy/booty/pkg/hardware"
)

const k0sTestToken = "abcdef.0123456789abcdef"

func k0sNode(t *testing.T, role k0s.Role, os, cniName string) *k0s.Node {
	t.Helper()
	p, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"))
	if err != nil {
		t.Fatal(err)
	}
	o := k0s.Options{Role: role, OS: os, Server: "192.168.1.10:8080", Endpoint: "10.77.0.40", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", CNI: cniName}
	if role == k0s.Controller {
		o.PKI = p.Files()
		if o.Secrets, err = token.K0sBootstrapSecret(k0s.Worker, k0sTestToken, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	} else if o.WorkerToken, err = token.EncodeK0s(k0s.Worker, "10.77.0.40", p.CACert(), k0sTestToken); err != nil {
		t.Fatal(err)
	}
	n, err := k0s.Render(o)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func k0sRender(t *testing.T, host *hardware.Host, opts Options) (types.Config, string) {
	t.Helper()
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

func k0sOptions(t *testing.T, role k0s.Role, os, cniName, containerdDisk string) Options {
	t.Helper()
	return Options{ContainerdDisk: containerdDisk, K0s: &K0sOptions{Node: k0sNode(t, role, os, cniName), Version: k0s.DefaultVersion, SHA256: k0s.DefaultSHA256, ControlPlaneDisk: "/dev/vdb"}}
}

func TestK0sControllerFlatcar(t *testing.T) {
	host := &hardware.Host{MAC: "52:54:00:aa:00:44", Hostname: "flatcar-cp", OS: "flatcar", Role: hardware.RoleControlPlane}
	cfg, raw := k0sRender(t, host, k0sOptions(t, k0s.Controller, "flatcar", cni.Cilium, "/dev/vda"))
	_, names := unitsByName(cfg)
	want := []string{ControlPlaneMountUnit, UnitSeed, K0sDataMountUnit, K0sContainerdMountUnit, k0s.InstallUnit, k0s.ControllerUnit, k0s.UnitClusterReady, cni.UnitName}
	if got := strings.Join(names, ","); got != strings.Join(want, ",") {
		t.Fatalf("units:\n got %s\nwant %s", got, strings.Join(want, ","))
	}
	for _, kubeadmUnit := range []string{UnitInit, UnitJoin, UnitKubeTools, UnitKubeletSetup, UnitCNIInstall, UnitContainerd, "etc-kubernetes.mount", "kubelet.service"} {
		if strings.Contains(raw, kubeadmUnit) {
			t.Fatalf("no kubeadm pieces under k0s: %s", kubeadmUnit)
		}
	}
	if len(cfg.Storage.Filesystems) != 2 {
		t.Fatalf("filesystems: %+v", cfg.Storage.Filesystems)
	}
	cp, containerd := cfg.Storage.Filesystems[0], cfg.Storage.Filesystems[1]
	if cp.Device != "/dev/vdb" || *cp.Label != ControlPlaneDiskLabel || *cp.Path != ControlPlaneMount || *cp.WipeFilesystem {
		t.Fatalf("control-plane filesystem: %+v", cp)
	}
	if containerd.Device != "/dev/vda" || *containerd.Label != ContainerdDiskLabel || *containerd.Path != k0s.ContainerdDir || !*containerd.WipeFilesystem {
		t.Fatalf("containerd filesystem: %+v", containerd)
	}
	byName, _ := unitsByName(cfg)
	if m := *byName[K0sDataMountUnit].Contents; !strings.Contains(m, "What="+ControlPlaneMount+"/k0s\n") || !strings.Contains(m, "Where=/var/lib/k0s\n") || !strings.Contains(m, "Options=bind") || !strings.Contains(m, "Requires="+UnitSeed) {
		t.Fatalf("bind mount:\n%s", m)
	}
	if m := *byName[K0sContainerdMountUnit].Contents; !strings.Contains(m, "Where=/var/lib/k0s/containerd\n") || !strings.Contains(m, "RequiresMountsFor=/var/lib/k0s\n") || !strings.Contains(m, "What=/dev/disk/by-label/ssd") {
		t.Fatalf("containerd mount:\n%s", m)
	}
	if u := *byName[UnitSeed].Contents; !strings.Contains(u, "ExecStart="+K0sSeedScript) || !strings.Contains(u, "DefaultDependencies=no") {
		t.Fatalf("seed unit:\n%s", u)
	}
	seed := decodeFile(t, cfg, K0sSeedScript)
	for _, want := range []string{"cp -an /var/lib/k0s/. /var/lib/booty-cp/k0s/", "cp -a /var/lib/k0s/manifests/booty/. /var/lib/booty-cp/k0s/manifests/booty/", "chmod 0600 /var/lib/booty-cp/k0s/pki/*.key"} {
		if !strings.Contains(seed, want) {
			t.Errorf("seed script missing %q:\n%s", want, seed)
		}
	}
	if u := byName[k0s.ControllerUnit]; !strings.Contains(*u.Contents, "ExecStart=/opt/bin/k0s controller -c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot") || u.Enabled == nil || !*u.Enabled {
		t.Fatalf("controller unit: %+v", u)
	}
	for _, path := range []string{k0s.PKIDir + "/ca.crt", k0s.PKIDir + "/ca.key", k0s.PKIDir + "/sa.key", k0s.PKIDir + "/sa.pub", k0s.PKIDir + "/etcd/ca.crt", k0s.PKIDir + "/etcd/ca.key", k0s.ConfigPath, k0s.TokensManifest} {
		if fileMode(t, cfg, path) != 0o600 {
			t.Errorf("%s must be 0600", path)
		}
	}
	for _, path := range []string{K0sInstallScript, K0sSeedScript, k0s.ClusterReadyScript, cni.ScriptPath} {
		if fileMode(t, cfg, path) != 0o755 {
			t.Errorf("%s must be 0755", path)
		}
	}
	if !strings.Contains(decodeAnyFile(t, cfg, k0s.PKIDir+"/ca.key"), "PRIVATE KEY") {
		t.Fatal("controller carries the CA key")
	}
	install := decodeFile(t, cfg, K0sInstallScript)
	for _, want := range []string{`K0S_VERSION="v1.36.4+k0s.0"`, `K0S_SHA256="` + k0s.DefaultSHA256 + `"`, `URL="https://github.com/k0sproject/k0s/releases/download/v1.36.4%2Bk0s.0/k0s-v1.36.4%2Bk0s.0-amd64"`, "sha256sum -c -", "mv -f", "already installed"} {
		if !strings.Contains(install, want) {
			t.Errorf("install script missing %q:\n%s", want, install)
		}
	}
	if u := *byName[k0s.InstallUnit].Contents; !strings.Contains(u, "Wants=network-online.target") || !strings.Contains(u, "Restart=on-failure") {
		t.Fatalf("install unit:\n%s", u)
	}
}

func TestK0sControllerNeedsDiskAndDistinctDevices(t *testing.T) {
	host := &hardware.Host{MAC: "52:54:00:aa:00:44", OS: "flatcar", Role: hardware.RoleControlPlane}
	opts := k0sOptions(t, k0s.Controller, "flatcar", cni.None, "/dev/vda")
	opts.K0s.ControlPlaneDisk = ""
	if _, err := Fragment(host, opts); err == nil || !strings.Contains(err.Error(), "ControlPlaneDisk") {
		t.Fatalf("controller without a disk: %v", err)
	}
	opts.K0s.ControlPlaneDisk = "/dev/vda"
	if _, err := Fragment(host, opts); err == nil || !strings.Contains(err.Error(), "also the containerd disk") {
		t.Fatalf("same device twice: %v", err)
	}
	opts.K0s.ControlPlaneDisk, opts.ContainerdDisk = "/dev/vdb", ""
	cfg, _ := k0sRender(t, host, opts)
	_, names := unitsByName(cfg)
	if got := strings.Join(names, ","); strings.Contains(got, K0sContainerdMountUnit) || len(cfg.Storage.Filesystems) != 1 || !strings.Contains(got, k0s.ControllerUnit) {
		t.Fatalf("without --containerdDisk only the control-plane disk: %s %+v", got, cfg.Storage.Filesystems)
	}
	if got := strings.Join(names, ","); strings.Contains(got, cni.UnitName) {
		t.Fatalf("--cni=none installs nothing: %s", got)
	}
	opts.K0s.Version = "1.36"
	if _, err := Fragment(host, opts); err == nil {
		t.Fatal("bad version must fail")
	}
	opts.K0s.Version, opts.K0s.SHA256 = k0s.DefaultVersion, "xyz"
	if _, err := Fragment(host, opts); err == nil {
		t.Fatal("bad sha must fail")
	}
	opts.K0s.SHA256 = ""
	cfg, _ = k0sRender(t, host, opts)
	if install := decodeFile(t, cfg, K0sInstallScript); !strings.Contains(install, `K0S_SHA256=""`) || !strings.Contains(install, "sha256sums.txt") {
		t.Fatalf("unpinned version verifies against sha256sums.txt:\n%s", install)
	}
	opts.Profile = KubeadmWorker
	if _, err := Fragment(host, opts); err == nil {
		t.Fatal("a profile and a k0s node are mutually exclusive")
	}
}

func TestK0sWorkers(t *testing.T) {
	for _, os := range []string{"flatcar", "coreos"} {
		host := &hardware.Host{MAC: "52:54:00:aa:00:41", Hostname: "w", OS: os}
		cfg, raw := k0sRender(t, host, k0sOptions(t, k0s.Worker, os, cni.Cilium, "/dev/vda"))
		_, names := unitsByName(cfg)
		if got := strings.Join(names, ","); got != K0sDataMountUnit+","+k0s.InstallUnit+","+k0s.WorkerUnit {
			t.Fatalf("%s worker units: %s", os, got)
		}
		if len(cfg.Storage.Filesystems) != 1 || cfg.Storage.Filesystems[0].Device != "/dev/vda" || *cfg.Storage.Filesystems[0].Path != k0s.DataDir || !*cfg.Storage.Filesystems[0].WipeFilesystem {
			t.Fatalf("%s worker filesystems: %+v", os, cfg.Storage.Filesystems)
		}
		byName, _ := unitsByName(cfg)
		if m := *byName[K0sDataMountUnit].Contents; !strings.Contains(m, "Where=/var/lib/k0s\n") || !strings.Contains(m, "What=/dev/disk/by-label/ssd") {
			t.Fatalf("%s worker mount:\n%s", os, m)
		}
		if u := *byName[k0s.WorkerUnit].Contents; !strings.Contains(u, "ExecStart=/opt/bin/k0s worker --token-file /etc/k0s/token\n") || !strings.Contains(u, "RequiresMountsFor=/var/lib/k0s") {
			t.Fatalf("%s worker unit:\n%s", os, u)
		}
		if fileMode(t, cfg, k0s.TokenPath) != 0o600 {
			t.Fatal("token mode")
		}
		join, err := token.ParseK0s(strings.TrimSpace(decodeAnyFile(t, cfg, k0s.TokenPath)))
		if err != nil || join.Server != "https://10.77.0.40:6443" || join.Role != k0s.Worker {
			t.Fatalf("%s token: %+v %v", os, join, err)
		}
		if strings.Contains(raw, "PRIVATE KEY") || strings.Contains(raw, "booty-cp") || strings.Contains(raw, "k0s.yaml") || strings.Contains(raw, "kubeadm") {
			t.Fatalf("%s worker must carry neither keys nor controller pieces", os)
		}
		for _, f := range cfg.Storage.Files {
			if strings.Contains(decodeAnyFile(t, cfg, f.Path), "PRIVATE KEY") {
				t.Fatalf("%s: %s carries a private key", os, f.Path)
			}
		}

		cfg, _ = k0sRender(t, host, k0sOptions(t, k0s.Worker, os, cni.Cilium, ""))
		_, names = unitsByName(cfg)
		if got := strings.Join(names, ","); got != k0s.InstallUnit+","+k0s.WorkerUnit || len(cfg.Storage.Filesystems) != 0 {
			t.Fatalf("%s worker without --containerdDisk: %s", os, got)
		}
	}
}

func TestK0sFragmentSkipsBluefinAndNil(t *testing.T) {
	opts := k0sOptions(t, k0s.Worker, "bluefin", cni.None, "")
	cfg, err := Fragment(&hardware.Host{MAC: "x", OS: "bluefin"}, opts)
	if err != nil || len(cfg.Systemd.Units) != 0 || len(cfg.Storage.Files) != 0 {
		t.Fatalf("bluefin is provisioned through creds, not Ignition: %v %+v", err, cfg)
	}
	cfg, err = Fragment(&hardware.Host{MAC: "x", OS: "flatcar"}, Options{})
	if err != nil || len(cfg.Systemd.Units) != 0 {
		t.Fatalf("no profile, no k0s: %v", err)
	}
	if _, err := Fragment(&hardware.Host{MAC: "x", OS: "flatcar"}, Options{K0s: &K0sOptions{}}); err == nil {
		t.Fatal("k0s options without a node must fail")
	}
}

// TestK0sRenderValidates runs ignition-validate on the k0s fragments when
// it is installed.
func TestK0sRenderValidates(t *testing.T) {
	ignitionValidate, err := exec.LookPath("ignition-validate")
	if err != nil {
		t.Skip("ignition-validate not available")
	}
	dir := t.TempDir()
	for name, render := range map[string]func() (types.Config, string){
		"controller": func() (types.Config, string) {
			return k0sRender(t, &hardware.Host{MAC: "a", OS: "flatcar", Role: hardware.RoleControlPlane}, k0sOptions(t, k0s.Controller, "flatcar", cni.Cilium, "/dev/vda"))
		},
		"worker": func() (types.Config, string) {
			return k0sRender(t, &hardware.Host{MAC: "b", OS: "coreos"}, k0sOptions(t, k0s.Worker, "coreos", cni.Cilium, "/dev/vda"))
		},
	} {
		_, raw := render()
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(ignitionValidate, path).CombinedOutput(); err != nil {
			t.Errorf("ignition-validate %s: %v\n%s", name, err, out)
		}
	}
}
