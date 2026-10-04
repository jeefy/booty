package controller

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

func TestStatusBoundsReleasesAndEvents(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	t0 := h.time().Add(-30 * 24 * time.Hour)
	for i := range 15 {
		h.seedRelease("bluefin", fmt.Sprintf("26.08.%d", 100+i), ReleaseGood, t0.Add(time.Duration(i)*time.Hour))
	}
	h.seedRelease("bluefin", "26.08.9", ReleaseGood, t0.Add(20*time.Hour))
	h.seedRelease("bluefin", "26.08.50", ReleaseQuarantined, t0)
	h.seedRelease("bluefin", "26.08.40", ReleaseTimeout, h.time()).Retried = true
	h.seedRelease("bluefin", "26.08.30", ReleaseRolling, t0)
	h.c.mu.Lock()
	for i := range 150 {
		h.c.event(EventEpisode, "bluefin", "26.08.100", macA, fmt.Sprintf("event %d", i))
	}
	h.c.mu.Unlock()
	h.tick()

	st := h.c.Status()
	os := st.OS["bluefin"]
	if os.ReleaseCount != 21 {
		t.Fatalf("releaseCount counts every record: %d", os.ReleaseCount)
	}
	var versions []string
	for _, r := range os.Releases {
		versions = append(versions, r.Version)
	}
	want := []string{"27.01.100", "26.09.673", "26.08.114", "26.08.113", "26.08.112", "26.08.111", "26.08.110", "26.08.109", "26.08.108", "26.08.107", "26.08.106", "26.08.105", "26.08.50", "26.08.40", "26.08.30"}
	if strings.Join(versions, " ") != strings.Join(want, " ") {
		t.Fatalf("default releases are the live ones plus the %d newest good ones without files, newest first by version:\n got %v\nwant %v", DefaultGoodHistory, versions, want)
	}
	all := h.c.StatusWith(StatusOptions{AllReleases: true}).OS["bluefin"]
	if len(all.Releases) != 21 || all.ReleaseCount != 21 {
		t.Fatalf("?releases=all lists every record: %d", len(all.Releases))
	}
	if all.Releases[len(all.Releases)-1].Version != "26.08.9" {
		t.Fatalf("versions sort as dotted numbers, not strings: %s", all.Releases[len(all.Releases)-1].Version)
	}

	if len(st.Events) != DefaultEvents || st.EventCount < 150 || st.Events[len(st.Events)-1].Text != "event 149" {
		t.Fatalf("default events are the newest %d of %d: %d, last %q", DefaultEvents, st.EventCount, len(st.Events), st.Events[len(st.Events)-1].Text)
	}
	if !strings.HasPrefix(st.Events[0].Text, "event ") || st.Events[0].Text == "event 0" {
		t.Fatalf("the oldest events are what gets cut: first %q", st.Events[0].Text)
	}
	if ev := h.c.StatusWith(StatusOptions{AllEvents: true}).Events; len(ev) != st.EventCount {
		t.Fatalf("?events=all returns the whole ring: %d of %d", len(ev), st.EventCount)
	}
	if st.Revision == 0 {
		t.Fatal("status carries the state revision")
	}
}

// TestBoundedPayloadOnFortyReleases is the fixture the plan asked for:
// five days of Bluefin releases with the ring full, measured as the old
// API served it (every record, every event, before compaction) and as the
// default answer is now.
func TestBoundedPayloadOnFortyReleases(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	macs := make([]string, 6)
	for i := range macs {
		macs[i] = fmt.Sprintf("aa:bb:cc:dd:ee:%02d", i+1)
		h.fleet.add(hardware.Host{MAC: macs[i], Hostname: fmt.Sprintf("node-%d", i), IP: "192.168.1.5", OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "27.01.100"})
	}
	t0 := h.time().Add(-5 * 24 * time.Hour)
	for i := range 38 {
		r := h.seedRelease("bluefin", fmt.Sprintf("26.09.%d", 700+i), ReleaseGood, t0.Add(time.Duration(i)*3*time.Hour), macs...)
		r.Attempts = 6
	}
	h.seedRelease("bluefin", "26.09.673", ReleaseGood, t0, macs...)
	h.seedRelease("bluefin", "27.01.100", ReleaseGood, h.time(), macs...)
	h.c.mu.Lock()
	for i := range maxEvents {
		h.c.event(EventEpisode, "bluefin", fmt.Sprintf("26.09.%d", 700+i%38), macs[i%6], fmt.Sprintf("attempt 1 into 26.09.%d healthy (node Ready, workloads healthy)", 700+i%38))
	}
	for _, mac := range macs {
		hs := h.c.state.hostState(mac)
		hs.HealthyOn = "27.01.100"
		e := &Episode{MAC: mac, OS: "bluefin", Release: "27.01.100", Target: "27.01.100", Attempt: 1, State: hardware.AutopilotIdle, Done: true, Since: h.time(), Started: h.time(), T0: h.time()}
		for j := range 8 {
			h.c.timelineAt(e, h.time().Add(time.Duration(j)*20*time.Second), fmt.Sprintf("signal %d observed", j))
		}
		e.Attempts = []Attempt{{Attempt: 1, Target: "27.01.100", Outcome: OutcomeHealthy, Ended: h.time(), Note: "node Ready, workloads healthy", Signals: e.Signals}}
		hs.Episode = e
	}
	h.c.dirty = true
	h.c.save()
	h.c.mu.Unlock()
	size := func(v any) int {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return len(b)
	}
	stateBefore := fileSize(t, h.path)
	before := size(h.c.StatusWith(StatusOptions{AllReleases: true, AllEvents: true}))

	h.tick()
	h.c.mu.Lock()
	h.c.dirty = true
	h.c.save()
	h.c.mu.Unlock()
	stateAfter := fileSize(t, h.path)
	after := size(h.c.Status())
	allAfter := size(h.c.StatusWith(StatusOptions{AllReleases: true, AllEvents: true}))
	t.Logf("40 Bluefin releases, %d events, 6 hosts: GET /autopilot %d B before -> %d B default (%d B with ?releases=all&events=all); state.json %d B -> %d B", maxEvents, before, after, allAfter, stateBefore, stateAfter)
	if after >= before/2 {
		t.Fatalf("the default answer must be well under half the unbounded one: %d vs %d", after, before)
	}
	if stateAfter >= stateBefore {
		t.Fatalf("compaction must shrink state.json: %d vs %d", stateAfter, stateBefore)
	}
	st := h.c.Status()
	if st.OS["bluefin"].ReleaseCount != 40 || len(st.OS["bluefin"].Releases) != 2+DefaultGoodHistory || len(st.Events) != DefaultEvents {
		t.Fatalf("default bounds: %d records, %d listed, %d events", st.OS["bluefin"].ReleaseCount, len(st.OS["bluefin"].Releases), len(st.Events))
	}
}

func fileSize(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(info.Size())
}
