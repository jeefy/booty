package power

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/hardware"
)

// Fleet is what the tracker reads and writes about hosts; the real one
// wraps pkg/hardware, tests use an in-memory one.
type Fleet interface {
	Hosts() []*hardware.Host
	Host(mac string) (*hardware.Host, bool)
	Update(mac string, fn func(*hardware.Host)) error
}

// Cluster is the read side of the k8s client the tracker uses: Node Ready
// counts as a sign of life, and the last-worker guard counts Ready
// workers. nil means no cluster.
type Cluster interface {
	Configured() bool
	Nodes(ctx context.Context) ([]k8s.Node, error)
}

// Autopilot is what the tracker asks the controller: whether it is
// waiting for a host to reboot (shown as rebooting), whether it is driving
// a host (operator actions are refused), and where to mirror power events.
type Autopilot interface {
	RebootWanted(mac string) (bool, string)
	Driving(mac string) bool
	Event(kind, mac, text string)
}

// Actuators picks the operator actuator; *actuator.Chooser implements it.
type Actuators interface {
	Operator(ctx context.Context) (actuator.PowerActuator, error)
	OperatorName(ctx context.Context) string
}

// Probe reports whether the host at ip answers, and how ("tcp/22").
type Probe func(ctx context.Context, ip string) (ok bool, method string)

// Options configure a Tracker. Fleet is required; everything else may be
// nil or zero.
type Options struct {
	Fleet     Fleet
	Cluster   Cluster
	Autopilot Autopilot
	Actuators Actuators
	Waker     *Waker
	Probe     Probe
	Now       func() time.Time
	// Interval is how often Run ticks (default 30 s).
	Interval time.Duration
}

// Timing of the state machine (docs/plans/2026-09-30-power.md).
const (
	OnWindow       = 10 * time.Minute
	RebootWindow   = 10 * time.Minute
	ShutdownWindow = 5 * time.Minute
	StaleAfter     = 15 * time.Minute
	UncordonGiveUp = 15 * time.Minute
	ProbeTimeout   = 3 * time.Second
	maxEvents      = 200
)

// EventKind is the kind of every power event, in the tracker's ring and
// the autopilot's.
const EventKind = "power"

// ProbePorts are tried in order by TCPProbe.
var ProbePorts = []int{22, 10250}

// TCPProbe connects to ip on ProbePorts in turn with ProbeTimeout each.
func TCPProbe(ctx context.Context, ip string) (bool, string) {
	d := net.Dialer{Timeout: ProbeTimeout}
	for _, port := range ProbePorts {
		method := "tcp/" + strconv.Itoa(port)
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
			return true, method
		}
		if ctx.Err() != nil {
			return false, method
		}
	}
	return false, "tcp/" + strconv.Itoa(ProbePorts[len(ProbePorts)-1])
}

// Event is one line of the power timeline. Text never carries hostnames,
// IPs or MACs; the MAC is a field.
type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	MAC  string    `json:"mac,omitempty"`
	Text string    `json:"text"`
}

// Tracker derives every host's power state and runs the operator actions.
// All methods are safe for concurrent use.
type Tracker struct {
	opts Options

	mu       sync.Mutex
	events   []Event
	tokens   map[string]int
	warned   map[string]bool
	cordoned map[string]time.Time
	bg       sync.WaitGroup
}

// New builds a Tracker. It starts no goroutine; call Run.
func New(opts Options) (*Tracker, error) {
	if opts.Fleet == nil {
		return nil, fmt.Errorf("power: Fleet is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Probe == nil {
		opts.Probe = TCPProbe
	}
	if opts.Waker == nil {
		opts.Waker = &Waker{}
	}
	return &Tracker{opts: opts, tokens: map[string]int{}, warned: map[string]bool{}, cordoned: map[string]time.Time{}}, nil
}

// Wait blocks until the background work (actuator calls, uncordons, magic
// packets) has finished.
func (t *Tracker) Wait() { t.bg.Wait() }

func (t *Tracker) spawn(f func()) {
	t.bg.Add(1)
	go func() {
		defer t.bg.Done()
		f()
	}()
}

func (t *Tracker) now() time.Time { return t.opts.Now() }

func stamp(at time.Time) string { return at.UTC().Format(time.RFC3339) }

func parseStamp(s string) time.Time {
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return at
}

// Run ticks every Interval until ctx is done.
func (t *Tracker) Run(ctx context.Context) {
	slog.Info("Power tracker started", "interval", t.opts.Interval, "probePorts", ProbePorts)
	t.Tick(ctx)
	ticker := time.NewTicker(t.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Power tracker stopped")
			return
		case <-ticker.C:
			t.Tick(ctx)
		}
	}
}

