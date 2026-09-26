// Package k0s renders what a k0s node needs from Booty, independently of
// how it gets there: the Ignition fragment for Flatcar/Fedora CoreOS
// (pkg/profile) and the Bluefin Server credentials bundle (pkg/creds) both
// write the same files, units and drop-ins, so /etc/k0s/k0s.yaml, the six
// PKI files, the join token, the bootstrap Secret manifest and the
// ready/CNI scripts have exactly one source.
//
// A controller gets the cluster CA, service-account keys and etcd CA under
// /var/lib/k0s/pki before k0s starts for the first time (k0s then signs
// everything else with them), a ClusterConfig with the endpoint and the
// networks, and the pre-shared bootstrap tokens as a Secret manifest that
// k0s applies from /var/lib/k0s/manifests. A worker gets the encoded join
// token at /etc/k0s/token. On a PXE-booted OS Booty ships its own
// k0scontroller.service/k0sworker.service around /opt/bin/k0s; on Bluefin
// the image's k0scontroller.service (started by k0s-first-boot.service by
// name on every boot) is retargeted with an ExecStart= drop-in, so a
// Bluefin worker still runs a unit called k0scontroller.service.
package k0s

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jeefy/booty/pkg/cluster/pki"
	"github.com/jeefy/booty/pkg/cluster/token"
	"github.com/jeefy/booty/pkg/cni"
	"github.com/jeefy/booty/pkg/config"
	"gopkg.in/yaml.v3"
)

// Pinned release. Checked against the GitHub releases API on 2026-09-26:
// https://github.com/k0sproject/k0s/releases/tag/v1.36.4%2Bk0s.0 ships
// k0s-v1.36.4+k0s.0-amd64 (262445792 bytes) with this sha256 both in the
// release's sha256sums.txt and as the asset digest. Bluefin Server 26.08.0
// runs the same version as /usr/bin/k0s.
const (
	DefaultVersion = config.DefaultK0sVersion
	DefaultSHA256  = "ca1e9e68107335846e8296777fce2ccd654284e6265b4b5d32c34ead872af98f"
)

// Paths on the node.
const (
	PXEBinary     = "/opt/bin/k0s"
	BluefinBinary = "/usr/bin/k0s"

	ConfigDir       = "/etc/k0s"
	ConfigPath      = ConfigDir + "/k0s.yaml"
	TokenPath       = ConfigDir + "/token"
	DataDir         = "/var/lib/k0s"
	PKIDir          = DataDir + "/pki"
	AdminKubeconfig = PKIDir + "/admin.conf"
	ManifestsDir    = DataDir + "/manifests/booty"
	TokensManifest  = ManifestsDir + "/tokens.yaml"
	ContainerdDir   = DataDir + "/containerd"

	ScriptDir          = "/opt/booty"
	ClusterReadyScript = ScriptDir + "/cluster-ready.sh"

	// PXEMarker and BluefinMarker are where the CNI install leaves its
	// done-marker: the control-plane disk on a PXE host (RAM root), the
	// installed root on Bluefin.
	PXEMarker     = cni.MarkerPath
	BluefinMarker = "/var/lib/booty/cni-applied"
)

// Unit names. ControllerUnit is also the name of Bluefin's own unit.
const (
	ControllerUnit   = "k0scontroller.service"
	WorkerUnit       = "k0sworker.service"
	InstallUnit      = "booty-k0s-install.service"
	UnitClusterReady = "booty-cluster-ready.service"
	// RoleDropIn is the Bluefin drop-in that resets ExecStart= to Booty's
	// command; it sits next to the Wants= drop-in pkg/creds already writes.
	RoleDropIn = "booty-role.conf"

	// ControllerArgs are the k0s controller flags on every OS: the
	// controller also runs a worker, and helm/autopilot are disabled as in
	// Bluefin's stock unit (whose --single is dropped so the node can be
	// joined).
	ControllerArgs = "controller -c " + ConfigPath + " --enable-worker --disable-components=helm,autopilot"
	WorkerArgs     = "worker --token-file " + TokenPath
)

