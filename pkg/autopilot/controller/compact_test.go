package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
)

// seedRelease plants a release record the way days of rollouts would
// have left it: hosts that passed, the host that blamed it, attempts.
func (h *harness) seedRelease(osName, version, state string, since time.Time, healthy ...string) *Release {
	h.t.Helper()
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	r := &Release{OS: osName, Version: version, State: state, Since: since, Attempts: len(healthy), Healthy: healthy}
	h.c.state.osState(osName).Releases[version] = r
	h.c.dirty = true
	return r
}

func (h *harness) record(osName, version string) *Release {
	h.t.Helper()
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return h.c.state.osState(osName).Releases[version]
}

func (h *harness) recordCount(osName string) int {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return len(h.c.state.osState(osName).Releases)
}

func (h *harness) revision() uint64 {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return h.c.state.Revision
}

func TestCompactionFoldsGoodHistoryAndKeepsVerdicts(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	t0 := h.time().Add(-72 * time.Hour)
	folded := h.seedRelease("bluefin", "26.09.600", ReleaseGood, t0, macA, macB)
	folded.FailedOn = macC
	folded.Class = ClassFailedUnits
	quarantined := h.seedRelease("bluefin", "26.09.650", ReleaseQuarantined, t0.Add(time.Hour), macA)
	quarantined.FailedOn = macB
	timedOut := h.seedRelease("bluefin", "26.09.660", ReleaseTimeout, h.time(), macA)
	timedOut.FailedOn, timedOut.Retried = macB, true
	reported := h.seedRelease("bluefin", "26.09.640", ReleaseGood, t0.Add(2*time.Hour), macA, macB)
	reported.Report = "bluefin-26.09.640"
	lastGood := h.seedRelease("bluefin", "26.09.673", ReleaseGood, t0.Add(3*time.Hour), macA, macB)
	rolling := h.seedRelease("bluefin", "26.09.500", ReleaseRolling, t0, macA)

	h.tick()

	if r := h.record("bluefin", "26.09.600"); r == nil || r.Healthy != nil || r.FailedOn != "" {
		t.Fatalf("a good record without files is folded (host lists dropped): %+v", r)
	} else if r.State != ReleaseGood || !r.Since.Equal(t0) || r.Attempts != 2 || r.Class != ClassFailedUnits {
		t.Fatalf("folding keeps version, state, since, attempts and class: %+v", r)
	}
	for _, r := range []*Release{quarantined, timedOut, reported, lastGood, rolling} {
		got := h.record("bluefin", r.Version)
		if got == nil || len(got.Healthy) != len(r.Healthy) || got.FailedOn != r.FailedOn {
			t.Fatalf("%s %s must be kept intact, got %+v", r.State, r.Version, got)
		}
	}
	if n := h.recordCount("bluefin"); n != 7 {
		t.Fatalf("every record is still there (the six seeded plus current): %d", n)
	}
}

func TestCompactionCapsGoodHistoryAndRunsOnlyOnChange(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	t0 := h.time().Add(-100 * 24 * time.Hour)
	for i := range 60 {
		h.seedRelease("bluefin", fmt.Sprintf("26.08.%d", 100+i), ReleaseGood, t0.Add(time.Duration(i)*24*time.Hour), macA, macB)
	}
	keeper := h.seedRelease("bluefin", "26.08.50", ReleaseGood, t0.Add(-time.Hour), macA)
	keeper.Report = "bluefin-26.08.50"
	h.seedRelease("bluefin", "26.08.40", ReleaseQuarantined, t0.Add(-2*time.Hour), macA)

	h.tick()

	if n := h.recordCount("bluefin"); n != maxGoodHistory+3 {
		t.Fatalf("the good records without files are capped at %d (49 folded plus the reported one), the quarantined one and current/lastGood stay: %d records", maxGoodHistory, n)
	}
	for i := range 11 {
		if h.record("bluefin", fmt.Sprintf("26.08.%d", 100+i)) != nil {
			t.Fatalf("the 11 oldest folded records are dropped (the reported one counts toward the cap), 26.08.%d is still there", 100+i)
		}
	}
	if h.record("bluefin", "26.08.111") == nil || h.record("bluefin", "26.08.159") == nil {
		t.Fatal("the newest folded records are kept")
	}
	if h.record("bluefin", "26.08.50") == nil || h.record("bluefin", "26.08.40") == nil {
		t.Fatal("a good record with a report and a quarantined record are never dropped, however old")
	}
	if !h.hasEvent("release history compacted: 11 oldest good record(s) without files dropped") {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}

	rev := h.revision()
	h.tick()
	h.tick()
	if got := h.revision(); got != rev {
		t.Fatalf("a quiet tick must not touch the state: revision %d -> %d", rev, got)
	}
	h.fleet.cached["bluefin"] = []string{"27.01.100"}
	h.tick()
	if got := h.revision(); got <= rev {
		t.Fatalf("a changed cached list is a new revision: %d -> %d", rev, got)
	}
}

func TestEpisodeKeepsTheNewestTenAttempts(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	host, _ := h.fleet.Host(macA)
	h.c.mu.Lock()
	e := h.c.startEpisode(host, "4800.0.0", "test")
	for i := 1; i <= 13; i++ {
		e.Attempt = i
		h.c.finishAttempt(e, OutcomeFailed, ClassTimeout, "")
	}
	h.c.mu.Unlock()
	if len(e.Attempts) != maxAttempts || e.Attempts[0].Attempt != 4 || e.Attempts[maxAttempts-1].Attempt != 13 {
		t.Fatalf("an episode keeps its newest %d attempts: %d, first %d", maxAttempts, len(e.Attempts), e.Attempts[0].Attempt)
	}
}
