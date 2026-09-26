// Package cluster holds the settings and state of the one Kubernetes
// cluster a Booty instance provisions: which distribution, whether Booty
// manages the control plane, the cluster CA and the persisted bootstrap
// tokens. Rendering per role and distribution builds on it in later slices.
package cluster

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// Distribution is the Kubernetes flavour installed on every node.
type Distribution string

// Mode says who runs the control plane.
type Mode string

// Role is a host's place in the cluster.
type Role string

// CNI is the network plugin installed from the first control plane.
type CNI string

const (
	Kubeadm Distribution = "kubeadm"
	K0s     Distribution = "k0s"

	External Mode = "external"
	Managed  Mode = "managed"

	ControlPlane Role = hardware.RoleControlPlane
	Worker       Role = hardware.RoleWorker

	Cilium  CNI = "cilium"
	Calico  CNI = "calico"
	Flannel CNI = "flannel"
	NoCNI   CNI = "none"
)

var (
	distributions = []Distribution{Kubeadm, K0s}
	modes         = []Mode{External, Managed}
	cnis          = []CNI{Cilium, Calico, Flannel, NoCNI}
)

// Settings is the cluster configuration as read from the flags.
type Settings struct {
	Distribution Distribution
	ControlPlane Mode
	Endpoint     string
	CADir        string
	CNI          CNI
	CNIRelease   string
	PodCIDR      string
	ServiceCIDR  string
	K0sTokenFile string
	Kubeconfig   string
	Profile      string
	KubeadmJoin  string
}

// FromConfig reads the cluster settings from viper.
func FromConfig() Settings {
	return Settings{
		Distribution: Distribution(viper.GetString(config.ClusterDistribution)),
		ControlPlane: Mode(viper.GetString(config.ControlPlane)),
		Endpoint:     strings.TrimSpace(viper.GetString(config.ControlPlaneEndpt)),
		CADir:        viper.GetString(config.ClusterCADir),
		CNI:          CNI(viper.GetString(config.CNI)),
		CNIRelease:   viper.GetString(config.CNIRelease),
		PodCIDR:      viper.GetString(config.PodCIDR),
		ServiceCIDR:  viper.GetString(config.ServiceCIDR),
		K0sTokenFile: viper.GetString(config.K0sTokenFile),
		Kubeconfig:   viper.GetString(config.Kubeconfig),
		Profile:      viper.GetString(config.Profile),
		KubeadmJoin:  viper.GetString(config.KubeadmJoin),
	}
}

// Managed reports whether Booty renders the control plane itself.
func (s Settings) Managed() bool { return s.ControlPlane == Managed }

// Validate checks the flag values against each other. It is what cmd/main
// runs at startup; every message names the offending flag.
func (s Settings) Validate() error {
	if !slices.Contains(distributions, s.Distribution) {
		return fmt.Errorf("invalid --%s %q: must be %s", config.ClusterDistribution, s.Distribution, join(distributions))
	}
	if !slices.Contains(modes, s.ControlPlane) {
		return fmt.Errorf("invalid --%s %q: must be %s", config.ControlPlane, s.ControlPlane, join(modes))
	}
	if !slices.Contains(cnis, s.CNI) {
		return fmt.Errorf("invalid --%s %q: must be %s", config.CNI, s.CNI, join(cnis))
	}
	for key, cidr := range map[string]string{config.PodCIDR: s.PodCIDR, config.ServiceCIDR: s.ServiceCIDR} {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid --%s %q: %w", key, cidr, err)
		}
	}
	if s.Endpoint != "" {
		if _, _, err := config.ParseHostPort(s.Endpoint); err != nil {
			return fmt.Errorf("invalid --%s: %w", config.ControlPlaneEndpt, err)
		}
	}
	if s.Profile != "" && s.Distribution != Kubeadm {
		return fmt.Errorf("--%s=%s conflicts with --%s=%s", config.Profile, s.Profile, config.ClusterDistribution, s.Distribution)
	}
	if s.CADir != "" && s.Managed() {
		if err := readableCADir(s.CADir); err != nil {
			return fmt.Errorf("--%s: cluster CA directory %s: %w", config.ClusterCADir, s.CADir, err)
		}
	}
	if s.Kubeconfig != "" {
		if _, err := os.Stat(s.Kubeconfig); err != nil {
			return fmt.Errorf("--%s: %w", config.Kubeconfig, err)
		}
	}
	return nil
}

func readableCADir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("not a directory")
	}
	for _, name := range []string{"ca.crt", "ca.key"} {
		f, err := os.Open(dir + string(os.PathSeparator) + name)
		if err != nil {
			return err
		}
		config.CloseQuietly(f, name)
	}
	return nil
}

// Supports reports whether hosts running os can run distribution: Bluefin
// Server ships k0s and has no containerd/kubelet for kubeadm; Flatcar and
// Fedora CoreOS take both. An empty os is Booty's default (Flatcar).
func Supports(os string, distribution Distribution) bool {
	switch os {
	case "bluefin":
		return distribution == K0s
	case "", "flatcar", "coreos":
		return distribution == Kubeadm || distribution == K0s
	}
	return false
}

// RoleOf maps a host's registered role (empty = worker).
func RoleOf(h *hardware.Host) Role {
	if h.IsControlPlane() {
		return ControlPlane
	}
	return Worker
}

func join[T ~string](values []T) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprintf("%q", string(v))
	}
	return strings.Join(parts, ", ")
}