type Role = token.K0sRole

const (
	Controller = token.K0sController
	Worker     = token.K0sWorker
)

// VersionPattern is what --k0sVersion must look like: a k0s release tag.
var VersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k0s\.[0-9]+$`)

// ValidateVersion rejects anything but a k0s release tag.
func ValidateVersion(v string) error {
	if !VersionPattern.MatchString(v) {
		return fmt.Errorf("invalid --%s %q: must be a k0s release tag such as %s", config.K0sVersion, v, DefaultVersion)
	}
	return nil
}

// DownloadURL is the amd64 binary of version on GitHub; the tag's '+' is
// percent-encoded as the releases API does.
func DownloadURL(version string) string {
	return releaseBase(version) + "/k0s-" + escape(version) + "-amd64"
}

// ChecksumsURL is the release's sha256sums.txt, used when version is not
// the pinned one.
func ChecksumsURL(version string) string {
	return releaseBase(version) + "/sha256sums.txt"
}

// PinnedSHA256 is the checksum baked into Booty for version, empty for any
// other version (the install script then verifies against sha256sums.txt).
func PinnedSHA256(version string) string {
	if version == DefaultVersion {
		return DefaultSHA256
	}
	return ""
}

func releaseBase(version string) string {
	return "https://github.com/k0sproject/k0s/releases/download/" + escape(version)
}

func escape(version string) string { return strings.ReplaceAll(version, "+", "%2B") }

// Provider maps --cni to spec.network.provider: calico is k0s's built-in
// Calico, none keeps k0s's default kube-router (the node is Ready out of
// the box), cilium and flannel are installed by Booty's CNI oneshot on top
// of provider custom.
func Provider(cniName string) string {
	switch cniName {
	case cni.Calico:
		return "calico"
	case cni.None:
		return "kuberouter"
	}
	return "custom"
}

// Custom reports whether Booty installs the CNI itself (provider custom).
func Custom(cniName string) bool { return Provider(cniName) == "custom" }

// File, Unit and DropIn are the renderer-agnostic outputs.
type File struct {
	Path     string
	Mode     int
	Contents string
}

// Unit is a whole unit file; WantedBy is the target it is enabled into
// (empty: installed but not enabled).
type Unit struct {
	Name     string
	Contents string
	WantedBy string
}

// DropIn is <Unit>.d/<Name> with Contents.
type DropIn struct {
	Unit     string
	Name     string
	Contents string
}

// Node is everything a k0s node gets from Booty. Files are sorted by path,
// so two renders of the same input compare equal.
type Node struct {
	Role    Role
	OS      string
	Files   []File
	Units   []Unit
	DropIns []DropIn
}

// Options is what Render needs. Endpoint is host[:port] (port defaults to
// 6443); Server is Booty's host[:port] for POST /cluster/ready. A
// controller needs PKI (the six files) and Secrets (the bootstrap Secret
// manifest); a worker needs WorkerToken (the encoded join token).
type Options struct {
	Role        Role
	OS          string
	Server      string
	Endpoint    string
	PodCIDR     string
	ServiceCIDR string
	CNI         string
	CNIRelease  string
	PKI         map[string][]byte
	Secrets     []byte
	WorkerToken string
}

// PKIFiles are the six files k0s reads from /var/lib/k0s/pki, keyed by
// their path relative to it; the order is the order they are rendered in.
var PKIFiles = []string{pki.CACertFile, pki.CAKeyFile, pki.SAKeyFile, pki.SAPubFile, pki.EtcdCACertFile, pki.EtcdCAKeyFile}

// platform is what differs between a PXE-booted OS and Bluefin.
type platform struct {
	binary  string
	pxe     bool
	marker  string
	install string // unit the k0s service waits for; empty when the binary is in the image
}

func platformFor(os string) (platform, error) {
	switch os {
	case "", "flatcar", "coreos":
		return platform{binary: PXEBinary, pxe: true, marker: PXEMarker, install: InstallUnit}, nil
	case "bluefin":
		return platform{binary: BluefinBinary, marker: BluefinMarker}, nil
	}
	return platform{}, fmt.Errorf("k0s: unsupported os %q", os)
}

// Render builds the Node for o.
func Render(o Options) (*Node, error) {
	p, err := platformFor(o.OS)
	if err != nil {
		return nil, err
	}
	if o.Role != Controller && o.Role != Worker {
		return nil, fmt.Errorf("k0s: unsupported role %q", o.Role)
	}
	if strings.ContainsAny(o.Server+o.Endpoint, " \t\r\n\"'`$\\") {
		return nil, errors.New("k0s: server and endpoint must not contain whitespace or quotes")
	}
	n := &Node{Role: o.Role, OS: o.OS}
	if o.Role == Worker {
		err = n.worker(o, p)
	} else {
		err = n.controller(o, p)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(n.Files, func(i, j int) bool { return n.Files[i].Path < n.Files[j].Path })
	return n, nil
}

