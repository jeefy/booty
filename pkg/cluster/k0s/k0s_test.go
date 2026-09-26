package k0s

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/cluster/pki"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/cni"
)

const bootstrapToken = "abcdef.0123456789abcdef"

func testPKI(t *testing.T) *pki.PKI {
	t.Helper()
	p, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func controllerOptions(t *testing.T, os, cniName string) Options {
	t.Helper()
	p := testPKI(t)
	secret, err := token.K0sBootstrapSecret(Worker, bootstrapToken, time.Now().Add(token.TTL))
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Role: Controller, OS: os, Server: "192.168.1.10:8080", Endpoint: "10.77.0.40",
		PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", CNI: cniName,
		PKI: p.Files(), Secrets: secret,
	}
}

func workerOptions(t *testing.T, os string) Options {
	t.Helper()
	tok, err := token.EncodeK0s(Worker, "10.77.0.40", []byte(testPKI(t).CACert()), bootstrapToken)
	if err != nil {
		t.Fatal(err)
	}
	return Options{Role: Worker, OS: os, WorkerToken: tok}
}

func (n *Node) file(t *testing.T, path string) File {
	t.Helper()
	for _, f := range n.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("file %s missing in %v", path, n.paths())
	return File{}
}

func (n *Node) paths() []string {
	out := make([]string, 0, len(n.Files))
	for _, f := range n.Files {
		out = append(out, f.Path)
	}
	return out
}

func (n *Node) unitNames() []string {
	out := make([]string, 0, len(n.Units))
	for _, u := range n.Units {
		out = append(out, u.Name)
	}
	return out
}

func TestReleasePins(t *testing.T) {
	if DefaultVersion != "v1.36.4+k0s.0" {
		t.Fatalf("the default must match Bluefin Server's /usr/bin/k0s, got %s", DefaultVersion)
	}
	if len(DefaultSHA256) != 64 || PinnedSHA256(DefaultVersion) != DefaultSHA256 || PinnedSHA256("v1.36.5+k0s.0") != "" {
		t.Fatalf("pin: %s", DefaultSHA256)
	}
	if got := DownloadURL(DefaultVersion); got != "https://github.com/k0sproject/k0s/releases/download/v1.36.4%2Bk0s.0/k0s-v1.36.4%2Bk0s.0-amd64" {
		t.Fatalf("download URL: %s", got)
	}
	if got := ChecksumsURL(DefaultVersion); got != "https://github.com/k0sproject/k0s/releases/download/v1.36.4%2Bk0s.0/sha256sums.txt" {
		t.Fatalf("checksums URL: %s", got)
	}
	for v, ok := range map[string]bool{DefaultVersion: true, "v1.36.5+k0s.1": true, "1.36.4": false, "v1.36.4": false, "v1.36.4+k0s.0; rm": false, "": false} {
		if err := ValidateVersion(v); (err == nil) != ok {
			t.Errorf("ValidateVersion(%q)=%v", v, err)
		}
	}
}

func TestProvider(t *testing.T) {
	for name, want := range map[string]string{cni.Cilium: "custom", cni.Flannel: "custom", cni.Calico: "calico", cni.None: "kuberouter"} {
		if got := Provider(name); got != want {
			t.Errorf("Provider(%s)=%s want %s", name, got, want)
		}
		if Custom(name) != (want == "custom") {
			t.Errorf("Custom(%s)", name)
		}
	}
}

