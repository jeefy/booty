package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/cluster/k8s/fake"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

const (
	macA = "aa:bb:cc:dd:ee:01"
	macB = "aa:bb:cc:dd:ee:02"
	macC = "aa:bb:cc:dd:ee:03"
)

type fakeFleet struct {
	mu       sync.Mutex
	hosts    map[string]*hardware.Host
	current  map[string]string
	lastGood map[string]string
	cached   map[string][]string
	holds    map[string]string
	serial   map[string]bool
	updates  int
}

func newFakeFleet() *fakeFleet {
	return &fakeFleet{hosts: map[string]*hardware.Host{}, current: map[string]string{}, lastGood: map[string]string{}, cached: map[string][]string{}, holds: map[string]string{}, serial: map[string]bool{}}
}

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

func (f *fakeFleet) Current(osName string) string  { return f.current[osName] }
func (f *fakeFleet) LastGood(osName string) string { return f.lastGood[osName] }
func (f *fakeFleet) SetLastGood(osName, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.cached[osName], version) {
		return fmt.Errorf("%s %s not cached", osName, version)
	}
	f.lastGood[osName] = version
	return nil
}
func (f *fakeFleet) Cached(osName string) []string { return f.cached[osName] }
func (f *fakeFleet) Hold(osName, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if version == "" {
		delete(f.holds, osName)
		return
	}
	f.holds[osName] = version
}

func (f *fakeFleet) SerialRollout(osName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serial[osName] = true
}

func (f *fakeFleet) fleetTarget(osName string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v := f.holds[osName]; v != "" {
		return v
	}
	if lg := f.lastGood[osName]; f.serial[osName] && lg != "" && lg != f.current[osName] {
		return lg
	}
	return f.current[osName]
}

func (f *fakeFleet) EffectiveTarget(h *hardware.Host) string {
	os := hostOS(h)
	if h.TargetVersion != "" && slices.Contains(f.cached[os], h.TargetVersion) {
		return h.TargetVersion
	}
	return f.fleetTarget(os)
}

type fakeActuator struct {
	mu    sync.Mutex
	name  string
	err   error
	calls []string
}

func (a *fakeActuator) Choose(context.Context) (actuator.Rebooter, error) {
	if a.err != nil {
		return nil, a.err
	}
	if a.name == actuator.NameKured {
		return actuator.Kured{}, nil
	}
	return a, nil
}

func (a *fakeActuator) Name() string { return a.name }
func (a *fakeActuator) record(what string, h actuator.Host) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, what+" "+h.MAC)
	return nil
}
func (a *fakeActuator) Prepare(_ context.Context, h actuator.Host) error {
	return a.record("prepare", h)
}
func (a *fakeActuator) Reboot(_ context.Context, h actuator.Host) error { return a.record("reboot", h) }
func (a *fakeActuator) Finish(_ context.Context, h actuator.Host) error { return a.record("finish", h) }

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
		time.Sleep(5 * time.Millisecond)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t.Fatalf("waited for %d actuator calls, have %v", n, a.calls)
	return nil
}

type podFixture struct {
	ns, name, owner, phase, waiting string
	ready, containers               int
	labels                          map[string]string
}

type harness struct {
	t     *testing.T
	clock atomic.Int64
	fleet *fakeFleet
	act   *fakeActuator
	api   *fake.API
	c     *Controller
	path  string
	opts  Options
}

func newHarness(t *testing.T, mode string) *harness {
	t.Helper()
	api, client, _ := fake.New(t)
	h := &harness{t: t, fleet: newFakeFleet(), act: &fakeActuator{name: actuator.NameAPI}, api: api, path: filepath.Join(t.TempDir(), "state.json")}
	h.clock.Store(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC).Unix())
	h.fleet.current["flatcar"], h.fleet.lastGood["flatcar"], h.fleet.cached["flatcar"] = "4800.0.0", "4757.2.0", []string{"4800.0.0", "4757.2.0"}
	h.fleet.current["bluefin"], h.fleet.lastGood["bluefin"], h.fleet.cached["bluefin"] = "27.01.100", "26.09.673", []string{"27.01.100", "26.09.673"}
	h.opts = Options{Mode: mode, Fleet: h.fleet, Cluster: client, Actuators: h.act, StatePath: h.path, HealthWindow: 15 * time.Minute, RetryAfter: time.Hour, Now: h.time}
	h.start()
	return h
}

func (h *harness) start() {
	h.t.Helper()
	c, err := New(h.opts)
	if err != nil {
		h.t.Fatal(err)
	}
	h.c = c
	h.t.Cleanup(c.Wait)
}

func (h *harness) time() time.Time         { return time.Unix(h.clock.Load(), 0).UTC() }
func (h *harness) advance(d time.Duration) { h.clock.Add(int64(d / time.Second)) }
func (h *harness) tick()                   { h.c.Tick(context.Background()) }

