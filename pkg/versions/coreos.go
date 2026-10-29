package versions

import (
	"context"
	"crypto"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/buger/jsonparser"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

func coreOSJSONPath() string {
	return config.DataPath(viper.GetString(config.CoreOSChannel) + ".json")
}

func coreOSArtifactNames(version, arch string) map[string]string {
	return map[string]string{
		"initramfs": fmt.Sprintf("fedora-coreos-%s-live-initramfs.%s.img", version, arch),
		"kernel":    fmt.Sprintf("fedora-coreos-%s-live-kernel-%s", version, arch),
		"rootfs":    fmt.Sprintf("fedora-coreos-%s-live-rootfs.%s.img", version, arch),
	}
}

// CoreOSVersionCheck downloads the channel's latest Fedora CoreOS PXE
// artifacts when they differ from the served version. Concurrent invocations
// are skipped.
func CoreOSVersionCheck() {
	arch := viper.GetString(config.CoreOSArchitecture)
	var body []byte
	runReleaseSync(releaseSync{
		os:      OSCoreOS,
		label:   "CoreOS",
		tracked: config.CoreOSTracked,
		tryLock: state.TryLockCoreOSUpdate,
		local: func() string {
			current := state.CurrentCoreOSVersion()
			if current == "" {
				current = loadLocalCoreOSVersion(arch)
				state.SetCurrentCoreOSVersion(current)
			}
			return current
		},
		target: func(ctx context.Context) (string, error) {
			type stream struct {
				body    []byte
				version string
			}
			st, err := retryMetadata(ctx, "CoreOS streams", func(ctx context.Context) (stream, error) {
				b, v, err := LoadRemoteCoreOSVersion(ctx, arch)
				return stream{b, v}, err
			})
			body = st.body
			return st.version, err
		},
		install: func(ctx context.Context, target, current string) error {
			return installCoreOSRelease(ctx, body, target, current, arch)
		},
		setCurrent: state.SetCurrentCoreOSVersion,
	})
}

// installCoreOSRelease downloads and verifies target's PXE artifacts, saves
// the streams JSON and links target as coreos/current.
func installCoreOSRelease(ctx context.Context, body []byte, target, current, arch string) error {
	if err := downloadCoreOSArtifacts(ctx, body, target, arch); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(coreOSJSONPath(), body, 0o644); err != nil {
		slog.Error("Error saving CoreOS streams JSON", "error", err)
	}
	if err := linkCoreOSRelease(target, current, arch); err != nil {
		return fmt.Errorf("links: %w", err)
	}
	return nil
}

// linkCoreOSRelease makes version the current CoreOS release and swaps the
// top-level compatibility names (fedora-coreos-<version>-live-*) from the
// release it replaces to symlinks into coreos/current.
func linkCoreOSRelease(version, old, arch string) error {
	if err := linkRelease(OSCoreOS, version); err != nil {
		return err
	}
	if versionIsSet(old) && old != version {
		removeCoreOSCompatLinks(old, arch)
	}
	for _, file := range coreOSArtifactNames(version, arch) {
		if err := config.ReplaceSymlink(filepath.Join(OSCoreOS, CurrentLink, file), config.DataPath(file)); err != nil {
			return fmt.Errorf("linking %s: %w", file, err)
		}
	}
	return nil
}

func removeCoreOSCompatLinks(version, arch string) {
	for _, file := range coreOSArtifactNames(version, arch) {
		path := config.DataPath(file)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("Could not remove old CoreOS artifact link", "path", path, "error", err)
		}
	}
}

// CoreOSDataDir is the /data/ path prefix ("coreos/<version>") of the
// release a host boots, "" while no release is cached (the compatibility
// names at the top of DataDir then).
func CoreOSDataDir(version string) string {
	if version == "" {
		return ""
	}
	return OSCoreOS + "/" + version
}

// loadLocalCoreOSVersion is the release coreos/current names, falling back
// to the streams JSON of installs that predate the release directories.
func loadLocalCoreOSVersion(arch string) string {
	if v := CurrentRelease(OSCoreOS); v != "" {
		slog.Info("Local CoreOS version found", "version", v)
		return v
	}
	path := coreOSJSONPath()
	b, err := os.ReadFile(path)
	if err != nil {
		slog.Info("CoreOS streams JSON not found, starting from 0.0.0", "path", path)
		return "0.0.0"
	}
	v, err := jsonparser.GetString(b, "architectures", arch, "artifacts", "metal", "release")
	if err != nil || v == "" {
		slog.Warn("Local CoreOS streams JSON is invalid, starting from 0.0.0", "path", path, "error", err)
		return "0.0.0"
	}
	slog.Info("Local CoreOS version found", "version", v)
	return v
}

