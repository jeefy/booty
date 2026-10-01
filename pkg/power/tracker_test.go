package power

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/hardware"
)

const (
	macA = "aa:bb:cc:dd:ee:01"
	macB = "aa:bb:cc:dd:ee:02"
	macC = "aa:bb:cc:dd:ee:03"
)

type fakeFleet struct {
	mu      sync.Mutex
	hosts   map[string]*hardware.Host
	updates int
}

func newFakeFleet() *fakeFleet { return &fakeFleet{hosts: map[string]*hardware.Host{}} }

func (f *fakeFleet) add(h hardware.Host) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := h
	f.hosts[h.MAC] = &c
}

func (f *fakeFleet) Hosts() []*hardware.Host {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*hardware.Host, 0, len(f.hosts))
	for _, h := range f.hosts {
		c := *h
		out = append(out, &c)
	}
	slices.SortFunc(out, func(a, b *hardware.Host) int { return strings.Compare(a.MAC, b.MAC) })
	return out
}

func (f *fakeFleet) Host(mac string) (*hardware.Host, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[mac]
	if !ok {
		return nil, false
	}
	c := *h
	return &c, true
}

func (f *fakeFleet) Update(mac string, fn func(*hardware.Host)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[mac]
	if !ok {
		return hardware.ErrNotFound
	}
	fn(h)
	f.updates++
	return nil
}

func (f *fakeFleet) power(mac string) *hardware.HostPower {
	h, _ := f.Host(mac)
	if h == nil || h.Power == nil {
		return &hardware.HostPower{State: hardware.PowerUnknown}
	}
	return h.Power
}

type fakeCluster struct {
	mu    sync.Mutex
	nodes []k8s.Node
	err   error
}

func (c *fakeCluster) Configured() bool { return c != nil }
func (c *fakeCluster) Nodes(context.Context) ([]k8s.Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.nodes), c.err
}

type fakeAutopilot struct {
	mu      sync.Mutex
	wanted  map[string]string
	driving map[string]bool
	events  []string
}

func newFakeAutopilot() *fakeAutopilot {
	return &fakeAutopilot{wanted: map[string]string{}, driving: map[string]bool{}}
}

func (a *fakeAutopilot) RebootWanted(mac string) (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	why, ok := a.wanted[mac]
	return ok, why
}

func (a *fakeAutopilot) Driving(mac string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.driving[mac]
}

func (a *fakeAutopilot) Event(kind, mac, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, kind+" "+mac+" "+text)
}

type fakeActuator struct {
	mu          sync.Mutex
	name        string
	err         error
	prepareErr  error
	actErr      error
	calls       []string
	finishCalls int
}

func (a *fakeActuator) Operator(context.Context) (actuator.PowerActuator, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a, nil
}

func (a *fakeActuator) OperatorName(ctx context.Context) string {
	if a.err != nil {
		return actuator.NameNone
	}
	return a.name
}

func (a *fakeActuator) Name() string { return a.name }
func (a *fakeActuator) record(what string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, what)
}
func (a *fakeActuator) Prepare(context.Context, actuator.Host) error {
	a.record("prepare")
	return a.prepareErr
}
func (a *fakeActuator) Reboot(context.Context, actuator.Host) error {
	a.record("reboot")
	return a.actErr
}
func (a *fakeActuator) PowerOff(context.Context, actuator.Host) error {
	a.record("poweroff")
	return a.actErr
}
func (a *fakeActuator) Finish(context.Context, actuator.Host) error {
	a.record("finish")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finishCalls++
	return nil
}

func (a *fakeActuator) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		calls := slices.Clone(a.calls)
		a.mu.Unlock()
		if len(calls) >= n {
			return calls
		}
		time.Sleep(2 * time.Millisecond)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t.Fatalf("waited for %d actuator calls, have %v", n, a.calls)
	return nil
}

