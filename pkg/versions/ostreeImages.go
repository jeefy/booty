package versions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

const imageSyncTimeout = 30 * time.Minute

// inflight serializes concurrent pulls of the same image (e.g. a /register
// triggered pull racing the scheduled sync) so the loser sees the digest
// already matching and skips instead of copying twice.
var inflight struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func lockImage(image string) func() {
	inflight.mu.Lock()
	if inflight.locks == nil {
		inflight.locks = map[string]*sync.Mutex{}
	}
	mu, ok := inflight.locks[image]
	if !ok {
		mu = &sync.Mutex{}
		inflight.locks[image] = mu
	}
	inflight.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// RegistryBlobDir is the directory handed to ggcr's disk blob handler; blobs
// end up in <dir>/sha256/<hex>.
func RegistryBlobDir() string {
	return config.DataPath("registry", "blobs")
}

func EnsureOCIFolders() error {
	if err := os.MkdirAll(RegistryBlobDir(), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", RegistryBlobDir(), err)
	}
	return nil
}

func LocalImageRef(image string) string {
	return config.LocalRegistry() + "/" + image
}

func ClientImageRef(image string) string {
	return config.ClientRegistry() + "/" + image
}

// LocalOptions are the crane options for talking to Booty's own registry
// over plain HTTP on loopback.
func LocalOptions(ctx context.Context) []crane.Option {
	return []crane.Option{crane.Insecure, crane.WithContext(ctx), crane.WithAuthFromKeychain(authn.DefaultKeychain)}
}

func RemoteOptions(ctx context.Context, extra ...crane.Option) []crane.Option {
	opts := []crane.Option{crane.WithContext(ctx), crane.WithAuthFromKeychain(authn.DefaultKeychain)}
	return append(opts, extra...)
}

// referencedImages returns the de-duplicated, sorted set of OSTree images
// referenced by registered hosts.
func referencedImages() []string {
	seen := map[string]bool{}
	for _, host := range hardware.Snapshot().Hosts {
		if host.OSTreeImage != "" {
			seen[host.OSTreeImage] = true
		}
	}
	images := make([]string, 0, len(seen))
	for img := range seen {
		images = append(images, img)
	}
	sort.Strings(images)
	return images
}

// OSTreeImageSync mirrors every OSTree image referenced by a registered host
// into the local registry and, when every image synced cleanly, garbage
// collects unreferenced blobs. Concurrent invocations are skipped.
//
// ggcr's in-process registry keeps manifests in memory only, so after a
// restart the local catalog is empty even though the blobs are still on
// disk. ReplayStoredManifests restores the catalog from DataDir/registry/
// manifests first; this pre-sync then only re-copies what changed upstream.
func OSTreeImageSync() {
	if !state.OSTreeSyncMu.TryLock() {
		slog.Info("OSTree image sync already in progress, skipping")
		return
	}
	defer state.OSTreeSyncMu.Unlock()

	if err := EnsureOCIFolders(); err != nil {
		slog.Error("Could not prepare OCI registry folders", "error", err)
		return
	}

	images := referencedImages()
	failures := 0
	for _, image := range images {
		ctx, cancel := context.WithTimeout(context.Background(), imageSyncTimeout)
		copied, err := OSTreeImagePull(ctx, image)
		cancel()
		if err != nil {
			failures++
			slog.Error("Error syncing OCI image", "image", image, "error", err)
			continue
		}
		if copied {
			slog.Info("OCI image synced", "image", image)
		}
	}
	if failures > 0 {
		slog.Warn("OSTree image sync finished with errors; skipping blob GC", "images", len(images), "failures", failures)
		return
	}
	invalidateImageCache()

	if !viper.GetBool(config.OCIGC) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	referenced, err := referencedBlobs(ctx, images)
	if err != nil {
		slog.Error("Could not compute referenced blobs; skipping blob GC", "error", err)
		return
	}
	if !gcBlobs(filepath.Join(RegistryBlobDir(), "sha256"), referenced, viper.GetBool(config.OCIGCEmpty)) {
		return
	}
	if pruned, err := PruneStoredManifests(ManifestStoreDir(), images); err != nil {
		slog.Warn("Pruning stored manifests failed", "error", err)
	} else if pruned > 0 {
		slog.Info("Pruned stored manifests for unreferenced images", "pruned", pruned)
	}
}

// gcBlobs is the pure GC step: an empty referenced set means every blob on
// disk would go, which is only acceptable when the operator opted in via
// --ociGCEmpty. It reports whether GC ran.
func gcBlobs(dir string, referenced map[string]bool, allowEmpty bool) bool {
	if len(referenced) == 0 && !allowEmpty {
		slog.Warn("no ostree images referenced; refusing to wipe the blob cache (set --ociGCEmpty to allow)")
		return false
	}
	deleted, kept, err := deleteUnreferencedBlobs(dir, referenced)
	if err != nil {
		slog.Error("Blob GC failed", "error", err)
	}
	slog.Info("OCI blob GC complete", "referenced", len(referenced), "kept", kept, "deleted", deleted)
	return true
}

// OSTreeImagePull mirrors src into the local registry with a single copy,
// skipping when the local digest already matches upstream. It reports
// whether a copy happened. After a copy the manifest is persisted to disk so
// it can be replayed into the in-memory registry on the next start.
func OSTreeImagePull(ctx context.Context, src string, opts ...crane.Option) (bool, error) {
	if _, err := name.ParseReference(src); err != nil {
		return false, fmt.Errorf("parsing reference %q: %w", src, err)
	}
	defer lockImage(src)()
	remoteOpts := RemoteOptions(ctx, opts...)
	local := LocalImageRef(src)

	remoteDigest, err := crane.Digest(src, remoteOpts...)
	if err != nil {
		return false, fmt.Errorf("resolving upstream digest for %q: %w", src, err)
	}
	if localDigest, err := crane.Digest(local, LocalOptions(ctx)...); err == nil && localDigest == remoteDigest {
		slog.Debug("OCI image already up to date in local cache", "image", src, "digest", remoteDigest)
		return false, nil
	}

	slog.Info("Copying OCI image to local cache", "image", src, "digest", remoteDigest)
	copyOpts := append(RemoteOptions(ctx, opts...), crane.Insecure)
	if err := crane.Copy(src, local, copyOpts...); err != nil {
		return false, fmt.Errorf("copying %q to local registry: %w", src, err)
	}
	if err := StoreManifests(ctx, ManifestStoreDir(), config.LocalRegistry(), src); err != nil {
		slog.Warn("Could not persist manifest for restart replay", "image", src, "error", err)
	}
	return true, nil
}

// referencedBlobs walks each image as stored in the LOCAL registry and
// returns the hex digests of every manifest, config and layer it references.
func referencedBlobs(ctx context.Context, images []string) (map[string]bool, error) {
	o := crane.GetOptions(LocalOptions(ctx)...)
	set := map[string]bool{}
	for _, image := range images {
		ref, err := name.ParseReference(LocalImageRef(image), o.Name...)
		if err != nil {
			return nil, err
		}
		desc, err := remote.Get(ref, o.Remote...)
		if err != nil {
			return nil, fmt.Errorf("reading %s from local registry: %w", image, err)
		}
		set[desc.Digest.Hex] = true
		if err := collectDescriptor(desc, set); err != nil {
			return nil, fmt.Errorf("walking %s: %w", image, err)
		}
	}
	return set, nil
}

func collectDescriptor(desc *remote.Descriptor, set map[string]bool) error {
	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return err
		}
		return collectIndex(idx, set)
	}
	img, err := desc.Image()
	if err != nil {
		return err
	}
	return collectImage(img, set)
}

