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
	"strings"
)

// Download fetches url into dest. Data is streamed to dest+".tmp" and only
// renamed into place once the transfer completed and, when expectedHex is
// non-empty, the digest computed with h (crypto.SHA256 or crypto.SHA512)
// matched. On any failure no file is left behind at dest or dest+".tmp".
func Download(ctx context.Context, client *http.Client, url, dest string, h crypto.Hash, expectedHex string) error {
	var hasher hash.Hash
	if expectedHex != "" {
		if h != crypto.SHA256 && h != crypto.SHA512 {
			return fmt.Errorf("download %s: unsupported checksum algorithm %v", url, h)
		}
		hasher = h.New()
	}

	slog.Info("Downloading", "url", url, "dest", dest, "verify", hasher != nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer CloseQuietly(resp.Body, url)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed for %s: HTTP %d", url, resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	cleanup := func(err error) error {
		if rmErr := os.Remove(tmp); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Warn("Could not remove partial download", "path", tmp, "error", rmErr)
		}
		return err
	}

	w := io.Writer(f)
	if hasher != nil {
		w = io.MultiWriter(f, hasher)
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		CloseQuietly(f, tmp)
		return cleanup(fmt.Errorf("download %s: %w", url, err))
	}
	if err := f.Sync(); err != nil {
		CloseQuietly(f, tmp)
		return cleanup(err)
	}
	if err := f.Close(); err != nil {
		return cleanup(err)
	}

	if hasher != nil {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actual, expectedHex) {
			return cleanup(fmt.Errorf("checksum mismatch for %s: expected %s, got %s", url, strings.ToLower(expectedHex), actual))
		}
	}

	if err := os.Rename(tmp, dest); err != nil {
		return cleanup(err)
	}
	slog.Info("Download completed", "url", url, "dest", dest, "size_bytes", n)
	return nil
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
	return nil
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
