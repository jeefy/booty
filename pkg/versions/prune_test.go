package versions

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func TestPruneKeepsPinsHoldsNetbootAndRecentDemotions(t *testing.T) {
	dir := setupVerify(t)
	demotionGrace = time.Hour
	now := time.Now()
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = time.Now })
	demotions.Lock()
	clear(demotions.at)
	demotions.Unlock()

	vs := []string{"27.01.1", "27.02.2", "27.03.3", "27.04.4", "27.05.5", "27.06.6"}
	for _, v := range vs {
		writeTestBluefinRelease(t, dir, v)
	}
	for _, v := range vs[:3] {
		if err := linkRelease(OSBluefin, v); err != nil {
			t.Fatal(err)
		}
	}
	// lastGood=27.01.1, previous=27.02.2, current=27.03.3; 27.01.1 and
	// 27.02.2 were demoted "now".
	if err := os.WriteFile(filepath.Join(dir, "hardware.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.HardwareMap, "hardware.json")
	if err := hardware.Load(); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.BluefinVersion, "27.04.4")
	HoldFleetTarget(OSBluefin, "27.05.5")
	t.Cleanup(func() { HoldFleetTarget(OSBluefin, "") })
	if _, err := hardware.Put(hardware.Host{MAC: "aa:bb:cc:dd:ee:09", OS: OSBluefin, NetbootVersion: "27.06.6"}); err != nil {
		t.Fatal(err)
	}

	pruneReleases(OSBluefin)
	got := CachedReleases(OSBluefin)
	want := []string{"27.06.6", "27.05.5", "27.04.4", "27.03.3", "27.02.2", "27.01.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}

	// A demoted release that is nothing else is kept within the grace
	// window and pruned after it.
	writeTestBluefinRelease(t, dir, "27.07.7")
	if err := linkRelease(OSBluefin, "27.07.7"); err != nil { // demotes 27.03.3
		t.Fatal(err)
	}
	if err := SetLastGood(OSBluefin, "27.07.7"); err != nil {
		t.Fatal(err)
	}
	pruneReleases(OSBluefin)
	if !slices.Contains(CachedReleases(OSBluefin), "27.01.1") {
		t.Fatal("release demoted inside the grace window pruned")
	}
	now = now.Add(2 * time.Hour)
	pruneReleases(OSBluefin)
	got = CachedReleases(OSBluefin)
	for _, gone := range []string{"27.01.1", "27.02.2"} {
		if slices.Contains(got, gone) {
			t.Errorf("%s kept after the grace window: %v", gone, got)
		}
	}
	if !slices.Contains(got, "27.03.3") {
		t.Errorf("previous pruned: %v", got)
	}
}

// TestStorageRetainedMatchesPrune: /storage reports a reason for exactly
// the releases a prune keeps, including config pins, the fleet hold,
// netboot releases and recent demotions.
func TestStorageRetainedMatchesPrune(t *testing.T) {
	dir := setupVerify(t)
	demotionGrace = time.Hour
	now := time.Now()
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = time.Now })
	demotions.Lock()
	clear(demotions.at)
	demotions.Unlock()

	for _, v := range []string{"27.00.0", "27.01.1", "27.02.2", "27.03.3", "27.04.4", "27.05.5", "27.06.6", "27.07.7", "27.08.8"} {
		writeTestBluefinRelease(t, dir, v)
	}
	for _, v := range []string{"27.01.1", "27.02.2", "27.03.3"} {
		if err := linkRelease(OSBluefin, v); err != nil {
			t.Fatal(err)
		}
	}
	recordDemotion(OSBluefin, "27.07.7")
	if err := os.WriteFile(filepath.Join(dir, "hardware.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.HardwareMap, "hardware.json")
	if err := hardware.Load(); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.BluefinVersion, "27.04.4")
	HoldFleetTarget(OSBluefin, "27.05.5")
	t.Cleanup(func() { HoldFleetTarget(OSBluefin, "") })
	for _, h := range []hardware.Host{
		{MAC: "aa:bb:cc:dd:ee:09", OS: OSBluefin, NetbootVersion: "27.06.6"},
		{MAC: "aa:bb:cc:dd:ee:0a", OS: OSBluefin, TargetVersion: "27.08.8"},
	} {
		if _, err := hardware.Put(h); err != nil {
			t.Fatal(err)
		}
	}

	InvalidateStorage()
	got := map[string]string{}
	for _, r := range CurrentStorage().OS[OSBluefin].Releases {
		got[r.Version] = r.Retained
	}
	want := map[string]string{
		"27.00.0": RetainedNone,
		"27.01.1": RetainedLastGood,
		"27.02.2": RetainedPrevious,
		"27.03.3": RetainedCurrent,
		"27.04.4": RetainedPinned,
		"27.05.5": RetainedHeld,
		"27.06.6": RetainedNetboot,
		"27.07.7": RetainedDemoted,
		"27.08.8": RetainedPinned,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("/storage retained = %v, want %v", got, want)
	}

	pruneReleases(OSBluefin)
	for v, why := range got {
		if kept := slices.Contains(CachedReleases(OSBluefin), v); kept != (why != RetainedNone) {
			t.Errorf("%s: /storage said %q but prune kept=%v", v, why, kept)
		}
	}
}
