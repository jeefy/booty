package power

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/hardware"
)

// Request is the body of POST /power/{mac}/{action}.
type Request struct {
	Reason string `json:"reason,omitempty"`
	Force  bool   `json:"force,omitempty"`
	// Drain defaults to true; false reboots or powers off without a cordon
	// and drain.
	Drain *bool `json:"drain,omitempty"`
}

func (r Request) drain() bool { return r.Drain == nil || *r.Drain }

// ErrConflict wraps every refusal: an action in flight, a guard, a state
// the action is not allowed from, or no actuator. The handler answers 409.
var ErrConflict = errors.New("conflict")

func conflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...))
}

// Conflict is the 409 message of err without the wrapper, "" otherwise.
func Conflict(err error) string {
	if !errors.Is(err, ErrConflict) {
		return ""
	}
	return strings.TrimPrefix(err.Error(), ErrConflict.Error()+": ")
}

// PowerOn sends the magic packet to a host that is off, unreachable or
// unknown. requestedBy is recorded on the host; it never enters an event.
func (t *Tracker) PowerOn(ctx context.Context, mac, requestedBy string, req Request) (*hardware.HostPower, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return nil, hardware.ErrNotFound
	}
	p := clonePower(h.Power)
	if p.InFlight() {
		if p.Request == hardware.PowerRequestOn {
			return p, nil
		}
		return nil, conflict("host is %s; wait for it or cancel", p.State)
	}
	switch p.State {
	case hardware.PowerOff, hardware.PowerUnreachable, hardware.PowerUnknown:
	default:
		return nil, conflict("host is %s; power on needs off, unreachable or unknown", p.State)
	}
	now := t.now()
	t.set(p, hardware.PowerPoweringOn, reasonOr(req.Reason, "magic packet sent"), now)
	p.Request, p.RequestedBy, p.RequestedAt = hardware.PowerRequestOn, requestedBy, stamp(now)
	t.write(mac, p)
	t.event(mac, "power on requested: sending the magic packet")
	token := t.bump(mac)
	ip := h.IP
	t.spawn(func() {
		wctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		dests, err := t.opts.Waker.Wake(wctx, mac, ip)
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.tokens[mac] != token {
			return
		}
		if err != nil {
			t.event(mac, "magic packet could not be sent: "+err.Error())
			return
		}
		t.event(mac, fmt.Sprintf("magic packet sent to %d destination(s), %d rounds", len(dests), t.opts.Waker.rounds()))
	})
	return p, nil
}

func reasonOr(reason, fallback string) string {
	if r := strings.TrimSpace(reason); r != "" {
		return r
	}
	return fallback
}

func (t *Tracker) bump(mac string) int {
	t.tokens[mac]++
	return t.tokens[mac]
}

// Reboot drains (when asked and a cluster is reachable) and reboots an up
// host through the operator actuator.
func (t *Tracker) Reboot(ctx context.Context, mac, requestedBy string, req Request) (*hardware.HostPower, error) {
	return t.takeDown(ctx, mac, requestedBy, req, hardware.PowerRequestReboot)
}

// Shutdown drains and powers an up host off; the node stays cordoned until
// the host reports /booted again.
func (t *Tracker) Shutdown(ctx context.Context, mac, requestedBy string, req Request) (*hardware.HostPower, error) {
	return t.takeDown(ctx, mac, requestedBy, req, hardware.PowerRequestShutdown)
}

func verb(action string) string {
	if action == hardware.PowerRequestShutdown {
		return "shut down"
	}
	return "reboot"
}

func (t *Tracker) takeDown(ctx context.Context, mac, requestedBy string, req Request, action string) (*hardware.HostPower, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return nil, hardware.ErrNotFound
	}
	p := clonePower(h.Power)
	if p.InFlight() {
		if p.Request == action {
			return p, nil
		}
		if p.RequestedBy == RequestedByAutopilot {
			return nil, conflict("autopilot is driving this host")
		}
		return nil, conflict("host is %s; wait for it or cancel", p.State)
	}
	if p.State != hardware.PowerUp {
		return nil, conflict("host is %s; %s needs up", p.State, verb(action))
	}
	if t.opts.Autopilot != nil && t.opts.Autopilot.Driving(mac) {
		return nil, conflict("autopilot is driving this host")
	}
	if h.IsControlPlane() && !req.Force {
		return nil, conflict("control-plane host; set force to %s it anyway", verb(action))
	}
	if action == hardware.PowerRequestShutdown && !req.Force && t.lastWorker(ctx, h) {
		return nil, conflict("last Ready worker; set force to shut it down anyway")
	}
	if t.opts.Actuators == nil {
		return nil, conflict("%s", actuator.ErrNoOperatorActuator.Error())
	}
	r, err := t.opts.Actuators.Operator(ctx)
	if err != nil {
		return nil, conflict("%s", err.Error())
	}
	drains := req.drain() && t.opts.Cluster != nil && t.opts.Cluster.Configured()
	now := t.now()
	state := hardware.PowerRebooting
	if action == hardware.PowerRequestShutdown {
		state = hardware.PowerShuttingDown
	}
	if drains {
		t.set(p, hardware.PowerDraining, reasonOr(req.Reason, verb(action)+" requested"), now)
	} else {
		t.set(p, state, reasonOr(req.Reason, verb(action)+" requested"), now)
	}
	p.Request, p.RequestedBy, p.RequestedAt = action, requestedBy, stamp(now)
	t.write(mac, p)
	t.event(mac, fmt.Sprintf("%s requested through the %s actuator (drain: %v, force: %v)", verb(action), r.Name(), drains, req.Force))
	token := t.bump(mac)
	target := actuator.FromHardware(h)
	t.spawn(func() { t.act(r, target, mac, action, state, token, drains) })
	return p, nil
}

