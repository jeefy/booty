package versions

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
)

// Every OS keeps its releases in DataDir/<os>/<version>/ behind three
// relative symlinks: current (the fleet target), previous (the release
// current replaced, so hosts mid-boot can finish) and lastGood (the newest
// release the whole fleet was healthy on; P1 sets it to current once and
// the autopilot controller moves it). Pruning keeps exactly those three
// plus any host's targetVersion.
const (
	OSFlatcar = "flatcar"
	OSCoreOS  = "coreos"
	OSBluefin = "bluefin"

	CurrentLink  = config.BluefinCurrentLink
	PreviousLink = config.BluefinPreviousLink
	LastGoodLink = "lastGood"
)

var releaseLinks = []string{CurrentLink, PreviousLink, LastGoodLink}

// ReleaseOSes lists the operating systems with a release directory.
var ReleaseOSes = []string{OSFlatcar, OSCoreOS, OSBluefin}

// ReleaseDir is DataDir/<os>/<version>.
func ReleaseDir(osName, version string) string {
	return config.DataPath(osName, version)
}

// ReleaseCached reports whether version of osName is on disk.
func ReleaseCached(osName, version string) bool {
	if version == "" || !hardware.IsValidOS(osName) || hardware.ValidateTargetVersion(version) != nil {
		return false
	}
	info, err := os.Stat(ReleaseDir(osName, version))
	return err == nil && info.IsDir()
}

// CompareVersions orders two release versions as dotted numbers (newer is
// greater), the way CachedReleases sorts them.
func CompareVersions(a, b string) int { return compareVersions(a, b) }

// CachedReleases lists the release directories of osName, newest first.
func CachedReleases(osName string) []string {
	entries, err := os.ReadDir(config.DataPath(osName))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && hardware.ValidateTargetVersion(e.Name()) == nil {
			out = append(out, e.Name())
		}
	}
	slices.SortFunc(out, func(a, b string) int { return compareVersions(b, a) })
	return out
}

// readReleaseLink resolves DataDir/<os>/<link> to the version it names,
// "" when the link is missing or dangling.
func readReleaseLink(osName, link string) string {
	target, err := os.Readlink(config.DataPath(osName, link))
	if err != nil {
		return ""
	}
	version := filepath.Base(target)
	if !ReleaseCached(osName, version) {
		return ""
	}
	return version
}

// CurrentRelease, PreviousRelease and LastGood are the versions the three
// links name, "" when unset.
func CurrentRelease(osName string) string  { return readReleaseLink(osName, CurrentLink) }
func PreviousRelease(osName string) string { return readReleaseLink(osName, PreviousLink) }
func LastGood(osName string) string        { return readReleaseLink(osName, LastGoodLink) }

// SetLastGood points DataDir/<os>/lastGood at version, which must be cached.
func SetLastGood(osName, version string) error {
	if !ReleaseCached(osName, version) {
		return fmt.Errorf("%s %s is not a cached release", osName, version)
	}
	if err := config.ReplaceSymlink(version, config.DataPath(osName, LastGoodLink)); err != nil {
		return fmt.Errorf("linking %s lastGood: %w", osName, err)
	}
	slog.Info("lastGood release set", "os", osName, "version", version)
	return nil
}

// linkRelease makes version the current release of osName: previous gets
// the release it replaces and lastGood is initialised to version when it
// is unset or dangling. It never moves an existing lastGood.
func linkRelease(osName, version string) error {
	old := CurrentRelease(osName)
	if old != "" && old != version {
		if err := config.ReplaceSymlink(old, config.DataPath(osName, PreviousLink)); err != nil {
			return fmt.Errorf("linking %s previous: %w", osName, err)
		}
	}
	if err := config.ReplaceSymlink(version, config.DataPath(osName, CurrentLink)); err != nil {
		return fmt.Errorf("linking %s current: %w", osName, err)
	}
	return ensureLastGood(osName)
}

func ensureLastGood(osName string) error {
	if LastGood(osName) != "" {
		return nil
	}
	current := CurrentRelease(osName)
	if current == "" {
		return nil
	}
	return SetLastGood(osName, current)
}

// retainedReleases is what pruneReleases keeps: current, previous,
// lastGood and every registered host's targetVersion for osName.
func retainedReleases(osName string) []string {
	var keep []string
	add := func(v string) {
		if v != "" && !slices.Contains(keep, v) {
			keep = append(keep, v)
		}
	}
	for _, link := range releaseLinks {
		add(readReleaseLink(osName, link))
	}
	for _, h := range hardware.Snapshot().Hosts {
		if hostOS(h) == osName {
			add(h.TargetVersion)
		}
	}
	return keep
}

