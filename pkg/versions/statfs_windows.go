//go:build windows

package versions

import (
	"log/slog"

	"golang.org/x/sys/windows"
)

func statfs(dir string) (total, free int64) {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		slog.Debug("statfs failed", "dir", dir, "error", err)
		return 0, 0
	}
	var freeToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(path, &freeToCaller, &totalBytes, &totalFree); err != nil {
		slog.Debug("statfs failed", "dir", dir, "error", err)
		return 0, 0
	}
	return int64(totalBytes), int64(freeToCaller) //nolint:gosec // disk sizes fit in int64
}
