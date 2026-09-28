package profile

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func writeExecutable(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o755)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSystemdEscapePath(t *testing.T) {
	for in, want := range map[string]string{
		"/var/lib/containerd-disk": `var-lib-containerd\x2ddisk`,
		"/var/lib/containerd":      "var-lib-containerd",
		"/":                        "-",
		"/a//b/":                   "a-b",
		"/srv/.hidden":             `srv-\x2ehidden`,
		"/mnt/a b":                 `mnt-a\x20b`,
	} {
		if got := SystemdEscapePath(in); got != want {
			t.Errorf("SystemdEscapePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func bluefinUnits(w BluefinWorker) (map[string]BluefinUnit, []string) {
	units := map[string]BluefinUnit{}
	var names []string
	for _, u := range w.Units {
		units[u.Name] = u
		names = append(names, u.Name)
	}
	return units, names
}

func TestBluefinKubeadmWorkerContainerdDisk(t *testing.T) {
	w := BluefinKubeadmWorker(BluefinWorkerOptions{Hostname: "aren", JoinString: "kubeadm join 10.0.0.1:6443 --token t.s --discovery-token-ca-cert-hash sha256:abc", ContainerdDisk: "/dev/sda"})
	units, names := bluefinUnits(w)
	diskMount := `var-lib-containerd\x2ddisk.mount`
	want := []string{diskMount, BluefinContainerdDirUnit, ContainerdMountUnit, "containerd.service", "kubelet.service", BluefinUnitJoin}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("units %v, want %v", names, want)
	}

	disk := units[diskMount]
	for _, s := range []string{"What=/dev/sda\n", "Where=/var/lib/containerd-disk\n", "Type=ext4\n", "Options=defaults,nofail\n"} {
		if !strings.Contains(disk.Contents, s) {
			t.Errorf("disk mount missing %q:\n%s", s, disk.Contents)
		}
	}
	if disk.Enabled || strings.Contains(disk.Contents, "[Install]") {
		t.Errorf("the disk is only pulled in by containerd, never by local-fs.target:\n%s", disk.Contents)
	}

	dir := units[BluefinContainerdDirUnit]
	for _, s := range []string{"DefaultDependencies=no\n", "Requires=" + diskMount + "\n", "After=" + diskMount + "\n", "AssertPathIsMountPoint=/var/lib/containerd-disk\n", "[ -L /var/lib/containerd-disk/bluefin ]", "mkdir -p -m 0711 /var/lib/containerd-disk/bluefin'"} {
		if !strings.Contains(dir.Contents, s) {
			t.Errorf("dir unit missing %q:\n%s", s, dir.Contents)
		}
	}

	bind := units[ContainerdMountUnit]
	for _, s := range []string{"What=/var/lib/containerd-disk/bluefin\n", "Where=/var/lib/containerd\n", "Options=bind,nofail\n", "Requires=" + BluefinContainerdDirUnit + "\n", "After=" + BluefinContainerdDirUnit + "\n"} {
		if !strings.Contains(bind.Contents, s) {
			t.Errorf("bind mount missing %q:\n%s", s, bind.Contents)
		}
	}

	for _, u := range w.Units {
		for _, bad := range []string{"mkfs", "wipefs", "rm -", "/opt/bin"} {
			if strings.Contains(u.Contents, bad) {
				t.Errorf("%s must not contain %q:\n%s", u.Name, bad, u.Contents)
			}
		}
	}

	ctd := units["containerd.service"]
	if !ctd.Enabled || ctd.Contents != "" || len(ctd.Dropins) != 1 || ctd.Dropins[0].Name != BluefinContainerdDropin ||
		ctd.Dropins[0].Contents != "[Unit]\nRequires=var-lib-containerd.mount\nAfter=var-lib-containerd.mount\n" {
		t.Fatalf("containerd.service is the sysext's, enabled and ordered after the bind mount: %+v", ctd)
	}
	if k := units["kubelet.service"]; !k.Enabled || k.Contents != "" || len(k.Dropins) != 0 {
		t.Fatalf("kubelet.service is the sysext's, only enabled: %+v", k)
	}
}

func TestBluefinKubeadmWorkerWithoutContainerdDisk(t *testing.T) {
	w := BluefinKubeadmWorker(BluefinWorkerOptions{Hostname: "aren", JoinString: "kubeadm join x"})
	units, names := bluefinUnits(w)
	if strings.Join(names, ",") != "containerd.service,kubelet.service,"+BluefinUnitJoin {
		t.Fatalf("no disk, no mounts: %v", names)
	}
	if len(units["containerd.service"].Dropins) != 0 {
		t.Fatalf("no disk, no drop-in: %+v", units["containerd.service"])
	}
}

func TestBluefinKubeadmWorkerJoin(t *testing.T) {
	w := BluefinKubeadmWorker(BluefinWorkerOptions{Hostname: "aren", JoinString: "kubeadm join 10.0.0.1:6443 --token t.s  --discovery-token-ca-cert-hash sha256:abc"})
	files := map[string]BluefinFile{}
	for _, f := range w.Files {
		files[f.Path] = f
		if strings.HasPrefix(f.Path, "/opt") || strings.HasPrefix(f.Path, "/usr") {
			t.Errorf("/opt stays for the CNI and /usr is read-only: %s", f.Path)
		}
	}
	if f := files[KubeletDefaults]; f.Mode != 0o644 || f.Contents != "KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false\n" {
		t.Fatalf("kubelet extra args mirror the Flatcar profile: %+v", f)
	}
	script := files[BluefinJoinScript]
	if script.Mode != 0o755 {
		t.Fatalf("join script: %+v", script)
	}
	units, _ := bluefinUnits(w)
	join := units[BluefinUnitJoin]
	if !join.Enabled {
		t.Fatal("join unit must be enabled")
	}
	for _, s := range []string{
		"Wants=network-online.target containerd.service\n",
		"After=network-online.target systemd-sysext.service containerd.service\n",
		"Restart=on-failure\n", "RestartSec=15s\n", "RestartSteps=4\n", "RestartMaxDelaySec=2min\n",
		"RestartPreventExitStatus=3\n", "StartLimitIntervalSec=0\n",
		`Environment="JOIN_STRING=kubeadm join 10.0.0.1:6443 --token t.s --discovery-token-ca-cert-hash sha256:abc"` + "\n",
		`Environment="NODE_NAME=aren"` + "\n",
		`Environment="CRI_SOCKET=unix:///run/containerd/containerd.sock"` + "\n",
		`Environment="MAX_ATTEMPTS=30"` + "\n",
		"ExecStart=/usr/bin/bash /etc/booty/kubeadm-join.sh\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(join.Contents, s) {
			t.Errorf("join unit missing %q:\n%s", s, join.Contents)
		}
	}
	if !strings.Contains(script.Contents, "\n  exit 3\n") || BluefinJoinGiveUp != 3 {
		t.Fatal("the give-up exit status must match RestartPreventExitStatus")
	}
	for _, s := range []string{
		"export PATH=/usr/bin:/usr/sbin\n",
		"if [ -f /etc/kubernetes/kubelet.conf ]; then",
		`if [ -z "${JOIN_STRING:-}" ]; then`,
		`if [ -z "$(systemctl show -P FragmentPath containerd.service)" ]; then systemctl daemon-reload; fi`,
		"systemctl enable --now containerd.service kubelet.service || exit 1\n",
		"modprobe br_netfilter\n",
		`kubeadm reset -f --cri-socket "${CRI_SOCKET}" || true`,
		`args+=(--cri-socket "${CRI_SOCKET}")`,
		`args+=(--node-name "${NODE_NAME}")`,
		`${JOIN_STRING} "${args[@]}" 2>&1 | tee /run/booty/kubeadm-join.log`,
		`grep -q "already exists in the cluster" /run/booty/kubeadm-join.log`,
	} {
		if !strings.Contains(script.Contents, s) {
			t.Errorf("join script missing %q:\n%s", s, script.Contents)
		}
	}
	if strings.Contains(script.Contents, "/opt") {
		t.Errorf("Bluefin's tools are in /usr/bin:\n%s", script.Contents)
	}
	if strings.Index(script.Contents, "kubelet.conf") > strings.Index(script.Contents, "kubeadm reset") {
		t.Fatal("an already joined node must be skipped before kubeadm reset")
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(script.Contents)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, out)
		}
	}
}

