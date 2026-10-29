package config

import (
	"context"
	"crypto"
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Download retry tuning; tests shorten downloadBackoff.
var (
	downloadAttempts = 3
	downloadBackoff  = time.Second
)

// Download fetches url into dest through an AtomicFile (a "<base>.*.tmp"
// temp file in dest's directory), renamed into place only once the transfer
// completed and, when expectedHex is non-empty, the digest computed with h
// (crypto.SHA256 or crypto.SHA512) over the whole file matched. Network
// errors, 5xx and 429 answers are retried (downloadAttempts tries with
// exponential backoff, aborted by ctx); a retry resumes with an HTTP Range
// request (with If-Range naming the ETag, else the Last-Modified, of the
// response the partial bytes came from) when the server answers 206 for the
// expected offset and refetches the whole file otherwise. A checksum
// mismatch after a resumed transfer spends a remaining attempt on a fetch
// from byte 0; on a transfer that was never resumed it fails at once. On
// failure nothing is left at dest or in a temp file.
func Download(ctx context.Context, client *http.Client, url, dest string, h crypto.Hash, expectedHex string) error {
	if expectedHex == "" {
		h = crypto.SHA256
	} else if h != crypto.SHA256 && h != crypto.SHA512 {
		return fmt.Errorf("download %s: unsupported checksum algorithm %v", url, h)
	}
	slog.Info("Downloading", "url", url, "dest", dest, "verify", expectedHex != "")
	af, err := CreateAtomic(dest, h)
	if err != nil {
		return err
	}
	defer af.Abort()

	verify := func(sum string) error {
		if expectedHex != "" && !strings.EqualFold(sum, expectedHex) {
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", url, strings.ToLower(expectedHex), sum)
		}
		return nil
	}
	var st downloadState
	var lastErr error
	for attempt := 0; attempt < downloadAttempts; attempt++ {
		if attempt > 0 {
			wait := downloadBackoff << (attempt - 1)
			slog.Warn("Retrying download", "url", url, "attempt", attempt+1, "offset", af.Size(), "in", wait, "error", lastErr)
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return fmt.Errorf("download %s: %w (last error: %v)", url, ctx.Err(), lastErr)
			case <-t.C:
			}
		}
		retry, err := downloadOnce(ctx, client, url, af, &st)
		if err == nil {
			if mismatch := verify(af.Sum()); mismatch != nil && st.resumed && attempt+1 < downloadAttempts {
				// The bytes before the resume may come from another
				// version of the file: start over from byte 0.
				slog.Warn("Checksum mismatch after a resumed download; refetching from the start", "url", url, "error", mismatch)
				if err := af.Reset(); err != nil {
					return err
				}
				st = downloadState{}
				lastErr = mismatch
				continue
			}
			if _, err := af.Commit(verify); err != nil {
				return err
			}
			slog.Info("Download completed", "url", url, "dest", dest, "size_bytes", af.Size())
			return nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			return err
		}
	}
	return lastErr
}

// downloadState is what Download carries between attempts.
type downloadState struct {
	// validator is the strong ETag, else the Last-Modified date, of the
	// response the bytes in the AtomicFile came from; sent as If-Range.
	validator string
	// resumed is set once a 206 was appended to bytes of an earlier attempt.
	resumed bool
}

// rangeValidator picks the If-Range value for a response: its ETag unless
// weak (If-Range needs a strong one), else its Last-Modified date.
func rangeValidator(hdr http.Header) string {
	if etag := hdr.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		return etag
	}
	return hdr.Get("Last-Modified")
}