type harness struct {
	t       *testing.T
	clock   atomic.Int64
	fleet   *fakeFleet
	cluster *fakeCluster
	pilot   *fakeAutopilot
	act     *fakeActuator
	probes  sync.Map
	tr      *Tracker
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, fleet: newFakeFleet(), cluster: &fakeCluster{}, pilot: newFakeAutopilot(), act: &fakeActuator{name: actuator.NameSSH}}
	h.clock.Store(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC).Unix())
	tr, err := New(Options{
		Fleet: h.fleet, Cluster: h.cluster, Autopilot: h.pilot, Actuators: h.act,
		Waker: &Waker{Interfaces: func() ([]net.Addr, error) { return nil, nil }, Sleep: func(context.Context, time.Duration) error { return nil }},
		Probe: func(_ context.Context, ip string) (bool, string) {
			ok, _ := h.probes.Load(ip)
			if ok == true {
				return true, "tcp/22"
			}
			return false, "tcp/10250"
		},
		Now: h.time,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.tr = tr
	t.Cleanup(tr.Wait)
	return h
}

func (h *harness) time() time.Time         { return time.Unix(h.clock.Load(), 0).UTC() }
func (h *harness) advance(d time.Duration) { h.clock.Add(int64(d / time.Second)) }
func (h *harness) tick()                   { h.tr.Tick(context.Background()) }
func (h *harness) answers(ip string, ok bool) {
	h.probes.Store(ip, ok)
}

func (h *harness) host(mac, hostname, ip string, power *hardware.HostPower) {
	h.fleet.add(hardware.Host{MAC: mac, Hostname: hostname, IP: ip, OS: "flatcar", Power: power})
}

func (h *harness) expectState(mac, want string) {
	h.t.Helper()
	if got := h.fleet.power(mac); got.State != want {
		h.t.Fatalf("%s: state %q (%s), want %q", mac, got.State, got.Reason, want)
	}
}

func (h *harness) events() []string {
	var out []string
	for _, e := range h.tr.Events() {
		out = append(out, e.Text)
	}
	return out
}

func (h *harness) hasEvent(sub string) bool {
	for _, e := range h.events() {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestProbeDerivesUpAndUnreachable(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", nil)
	h.answers("10.0.0.1", true)
	h.tick()
	h.expectState(macA, hardware.PowerUp)
	if p := h.fleet.power(macA); !p.Probe.OK || p.Probe.Method != "tcp/22" || p.Probe.At == "" {
		t.Fatalf("probe not recorded: %+v", p)
	}

	h.answers("10.0.0.1", false)
	h.advance(time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerUp)

	h.tr.ObserveSeen(macA)
	h.advance(14 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerUp)

	h.advance(2 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerUnreachable)
	if !h.hasEvent("host is unreachable") {
		t.Fatalf("events %v", h.events())
	}

	h.tr.ObserveSeen(macA)
	h.expectState(macA, hardware.PowerUp)
}

func TestNoIPHostIsNeverUnreachable(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "", nil)
	h.tick()
	h.expectState(macA, hardware.PowerUnknown)
	h.tr.ObserveSeen(macA)
	h.expectState(macA, hardware.PowerUp)
	h.advance(16 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerUnknown)
}

func TestNodeReadyCountsAsSeen(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", nil)
	h.cluster.nodes = []k8s.Node{{Name: "alpha", Ready: true}}
	h.answers("10.0.0.1", false)
	h.tick()
	h.expectState(macA, hardware.PowerUp)
	h.cluster.nodes = nil
	h.advance(16 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerUnreachable)
}

func TestQuietTickDoesNotRewriteTheRecord(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", nil)
	h.answers("10.0.0.1", true)
	h.tick()
	before := h.fleet.updates
	h.advance(30 * time.Second)
	h.tick()
	if h.fleet.updates != before {
		t.Fatal("a probe 30 s later must not rewrite the record")
	}
	h.advance(time.Minute)
	h.tick()
	if h.fleet.updates == before {
		t.Fatal("a probe a minute later refreshes probe.at")
	}
}

func TestPowerOnThroughBootingToUp(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerOff})
	p, err := h.tr.PowerOn(context.Background(), macA, "192.168.1.20", Request{Reason: "maintenance over"})
	if err != nil || p.State != hardware.PowerPoweringOn || p.Request != hardware.PowerRequestOn || p.RequestedBy != "192.168.1.20" {
		t.Fatalf("%+v %v", p, err)
	}
	h.tr.Wait()
	if !h.hasEvent("magic packet sent to") {
		t.Fatalf("events %v", h.events())
	}
	if again, err := h.tr.PowerOn(context.Background(), macA, "x", Request{}); err != nil || again.State != hardware.PowerPoweringOn {
		t.Fatalf("second power on within the window must be idempotent: %+v %v", again, err)
	}
	if _, err := h.tr.Reboot(context.Background(), macA, "x", Request{}); Conflict(err) == "" {
		t.Fatalf("reboot while powering on must conflict: %v", err)
	}

	h.advance(2 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerPoweringOn)

	h.tr.ObserveFetch(macA, FetchKernel)
	h.expectState(macA, hardware.PowerBooting)
	if p := h.fleet.power(macA); p.Request != "" {
		t.Fatalf("kernel fetch must clear the request: %+v", p)
	}
	h.tr.ObserveFetch(macA, FetchIgnition)
	h.expectState(macA, hardware.PowerBooting)
	h.tr.ObserveBooted(macA)
	h.expectState(macA, hardware.PowerUp)
	if !h.hasEvent("woke up") {
		t.Fatalf("events %v", h.events())
	}
}

