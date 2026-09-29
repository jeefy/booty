// Package actuator is how the autopilot makes a host reboot: through
// kured's sentinel when kured runs in the cluster, through the Kubernetes
// API (cordon, evict, a privileged reboot Pod) when Booty can reach it,
// or over SSH. Choose picks one in that order. Every Rebooter is a plain
// value that does nothing until Prepare/Reboot/Finish are called; the
// controller (plan P3) is the only caller, P2 only reports which one
// would be used.
package actuator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/hardware"
)

// Host is what an actuator needs to know about the machine to reboot.
type Host struct {
	// Hostname is the node name too: both OS paths set it from Booty.
	Hostname string
	OS       string
	IP       string
	MAC      string
	// ProductUUID is the host's DMI product UUID from its last health
	// report (empty when unknown); Resolve compares it with the node's
	// systemUUID before trusting the hostname match.
	ProductUUID string
}

// FromHardware builds a Host from a registered host.
func FromHardware(h *hardware.Host) Host {
	host := Host{Hostname: h.Hostname, OS: h.OS, IP: h.IP, MAC: h.MAC}
	if h.Health != nil {
		host.ProductUUID = h.Health.DMI.ProductUUID
	}
	return host
}

// Rebooter drains, reboots and restores a host. Prepare and Finish may be
// no-ops (kured does both itself); Reboot must return once the reboot is
// under way, not once the host is back.
type Rebooter interface {
	Name() string
	Prepare(ctx context.Context, host Host) error
	Reboot(ctx context.Context, host Host) error
	Finish(ctx context.Context, host Host) error
}

// Names of the actuators, as reported by Name and GET /autopilot.
const (
	NameKured = "kured"
	NameAPI   = "api"
	NameSSH   = "ssh"
	NameNone  = "none"
)

// ErrNoActuator is returned by Choose when no way to reboot exists: no
// reachable cluster and no SSH key.
var ErrNoActuator = errors.New("no reboot actuator available: no kured, no reachable Kubernetes API and no --rebootSSHKey")

// ErrNodeMismatch is returned by Resolve when the node named after the
// host reports a different system UUID than the host's health report.
var ErrNodeMismatch = errors.New("node system UUID does not match the host's DMI product UUID")

// Resolve finds the cluster node for host: the node named like the host,
// confirmed against the host's product UUID when both are known. It is
// read-only.
func Resolve(ctx context.Context, client *k8s.Client, host Host) (k8s.Node, error) {
	if host.Hostname == "" {
		return k8s.Node{}, errors.New("host has no hostname, so no node name")
	}
	node, err := client.Node(ctx, host.Hostname)
	if err != nil {
		return k8s.Node{}, err
	}
	if host.ProductUUID != "" && node.NodeInfo.SystemUUID != "" && !strings.EqualFold(host.ProductUUID, node.NodeInfo.SystemUUID) {
		return k8s.Node{}, fmt.Errorf("%w: node %s has %s, host %s reported %s", ErrNodeMismatch, node.Name, node.NodeInfo.SystemUUID, host.MAC, host.ProductUUID)
	}
	return node, nil
}

// Options configure Choose.
type Options struct {
	// Client is nil when Booty has no cluster (no in-cluster environment
	// and no --kubeconfig).
	Client *k8s.Client
	// Namespace and Image are where and from what the API actuator runs
	// its reboot Pods.
	Namespace string
	Image     string
	// DrainTimeout bounds PodDisruptionBudget retries during an API drain.
	DrainTimeout time.Duration
	// SSHKeyPath is --rebootSSHKey; empty disables the SSH actuator.
	SSHKeyPath string
	// KnownHosts is where the SSH actuator pins host keys.
	KnownHosts string
	// KuredTTL is how long a kured detection is trusted (default 5 min).
	KuredTTL time.Duration
	Now      func() time.Time
}

// Chooser applies the plan's order at each use: kured if present (cached
// for KuredTTL), else the API when reachable, else SSH when a key is
// configured.
type Chooser struct {
	opts Options

	mu           sync.Mutex
	kured        bool
	kuredChecked time.Time
	kuredErr     error
}

