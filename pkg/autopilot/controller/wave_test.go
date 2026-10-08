package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

func (h *harness) paced(soak, cooldown time.Duration) {
	h.t.Helper()
	h.opts.Soak, h.opts.Cooldown = soak, cooldown
	h.start()
}

func (h *harness) bluefinHost(mac, name string, canary bool) {
	h.fleet.add(hardware.Host{MAC: mac, Hostname: name, OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673", Canary: canary})
	h.healthyNode(name, "Bluefin Server 26.09.673")
}

func (h *harness) flatcarCanary(mac, name string) {
	h.fleet.add(hardware.Host{MAC: mac, Hostname: name, IP: "192.168.1.5", OS: "flatcar", Booted: "2026-09-28T10:00:00Z", Running: "4757.2.0", Canary: true})
}

// pass boots mac into v and lets the gate close on it: fetch, /booted +
// /health, a healthy node on v, one tick.
func (h *harness) pass(mac, name, v string) {
	h.t.Helper()
	h.fetch(mac)
	h.up(mac, v)
	h.healthyNode(name, hostOSName(h, mac)+" "+v)
	h.tick()
	if e := h.episode(mac); e == nil || !e.Done || e.State != hardware.AutopilotIdle {
		h.t.Fatalf("%s did not pass on %s: %+v", mac, v, e)
	}
}

func hostOSName(h *harness, mac string) string {
	host, _ := h.fleet.Host(mac)
	if hostOS(host) == "bluefin" {
		return "Bluefin Server"
	}
	return "Flatcar"
}

func (h *harness) wave(os string) *Wave { return h.c.Status().OS[os].Wave }

func (h *harness) wantWave(os, release, outcome string) {
	h.t.Helper()
	w := h.wave(os)
	if w == nil {
		h.t.Fatalf("%s: no wave, want %s %s", os, release, outcome)
	}
	if w.Release != release || w.Outcome != outcome || (outcome == WaveRolling) != w.EndedAt.IsZero() {
		h.t.Fatalf("%s wave %+v, want %s %s", os, w, release, outcome)
	}
}

func (h *harness) wantNoWave(os string) {
	h.t.Helper()
	if w := h.wave(os); w != nil {
		h.t.Fatalf("%s: unexpected wave %+v", os, w)
	}
}

func (h *harness) wantNoEpisode(mac string) {
	h.t.Helper()
	if e := h.episode(mac); e.Active() {
		h.t.Fatalf("%s: unexpected active episode %+v", mac, e)
	}
}

func (h *harness) countEvents(sub string) int {
	n := 0
	for _, e := range h.c.StatusWith(StatusOptions{AllEvents: true}).Events {
		if strings.Contains(e.Text, sub) {
			n++
		}
	}
	return n
}

// TestUnpacedGuardPinsNothingForRollouts is the compatibility guarantee:
// with both clocks at zero a canary is just a host, nothing is pinned,
// no wave exists and the hardware map is never written.
func TestUnpacedGuardPinsNothingForRollouts(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarCanary(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")
	h.tick()
	h.tick()
	h.wantPin(macA, "")
	h.wantPin(macB, "")
	h.wantNoWave("flatcar")
	h.wantHold("flatcar", "4800.0.0")
	if h.fleet.updates != 0 {
		t.Fatalf("an unpaced guard must not write the hardware map: %d updates", h.fleet.updates)
	}
	if h.fleet.serial["flatcar"] {
		t.Fatal("an unpaced OS keeps the plain fleet target")
	}
	if st := h.c.Status().OS["flatcar"]; st.Canaries != 1 || !st.NextWaveAt.IsZero() {
		t.Fatalf("status: %+v", st)
	}
}

// Test 1: full/Bluefin paced. Only the canary moves when R lands; the
// workers wait for the soak, then move one at a time in a wave.
func TestPacedBluefinFullCanaryThenWave(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	h.paced(24*time.Hour, 48*time.Hour)
	h.bluefinHost(macA, "a", true)
	h.bluefinHost(macB, "b", false)
	h.bluefinHost(macC, "c", false)
	const r = "27.01.100"

	h.tick()
	h.wantHold("bluefin", "26.09.673")
	h.wantState(macA, hardware.AutopilotRolling, 1, "")
	h.wantPin(macA, r)
	h.wantPin(macB, "")
	h.wantPin(macC, "")
	h.wantNoEpisode(macB)
	h.wantNoWave("bluefin")
	h.act.wait(t, 2)

	h.pass(macA, "a", r)
	rel := h.release("bluefin", r)
	if !rel.FirstHealthyAt.Equal(h.time()) || rel.Soaked {
		t.Fatalf("release after the canary passed: %+v", rel)
	}
	h.wantNoEpisode(macB)
	h.wantNoWave("bluefin")
	h.wantPin(macA, r)
	if h.hasEvent("release soaked") {
		t.Fatal("not soaked yet")
	}

	h.advance(23 * time.Hour)
	h.tick()
	h.wantNoWave("bluefin")
	h.advance(time.Hour)
	h.tick()
	if !h.hasEvent("release soaked: healthy on a canary for 24h0m0s; eligible for the next wave") {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
	if !h.hasEvent("wave started into " + r + " (2 host(s)); next wave not before " + h.time().Add(48*time.Hour).Format(time.RFC3339)) {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
	h.wantWave("bluefin", r, WaveRolling)
	if !h.release("bluefin", r).Soaked {
		t.Fatal("soaked must show in the status")
	}
	h.wantState(macB, hardware.AutopilotRolling, 1, "")
	h.wantPin(macB, r)
	h.wantNoEpisode(macC)

	h.pass(macB, "b", r)
	h.wantState(macC, hardware.AutopilotRolling, 1, "")
	h.wantPin(macC, r)
	h.wantWave("bluefin", r, WaveRolling)

	h.pass(macC, "c", r)
	if h.fleet.lastGood["bluefin"] != r {
		t.Fatalf("lastGood after the wave: %s", h.fleet.lastGood["bluefin"])
	}
	h.wantWave("bluefin", r, WaveDone)
	if !h.hasEvent("wave done: every host is on " + r) {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
	for _, mac := range []string{macA, macB, macC} {
		h.wantPin(mac, "")
	}
	h.wantHold("bluefin", r)
	if rel := h.release("bluefin", r); rel.State != ReleaseGood {
		t.Fatalf("release: %+v", rel)
	}
	if writes := h.api.Writes(); len(writes) != 0 {
		t.Fatalf("cluster writes: %v", writes)
	}
}

// Test 2: skip-ahead. Two releases land during the cooldown, the canary
// moves to each, the wave targets the newest soaked one and the one the
// fleet never moved to is skipped.
func TestPacedWaveSkipsAheadToTheNewestSoakedRelease(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	h.paced(24*time.Hour, 48*time.Hour)
	h.bluefinHost(macA, "a", true)
	h.bluefinHost(macB, "b", false)
	h.bluefinHost(macC, "c", false)
	const r1, r2, r3 = "27.01.100", "27.02.0", "27.03.0"

	h.tick()
	h.act.wait(t, 2)
	h.pass(macA, "a", r1)
	h.advance(24 * time.Hour)
	h.tick()
	h.wantWave("bluefin", r1, WaveRolling)
	waveStart := h.time()
	h.pass(macB, "b", r1)
	h.pass(macC, "c", r1)
	h.wantWave("bluefin", r1, WaveDone)
	if h.fleet.lastGood["bluefin"] != r1 {
		t.Fatalf("lastGood: %s", h.fleet.lastGood["bluefin"])
	}

	h.fleet.current["bluefin"], h.fleet.cached["bluefin"] = r2, []string{r2, r1, "26.09.673"}
	h.tick()
	h.wantState(macA, hardware.AutopilotRolling, 1, "")
	h.wantPin(macA, r2)
	h.pass(macA, "a", r2)
	h.wantWave("bluefin", r1, WaveDone)
	h.wantNoEpisode(macB)

	h.advance(24 * time.Hour)
	h.fleet.current["bluefin"], h.fleet.cached["bluefin"] = r3, []string{r3, r2, r1, "26.09.673"}
	h.tick()
	h.wantPin(macA, r3)
	h.pass(macA, "a", r3)
	if rel := h.release("bluefin", r2); rel.Soaked {
		t.Fatal("a canary that moved on un-soaks the release it left")
	}
	h.wantWave("bluefin", r1, WaveDone)

	h.advance(24 * time.Hour)
	h.tick()
	if got := h.time().Sub(waveStart); got != 48*time.Hour {
		t.Fatalf("test clock: %s since wave 1", got)
	}
	h.wantWave("bluefin", r3, WaveRolling)
	h.wantState(macB, hardware.AutopilotRolling, 1, "")
	h.wantPin(macB, r3)
	h.pass(macB, "b", r3)
	h.pass(macC, "c", r3)
	if h.fleet.lastGood["bluefin"] != r3 {
		t.Fatalf("lastGood: %s", h.fleet.lastGood["bluefin"])
	}
	if rel := h.release("bluefin", r2); rel.State != ReleaseSkipped {
		t.Fatalf("the release the fleet never moved to is skipped: %+v", rel)
	}
	if !h.hasEvent("release skipped: the fleet moved to " + r3 + " without it") {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
	if rel := h.release("bluefin", r3); rel.State != ReleaseGood {
		t.Fatalf("%+v", rel)
	}
	h.wantWave("bluefin", r3, WaveDone)
}

// Test 3: the cooldown spaces waves from the previous wave's start, and
// nextWaveAt says until when.
func TestPacedCooldownSpacesWaves(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.paced(0, 48*time.Hour)
	h.flatcarHost(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")

	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveRolling)
	h.wantPin(macA, "4800.0.0")
	h.wantPin(macB, "4800.0.0")
	h.wantHold("flatcar", "4757.2.0")
	start := h.time()
	h.pass(macA, "ehrlitan", "4800.0.0")
	h.pass(macB, "aren", "4800.0.0")
	h.wantWave("flatcar", "4800.0.0", WaveDone)
	h.wantPin(macA, "")
	h.wantHold("flatcar", "4800.0.0")

	h.advance(time.Hour)
	h.fleet.current["flatcar"], h.fleet.cached["flatcar"] = "4900.0.0", []string{"4900.0.0", "4800.0.0", "4757.2.0"}
	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveDone)
	h.wantPin(macA, "")
	h.wantHold("flatcar", "4800.0.0")
	if got := h.c.Status().OS["flatcar"].NextWaveAt; !got.Equal(start.Add(48 * time.Hour)) {
		t.Fatalf("nextWaveAt %s, want %s", got, start.Add(48*time.Hour))
	}
	h.advance(46 * time.Hour)
	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveDone)
	h.wantPin(macB, "")
	h.advance(time.Hour)
	h.tick()
	h.wantWave("flatcar", "4900.0.0", WaveRolling)
	h.wantPin(macA, "4900.0.0")
	h.wantPin(macB, "4900.0.0")
}

// Test 4: a canary that fails on R keeps R from ever soaking; the next
// release soaks normally.
func TestPacedBadCanaryNeverSoaks(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	h.paced(24*time.Hour, 0)
	h.bluefinHost(macA, "a", true)
	h.bluefinHost(macB, "b", false)
	const r1, r2 = "27.01.100", "27.02.0"

	h.tick()
	h.act.wait(t, 2)
	h.fetch(macA)
	h.up(macA, r1, "kubelet.service")
	h.fetch(macA)
	h.up(macA, r1, "kubelet.service")
	h.wantState(macA, hardware.AutopilotRolledBack, 3, ClassFailedUnits)
	h.fetch(macA)
	h.up(macA, "26.09.673")
	h.tick()
	if rel := h.release("bluefin", r1); rel.State != ReleaseTimeout || rel.Soaked {
		t.Fatalf("release: %+v", rel)
	}
	h.wantNoWave("bluefin")
	h.wantNoEpisode(macB)

	h.advance(25 * time.Hour)
	h.tick()
	h.wantState(macA, hardware.AutopilotRetrying, 1, "")
	h.wantNoWave("bluefin")
	h.fetch(macA)
	h.up(macA, r1, "kubelet.service")
	if rel := h.release("bluefin", r1); rel.State != ReleaseQuarantined {
		t.Fatalf("release: %+v", rel)
	}
	h.fetch(macA)
	h.up(macA, "26.09.673")
	h.tick()
	h.wantNoWave("bluefin")
	h.wantNoEpisode(macB)

	h.fleet.current["bluefin"], h.fleet.cached["bluefin"] = r2, []string{r2, r1, "26.09.673"}
	h.tick()
	h.wantState(macA, hardware.AutopilotRolling, 1, "")
	h.wantPin(macA, r2)
	h.pass(macA, "a", r2)
	h.wantNoWave("bluefin")
	h.advance(24 * time.Hour)
	h.tick()
	h.wantWave("bluefin", r2, WaveRolling)
	h.wantState(macB, hardware.AutopilotRolling, 1, "")
	if !h.release("bluefin", r2).Soaked {
		t.Fatal("the next release soaks normally")
	}
}

// Test 5: without a canary the soak has no effect (warned once per
// release); the cooldown still spaces the waves.
func TestPacedNoCanaryWarnsOnceAndWavesAtOnce(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.paced(24*time.Hour, 48*time.Hour)
	h.flatcarHost(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")
	const warn = "no canary registered for flatcar; --autopilotSoak has no effect on it"

	h.tick()
	h.tick()
	if n := h.countEvents(warn); n != 1 {
		t.Fatalf("warned %d times, want once: %+v", n, h.c.Status().Events)
	}
	h.wantWave("flatcar", "4800.0.0", WaveRolling)
	h.wantPin(macA, "4800.0.0")
	h.wantPin(macB, "4800.0.0")
	if h.hasEvent("release soaked") {
		t.Fatal("no soaked event without a canary")
	}
	h.pass(macA, "ehrlitan", "4800.0.0")
	h.pass(macB, "aren", "4800.0.0")
	h.wantWave("flatcar", "4800.0.0", WaveDone)

	h.fleet.current["flatcar"], h.fleet.cached["flatcar"] = "4900.0.0", []string{"4900.0.0", "4800.0.0", "4757.2.0"}
	h.tick()
	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveDone)
	if n := h.countEvents(warn); n != 2 {
		t.Fatalf("one warning per release: %d", n)
	}
	h.advance(48 * time.Hour)
	h.tick()
	h.wantWave("flatcar", "4900.0.0", WaveRolling)
	if n := h.countEvents(warn); n != 2 {
		t.Fatalf("one warning per release: %d", n)
	}
}

// Test 6: a canary already on R3 does not keep lastGood from advancing to
// R2 once the rest of the fleet is there.
func TestPacedCanaryAheadDoesNotBlockLastGood(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	h.paced(24*time.Hour, 0)
	h.bluefinHost(macA, "a", true)
	h.bluefinHost(macB, "b", false)
	h.bluefinHost(macC, "c", false)
	const r2, r3 = "27.01.100", "27.02.0"

	h.tick()
	h.act.wait(t, 2)
	h.pass(macA, "a", r2)
	h.advance(24 * time.Hour)
	h.tick()
	h.wantWave("bluefin", r2, WaveRolling)
	h.wantState(macB, hardware.AutopilotRolling, 1, "")

	h.fleet.current["bluefin"], h.fleet.cached["bluefin"] = r3, []string{r3, r2, "26.09.673"}
	h.pass(macB, "b", r2)
	h.wantState(macA, hardware.AutopilotRolling, 1, "")
	h.wantPin(macA, r3)
	h.wantNoEpisode(macC)
	h.pass(macA, "a", r3)
	h.wantState(macC, hardware.AutopilotRolling, 1, "")
	h.wantPin(macC, r2)
	h.pass(macC, "c", r2)
	if h.fleet.lastGood["bluefin"] != r2 {
		t.Fatalf("lastGood must advance to R2 with the canary on R3: %s", h.fleet.lastGood["bluefin"])
	}
	h.wantWave("bluefin", r2, WaveDone)
	if rel := h.release("bluefin", r3); rel.State != ReleaseRolling || rel.Soaked {
		t.Fatalf("R3: %+v", rel)
	}
	h.wantPin(macA, r3)
	h.wantPin(macB, "")
	h.wantPin(macC, "")
}

// Test 7: a release going bad mid-wave aborts the wave; idle pinned hosts
// fall back to the held lastGood and the next wave waits for the cooldown
// from this wave's start.
func TestPacedWaveAbortsWhenTheReleaseGoesBad(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.paced(0, 48*time.Hour)
	h.act.name = actuator.NameKured
	h.flatcarHost(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")
	h.healthyNode("ehrlitan", "Flatcar 4757.2.0")
	h.healthyNode("aren", "Flatcar 4757.2.0")

	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveRolling)
	start := h.time()
	h.wantPin(macA, "4800.0.0")
	h.wantPin(macB, "4800.0.0")

	h.fetch(macB)
	h.up(macB, "4800.0.0", "kubelet.service")
	h.advance(time.Minute)
	h.fetch(macB)
	h.up(macB, "4800.0.0", "kubelet.service")
	h.wantState(macB, hardware.AutopilotRolledBack, 3, ClassFailedUnits)
	h.wantPin(macB, "4757.2.0")
	h.wantPin(macA, "4800.0.0")
	h.advance(time.Minute)
	h.fetch(macB)
	h.up(macB, "4757.2.0")
	h.tick()
	if rel := h.release("flatcar", "4800.0.0"); rel.State != ReleaseTimeout {
		t.Fatalf("release: %+v", rel)
	}
	h.wantWave("flatcar", "4800.0.0", WaveAborted)
	if !h.hasEvent("wave aborted: 4800.0.0 is timeout") {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
	h.wantPin(macA, "")
	h.wantNoEpisode(macA)
	h.wantHold("flatcar", "4757.2.0")
	hostA, _ := h.fleet.Host(macA)
	if got := h.fleet.EffectiveTarget(hostA); got != "4757.2.0" {
		t.Fatalf("an unpinned idle host falls back to lastGood, got %s", got)
	}

	h.advance(61 * time.Minute)
	h.tick()
	h.wantState(macB, hardware.AutopilotRetrying, 1, "")
	h.fetch(macB)
	h.up(macB, "4800.0.0")
	h.healthyNode("aren", "Flatcar 4800.0.0")
	h.tick()
	if rel := h.release("flatcar", "4800.0.0"); rel.State != ReleaseRolling {
		t.Fatalf("release after the healthy retry: %+v", rel)
	}
	h.wantWave("flatcar", "4800.0.0", WaveAborted)
	h.wantPin(macA, "")
	if got := h.c.Status().OS["flatcar"].NextWaveAt; !got.Equal(start.Add(48 * time.Hour)) {
		t.Fatalf("nextWaveAt %s, want %s", got, start.Add(48*time.Hour))
	}
	h.advance(48 * time.Hour)
	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveRolling)
	h.wantPin(macA, "4800.0.0")
	if !h.hasEvent("wave started into 4800.0.0 (1 host(s))") {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
}

// Test 8: guard, Flatcar paced. The canary is pinned and kured reboots
// it; the wave pins every worker at once.
func TestPacedGuardFlatcarPinsCanaryThenWave(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.paced(24*time.Hour, 48*time.Hour)
	h.act.name = actuator.NameKured
	h.flatcarCanary(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")
	h.flatcarHost(macC, "pale")

	h.tick()
	h.wantPin(macA, "4800.0.0")
	h.wantPin(macB, "")
	h.wantPin(macC, "")
	h.wantHold("flatcar", "4757.2.0")
	h.wantNoEpisode(macA)
	h.wantNoWave("flatcar")
	if !h.fleet.serial["flatcar"] {
		t.Fatal("a paced OS gets the serial fleet-target policy")
	}
	hostB, _ := h.fleet.Host(macB)
	if got := h.fleet.EffectiveTarget(hostB); got != "4757.2.0" {
		t.Fatalf("workers boot lastGood, got %s", got)
	}
	updates := h.fleet.updates
	h.tick()
	if h.fleet.updates != updates {
		t.Fatal("a quiet tick must not re-pin")
	}

	h.fetch(macA)
	h.wantState(macA, hardware.AutopilotGating, 1, "")
	h.pass(macA, "ehrlitan", "4800.0.0")
	first := h.time()
	if rel := h.release("flatcar", "4800.0.0"); !rel.FirstHealthyAt.Equal(first) || rel.Soaked {
		t.Fatalf("release: %+v", rel)
	}
	h.wantPin(macA, "4800.0.0")
	h.wantNoWave("flatcar")

	h.advance(24 * time.Hour)
	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveRolling)
	h.wantPin(macB, "4800.0.0")
	h.wantPin(macC, "4800.0.0")
	h.wantNoEpisode(macB)
	if err := h.fleet.Update(macC, func(x *hardware.Host) { x.TargetVersion = "" }); err != nil {
		t.Fatal(err)
	}
	h.tick()
	h.wantPin(macC, "4800.0.0")

	h.fetch(macB)
	h.wantState(macB, hardware.AutopilotGating, 1, "")
	h.pass(macB, "aren", "4800.0.0")
	h.wantWave("flatcar", "4800.0.0", WaveRolling)
	h.pass(macC, "pale", "4800.0.0")
	if h.fleet.lastGood["flatcar"] != "4800.0.0" {
		t.Fatalf("lastGood: %s", h.fleet.lastGood["flatcar"])
	}
	h.wantWave("flatcar", "4800.0.0", WaveDone)
	for _, mac := range []string{macA, macB, macC} {
		h.wantPin(mac, "")
	}
	h.wantHold("flatcar", "4800.0.0")
	if got := h.c.Status().OS["flatcar"]; got.Held {
		t.Fatalf("hold released once lastGood = current: %+v", got)
	}
}

// Test 9: the wave and firstHealthyAt survive a restart, and the cooldown
// is honoured across it.
func TestPacedStateSurvivesRestart(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.paced(24*time.Hour, 48*time.Hour)
	h.act.name = actuator.NameKured
	h.flatcarCanary(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")

	h.tick()
	h.fetch(macA)
	h.pass(macA, "ehrlitan", "4800.0.0")
	first := h.time()
	h.advance(24 * time.Hour)
	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveRolling)
	start := h.time()
	h.fleet.Hold("flatcar", "")

	h.advance(time.Minute)
	h.start()
	h.wantWave("flatcar", "4800.0.0", WaveRolling)
	if w := h.wave("flatcar"); !w.StartedAt.Equal(start) {
		t.Fatalf("wave after restart: %+v", w)
	}
	if rel := h.release("flatcar", "4800.0.0"); !rel.FirstHealthyAt.Equal(first) || !rel.Soaked || rel.SoakedAt.IsZero() {
		t.Fatalf("release after restart: %+v", rel)
	}
	h.wantHold("flatcar", "4757.2.0")
	if !h.fleet.serial["flatcar"] {
		t.Fatal("the serial policy is re-applied on load")
	}
	h.wantPin(macB, "4800.0.0")

	h.pass(macB, "aren", "4800.0.0")
	h.wantWave("flatcar", "4800.0.0", WaveDone)
	h.fleet.current["flatcar"], h.fleet.cached["flatcar"] = "4900.0.0", []string{"4900.0.0", "4800.0.0", "4757.2.0"}
	h.advance(time.Hour)
	h.start()
	h.tick()
	h.wantPin(macA, "4900.0.0")
	h.pass(macA, "ehrlitan", "4900.0.0")
	h.advance(24 * time.Hour)
	h.tick()
	h.wantWave("flatcar", "4800.0.0", WaveDone)
	if got := h.c.Status().OS["flatcar"].NextWaveAt; !got.Equal(start.Add(48 * time.Hour)) {
		t.Fatalf("the cooldown is honoured across the restart: nextWaveAt %s, want %s", got, start.Add(48*time.Hour))
	}
	if n := h.countEvents("release soaked"); n != 2 {
		t.Fatalf("one soaked event per release, also across restarts: %d", n)
	}
}

// Test 10: the status carries wave, nextWaveAt, canaries, firstHealthyAt,
// soaked and the skipped state as the plan specifies them.
func TestPacedStatusFields(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.paced(24*time.Hour, 48*time.Hour)
	h.act.name = actuator.NameKured
	h.flatcarCanary(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")
	h.seedRelease("flatcar", "4790.0.0", ReleaseRolling, h.time().Add(-time.Hour), macA)

	h.tick()
	h.fetch(macA)
	h.pass(macA, "ehrlitan", "4800.0.0")
	h.advance(24 * time.Hour)
	h.tick()
	h.pass(macB, "aren", "4800.0.0")

	raw, err := json.Marshal(h.c.Status())
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		OS map[string]struct {
			Wave *struct {
				Release   string    `json:"release"`
				StartedAt time.Time `json:"startedAt"`
				EndedAt   time.Time `json:"endedAt"`
				Outcome   string    `json:"outcome"`
			} `json:"wave"`
			NextWaveAt time.Time `json:"nextWaveAt"`
			Canaries   int       `json:"canaries"`
			Releases   []struct {
				Version        string    `json:"version"`
				State          string    `json:"state"`
				FirstHealthyAt time.Time `json:"firstHealthyAt"`
				Soaked         bool      `json:"soaked"`
				Cached         bool      `json:"cached"`
			} `json:"releases"`
		} `json:"os"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	fc := st.OS["flatcar"]
	if fc.Wave == nil || fc.Wave.Release != "4800.0.0" || fc.Wave.Outcome != WaveDone || fc.Wave.StartedAt.IsZero() || fc.Wave.EndedAt.IsZero() {
		t.Fatalf("wave: %+v", fc.Wave)
	}
	if fc.NextWaveAt.IsZero() || fc.Canaries != 1 {
		t.Fatalf("nextWaveAt %s canaries %d", fc.NextWaveAt, fc.Canaries)
	}
	byVersion := map[string]int{}
	for i, r := range fc.Releases {
		byVersion[r.Version] = i
	}
	cur := fc.Releases[byVersion["4800.0.0"]]
	if cur.FirstHealthyAt.IsZero() || !cur.Soaked || cur.State != ReleaseGood {
		t.Fatalf("current release: %+v", cur)
	}
	skipped := fc.Releases[byVersion["4790.0.0"]]
	if skipped.State != ReleaseSkipped || skipped.Cached {
		t.Fatalf("skipped release is listed with the history: %+v", skipped)
	}
	if strings.Contains(string(raw), `"soakWarned"`) {
		t.Fatal("no warning flag without a warning")
	}
	bf := st.OS["bluefin"]
	if bf.Wave != nil || !bf.NextWaveAt.IsZero() || bf.Canaries != 0 {
		t.Fatalf("an OS without hosts: %+v", bf)
	}
}