func TestPowerOnThatDoesNotWakeGoesBackToOff(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUnreachable})
	if _, err := h.tr.PowerOn(context.Background(), macA, "op", Request{}); err != nil {
		t.Fatal(err)
	}
	h.advance(9 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerPoweringOn)
	h.advance(2 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerOff)
	if !h.hasEvent("did not wake") {
		t.Fatalf("events %v", h.events())
	}
	for _, mac := range []string{macA} {
		if p := h.fleet.power(mac); p.Request != "" {
			t.Fatalf("request must be cleared: %+v", p)
		}
	}
}

func TestPowerOnRefusedWhenUp(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	_, err := h.tr.PowerOn(context.Background(), macA, "op", Request{})
	if msg := Conflict(err); !strings.Contains(msg, "host is up") {
		t.Fatalf("%v", err)
	}
	if _, err := h.tr.PowerOn(context.Background(), "aa:bb:cc:dd:ee:ff", "op", Request{}); !errors.Is(err, hardware.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
}

func TestRebootDrainsRebootsAndUncordonsOnBooted(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	h.answers("10.0.0.1", true)
	p, err := h.tr.Reboot(context.Background(), macA, "op", Request{Reason: "kernel update"})
	if err != nil || p.State != hardware.PowerDraining || p.Request != hardware.PowerRequestReboot || p.Reason != "kernel update" {
		t.Fatalf("%+v %v", p, err)
	}
	calls := h.act.wait(t, 2)
	if !slices.Equal(calls, []string{"prepare", "reboot"}) {
		t.Fatalf("calls %v", calls)
	}
	h.tr.Wait()
	h.expectState(macA, hardware.PowerRebooting)
	if p := h.fleet.power(macA); !p.Cordoned {
		t.Fatalf("drained node must be marked cordoned: %+v", p)
	}
	if !h.hasEvent("reboot under way through the ssh actuator") {
		t.Fatalf("events %v", h.events())
	}

	h.advance(time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerRebooting)

	h.tr.ObserveFetch(macA, FetchKernel)
	h.expectState(macA, hardware.PowerBooting)
	h.tr.ObserveBooted(macA)
	h.expectState(macA, hardware.PowerUp)
	h.act.wait(t, 3)
	h.tr.Wait()
	if p := h.fleet.power(macA); p.Cordoned || p.Request != "" {
		t.Fatalf("booted host must be uncordoned: %+v", p)
	}
	if !h.hasEvent("node uncordoned") {
		t.Fatalf("events %v", h.events())
	}
	if len(h.pilot.events) == 0 || !strings.HasPrefix(h.pilot.events[0], "power "+macA+" ") {
		t.Fatalf("events must be mirrored to the autopilot: %v", h.pilot.events)
	}
}

func TestRebootWithoutDrainOrCluster(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	no := false
	p, err := h.tr.Reboot(context.Background(), macA, "op", Request{Drain: &no})
	if err != nil || p.State != hardware.PowerRebooting {
		t.Fatalf("%+v %v", p, err)
	}
	if calls := h.act.wait(t, 1); !slices.Equal(calls, []string{"reboot"}) {
		t.Fatalf("calls %v", calls)
	}
	h.tr.Wait()
	if p := h.fleet.power(macA); p.Cordoned {
		t.Fatalf("no drain, no cordon: %+v", p)
	}
}

func TestRebootNotObservedExpires(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	h.answers("10.0.0.1", true)
	no := false
	if _, err := h.tr.Reboot(context.Background(), macA, "op", Request{Drain: &no}); err != nil {
		t.Fatal(err)
	}
	h.tr.Wait()
	h.advance(11 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerUp)
	if !h.hasEvent("reboot was not observed") {
		t.Fatalf("events %v", h.events())
	}

	h.host(macB, "bravo", "10.0.0.2", &hardware.HostPower{State: hardware.PowerUp})
	h.answers("10.0.0.2", false)
	if _, err := h.tr.Reboot(context.Background(), macB, "op", Request{Drain: &no}); err != nil {
		t.Fatal(err)
	}
	h.tr.Wait()
	h.advance(11 * time.Minute)
	h.tick()
	h.expectState(macB, hardware.PowerUnreachable)
}

func TestRebootLeftCordonedAfterGiveUp(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	h.answers("10.0.0.1", false)
	if _, err := h.tr.Reboot(context.Background(), macA, "op", Request{}); err != nil {
		t.Fatal(err)
	}
	h.act.wait(t, 2)
	h.tr.Wait()
	h.advance(16 * time.Minute)
	h.tick()
	if p := h.fleet.power(macA); !p.Cordoned {
		t.Fatalf("still cordoned: %+v", p)
	}
	if !h.hasEvent("left cordoned") {
		t.Fatalf("events %v", h.events())
	}
	h.tick()
	n := 0
	for _, e := range h.events() {
		if strings.Contains(e, "left cordoned") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the give-up is said once, got %d", n)
	}
	h.tr.ObserveBooted(macA)
	h.act.wait(t, 3)
	h.tr.Wait()
	if p := h.fleet.power(macA); p.Cordoned {
		t.Fatalf("a late /booted still uncordons: %+v", p)
	}
}

func TestShutdownToOffAndBack(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	h.host(macB, "bravo", "10.0.0.2", &hardware.HostPower{State: hardware.PowerUp})
	h.cluster.nodes = []k8s.Node{{Name: "alpha", Ready: true}, {Name: "bravo", Ready: true}}
	h.answers("10.0.0.1", true)
	p, err := h.tr.Shutdown(context.Background(), macA, "op", Request{})
	if err != nil || p.State != hardware.PowerDraining || p.Request != hardware.PowerRequestShutdown {
		t.Fatalf("%+v %v", p, err)
	}
	if calls := h.act.wait(t, 2); !slices.Equal(calls, []string{"prepare", "poweroff"}) {
		t.Fatalf("calls %v", calls)
	}
	h.tr.Wait()
	h.expectState(macA, hardware.PowerShuttingDown)
	h.cluster.nodes = nil

	h.advance(time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerShuttingDown)

	h.answers("10.0.0.1", false)
	h.advance(time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerOff)
	host, _ := h.fleet.Host(macA)
	if !host.PoweredOffByRequest() || !host.Power.Cordoned {
		t.Fatalf("off by request, still cordoned: %+v", host.Power)
	}
	if _, err := h.tr.Reboot(context.Background(), macA, "op", Request{}); Conflict(err) == "" {
		t.Fatalf("reboot of an off host: %v", err)
	}

	h.advance(time.Hour)
	h.tick()
	h.expectState(macA, hardware.PowerOff)

	h.tr.ObserveFetch(macA, FetchKernel)
	host, _ = h.fleet.Host(macA)
	if host.PoweredOffByRequest() || host.Power.State != hardware.PowerBooting {
		t.Fatalf("a boot ends the exclusion: %+v", host.Power)
	}
	h.tr.ObserveBooted(macA)
	h.act.wait(t, 3)
	h.tr.Wait()
	if p := h.fleet.power(macA); p.Cordoned || p.State != hardware.PowerUp {
		t.Fatalf("back up and uncordoned: %+v", p)
	}
}

func TestShutdownThatDoesNotHappen(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	h.host(macB, "bravo", "10.0.0.2", &hardware.HostPower{State: hardware.PowerUp})
	h.answers("10.0.0.1", true)
	no := false
	if _, err := h.tr.Shutdown(context.Background(), macA, "op", Request{Drain: &no}); err != nil {
		t.Fatal(err)
	}
	h.tr.Wait()
	h.advance(6 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerUp)
	if !h.hasEvent("did not shut down") {
		t.Fatalf("events %v", h.events())
	}
}

func TestOffHostComingBackWithoutARequest(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerOff, Request: hardware.PowerRequestShutdown})
	h.answers("10.0.0.1", true)
	h.tick()
	h.expectState(macA, hardware.PowerUp)
	if p := h.fleet.power(macA); p.Request != "" {
		t.Fatalf("%+v", p)
	}
	if !h.hasEvent("came back without a request") {
		t.Fatalf("events %v", h.events())
	}
}

