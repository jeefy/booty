package versions

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

func EnsureOCIFolders() error {
	for _, dir := range [][]string{
		{"registry"},
		{"registry", "blobs"},
		{"registry", "blobs", "sha256"},
	} {
		path := config.DataPath(dir...)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
	}
	symSrc, err := filepath.Abs(config.DataPath("registry", "blobs", "sha256"))
	if err != nil {
		return fmt.Errorf("resolving registry blob path: %w", err)
	}
	link := config.DataPath("registry", "sha256")
	if err := os.Symlink(symSrc, link); err != nil && !os.IsExist(err) {
		return fmt.Errorf("creating registry symlink %s: %w", link, err)
	}
	return nil
}

// LocalImageRef returns the reference under which image is cached in Booty's
// embedded registry.
func LocalImageRef(image string) string {
	return fmt.Sprintf("%s:%d/%s", viper.GetString(config.ServerIP), viper.GetInt(config.HttpPort), image)
}

// OSTreeImageSync mirrors every OSTree image referenced by a registered host
// into the local registry. Concurrent invocations are skipped.
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
	pulled := make(map[string]bool)
	for _, host := range hardware.Snapshot().Hosts {
		if host.OSTreeImage == "" || pulled[host.OSTreeImage] {
			continue
		}
		if err := OSTreeImagePull(host.OSTreeImage); err != nil {
			slog.Error("Error copying OCI image", "image", host.OSTreeImage, "error", err)
			continue
		}
		slog.Info("Done copying OCI image", "image", host.OSTreeImage)
		pulled[host.OSTreeImage] = true
	}
}

func OSTreeImagePull(src string) error {
	o := crane.Options{
		Remote: []remote.Option{
			remote.WithAuthFromKeychain(authn.DefaultKeychain),
		},
		Keychain: authn.DefaultKeychain,
	}

	srcRef, err := name.ParseReference(src)
	if err != nil {
		return fmt.Errorf("parsing reference %q: %w", src, err)
	}

	img, err := remote.Image(srcRef, o.Remote...)
	if err != nil {
		return fmt.Errorf("fetching image %q: %w", srcRef, err)
	}

	slog.Info("Saving image", "ref", srcRef.String())
	if err := crane.SaveOCI(img, config.DataPath("registry")); err != nil {
		return fmt.Errorf("saving image %q: %w", srcRef, err)
	}

	localImage := LocalImageRef(src)
	if err := crane.Copy(src, localImage); err != nil {
		return fmt.Errorf("copying image %q to local registry: %w", srcRef, err)
	}
	slog.Info("Done saving image", "ref", srcRef.String())

	if digest, err := crane.Digest(localImage); err != nil {
		slog.Error("Error reading image back from cache", "image", localImage, "error", err)
	} else if digest == "" {
		slog.Warn("Image not found in local cache yet", "image", localImage)
	}
	return nil
}

// StartScheduler runs the Flatcar, CoreOS and OSTree checks on schedule with
// a single scheduler; every job is in singleton mode so a slow run is never
// overlapped by the next tick.
func StartScheduler(schedule string) (*gocron.Scheduler, error) {
	s := gocron.NewScheduler(time.UTC)
	jobs := []struct {
		name string
		fn   func()
	}{
		{"flatcar", FlatcarVersionCheck},
		{"coreos", CoreOSVersionCheck},
		{"ostree", OSTreeImageSync},
	}
	for _, job := range jobs {
		if _, err := s.Cron(schedule).SingletonMode().Do(job.fn); err != nil {
			return nil, fmt.Errorf("scheduling %s check with %q: %w", job.name, schedule, err)
		}
	}
	s.StartAsync()
	slog.Info("Update scheduler started", "schedule", schedule)
	return s, nil
}