func TestConfigYAML(t *testing.T) {
	out, err := ConfigYAML("10.77.0.40", "10.244.0.0/16", "10.96.0.0/12", cni.Cilium)
	if err != nil {
		t.Fatal(err)
	}
	want := "apiVersion: k0s.k0sproject.io/v1beta1\nkind: ClusterConfig\nmetadata:\n    name: k0s\nspec:\n    api:\n        externalAddress: 10.77.0.40\n    network:\n        podCIDR: 10.244.0.0/16\n        serviceCIDR: 10.96.0.0/12\n        provider: custom\n"
	if string(out) != want {
		t.Fatalf("k0s.yaml:\n%s\nwant:\n%s", out, want)
	}
	out, err = ConfigYAML("cp.example.org:7443", "10.244.0.0/16", "10.96.0.0/12", cni.Calico)
	if err != nil || !strings.Contains(string(out), "externalAddress: cp.example.org\n        port: 7443\n") || !strings.Contains(string(out), "provider: calico") {
		t.Fatalf("endpoint port and calico: %v\n%s", err, out)
	}
	out, _ = ConfigYAML("10.77.0.40:6443", "10.244.0.0/16", "10.96.0.0/12", cni.None)
	if strings.Contains(string(out), "port:") || !strings.Contains(string(out), "provider: kuberouter") {
		t.Fatalf("6443 is the default and none is kuberouter:\n%s", out)
	}
	for _, bad := range [][3]string{{"https://x", "10.244.0.0/16", "10.96.0.0/12"}, {"", "10.244.0.0/16", "10.96.0.0/12"}, {"10.77.0.40", "", "10.96.0.0/12"}} {
		if _, err := ConfigYAML(bad[0], bad[1], bad[2], cni.None); err == nil {
			t.Errorf("ConfigYAML(%v) must fail", bad)
		}
	}
}

func TestRenderControllerPXE(t *testing.T) {
	for _, os := range []string{"flatcar", "coreos", ""} {
		n, err := Render(controllerOptions(t, os, cni.Cilium))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range PKIFiles {
			f := n.file(t, PKIDir+"/"+name)
			if f.Mode != 0o600 || !strings.Contains(f.Contents, "-----BEGIN") {
				t.Errorf("%s: %+v", name, f)
			}
		}
		if n.file(t, PKIDir+"/ca.key").Mode != 0o600 || !strings.Contains(n.file(t, PKIDir+"/etcd/ca.key").Contents, "PRIVATE KEY") {
			t.Fatal("key files")
		}
		cfg := n.file(t, ConfigPath)
		if cfg.Mode != 0o600 || !strings.Contains(cfg.Contents, "externalAddress: 10.77.0.40") || !strings.Contains(cfg.Contents, "provider: custom") {
			t.Fatalf("k0s.yaml: %+v", cfg)
		}
		if m := n.file(t, TokensManifest); !strings.Contains(m.Contents, "type: bootstrap.kubernetes.io/token") || m.Mode != 0o600 {
			t.Fatalf("tokens manifest: %+v", m)
		}
		ready := n.file(t, ClusterReadyScript)
		if ready.Mode != 0o755 || !strings.Contains(ready.Contents, "KUBECONFIG=/var/lib/k0s/pki/admin.conf /opt/bin/k0s kubectl get --raw /readyz") || !strings.Contains(ready.Contents, `http://192.168.1.10:8080/cluster/ready?mac=$MAC`) {
			t.Fatalf("ready script: %s", ready.Contents)
		}
		if apply := n.file(t, cni.ScriptPath); !strings.Contains(apply.Contents, `kubectl() { /opt/bin/k0s kubectl "$@"; }`) || !strings.Contains(apply.Contents, "touch "+PXEMarker) {
			t.Fatalf("cni script: %s", apply.Contents)
		}
		if got := strings.Join(n.unitNames(), ","); got != ControllerUnit+","+UnitClusterReady+","+cni.UnitName {
			t.Fatalf("%q units: %s", os, got)
		}
		if len(n.DropIns) != 0 {
			t.Fatalf("PXE hosts get whole units, not drop-ins: %+v", n.DropIns)
		}
		ctl := n.Units[0]
		for _, want := range []string{
			"ExecStart=/opt/bin/k0s controller -c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot\n",
			"Requires=" + InstallUnit + "\n", "After=network-online.target " + InstallUnit + "\n", "RequiresMountsFor=/var/lib/k0s\n",
			"Restart=always", "Delegate=yes", "KillMode=process", "WantedBy=multi-user.target",
		} {
			if !strings.Contains(ctl.Contents, want) {
				t.Errorf("controller unit missing %q:\n%s", want, ctl.Contents)
			}
		}
		if strings.Contains(ctl.Contents, "--single") || ctl.WantedBy != "multi-user.target" {
			t.Fatalf("controller unit: %+v", ctl)
		}
		if !strings.Contains(n.Units[1].Contents, "Wants="+ControllerUnit) || strings.Contains(n.Units[1].Contents, "Requires=") {
			t.Fatalf("ready unit only Wants= the controller:\n%s", n.Units[1].Contents)
		}
		if !strings.Contains(n.Units[2].Contents, "Wants="+ControllerUnit+"\n") || !strings.Contains(n.Units[2].Contents, "ConditionPathExists=!"+PXEMarker) {
			t.Fatalf("cni unit:\n%s", n.Units[2].Contents)
		}
	}
}

