package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

// Options configure a Controller. Fleet is required; Cluster and
// Actuators may be nil (no L2 gate, no forced reboots). Now is the clock.
type Options struct {
	Mode         string
	Fleet        Fleet
	Cluster      Cluster
	Actuators    Actuators
	StatePath    string
	HealthWindow time.Duration
	RetryAfter   time.Duration
	// Poll is how often Run reconciles (default 30 s).
	Poll time.Duration
	// IgnitionGrace is how long after L0 a missing Ignition fetch becomes
	// no-ignition (default 5 min); PendingGrace how long a pod may stay
	// Pending before it counts (default 5 min); NodeStable how long the
	// node must have been Ready without a Ready=False transition (default
	// 5 min, the plan's "last 5 min of the window"; negative disables).
	IgnitionGrace time.Duration
	PendingGrace  time.Duration
	NodeStable    time.Duration
	Now           func() time.Time
}

func (o *Options) defaults() {
	if o.Poll <= 0 {
		o.Poll = 30 * time.Second
	}
	if o.HealthWindow <= 0 {
		o.HealthWindow = config.DefaultAutopilotHealthWindow
	}
	if o.RetryAfter <= 0 {
		o.RetryAfter = config.DefaultAutopilotRetryAfter
	}
	if o.IgnitionGrace <= 0 {
		o.IgnitionGrace = 5 * time.Minute
	}
	if o.PendingGrace <= 0 {
		o.PendingGrace = 5 * time.Minute
	}
	if o.NodeStable == 0 {
		o.NodeStable = 5 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Controller is the autopilot state machine. All methods are safe for
// concurrent use; the HTTP handlers feed it signals while Run reconciles.
type Controller struct {
	opts Options

	mu       sync.Mutex
	state    *State
	actuator string
	dirty    bool
}

// New loads the persisted state (if any) and re-applies the fleet holds
// it recorded, so a restart resumes gates in progress with their stored
// t0. It starts no goroutine; call Run.
func New(opts Options) (*Controller, error) {
	if opts.Fleet == nil {
		return nil, errors.New("controller: Fleet is required")
	}
	opts.defaults()
	c := &Controller{opts: opts, state: newState(), actuator: ActuatorNone}
	if opts.StatePath != "" {
		if err := c.load(); err != nil {
			return nil, err
		}
	}
	for osName, st := range c.state.OS {
		if st.Held != "" {
			c.opts.Fleet.Hold(osName, st.Held)
		}
	}
	return c, nil
}

// Mode is the autopilot mode the controller runs in.
func (c *Controller) Mode() string { return c.opts.Mode }

func (c *Controller) full() bool { return c.opts.Mode == config.AutopilotFull }

func (c *Controller) now() time.Time { return c.opts.Now() }

func (c *Controller) load() error {
	data, err := os.ReadFile(c.opts.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading autopilot state %s: %w", c.opts.StatePath, err)
	}
	st := newState()
	if err := json.Unmarshal(data, st); err != nil {
		return fmt.Errorf("parsing autopilot state %s: %w", c.opts.StatePath, err)
	}
	if st.OS == nil {
		st.OS = map[string]*OSState{}
	}
	if st.Hosts == nil {
		st.Hosts = map[string]*HostState{}
	}
	if st.Reports == nil {
		st.Reports = map[string]*Report{}
	}
	c.state = st
	slog.Info("Autopilot state loaded", "path", c.opts.StatePath, "hosts", len(st.Hosts), "events", len(st.Events))
	return nil
}

func (c *Controller) save() {
	if c.opts.StatePath == "" || !c.dirty {
		return
	}
	c.state.Saved = c.now()
	data, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		slog.Error("Encoding autopilot state failed", "error", err)
		return
	}
	if err := config.WriteFileAtomic(c.opts.StatePath, append(data, '\n'), 0o644); err != nil {
		slog.Error("Writing autopilot state failed", "path", c.opts.StatePath, "error", err)
		return
	}
	c.dirty = false
}

// Run reconciles every Poll until ctx is done. It never returns an error:
// a broken cluster or fleet only produces events.
func (c *Controller) Run(ctx context.Context) {
	slog.Info("Autopilot controller started", "mode", c.opts.Mode, "healthWindow", c.opts.HealthWindow, "retryAfter", c.opts.RetryAfter, "poll", c.opts.Poll)
	c.Tick(ctx)
	t := time.NewTicker(c.opts.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			c.dirty = true
			c.save()
			c.mu.Unlock()
			slog.Info("Autopilot controller stopped")
			return
		case <-t.C:
			c.Tick(ctx)
		}
	}
}

