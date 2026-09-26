package profile

import (
	"fmt"
	"strings"

	"github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/cni"
	ign "github.com/jeefy/booty/pkg/ignition"
	"gopkg.in/yaml.v3"
)

// Paths, units and labels of the managed kubeadm control plane.
const (
	InitScript         = ScriptDir + "/init.sh"
	ClusterReadyScript = ScriptDir + "/cluster-ready.sh"
	SeedScript         = ScriptDir + "/cp-seed.sh"
	KubeadmInitConfig  = "/etc/booty/kubeadm-init.yaml"
	CACertPath         = "/etc/kubernetes/pki/ca.crt"
	CAKeyPath          = "/etc/kubernetes/pki/ca.key"
	KubeletConfPath    = "/etc/kubernetes/kubelet.conf"

	UnitInit         = "booty-k8s-init.service"
	UnitClusterReady = "booty-cluster-ready.service"
	UnitSeed         = "booty-cp-seed.service"

	ControlPlaneDiskLabel = "booty-cp"
	ControlPlaneMount     = "/var/lib/booty-cp"
	// ControlPlaneMountUnit is systemd-escape(ControlPlaneMount).
	ControlPlaneMountUnit = `var-lib-booty\x2dcp.mount`
	KubeletDropinName     = "20-booty-control-plane.conf"

	CRISocket = "unix:///run/containerd/containerd.sock"
)

// bindMounts are the state directories a PXE-booted control plane keeps on
// the control-plane disk, as (mount unit, mount point, subdirectory).
var bindMounts = []struct{ unit, where, sub string }{
	{"etc-kubernetes.mount", "/etc/kubernetes", "kubernetes"},
	{"var-lib-etcd.mount", "/var/lib/etcd", "etcd"},
	{"var-lib-kubelet.mount", "/var/lib/kubelet", "kubelet"},
}

// ControlPlaneOptions is what the managed kubeadm control plane renders
// from. Every field is required except CNI (nil installs no network plugin).
type ControlPlaneOptions struct {
	// Server is Booty's host[:port], for POST /cluster/ready.
	Server string
	// Endpoint is the controlPlaneEndpoint every node uses (host[:port]).
	Endpoint       string
	PodCIDR        string
	ServiceCIDR    string
	CACert         []byte
	CAKey          []byte
	BootstrapToken string
	CertificateKey string
	// Disk is the block device formatted once (ext4, label booty-cp) and
	// bind-mounted over /etc/kubernetes, /var/lib/etcd and /var/lib/kubelet.
	Disk string
	CNI  *cni.Install
}

func (o *ControlPlaneOptions) validate() error {
	if o == nil {
		return fmt.Errorf("control plane options missing")
	}
	missing := func(field string) error { return fmt.Errorf("control plane options: %s is required", field) }
	switch {
	case o.Server == "":
		return missing("Server")
	case o.Endpoint == "":
		return missing("Endpoint")
	case o.PodCIDR == "":
		return missing("PodCIDR")
	case o.ServiceCIDR == "":
		return missing("ServiceCIDR")
	case len(o.CACert) == 0 || len(o.CAKey) == 0:
		return missing("CACert and CAKey")
	case o.BootstrapToken == "":
		return missing("BootstrapToken")
	case o.CertificateKey == "":
		return missing("CertificateKey")
	case o.Disk == "":
		return missing("Disk")
	}
	if strings.ContainsAny(o.Endpoint+o.PodCIDR+o.ServiceCIDR+o.BootstrapToken+o.CertificateKey, " \t\r\n\"'") {
		return fmt.Errorf("control plane options: values must not contain whitespace or quotes")
	}
	return nil
}

func controlPlaneFragment(cfg types.Config, opts Options) (types.Config, error) {
	cp := opts.ControlPlane
	if err := cp.validate(); err != nil {
		return cfg, err
	}
	if cp.Disk == opts.ContainerdDisk {
		return cfg, fmt.Errorf("control plane disk %s is also the containerd disk", cp.Disk)
	}
	initYAML, err := KubeadmInitYAML(opts.K8sVersion, cp)
	if err != nil {
		return cfg, err
	}

	cfg.Storage.Files = append(toolScripts(opts),
		ign.InlineFile(SeedScript, seedScript, 0o755),
		ign.InlineFile(InitScript, initScript, 0o755),
		ign.InlineFile(ClusterReadyScript, clusterReadyScript(cp.Server), 0o755),
		ign.InlineFile(CACertPath, string(cp.CACert), 0o644),
		ign.InlineFile(CAKeyPath, string(cp.CAKey), 0o600),
		ign.InlineFile(KubeadmInitConfig, initYAML, 0o600),
	)
	cfg.Storage.Filesystems, cfg.Systemd.Units = containerdDisk(opts.ContainerdDisk)
	cfg.Storage.Filesystems = append(cfg.Storage.Filesystems, controlPlaneFilesystem(cp.Disk))
	cfg.Systemd.Units = append(cfg.Systemd.Units, controlPlaneDiskUnits()...)
	cfg.Systemd.Units = append(cfg.Systemd.Units, toolChain(opts, requiresStateMounts)...)
	cfg.Systemd.Units = append(cfg.Systemd.Units,
		ign.Unit(UnitInit, true, initUnit),
		ign.Unit(UnitClusterReady, true, clusterReadyUnit),
	)
	if cp.CNI != nil {
		cfg.Storage.Files = append(cfg.Storage.Files, ign.InlineFile(cni.ScriptPath, cp.CNI.Script, 0o755))
		cfg.Systemd.Units = append(cfg.Systemd.Units, ign.Unit(cni.UnitName, true, cp.CNI.Unit))
	}
	return cfg, nil
}