func (h *harness) node(name string, ready bool, readySince time.Time, osImage string, pods ...podFixture) {
	status := "False"
	if ready {
		status = "True"
	}
	node := map[string]any{
		"metadata": map[string]any{"name": name, "labels": map[string]string{"kubernetes.io/hostname": name}},
		"status": map[string]any{
			"conditions": []map[string]any{{"type": "Ready", "status": status, "reason": "KubeletReady", "lastTransitionTime": readySince.UTC().Format(time.RFC3339)}},
			"nodeInfo":   map[string]string{"osImage": osImage, "kubeletVersion": "v1.34.3", "systemUUID": "uuid-" + name},
		},
	}
	items := []map[string]any{}
	for _, p := range pods {
		var owners []map[string]any
		if p.owner != "" {
			owners = append(owners, map[string]any{"kind": p.owner, "name": p.owner + "-x", "controller": true})
		}
		var statuses []map[string]any
		for i := 0; i < p.containers; i++ {
			st := map[string]any{"ready": i < p.ready, "restartCount": 0, "state": map[string]any{}}
			if p.waiting != "" && i >= p.ready {
				st["state"] = map[string]any{"waiting": map[string]string{"reason": p.waiting}}
			}
			statuses = append(statuses, st)
		}
		containers := make([]map[string]string, p.containers)
		for i := range containers {
			containers[i] = map[string]string{"name": fmt.Sprintf("c%d", i)}
		}
		items = append(items, map[string]any{
			"metadata": map[string]any{"name": p.name, "namespace": p.ns, "ownerReferences": owners, "labels": p.labels},
			"spec":     map[string]any{"nodeName": name, "containers": containers},
			"status":   map[string]any{"phase": p.phase, "containerStatuses": statuses},
		})
	}
	nb, _ := json.Marshal(node)
	pb, _ := json.Marshal(map[string]any{"items": items})
	h.api.Set("/api/v1/nodes/"+name, nb)
	h.api.Set("/api/v1/pods?spec.nodeName="+name, pb)
}

func (h *harness) healthyNode(name, osImage string) {
	h.node(name, true, h.time().Add(-10*time.Minute), osImage, podFixture{ns: "kube-system", name: "cilium-1", owner: "DaemonSet", phase: "Running", ready: 1, containers: 1})
}

func (h *harness) flatcarHost(mac, hostname string) {
	h.fleet.add(hardware.Host{MAC: mac, Hostname: hostname, IP: "192.168.1.5", OS: "flatcar", Booted: "2026-09-28T10:00:00Z", Running: "4757.2.0"})
}

func (h *harness) fetch(mac string) {
	h.c.ObserveFetch(mac, FetchKernel)
	h.c.ObserveFetch(mac, FetchIgnition)
}

func (h *harness) up(mac, running string, failed ...string) {
	h.c.ObserveBooted(mac, "")
	h.c.ObserveHealth(mac, &hardware.Health{Running: running, FailedUnits: failed, DMI: hardware.DMI{Vendor: "HP", Product: "EliteDesk", BIOSVersion: "L01", ProductUUID: "u1"}, Firmware: "uefi", Kernel: "6.1"})
}

func (h *harness) episode(mac string) *Episode {
	h.t.Helper()
	for _, hs := range h.c.Status().Hosts {
		if hs.MAC == mac {
			return hs.Episode
		}
	}
	return nil
}

func (h *harness) release(os, v string) *Release {
	h.t.Helper()
	for _, r := range h.c.Status().OS[os].Releases {
		if r.Version == v {
			return r
		}
	}
	return nil
}

func (h *harness) wantState(mac, state string, attempt int, class string) {
	h.t.Helper()
	e := h.episode(mac)
	if e == nil {
		h.t.Fatalf("%s: no episode", mac)
	}
	if e.State != state || e.Attempt != attempt || e.Class != class {
		h.t.Fatalf("%s: state=%s attempt=%d class=%q, want %s/%d/%q (note %q)", mac, e.State, e.Attempt, e.Class, state, attempt, class, e.Note)
	}
}

func (h *harness) wantHold(os, v string) {
	h.t.Helper()
	if got := h.fleet.fleetTarget(os); got != v {
		h.t.Fatalf("%s fleet target %q, want %q", os, got, v)
	}
}

func (h *harness) wantPin(mac, v string) {
	h.t.Helper()
	host, _ := h.fleet.Host(mac)
	if host.TargetVersion != v {
		h.t.Fatalf("%s targetVersion %q, want %q", mac, host.TargetVersion, v)
	}
}

func (h *harness) hasEvent(sub string) bool {
	for _, e := range h.c.Status().Events {
		if strings.Contains(e.Text, sub) {
			return true
		}
	}
	return false
}