// Tick is one reconcile pass: expire timers, poll the cluster for gates
// in progress, drive rollouts, advance lastGood, sync host summaries and
// persist.
func (c *Controller) Tick(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshActuator(ctx)
	c.reconcileReleases()
	c.evaluateGates(ctx)
	c.driveRollouts(ctx)
	c.advanceLastGood()
	c.applyHolds()
	c.syncHostSummaries()
	c.save()
}

func (c *Controller) refreshActuator(ctx context.Context) {
	if c.opts.Actuators == nil {
		c.actuator = ActuatorNone
		return
	}
	r, err := c.opts.Actuators.Choose(ctx)
	if err != nil {
		c.actuator = ActuatorNone
		return
	}
	c.actuator = r.Name()
}

// Actuator is the name of the actuator the last reconcile would use.
func (c *Controller) Actuator() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.actuator
}

func (c *Controller) event(kind, osName, release, mac, text string) {
	ev := Event{At: c.now(), Kind: kind, OS: osName, Release: release, MAC: mac, Text: text}
	c.state.Events = append(c.state.Events, ev)
	if n := len(c.state.Events); n > maxEvents {
		c.state.Events = slices.Clone(c.state.Events[n-maxEvents:])
	}
	c.dirty = true
	attrs := []any{"kind", kind, "os", osName, "release", release, "mac", mac}
	switch kind {
	case EventAlert:
		slog.Error("Autopilot: "+text, attrs...)
	default:
		slog.Info("Autopilot: "+text, attrs...)
	}
}

func (c *Controller) release(osName, version string) *Release {
	st := c.state.osState(osName)
	r := st.Releases[version]
	if r == nil {
		r = &Release{OS: osName, Version: version, State: ReleaseRolling, Since: c.now()}
		st.Releases[version] = r
		c.dirty = true
	}
	return r
}

func (c *Controller) setReleaseState(r *Release, state string) {
	if r.State == state {
		return
	}
	r.State, r.Since = state, c.now()
	c.dirty = true
}

// hostsOf lists the registered hosts of osName that take part in the
// autopilot: installed Bluefin hosts never netboot and are skipped.
func (c *Controller) hostsOf(osName string) []*hardware.Host {
	var out []*hardware.Host
	for _, h := range c.opts.Fleet.Hosts() {
		if hostOS(h) != osName || h.Installed() {
			continue
		}
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b *hardware.Host) int { return compareStrings(a.MAC, b.MAC) })
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (c *Controller) healthyOn(h *hardware.Host) string {
	hs := c.state.Hosts[h.MAC]
	if hs != nil && hs.HealthyOn != "" {
		return hs.HealthyOn
	}
	if h.Running != "" && slices.Contains(c.opts.Fleet.Cached(hostOS(h)), h.Running) {
		return h.Running
	}
	return ""
}

func (c *Controller) activeEpisode(mac string) *Episode {
	hs := c.state.Hosts[mac]
	if hs == nil || !hs.Episode.Active() {
		return nil
	}
	return hs.Episode
}

// pin writes the host's targetVersion and remembers the controller did.
func (c *Controller) pin(mac, version string) {
	err := c.opts.Fleet.Update(mac, func(h *hardware.Host) { h.TargetVersion = version })
	if err != nil {
		c.event(EventAlert, "", version, mac, "could not set the host's targetVersion: "+err.Error())
		return
	}
	c.state.hostState(mac).Pinned = true
	c.dirty = true
}

// unpin clears a targetVersion the controller set; operator pins are left.
func (c *Controller) unpin(mac string) {
	hs := c.state.Hosts[mac]
	if hs == nil || !hs.Pinned {
		return
	}
	err := c.opts.Fleet.Update(mac, func(h *hardware.Host) { h.TargetVersion = "" })
	if err != nil {
		c.event(EventAlert, "", "", mac, "could not clear the host's targetVersion: "+err.Error())
		return
	}
	hs.Pinned = false
	c.dirty = true
}

