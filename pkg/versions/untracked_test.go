package versions

import (
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

func TestUntrackedOSHasNoCachedReleases(t *testing.T) {
	dir := homelabLayout(t)
	MigrateReleaseLayout()
	if !ReleaseCached(OSCoreOS, homelabCoreOS) || CurrentRelease(OSCoreOS) != homelabCoreOS {
		t.Fatalf("precondition: coreos %s must be cached and current", homelabCoreOS)
	}
	if !OSTracked(OSCoreOS) || !OSTracked(OSFlatcar) || !OSTracked(OSBluefin) {
		t.Fatal("every OS is tracked by default")
	}

	viper.Set(config.CoreOSChannel, "None")
	viper.Set(config.CoreOSURL, "http://127.0.0.1:1/%s/%s/%s")
	state.SetCurrentCoreOSVersion("")

	if OSTracked(OSCoreOS) || !OSTracked(OSFlatcar) || !OSTracked(OSBluefin) {
		t.Fatal("only coreos is untracked")
	}
	if ReleaseCached(OSCoreOS, homelabCoreOS) {
		t.Fatal("an untracked OS has no cached releases")
	}
	if got := CachedReleases(OSCoreOS); got != nil {
		t.Fatalf("CachedReleases=%v want nil", got)
	}
	if got := releaseDirs(OSCoreOS); len(got) != 1 || got[0] != homelabCoreOS {
		t.Fatalf("releaseDirs still lists what is on disk, got %v", got)
	}
	if CurrentRelease(OSCoreOS) != "" || CurrentTarget(OSCoreOS) != "" || FleetTarget(OSCoreOS) != "" {
		t.Fatal("an untracked OS has no current release or fleet target")
	}
	if ReleaseCached(OSBluefin, homelabBluefin) != true {
		t.Fatal("the other OSes are unaffected")
	}

	host := &hardware.Host{MAC: "aa:bb:cc:dd:ee:01", OS: OSCoreOS}
	if err := ValidateHostOS(host); err == nil || !strings.Contains(err.Error(), "--coreOSChannel=none") {
		t.Fatalf("ValidateHostOS=%v want the untracked reason", err)
	}
	host.TargetVersion = homelabCoreOS
	if err := ValidateTargetVersion(host); err == nil || !strings.Contains(err.Error(), "not tracked") {
		t.Fatalf("ValidateTargetVersion=%v", err)
	}
	if err := ValidateHostOS(&hardware.Host{OS: OSBluefin}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHostOS(&hardware.Host{}); err != nil {
		t.Fatalf("an OS-less host is flatcar, which is tracked: %v", err)
	}
	if got := EffectiveTarget(host); got != "" {
		t.Fatalf("EffectiveTarget=%q want none", got)
	}

	CoreOSVersionCheck()
	VerifyLocalArtifacts()
	if v := state.CurrentCoreOSVersion(); v != "" {
		t.Fatalf("neither the check nor the artifact scan may record a CoreOS version, got %q", v)
	}
	if !releaseDirExists(OSCoreOS, homelabCoreOS) {
		t.Fatal("nothing on disk is touched")
	}

	viper.Set(config.FlatcarChannel, config.ChannelNone)
	state.SetCurrentFlatcarVersion("")
	state.Init()
	if state.CurrentFlatcarVersion() != "" {
		t.Fatal("state.Init must not load version.txt for an untracked Flatcar")
	}
	FlatcarVersionCheck()
	if state.CurrentFlatcarVersion() != "" || state.RemoteFlatcarVersion() != "" {
		t.Fatal("FlatcarVersionCheck is a no-op when untracked")
	}
	if err := ValidateHostOS(&hardware.Host{}); err == nil || !strings.Contains(err.Error(), "--flatcarChannel=none") {
		t.Fatalf("ValidateHostOS(flatcar)=%v", err)
	}
	if keep := retainedReleases(OSFlatcar); len(keep) != 0 {
		t.Fatalf("retainedReleases(flatcar)=%v: an untracked OS's links name nothing", keep)
	}
	if !releaseDirExists(OSFlatcar, homelabFlatcar) {
		t.Fatalf("the Flatcar release under %s is left alone", dir)
	}
}