// TestGuardFullEpisode is the plan's sequence on a Flatcar host under
// guard: fail, fail, rollback healthy -> TIMEOUT -> retry fail ->
// QUARANTINED, with the fleet held at lastGood from the first failure.
func TestGuardFullEpisode(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")
	h.healthyNode("ehrlitan", "Flatcar Container Linux by Kinvolk 4800.0.0")
	h.tick()
	if got := h.c.Status().OS["flatcar"]; got.FleetTarget != "4800.0.0" || got.Held {
		t.Fatalf("idle fleet: %+v", got)
	}

	h.fetch(macA)
	h.wantState(macA, hardware.AutopilotGating, 1, "")
	h.up(macA, "4800.0.0", "kubelet.service")
	h.wantState(macA, hardware.AutopilotRetrying, 2, ClassFailedUnits)
	h.wantHold("flatcar", "4757.2.0")
	calls := h.act.wait(t, 2)
	if calls[0] != "prepare "+macA || calls[1] != "reboot "+macA {
		t.Fatalf("actuator calls %v", calls)
	}
	if want, reason := h.c.RebootWanted(macA); !want || reason == "" {
		t.Fatalf("RebootWanted after a failed attempt: %v %q", want, reason)
	}

	hostB, _ := h.fleet.Host(macB)
	if got := h.fleet.EffectiveTarget(hostB); got != "4757.2.0" {
		t.Fatalf("second host must be held at lastGood, got %s", got)
	}
	h.fetch(macB)
	if h.episode(macB) != nil {
		t.Fatal("a host booting the held lastGood it already runs must not start an episode")
	}

	h.advance(time.Minute)
	h.fetch(macA)
	h.wantState(macA, hardware.AutopilotGating, 2, ClassFailedUnits)
	if want, _ := h.c.RebootWanted(macA); want {
		t.Fatal("RebootWanted must drop once the reboot was observed")
	}
	h.up(macA, "4800.0.0", "kubelet.service")
	h.wantState(macA, hardware.AutopilotRolledBack, 3, ClassFailedUnits)
	h.wantPin(macA, "4757.2.0")
	h.act.wait(t, 4)

	h.advance(time.Minute)
	h.fetch(macA)
	h.up(macA, "4757.2.0")
	h.healthyNode("ehrlitan", "Flatcar Container Linux by Kinvolk 4757.2.0")
	h.tick()
	e := h.episode(macA)
	if e.State != hardware.AutopilotRolledBack || !e.Done {
		t.Fatalf("rollback gate: %+v", e)
	}
	r := h.release("flatcar", "4800.0.0")
	if r.State != ReleaseTimeout || r.FailedOn != macA || r.Attempts != 2 || r.Report != "flatcar-4800.0.0" {
		t.Fatalf("release after rollback: %+v", r)
	}
	h.wantHold("flatcar", "4757.2.0")
	h.wantPin(macA, "")
	st := h.c.Status()
	if len(st.Reports) != 1 || st.Reports[0].Attempts != 3 || st.Reports[0].RollbackResult == "" {
		t.Fatalf("report stub: %+v", st.Reports)
	}
	if calls := h.act.wait(t, 5); calls[4] != "finish "+macA {
		t.Fatalf("api actuator must uncordon after a healthy gate: %v", calls)
	}

	h.advance(59 * time.Minute)
	h.tick()
	if r := h.release("flatcar", "4800.0.0"); r.State != ReleaseTimeout || r.Retried {
		t.Fatalf("retry must wait the whole TIMEOUT: %+v", r)
	}
	h.advance(2 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotRetrying, 1, "")
	h.wantPin(macA, "4800.0.0")
	if e := h.episode(macA); !e.Retry {
		t.Fatalf("retry episode: %+v", e)
	}
	h.fetch(macA)
	h.up(macA, "4800.0.0", "kubelet.service")
	if r := h.release("flatcar", "4800.0.0"); r.State != ReleaseQuarantined || r.Attempts != 3 {
		t.Fatalf("release after failed retry: %+v", r)
	}
	h.wantState(macA, hardware.AutopilotRolledBack, 2, ClassFailedUnits)
	h.wantPin(macA, "4757.2.0")
	h.fetch(macA)
	h.up(macA, "4757.2.0")
	h.tick()
	if e := h.episode(macA); !e.Done {
		t.Fatalf("host must be back on lastGood: %+v", e)
	}
	h.wantHold("flatcar", "4757.2.0")
	if s := h.c.Summary(); s.Quarantine != 1 || len(s.Held) != 1 || s.Held[0] != "flatcar" || s.NeedsHands != 0 {
		t.Fatalf("summary: %+v", s)
	}
	if got := h.fleet.lastGood["flatcar"]; got != "4757.2.0" {
		t.Fatalf("lastGood must not move: %s", got)
	}

	if err := h.c.Clear("flatcar", "4800.0.0"); err != nil {
		t.Fatal(err)
	}
	if r := h.release("flatcar", "4800.0.0"); r.State != ReleaseRolling || r.Retried {
		t.Fatalf("cleared release: %+v", r)
	}
	h.wantHold("flatcar", "4800.0.0")
	if err := h.c.Clear("flatcar", "4800.0.0"); err == nil {
		t.Fatal("clearing a rolling release must fail")
	}
	if err := h.c.Clear("flatcar", "nope"); err == nil {
		t.Fatal("clearing an unknown release must fail")
	}
	if writes := h.api.Writes(); len(writes) != 0 {
		t.Fatalf("the controller must never write to the cluster itself: %v", writes)
	}
}