type probeResult struct {
	ok     bool
	method string
}

// Tick probes every host with an IP, reads the cluster's node readiness
// and recomputes each host's state.
func (t *Tracker) Tick(ctx context.Context) {
	hosts := t.opts.Fleet.Hosts()
	ready := t.readyNodes(ctx)
	probes := t.probeAll(ctx, hosts)
	for _, h := range hosts {
		t.derive(h.MAC, probes[h.MAC], ready[h.Hostname])
	}
}

func (t *Tracker) readyNodes(ctx context.Context) map[string]bool {
	if t.opts.Cluster == nil || !t.opts.Cluster.Configured() {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	nodes, err := t.opts.Cluster.Nodes(cctx)
	if err != nil {
		slog.Debug("Power tracker: node listing failed", "error", err)
		return nil
	}
	out := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		out[n.Name] = n.Ready
	}
	return out
}

func (t *Tracker) probeAll(ctx context.Context, hosts []*hardware.Host) map[string]*probeResult {
	out := map[string]*probeResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, h := range hosts {
		if h.IP == "" || net.ParseIP(h.IP) == nil {
			continue
		}
		wg.Add(1)
		go func(mac, ip string) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, ProbeTimeout*time.Duration(len(ProbePorts))+time.Second)
			defer cancel()
			ok, method := t.opts.Probe(pctx, ip)
			mu.Lock()
			out[mac] = &probeResult{ok: ok, method: method}
			mu.Unlock()
		}(h.MAC, h.IP)
	}
	wg.Wait()
	return out
}

// derive is one host's share of a tick.
func (t *Tracker) derive(mac string, probe *probeResult, nodeReady bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return
	}
	now := t.now()
	old := h.Power
	p := clonePower(old)
	if nodeReady {
		p.LastSeen = stamp(now)
	}
	if probe != nil {
		p.Probe = hardware.Probe{OK: probe.ok, At: stamp(now), Method: probe.method}
		if probe.ok {
			p.LastSeen = stamp(now)
		}
	}
	var events []string
	events = append(events, t.applyAutopilot(mac, p, now)...)
	events = append(events, t.step(p, probe, now)...)
	events = append(events, t.checkCordon(mac, p, now)...)
	if !powerChanged(old, p) {
		return
	}
	t.write(mac, p)
	for _, text := range events {
		t.event(mac, text)
	}
}

func clonePower(p *hardware.HostPower) *hardware.HostPower {
	if p == nil {
		return &hardware.HostPower{State: hardware.PowerUnknown}
	}
	c := *p
	if c.State == "" {
		c.State = hardware.PowerUnknown
	}
	return &c
}

// powerChanged says whether a derived block is worth persisting: any
// field but the probe time and lastSeen, or those moved by a minute.
func powerChanged(old, p *hardware.HostPower) bool {
	if old == nil {
		return true
	}
	a, b := *old, *p
	a.Probe.At, b.Probe.At, a.LastSeen, b.LastSeen = "", "", "", ""
	if a != b {
		return true
	}
	moved := func(x, y string) bool {
		if x == y {
			return false
		}
		if x == "" || y == "" {
			return true
		}
		return parseStamp(y).Sub(parseStamp(x)).Abs() >= time.Minute
	}
	return moved(old.Probe.At, p.Probe.At) || moved(old.LastSeen, p.LastSeen)
}

// RequestedByAutopilot marks a rebooting state the controller asked for.
const RequestedByAutopilot = "autopilot"

func (t *Tracker) applyAutopilot(mac string, p *hardware.HostPower, now time.Time) []string {
	if t.opts.Autopilot == nil || p.Request != "" {
		return nil
	}
	want, why := t.opts.Autopilot.RebootWanted(mac)
	switch {
	case want && p.State != hardware.PowerRebooting && p.State != hardware.PowerBooting:
		t.set(p, hardware.PowerRebooting, why, now)
		p.RequestedBy, p.RequestedAt = RequestedByAutopilot, stamp(now)
		return []string{"reboot requested by the autopilot (" + why + ")"}
	case !want && p.State == hardware.PowerRebooting && p.RequestedBy == RequestedByAutopilot:
		t.clearRequest(p)
		t.settle(p, nil, now, "the autopilot stopped waiting for the reboot")
	}
	return nil
}

