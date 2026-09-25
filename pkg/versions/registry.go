package versions

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/jeefy/booty/pkg/config"
)

const (
	imageCacheTTL       = 60 * time.Second
	upstreamGetTimeout  = 10 * time.Second
	localRegistryPrefix = "local registry: "
)

type CachedImage struct {
	Registry string `json:"registry"`
	Image    string `json:"image"`
	Tag      string `json:"tag"`
	Digest   string `json:"digest"`
	UpToDate bool   `json:"upToDate"`
	Error    string `json:"error,omitempty"`
}

// LocalRegistryError marks failures talking to Booty's own registry so the
// HTTP layer can map them to 502.
type LocalRegistryError struct{ Err error }

func (e *LocalRegistryError) Error() string { return localRegistryPrefix + e.Err.Error() }
func (e *LocalRegistryError) Unwrap() error { return e.Err }

var imageCache struct {
	mu      sync.Mutex
	images  []CachedImage
	expires time.Time
}

func invalidateImageCache() {
	imageCache.mu.Lock()
	imageCache.expires = time.Time{}
	imageCache.mu.Unlock()
}

// ListCachedImages returns the local registry contents with their
// up-to-date status versus upstream, memoized for imageCacheTTL.
func ListCachedImages(ctx context.Context) ([]CachedImage, error) {
	imageCache.mu.Lock()
	defer imageCache.mu.Unlock()
	if time.Now().Before(imageCache.expires) {
		return append([]CachedImage(nil), imageCache.images...), nil
	}
	images, err := listCachedImages(ctx)
	if err != nil {
		return nil, err
	}
	imageCache.images = images
	imageCache.expires = time.Now().Add(imageCacheTTL)
	return append([]CachedImage(nil), images...), nil
}

func listCachedImages(ctx context.Context) ([]CachedImage, error) {
	registry := config.LocalRegistry()
	localOpts := LocalOptions(ctx)

	repos, err := crane.Catalog(registry, localOpts...)
	if err != nil {
		return nil, &LocalRegistryError{Err: fmt.Errorf("catalog: %w", err)}
	}

	images := []CachedImage{}
	for _, repo := range repos {
		tags, err := crane.ListTags(registry+"/"+repo, localOpts...)
		if err != nil {
			return nil, &LocalRegistryError{Err: fmt.Errorf("tags for %s: %w", repo, err)}
		}
		for _, tag := range tags {
			ref := fmt.Sprintf("%s:%s", repo, tag)
			cached, err := crane.Get(LocalImageRef(ref), localOpts...)
			if err != nil {
				return nil, &LocalRegistryError{Err: fmt.Errorf("manifest %s: %w", ref, err)}
			}
			entry := CachedImage{Registry: registry, Image: repo, Tag: tag, Digest: cached.Digest.String()}

			upCtx, cancel := context.WithTimeout(ctx, upstreamGetTimeout)
			upstream, err := crane.Get(ref, RemoteOptions(upCtx)...)
			cancel()
			if err != nil {
				slog.Warn("Upstream image lookup failed", "image", ref, "error", err)
				entry.Error = err.Error()
			} else {
				entry.UpToDate = cached.Digest == upstream.Digest
			}
			images = append(images, entry)
		}
	}
	return images, nil
}