// syncHostSummaries mirrors each host's episode onto its hardware record
// (state, attempt, class, since) and writes only when it changed.
func (c *Controller) syncHostSummaries() {
	for _, h := range c.opts.Fleet.Hosts() {
		hs := c.state.Hosts[h.MAC]
		var want *hardware.HostAutopilot
		if hs != nil && (hs.Episode != nil || hs.Pinned) {
			want = &hardware.HostAutopilot{State: hardware.AutopilotIdle, Pinned: hs.Pinned}
			if e := hs.Episode; e != nil {
				want.State, want.Attempt, want.Class, want.Release, want.Target = e.State, e.Attempt, e.Class, e.Release, e.Target
				want.Since = e.Since.UTC().Format(time.RFC3339)
				if e.Done && e.State != hardware.AutopilotNeedsHands && e.State != hardware.AutopilotRolledBack {
					want.State = hardware.AutopilotIdle
				}
			}
		}
		if h.Autopilot.Equal(want) {
			continue
		}
		if err := c.opts.Fleet.Update(h.MAC, func(h *hardware.Host) { h.Autopilot = want }); err != nil {
			slog.Warn("Could not record autopilot summary on host", "mac", h.MAC, "error", err)
		}
	}
}

// applyHolds derives each OS's fleet-target hold from its releases and
// mode and applies it: held at lastGood while current is bad or a host is
// failing on it, held at the rollout candidate (or lastGood) for Bluefin
// under full until the whole fleet passed.
func (c *Controller) applyHolds() {
	for _, osName := range osNames() {
		st := c.state.osState(osName)
		want := c.desiredHold(osName)
		if want == st.Held {
			continue
		}
		st.Held, st.HeldSince = want, c.now()
		c.opts.Fleet.Hold(osName, want)
		c.dirty = true
		if want == "" {
			c.event(EventFleet, osName, "", "", "fleet target released; back to the current release")
		} else {
			c.event(EventFleet, osName, want, "", "fleet target held at "+want)
		}
	}
}

func (c *Controller) desiredHold(osName string) string {
	cur := c.opts.Fleet.Current(osName)
	lastGood := c.opts.Fleet.LastGood(osName)
	if cur == "" || lastGood == "" {
		return ""
	}
	st := c.state.osState(osName)
	if c.full() && osName == "bluefin" {
		if cur == lastGood {
			return ""
		}
		return lastGood
	}
	r := st.Releases[cur]
	if r != nil && (r.Bad() || r.Failing) && lastGood != cur {
		return lastGood
	}
	return ""
}

func osNames() []string { return []string{"flatcar", "coreos", "bluefin"} }

// reconcileReleases makes sure every current and lastGood release has a
// record, marks lastGood good and expires TIMEOUTs into their single retry.
func (c *Controller) reconcileReleases() {
	for _, osName := range osNames() {
		cur, lastGood := c.opts.Fleet.Current(osName), c.opts.Fleet.LastGood(osName)
		if cur == "" {
			continue
		}
		c.release(osName, cur)
		if lastGood != "" {
			lg := c.release(osName, lastGood)
			if !lg.Bad() {
				c.setReleaseState(lg, ReleaseGood)
			}
		}
		for _, r := range c.state.osState(osName).Releases {
			if r.State == ReleaseTimeout && !r.Retried && c.now().Sub(r.Since) >= c.opts.RetryAfter {
				c.startRetry(r)
			}
		}
	}
}

// Clear un-quarantines (or ends the TIMEOUT of) a release so the fleet
// tries it again.
func (c *Controller) Clear(osName, version string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state.OS[osName]
	if st == nil || st.Releases[version] == nil {
		return fmt.Errorf("%s %s is not a release the autopilot knows", osName, version)
	}
	r := st.Releases[version]
	if !r.Bad() {
		return fmt.Errorf("%s %s is %s, nothing to clear", osName, version, r.State)
	}
	c.setReleaseState(r, ReleaseRolling)
	r.Failing, r.Retried, r.FailedOn, r.Class = false, false, "", ""
	c.event(EventRelease, osName, version, "", "release cleared by the operator; the fleet may try it again")
	c.applyHolds()
	c.save()
	return nil
}

// RebootWanted reports whether the controller is waiting for mac to
// reboot into its current target (the kured actuator's whole mechanism:
// /update-check answers rebootRequired:true while this is so).
func (c *Controller) RebootWanted(mac string) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.activeEpisode(mac)
	if e == nil || !e.T0.IsZero() || e.RequestedAt.IsZero() {
		return false, ""
	}
	return true, fmt.Sprintf("autopilot: attempt %d into %s", e.Attempt, e.Target)
}