// NewChooser returns a Chooser; Options.Client may be nil.
func NewChooser(opts Options) *Chooser {
	if opts.KuredTTL <= 0 {
		opts.KuredTTL = 5 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Chooser{opts: opts}
}

// Options returns what the chooser was built with.
func (c *Chooser) Options() Options { return c.opts }

// failureTTL is how long an API failure is remembered before kured is
// looked up again, so a dead API server costs one timeout per half minute
// rather than one per call.
const failureTTL = 30 * time.Second

// Kured reports whether kured runs in the cluster, asking the API at most
// once per KuredTTL. Without a client it is false with no error; an API
// failure is returned and remembered for failureTTL.
func (c *Chooser) Kured(ctx context.Context) (bool, error) {
	if c.opts.Client == nil || !c.opts.Client.Configured() {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.opts.Now()
	if !c.kuredChecked.IsZero() {
		switch age := now.Sub(c.kuredChecked); {
		case c.kuredErr != nil && age < failureTTL:
			return false, c.kuredErr
		case c.kuredErr == nil && age < c.opts.KuredTTL:
			return c.kured, nil
		}
	}
	present, err := c.opts.Client.KuredPresent(ctx)
	c.kured, c.kuredChecked, c.kuredErr = present, now, err
	return present, err
}

func (c *Chooser) recentFailure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kuredErr != nil && c.opts.Now().Sub(c.kuredChecked) < failureTTL {
		return c.kuredErr
	}
	return nil
}

func (c *Chooser) noteFailure(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kured, c.kuredChecked, c.kuredErr = false, c.opts.Now(), err
}

// Choose picks the actuator for this moment. The cluster is "reachable"
// when the kured lookup succeeded; a client that answers 403 still counts
// as reachable for the API actuator (its own calls will say what RBAC is
// missing), a transport failure does not.
func (c *Chooser) Choose(ctx context.Context) (Rebooter, error) {
	present, err := c.Kured(ctx)
	if err != nil && !k8s.IsUnreachable(err) && !k8s.IsForbidden(err) {
		return nil, err
	}
	switch {
	case present:
		return Kured{}, nil
	case c.opts.Client != nil && c.opts.Client.Configured() && !k8s.IsUnreachable(err):
		return &API{Client: c.opts.Client, Namespace: c.opts.Namespace, Image: c.opts.Image, DrainTimeout: c.opts.DrainTimeout}, nil
	case c.opts.SSHKeyPath != "":
		var drain *k8s.Client
		if c.opts.Client != nil && c.opts.Client.Configured() && !k8s.IsUnreachable(err) {
			drain = c.opts.Client
		}
		return &SSH{KeyPath: c.opts.SSHKeyPath, KnownHosts: c.opts.KnownHosts, Drain: drain, DrainTimeout: c.opts.DrainTimeout}, nil
	}
	return nil, ErrNoActuator
}

// Status is the dry-run view of the chooser: what GET /autopilot reports
// and what start-up logs.
type Status struct {
	Reachable bool   `json:"reachable"`
	Kured     bool   `json:"kured"`
	Nodes     int    `json:"nodes"`
	APIServer string `json:"apiServer,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Inspect reads the cluster (nodes and DaemonSets, both GETs) and names
// the actuator Choose would return. It never calls Prepare, Reboot or
// Finish.
func (c *Chooser) Inspect(ctx context.Context) (Status, string) {
	var st Status
	client := c.opts.Client
	if client != nil && client.Configured() {
		st.APIServer = client.APIServer()
		var nodes []k8s.Node
		err := c.recentFailure()
		if err == nil {
			nodes, err = client.Nodes(ctx)
		}
		switch {
		case err == nil:
			st.Reachable, st.Nodes = true, len(nodes)
		case k8s.IsForbidden(err):
			st.Reachable, st.Error = true, err.Error()
		default:
			st.Error = err.Error()
			c.noteFailure(err)
		}
		if kured, err := c.Kured(ctx); err == nil {
			st.Kured = kured
		} else if st.Error == "" {
			st.Error = err.Error()
		}
	}
	r, err := c.Choose(ctx)
	if err != nil {
		if st.Error == "" {
			st.Error = err.Error()
		}
		slog.Debug("No actuator", "error", err)
		return st, NameNone
	}
	return st, r.Name()
}
