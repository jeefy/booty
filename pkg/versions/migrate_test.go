package versions

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

const (
	homelabFlatcar   = "4593.2.1"
	homelabCoreOS    = "44.20260913.2.1"
	homelabBluefin   = "26.09.673"
	homelabBluefinPr = "2026.09.2"
)

// homelabLayout is the data directory of the homelab before this change:
// Flatcar's PXE files as plain top-level files plus flatcar_pin.txt and
// version.txt, CoreOS's live files flat at the top level, and Bluefin
// already in bluefin/<version>/ with current/previous.
func homelabLayout(t *testing.T) string {
	t.Helper()
	dir := setupVerify(t)
	touch(t, filepath.Join(dir, flatcarKernel))
	touch(t, filepath.Join(dir, flatcarInitrd))
	if err := os.WriteFile(filepath.Join(dir, "version.txt"), []byte("FLATCAR_VERSION="+homelabFlatcar+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.FlatcarPinFile), []byte(homelabFlatcar+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range coreOSArtifactNames(homelabCoreOS, "x86_64") {
		touch(t, filepath.Join(dir, f))
	}
	for _, v := range []string{homelabBluefinPr, homelabBluefin} {
		writeTestBluefinRelease(t, dir, v)
	}
	symlink(t, homelabBluefin, filepath.Join(dir, "bluefin", "current"))
	symlink(t, homelabBluefinPr, filepath.Join(dir, "bluefin", "previous"))
	hw := filepath.Join(dir, "hardware.json")
	if err := os.WriteFile(hw, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.HardwareMap, "hardware.json")
	if err := hardware.Load(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeTestBluefinRelease(t *testing.T, dir, version string) {
	t.Helper()
	rel := filepath.Join(dir, "bluefin", version)
	touch(t, filepath.Join(rel, BluefinNetbootUKI(version)))
	touch(t, filepath.Join(rel, BluefinDDI(version)))
	touch(t, filepath.Join(rel, BluefinSumsFile))
	touch(t, filepath.Join(rel, BluefinSigFile))
	m := BluefinManifest{Version: version, NetbootUKI: BluefinNetbootUKI(version), DDI: BluefinDDI(version), SHA256Sums: map[string]string{}}
	if err := writeBluefinManifest(rel, m); err != nil {
		t.Fatal(err)
	}
}

func readLink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return target
}

func TestMigrateReleaseLayoutFromHomelab(t *testing.T) {
	dir := homelabLayout(t)

	for range 2 {
		MigrateReleaseLayout()

		for _, a := range flatcarArtifacts {
			if !artifactPresent(filepath.Join(dir, flatcarDir, homelabFlatcar, a)) {
				t.Fatalf("%s not moved into flatcar/%s", a, homelabFlatcar)
			}
			if got := readLink(t, filepath.Join(dir, a)); got != filepath.Join(flatcarDir, CurrentLink, a) {
				t.Fatalf("top-level %s -> %q, want a link into flatcar/current", a, got)
			}
		}
		for _, f := range coreOSArtifactNames(homelabCoreOS, "x86_64") {
			if !artifactPresent(filepath.Join(dir, OSCoreOS, homelabCoreOS, f)) {
				t.Fatalf("%s not moved into coreos/%s", f, homelabCoreOS)
			}
			if got := readLink(t, filepath.Join(dir, f)); got != filepath.Join(OSCoreOS, CurrentLink, f) {
				t.Fatalf("top-level %s -> %q, want a link into coreos/current", f, got)
			}
		}
		want := map[string][3]string{
			OSFlatcar: {homelabFlatcar, "", homelabFlatcar},
			OSCoreOS:  {homelabCoreOS, "", homelabCoreOS},
			OSBluefin: {homelabBluefin, homelabBluefinPr, homelabBluefin},
		}
		for osName, w := range want {
			got := [3]string{CurrentRelease(osName), PreviousRelease(osName), LastGood(osName)}
			if got != w {
				t.Fatalf("%s current/previous/lastGood = %v, want %v", osName, got, w)
			}
		}
		if data, err := os.ReadFile(filepath.Join(dir, config.FlatcarPinFile)); err != nil || string(data) != homelabFlatcar+"\n" {
			t.Fatalf("flatcar_pin.txt must survive: %q %v", data, err)
		}
	}

	state.Init()
	if state.CurrentFlatcarVersion() != homelabFlatcar || state.CurrentBluefinVersion() != homelabBluefin {
		t.Fatalf("versions after migration: flatcar=%q bluefin=%q", state.CurrentFlatcarVersion(), state.CurrentBluefinVersion())
	}
	VerifyLocalArtifacts()
	if state.CurrentFlatcarVersion() != homelabFlatcar || state.CurrentCoreOSVersion() != homelabCoreOS {
		t.Fatalf("verify must accept the migrated layout: flatcar=%q coreos=%q", state.CurrentFlatcarVersion(), state.CurrentCoreOSVersion())
	}
}

func TestMigrateReleaseLayoutFromSymlinkedFlatcar(t *testing.T) {
	dir := setupVerify(t)
	for _, a := range flatcarArtifacts {
		touch(t, filepath.Join(dir, flatcarDir, "4757.2.0", a))
		symlink(t, filepath.Join(flatcarDir, "4757.2.0", a), filepath.Join(dir, a))
	}
	MigrateReleaseLayout()
	if CurrentRelease(OSFlatcar) != "4757.2.0" || LastGood(OSFlatcar) != "4757.2.0" {
		t.Fatalf("current=%q lastGood=%q", CurrentRelease(OSFlatcar), LastGood(OSFlatcar))
	}
	if got := readLink(t, filepath.Join(dir, flatcarKernel)); got != filepath.Join(flatcarDir, CurrentLink, flatcarKernel) {
		t.Fatalf("kernel link -> %q", got)
	}
	if MissingFlatcarArtifacts(dir) != nil {
		t.Fatal("artifacts must resolve through the compat links")
	}
}

func TestMigrateReleaseLayoutEmptyDirIsNoop(t *testing.T) {
	dir := setupVerify(t)
	MigrateReleaseLayout()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty data dir must stay empty, got %v", entries)
	}
}

func TestRetentionKeepsCurrentPreviousLastGoodAndTargets(t *testing.T) {
	dir := homelabLayout(t)
	MigrateReleaseLayout()
	for _, v := range []string{"27.01.1", "27.02.2", "27.03.3"} {
		writeTestBluefinRelease(t, dir, v)
		if err := linkRelease(OSBluefin, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hardware.Put(hardware.Host{MAC: "aa:bb:cc:dd:ee:01", OS: "bluefin", TargetVersion: "27.01.1"}); err != nil {
		t.Fatal(err)
	}
	pruneReleases(OSBluefin)
	got := CachedReleases(OSBluefin)
	want := []string{"27.03.3", "27.02.2", "27.01.1", homelabBluefin}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %v, want current, previous, a host's targetVersion and lastGood %v", got, want)
	}
	if err := SetLastGood(OSBluefin, "27.02.2"); err != nil {
		t.Fatal(err)
	}
	if err := SetLastGood(OSBluefin, "9.9.9"); err == nil {
		t.Fatal("lastGood must be a cached release")
	}
	pruneReleases(OSBluefin)
	if got := CachedReleases(OSBluefin); !reflect.DeepEqual(got, []string{"27.03.3", "27.02.2", "27.01.1"}) {
		t.Fatalf("after moving lastGood: %v", got)
	}
	host := &hardware.Host{MAC: "aa:bb:cc:dd:ee:01", OS: "bluefin", TargetVersion: "27.01.1"}
	if err := ValidateTargetVersion(host); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTargetVersion(&hardware.Host{OS: "bluefin", TargetVersion: homelabBluefin}); err == nil {
		t.Fatal("a pruned release is not a valid targetVersion")
	}
	if err := ValidateTargetVersion(&hardware.Host{OS: "flatcar", TargetVersion: "27.01.1"}); err == nil {
		t.Fatal("a release of another OS is not a valid targetVersion")
	}
	if got := EffectiveTarget(host); got != "27.01.1" {
		t.Fatalf("EffectiveTarget=%q", got)
	}
	state.SetCurrentBluefinVersion("27.03.3")
	t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
	if got := EffectiveTarget(&hardware.Host{OS: "bluefin"}); got != "27.03.3" {
		t.Fatalf("EffectiveTarget without targetVersion=%q, want the fleet target", got)
	}
	if got := EffectiveTarget(&hardware.Host{OS: "bluefin", TargetVersion: homelabBluefin}); got != "27.03.3" {
		t.Fatalf("a pruned targetVersion falls back to the fleet target, got %q", got)
	}
}