func downloadCoreOSArtifacts(ctx context.Context, body []byte, version, arch string) error {
	base := coreOSReleaseURL(version, arch)
	names := coreOSArtifactNames(version, arch)
	for _, artifactType := range []string{"initramfs", "kernel", "rootfs"} {
		file := names[artifactType]
		sha, err := extractCoreOSChecksum(body, arch, artifactType)
		if err != nil {
			removeOldCoreOSArtifacts(version, arch)
			return fmt.Errorf("%s: refusing to install without its sha256: %w: %w", file, errVerification, err)
		}
		// Fedora has renamed artifacts over time (e.g. -live-kernel-x86_64
		// became -live-kernel.x86_64), so fetch from the location the stream
		// publishes and keep our stable local name for the boot scripts.
		url := base + "/" + file
		if loc, err := extractCoreOSLocation(body, arch, artifactType); err == nil {
			url = loc
		} else {
			slog.Warn("Could not extract artifact location, falling back to constructed URL", "artifact", artifactType, "error", err)
		}
		if err := config.Download(ctx, config.DownloadClient, url, filepath.Join(ReleaseDir(OSCoreOS, version), file), crypto.SHA256, sha); err != nil {
			removeOldCoreOSArtifacts(version, arch)
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	return nil
}

// removeOldCoreOSArtifacts deletes an incomplete or retired release: its
// top-level names (files from before the release directories, or the
// compatibility symlinks) and its coreos/<version>/ directory.
func removeOldCoreOSArtifacts(oldVersion, arch string) {
	if !versionIsSet(oldVersion) {
		return
	}
	removeCoreOSCompatLinks(oldVersion, arch)
	dir := ReleaseDir(OSCoreOS, oldVersion)
	if err := os.RemoveAll(dir); err != nil {
		slog.Warn("Could not remove old CoreOS release", "path", dir, "error", err)
	}
}

// LoadRemoteCoreOSVersion fetches the Fedora CoreOS streams JSON, records the
// release for arch in state and returns the raw body for checksum extraction.
func LoadRemoteCoreOSVersion(ctx context.Context, arch string) ([]byte, string, error) {
	url := RemoteCoreOSJSONURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := config.MetadataClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", url, err)
	}
	defer config.CloseQuietly(resp.Body, url)
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", url, err)
	}
	v, err := jsonparser.GetString(b, "architectures", arch, "artifacts", "metal", "release")
	if err != nil {
		return nil, "", fmt.Errorf("%s: release not found for %s: %w", url, arch, err)
	}
	state.SetRemoteCoreOSVersion(v)
	slog.Debug("Remote CoreOS version found", "version", v)
	return b, v, nil
}

func coreOSReleaseURL(version, arch string) string {
	return fmt.Sprintf(viper.GetString(config.CoreOSURL), viper.GetString(config.CoreOSChannel), version, arch)
}

func RemoteCoreOSJSONURL() string {
	return fmt.Sprintf(viper.GetString(config.CoreOSStreamsURL), viper.GetString(config.CoreOSChannel))
}

func extractCoreOSLocation(body []byte, arch, artifactType string) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("no JSON body to extract location from")
	}
	loc, err := jsonparser.GetString(body, "architectures", arch, "artifacts", "metal", "formats", "pxe", artifactType, "location")
	if err != nil {
		return "", fmt.Errorf("location not found for %s: %w", artifactType, err)
	}
	if !strings.HasPrefix(loc, "https://") {
		return "", fmt.Errorf("refusing non-https location for %s: %q", artifactType, loc)
	}
	return loc, nil
}

func extractCoreOSChecksum(body []byte, arch, artifactType string) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("no JSON body to extract checksum from")
	}
	sha, err := jsonparser.GetString(body, "architectures", arch, "artifacts", "metal", "formats", "pxe", artifactType, "sha256")
	if err != nil {
		return "", fmt.Errorf("SHA256 checksum not found for %s: %w", artifactType, err)
	}
	if sha == "" {
		return "", fmt.Errorf("SHA256 checksum for %s is empty", artifactType)
	}
	return sha, nil
}