func TestGuards(t *testing.T) {
	h := newHarness(t)
	h.fleet.add(hardware.Host{MAC: macA, Hostname: "cp", IP: "10.0.0.1", Role: hardware.RoleControlPlane, Power: &hardware.HostPower{State: hardware.PowerUp}})
	h.host(macB, "bravo", "10.0.0.2", &hardware.HostPower{State: hardware.PowerUp})
	h.host(macC, "charlie", "10.0.0.3", &hardware.HostPower{State: hardware.PowerUp})
	ctx := context.Background()

	_, err := h.tr.Reboot(ctx, macA, "op", Request{})
	if msg := Conflict(err); !strings.Contains(msg, "control-plane") {
		t.Fatalf("control plane: %v", err)
	}
	_, err = h.tr.Shutdown(ctx, macA, "op", Request{})
	if msg := Conflict(err); !strings.Contains(msg, "control-plane") {
		t.Fatalf("control plane shutdown: %v", err)
	}

	h.pilot.driving[macB] = true
	_, err = h.tr.Reboot(ctx, macB, "op", Request{})
	if msg := Conflict(err); msg != "autopilot is driving this host" {
		t.Fatalf("driving: %v", err)
	}
	h.pilot.driving[macB] = false

	h.cluster.nodes = []k8s.Node{{Name: "cp", Ready: true, ControlPlane: true}, {Name: "bravo", Ready: true}, {Name: "charlie", Ready: true, Unschedulable: true}}
	_, err = h.tr.Shutdown(ctx, macB, "op", Request{})
	if msg := Conflict(err); !strings.Contains(msg, "last Ready worker") {
		t.Fatalf("last worker (cluster): %v", err)
	}
	if _, err := h.tr.Reboot(ctx, macB, "op", Request{}); err != nil {
		t.Fatalf("reboot of the last worker is allowed: %v", err)
	}
	h.tr.Wait()
	h.fleet.add(hardware.Host{MAC: macB, Hostname: "bravo", IP: "10.0.0.2", Power: &hardware.HostPower{State: hardware.PowerUp}})

	h.cluster.nodes = []k8s.Node{{Name: "cp", Ready: true, ControlPlane: true}, {Name: "bravo", Ready: true}, {Name: "charlie", Ready: true}}
	if _, err := h.tr.Shutdown(ctx, macB, "op", Request{}); err != nil {
		t.Fatalf("two Ready workers: %v", err)
	}
	h.tr.Wait()

	h.fleet.add(hardware.Host{MAC: macB, Hostname: "bravo", IP: "10.0.0.2", Power: &hardware.HostPower{State: hardware.PowerUp}})
	h.cluster.err = errors.New("down")
	h.fleet.add(hardware.Host{MAC: macC, Hostname: "charlie", IP: "10.0.0.3", Power: &hardware.HostPower{State: hardware.PowerOff}})
	_, err = h.tr.Shutdown(ctx, macB, "op", Request{})
	if msg := Conflict(err); !strings.Contains(msg, "last Ready worker") {
		t.Fatalf("last worker (hosts): %v", err)
	}
	p, err := h.tr.Shutdown(ctx, macB, "op", Request{Force: true})
	if err != nil || p.Request != hardware.PowerRequestShutdown {
		t.Fatalf("force: %+v %v", p, err)
	}
	h.tr.Wait()

	p, err = h.tr.Reboot(ctx, macA, "op", Request{Force: true})
	if err != nil || p.Request != hardware.PowerRequestReboot {
		t.Fatalf("force control plane: %+v %v", p, err)
	}
	h.tr.Wait()
	if _, err := h.tr.Reboot(ctx, macA, "op", Request{Force: true}); err != nil {
		t.Fatalf("same action in flight is idempotent: %v", err)
	}
	if _, err := h.tr.Shutdown(ctx, macA, "op", Request{Force: true}); Conflict(err) == "" {
		t.Fatalf("other action in flight: %v", err)
	}
}

