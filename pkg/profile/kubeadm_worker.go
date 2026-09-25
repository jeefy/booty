// Package profile provides versioned, opinionated Ignition fragments for
// whole node roles. A profile is appended to Booty's builtin fragment, after
// the builtin pieces and before the user's config, so the user still wins.
package profile

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	ign "github.com/jeefy/booty/pkg/ignition"
)

const (
	// KubeadmWorker installs CNI plugins, kubeadm/kubelet/kubectl/crictl,
	// the kubelet systemd units and joins the cluster on every boot.
	KubeadmWorker = "kubeadm-worker"

	ScriptDir       = "/opt/booty"
	CNIScript       = ScriptDir + "/cni.sh"
	KubeToolsScript = ScriptDir + "/kube-tools.sh"
	SystemdScript   = ScriptDir + "/systemd.sh"
	JoinScript      = ScriptDir + "/join.sh"

	UnitCNIInstall   = "booty-cni-install.service"
	UnitKubeTools    = "booty-kube-tools.service"
	UnitKubeletSetup = "booty-kubelet-setup.service"
	UnitJoin         = "booty-k8s-join.service"

	ContainerdMountUnit = "var-lib-containerd.mount"
	ContainerdDiskLabel = "ssd"
	ContainerdPath      = "/var/lib/containerd"
)

// Options is everything the kubeadm-worker fragment depends on.
type Options struct {
	Profile         string
	K8sVersion      string
	CNIVersion      string
	CrictlVersion   string
	ContainerdDisk  string
	KubeletUnitsURL string
	JoinString      string
}

// Validate rejects unknown --profile values.
func Validate(name string) error {
	switch name {
	case "", KubeadmWorker:
		return nil
	}
	return fmt.Errorf("invalid --%s %q: must be empty or %q", config.Profile, name, KubeadmWorker)
}

// AppliesTo reports whether a profile targets hosts running os. Only the
// container-Linux flavours (Flatcar, Fedora CoreOS) are kubeadm workers;
// Bluefin Server ships k0s as a sysext and is provisioned through systemd
// credentials, not Ignition. An empty os is Booty's default (Flatcar).
func AppliesTo(os string) bool {
	switch os {
	case "", "flatcar", "coreos":
		return true
	}
	return false
}

// Fragment builds the profile's Ignition config for host. It is always a
// valid spec 3.4.0 config; it is empty when no profile is selected or the
// host's OS is out of scope.
func Fragment(host *hardware.Host, opts Options) (types.Config, error) {
	cfg := types.Config{}
	cfg.Ignition.Version = types.MaxVersion.String()
	if err := Validate(opts.Profile); err != nil {
		return cfg, err
	}
	if opts.Profile == "" || host == nil {
		return cfg, nil
	}
	if !AppliesTo(host.OS) {
		slog.Debug("Profile skipped: host OS out of scope", "profile", opts.Profile, "mac", host.MAC, "os", host.OS)
		return cfg, nil
	}
	opts = withDefaults(opts)

	cfg.Storage.Files = []types.File{
		ign.InlineFile(CNIScript, cniScript(opts), 0o755),
		ign.InlineFile(KubeToolsScript, kubeToolsScript(opts), 0o755),
		ign.InlineFile(SystemdScript, systemdScript(opts), 0o755),
		ign.InlineFile(JoinScript, joinScript, 0o755),
	}
	if opts.ContainerdDisk != "" {
		cfg.Storage.Filesystems = []types.Filesystem{containerdFilesystem(opts.ContainerdDisk)}
		cfg.Systemd.Units = append(cfg.Systemd.Units,
			ign.Unit(ContainerdMountUnit, true, containerdMount),
			containerdDropin(),
		)
	}
	cfg.Systemd.Units = append(cfg.Systemd.Units,
		ign.Unit(UnitCNIInstall, true, oneshot("Install CNI plugins "+opts.CNIVersion, "network-online.target", CNIScript, "Wants=network-online.target\n")),
		ign.Unit(UnitKubeTools, true, oneshot("Install kubeadm, kubelet, kubectl and crictl "+opts.K8sVersion, UnitCNIInstall, KubeToolsScript, "")),
		ign.Unit(UnitKubeletSetup, true, oneshot("Install the kubelet systemd units", UnitKubeTools, SystemdScript, "")),
		ign.Unit(UnitJoin, true, joinUnit(opts.JoinString)),
	)
	return cfg, nil
}

func withDefaults(o Options) Options {
	if o.K8sVersion == "" {
		o.K8sVersion = config.DefaultK8sVersion
	}
	if o.CNIVersion == "" {
		o.CNIVersion = config.DefaultCNIVersion
	}
	if o.CrictlVersion == "" {
		o.CrictlVersion = crictlVersionFor(o.K8sVersion)
	}
	if o.KubeletUnitsURL == "" {
		o.KubeletUnitsURL = config.DefaultKubeletUnitsURL
	}
	o.KubeletUnitsURL = strings.TrimRight(o.KubeletUnitsURL, "/")
	return o
}

func oneshot(description, after, exec, extra string) string {
	return "[Unit]\nDescription=" + description + "\n" +
		"Requires=" + after + "\nAfter=" + after + "\n" + extra +
		"\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=" + exec + "\n" +
		"\n[Install]\nWantedBy=multi-user.target\n"
}

