//go:build unix

package ignition

import (
	"os"
	"syscall"
)

// fileInode is info's inode number, 0 when the platform has none. Cache
// keys include it so a file replaced by rename is never mistaken for the
// one cached, even with the same size and mtime.
func fileInode(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