func collectIndex(idx v1.ImageIndex, set map[string]bool) error {
	mf, err := idx.IndexManifest()
	if err != nil {
		return err
	}
	for _, m := range mf.Manifests {
		set[m.Digest.Hex] = true
		if m.MediaType.IsIndex() {
			child, err := idx.ImageIndex(m.Digest)
			if err != nil {
				return err
			}
			if err := collectIndex(child, set); err != nil {
				return err
			}
			continue
		}
		img, err := idx.Image(m.Digest)
		if err != nil {
			return err
		}
		if err := collectImage(img, set); err != nil {
			return err
		}
	}
	return nil
}

func collectImage(img v1.Image, set map[string]bool) error {
	digest, err := img.Digest()
	if err != nil {
		return err
	}
	set[digest.Hex] = true
	mf, err := img.Manifest()
	if err != nil {
		return err
	}
	set[mf.Config.Digest.Hex] = true
	for _, layer := range mf.Layers {
		set[layer.Digest.Hex] = true
	}
	return nil
}

// deleteUnreferencedBlobs removes every regular file in dir whose name is
// not in referenced. A missing dir is not an error.
func deleteUnreferencedBlobs(dir string, referenced map[string]bool) (deleted, kept int, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	var errs []error
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if referenced[e.Name()] {
			kept++
			continue
		}
		path := filepath.Join(dir, e.Name())
		if rmErr := os.Remove(path); rmErr != nil {
			errs = append(errs, rmErr)
			continue
		}
		slog.Debug("Removed unreferenced blob", "path", path)
		deleted++
	}
	return deleted, kept, errors.Join(errs...)
}

// Job is an extra periodic task run on the same cron schedule as the
// version checks.
type Job struct {
	Name string
	Fn   func()
}

// StartScheduler runs the Flatcar, CoreOS, Bluefin and OSTree checks, plus any extra
// jobs, on schedule. Every job is a singleton: a tick that arrives while the
// previous run is still executing is dropped.
func StartScheduler(schedule string, extra ...Job) (gocron.Scheduler, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, err
	}
	jobs := append([]Job{
		{"flatcar", FlatcarVersionCheck},
		{"coreos", CoreOSVersionCheck},
		{"bluefin", BluefinVersionCheck},
		{"ostree", OSTreeImageSync},
	}, extra...)
	for _, job := range jobs {
		_, err := s.NewJob(
			gocron.CronJob(schedule, false),
			gocron.NewTask(job.Fn),
			gocron.WithName(job.Name),
			gocron.WithSingletonMode(gocron.LimitModeReschedule),
		)
		if err != nil {
			return nil, fmt.Errorf("scheduling %s check with %q: %w", job.Name, schedule, err)
		}
	}
	s.Start()
	slog.Info("Update scheduler started", "schedule", schedule)
	return s, nil
}