func TestNoActuator(t *testing.T) {
	h := newHarness(t)
	h.act.err = actuator.ErrNoOperatorActuator
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	_, err := h.tr.Reboot(context.Background(), macA, "op", Request{})
	if msg := Conflict(err); msg != actuator.ErrNoOperatorActuator.Error() {
		t.Fatalf("%v", err)
	}
	h.expectState(macA, hardware.PowerUp)
	if st := h.tr.Status(context.Background()); st.Capabilities.Actuator != actuator.NameNone || !st.Capabilities.WoL {
		t.Fatalf("%+v", st.Capabilities)
	}
	if _, err := h.tr.PowerOn(context.Background(), macA, "op", Request{}); Conflict(err) == "" {
		t.Fatal("up host cannot be powered on")
	}
	h.host(macB, "bravo", "10.0.0.2", &hardware.HostPower{State: hardware.PowerOff})
	if _, err := h.tr.PowerOn(context.Background(), macB, "op", Request{}); err != nil {
		t.Fatalf("power on needs no actuator: %v", err)
	}
}

func TestActuatorFailureRevertsAndUncordons(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	h.act.actErr = errors.New("ssh: connection refused")
	if _, err := h.tr.Reboot(context.Background(), macA, "op", Request{}); err != nil {
		t.Fatal(err)
	}
	h.act.wait(t, 3)
	h.tr.Wait()
	p := h.fleet.power(macA)
	if p.State != hardware.PowerUp || p.Request != "" || p.Cordoned {
		t.Fatalf("%+v", p)
	}
	if !h.hasEvent("ssh actuator failed") || h.act.finishCalls != 1 {
		t.Fatalf("events %v finish %d", h.events(), h.act.finishCalls)
	}

	h.act.actErr, h.act.prepareErr = nil, errors.New("drain: PDB")
	if _, err := h.tr.Reboot(context.Background(), macA, "op", Request{}); err != nil {
		t.Fatal(err)
	}
	h.tr.Wait()
	if p := h.fleet.power(macA); p.State != hardware.PowerUp || !h.hasEvent("drain failed") {
		t.Fatalf("%+v %v", p, h.events())
	}
}

