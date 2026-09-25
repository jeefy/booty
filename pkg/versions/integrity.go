package versions

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

const unsetVersion = "0.0.0"

// artifactPresent follows symlinks, so a dangling link counts as missing.
func artifactPresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func missingArtifacts(dataDir string, names []string) []string {
	var missing []string
	for _, n := range names {
		if !artifactPresent(filepath.Join(dataDir, n)) {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	return missing
}

// MissingFlatcarArtifacts lists the served Flatcar PXE files absent from
// dataDir.
func MissingFlatcarArtifacts(dataDir string) []string {
	return missingArtifacts(dataDir, flatcarArtifacts)
}

// MissingCoreOSArtifacts lists the Fedora CoreOS live PXE files for
// version/arch absent from dataDir.
func MissingCoreOSArtifacts(dataDir, version, arch string) []string {
	names := coreOSArtifactNames(version, arch)
	files := make([]string, 0, len(names))
	for _, f := range names {
		files = append(files, f)
	}
	return missingArtifacts(dataDir, files)
}

func versionIsSet(v string) bool {
	return v != "" && v != unsetVersion
}

// VerifyLocalArtifacts reconciles the recorded Flatcar/CoreOS/Bluefin
// versions with what is actually on disk. A release whose PXE files are
// incomplete is reset to 0.0.0 so the next version check re-downloads it
// instead of trusting the stale record; a partial CoreOS set is removed
// outright.
func VerifyLocalArtifacts() {
	dataDir := viper.GetString(config.DataDir)

	if v := state.CurrentFlatcarVersion(); versionIsSet(v) {
		if missing := MissingFlatcarArtifacts(dataDir); len(missing) > 0 {
			slog.Warn("Flatcar artifacts missing on disk; version reset to 0.0.0 so the next check re-downloads them",
				"version", v, "missing", missing)
			state.SetCurrentFlatcarVersion(unsetVersion)
		}
	}

	if v := state.CurrentBluefinVersion(); versionIsSet(v) {
		if missing := MissingBluefinArtifacts(dataDir); len(missing) > 0 {
			slog.Warn("Bluefin artifacts missing on disk; version reset to 0.0.0 so the next check re-downloads them",
				"version", v, "missing", missing)
			state.SetCurrentBluefinVersion(unsetVersion)
		}
	}

	arch := viper.GetString(config.CoreOSArchitecture)
	v := state.CurrentCoreOSVersion()
	if v == "" {
		v = loadLocalCoreOSVersion(arch)
		state.SetCurrentCoreOSVersion(v)
	}
	if !versionIsSet(v) {
		return
	}
	missing := MissingCoreOSArtifacts(dataDir, v, arch)
	if len(missing) == 0 {
		return
	}
	slog.Warn("CoreOS artifacts missing on disk; removing the partial set and resetting version to 0.0.0",
		"version", v, "arch", arch, "missing", missing)
	removeOldCoreOSArtifacts(v, arch)
	state.SetCurrentCoreOSVersion(unsetVersion)
}
