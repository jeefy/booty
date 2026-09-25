package hardware

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff", true},
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff", true},
		{"aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff", true},
		{"aabb.ccdd.eeff", "aa:bb:cc:dd:ee:ff", true},
		{"  aa:bb:cc:dd:ee:ff\n", "aa:bb:cc:dd:ee:ff", true},
		{"", "", false},
		{"not-a-mac", "", false},
		{"aa:bb:cc:dd:ee", "", false},
		{"aa:bb:cc:dd:ee:gg", "", false},
		{"aa:bb:cc:dd:ee:ff:00", "", false},
	}
	for _, tc := range tests {
		got, err := NormalizeMAC(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("NormalizeMAC(%q): ok=%v err=%v", tc.in, tc.ok, err)
			continue
		}
		if err != nil && !errors.Is(err, ErrInvalidMAC) {
			t.Errorf("NormalizeMAC(%q): error should wrap ErrInvalidMAC, got %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("NormalizeMAC(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func openTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hardware.json")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db, path
}

func TestOpenCreatesMissingFile(t *testing.T) {
	_, path := openTestDB(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file should have been created: %v", err)
	}
	var m map[string]*Host
	if err := json.Unmarshal(data, &m); err != nil || len(m) != 0 {
		t.Fatalf("expected empty JSON object, got %q err=%v", data, err)
	}
}

func TestPutGetDeleteSnapshotRoundTrip(t *testing.T) {
	db, path := openTestDB(t)

	put, err := db.Put(Host{MAC: "AA-BB-CC-DD-EE-01", Hostname: "node1", OS: "flatcar", DoInstall: true})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if put.MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("Put should normalize MAC, got %q", put.MAC)
	}

	got, ok := db.Get("aa:bb:cc:dd:ee:01")
	if !ok || got.Hostname != "node1" || !got.DoInstall {
		t.Fatalf("Get: ok=%v host=%+v", ok, got)
	}
	got.Hostname = "mutated"
	if again, _ := db.Get("aa:bb:cc:dd:ee:01"); again.Hostname != "node1" {
		t.Fatal("Get must return a copy")
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if h, ok := reopened.Get("aa:bb:cc:dd:ee:01"); !ok || h.Hostname != "node1" {
		t.Fatalf("host not persisted: ok=%v host=%+v", ok, h)
	}

	snap := db.Snapshot()
	if len(snap.Hosts) != 1 || snap.Hosts["aa:bb:cc:dd:ee:01"].Hostname != "node1" {
		t.Fatalf("unexpected snapshot %+v", snap)
	}
	snap.Hosts["aa:bb:cc:dd:ee:01"].Hostname = "changed"
	if h, _ := db.Get("aa:bb:cc:dd:ee:01"); h.Hostname != "node1" {
		t.Fatal("Snapshot must deep copy")
	}
	if snap.UnknownHosts == nil {
		t.Fatal("UnknownHosts must be non-nil for JSON")
	}

	if err := db.Delete("aa:bb:cc:dd:ee:01"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := db.Get("aa:bb:cc:dd:ee:01"); ok {
		t.Fatal("host should be gone after Delete")
	}
	if err := db.Delete("aa:bb:cc:dd:ee:01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete should be ErrNotFound, got %v", err)
	}
	if _, err := db.Put(Host{MAC: "bogus"}); !errors.Is(err, ErrInvalidMAC) {
		t.Fatalf("Put with bad MAC should fail with ErrInvalidMAC, got %v", err)
	}
}

func TestDeletedHostDoesNotResurrect(t *testing.T) {
	db, path := openTestDB(t)
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01", Hostname: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:02", Hostname: "two"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete("aa:bb:cc:dd:ee:01"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:03", Hostname: "three"}); err != nil {
		t.Fatal(err)
	}

	if _, ok := db.Get("aa:bb:cc:dd:ee:01"); ok {
		t.Fatal("deleted host reappeared in memory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]*Host
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk["aa:bb:cc:dd:ee:01"]; ok {
		t.Fatal("deleted host reappeared on disk")
	}
	if len(onDisk) != 2 {
		t.Fatalf("expected 2 hosts on disk, got %d", len(onDisk))
	}
}

func TestExternalEditIsReloaded(t *testing.T) {
	db, path := openTestDB(t)
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01", Hostname: "before"}); err != nil {
		t.Fatal(err)
	}

	external := map[string]*Host{
		"AA:BB:CC:DD:EE:01": {MAC: "AA:BB:CC:DD:EE:01", Hostname: "edited-externally-with-a-longer-name"},
		"aa:bb:cc:dd:ee:09": {MAC: "aa:bb:cc:dd:ee:09", Hostname: "added-externally"},
	}
	data, err := json.Marshal(external)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	h, ok := db.Get("aa:bb:cc:dd:ee:01")
	if !ok || h.Hostname != "edited-externally-with-a-longer-name" {
		t.Fatalf("external edit not picked up: ok=%v host=%+v", ok, h)
	}
	if h.MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("external MAC key should be normalized, got %q", h.MAC)
	}
	if _, ok := db.Get("aa:bb:cc:dd:ee:09"); !ok {
		t.Fatal("externally added host not visible")
	}
}

func TestObserveBoundingAndEviction(t *testing.T) {
	db, _ := openTestDB(t)
	now := time.Unix(1_700_000_000, 0)
	db.now = func() time.Time { return now }

	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01"}); err != nil {
		t.Fatal(err)
	}
	db.Observe("aa:bb:cc:dd:ee:01", "10.0.0.1")
	db.Observe("garbage", "10.0.0.1")
	if snap := db.Snapshot(); len(snap.UnknownHosts) != 0 {
		t.Fatalf("registered/invalid MACs must not be recorded: %+v", snap.UnknownHosts)
	}

	db.Observe("02:00:00:00:00:00", "10.0.0.2")
	now = now.Add(time.Second)
	db.Observe("02:00:00:00:00:00", "10.0.0.3")
	snap := db.Snapshot()
	e := snap.UnknownHosts["02:00:00:00:00:00"]
	if e == nil || e.Count != 2 || e.IP != "10.0.0.3" || e.FirstSeen == e.LastSeen {
		t.Fatalf("unexpected unknown entry %+v", e)
	}
	if _, err := time.Parse(time.RFC3339, e.LastSeen); err != nil {
		t.Fatalf("lastSeen not RFC3339: %v", err)
	}

	for i := 1; i < MaxUnknownHosts; i++ {
		now = now.Add(time.Second)
		db.Observe(fmt.Sprintf("02:00:00:%02x:%02x:%02x", i>>16, (i>>8)&0xff, i&0xff), "10.0.0.9")
	}
	if got := len(db.Snapshot().UnknownHosts); got != MaxUnknownHosts {
		t.Fatalf("expected %d unknown hosts, got %d", MaxUnknownHosts, got)
	}

	now = now.Add(time.Second)
	db.Observe("02:00:00:ff:ff:ff", "10.0.0.10")
	snap = db.Snapshot()
	if len(snap.UnknownHosts) != MaxUnknownHosts {
		t.Fatalf("table must stay bounded, got %d", len(snap.UnknownHosts))
	}
	if _, ok := snap.UnknownHosts["02:00:00:00:00:00"]; ok {
		t.Fatal("least recently seen entry should have been evicted")
	}
	if _, ok := snap.UnknownHosts["02:00:00:ff:ff:ff"]; !ok {
		t.Fatal("newest entry should be present")
	}

	if _, err := db.Put(Host{MAC: "02:00:00:ff:ff:ff"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.Snapshot().UnknownHosts["02:00:00:ff:ff:ff"]; ok {
		t.Fatal("registering a host must remove it from unknownHosts")
	}
}

func TestMarkBooted(t *testing.T) {
	db, path := openTestDB(t)
	if _, err := db.Put(Host{MAC: "aa:bb:cc:dd:ee:01", IP: "old"}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.FixedZone("x", 3600))
	if err := db.MarkBooted("AA:BB:CC:DD:EE:01", "10.1.2.3", at); err != nil {
		t.Fatalf("MarkBooted: %v", err)
	}
	h, _ := db.Get("aa:bb:cc:dd:ee:01")
	if h.Booted != "2026-09-24T11:00:00Z" || h.IP != "10.1.2.3" {
		t.Fatalf("unexpected host after MarkBooted: %+v", h)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := reopened.Get("aa:bb:cc:dd:ee:01"); h.Booted != "2026-09-24T11:00:00Z" {
		t.Fatal("MarkBooted not persisted")
	}
	if err := db.MarkBooted("aa:bb:cc:dd:ee:02", "", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestIsValidOS(t *testing.T) {
	for _, ok := range []string{"flatcar", "coreos", "bluefin"} {
		if !IsValidOS(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "ublue", "Flatcar", "windows"} {
		if IsValidOS(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if ValidOSList() != "flatcar, coreos, bluefin" {
		t.Fatalf("ValidOSList=%q", ValidOSList())
	}
}

func TestValidateInstallDisk(t *testing.T) {
	for _, ok := range []string{"", "/dev/sda", "/dev/nvme0n1", "/dev/disk/by-id/wwn-0x5000c500a1b2c3d4"} {
		if err := ValidateInstallDisk(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"/dev/", "sda", "/etc/passwd", "/dev/sda inst.foo=1", "/dev/sda\tx", "/dev/../etc/passwd", "/dev/sda\n", "/dev/x\x00y"} {
		if err := ValidateInstallDisk(bad); !errors.Is(err, ErrInvalidInstall) {
			t.Errorf("%q should be rejected, got %v", bad, err)
		}
	}
}
