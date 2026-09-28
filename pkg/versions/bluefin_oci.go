package versions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jeefy/booty/pkg/config"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/viper"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const (
	bluefinOCILatestTag = "latest"
	// bluefinOCIPlainPrefix selects plain HTTP for a local registry
	// (--bluefinOCI=http://<registry-host>:30500/bluefin-server).
	bluefinOCIPlainPrefix = "http://"
	maxOCIManifestBytes   = 4 << 20
	// ghcrHost is the only registry --githubToken is sent to.
	ghcrHost = "ghcr.io"
)

// ociBluefinSource reads a release from an ORAS artifact: one layer per
// release file, named by its org.opencontainers.image.title annotation,
// tagged <version> and latest.
type ociBluefinSource struct {
	repo      *remote.Repository
	mu        sync.Mutex
	manifests map[string]ocispec.Manifest
}

func newOCIBluefinSource(ref string) (*ociBluefinSource, error) {
	plain := strings.HasPrefix(ref, bluefinOCIPlainPrefix)
	ref = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(ref, bluefinOCIPlainPrefix), "https://"), "/")
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", ref, err)
	}
	if repo.Reference.Reference != "" {
		return nil, fmt.Errorf("%q: name a repository without a tag or digest; Booty resolves %s or the pinned version itself", ref, bluefinOCILatestTag)
	}
	repo.PlainHTTP = plain
	client := &auth.Client{Client: config.DownloadClient, Cache: auth.NewCache()}
	if token := strings.TrimSpace(viper.GetString(config.GithubToken)); token != "" && repo.Reference.Registry == ghcrHost {
		client.Credential = auth.StaticCredential(ghcrHost, auth.Credential{Username: "booty", Password: token})
	}
	repo.Client = client
	return &ociBluefinSource{repo: repo, manifests: map[string]ocispec.Manifest{}}, nil
}

func (s *ociBluefinSource) String() string { return s.repo.Reference.String() }

// resolve reads the manifest behind tag and returns it with the version its
// netboot UKI layer names.
func (s *ociBluefinSource) resolve(ctx context.Context, tag string) (string, ocispec.Manifest, error) {
	desc, err := s.repo.Resolve(ctx, tag)
	if err != nil {
		return "", ocispec.Manifest{}, fmt.Errorf("%s:%s: %w", s, tag, err)
	}
	if desc.MediaType != ocispec.MediaTypeImageManifest {
		return "", ocispec.Manifest{}, fmt.Errorf("%s:%s is a %s, want an OCI image manifest", s, tag, desc.MediaType)
	}
	if desc.Size > maxOCIManifestBytes {
		return "", ocispec.Manifest{}, fmt.Errorf("%s:%s: manifest of %d bytes is too large", s, tag, desc.Size)
	}
	body, err := content.FetchAll(ctx, s.repo, desc)
	if err != nil {
		return "", ocispec.Manifest{}, fmt.Errorf("%s:%s: %w", s, tag, err)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return "", ocispec.Manifest{}, fmt.Errorf("%s:%s: manifest: %w", s, tag, err)
	}
	version, err := ociBluefinVersion(m)
	if err != nil {
		return "", ocispec.Manifest{}, fmt.Errorf("%s:%s (%s): %w", s, tag, desc.Digest, err)
	}
	s.mu.Lock()
	s.manifests[version] = m
	s.mu.Unlock()
	return version, m, nil
}

// ociBluefinVersion picks the version from the single
// bluefin-server-netboot_<version>.efi layer.
func ociBluefinVersion(m ocispec.Manifest) (string, error) {
	var versions []string
	for _, l := range m.Layers {
		title := l.Annotations[ocispec.AnnotationTitle]
		if strings.HasPrefix(title, bluefinNetbootPrefix) && strings.HasSuffix(title, bluefinNetbootSuffix) {
			versions = append(versions, strings.TrimSuffix(strings.TrimPrefix(title, bluefinNetbootPrefix), bluefinNetbootSuffix))
		}
	}
	if len(versions) != 1 {
		return "", fmt.Errorf("want exactly one %s<version>%s layer, found %d", bluefinNetbootPrefix, bluefinNetbootSuffix, len(versions))
	}
	if !ValidBluefinVersion(versions[0]) {
		return "", fmt.Errorf("netboot UKI version %q is not usable", versions[0])
	}
	return versions[0], nil
}

func (s *ociBluefinSource) latest(ctx context.Context) (string, error) {
	version, _, err := s.resolve(ctx, bluefinOCILatestTag)
	if err != nil {
		return "", err
	}
	slog.Debug("Remote Bluefin version found", "version", version, "oci", s.String())
	return version, nil
}

func (s *ociBluefinSource) manifest(ctx context.Context, version string) (ocispec.Manifest, error) {
	s.mu.Lock()
	m, ok := s.manifests[version]
	s.mu.Unlock()
	if ok {
		return m, nil
	}
	got, m, err := s.resolve(ctx, version)
	if err != nil {
		return m, err
	}
	if got != version {
		return m, fmt.Errorf("%s:%s carries the netboot UKI of %s", s, version, got)
	}
	return m, nil
}

func (s *ociBluefinSource) layer(ctx context.Context, version, name string) (ocispec.Descriptor, error) {
	m, err := s.manifest(ctx, version)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	for _, l := range m.Layers {
		if l.Annotations[ocispec.AnnotationTitle] == name {
			return l, nil
		}
	}
	return ocispec.Descriptor{}, fmt.Errorf("%s:%s: %s: %w", s, version, name, errBluefinAssetMissing)
}

func (s *ociBluefinSource) fetch(ctx context.Context, version, name string, limit int64) ([]byte, error) {
	desc, err := s.layer(ctx, version, name)
	if err != nil {
		return nil, err
	}
	if desc.Size > limit {
		return nil, fmt.Errorf("%s: layer of %d bytes exceeds %d", name, desc.Size, limit)
	}
	return content.FetchAll(ctx, s.repo.Blobs(), desc)
}

func (s *ociBluefinSource) download(ctx context.Context, version, name, dest, sha string) error {
	desc, err := s.layer(ctx, version, name)
	if err != nil {
		return err
	}
	slog.Info("Downloading", "oci", s.String()+":"+version, "file", name, "dest", dest, "digest", desc.Digest)
	rc, err := s.repo.Blobs().Fetch(ctx, desc)
	if err != nil {
		return err
	}
	defer config.CloseQuietly(rc, name)
	vr := content.NewVerifyReader(rc, desc)
	return writeVerified(vr, dest, sha, vr.Verify)
}

// writeVerified streams r to dest through dest+".tmp" and renames it into
// place only when the sha256 of the bytes matches want (empty skips that
// check) and check (if any) passes.
func writeVerified(r io.Reader, dest, want string, check func() error) error {
	_, err := writeHashed(r, dest, func(got string) error {
		if want != "" && !strings.EqualFold(got, want) {
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", filepath.Base(dest), strings.ToLower(want), got)
		}
		if check != nil {
			return check()
		}
		return nil
	})
	return err
}

func writeHashed(r io.Reader, dest string, accept func(sha string) error) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		config.CloseQuietly(f, tmp)
		if rmErr := os.Remove(tmp); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			slog.Warn("Could not remove partial file", "path", tmp, "error", rmErr)
		}
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := accept(sum); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fail(err)
	}
	return sum, nil
}