func (t *Tracker) act(r actuator.PowerActuator, host actuator.Host, mac, action, state string, token int, drains bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if drains {
		if err := r.Prepare(ctx, host); err != nil {
			t.actFailed(r, host, mac, token, "drain failed: "+err.Error(), true)
			return
		}
		if !t.advance(mac, token, func(p *hardware.HostPower) {
			p.Cordoned = true
			t.cordoned[mac] = t.now()
			t.set(p, state, p.Reason, t.now())
		}) {
			return
		}
	}
	var err error
	if action == hardware.PowerRequestShutdown {
		err = r.PowerOff(ctx, host)
	} else {
		err = r.Reboot(ctx, host)
	}
	if err != nil {
		t.actFailed(r, host, mac, token, r.Name()+" actuator failed: "+err.Error(), drains)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tokens[mac] != token {
		return
	}
	t.event(mac, verb(action)+" under way through the "+r.Name()+" actuator")
}

// advance applies fn to the host's power block when the action is still
// the current one.
func (t *Tracker) advance(mac string, token int, fn func(*hardware.HostPower)) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tokens[mac] != token {
		return false
	}
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return false
	}
	p := clonePower(h.Power)
	fn(p)
	t.write(mac, p)
	return true
}

func (t *Tracker) actFailed(r actuator.PowerActuator, host actuator.Host, mac string, token int, text string, uncordon bool) {
	if uncordon {
		fctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := r.Finish(fctx, host); err != nil {
			slog.Warn("Uncordon after a failed power action failed", "mac", mac, "error", err)
		}
		cancel()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tokens[mac] != token {
		return
	}
	t.event(mac, text+"; the host is left as it was")
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return
	}
	p := clonePower(h.Power)
	t.clearRequest(p)
	p.Cordoned = false
	t.set(p, hardware.PowerUp, text, t.now())
	t.write(mac, p)
}

// lastWorker reports whether taking h down would leave the cluster without
// a Ready, schedulable worker: counted from the nodes when a cluster is
// reachable, else from the registered workers that are up.
func (t *Tracker) lastWorker(ctx context.Context, h *hardware.Host) bool {
	if h.IsControlPlane() {
		return false
	}
	if t.opts.Cluster != nil && t.opts.Cluster.Configured() {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if nodes, err := t.opts.Cluster.Nodes(cctx); err == nil {
			ready, mine := 0, false
			for _, n := range nodes {
				if !n.Ready || n.ControlPlane || n.Unschedulable {
					continue
				}
				ready++
				if n.Name == h.Hostname {
					mine = true
				}
			}
			return mine && ready <= 1
		}
	}
	up := 0
	for _, other := range t.opts.Fleet.Hosts() {
		if !other.IsControlPlane() && other.PowerState() == hardware.PowerUp {
			up++
		}
	}
	return up <= 1
}

// Cancel drops a stuck expectation and recomputes the state from the last
// probe and lastSeen. A background action that is still running finishes
// without touching the state afterwards.
func (t *Tracker) Cancel(mac string) (*hardware.HostPower, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return nil, hardware.ErrNotFound
	}
	p := clonePower(h.Power)
	if !p.InFlight() {
		return p, nil
	}
	t.bump(mac)
	was := p.State
	t.clearRequest(p)
	now := t.now()
	var probe *probeResult
	if p.Probe.At != "" {
		probe = &probeResult{ok: p.Probe.OK, method: p.Probe.Method}
	}
	p.State = hardware.PowerUnknown
	t.settle(p, probe, now, "cancelled "+was)
	t.write(mac, p)
	t.event(mac, "operator cancelled the "+was+" expectation")
	return p, nil
}
