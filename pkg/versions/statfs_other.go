//go:build !(linux || darwin || freebsd || windows)

package versions

// statfs reports no disk figures on ports without a supported statfs call;
// the Storage page then omits total/free.
func statfs(string) (total, free int64) { return 0, 0 }
