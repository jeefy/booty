package versions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/jeefy/booty/pkg/config"
)

// storedManifest is what gets written to DataDir/registry/manifests. ggcr's
// in-process registry only keeps manifests in memory, so this is the sole
// record that lets a cached image become resolvable again after a restart
// without talking to upstream.
type storedManifest struct {
	MediaType string          `json:"mediaType"`
	Digest    string          `json:"digest"`
	Raw       json.RawMessage `json:"raw"`
}

const childFilePrefix = "@sha256_"

// ManifestStoreDir is where manifests of mirrored images are persisted.
func ManifestStoreDir() string {
	return config.DataPath("registry", "manifests")
}

func sanitizeTag(tag string) string {
	var b strings.Builder
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// manifestFilePaths mirrors the registry's own layout: the repo path is the
// literal path the image is served under locally (ubuntu:latest lives at
// /v2/ubuntu/, not /v2/index.docker.io/library/ubuntu/).
func manifestFilePaths(dir, image string) (repoDir, tagFile string, err error) {
	ref, err := name.ParseReference("localhost/"+image, name.Insecure)
	if err != nil {
		return "", "", err
	}
	tag, ok := ref.(name.Tag)
	if !ok {
		return "", "", fmt.Errorf("%q: only tagged references are stored", image)
	}
	repo, err := config.CleanRelPath(ref.Context().RepositoryStr())
	if err != nil {
		return "", "", fmt.Errorf("repository path for %q: %w", image, err)
	}
	repoDir = filepath.Join(dir, filepath.FromSlash(repo))
	return repoDir, filepath.Join(repoDir, sanitizeTag(tag.TagStr())+".json"), nil
}

func writeStoredManifest(path string, desc *remote.Descriptor) error {
	data, err := json.Marshal(storedManifest{
		MediaType: string(desc.MediaType),
		Digest:    desc.Digest.String(),
		Raw:       desc.Manifest,
	})
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, data, 0o644)
}

// StoreManifests fetches image (as mirrored under registry, reached over
// plain HTTP) and persists its manifest to dir. For an index every child
// manifest is stored by digest as well so the index can be replayed.
func StoreManifests(ctx context.Context, dir, registry, image string) error {
	repoDir, tagFile, err := manifestFilePaths(dir, image)
	if err != nil {
		return err
	}
	ref, err := name.ParseReference(registry+"/"+image, name.Insecure)
	if err != nil {
		return err
	}
	opts := []remote.Option{remote.WithContext(ctx)}
	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return fmt.Errorf("fetching %s from local registry: %w", image, err)
	}
	if desc.MediaType.IsIndex() {
		if err := storeIndexChildren(ctx, repoDir, ref.Context(), desc, opts); err != nil {
			return err
		}
	}
	return writeStoredManifest(tagFile, desc)
}

func storeIndexChildren(ctx context.Context, repoDir string, repo name.Repository, desc *remote.Descriptor, opts []remote.Option) error {
	im, err := v1.ParseIndexManifest(bytes.NewReader(desc.Manifest))
	if err != nil {
		return fmt.Errorf("parsing index %s: %w", desc.Digest, err)
	}
	for _, m := range im.Manifests {
		if !m.MediaType.IsIndex() && !m.MediaType.IsImage() {
			continue
		}
		child, err := remote.Get(repo.Digest(m.Digest.String()), opts...)
		if err != nil {
			return fmt.Errorf("fetching child %s: %w", m.Digest, err)
		}
		if child.MediaType.IsIndex() {
			if err := storeIndexChildren(ctx, repoDir, repo, child, opts); err != nil {
				return err
			}
		}
		if err := writeStoredManifest(filepath.Join(repoDir, childFilePrefix+m.Digest.Hex+".json"), child); err != nil {
			return err
		}
	}
	return nil
}

type replayEntry struct {
	repo      string
	target    string
	mediaType string
	raw       []byte
	isChild   bool
}

