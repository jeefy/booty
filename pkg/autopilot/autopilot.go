// Package autopilot wires the self-healing upgrade controller described in
// docs/plans/2026-09-28-autopilot.md: Setup reads the flags, builds the
// cluster client, the actuator chooser and (unless the mode is off) the
// controller; Status is GET /autopilot. Only the controller
// (pkg/autopilot/controller) ever calls an actuator.
package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/autopilot/controller"
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
	// HealthWindow and RetryAfter are the controller's two clocks: the
	// health-gate window from the observed reboot, and the TIMEOUT a bad
	// release sits in before its single retry.
	HealthWindow time.Duration
	RetryAfter   time.Duration
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
		HealthWindow: viper.GetDuration(config.AutopilotHealthWin),
		RetryAfter:   viper.GetDuration(config.AutopilotRetryAfter),
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
	if s.Enabled() && s.HealthWindow <= 0 {
		return fmt.Errorf("--%s must be positive, got %s", config.AutopilotHealthWin, s.HealthWindow)
	}
	if s.Enabled() && s.RetryAfter <= 0 {
		return fmt.Errorf("--%s must be positive, got %s", config.AutopilotRetryAfter, s.RetryAfter)
	}
	if s.SSHKey != "" {
		if _, err := os.Stat(s.SSHKey); err != nil {
			return fmt.Errorf("--%s: %w", config.RebootSSHKey, err)
		}
	}
	return nil
}

// Autopilot is the running instance: the settings, the cluster client (nil
// without a cluster), the actuator chooser and the controller (nil when
// the mode is off: nothing is started and nothing changes).
type Autopilot struct {
	Settings   Settings
	Client     *k8s.Client
	Chooser    *actuator.Chooser
	Controller *controller.Controller
}

// Setup builds the cluster client (from --kubeconfig, an existing API
// client such as the minter's, or the in-cluster environment), the
// chooser and, when the mode is not off, the controller with its state
// loaded from --dataDir/autopilot/state.json. It makes no API call and
// starts no goroutine; the caller runs Controller.Run.
func Setup(s Settings, shared *kubeadm.Client) (*Autopilot, error) {
	a, err := setupClient(s, shared)
	if err != nil || !s.Enabled() {
		return a, err
	}
	var cluster controller.Cluster
	if a.Client != nil && a.Client.Configured() {
		cluster = a.Client
	}
	ctrl, err := controller.New(controller.Options{
		Mode:         s.Mode,
		Fleet:        controller.LiveFleet{},
		Cluster:      cluster,
		Actuators:    a.Chooser,
		StatePath:    config.AutopilotPath(config.AutopilotStateFile),
		HealthWindow: s.HealthWindow,
		RetryAfter:   s.RetryAfter,
	})
	if err != nil {
		return nil, err
	}
	a.Controller = ctrl
	return a, nil
}

func setupClient(s Settings, shared *kubeadm.Client) (*Autopilot, error) {
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

// Status is GET /autopilot: the mode, what the cluster looks like, the
// actuator in use, and the controller's per-OS releases, per-host
// episodes, events and report stubs. DryRun is true only while no
// controller runs (mode off).
type Status struct {
	Mode     string          `json:"mode"`
	Cluster  actuator.Status `json:"cluster"`
	Actuator string          `json:"actuator"`
	// SSHUsers is per-OS only for the ssh actuator, which logs in as root
	// on Bluefin and core elsewhere.
	SSHUsers     map[string]string `json:"sshUsers,omitempty"`
	DryRun       bool              `json:"dryRun"`
	HealthWindow string            `json:"healthWindow,omitempty"`
	RetryAfter   string            `json:"retryAfter,omitempty"`
	*controller.Status
}

// Status inspects the cluster (reads only) and names the actuator.
func (a *Autopilot) Status(ctx context.Context) Status {
	st := Status{Mode: a.Settings.Mode, Actuator: actuator.NameNone, DryRun: true}
	if !a.Settings.Enabled() {
		return st
	}
	if a.Chooser != nil {
		st.Cluster, st.Actuator = a.Chooser.Inspect(ctx)
	}
	if st.Actuator == actuator.NameSSH {
		s := &actuator.SSH{}
		st.SSHUsers = map[string]string{"bluefin": s.User("bluefin"), "flatcar": s.User("flatcar"), "coreos": s.User("coreos")}
	}
	if a.Controller != nil {
		cs := a.Controller.Status()
		st.Status, st.DryRun = &cs, false
		st.HealthWindow, st.RetryAfter = a.Settings.HealthWindow.String(), a.Settings.RetryAfter.String()
	}
	return st
}

// Summary is the /info.autopilot block.
type Summary struct {
	Mode       string   `json:"mode"`
	Actuator   string   `json:"actuator"`
	Held       []string `json:"held"`
	Quarantine int      `json:"quarantined"`
	NeedsHands int      `json:"needsHands"`
}

// Summary is cheap: it reads the controller's state and the actuator the
// last reconcile chose, without touching the cluster.
func (a *Autopilot) Summary() Summary {
	s := Summary{Mode: config.AutopilotOff, Actuator: actuator.NameNone, Held: []string{}}
	if a == nil || a.Controller == nil {
		if a != nil {
			s.Mode = a.Settings.Mode
		}
		return s
	}
	cs := a.Controller.Summary()
	return Summary{Mode: cs.Mode, Actuator: cs.Actuator, Held: cs.Held, Quarantine: cs.Quarantine, NeedsHands: cs.NeedsHands}
}

// LogStatus writes the start-up summary.
func (a *Autopilot) LogStatus(ctx context.Context) {
	st := a.Status(ctx)
	if !a.Settings.Enabled() {
		slog.Info("Autopilot off")
		return
	}
	attrs := []any{"mode", st.Mode, "actuator", st.Actuator, "apiServer", st.Cluster.APIServer, "reachable", st.Cluster.Reachable, "kured", st.Cluster.Kured, "nodes", st.Cluster.Nodes, "namespace", a.Settings.Namespace, "image", a.Settings.Image, "drainTimeout", a.Settings.DrainTimeout, "healthWindow", a.Settings.HealthWindow, "retryAfter", a.Settings.RetryAfter}
	if st.Cluster.Error != "" {
		attrs = append(attrs, "error", st.Cluster.Error)
	}
	slog.Info("Autopilot on: health gate, retry, rollback to lastGood and fleet hold for every OS"+map[bool]string{true: "; Bluefin canary-serial rollout, TIMEOUT/retry, quarantine and skip-to-next", false: ""}[st.Mode == config.AutopilotFull], attrs...)
	switch st.Actuator {
	case actuator.NameSSH:
		slog.Info("Autopilot reboots over SSH", "users", st.SSHUsers, "key", a.Settings.SSHKey, "drain", a.Client.Configured() && st.Cluster.Reachable)
	case actuator.NameAPI:
		if a.Settings.Image == "" {
			slog.Warn("Autopilot would use the API actuator but Booty's image is unknown; set --autopilotImage or BOOTY_IMAGE or reboot Pods cannot be created")
		}
	case actuator.NameNone:
		slog.Warn("Autopilot has no actuator: no kured, no reachable Kubernetes API and no --rebootSSHKey. It still gates, holds and downgrades through targetVersion, but cannot force a reboot; hosts reboot when their own update timer sees rebootRequired")
	}
}
