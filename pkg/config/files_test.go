package config

import (
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func fastBackoff(t *testing.T) {
	t.Helper()
	old := downloadBackoff
	downloadBackoff = time.Millisecond
	t.Cleanup(func() { downloadBackoff = old })
}

func sumHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// flaky serves body, cutting the first answer off after cut bytes; with
// ranges it honours Range requests.
func flaky(t *testing.T, body string, cut int, ranges bool, rangeSeen *atomic.Int32) *httptest.Server {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		start := 0
		if rg := r.Header.Get("Range"); rg != "" && ranges {
			rangeSeen.Add(1)
			start, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-start))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		if n == 1 {
			_, _ = w.Write([]byte(body[:cut]))
			w.(http.Flusher).Flush()
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(body[start:]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadResumesWithRange(t *testing.T) {
	fastBackoff(t)
	body := strings.Repeat("0123456789", 100)
	var seen atomic.Int32
	srv := flaky(t, body, 300, true, &seen)
	dest := filepath.Join(t.TempDir(), "f.bin")
	if err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, sumHex(body)); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if seen.Load() != 1 {
		t.Fatalf("expected one Range request, got %d", seen.Load())
	}
	got, _ := os.ReadFile(dest)
	if string(got) != body {
		t.Fatal("content mismatch")
	}
	assertNoTemp(t, filepath.Dir(dest))
}

func TestDownloadRefetchesWithoutRangeSupport(t *testing.T) {
	fastBackoff(t)
	body := strings.Repeat("abcdefghij", 100)
	var seen atomic.Int32
	srv := flaky(t, body, 300, false, &seen)
	dest := filepath.Join(t.TempDir(), "f.bin")
	if err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, sumHex(body)); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != body {
		t.Fatal("content mismatch")
	}
}

func TestDownloadRetries5xxThenGivesUp(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f.bin")
	err := Download(context.Background(), srv.Client(), srv.URL, dest, 0, "")
	var se *HTTPStatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %v", err)
	}
	if calls.Load() != int32(downloadAttempts) {
		t.Fatalf("calls = %d", calls.Load())
	}
	assertNoFile(t, dest)
	assertNoTemp(t, filepath.Dir(dest))
}