func TestBootLoopThenHungNeedsHands(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.act.name = actuator.NameKured
	h.fetch(macA)
	h.advance(30 * time.Second)
	h.c.ObserveFetch(macA, FetchKernel)
	h.wantState(macA, hardware.AutopilotRetrying, 2, ClassBootLoop)
	if !h.hasEvent("delegated to kured") {
		t.Fatalf("kured actuator must only be named: %+v", h.c.Status().Events)
	}
	h.advance(14 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotRetrying, 2, ClassBootLoop)
	h.advance(2 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotNeedsHands, 2, ClassHung)
	if !h.hasEvent("NEEDS HANDS") || !h.hasEvent("no actuator can reach") {
		t.Fatalf("needs-hands alert: %+v", h.c.Status().Events)
	}
	h.wantHold("flatcar", "4800.0.0")
	if s := h.c.Summary(); s.NeedsHands != 1 {
		t.Fatalf("summary: %+v", s)
	}
	h.tick()
	host, _ := h.fleet.Host(macA)
	if host.Autopilot == nil || host.Autopilot.State != hardware.AutopilotNeedsHands || host.Autopilot.Class != ClassHung {
		t.Fatalf("host summary: %+v", host.Autopilot)
	}
}

func TestNoIgnitionAndTimeoutClasses(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.c.ObserveFetch(macA, FetchKernel)
	h.advance(4 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotGating, 1, "")
	h.advance(90 * time.Second)
	h.tick()
	h.wantState(macA, hardware.AutopilotRetrying, 2, ClassNoIgnition)

	h.fetch(macA)
	h.c.ObserveBooted(macA, "")
	h.advance(16 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotRolledBack, 3, ClassTimeout)
}

func TestNodeNotReadyAndWorkloadClasses(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.node("ehrlitan", false, h.time(), "Flatcar 4757.2.0")
	h.fetch(macA)
	h.up(macA, "4800.0.0")
	h.tick()
	if e := h.episode(macA); e.State != hardware.AutopilotGating || !strings.Contains(e.Note, "not Ready") {
		t.Fatalf("waiting note: %+v", e)
	}
	h.node("ehrlitan", true, h.time(), "Flatcar Container Linux by Kinvolk 4757.2.0")
	h.tick()
	if e := h.episode(macA); !strings.Contains(e.Note, "osImage of 4757.2.0") {
		t.Fatalf("stale osImage must hold the gate: %q", e.Note)
	}
	h.advance(16 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotRetrying, 2, ClassNodeNotReady)

	h.fetch(macA)
	h.up(macA, "4800.0.0")
	h.node("ehrlitan", true, h.time().Add(-10*time.Minute), "Flatcar 4800.0.0", podFixture{ns: "kube-system", name: "cilium-1", owner: "DaemonSet", phase: "Running", ready: 0, containers: 1, waiting: "CrashLoopBackOff"})
	h.tick()
	if e := h.episode(macA); !strings.Contains(e.Note, "cilium-1") {
		t.Fatalf("crashlooping DaemonSet pod must hold the gate: %q", e.Note)
	}
	h.advance(16 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotRolledBack, 3, ClassWorkloads)
}

func TestBaselineExcludesPreexistingFailures(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	bad := podFixture{ns: "default", name: "web-1", owner: "ReplicaSet", phase: "Running", ready: 0, containers: 1, waiting: "CrashLoopBackOff"}
	h.node("ehrlitan", true, h.time().Add(-10*time.Minute), "Flatcar 4757.2.0", bad)
	h.fetch(macA)
	if e := h.episode(macA); e == nil || h.c.state.Hosts[macA].Episode.Baseline == nil {
		t.Fatal("baseline must be taken at t0")
	}
	h.up(macA, "4800.0.0")
	h.node("ehrlitan", true, h.time().Add(-10*time.Minute), "Flatcar 4800.0.0", bad, podFixture{ns: "default", name: "job-1", owner: "Job", phase: "Pending", containers: 1})
	h.tick()
	e := h.episode(macA)
	if e.State != hardware.AutopilotIdle || !e.Done {
		t.Fatalf("pre-existing crashloop and Job pods must not fail the gate: %+v (%s)", e, e.Note)
	}
	if got := h.release("flatcar", "4800.0.0").Healthy; len(got) != 1 || got[0] != macA {
		t.Fatalf("healthy list: %v", got)
	}
}

