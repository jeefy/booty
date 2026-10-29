package versions

import (
	"log/slog"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
)

// staleTempAge is how old a temp file outside a locked OS tree must be
// before SweepStaleTemp removes it; writers keep touching their temp files,
// so anything this quiet was abandoned.
const staleTempAge = 5 * time.Minute

// SweepStaleTemp removes "*.tmp" leftovers of interrupted writes from the
// data directory: every temp file in an OS tree whose update lock it can
// take (no sync is writing there), and elsewhere those untouched for
// staleTempAge. Call it at startup, before StartScheduler.
func SweepStaleTemp() {
	locks := map[string]func() (func(), bool){
		OSFlatcar: state.TryLockFlatcarUpdate,
		OSCoreOS:  state.TryLockCoreOSUpdate,
		OSBluefin: state.TryLockBluefinUpdate,
	}
	total := 0
	for osName, tryLock := range locks {
		unlock, ok := tryLock()
		if !ok {
			continue
		}
		n, err := config.SweepStaleTemp(config.DataPath(osName), 0)
		unlock()
		total += n
		if err != nil {
			slog.Warn("Temp file sweep failed", "os", osName, "error", err)
		}
	}
	n, err := config.SweepStaleTemp(config.DataPath(), staleTempAge)
	total += n
	if err != nil {
		slog.Warn("Temp file sweep failed", "dir", config.DataPath(), "error", err)
	}
	if total > 0 {
		slog.Info("Removed stale temp files", "count", total)
	}
}
