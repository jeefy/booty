package versions

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/jeefy/booty/pkg/state"
)

// A tick that arrives while the previous run of the same job is still
// executing is dropped, not queued or run concurrently.
func TestSchedulerSingletonDropsOverlappingTicks(t *testing.T) {
	var running, maxRunning, runs atomic.Int32
	slow := func(context.Context) {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		runs.Add(1)
		time.Sleep(120 * time.Millisecond)
		running.Add(-1)
	}
	s, err := newScheduler(t.Context(), func() gocron.JobDefinition { return gocron.DurationJob(10 * time.Millisecond) }, "10ms", []Job{{"slow", slow}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if maxRunning.Load() != 1 {
		t.Errorf("max concurrent runs = %d", maxRunning.Load())
	}
	// ~40 ticks in 400ms; at 120ms per run at most 4 can have executed.
	if got := runs.Load(); got < 1 || got > 4 {
		t.Errorf("runs = %d, want 1..4 (overlapping ticks must be dropped)", got)
	}
}

func TestSchedulerRejectsBadSchedule(t *testing.T) {
	if _, err := StartScheduler(t.Context(), "not a cron"); err == nil {
		t.Error("bad schedule accepted")
	}
}

// A version check whose update lock is held (a sync in progress) returns
// at once without fetching anything.
func TestVersionCheckSkipsWhileLocked(t *testing.T) {
	srv, hits := fakeCoreOS(t, nil)
	setupCoreOS(t, srv.URL)
	unlock, ok := state.TryLockCoreOSUpdate()
	if !ok {
		t.Fatal("lock busy")
	}
	CoreOSVersionCheck(t.Context())
	unlock()
	for p, h := range hits {
		if h.Load() != 0 {
			t.Errorf("%s fetched while locked", p)
		}
	}
}