// TestOwnRebootPodNeverFailsTheGate is the live incident: the API
// actuator's reboot Pod ends Failed when the node reboots under it, and
// the gate must not read that as an unhealthy workload, whether it is
// recognised by name or only by label.
func TestOwnRebootPodNeverFailsTheGate(t *testing.T) {
	byName := podFixture{ns: "kube-system", name: actuator.RebootPodName("ehrlitan"), phase: "Failed", containers: 1}
	byLabel := podFixture{ns: "booty", name: "renamed-reboot-pod", phase: "Running", ready: 0, containers: 1, waiting: "Error", labels: map[string]string{actuator.PodNameLabel: actuator.PodNameValue, actuator.PodComponentLabel: actuator.PodComponentReboot}}
	for _, tc := range []struct {
		name string
		pod  podFixture
	}{{"by name", byName}, {"by label", byLabel}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, config.AutopilotGuard)
			h.flatcarHost(macA, "ehrlitan")
			h.healthyNode("ehrlitan", "Flatcar 4757.2.0")
			h.fetch(macA)
			h.up(macA, "4800.0.0")
			h.node("ehrlitan", true, h.time().Add(-10*time.Minute), "Flatcar 4800.0.0", tc.pod, podFixture{ns: "kube-system", name: "cilium-1", owner: "DaemonSet", phase: "Running", ready: 1, containers: 1})
			h.tick()
			e := h.episode(macA)
			if e.State != hardware.AutopilotIdle || !e.Done || e.Class != "" {
				t.Fatalf("Booty's own reboot pod must not fail the gate: %+v (%s)", e, e.Note)
			}
		})
	}

	t.Run("other failed pods still count", func(t *testing.T) {
		h := newHarness(t, config.AutopilotGuard)
		h.flatcarHost(macA, "ehrlitan")
		h.healthyNode("ehrlitan", "Flatcar 4757.2.0")
		h.fetch(macA)
		h.up(macA, "4800.0.0")
		h.node("ehrlitan", true, h.time().Add(-10*time.Minute), "Flatcar 4800.0.0", podFixture{ns: "default", name: "worker-1", owner: "ReplicaSet", phase: "Failed", containers: 1})
		h.tick()
		if e := h.episode(macA); e.State != hardware.AutopilotGating || !strings.Contains(e.Note, "worker-1") {
			t.Fatalf("a Failed workload must still hold the gate: %+v", e)
		}
	})
}

// TestClearHostEndsTheEpisode is the operator's acknowledgement: a host
// stuck in needs-hands, or one the controller is about to reboot again,
// goes idle, loses its pin, stops asking for a reboot and releases the
// fleet hold it caused.
func TestClearHostEndsTheEpisode(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.healthyNode("ehrlitan", "Flatcar 4800.0.0")
	if err := h.c.ClearHost(macA); !errors.Is(err, ErrNoEpisode) {
		t.Fatalf("no episode: %v", err)
	}
	if err := h.c.ClearHost("ff:ff:ff:ff:ff:ff"); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("unknown host: %v", err)
	}

	h.fetch(macA)
	h.up(macA, "4800.0.0", "kubelet.service")
	h.wantState(macA, hardware.AutopilotRetrying, 2, ClassFailedUnits)
	h.wantHold("flatcar", "4757.2.0")
	h.wantPin(macA, "4800.0.0")
	h.act.wait(t, 2)
	if want, _ := h.c.RebootWanted(macA); !want {
		t.Fatal("RebootWanted before the clear")
	}

	if err := h.c.ClearHost(macA); err != nil {
		t.Fatal(err)
	}
	e := h.episode(macA)
	if e == nil || e.State != hardware.AutopilotIdle || !e.Done || e.Attempt != 2 || e.Class != ClassFailedUnits {
		t.Fatalf("cleared episode: %+v", e)
	}
	if want, _ := h.c.RebootWanted(macA); want {
		t.Fatal("RebootWanted must be false after the clear")
	}
	if h.c.Driving(macA) {
		t.Fatal("a cleared host is not driven")
	}
	h.wantPin(macA, "")
	h.wantHold("flatcar", "4800.0.0")
	if r := h.release("flatcar", "4800.0.0"); r.Failing || r.State != ReleaseRolling {
		t.Fatalf("release after the clear: %+v", r)
	}
	if !h.hasEvent("episode cleared by the operator") {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
	host, _ := h.fleet.Host(macA)
	if host.Autopilot == nil || host.Autopilot.State != hardware.AutopilotIdle {
		t.Fatalf("host summary must follow at once: %+v", host.Autopilot)
	}
	if err := h.c.ClearHost(macA); !errors.Is(err, ErrNoEpisode) {
		t.Fatalf("second clear: %v", err)
	}

	h.act.name = actuator.NameKured
	h.fetch(macA)
	h.advance(30 * time.Second)
	h.c.ObserveFetch(macA, FetchKernel)
	h.advance(16 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotNeedsHands, 2, ClassHung)
	if s := h.c.Summary(); s.NeedsHands != 1 {
		t.Fatalf("summary: %+v", s)
	}
	if err := h.c.ClearHost(macA); err != nil {
		t.Fatal(err)
	}
	if s := h.c.Summary(); s.NeedsHands != 0 {
		t.Fatalf("needs-hands must drop after the clear: %+v", s)
	}
	h.wantState(macA, hardware.AutopilotIdle, 2, ClassHung)
}