// downloadOnce appends the rest of url to af, asking for a Range (guarded
// by If-Range when st has a validator) when af already holds bytes. retry
// reports whether the error is transient.
func downloadOnce(ctx context.Context, client *http.Client, url string, af *AtomicFile, st *downloadState) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("download %s: %w", url, err)
	}
	offset := af.Size()
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if st.validator != "" {
			req.Header.Set("If-Range", st.validator)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return true, fmt.Errorf("download %s: %w", url, err)
	}
	defer CloseQuietly(resp.Body, url)
	reset := func() error {
		st.resumed, st.validator = false, ""
		return af.Reset()
	}
	switch {
	case resp.StatusCode == http.StatusPartialContent && offset > 0 && contentRangeStart(resp.Header.Get("Content-Range")) == offset:
		st.resumed = true
	case resp.StatusCode == http.StatusOK:
		if offset > 0 {
			slog.Info("Server does not resume or the file changed; refetching from the start", "url", url)
			if err := reset(); err != nil {
				return false, err
			}
		}
		st.validator = rangeValidator(resp.Header)
	default:
		statusErr := &HTTPStatusError{URL: url, StatusCode: resp.StatusCode}
		if resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			if err := reset(); err != nil {
				return false, err
			}
			return true, statusErr
		}
		return resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests, statusErr
	}
	if _, err := io.Copy(af, resp.Body); err != nil {
		return true, fmt.Errorf("download %s: %w", url, err)
	}
	return false, nil
}

// contentRangeStart parses the first byte of "bytes <start>-<end>/<total>";
// -1 when unparseable.
func contentRangeStart(v string) int64 {
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return -1
	}
	startStr, _, ok := strings.Cut(rest, "-")
	if !ok {
		return -1
	}
	n, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// AtomicFile is a temp file "<base>.*.tmp" in the destination's directory
// that hashes everything written to it and is renamed over the destination
// by Commit (after an fsync of the file and, after the rename, of the
// directory). Abort removes it; it is a no-op after Commit.
type AtomicFile struct {
	dest   string
	f      *os.File
	h      crypto.Hash
	hasher hash.Hash
	n      int64
	done   bool
}

// CreateAtomic starts an AtomicFile for dest hashed with h (crypto.SHA256 or
// crypto.SHA512), creating dest's directory when needed.
func CreateAtomic(dest string, h crypto.Hash) (*AtomicFile, error) {
	if h != crypto.SHA256 && h != crypto.SHA512 {
		return nil, fmt.Errorf("atomic write %s: unsupported hash %v", dest, h)
	}
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, filepath.Base(dest)+".*.tmp")
	if err != nil {
		return nil, err
	}
	return &AtomicFile{dest: dest, f: f, h: h, hasher: h.New()}, nil
}

func (a *AtomicFile) Write(p []byte) (int, error) {
	n, err := a.f.Write(p)
	a.hasher.Write(p[:n])
	a.n += int64(n)
	return n, err
}

// Size is the number of bytes written so far.
func (a *AtomicFile) Size() int64 { return a.n }

// Sum is the hex digest of the bytes written so far.
func (a *AtomicFile) Sum() string { return hex.EncodeToString(a.hasher.Sum(nil)) }

// Reset discards everything written so far.
func (a *AtomicFile) Reset() error {
	if err := a.f.Truncate(0); err != nil {
		return err
	}
	if _, err := a.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	a.hasher = a.h.New()
	a.n = 0
	return nil
}

// Commit fsyncs the temp file, hands its hex digest to accept (nil accepts)
// and, when that returns nil, renames it over dest and fsyncs the directory.
// Any error removes the temp file.
func (a *AtomicFile) Commit(accept func(sumHex string) error) (string, error) {
	sum := hex.EncodeToString(a.hasher.Sum(nil))
	if err := a.f.Sync(); err != nil {
		a.Abort()
		return "", err
	}
	if accept != nil {
		if err := accept(sum); err != nil {
			a.Abort()
			return "", err
		}
	}
	if err := a.f.Close(); err != nil {
		a.Abort()
		return "", err
	}
	// CreateTemp makes 0600; published artifacts keep the 0644 os.Create
	// gave them before.
	if err := os.Chmod(a.f.Name(), 0o644); err != nil {
		a.Abort()
		return "", err
	}
	if err := os.Rename(a.f.Name(), a.dest); err != nil {
		a.Abort()
		return "", err
	}
	a.done = true
	if err := fsyncDir(filepath.Dir(a.dest)); err != nil {
		return "", err
	}
	return sum, nil
}