// joinUnit passes the join string through the environment; systemd expands
// $VAR in ExecStart itself, hence $$PATH for the shell.
func joinUnit(joinString string) string {
	return `[Unit]
Description=Join the Kubernetes cluster with kubeadm
Requires=` + UnitKubeletSetup + `
After=` + UnitKubeletSetup + `

[Service]
Type=oneshot
RemainAfterExit=yes
Environment="JOIN_STRING=` + systemdQuote(joinString) + `"
ExecStart=/bin/bash -c 'PATH=/opt/bin:$$PATH exec ` + JoinScript + `'

[Install]
WantedBy=multi-user.target
`
}

func systemdQuote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func containerdFilesystem(device string) types.Filesystem {
	format, label, path, wipe := "ext4", ContainerdDiskLabel, ContainerdPath, true
	return types.Filesystem{Device: device, Format: &format, Label: &label, Path: &path, WipeFilesystem: &wipe}
}

const containerdMount = `[Unit]
Description=Mount the ephemeral containerd disk at ` + ContainerdPath + `
Before=local-fs.target

[Mount]
What=/dev/disk/by-label/` + ContainerdDiskLabel + `
Where=` + ContainerdPath + `
Type=ext4

[Install]
WantedBy=local-fs.target
`

func containerdDropin() types.Unit {
	contents := "[Unit]\nAfter=" + ContainerdMountUnit + "\nRequires=" + ContainerdMountUnit + "\n"
	return types.Unit{Name: "containerd.service", Dropins: []types.Dropin{{Name: "10-wait-containerd.conf", Contents: &contents}}}
}

func cniScript(o Options) string {
	return `#!/bin/bash
set -euo pipefail
CNI_VERSION="` + o.CNIVersion + `"
mkdir -p /opt/bin/
curl -fL "https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/cni-plugins-linux-amd64-${CNI_VERSION}.tgz" | tar -C /opt/bin/ -xz
echo "CNI plugins ${CNI_VERSION} installed"
`
}

func kubeToolsScript(o Options) string {
	return `#!/bin/bash
set -euo pipefail
set -a; . /etc/os-release; set +a
RELEASE="` + o.K8sVersion + `"
CRICTL="` + o.CrictlVersion + `"
if [ "${VARIANT_ID:-}" == "fedora" ]; then
  KUBE_MINOR="${RELEASE%.*}"
  KUBE_VERSION="${RELEASE#v}"
  cat > /etc/yum.repos.d/kubernetes.repo <<REPO
[kubernetes]
name=Kubernetes
baseurl=https://pkgs.k8s.io/core:/stable:/${KUBE_MINOR}/rpm/
enabled=1
gpgcheck=1
gpgkey=https://pkgs.k8s.io/core:/stable:/${KUBE_MINOR}/rpm/repodata/repomd.xml.key
REPO
  dnf install -y "kubeadm-${KUBE_VERSION}" "kubelet-${KUBE_VERSION}" "kubectl-${KUBE_VERSION}" cri-tools
  echo "exclude=kubelet kubeadm kubectl cri-tools kubernetes-cni" >> /etc/yum.repos.d/kubernetes.repo
  mkdir -p /opt/bin/
  for tool in kubeadm kubelet kubectl crictl; do ln -sf "/usr/bin/${tool}" "/opt/bin/${tool}"; done
  mkdir -p /etc/kubernetes/manifests
  echo "Kubernetes ${RELEASE} tools installed from pkgs.k8s.io"
  exit 0
fi
mkdir -p /opt/bin/
cd /opt/bin/
curl -fL --remote-name-all "https://dl.k8s.io/${RELEASE}/bin/linux/amd64/{kubeadm,kubelet,kubectl}"
chmod +x kubeadm kubelet kubectl
# crictl is a convenience for operators; kubeadm join does not need it, so a
# missing release must not fail the boot.
if ! curl -fsSL "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL}/crictl-${CRICTL}-linux-amd64.tar.gz" | tar -C /opt/bin/ -xz; then
  echo "warning: crictl ${CRICTL} not installed (download failed); continuing" >&2
fi
mkdir -p /etc/kubernetes/manifests
echo "Kubernetes ${RELEASE} tools and crictl ${CRICTL} installed"
`
}

func systemdScript(o Options) string {
	return `#!/bin/bash
set -euo pipefail
UNITS="` + o.KubeletUnitsURL + `"
curl -fsSL "${UNITS}/kubelet/kubelet.service" | sed "s:/usr/bin:/opt/bin:g" > /etc/systemd/system/kubelet.service
mkdir -p /etc/systemd/system/kubelet.service.d
curl -fsSL "${UNITS}/kubeadm/10-kubeadm.conf" | sed "s:/usr/bin:/opt/bin:g" > /etc/systemd/system/kubelet.service.d/10-kubeadm.conf
echo "KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false" > /etc/default/kubelet
mkdir -p /etc/sysconfig
echo "KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false" > /etc/sysconfig/kubelet
systemctl daemon-reload
systemctl enable kubelet && systemctl start kubelet
echo "kubelet started"
`
}

const joinScript = `#!/bin/bash
set -uo pipefail
if [ -z "${JOIN_STRING:-}" ]; then
  echo "JOIN_STRING is empty: Booty could not provide a kubeadm join token; not joining" >&2
  exit 0
fi
modprobe br_netfilter
sysctl net.bridge.bridge-nf-call-iptables=1
sysctl net.ipv4.ip_forward=1
export PATH=/opt/bin/:$PATH
kubeadm reset -f || true
exec ${JOIN_STRING}
`

// crictlVersionFor returns the cri-tools release matching a Kubernetes
// version: cri-tools tags once per minor (v1.34.0), not per patch.
func crictlVersionFor(k8sVersion string) string {
	v := strings.TrimPrefix(k8sVersion, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return k8sVersion
	}
	return "v" + parts[0] + "." + parts[1] + ".0"
}
