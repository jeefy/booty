package versions

import (
	"errors"
	"log/slog"
	"os"

	"github.com/jeefy/booty/pkg/config"
)

// LegacyRegistryDir is where the removed OCI image cache kept its blobs
// and manifests.
const LegacyRegistryDir = "registry"

// CleanupLegacyRegistry removes DataDir/registry when the old OCI image
// cache left nothing but empty directories behind, and otherwise tells the
// operator once that whatever is in it is unused and can be deleted.
func CleanupLegacyRegistry() {
	dir := config.DataPath(LegacyRegistryDir)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return
	} else if err != nil {
		slog.Warn("Could not inspect the old OCI cache directory", "path", dir, "error", err)
		return
	}
	bytes, files, _ := dirSize(dir)
	if files > 0 {
		slog.Warn("The OCI image cache was removed from Booty; this directory is unused and can be deleted", "path", dir, "files", files, "bytes", bytes)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		slog.Warn("Could not remove the empty OCI cache directory", "path", dir, "error", err)
		return
	}
	slog.Info("Removed the empty OCI cache directory left by an earlier Booty", "path", dir)
}