func TestAutopilotRebootShowsAsRebooting(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	h.answers("10.0.0.1", true)
	h.pilot.wanted[macA] = "autopilot: attempt 2 into 4800.0.0"
	h.tick()
	p := h.fleet.power(macA)
	if p.State != hardware.PowerRebooting || p.RequestedBy != RequestedByAutopilot || p.Request != "" {
		t.Fatalf("%+v", p)
	}
	_, err := h.tr.Reboot(context.Background(), macA, "op", Request{})
	if msg := Conflict(err); msg != "autopilot is driving this host" {
		t.Fatalf("%v", err)
	}
	h.advance(20 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerRebooting)

	h.tr.ObserveFetch(macA, FetchKernel)
	h.expectState(macA, hardware.PowerBooting)
	delete(h.pilot.wanted, macA)
	h.tr.ObserveBooted(macA)
	h.tick()
	h.expectState(macA, hardware.PowerUp)

	h.pilot.wanted[macA] = "autopilot: attempt 2"
	h.tick()
	h.expectState(macA, hardware.PowerRebooting)
	delete(h.pilot.wanted, macA)
	h.tick()
	h.expectState(macA, hardware.PowerUp)
	if p := h.fleet.power(macA); p.RequestedBy != "" {
		t.Fatalf("%+v", p)
	}
}