func TestDownloadDoesNotRetry404(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_ = Download(context.Background(), srv.Client(), srv.URL, filepath.Join(t.TempDir(), "f"), 0, "")
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestDownloadBackoffHonoursContext(t *testing.T) {
	old := downloadBackoff
	downloadBackoff = time.Minute
	t.Cleanup(func() { downloadBackoff = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Download(ctx, srv.Client(), srv.URL, filepath.Join(t.TempDir(), "f"), 0, "")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestWriteHashedAtomicVeto(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "x")
	_, err := WriteHashedAtomic(strings.NewReader("data"), dest, crypto.SHA256, func(s string) error {
		if s != sumHex("data") {
			t.Errorf("sum %s", s)
		}
		return errors.New("no")
	})
	if err == nil {
		t.Fatal("expected veto error")
	}
	assertNoFile(t, dest)
	assertNoTemp(t, dir)
	sum, err := WriteHashedAtomic(strings.NewReader("data"), dest, crypto.SHA256, nil)
	if err != nil || sum != sumHex("data") {
		t.Fatalf("sum=%s err=%v", sum, err)
	}
}

func TestAtomicWritesFsyncDir(t *testing.T) {
	var synced []string
	old := fsyncDir
	fsyncDir = func(dir string) error { synced = append(synced, dir); return old(dir) }
	t.Cleanup(func() { fsyncDir = old })
	dir := t.TempDir()
	if err := WriteFileAtomic(filepath.Join(dir, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteHashedAtomic(strings.NewReader("y"), filepath.Join(dir, "b"), crypto.SHA256, nil); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 2 || synced[0] != dir || synced[1] != dir {
		t.Fatalf("synced %v", synced)
	}
	fsyncDir = func(string) error { return errors.New("fsync failed") }
	if err := WriteFileAtomic(filepath.Join(dir, "c"), []byte("x"), 0o644); err == nil {
		t.Fatal("dir fsync error must surface")
	}
}

func TestSweepStaleTemp(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "flatcar", "1.2.3")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	write := func(p string, stale bool) {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if stale {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(filepath.Join(sub, "kernel.123.tmp"), true)
	write(filepath.Join(dir, "version.txt.tmp"), true)
	write(filepath.Join(dir, "fresh.456.tmp"), false)
	write(filepath.Join(dir, "keep.bin"), true)
	if err := os.Symlink("keep.bin", filepath.Join(dir, "link.tmp")); err != nil {
		t.Fatal(err)
	}
	n, err := SweepStaleTemp(dir, time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	assertNoFile(t, filepath.Join(sub, "kernel.123.tmp"))
	for _, keep := range []string{"fresh.456.tmp", "keep.bin", "link.tmp"} {
		if _, err := os.Lstat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("%s removed: %v", keep, err)
		}
	}
	if n, err := SweepStaleTemp(filepath.Join(dir, "missing"), time.Hour); err != nil || n != 0 {
		t.Fatalf("missing dir: n=%d err=%v", n, err)
	}
}

func TestWriteHashedAtomicPublishes0644(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "sub", "f")
	if _, err := WriteHashedAtomic(strings.NewReader("data"), dest, crypto.SHA256, nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

// TestDownloadResumeSendsIfRange: a resumed request names the first
// response's validator in If-Range (strong ETag, else Last-Modified), so an
// upstream that changed in between answers 200 and the download restarts.
func TestDownloadResumeSendsIfRange(t *testing.T) {
	fastBackoff(t)
	lm1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	lm2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	for name, tc := range map[string]struct {
		v1, v2 http.Header
		want   string
	}{
		"etag":                    {http.Header{"Etag": {`"v1"`}, "Last-Modified": {lm1}}, http.Header{"Etag": {`"v2"`}}, `"v1"`},
		"last-modified":           {http.Header{"Last-Modified": {lm1}}, http.Header{"Last-Modified": {lm2}}, lm1},
		"weak etag uses last-mod": {http.Header{"Etag": {`W/"v1"`}, "Last-Modified": {lm1}}, http.Header{"Etag": {`W/"v2"`}}, lm1},
	} {
		t.Run(name, func(t *testing.T) {
			before, after := strings.Repeat("old-", 250), strings.Repeat("new-", 250)
			var calls atomic.Int32
			var mu sync.Mutex
			var gotRange, gotIfRange string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					for k, v := range tc.v1 {
						w.Header()[k] = v
					}
					w.Header().Set("Content-Length", strconv.Itoa(len(before)))
					_, _ = w.Write([]byte(before[:300]))
					w.(http.Flusher).Flush()
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				mu.Lock()
				gotRange, gotIfRange = r.Header.Get("Range"), r.Header.Get("If-Range")
				mu.Unlock()
				// The validator no longer matches: the whole new file.
				for k, v := range tc.v2 {
					w.Header()[k] = v
				}
				_, _ = w.Write([]byte(after))
			}))
			t.Cleanup(srv.Close)
			dest := filepath.Join(t.TempDir(), "f.bin")
			if err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, sumHex(after)); err != nil {
				t.Fatalf("Download: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if gotRange != "bytes=300-" || gotIfRange != tc.want {
				t.Fatalf("Range=%q If-Range=%q, want If-Range %q", gotRange, gotIfRange, tc.want)
			}
			if got, _ := os.ReadFile(dest); string(got) != after {
				t.Fatal("content mismatch")
			}
			assertNoTemp(t, filepath.Dir(dest))
		})
	}
}

// TestDownloadRefetchesAfterResumedChecksumMismatch: without a validator a
// changed upstream still answers the Range; the spliced file fails the
// checksum and one more attempt fetches it from byte 0.
func TestDownloadRefetchesAfterResumedChecksumMismatch(t *testing.T) {
	fastBackoff(t)
	before, after := strings.Repeat("old-", 250), strings.Repeat("new-", 250)
	var calls atomic.Int32
	var mu sync.Mutex
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		rg := r.Header.Get("Range")
		mu.Lock()
		ranges = append(ranges, rg)
		mu.Unlock()
		switch {
		case n == 1:
			w.Header().Set("Content-Length", strconv.Itoa(len(before)))
			_, _ = w.Write([]byte(before[:300]))
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
		case rg != "":
			start, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(after)-1, len(after)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(after[start:]))
		default:
			_, _ = w.Write([]byte(after))
		}
	}))
	t.Cleanup(srv.Close)
	dest := filepath.Join(t.TempDir(), "f.bin")
	if err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, sumHex(after)); err != nil {
		t.Fatalf("Download: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ranges) != 3 || ranges[1] != "bytes=300-" || ranges[2] != "" {
		t.Fatalf("requests' Range headers = %q, want [\"\" \"bytes=300-\" \"\"]", ranges)
	}
	if got, _ := os.ReadFile(dest); string(got) != after {
		t.Fatal("content mismatch")
	}
	assertNoTemp(t, filepath.Dir(dest))
}

func TestDownloadChecksumMismatchWithoutResumeFailsAtOnce(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(srv.Close)
	dest := filepath.Join(t.TempDir(), "f.bin")
	err := Download(context.Background(), srv.Client(), srv.URL, dest, crypto.SHA256, sumHex("other"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	assertNoFile(t, dest)
	assertNoTemp(t, filepath.Dir(dest))
}

func TestSweepStaleTempSkipsUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-0 directories")
	}
	dir := t.TempDir()
	locked, open := filepath.Join(dir, "a-locked"), filepath.Join(dir, "b-open")
	for _, d := range []string{locked, open} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	stale := filepath.Join(open, "x.123.tmp")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	n, err := SweepStaleTemp(dir, time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, want 1 removed past the unreadable directory", n, err)
	}
	assertNoFile(t, stale)
}
