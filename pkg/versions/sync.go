package versions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jeefy/booty/pkg/state"
)

// releaseSync is the per-OS part of a version check; runReleaseSync owns
// the shared flow: tracked? -> TryLock -> local -> target -> compare ->
// install (download, verify, link) -> set state -> prune.
type releaseSync struct {
	os      string
	label   string
	tracked func() bool
	tryLock func() (func(), bool)
	// local returns the served version, seeding state when it was empty.
	local func() string
	// target returns the version that should be served: the pin or the
	// newest remote release (recording the remote in state).
	target func(ctx context.Context) (string, error)
	// valid, when set, rejects unusable target versions.
	valid func(string) bool
	// install downloads, verifies and links target; current is the version
	// it replaces.
	install    func(ctx context.Context, target, current string) error
	setCurrent func(string)
	// after runs once target is current and pruning is done.
	after func(target string)
}

// errVerification marks failures to obtain the digest or signature an
// artefact must be checked against: the sync refuses to install.
var errVerification = errors.New("verification metadata unavailable")

// metadataAttempts and metadataBackoff bound the retries of digest and
// checksum metadata fetches before a sync fails closed.
var (
	metadataAttempts = 3
	metadataBackoff  = time.Second
)

// retryMetadata calls fetch up to metadataAttempts times with exponential
// backoff and wraps the last error in errVerification.
func retryMetadata[T any](ctx context.Context, what string, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	var err error
	for attempt := 0; attempt < metadataAttempts; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(metadataBackoff << (attempt - 1))
			select {
			case <-ctx.Done():
				t.Stop()
				return zero, fmt.Errorf("%w: %s: %w", errVerification, what, ctx.Err())
			case <-t.C:
			}
		}
		var v T
		if v, err = fetch(ctx); err == nil {
			return v, nil
		}
		var stop errStopRetry
		if errors.As(err, &stop) {
			return zero, stop.err
		}
		slog.Warn("Could not fetch verification metadata", "what", what, "attempt", attempt+1, "error", err)
	}
	return zero, fmt.Errorf("%w: %s: %w", errVerification, what, err)
}

// errStopRetry makes retryMetadata return the wrapped error at once,
// unwrapped (e.g. a 404 that no retry will fix).
type errStopRetry struct{ err error }

func (e errStopRetry) Error() string { return e.err.Error() }
func (e errStopRetry) Unwrap() error { return e.err }

// runReleaseSync runs rs under ctx. A sync cut short because ctx is done
// (shutdown) returns without advancing the version or recording a sync
// error: it did not fail, it was stopped.
func runReleaseSync(ctx context.Context, rs releaseSync) {
	if ctx.Err() != nil {
		slog.Debug("Shutting down; skipping "+rs.label+" version check", "os", rs.os)
		return
	}
	if rs.tracked != nil && !rs.tracked() {
		slog.Debug(rs.label+" is not tracked; skipping version check", "os", rs.os)
		return
	}
	unlock, ok := rs.tryLock()
	if !ok {
		slog.Info(rs.label + " update already in progress, skipping version check")
		return
	}
	defer unlock()
	slog.Debug("Checking " + rs.label + " version")

	current := rs.local()
	target, err := rs.target(ctx)
	if err != nil {
		if ctx.Err() != nil {
			slog.Info(rs.label+" version check interrupted by shutdown", "error", err)
			return
		}
		slog.Error("Could not determine remote "+rs.label+" version, skipping update", "error", err)
		state.SetSyncError(rs.os, err)
		return
	}
	if rs.valid != nil && !rs.valid(target) {
		err := fmt.Errorf("%s version %q is not usable", rs.label, target)
		slog.Error(rs.label+" version is not usable, skipping update", "version", target)
		state.SetSyncError(rs.os, err)
		return
	}
	if target == current {
		state.ClearSyncError(rs.os)
		return
	}
	slog.Info("Target "+rs.label+" version differs from local", "target", target, "local", current)
	if err := rs.install(ctx, target, current); err != nil {
		if ctx.Err() != nil {
			slog.Info(rs.label+" release install interrupted by shutdown, not advancing version", "target", target, "error", err)
			return
		}
		slog.Error(rs.label+" release install failed, not advancing version", "target", target, "error", err)
		state.SetSyncError(rs.os, fmt.Errorf("install %s: %w", target, err))
		return
	}
	rs.setCurrent(target)
	state.ClearSyncError(rs.os)
	slog.Info(rs.label+" updated", "version", target)
	pruneReleases(rs.os)
	if rs.after != nil {
		rs.after(target)
	}
}
