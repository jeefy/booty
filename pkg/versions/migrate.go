package versions

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

// MigrateReleaseLayout brings a data directory from before the per-OS
// release directories to DataDir/<os>/<version>/ with current, previous
// and lastGood links: Flatcar's top-level kernel/initrd (plain files, or
// symlinks into flatcar/<version>/) and CoreOS's flat
// fedora-coreos-<version>-live-* files move into their release directory,
// the top-level names stay as symlinks into <os>/current, and every OS
// with a current release gets lastGood = current when it has none. It is
// idempotent and runs before state.Init reads the versions.
func MigrateReleaseLayout() {
	if err := migrateFlatcarLayout(); err != nil {
		slog.Error("Flatcar release layout migration failed", "error", err)
	}
	if err := migrateCoreOSLayout(viper.GetString(config.CoreOSArchitecture)); err != nil {
		slog.Error("CoreOS release layout migration failed", "error", err)
	}
	for _, osName := range ReleaseOSes {
		if err := ensureLastGood(osName); err != nil {
			slog.Error("Could not initialise lastGood", "os", osName, "error", err)
		}
	}
}

func migrateFlatcarLayout() error {
	if v := CurrentRelease(OSFlatcar); v != "" {
		return linkFlatcarRelease(v)
	}
	version := flatcarVersionFromLinks()
	if version == "" {
		version = state.LoadLocalFlatcarVersion()
	}
	if version == "" || (!ReleaseCached(OSFlatcar, version) && !flatcarTopLevelFiles()) {
		return nil
	}
	if flatcarTopLevelFiles() {
		dir := ReleaseDir(OSFlatcar, version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for _, a := range flatcarArtifacts {
			if err := moveRegularFile(config.DataPath(a), filepath.Join(dir, a)); err != nil {
				return err
			}
		}
		slog.Info("Migrated Flatcar artifacts into their release directory", "version", version, "dir", dir)
	}
	if MissingFlatcarArtifactsIn(ReleaseDir(OSFlatcar, version)) != nil {
		slog.Warn("Flatcar release directory is incomplete; not linking it as current", "version", version)
		return nil
	}
	if err := linkFlatcarRelease(version); err != nil {
		return err
	}
	slog.Info("Flatcar release layout migrated", "current", version)
	return nil
}

// flatcarVersionFromLinks reads the version out of a top-level symlink of
// the previous layout (flatcar_production_pxe.vmlinuz -> flatcar/<v>/...).
func flatcarVersionFromLinks() string {
	for _, a := range flatcarArtifacts {
		target, err := os.Readlink(config.DataPath(a))
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(target), "/")
		if len(parts) == 3 && parts[0] == flatcarDir && parts[1] != CurrentLink && ReleaseCached(OSFlatcar, parts[1]) {
			return parts[1]
		}
	}
	return ""
}

func flatcarTopLevelFiles() bool {
	for _, a := range flatcarArtifacts {
		if info, err := os.Lstat(config.DataPath(a)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// MissingFlatcarArtifactsIn lists the Flatcar PXE files absent from a
// release directory.
func MissingFlatcarArtifactsIn(dir string) []string {
	return missingArtifacts(dir, flatcarArtifacts)
}

var coreOSFlatName = regexp.MustCompile(`^fedora-coreos-([0-9][0-9A-Za-z._+~-]*)-live-(kernel-|initramfs\.|rootfs\.)`)

func migrateCoreOSLayout(arch string) error {
	entries, err := os.ReadDir(viper.GetString(config.DataDir))
	if err != nil {
		return err
	}
	moved := map[string]bool{}
	for _, e := range entries {
		m := coreOSFlatName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		version := m[1]
		dir := ReleaseDir(OSCoreOS, version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := moveRegularFile(config.DataPath(e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
		moved[version] = true
	}
	for v := range moved {
		slog.Info("Migrated CoreOS artifacts into their release directory", "version", v, "dir", ReleaseDir(OSCoreOS, v))
	}
	current := CurrentRelease(OSCoreOS)
	if current == "" {
		current = coreOSMigrationCurrent(arch, moved)
	}
	if current == "" {
		return nil
	}
	if err := linkCoreOSRelease(current, "", arch); err != nil {
		return err
	}
	if len(moved) > 0 {
		slog.Info("CoreOS release layout migrated", "current", current)
	}
	return nil
}

// coreOSMigrationCurrent picks the release coreos/current should name when
// there is none yet: the streams JSON's version when its files are all
// there, else the newest complete release directory.
func coreOSMigrationCurrent(arch string, moved map[string]bool) string {
	dataDir := viper.GetString(config.DataDir)
	if v := loadLocalCoreOSVersion(arch); versionIsSet(v) && MissingCoreOSArtifacts(dataDir, v, arch) == nil && ReleaseCached(OSCoreOS, v) {
		return v
	}
	for _, v := range CachedReleases(OSCoreOS) {
		if MissingCoreOSArtifacts(dataDir, v, arch) == nil {
			return v
		}
	}
	if len(moved) > 0 {
		slog.Warn("Migrated CoreOS files do not form a complete release; the next check downloads afresh", "versions", moved)
	}
	return ""
}

func moveRegularFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("moving %s: %s already exists", src, dst)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("moving %s: %w", src, err)
	}
	return nil
}