func TestRenderControllerBluefin(t *testing.T) {
	n, err := Render(controllerOptions(t, "bluefin", cni.Flannel))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(n.unitNames(), ","); got != UnitClusterReady+","+cni.UnitName {
		t.Fatalf("bluefin controller units: %s", got)
	}
	if len(n.DropIns) != 1 || n.DropIns[0].Unit != ControllerUnit || n.DropIns[0].Name != RoleDropIn {
		t.Fatalf("drop-ins: %+v", n.DropIns)
	}
	d := n.DropIns[0].Contents
	if strings.Count(d, "ExecStart=") != 2 || !strings.HasPrefix(d, "[Service]\nExecStart=\nExecStart=/usr/bin/k0s controller -c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot\n") || strings.Contains(d, "--single") {
		t.Fatalf("drop-in:\n%s", d)
	}
	if !strings.Contains(n.file(t, ClusterReadyScript).Contents, "/usr/bin/k0s kubectl") || strings.Contains(n.file(t, ClusterReadyScript).Contents, "/opt/bin") {
		t.Fatal("bluefin uses /usr/bin/k0s")
	}
	if apply := n.file(t, cni.ScriptPath); !strings.Contains(apply.Contents, `kubectl() { /usr/bin/k0s kubectl "$@"; }`) || !strings.Contains(apply.Contents, "touch "+BluefinMarker) {
		t.Fatalf("cni script: %s", apply.Contents)
	}
	if !strings.Contains(n.file(t, ConfigPath).Contents, "provider: custom") {
		t.Fatal("flannel is provider custom")
	}
}

func TestRenderControllerWithoutCustomCNI(t *testing.T) {
	for _, name := range []string{cni.Calico, cni.None} {
		n, err := Render(controllerOptions(t, "flatcar", name))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(n.unitNames(), ","); got != ControllerUnit+","+UnitClusterReady {
			t.Fatalf("%s: no CNI oneshot expected: %s", name, got)
		}
		for _, f := range n.Files {
			if f.Path == cni.ScriptPath {
				t.Fatalf("%s: no CNI script expected", name)
			}
		}
		if !strings.Contains(n.file(t, ConfigPath).Contents, "provider: "+Provider(name)) {
			t.Fatalf("%s: provider", name)
		}
	}
}