func controlPlaneFilesystem(device string) types.Filesystem {
	format, label, path, wipe := "ext4", ControlPlaneDiskLabel, ControlPlaneMount, false
	return types.Filesystem{Device: device, Format: &format, Label: &label, Path: &path, WipeFilesystem: &wipe}
}

func controlPlaneDiskUnits() []types.Unit {
	units := []types.Unit{
		ign.Unit(ControlPlaneMountUnit, true, controlPlaneMount),
		ign.Unit(UnitSeed, true, seedUnit),
	}
	for _, m := range bindMounts {
		units = append(units, ign.Unit(m.unit, true, bindMountUnit(m.where, m.sub)))
	}
	dropin := "[Unit]\n" + requiresStateMounts
	units = append(units, types.Unit{Name: "kubelet.service", Dropins: []types.Dropin{{Name: KubeletDropinName, Contents: &dropin}}})
	return units
}

const controlPlaneMount = `[Unit]
Description=Mount the persistent control-plane disk at ` + ControlPlaneMount + `
Before=local-fs.target

[Mount]
What=/dev/disk/by-label/` + ControlPlaneDiskLabel + `
Where=` + ControlPlaneMount + `
Type=ext4

[Install]
WantedBy=local-fs.target
`

// seedUnit runs before basic.target (DefaultDependencies=no) so the bind
// mounts it prepares can still be ordered before local-fs.target.
const seedUnit = `[Unit]
Description=Seed the control-plane disk with the state directories and the cluster CA
DefaultDependencies=no
RequiresMountsFor=` + ControlPlaneMount + `
After=local-fs-pre.target
Before=local-fs.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + SeedScript + `

[Install]
WantedBy=local-fs.target
`

func bindMountUnit(where, sub string) string {
	return `[Unit]
Description=Bind ` + where + ` to the control-plane disk
RequiresMountsFor=` + ControlPlaneMount + `
Requires=` + UnitSeed + `
After=` + UnitSeed + `

[Mount]
What=` + ControlPlaneMount + `/` + sub + `
Where=` + where + `
Type=none
Options=bind

[Install]
WantedBy=local-fs.target
`
}

var seedScript = func() string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -euo pipefail\n")
	for _, m := range bindMounts {
		fmt.Fprintf(&b, "mkdir -p %s/%s %s\n", ControlPlaneMount, m.sub, m.where)
	}
	fmt.Fprintf(&b, "cp -an /etc/kubernetes/. %s/kubernetes/\n", ControlPlaneMount)
	fmt.Fprintf(&b, "chmod 0600 %s/kubernetes/pki/ca.key\n", ControlPlaneMount)
	b.WriteString(`echo "control-plane disk seeded"` + "\n")
	return b.String()
}()

const initScript = `#!/bin/bash
set -euo pipefail
export PATH=/opt/bin:$PATH
modprobe br_netfilter
sysctl -w net.bridge.bridge-nf-call-iptables=1
sysctl -w net.ipv4.ip_forward=1
exec kubeadm init --config ` + KubeadmInitConfig + ` --upload-certs
`

const initUnit = `[Unit]
Description=Initialise the Kubernetes control plane with kubeadm
Requires=` + UnitKubeletSetup + `
After=` + UnitKubeletSetup + `
` + requiresStateMounts + `ConditionPathExists=!` + KubeletConfPath + `

[Service]
Type=oneshot
RemainAfterExit=yes
Restart=on-failure
RestartSec=30s
ExecStart=` + InitScript + `

[Install]
WantedBy=multi-user.target
`

const requiresStateMounts = "RequiresMountsFor=/etc/kubernetes /var/lib/etcd /var/lib/kubelet\n"

// clusterReadyUnit only Wants= the init unit: a failing init keeps
// restarting, and this unit's own restart loop waits for admin.conf.
const clusterReadyUnit = `[Unit]
Description=Tell Booty the control plane answers (POST /cluster/ready)
Wants=` + UnitInit + `
After=` + UnitInit + ` network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
Restart=on-failure
RestartSec=30s
ExecStart=` + ClusterReadyScript + `

[Install]
WantedBy=multi-user.target
`

func clusterReadyScript(server string) string {
	return `#!/bin/bash