func (n *Node) worker(o Options, p platform) error {
	join, err := token.ParseK0s(o.WorkerToken)
	if err != nil {
		return fmt.Errorf("k0s worker: %w", err)
	}
	if join.Role != Worker {
		return fmt.Errorf("k0s worker: token is a %s token", join.Role)
	}
	n.Files = append(n.Files, File{Path: TokenPath, Mode: 0o600, Contents: o.WorkerToken + "\n"})
	n.service(p, WorkerUnit, "k0s worker", WorkerArgs)
	return nil
}

func (n *Node) controller(o Options, p platform) error {
	if o.Server == "" || o.Endpoint == "" {
		return errors.New("k0s controller: Server and Endpoint are required")
	}
	for _, name := range PKIFiles {
		data, ok := o.PKI[name]
		if !ok || len(data) == 0 {
			return fmt.Errorf("k0s controller: PKI file %s missing", name)
		}
		n.Files = append(n.Files, File{Path: PKIDir + "/" + name, Mode: 0o600, Contents: string(data)})
	}
	if len(o.Secrets) == 0 {
		return errors.New("k0s controller: bootstrap Secret manifest missing")
	}
	cfg, err := ConfigYAML(o.Endpoint, o.PodCIDR, o.ServiceCIDR, o.CNI)
	if err != nil {
		return err
	}
	n.Files = append(n.Files,
		File{Path: ConfigPath, Mode: 0o600, Contents: string(cfg)},
		File{Path: TokensManifest, Mode: 0o600, Contents: string(o.Secrets)},
		File{Path: ClusterReadyScript, Mode: 0o755, Contents: clusterReadyScript(p.binary, o.Server)},
	)
	n.service(p, ControllerUnit, "k0s controller (with embedded worker)", ControllerArgs)
	n.Units = append(n.Units, Unit{Name: UnitClusterReady, Contents: clusterReadyUnit, WantedBy: "multi-user.target"})
	if Custom(o.CNI) {
		install, err := cni.Render(o.CNI, o.CNIRelease, o.PodCIDR, cni.Target{
			After:      ControllerUnit,
			Weak:       true,
			Kubectl:    p.binary + " kubectl",
			Kubeconfig: AdminKubeconfig,
			Marker:     p.marker,
		})
		if err != nil {
			return err
		}
		n.Files = append(n.Files, File{Path: cni.ScriptPath, Mode: 0o755, Contents: install.Script})
		n.Units = append(n.Units, Unit{Name: cni.UnitName, Contents: install.Unit, WantedBy: "multi-user.target"})
	}
	return nil
}

// service adds the k0s service itself: a whole unit on a PXE-booted OS, a
// drop-in on Bluefin's k0scontroller.service (whatever the role, see the
// package comment).
func (n *Node) service(p platform, name, description, args string) {
	if !p.pxe {
		n.DropIns = append(n.DropIns, DropIn{Unit: ControllerUnit, Name: RoleDropIn, Contents: "[Service]\nExecStart=\nExecStart=" + p.binary + " " + args + "\n"})
		return
	}
	n.Units = append(n.Units, Unit{Name: name, Contents: pxeUnit(p, description, args), WantedBy: "multi-user.target"})
}

