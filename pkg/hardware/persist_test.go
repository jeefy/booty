package hardware

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readMap(t *testing.T, path string) map[string]*Host {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*Host{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUpdateIsBatched(t *testing.T) {
	db, path := openTestDB(t)
	db.interval = time.Hour
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := db.Update("aa:bb:cc:dd:ee:01", func(h *Host) { h.Running = "v" + string(rune('a'+i%26)) }); err != nil {
			t.Fatal(err)
		}
	}
	if got := readMap(t, path)["aa:bb:cc:dd:ee:01"].Running; got != "" {
		t.Fatalf("Update must not write synchronously, file has running=%q", got)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := readMap(t, path)["aa:bb:cc:dd:ee:01"].Running; got == "" {
		t.Fatal("Flush did not write the pending update")
	}
}

func TestBackgroundFlush(t *testing.T) {
	db, path := openTestDB(t)
	db.interval = 10 * time.Millisecond
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update("aa:bb:cc:dd:ee:01", func(h *Host) { h.Running = "1.2.3" }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for readMap(t, path)["aa:bb:cc:dd:ee:01"].Running != "1.2.3" {
		if time.Now().After(deadline) {
			t.Fatal("background flush never wrote the update")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUnchangedUpdateWritesNothing(t *testing.T) {
	db, _ := openTestDB(t)
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update("aa:bb:cc:dd:ee:01", func(h *Host) { h.IP = "10.0.0.1" }); err != nil {
		t.Fatal(err)
	}
	db.mu.Lock()
	dirty, timer := len(db.dirty), db.flushing
	db.mu.Unlock()
	if dirty != 0 || timer != nil {
		t.Fatalf("a no-op Update must not schedule a write: dirty=%d timer=%v", dirty, timer != nil)
	}
}

func TestPutWritesPendingUpdates(t *testing.T) {
	db, path := openTestDB(t)
	db.interval = time.Hour
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update("aa:bb:cc:dd:ee:01", func(h *Host) { h.Running = "1.2.3" }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:02"}); err != nil {
		t.Fatal(err)
	}
	m := readMap(t, path)
	if m["aa:bb:cc:dd:ee:01"].Running != "1.2.3" || m["aa:bb:cc:dd:ee:02"] == nil {
		t.Fatalf("Put must persist synchronously with pending updates: %+v", m)
	}
}

func TestCloseFlushes(t *testing.T) {
	db, path := openTestDB(t)
	db.interval = time.Hour
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update("aa:bb:cc:dd:ee:01", func(h *Host) { h.Running = "1.2.3" }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if readMap(t, path)["aa:bb:cc:dd:ee:01"].Running != "1.2.3" {
		t.Fatal("Close did not flush")
	}
}

// Our own batched write must not look like an external edit: the pending
// state survives the flush and later reads do not reload anything else.
func TestOwnFlushIsNotReloaded(t *testing.T) {
	db, path := openTestDB(t)
	db.interval = time.Hour
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update("aa:bb:cc:dd:ee:01", func(h *Host) { h.Running = "1" }); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update("aa:bb:cc:dd:ee:01", func(h *Host) { h.Running = "2" }); err != nil {
		t.Fatal(err)
	}
	if h, _ := db.Get("aa:bb:cc:dd:ee:01"); h.Running != "2" {
		t.Fatalf("pending update lost after our own flush: %+v", h)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if readMap(t, path)["aa:bb:cc:dd:ee:01"].Running != "2" {
		t.Fatal("second flush lost")
	}
}

// An external edit made while batched updates are pending wins: it is
// reloaded, the pending updates are dropped, and the flush does not write
// them over it.
func TestExternalEditWinsOverPendingUpdates(t *testing.T) {
	db, path := openTestDB(t)
	db.interval = time.Hour
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01", Hostname: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update("aa:bb:cc:dd:ee:01", func(h *Host) { h.Running = "pending" }); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]*Host{"aa:bb:cc:dd:ee:01": {Hostname: "operator-edited-this-host"}})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	onDisk, _ := os.ReadFile(path)
	if !bytes.Equal(onDisk, data) {
		t.Fatalf("flush overwrote the external edit:\n%s", onDisk)
	}
	h, _ := db.Get("aa:bb:cc:dd:ee:01")
	if h.Hostname != "operator-edited-this-host" || h.Running != "" {
		t.Fatalf("external edit must win over pending updates: %+v", h)
	}
	db.mu.Lock()
	dirty := len(db.dirty)
	db.mu.Unlock()
	if dirty != 0 {
		t.Fatal("dropped updates must not stay dirty")
	}
}

func TestCopyHostIsDeep(t *testing.T) {
	h := &Host{
		Extensions: []string{"zfs"},
		Autopilot:  &HostAutopilot{State: AutopilotIdle},
		Power:      &HostPower{State: "up"},
		Health:     &Health{FailedUnits: []string{"a"}, JournalErrors: []string{"b"}},
	}
	c := copyHost(h)
	c.Extensions[0] = "x"
	c.Autopilot.State = "x"
	c.Power.State = "x"
	c.Health.FailedUnits[0] = "x"
	c.Health.JournalErrors[0] = "x"
	c.Health.Running = "x"
	if h.Extensions[0] != "zfs" || h.Autopilot.State != AutopilotIdle || h.Power.State != "up" ||
		h.Health.FailedUnits[0] != "a" || h.Health.JournalErrors[0] != "b" || h.Health.Running != "" {
		t.Fatalf("copyHost shares memory with the original: %+v", h)
	}
	if c := copyHost(&Host{}); c.Extensions != nil || c.Autopilot != nil || c.Power != nil || c.Health != nil {
		t.Fatalf("nil fields must stay nil: %+v", c)
	}
}

func TestGetReturnsIndependentCopy(t *testing.T) {
	db, _ := openTestDB(t)
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01", Extensions: []string{"zfs"}, Health: &Health{FailedUnits: []string{"u"}}}); err != nil {
		t.Fatal(err)
	}
	h, _ := db.Get("aa:bb:cc:dd:ee:01")
	h.Extensions[0] = "x"
	h.Health.FailedUnits[0] = "x"
	again, _ := db.Get("aa:bb:cc:dd:ee:01")
	if again.Extensions[0] != "zfs" || again.Health.FailedUnits[0] != "u" {
		t.Fatalf("Get leaked internal state: %+v", again)
	}
}

// One-shot transitions are written before Update returns, with no Flush:
// a cleared doInstall lost to the batch window would reinstall the host.
func TestOneShotUpdateIsPersistedImmediately(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:01"
	cases := []struct {
		name  string
		start Host
		fn    func(*Host)
		check func(*Host) bool
	}{
		{"doInstall", Host{DoInstall: true}, func(h *Host) { h.DoInstall = false }, func(h *Host) bool { return !h.DoInstall }},
		{"mode", Host{}, func(h *Host) { h.Mode = ModeInstalled }, func(h *Host) bool { return h.Mode == ModeInstalled }},
		{"installServedAt", Host{}, func(h *Host) { h.InstallServedAt = "2026-10-29T00:00:00Z" }, func(h *Host) bool { return h.InstallServedAt != "" }},
		{"targetVersion", Host{}, func(h *Host) { h.TargetVersion = "1.2.3" }, func(h *Host) bool { return h.TargetVersion == "1.2.3" }},
		{"pin", Host{Autopilot: &HostAutopilot{State: AutopilotIdle}}, func(h *Host) { h.Autopilot = &HostAutopilot{State: AutopilotIdle, Pinned: true} }, func(h *Host) bool { return h.Autopilot != nil && h.Autopilot.Pinned }},
		{"cordon", Host{Power: &HostPower{State: PowerUp}}, func(h *Host) { h.Power.Cordoned = true }, func(h *Host) bool { return h.Power != nil && h.Power.Cordoned }},
		{"uncordon", Host{Power: &HostPower{State: PowerUp, Cordoned: true}}, func(h *Host) { h.Power.Cordoned = false }, func(h *Host) bool { return h.Power != nil && !h.Power.Cordoned }},
		{"powerRequest", Host{}, func(h *Host) { h.Power = &HostPower{State: PowerDraining, Request: PowerRequestReboot} }, func(h *Host) bool { return h.Power != nil && h.Power.Request == PowerRequestReboot }},
		{"powerRequestCleared", Host{Power: &HostPower{State: PowerRebooting, Request: PowerRequestShutdown}}, func(h *Host) { h.Power = &HostPower{State: PowerUp} }, func(h *Host) bool { return h.Power != nil && h.Power.Request == "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, path := openTestDB(t)
			db.interval = time.Hour
			start := tc.start
			start.MAC = mac
			if _, err := db.Put(start); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Update(mac, func(h *Host) { h.Running = "batched" }); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Update(mac, tc.fn); err != nil {
				t.Fatal(err)
			}
			got := readMap(t, path)[mac]
			if !tc.check(got) {
				t.Fatalf("one-shot change not on disk without Flush: %+v", got)
			}
			if got.Running != "batched" {
				t.Fatal("the synchronous write must carry pending batched changes")
			}
			db.mu.Lock()
			dirty := len(db.dirty)
			db.mu.Unlock()
			if dirty != 0 {
				t.Fatal("nothing may stay dirty after the synchronous write")
			}
		})
	}
}

// A failed synchronous write is returned with the host as applied in
// memory, and stays dirty for the background flush to retry.
func TestOneShotUpdateWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	const mac = "aa:bb:cc:dd:ee:01"
	db, path := openTestDB(t)
	db.interval = time.Hour
	if _, err := db.Put(Host{MAC: mac, DoInstall: true}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	h, err := db.Update(mac, func(h *Host) { h.DoInstall = false })
	if err == nil {
		t.Fatal("a failed write of a one-shot change must be returned")
	}
	if h == nil || h.DoInstall {
		t.Fatalf("the change must stay applied in memory: %+v", h)
	}
	db.mu.Lock()
	dirty, timer := db.dirty[mac], db.flushing != nil
	db.mu.Unlock()
	if !dirty || !timer {
		t.Fatalf("the failed write must be retried: dirty=%v timer=%v", dirty, timer)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if readMap(t, path)[mac].DoInstall {
		t.Fatal("retry did not write the cleared doInstall")
	}
}

func TestUpdateAfterCloseIsPersisted(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:01"
	db, path := openTestDB(t)
	if _, err := db.Put(Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update(mac, func(h *Host) { h.Running = "after-close" }); err != nil {
		t.Fatal(err)
	}
	if got := readMap(t, path)[mac].Running; got != "after-close" {
		t.Fatalf("an Update after Close must be written at once, file has running=%q", got)
	}
	if changed, err := db.SetSecureBoot(mac, true); err != nil || !changed {
		t.Fatalf("SetSecureBoot: changed=%v err=%v", changed, err)
	}
	if !readMap(t, path)[mac].SecureBoot {
		t.Fatal("SetSecureBoot after Close must be written at once")
	}
}