// runBluefinJoin runs the join script with its absolute paths moved into a
// temp dir and fake systemctl/modprobe/sysctl/kubeadm on PATH. The fake
// kubeadm join fails like kubeadm does while the Node is still Ready when
// root/node-ready exists, and records its arguments otherwise.
func runBluefinJoin(t *testing.T, root, joinString string) (int, string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	bin := root + "/bin"
	fakes := map[string]string{
		"systemctl": "#!/bin/sh\necho \"systemctl $*\" >> " + root + "/calls\n",
		"modprobe":  "#!/bin/sh\n",
		"sysctl":    "#!/bin/sh\n",
		"kubeadm": "#!/bin/sh\ncase \"$1\" in\n  reset) echo reset >> " + root + "/calls; exit 0;;\n  join)\n" +
			"    if [ -e " + root + "/node-ready ]; then echo 'error execution phase kubelet-start: a Node with name \"aren\" and status \"Ready\" already exists in the cluster. You must delete the existing Node or change the name of this new joining Node'; exit 1; fi\n" +
			"    echo \"$*\" > " + root + "/joined; exit 0;;\nesac\n",
	}
	for _, d := range []string{bin, root + "/etc/kubernetes"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range fakes {
		if err := writeExecutable(bin+"/"+name, body); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer(
		"export PATH=/usr/bin:/usr/sbin", "export PATH="+bin+":/usr/bin:/bin",
		"/run/booty", root+"/run",
		"/etc/kubernetes", root+"/etc/kubernetes",
		"/usr/bin/kubeadm", bin+"/kubeadm",
	).Replace(bluefinJoinScript)
	cmd := exec.Command(bash, "-c", script)
	cmd.Env = []string{"JOIN_STRING=" + joinString, "NODE_NAME=aren", "CRI_SOCKET=" + BluefinCRISocket, "MAX_ATTEMPTS=3", "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), string(out)
	} else if err != nil {
		t.Fatal(err)
	}
	return 0, string(out)
}

func TestBluefinJoinScriptRetriesWhileTheOldNodeIsReady(t *testing.T) {
	root := t.TempDir()
	join := "kubeadm join 10.0.0.1:6443 --token t.s --discovery-token-ca-cert-hash sha256:abc"
	if err := writeExecutable(root+"/node-ready", ""); err != nil {
		t.Fatal(err)
	}
	code, out := runBluefinJoin(t, root, join)
	if code != 1 || !strings.Contains(out, "attempt 1: Node aren is still Ready from its previous boot") {
		t.Fatalf("a Ready Node of the same name fails the attempt for systemd to retry: %d\n%s", code, out)
	}
	if err := os.Remove(root + "/node-ready"); err != nil {
		t.Fatal(err)
	}
	code, out = runBluefinJoin(t, root, join)
	if code != 0 || !strings.Contains(out, "joined the cluster as aren on attempt 2") {
		t.Fatalf("the retry joins: %d\n%s", code, out)
	}
	joined := readFile(t, root+"/joined")
	if joined != "join 10.0.0.1:6443 --token t.s --discovery-token-ca-cert-hash sha256:abc --cri-socket unix:///run/containerd/containerd.sock --node-name aren\n" {
		t.Fatalf("join arguments: %q", joined)
	}
	if calls := readFile(t, root+"/calls"); !strings.Contains(calls, "systemctl daemon-reload\nsystemctl enable --now containerd.service kubelet.service\n") || strings.Index(calls, "systemctl enable") > strings.Index(calls, "reset") {
		t.Fatalf("containerd and the kubelet are started before kubeadm runs: %q", calls)
	}

	if err := writeExecutable(root+"/node-ready", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		runBluefinJoin(t, root, join)
	}
	if code, out := runBluefinJoin(t, root, join); code != 3 || !strings.Contains(out, "giving up after 3 failed kubeadm join attempts") {
		t.Fatalf("attempts are bounded by MAX_ATTEMPTS: %d\n%s", code, out)
	}
}

func TestBluefinJoinScriptNoOps(t *testing.T) {
	root := t.TempDir()
	if code, out := runBluefinJoin(t, root, ""); code != 0 || !strings.Contains(out, "JOIN_STRING is empty") {
		t.Fatalf("an empty join string boots without joining: %d\n%s", code, out)
	}
	if err := writeExecutable(root+"/etc/kubernetes/kubelet.conf", ""); err != nil {
		t.Fatal(err)
	}
	if code, out := runBluefinJoin(t, root, "kubeadm join x"); code != 0 || !strings.Contains(out, "already joined") {
		t.Fatalf("a joined node is left alone: %d\n%s", code, out)
	}
}