func loadReplayEntries(dir string) ([]replayEntry, error) {
	var entries []replayEntry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(dir, filepath.Dir(path))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var sm storedManifest
		if err := json.Unmarshal(data, &sm); err != nil {
			slog.Warn("Skipping unparseable stored manifest", "path", path, "error", err)
			return nil
		}
		e := replayEntry{repo: filepath.ToSlash(rel), mediaType: sm.MediaType, raw: sm.Raw}
		if strings.HasPrefix(d.Name(), childFilePrefix) {
			e.isChild = true
			e.target = sm.Digest
		} else {
			e.target = strings.TrimSuffix(d.Name(), ".json")
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// ggcr rejects an index whose children are not present yet, so replay
	// order is: child images, child indexes, then tags.
	rank := func(e replayEntry) int {
		switch {
		case e.isChild && !types.MediaType(e.mediaType).IsIndex():
			return 0
		case e.isChild:
			return 1
		}
		return 2
	}
	sort.SliceStable(entries, func(i, j int) bool {
		ri, rj := rank(entries[i]), rank(entries[j])
		if ri != rj {
			return ri < rj
		}
		return entries[i].repo+"/"+entries[i].target < entries[j].repo+"/"+entries[j].target
	})
	return entries, nil
}

func putManifest(ctx context.Context, client *http.Client, baseURL string, e replayEntry) error {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", strings.TrimSuffix(baseURL, "/"), e.repo, e.target)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(e.raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", e.mediaType)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer config.CloseQuietly(resp.Body, url)
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("PUT %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ReplayManifests pushes every manifest stored under dir into the registry
// at baseURL (e.g. http://127.0.0.1:8080). It returns how many were
// restored and how many failed; a missing dir is not an error.
func ReplayManifests(ctx context.Context, dir, baseURL string) (restored, failed int, err error) {
	entries, err := loadReplayEntries(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	for _, e := range entries {
		if err := putManifest(ctx, client, baseURL, e); err != nil {
			slog.Warn("Could not replay stored manifest", "repo", e.repo, "target", e.target, "error", err)
			failed++
			continue
		}
		restored++
	}
	return restored, failed, nil
}

// ReplayStoredManifests restores the local registry catalog from
// ManifestStoreDir at startup. Failures are logged, never fatal.
func ReplayStoredManifests(ctx context.Context, baseURL string) {
	restored, failed, err := ReplayManifests(ctx, ManifestStoreDir(), baseURL)
	if err != nil {
		slog.Warn("Could not replay stored OCI manifests", "error", err)
		return
	}
	if restored == 0 && failed == 0 {
		return
	}
	slog.Info("Replayed stored OCI manifests into local registry", "restored", restored, "failed", failed)
	invalidateImageCache()
}

// PruneStoredManifests deletes stored manifests for <repo>:<tag> pairs not
// in keep, child manifests no longer referenced by any kept index, and
// directories left empty. It returns the number of files removed.
func PruneStoredManifests(dir string, keep []string) (int, error) {
	keepTags := map[string]map[string]bool{}
	for _, image := range keep {
		repoDir, tagFile, err := manifestFilePaths(dir, image)
		if err != nil {
			continue
		}
		if keepTags[repoDir] == nil {
			keepTags[repoDir] = map[string]bool{}
		}
		keepTags[repoDir][filepath.Base(tagFile)] = true
	}
	keepChildren := map[string]bool{}
	for repoDir, tags := range keepTags {
		for tag := range tags {
			for _, hex := range indexChildren(filepath.Join(repoDir, tag)) {
				keepChildren[filepath.Join(repoDir, childFilePrefix+hex+".json")] = true
			}
		}
	}

	pruned := 0
	var errs []error
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		if strings.HasPrefix(d.Name(), childFilePrefix) {
			if keepChildren[path] {
				return nil
			}
		} else if keepTags[filepath.Dir(path)][d.Name()] {
			return nil
		}
		if rmErr := os.Remove(path); rmErr != nil {
			errs = append(errs, rmErr)
			return nil
		}
		pruned++
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return pruned, err
	}
	removeEmptyDirs(dir)
	return pruned, errors.Join(errs...)
}

// indexChildren returns the hex digests of every manifest (transitively,
// via sibling child files) referenced by the stored manifest at path.
func indexChildren(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var sm storedManifest
	if err := json.Unmarshal(data, &sm); err != nil || !types.MediaType(sm.MediaType).IsIndex() {
		return nil
	}
	im, err := v1.ParseIndexManifest(bytes.NewReader(sm.Raw))
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range im.Manifests {
		out = append(out, m.Digest.Hex)
		if m.MediaType.IsIndex() {
			out = append(out, indexChildren(filepath.Join(filepath.Dir(path), childFilePrefix+m.Digest.Hex+".json"))...)
		}
	}
	return out
}

func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		if entries, err := os.ReadDir(d); err == nil && len(entries) == 0 {
			_ = os.Remove(d)
		}
	}
}
