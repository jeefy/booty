//go:build linux || darwin || freebsd

package versions

import (
	"log/slog"
	"syscall"
)

// statfs reports the size and the space available to unprivileged users of
// the filesystem holding dir, or zeros when it cannot be read.
func statfs(dir string) (total, free int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		slog.Debug("statfs failed", "dir", dir, "error", err)
		return 0, 0
	}
	bsize := int64(st.Bsize)                                  //nolint:unconvert // Bsize is int64 on linux/amd64 but int32/uint32 on other ports
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize //nolint:unconvert // field types differ per port
}