func TestPendingPodCountsAfterGrace(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.healthyNode("ehrlitan", "Flatcar 4757.2.0")
	h.fetch(macA)
	h.up(macA, "4800.0.0")
	h.node("ehrlitan", true, h.time(), "Flatcar 4800.0.0", podFixture{ns: "default", name: "new-1", owner: "ReplicaSet", phase: "Pending", containers: 1})
	h.tick()
	h.wantState(macA, hardware.AutopilotGating, 1, "")
	if e := h.episode(macA); !strings.Contains(e.Note, "stability") {
		t.Fatalf("a node Ready for less than NodeStable must hold the gate: %q", e.Note)
	}
	h.advance(6 * time.Minute)
	h.tick()
	if e := h.episode(macA); !strings.Contains(e.Note, "Pending for") {
		t.Fatalf("pending pod past grace must block: %q", e.Note)
	}
	h.advance(10 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotRetrying, 2, ClassWorkloads)
}

func TestLastGoodAdvancesOnlyWhenEveryHostIsHealthy(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")
	h.fleet.add(hardware.Host{MAC: macC, Hostname: "never", OS: "flatcar"})
	h.healthyNode("ehrlitan", "Flatcar 4800.0.0")
	h.healthyNode("aren", "Flatcar 4800.0.0")
	h.fetch(macA)
	h.up(macA, "4800.0.0")
	h.tick()
	if e := h.episode(macA); !e.Done || e.State != hardware.AutopilotIdle {
		t.Fatalf("first host: %+v", e)
	}
	if h.fleet.lastGood["flatcar"] != "4757.2.0" {
		t.Fatal("lastGood must wait for the second host")
	}
	h.fetch(macB)
	h.up(macB, "4800.0.0")
	h.tick()
	if h.fleet.lastGood["flatcar"] != "4800.0.0" {
		t.Fatalf("lastGood must advance once every booted host is healthy: %s", h.fleet.lastGood["flatcar"])
	}
	if r := h.release("flatcar", "4800.0.0"); r.State != ReleaseGood {
		t.Fatalf("release: %+v", r)
	}
	h.fetch(macA)
	if e := h.episode(macA); e.Active() {
		t.Fatal("rebooting into the release it passed must not start an episode")
	}
}

