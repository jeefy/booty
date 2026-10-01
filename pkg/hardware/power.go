package hardware

// HostPower is the power tracker's view of a host (plan
// docs/plans/2026-09-30-power.md): the derived state and since when, the
// action in flight (or the satisfied shutdown while the host is off), who
// asked for it, the last moment a Booty-side signal proved the OS up,
// whether Booty cordoned the node and owes it an uncordon, and the last
// active probe. pkg/power owns it; /register echoes it back unchanged.
type HostPower struct {
	State string `json:"state"`
	Since string `json:"since,omitempty"`
	// Reason is the UI tooltip: why the state is what it is. It never
	// names another host.
	Reason string `json:"reason,omitempty"`
	// Request is PowerRequestOn, PowerRequestReboot or PowerRequestShutdown
	// while an operator action is in flight; a shutdown stays recorded
	// while the host is off so the autopilot keeps excluding it.
	Request     string `json:"request,omitempty"`
	RequestedBy string `json:"requestedBy,omitempty"`
	RequestedAt string `json:"requestedAt,omitempty"`
	LastSeen    string `json:"lastSeen,omitempty"`
	Cordoned    bool   `json:"cordoned,omitempty"`
	Probe       Probe  `json:"probe"`
}

// Probe is the last active probe of a host: whether a TCP connect to its
// IP succeeded, when, and on which port ("tcp/22", "tcp/10250").
type Probe struct {
	OK     bool   `json:"ok"`
	At     string `json:"at,omitempty"`
	Method string `json:"method,omitempty"`
}

// Power states, as the plan names them.
const (
	PowerUnknown      = "unknown"
	PowerOff          = "off"
	PowerPoweringOn   = "powering-on"
	PowerBooting      = "booting"
	PowerUp           = "up"
	PowerDraining     = "draining"
	PowerRebooting    = "rebooting"
	PowerShuttingDown = "shutting-down"
	PowerUnreachable  = "unreachable"
)

// Operator power requests.
const (
	PowerRequestOn       = "on"
	PowerRequestReboot   = "reboot"
	PowerRequestShutdown = "shutdown"
)

// PowerState is the host's power state, PowerUnknown when the tracker has
// not recorded one.
func (h *Host) PowerState() string {
	if h == nil || h.Power == nil || h.Power.State == "" {
		return PowerUnknown
	}
	return h.Power.State
}

// PoweredOffByRequest reports whether an operator shut the host down and
// it has not come back: the autopilot leaves such a host alone (no
// episode, no hung, not counted for lastGood).
func (h *Host) PoweredOffByRequest() bool {
	if h == nil || h.Power == nil || h.Power.Request != PowerRequestShutdown {
		return false
	}
	switch h.Power.State {
	case PowerShuttingDown, PowerOff, PowerDraining:
		return true
	}
	return false
}

// Equal reports whether two power blocks carry the same values.
func (p *HostPower) Equal(q *HostPower) bool {
	if p == nil || q == nil {
		return p == q
	}
	return *p == *q
}

// InFlight reports whether a power transition is under way (requested by
// an operator or the autopilot), as opposed to a settled state.
func (p *HostPower) InFlight() bool {
	if p == nil {
		return false
	}
	switch p.State {
	case PowerPoweringOn, PowerDraining, PowerRebooting, PowerShuttingDown:
		return true
	}
	return false
}