func (t *Tracker) set(p *hardware.HostPower, state, reason string, now time.Time) {
	if p.State != state {
		p.Since = stamp(now)
	}
	p.State, p.Reason = state, reason
}

func (t *Tracker) clearRequest(p *hardware.HostPower) {
	p.Request, p.RequestedBy, p.RequestedAt = "", "", ""
}

func (t *Tracker) expired(p *hardware.HostPower, window time.Duration, now time.Time) bool {
	since := parseStamp(p.RequestedAt)
	if since.IsZero() {
		since = parseStamp(p.Since)
	}
	return !since.IsZero() && now.Sub(since) >= window
}

func seenAgo(p *hardware.HostPower, now time.Time) (time.Duration, bool) {
	at := parseStamp(p.LastSeen)
	if at.IsZero() {
		return 0, false
	}
	return now.Sub(at), true
}

// step applies the probe to the current state (the table in the plan).
func (t *Tracker) step(p *hardware.HostPower, probe *probeResult, now time.Time) []string {
	switch p.State {
	case hardware.PowerDraining:
		return nil
	case hardware.PowerPoweringOn:
		switch {
		case probe != nil && probe.ok:
			t.set(p, hardware.PowerUp, "answered the probe after the wake-up ("+probe.method+")", now)
			t.clearRequest(p)
			return []string{"host answers after the magic packet; no boot was seen, so it was only unreachable"}
		case t.expired(p, OnWindow, now):
			t.set(p, hardware.PowerOff, "did not wake within "+OnWindow.String(), now)
			t.clearRequest(p)
			return []string{"did not wake: no kernel fetch and no answer " + OnWindow.String() + " after the magic packet"}
		}
		return nil
	case hardware.PowerRebooting:
		if p.Request == "" || !t.expired(p, RebootWindow, now) {
			return nil
		}
		t.clearRequest(p)
		switch {
		case probe != nil && probe.ok:
			t.set(p, hardware.PowerUp, "reboot was not observed; the host answers", now)
			return []string{"reboot was not observed within " + RebootWindow.String() + "; the host still answers"}
		case probe != nil:
			t.set(p, hardware.PowerUnreachable, "reboot requested "+RebootWindow.String()+" ago; nothing fetched and no answer", now)
			return []string{"reboot requested " + RebootWindow.String() + " ago; nothing fetched and the host does not answer"}
		}
		t.set(p, hardware.PowerUnknown, "reboot requested; host has no IP to probe", now)
		return []string{"reboot was not observed within " + RebootWindow.String() + " and the host has no IP to probe"}
	case hardware.PowerShuttingDown:
		switch {
		case probe != nil && !probe.ok:
			t.set(p, hardware.PowerOff, "shutdown requested; the host stopped answering", now)
			return []string{"host is off: it stopped answering after the shutdown"}
		case !t.expired(p, ShutdownWindow, now):
			return nil
		case probe != nil:
			t.set(p, hardware.PowerUp, "did not shut down within "+ShutdownWindow.String(), now)
			t.clearRequest(p)
			return []string{"did not shut down: still answering " + ShutdownWindow.String() + " after the request"}
		}
		t.set(p, hardware.PowerUnknown, "shutdown requested; host has no IP to confirm it", now)
		t.clearRequest(p)
		return []string{"cannot confirm the shutdown: the host has no IP to probe"}
	case hardware.PowerOff:
		if probe != nil && probe.ok {
			t.set(p, hardware.PowerUp, "answered the probe ("+probe.method+")", now)
			t.clearRequest(p)
			return []string{"host came back without a request"}
		}
		return nil
	case hardware.PowerBooting:
		if probe != nil && probe.ok {
			t.set(p, hardware.PowerUp, "answered the probe while booting ("+probe.method+")", now)
			return nil
		}
		if ago, seen := seenAgo(p, now); seen && ago < StaleAfter {
			return nil
		}
		if since := parseStamp(p.Since); !since.IsZero() && now.Sub(since) < StaleAfter {
			return nil
		}
		if probe != nil {
			t.set(p, hardware.PowerUnreachable, "boot was seen but nothing since, and the host does not answer", now)
			return []string{"booting host went quiet for " + StaleAfter.String() + " and does not answer"}
		}
		t.set(p, hardware.PowerUnknown, "boot was seen but nothing since; no IP to probe", now)
		return nil
	}
	return t.settle(p, probe, now, "")
}