func TestCancelRecomputesFromSignals(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerOff})
	if _, err := h.tr.PowerOn(context.Background(), macA, "op", Request{}); err != nil {
		t.Fatal(err)
	}
	h.tr.Wait()
	h.answers("10.0.0.1", false)
	h.tick()
	p, err := h.tr.Cancel(macA)
	if err != nil || p.State != hardware.PowerUnreachable || p.Request != "" {
		t.Fatalf("%+v %v", p, err)
	}
	if !h.hasEvent("cancelled the powering-on") {
		t.Fatalf("events %v", h.events())
	}
	if again, err := h.tr.Cancel(macA); err != nil || again.State != hardware.PowerUnreachable {
		t.Fatalf("cancel with nothing in flight: %+v %v", again, err)
	}

	h.host(macB, "bravo", "10.0.0.2", &hardware.HostPower{State: hardware.PowerUp})
	h.answers("10.0.0.2", true)
	h.tick()
	no := false
	if _, err := h.tr.Reboot(context.Background(), macB, "op", Request{Drain: &no}); err != nil {
		t.Fatal(err)
	}
	h.tr.Wait()
	if p, _ := h.tr.Cancel(macB); p.State != hardware.PowerUp {
		t.Fatalf("%+v", p)
	}

	h.host(macC, "charlie", "", &hardware.HostPower{State: hardware.PowerUnknown})
	if _, err := h.tr.PowerOn(context.Background(), macC, "op", Request{}); err != nil {
		t.Fatal(err)
	}
	h.tr.Wait()
	if p, _ := h.tr.Cancel(macC); p.State != hardware.PowerUnknown {
		t.Fatalf("%+v", p)
	}
}

func TestBootingHostGoesQuiet(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", nil)
	h.answers("10.0.0.1", false)
	h.tr.ObserveFetch(macA, FetchKernel)
	h.expectState(macA, hardware.PowerBooting)
	h.advance(10 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerBooting)
	h.advance(6 * time.Minute)
	h.tick()
	h.expectState(macA, hardware.PowerUnreachable)

	h.host(macB, "bravo", "10.0.0.2", nil)
	h.answers("10.0.0.2", true)
	h.tr.ObserveFetch(macB, FetchKernel)
	h.tick()
	h.expectState(macB, hardware.PowerUp)
}

func TestStatusAndSummary(t *testing.T) {
	h := newHarness(t)
	h.host(macA, "alpha", "10.0.0.1", &hardware.HostPower{State: hardware.PowerUp})
	h.host(macB, "bravo", "10.0.0.2", &hardware.HostPower{State: hardware.PowerOff})
	h.host(macC, "charlie", "10.0.0.3", &hardware.HostPower{State: hardware.PowerRebooting, Request: hardware.PowerRequestReboot})
	h.fleet.add(hardware.Host{MAC: "aa:bb:cc:dd:ee:04", Power: &hardware.HostPower{State: hardware.PowerUnreachable}})
	h.fleet.add(hardware.Host{MAC: "aa:bb:cc:dd:ee:05"})
	st := h.tr.Status(context.Background())
	if st.Summary != (Summary{Up: 1, Off: 1, Unreachable: 1, InFlight: 1}) {
		t.Fatalf("%+v", st.Summary)
	}
	if len(st.Hosts) != 5 || st.Hosts["aa:bb:cc:dd:ee:05"].State != hardware.PowerUnknown || st.Capabilities.Actuator != actuator.NameSSH || st.Events == nil {
		t.Fatalf("%+v", st)
	}
}

func TestEventsRingIsBounded(t *testing.T) {
	h := newHarness(t)
	h.tr.mu.Lock()
	for i := 0; i < maxEvents+20; i++ {
		h.tr.event(macA, "e")
	}
	h.tr.mu.Unlock()
	if n := len(h.tr.Events()); n != maxEvents {
		t.Fatalf("%d events, want %d", n, maxEvents)
	}
}

func TestTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	old := ProbePorts
	ProbePorts = []int{1, port}
	t.Cleanup(func() { ProbePorts = old })
	ok, method := TCPProbe(context.Background(), "127.0.0.1")
	if !ok || method != "tcp/"+strconv.Itoa(port) {
		t.Fatalf("%v %s", ok, method)
	}
	ProbePorts = []int{1}
	if ok, _ := TCPProbe(context.Background(), "127.0.0.1"); ok {
		t.Fatal("port 1 must be closed")
	}
}