// Abort closes and removes the temp file unless Commit succeeded.
func (a *AtomicFile) Abort() {
	if a.done {
		return
	}
	a.done = true
	tmp := a.f.Name()
	if err := a.f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		slog.Debug("Closing temp file", "path", tmp, "error", err)
	}
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		slog.Warn("Could not remove temp file", "path", tmp, "error", err)
	}
}

// WriteHashedAtomic streams r into dest through an AtomicFile hashed with h
// and returns the hex digest; accept (nil accepts) vetoes the rename.
func WriteHashedAtomic(r io.Reader, dest string, h crypto.Hash, accept func(sumHex string) error) (string, error) {
	af, err := CreateAtomic(dest, h)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(af, r); err != nil {
		af.Abort()
		return "", err
	}
	return af.Commit(accept)
}

// fsyncDir makes a rename in dir durable; a variable so tests can observe it.
var fsyncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer CloseQuietly(d, dir)
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return fmt.Errorf("fsync %s: %w", dir, err)
	}
	return nil
}

// SweepStaleTemp removes regular files under dir (recursively, symlinks not
// followed) whose name ends in ".tmp" and that were last modified more than
// olderThan ago: the leftovers of writes interrupted by a crash. It returns
// how many it removed. Unreadable directories are logged and skipped. Call
// it at startup on the data directory.
func SweepStaleTemp(dir string, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			slog.Warn("Could not sweep directory for stale temp files", "path", path, "error", err)
			return fs.SkipDir
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".tmp") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("Could not remove stale temp file", "path", path, "error", err)
			return nil
		}
		slog.Info("Removed stale temp file", "path", path)
		removed++
		return nil
	})
	return removed, err
}

// HTTPStatusError is Download's error for a non-200 answer, so callers can
// tell a missing file (404) from a failed transfer.
type HTTPStatusError struct {
	URL        string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("download failed for %s: HTTP %d", e.URL, e.StatusCode)
}

// WriteFileAtomic writes data to path via a temporary file in the same
// directory, fsyncs it and renames it into place so readers never observe a
// partially written file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		if rmErr := os.Remove(tmp); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Warn("Could not remove temp file", "path", tmp, "error", rmErr)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		CloseQuietly(f, tmp)
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		CloseQuietly(f, tmp)
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return fsyncDir(dir)
}

// ProbeWritable reports whether WriteFileAtomic(path) could succeed without
// changing anything: an existing file is opened write-only and closed again;
// for a missing one a temp file is created and removed in the nearest
// existing ancestor directory. The reason names the path and the OS error,
// e.g. "/data/config/ignition.yaml is read-only (read-only file system)",
// which is what a Kubernetes ConfigMap mount produces.
func ProbeWritable(path string) (writable bool, reason string) {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return false, fmt.Sprintf("%s is a directory", path)
	case err == nil:
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return false, readOnlyReason(path, err)
		}
		CloseQuietly(f, path)
		return true, ""
	case !errors.Is(err, fs.ErrNotExist):
		return false, readOnlyReason(path, err)
	}

	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	f, err := os.CreateTemp(dir, ".booty-probe-*.tmp")
	if err != nil {
		return false, readOnlyReason(dir, err)
	}
	tmp := f.Name()
	CloseQuietly(f, tmp)
	if err := os.Remove(tmp); err != nil {
		slog.Warn("Could not remove probe file", "path", tmp, "error", err)
	}
	return true, ""
}

func readOnlyReason(path string, err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Sprintf("%s is read-only (%v)", path, err)
}

// ReplaceSymlink atomically points linkPath at target, replacing whatever
// (symlink or regular file) currently occupies linkPath.
func ReplaceSymlink(target, linkPath string) error {
	tmp := linkPath + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		if rmErr := os.Remove(tmp); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Warn("Could not remove temp symlink", "path", tmp, "error", rmErr)
		}
		return err
	}
	return nil
}

// FileHashMatches reports whether the file at path hashes (with h) to
// expectedHex. Any error (including a missing file) yields false.
func FileHashMatches(path string, h crypto.Hash, expectedHex string) bool {
	if expectedHex == "" || (h != crypto.SHA256 && h != crypto.SHA512) {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer CloseQuietly(f, path)
	hasher := h.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expectedHex)
}