// settle derives up/unreachable/unknown from the probe and lastSeen.
func (t *Tracker) settle(p *hardware.HostPower, probe *probeResult, now time.Time, note string) []string {
	ago, seen := seenAgo(p, now)
	prefix := ""
	if note != "" {
		prefix = note + "; "
	}
	switch {
	case probe != nil && probe.ok:
		t.set(p, hardware.PowerUp, prefix+"answers on "+probe.method, now)
	case probe != nil && seen && ago < StaleAfter:
		if p.State != hardware.PowerUp {
			t.set(p, hardware.PowerUp, prefix+"no answer on the probe but seen recently", now)
		}
	case probe != nil:
		if p.State != hardware.PowerUnreachable {
			t.set(p, hardware.PowerUnreachable, prefix+"no answer on "+portsText()+" and nothing seen for "+staleText(ago, seen), now)
			return []string{"host is unreachable: no answer on " + portsText() + " and nothing seen for " + staleText(ago, seen)}
		}
	case seen && ago < StaleAfter:
		if p.State != hardware.PowerUp {
			t.set(p, hardware.PowerUp, prefix+"seen recently (no IP to probe)", now)
		}
	default:
		if p.State != hardware.PowerUnknown {
			t.set(p, hardware.PowerUnknown, prefix+"no IP to probe and nothing seen for "+staleText(ago, seen), now)
		}
	}
	return nil
}

func portsText() string {
	parts := make([]string, len(ProbePorts))
	for i, port := range ProbePorts {
		parts[i] = strconv.Itoa(port)
	}
	return "tcp " + strings.Join(parts, "/")
}

func staleText(ago time.Duration, seen bool) string {
	if !seen {
		return "ever"
	}
	return ago.Round(time.Minute).String()
}

// checkCordon says once when a rebooted node has not reported /booted
// within UncordonGiveUp: it stays cordoned until it does.
func (t *Tracker) checkCordon(mac string, p *hardware.HostPower, now time.Time) []string {
	if !p.Cordoned || p.State == hardware.PowerUp || t.warned[mac] || p.Request == hardware.PowerRequestShutdown {
		return nil
	}
	at, ok := t.cordoned[mac]
	if !ok {
		at = parseStamp(p.RequestedAt)
	}
	if at.IsZero() {
		at = parseStamp(p.Since)
	}
	if at.IsZero() || now.Sub(at) < UncordonGiveUp {
		return nil
	}
	t.warned[mac] = true
	return []string{"no /booted within " + UncordonGiveUp.String() + " of the reboot; the node is left cordoned until it reports in"}
}

func (t *Tracker) write(mac string, p *hardware.HostPower) {
	if err := t.opts.Fleet.Update(mac, func(h *hardware.Host) { c := *p; h.Power = &c }); err != nil {
		slog.Warn("Could not record power state", "mac", mac, "error", err)
	}
}

// event appends to the ring and mirrors to the autopilot when one runs.
// Caller holds t.mu.
func (t *Tracker) event(mac, text string) {
	ev := Event{At: t.now(), Kind: EventKind, MAC: mac, Text: text}
	t.events = append(t.events, ev)
	if n := len(t.events); n > maxEvents {
		t.events = slices.Clone(t.events[n-maxEvents:])
	}
	slog.Info("Power: "+text, "mac", mac)
	if t.opts.Autopilot != nil {
		t.opts.Autopilot.Event(EventKind, mac, text)
	}
}

// ObserveFetch is a kernel/UKI (FetchKernel) or Ignition (FetchIgnition)
// fetch: the host is booting under its own power, so any wake-up, reboot
// or satisfied shutdown request is resolved.
func (t *Tracker) ObserveFetch(mac, kind string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return
	}
	now := t.now()
	p := clonePower(h.Power)
	p.LastSeen = stamp(now)
	var text string
	switch kind {
	case FetchKernel:
		switch p.State {
		case hardware.PowerPoweringOn:
			text = "woke up: kernel fetched after the magic packet"
		case hardware.PowerRebooting:
			text = "reboot observed: kernel fetched"
		case hardware.PowerOff, hardware.PowerShuttingDown:
			text = "host is booting again"
		}
		t.set(p, hardware.PowerBooting, "kernel fetched", now)
		t.clearRequest(p)
	default:
		switch p.State {
		case hardware.PowerUnknown, hardware.PowerOff, hardware.PowerPoweringOn, hardware.PowerRebooting, hardware.PowerUnreachable:
			t.set(p, hardware.PowerBooting, "Ignition fetched", now)
			t.clearRequest(p)
		}
	}
	t.write(mac, p)
	if text != "" {
		t.event(mac, text)
	}
}