// pxeUnit follows the unit `k0s install` writes (Delegate, KillMode,
// limits, Restart=always), ordered after Booty's install oneshot and the
// state mounts.
func pxeUnit(p platform, description, args string) string {
	return `[Unit]
Description=` + description + `
Documentation=https://docs.k0sproject.io
Wants=network-online.target
After=network-online.target ` + p.install + `
Requires=` + p.install + `
RequiresMountsFor=` + DataDir + `

[Service]
ExecStart=` + p.binary + ` ` + args + `
RestartSec=10
Delegate=yes
KillMode=process
LimitCORE=infinity
TasksMax=infinity
TimeoutStartSec=0
LimitNOFILE=999999
Restart=always

[Install]
WantedBy=multi-user.target
`
}

// clusterReadyUnit only Wants= the controller: k0scontroller.service is a
// long-running service with Restart=always, and this oneshot has its own
// retry loop until admin.conf exists and /readyz answers.
const clusterReadyUnit = `[Unit]
Description=Tell Booty the control plane answers (POST /cluster/ready)
Wants=` + ControllerUnit + ` network-online.target
After=` + ControllerUnit + ` network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
Restart=on-failure
RestartSec=30s
ExecStart=` + ClusterReadyScript + `

[Install]
WantedBy=multi-user.target
`

func clusterReadyScript(binary, server string) string {
	return `#!/bin/bash
set -euo pipefail
if [ ! -f ` + AdminKubeconfig + ` ]; then
  echo "k0s controller has not written ` + AdminKubeconfig + ` yet" >&2
  exit 1
fi
KUBECONFIG=` + AdminKubeconfig + ` ` + binary + ` kubectl get --raw /readyz >/dev/null
set -- $(ip -o route get 1); while [ $# -gt 1 ] && [ "$1" != dev ]; do shift; done; MAC=$(cat /sys/class/net/$2/address)
curl -fsS --retry 5 --retry-connrefused -X POST "http://` + server + `/cluster/ready?mac=$MAC"
echo
`
}

type clusterConfig struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		API struct {
			ExternalAddress string `yaml:"externalAddress"`
			Port            int    `yaml:"port,omitempty"`
		} `yaml:"api"`
		Network struct {
			PodCIDR     string `yaml:"podCIDR"`
			ServiceCIDR string `yaml:"serviceCIDR"`
			Provider    string `yaml:"provider"`
		} `yaml:"network"`
	} `yaml:"spec"`
}

// ConfigYAML renders /etc/k0s/k0s.yaml: a v1beta1 ClusterConfig whose
// spec.api.externalAddress is the endpoint host (its port, when not 6443,
// becomes spec.api.port) and whose network carries the cluster CIDRs and
// the provider for cniName. Everything else keeps k0s's defaults (etcd,
// kube-proxy in iptables mode, konnectivity). Validates with
// `k0s config validate`.
func ConfigYAML(endpoint, podCIDR, serviceCIDR, cniName string) ([]byte, error) {
	host, port, err := config.ParseHostPort(endpoint)
	if err != nil {
		return nil, fmt.Errorf("k0s config: %w", err)
	}
	if podCIDR == "" || serviceCIDR == "" {
		return nil, errors.New("k0s config: podCIDR and serviceCIDR are required")
	}
	var c clusterConfig
	c.APIVersion = "k0s.k0sproject.io/v1beta1"
	c.Kind = "ClusterConfig"
	c.Metadata.Name = "k0s"
	c.Spec.API.ExternalAddress = host
	if port != 0 && port != 6443 {
		c.Spec.API.Port = port
	}
	c.Spec.Network.PodCIDR = podCIDR
	c.Spec.Network.ServiceCIDR = serviceCIDR
	c.Spec.Network.Provider = Provider(cniName)
	out, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("k0s config: %w", err)
	}
	return out, nil
}