func hostOS(h *hardware.Host) string {
	if h == nil {
		return ""
	}
	if h.OS == "" {
		return OSFlatcar
	}
	return h.OS
}

// pruneReleases removes every release directory of osName that
// retainedReleases does not name.
func pruneReleases(osName string) {
	keep := retainedReleases(osName)
	root := config.DataPath(osName)
	entries, err := os.ReadDir(root)
	if err != nil {
		slog.Warn("Could not list release directories", "os", osName, "path", root, "error", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() || slices.Contains(keep, e.Name()) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("Could not remove old release", "os", osName, "path", path, "error", err)
			continue
		}
		slog.Info("Removed old release", "os", osName, "path", path, "kept", keep)
	}
}

var (
	holdMu        sync.RWMutex
	fleetHolds    = map[string]string{}
	serialRollout = map[string]bool{}
)

// HoldFleetTarget pins the fleet target of osName at version (a cached
// release, typically lastGood) instead of current; "" releases the hold.
// The autopilot controller is the only caller: it holds after a failed
// rollout and while a Bluefin release is rolled canary-serially. The hold
// is not persisted here; the controller re-applies it from its own state.
func HoldFleetTarget(osName, version string) {
	holdMu.Lock()
	defer holdMu.Unlock()
	if version == "" {
		delete(fleetHolds, osName)
		return
	}
	fleetHolds[osName] = version
}

// SerialRollout switches osName to the canary-serial policy: FleetTarget
// answers lastGood whenever current differs from it, from the instant a
// release lands and not only once the controller's next tick holds the
// fleet. Without it a host's update timer firing in that gap sees the new
// current and kured reboots a host the rollout had not picked (seen in
// the P5 QEMU run). The controller sets it under --autopilot=full.
func SerialRollout(osName string, on bool) {
	holdMu.Lock()
	defer holdMu.Unlock()
	if on {
		serialRollout[osName] = true
		return
	}
	delete(serialRollout, osName)
}

// FleetHold is the version FleetTarget is held at for osName, "" when not
// held (or the held release is no longer cached). Under SerialRollout it
// is lastGood while current differs from it.
func FleetHold(osName string) string {
	holdMu.RLock()
	v, serial := fleetHolds[osName], serialRollout[osName]
	holdMu.RUnlock()
	if v != "" && !ReleaseCached(osName, v) {
		v = ""
	}
	if v == "" && serial {
		if lg := LastGood(osName); lg != "" && lg != CurrentTarget(osName) && ReleaseCached(osName, lg) {
			return lg
		}
	}
	return v
}

// FleetTarget is the release hosts of osName boot unless their
// targetVersion says otherwise: the OS's current release, "" while none
// is cached, or the release the autopilot holds the fleet at (see
// HoldFleetTarget). Bluefin's current is the bluefin/current link itself,
// which is what /bluefin/<mac>/ served before targetVersion existed.
func FleetTarget(osName string) string {
	if held := FleetHold(osName); held != "" {
		return held
	}
	return CurrentTarget(osName)
}

// CurrentTarget is the OS's current release regardless of any hold.
func CurrentTarget(osName string) string {
	var v string
	switch osName {
	case OSFlatcar:
		v = state.CurrentFlatcarVersion()
	case OSCoreOS:
		v = state.CurrentCoreOSVersion()
	case OSBluefin:
		v = CurrentRelease(OSBluefin)
	}
	if !versionIsSet(v) {
		return ""
	}
	return v
}

// EffectiveTarget is the release host boots: its targetVersion when set
// and still cached (a pruned one falls back with a warning), else the
// fleet target of its OS.
func EffectiveTarget(host *hardware.Host) string {
	osName := hostOS(host)
	if host != nil && host.TargetVersion != "" {
		if ReleaseCached(osName, host.TargetVersion) {
			return host.TargetVersion
		}
		slog.Warn("Host targetVersion is not a cached release; serving the fleet target", "mac", host.MAC, "os", osName, "targetVersion", host.TargetVersion)
	}
	return FleetTarget(osName)
}

// ValidateTargetVersion checks a host's targetVersion at registration:
// empty, or a cached release of the host's OS.
func ValidateTargetVersion(host *hardware.Host) error {
	if host.TargetVersion == "" {
		return nil
	}
	if err := hardware.ValidateTargetVersion(host.TargetVersion); err != nil {
		return err
	}
	osName := hostOS(host)
	if !ReleaseCached(osName, host.TargetVersion) {
		return fmt.Errorf("%w %q: not a cached %s release (have %v)", hardware.ErrInvalidTargetVersion, host.TargetVersion, osName, CachedReleases(osName))
	}
	return nil
}