func TestBluefinFullCanarySerial(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	add := func(mac, name string, canary bool, role string) {
		h.fleet.add(hardware.Host{MAC: mac, Hostname: name, OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673", Canary: canary, Role: role})
		h.healthyNode(name, "Bluefin Server 26.09.673")
	}
	add(macA, "a", false, "")
	add(macB, "b", true, "")
	add(macC, "c", false, hardware.RoleControlPlane)
	h.fleet.add(hardware.Host{MAC: "aa:bb:cc:dd:ee:09", Hostname: "installed", OS: "bluefin", Mode: hardware.ModeInstalled, Booted: "2026-09-28T10:00:00Z"})

	h.tick()
	h.wantHold("bluefin", "26.09.673")
	h.wantPin(macB, "27.01.100")
	h.wantPin(macA, "")
	h.wantPin(macC, "")
	h.wantState(macB, hardware.AutopilotRolling, 1, "")
	h.act.wait(t, 2)
	h.tick()
	if e := h.episode(macA); e != nil {
		t.Fatal("only one host at a time")
	}
	h.fetch(macB)
	h.up(macB, "27.01.100")
	h.healthyNode("b", "Bluefin Server 27.01.100")
	h.tick()
	h.wantState(macA, hardware.AutopilotRolling, 1, "")
	h.wantPin(macA, "27.01.100")
	h.wantPin(macB, "27.01.100")
	h.fetch(macA)
	h.up(macA, "27.01.100")
	h.healthyNode("a", "Bluefin Server 27.01.100")
	h.tick()
	h.wantState(macC, hardware.AutopilotRolling, 1, "")
	h.fetch(macC)
	h.up(macC, "27.01.100")
	h.healthyNode("c", "Bluefin Server 27.01.100")
	h.tick()
	if h.fleet.lastGood["bluefin"] != "27.01.100" {
		t.Fatalf("lastGood after the rollout: %s", h.fleet.lastGood["bluefin"])
	}
	h.wantHold("bluefin", "27.01.100")
	for _, mac := range []string{macA, macB, macC} {
		h.wantPin(mac, "")
	}
	if e := h.episode("aa:bb:cc:dd:ee:09"); e != nil {
		t.Fatal("installed hosts never take part")
	}
	if writes := h.api.Writes(); len(writes) != 0 {
		t.Fatalf("cluster writes: %v", writes)
	}
}

func TestBluefinFullSkipToNext(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	h.fleet.current["bluefin"], h.fleet.cached["bluefin"] = "27.03.1", []string{"27.03.1", "27.02.5", "27.01.100", "26.09.673"}
	h.fleet.add(hardware.Host{MAC: macA, Hostname: "a", OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673", Canary: true})
	h.healthyNode("a", "Bluefin Server 26.09.673")
	h.tick()
	h.wantPin(macA, "27.03.1")
	h.c.mu.Lock()
	h.c.release("bluefin", "27.03.1").State = ReleaseQuarantined
	h.c.release("bluefin", "27.02.5").State = ReleaseTimeout
	h.c.state.Hosts[macA].Episode.Done = true
	h.c.mu.Unlock()
	h.tick()
	h.wantPin(macA, "27.01.100")
	if e := h.episode(macA); e.Release != "27.01.100" {
		t.Fatalf("skip-to-next must pick the newest release that is neither quarantined nor in timeout: %+v", e)
	}
	h.c.mu.Lock()
	h.c.release("bluefin", "27.01.100").State = ReleaseQuarantined
	h.c.state.Hosts[macA].Episode.Done = true
	h.c.state.Hosts[macA].Pinned = false
	h.c.mu.Unlock()
	if err := h.fleet.Update(macA, func(h *hardware.Host) { h.TargetVersion = "" }); err != nil {
		t.Fatal(err)
	}
	h.tick()
	h.wantPin(macA, "")
	h.wantHold("bluefin", "26.09.673")
}

func TestBluefinFullTimeoutRetriesOnCanary(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	h.fleet.add(hardware.Host{MAC: macA, Hostname: "a", OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673"})
	h.fleet.add(hardware.Host{MAC: macB, Hostname: "b", OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673", Canary: true})
	h.healthyNode("a", "Bluefin Server 26.09.673")
	h.healthyNode("b", "Bluefin Server 26.09.673")
	h.tick()
	h.wantState(macB, hardware.AutopilotRolling, 1, "")
	h.fetch(macB)
	h.up(macB, "27.01.100", "kubelet.service")
	h.fetch(macB)
	h.up(macB, "27.01.100", "kubelet.service")
	h.wantState(macB, hardware.AutopilotRolledBack, 3, ClassFailedUnits)
	h.fetch(macB)
	h.up(macB, "26.09.673")
	h.tick()
	if r := h.release("bluefin", "27.01.100"); r.State != ReleaseTimeout {
		t.Fatalf("release: %+v", r)
	}
	h.tick()
	if e := h.episode(macA); e != nil {
		t.Fatal("rollout must pause while the release is in TIMEOUT")
	}
	h.advance(61 * time.Minute)
	h.tick()
	h.wantState(macB, hardware.AutopilotRetrying, 1, "")
	if e := h.episode(macB); !e.Retry {
		t.Fatalf("retry must run on the canary: %+v", e)
	}
	h.fetch(macB)
	h.up(macB, "27.01.100")
	h.healthyNode("b", "Bluefin Server 27.01.100")
	h.tick()
	if r := h.release("bluefin", "27.01.100"); r.State != ReleaseRolling {
		t.Fatalf("healthy retry must resume the rollout: %+v", r)
	}
	h.tick()
	h.wantState(macA, hardware.AutopilotRolling, 1, "")
}

func TestRestartResumesGate(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.healthyNode("ehrlitan", "Flatcar 4800.0.0")
	h.fetch(macA)
	h.up(macA, "4800.0.0", "x.service")
	h.wantHold("flatcar", "4757.2.0")
	t0 := h.episode(macA).Since
	h.fleet.Hold("flatcar", "")
	h.advance(time.Minute)
	h.start()
	h.wantHold("flatcar", "4757.2.0")
	e := h.episode(macA)
	if e == nil || e.State != hardware.AutopilotRetrying || !e.Since.Equal(t0) {
		t.Fatalf("resumed episode: %+v", e)
	}
	h.fetch(macA)
	h.up(macA, "4800.0.0")
	h.tick()
	if e := h.episode(macA); !e.Done || e.State != hardware.AutopilotIdle {
		t.Fatalf("gate after restart: %+v", e)
	}
	if r := h.release("flatcar", "4800.0.0"); r.Failing {
		t.Fatal("a healthy attempt 2 clears the hold")
	}
	h.wantHold("flatcar", "4800.0.0")
}

func TestNoActuatorStillGatesAndRollsBack(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.opts.Actuators = nil
	h.start()
	h.flatcarHost(macA, "ehrlitan")
	h.fetch(macA)
	h.up(macA, "4800.0.0", "x.service")
	h.wantState(macA, hardware.AutopilotRetrying, 2, ClassFailedUnits)
	if !h.hasEvent("no actuator configured") {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
	if want, _ := h.c.RebootWanted(macA); !want {
		t.Fatal("update-check must still ask for the reboot")
	}
	h.fetch(macA)
	h.up(macA, "4800.0.0", "x.service")
	h.wantPin(macA, "4757.2.0")
	h.wantHold("flatcar", "4757.2.0")
	if h.c.Actuator() != ActuatorNone {
		t.Fatalf("actuator %q", h.c.Actuator())
	}
}

func TestEventsCarryNoPII(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.fleet.hosts[macA].IP = "10.9.8.7"
	h.healthyNode("ehrlitan", "Flatcar 4800.0.0")
	h.fetch(macA)
	h.up(macA, "4800.0.0", "kubelet.service")
	h.fetch(macA)
	h.up(macA, "4800.0.0", "kubelet.service")
	h.fetch(macA)
	h.up(macA, "4757.2.0")
	h.tick()
	events := h.c.Status().Events
	if len(events) < 8 {
		t.Fatalf("expected a full timeline, got %d events", len(events))
	}
	for _, e := range events {
		for _, bad := range []string{"ehrlitan", "10.9.8.7", macA, "aa:bb"} {
			if strings.Contains(e.Text, bad) {
				t.Fatalf("event text carries %q: %q", bad, e.Text)
			}
		}
	}
}

func TestHostBootingSomethingElseGatesWhatItBoots(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.fetch(macA)
	h.up(macA, "4800.0.0", "x.service")
	h.fleet.cached["flatcar"] = []string{"4900.0.0", "4800.0.0", "4757.2.0"}
	if err := h.fleet.Update(macA, func(h *hardware.Host) { h.TargetVersion = "4900.0.0" }); err != nil {
		t.Fatal(err)
	}
	h.fetch(macA)
	if e := h.episode(macA); e.Target != "4900.0.0" || e.State != hardware.AutopilotGating {
		t.Fatalf("episode: %+v", e)
	}
}

func TestNodeStableIsCappedAtHalfTheWindow(t *testing.T) {
	for _, tc := range []struct{ window, want time.Duration }{
		{15 * time.Minute, 5 * time.Minute},
		{10 * time.Minute, 5 * time.Minute},
		{6 * time.Minute, 3 * time.Minute},
		{2 * time.Minute, time.Minute},
	} {
		o := Options{HealthWindow: tc.window}
		o.defaults()
		if o.NodeStable != tc.want {
			t.Errorf("window %s: NodeStable = %s, want %s", tc.window, o.NodeStable, tc.want)
		}
	}
	o := Options{HealthWindow: 6 * time.Minute, NodeStable: 4 * time.Minute}
	o.defaults()
	if o.NodeStable != 4*time.Minute {
		t.Errorf("an explicit NodeStable must be kept, got %s", o.NodeStable)
	}
}

// A 6-minute window with a node that needs 90 s to come up must pass the
// gate: with the uncapped 5-minute stability it never could.
func TestShortWindowGatePasses(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.opts.HealthWindow = 6 * time.Minute
	h.start()
	h.flatcarHost(macA, "ehrlitan")
	h.healthyNode("ehrlitan", "Flatcar 4757.2.0")
	h.fetch(macA)
	h.advance(90 * time.Second)
	h.up(macA, "4800.0.0")
	h.node("ehrlitan", true, h.time(), "Flatcar 4800.0.0")
	h.tick()
	h.wantState(macA, hardware.AutopilotGating, 1, "")
	h.advance(3 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotIdle, 1, "")
	if got := h.release("flatcar", "4800.0.0").Healthy; len(got) != 1 || got[0] != macA {
		t.Fatalf("healthy list: %v", got)
	}
}

// A release landing under full must not move a non-canary Bluefin host's
// effective target before the controller's next tick: the hold is a
// policy of the fleet, not a state the tick applies later.
func TestBluefinFullHoldIsImmediateOnRelease(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	h.fleet.current["bluefin"], h.fleet.lastGood["bluefin"], h.fleet.cached["bluefin"] = "26.09.673", "26.09.673", []string{"26.09.673"}
	h.fleet.add(hardware.Host{MAC: macA, Hostname: "a", OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673"})
	h.fleet.add(hardware.Host{MAC: macB, Hostname: "b", OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673", Canary: true})
	h.tick()
	if got := h.c.Status().OS["bluefin"]; got.Held || got.FleetTarget != "26.09.673" {
		t.Fatalf("idle fleet: %+v", got)
	}
	h.fleet.current["bluefin"], h.fleet.cached["bluefin"] = "26.09.674", []string{"26.09.674", "26.09.673"}
	hostA, _ := h.fleet.Host(macA)
	if got := h.fleet.EffectiveTarget(hostA); got != "26.09.673" {
		t.Fatalf("before the tick a non-canary host must still boot lastGood, got %s", got)
	}
	h.tick()
	h.wantHold("bluefin", "26.09.673")
	h.wantPin(macB, "26.09.674")
	h.wantPin(macA, "")
	hostA, _ = h.fleet.Host(macA)
	if got := h.fleet.EffectiveTarget(hostA); got != "26.09.673" {
		t.Fatalf("after the tick: %s", got)
	}
}