func TestRenderWorker(t *testing.T) {
	for _, os := range []string{"flatcar", "coreos", "bluefin"} {
		o := workerOptions(t, os)
		n, err := Render(o)
		if err != nil {
			t.Fatal(err)
		}
		if len(n.Files) != 1 || n.Files[0].Path != TokenPath || n.Files[0].Mode != 0o600 || n.Files[0].Contents != o.WorkerToken+"\n" {
			t.Fatalf("%s: worker files: %+v", os, n.paths())
		}
		join, err := token.ParseK0s(strings.TrimSpace(n.Files[0].Contents))
		if err != nil || join.Server != "https://10.77.0.40:6443" || join.Role != Worker || join.Token != bootstrapToken {
			t.Fatalf("%s: token decodes to %+v (%v)", os, join, err)
		}
		for _, f := range n.Files {
			if strings.Contains(f.Contents, "PRIVATE KEY") {
				t.Fatalf("%s: a worker never sees a private key", os)
			}
		}
		if os == "bluefin" {
			if len(n.Units) != 0 || len(n.DropIns) != 1 {
				t.Fatalf("bluefin worker: units %v dropins %+v", n.unitNames(), n.DropIns)
			}
			d := n.DropIns[0]
			if d.Unit != ControllerUnit || d.Name != RoleDropIn || d.Contents != "[Service]\nExecStart=\nExecStart=/usr/bin/k0s worker --token-file /etc/k0s/token\n" {
				t.Fatalf("bluefin worker drop-in: %+v", d)
			}
			continue
		}
		if len(n.Units) != 1 || n.Units[0].Name != WorkerUnit || len(n.DropIns) != 0 {
			t.Fatalf("%s worker: units %v dropins %+v", os, n.unitNames(), n.DropIns)
		}
		if u := n.Units[0].Contents; !strings.Contains(u, "ExecStart=/opt/bin/k0s worker --token-file /etc/k0s/token\n") || !strings.Contains(u, "Requires="+InstallUnit) || !strings.Contains(u, "RequiresMountsFor=/var/lib/k0s") || !strings.Contains(u, "Restart=always") {
			t.Fatalf("%s worker unit:\n%s", os, u)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	good := controllerOptions(t, "flatcar", cni.None)
	cases := map[string]func(o *Options){
		"os":            func(o *Options) { o.OS = "windows" },
		"role":          func(o *Options) { o.Role = "agent" },
		"endpoint":      func(o *Options) { o.Endpoint = "" },
		"server":        func(o *Options) { o.Server = "" },
		"quotes":        func(o *Options) { o.Server = `x"y` },
		"secrets":       func(o *Options) { o.Secrets = nil },
		"pki":           func(o *Options) { delete(o.PKI, pki.SAKeyFile) },
		"podCIDR":       func(o *Options) { o.PodCIDR = "" },
		"worker token":  func(o *Options) { o.Role = Worker; o.WorkerToken = "not-a-token" },
		"controller tk": func(o *Options) { o.Role = Worker; o.WorkerToken = controllerToken(t) },
	}
	for name, mutate := range cases {
		o := good
		o.PKI = map[string][]byte{}
		for k, v := range good.PKI {
			o.PKI[k] = v
		}
		mutate(&o)
		if _, err := Render(o); err == nil {
			t.Errorf("%s: Render must fail", name)
		}
	}
	if _, err := Render(good); err != nil {
		t.Fatal(err)
	}
}

// controllerToken is a valid k0s token of the wrong flavour: a worker must
// not accept it, since `k0s worker` would talk to :9443 with it.
func controllerToken(t *testing.T) string {
	t.Helper()
	tok, err := token.EncodeK0s(Controller, "10.77.0.40", []byte(testPKI(t).CACert()), bootstrapToken)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestConfigValidatesWithK0s runs `k0s config validate` from the pinned
// k0s image on every provider Booty renders. It needs podman and the
// network (or a cached image) and is skipped otherwise.
func TestConfigValidatesWithK0s(t *testing.T) {
	podman, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("podman not available")
	}
	image := "docker.io/k0sproject/k0s:" + strings.ReplaceAll(DefaultVersion, "+", "-")
	if out, err := exec.Command(podman, "image", "exists", image).CombinedOutput(); err != nil {
		if os.Getenv("K0S_PULL") == "" {
			t.Skipf("image %s not cached (set K0S_PULL=1 to pull): %s", image, out)
		}
		if out, err := exec.Command(podman, "pull", image).CombinedOutput(); err != nil {
			t.Skipf("podman pull %s: %v\n%s", image, err, out)
		}
	}
	dir := t.TempDir()
	for _, cniName := range []string{cni.Cilium, cni.Calico, cni.Flannel, cni.None} {
		for _, endpoint := range []string{"10.77.0.40", "cp.example.org:7443"} {
			cfg, err := ConfigYAML(endpoint, "10.244.0.0/16", "10.96.0.0/12", cniName)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, cniName+"-k0s.yaml")
			if err := os.WriteFile(path, cfg, 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(podman, "run", "--rm", "-v", path+":/k0s.yaml:ro,Z", image, "k0s", "config", "validate", "-c", "/k0s.yaml").CombinedOutput()
			if err != nil {
				t.Errorf("%s %s: k0s config validate: %v\n%s\n%s", cniName, endpoint, err, out, cfg)
			}
		}
	}
}
