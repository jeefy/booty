// Package autopilot wires the self-healing upgrade controller described in
// docs/plans/2026-09-28-autopilot.md. P2 ships the cluster client and the
// actuator chain behind a dry run: Setup reads the flags, builds the
// cluster client and the actuator chooser, and Status reports what would
// be used. Nothing in this package reboots, cordons or evicts; only the
// controller of P3 calls the actuators.
package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/kubeadm"
	"github.com/spf13/viper"
)

// Settings are the autopilot flags.
type Settings struct {
	Mode         string
	Kubeconfig   string
	Namespace    string
	Image        string
	DrainTimeout time.Duration
	SSHKey       string
}

// FromConfig reads the flags. Namespace falls back to POD_NAMESPACE, then
// kube-system; Image falls back to BOOTY_IMAGE and may stay empty (the API
// actuator then refuses to build a reboot Pod, with a message naming both).
func FromConfig() Settings {
	s := Settings{
		Mode:         viper.GetString(config.Autopilot),
		Kubeconfig:   viper.GetString(config.Kubeconfig),
		Namespace:    viper.GetString(config.AutopilotNamespace),
		Image:        viper.GetString(config.AutopilotImage),
		DrainTimeout: viper.GetDuration(config.AutopilotDrainTO),
		SSHKey:       viper.GetString(config.RebootSSHKey),
	}
	if s.Namespace == "" {
		s.Namespace = os.Getenv(config.PodNamespaceEnv)
	}
	if s.Namespace == "" {
		s.Namespace = config.DefaultAutopilotNamespace
	}
	if s.Image == "" {
		s.Image = os.Getenv(config.BootyImageEnv)
	}
	return s
}

// Enabled reports whether the mode is anything but off.
func (s Settings) Enabled() bool { return s.Mode != config.AutopilotOff }

// Validate checks the flag values.
func (s Settings) Validate() error {
	if err := config.ValidateAutopilot(s.Mode); err != nil {
		return err
	}
	if s.DrainTimeout <= 0 {
		return fmt.Errorf("--%s must be positive, got %s", config.AutopilotDrainTO, s.DrainTimeout)
	}
	if s.SSHKey != "" {
		if _, err := os.Stat(s.SSHKey); err != nil {
			return fmt.Errorf("--%s: %w", config.RebootSSHKey, err)
		}
	}
	return nil
}

// Autopilot is the running instance: the settings, the cluster client (nil
// without a cluster) and the actuator chooser.
type Autopilot struct {
	Settings Settings
	Client   *k8s.Client
	Chooser  *actuator.Chooser
}

// Setup builds the cluster client (from --kubeconfig, an existing API
// client such as the minter's, or the in-cluster environment) and the
// chooser. It makes no API call.
func Setup(s Settings, shared *kubeadm.Client) (*Autopilot, error) {
	var client *k8s.Client
	switch {
	case shared != nil && shared.APIServer() != "":
		client = k8s.New(shared)
	case s.Kubeconfig != "":
		c, err := k8s.FromKubeconfig(s.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", config.Kubeconfig, err)
		}
		client = c
	default:
		if c := k8s.InCluster(); c.Configured() {
			client = c
		}
	}
	chooser := actuator.NewChooser(actuator.Options{
		Client:       client,
		Namespace:    s.Namespace,
		Image:        s.Image,
		DrainTimeout: s.DrainTimeout,
		SSHKeyPath:   s.SSHKey,
		KnownHosts:   config.AutopilotPath("known_hosts"),
	})
	return &Autopilot{Settings: s, Client: client, Chooser: chooser}, nil
}

// Status is GET /autopilot in P2: the mode, what the cluster looks like,
// the actuator that would be used, and that nothing acts yet.
type Status struct {
	Mode     string          `json:"mode"`
	Cluster  actuator.Status `json:"cluster"`
	Actuator string          `json:"actuator"`
	// SSHUsers is per-OS only for the ssh actuator, which logs in as root
	// on Bluefin and core elsewhere.
	SSHUsers map[string]string `json:"sshUsers,omitempty"`
	DryRun   bool              `json:"dryRun"`
}

// Status inspects the cluster (reads only) and names the actuator.
func (a *Autopilot) Status(ctx context.Context) Status {
	st := Status{Mode: a.Settings.Mode, Actuator: actuator.NameNone, DryRun: true}
	if !a.Settings.Enabled() {
		return st
	}
	cluster, name := a.Chooser.Inspect(ctx)
	st.Cluster, st.Actuator = cluster, name
	if name == actuator.NameSSH {
		s := &actuator.SSH{}
		st.SSHUsers = map[string]string{"bluefin": s.User("bluefin"), "flatcar": s.User("flatcar"), "coreos": s.User("coreos")}
	}
	return st
}

// LogStatus writes the start-up summary.
func (a *Autopilot) LogStatus(ctx context.Context) {
	st := a.Status(ctx)
	if !a.Settings.Enabled() {
		slog.Info("Autopilot off")
		return
	}
	attrs := []any{"mode", st.Mode, "dryRun", true, "actuator", st.Actuator, "apiServer", st.Cluster.APIServer, "reachable", st.Cluster.Reachable, "kured", st.Cluster.Kured, "nodes", st.Cluster.Nodes, "namespace", a.Settings.Namespace, "image", a.Settings.Image, "drainTimeout", a.Settings.DrainTimeout}
	if st.Cluster.Error != "" {
		attrs = append(attrs, "error", st.Cluster.Error)
	}
	slog.Info("Autopilot dry run: P2 reports the actuator it would use; nothing reboots, cordons or evicts", attrs...)
	switch st.Actuator {
	case actuator.NameSSH:
		slog.Info("Autopilot would reboot over SSH", "users", st.SSHUsers, "key", a.Settings.SSHKey, "drain", a.Client.Configured() && st.Cluster.Reachable)
	case actuator.NameAPI:
		if a.Settings.Image == "" {
			slog.Warn("Autopilot would use the API actuator but Booty's image is unknown; set --autopilotImage or BOOTY_IMAGE before P3 can run reboot Pods")
		}
	case actuator.NameNone:
		slog.Warn("Autopilot has no actuator: no kured, no reachable Kubernetes API and no --rebootSSHKey")
	}
}