set -euo pipefail
export PATH=/opt/bin:$PATH
if [ ! -f /etc/kubernetes/admin.conf ]; then
  echo "kubeadm init has not finished yet" >&2
  exit 1
fi
kubectl --kubeconfig /etc/kubernetes/admin.conf get --raw /readyz >/dev/null
set -- $(ip -o route get 1); while [ $# -gt 1 ] && [ "$1" != dev ]; do shift; done; MAC=$(cat /sys/class/net/$2/address)
curl -fsS --retry 5 --retry-connrefused -X POST "http://` + server + `/cluster/ready?mac=$MAC"
echo
`
}

type kubeadmInitConfiguration struct {
	APIVersion       string                  `yaml:"apiVersion"`
	Kind             string                  `yaml:"kind"`
	BootstrapTokens  []kubeadmBootstrapToken `yaml:"bootstrapTokens"`
	CertificateKey   string                  `yaml:"certificateKey"`
	NodeRegistration kubeadmNodeRegistration `yaml:"nodeRegistration"`
}

type kubeadmBootstrapToken struct {
	Token  string   `yaml:"token"`
	TTL    string   `yaml:"ttl"`
	Usages []string `yaml:"usages"`
	Groups []string `yaml:"groups"`
}

type kubeadmNodeRegistration struct {
	CRISocket        string       `yaml:"criSocket"`
	KubeletExtraArgs []kubeadmArg `yaml:"kubeletExtraArgs"`
}

type kubeadmArg struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type kubeadmClusterConfiguration struct {
	APIVersion           string            `yaml:"apiVersion"`
	Kind                 string            `yaml:"kind"`
	KubernetesVersion    string            `yaml:"kubernetesVersion"`
	ClusterName          string            `yaml:"clusterName"`
	ControlPlaneEndpoint string            `yaml:"controlPlaneEndpoint"`
	CertificatesDir      string            `yaml:"certificatesDir"`
	Networking           kubeadmNetworking `yaml:"networking"`
	Etcd                 kubeadmEtcd       `yaml:"etcd"`
}

type kubeadmNetworking struct {
	DNSDomain     string `yaml:"dnsDomain"`
	PodSubnet     string `yaml:"podSubnet"`
	ServiceSubnet string `yaml:"serviceSubnet"`
}

type kubeadmEtcd struct {
	Local struct {
		DataDir string `yaml:"dataDir"`
	} `yaml:"local"`
}

type kubeletConfiguration struct {
	APIVersion   string `yaml:"apiVersion"`
	Kind         string `yaml:"kind"`
	CgroupDriver string `yaml:"cgroupDriver"`
	FailSwapOn   bool   `yaml:"failSwapOn"`
}

// KubeadmInitYAML renders /etc/booty/kubeadm-init.yaml: a v1beta4
// InitConfiguration carrying the pre-generated worker bootstrap token and
// the upload-certs key, the ClusterConfiguration and the kubelet
// configuration matching the worker profile (systemd cgroups, swap
// tolerated).
func KubeadmInitYAML(k8sVersion string, cp *ControlPlaneOptions) (string, error) {
	init := kubeadmInitConfiguration{
		APIVersion: "kubeadm.k8s.io/v1beta4",
		Kind:       "InitConfiguration",
		BootstrapTokens: []kubeadmBootstrapToken{{
			Token:  cp.BootstrapToken,
			TTL:    "168h0m0s",
			Usages: []string{"signing", "authentication"},
			Groups: []string{"system:bootstrappers:kubeadm:default-node-token"},
		}},
		CertificateKey: cp.CertificateKey,
		NodeRegistration: kubeadmNodeRegistration{
			CRISocket:        CRISocket,
			KubeletExtraArgs: []kubeadmArg{{Name: "fail-swap-on", Value: "false"}},
		},
	}
	cluster := kubeadmClusterConfiguration{
		APIVersion:           "kubeadm.k8s.io/v1beta4",
		Kind:                 "ClusterConfiguration",
		KubernetesVersion:    k8sVersion,
		ClusterName:          "kubernetes",
		ControlPlaneEndpoint: cp.Endpoint,
		CertificatesDir:      "/etc/kubernetes/pki",
		Networking:           kubeadmNetworking{DNSDomain: "cluster.local", PodSubnet: cp.PodCIDR, ServiceSubnet: cp.ServiceCIDR},
	}
	cluster.Etcd.Local.DataDir = "/var/lib/etcd"
	kubelet := kubeletConfiguration{
		APIVersion:   "kubelet.config.k8s.io/v1beta1",
		Kind:         "KubeletConfiguration",
		CgroupDriver: "systemd",
		FailSwapOn:   false,
	}
	var b strings.Builder
	for i, doc := range []any{init, cluster, kubelet} {
		if i > 0 {
			b.WriteString("---\n")
		}
		out, err := yaml.Marshal(doc)
		if err != nil {
			return "", fmt.Errorf("kubeadm init config: %w", err)
		}
		b.Write(out)
	}
	return b.String(), nil
}