// Fetch kinds, the controller's names.
const (
	FetchKernel   = "kernel"
	FetchIgnition = "ignition"
)

// ObserveBooted is POST /booted: the OS is up; a node Booty cordoned is
// uncordoned.
func (t *Tracker) ObserveBooted(mac string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return
	}
	now := t.now()
	p := clonePower(h.Power)
	p.LastSeen = stamp(now)
	t.set(p, hardware.PowerUp, "booted", now)
	t.clearRequest(p)
	delete(t.warned, mac)
	delete(t.cordoned, mac)
	t.write(mac, p)
	if p.Cordoned {
		t.uncordon(h)
	}
}

// ObserveSeen is a /health or /update-check from the host: proof the OS
// is up right now.
func (t *Tracker) ObserveSeen(mac string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.opts.Fleet.Host(mac)
	if !ok {
		return
	}
	now := t.now()
	p := clonePower(h.Power)
	old := h.Power
	p.LastSeen = stamp(now)
	var text string
	switch p.State {
	case hardware.PowerOff:
		text = "host came back without a request"
		fallthrough
	case hardware.PowerBooting, hardware.PowerUnknown, hardware.PowerUnreachable:
		t.set(p, hardware.PowerUp, "host checked in", now)
		t.clearRequest(p)
	}
	if !powerChanged(old, p) {
		return
	}
	t.write(mac, p)
	if text != "" {
		t.event(mac, text)
	}
}

func (t *Tracker) uncordon(h *hardware.Host) {
	if t.opts.Actuators == nil {
		return
	}
	mac, target := h.MAC, actuator.FromHardware(h)
	t.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		r, err := t.opts.Actuators.Operator(ctx)
		if err == nil {
			err = r.Finish(ctx, target)
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if err != nil {
			t.event(mac, "uncordon failed: "+err.Error())
			return
		}
		if _, ok := t.opts.Fleet.Host(mac); ok {
			if err := t.opts.Fleet.Update(mac, func(h *hardware.Host) {
				if h.Power != nil {
					h.Power.Cordoned = false
				}
			}); err != nil {
				slog.Warn("Could not record uncordon", "mac", mac, "error", err)
			}
		}
		t.event(mac, "node uncordoned")
	})
}

// Capabilities is what the UI needs to enable the buttons.
type Capabilities struct {
	WoL      bool   `json:"wol"`
	Actuator string `json:"actuator"`
}

// Summary is the /info.power block.
type Summary struct {
	Up          int `json:"up"`
	Off         int `json:"off"`
	Unreachable int `json:"unreachable"`
	InFlight    int `json:"inFlight"`
}

// Status is GET /power.
type Status struct {
	Hosts        map[string]*hardware.HostPower `json:"hosts"`
	Events       []Event                        `json:"events"`
	Capabilities Capabilities                   `json:"capabilities"`
	Summary      Summary                        `json:"summary"`
}

// Status snapshots every host's power block, the events and the
// capabilities. The actuator probe is the chooser's cached one.
func (t *Tracker) Status(ctx context.Context) Status {
	st := Status{Hosts: map[string]*hardware.HostPower{}, Capabilities: Capabilities{WoL: true, Actuator: actuator.NameNone}}
	if t.opts.Actuators != nil {
		st.Capabilities.Actuator = t.opts.Actuators.OperatorName(ctx)
	}
	for _, h := range t.opts.Fleet.Hosts() {
		p := clonePower(h.Power)
		st.Hosts[h.MAC] = p
	}
	st.Summary = Summarize(t.opts.Fleet.Hosts())
	t.mu.Lock()
	st.Events = slices.Clone(t.events)
	t.mu.Unlock()
	if st.Events == nil {
		st.Events = []Event{}
	}
	return st
}

// Summarize counts hosts by power state.
func Summarize(hosts []*hardware.Host) Summary {
	var s Summary
	for _, h := range hosts {
		switch h.PowerState() {
		case hardware.PowerUp:
			s.Up++
		case hardware.PowerOff:
			s.Off++
		case hardware.PowerUnreachable:
			s.Unreachable++
		}
		if h.Power.InFlight() {
			s.InFlight++
		}
	}
	return s
}

// Events returns the tracker's own ring.
func (t *Tracker) Events() []Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.events)
}
