package versions

import (
	"context"
	"crypto"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

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
	if !state.CoreOSUpdateMu.TryLock() {
		slog.Info("CoreOS update already in progress, skipping version check")
		return
	}
	defer state.CoreOSUpdateMu.Unlock()
	ctx := context.Background()
	slog.Debug("Checking CoreOS version")

	arch := viper.GetString(config.CoreOSArchitecture)
	current := state.CurrentCoreOSVersion()
	if current == "" {
		current = loadLocalCoreOSVersion(arch)
		state.SetCurrentCoreOSVersion(current)
	}

	body, remote, err := LoadRemoteCoreOSVersion(ctx, arch)
	if err != nil {
		slog.Error("Could not determine remote CoreOS version, skipping update", "error", err)
		return
	}
	if remote == current {
		return
	}
	slog.Info("Remote CoreOS version differs from local", "remote", remote, "local", current)

	if err := downloadCoreOSArtifacts(ctx, body, remote, arch); err != nil {
		slog.Error("CoreOS artifact download failed, not advancing version", "target", remote, "error", err)
		return
	}
	if err := config.WriteFileAtomic(coreOSJSONPath(), body, 0o644); err != nil {
		slog.Error("Error saving CoreOS streams JSON", "error", err)
	}

	state.SetCurrentCoreOSVersion(remote)
	slog.Info("CoreOS updated", "version", remote)
	removeOldCoreOSArtifacts(current, arch)
}

func loadLocalCoreOSVersion(arch string) string {
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
			slog.Warn("Could not extract checksum, falling back to unverified download", "artifact", artifactType, "error", err)
		}
		hashAlgo := crypto.Hash(0)
		if sha != "" {
			hashAlgo = crypto.SHA256
		}
		if err := config.Download(ctx, config.DownloadClient, base+"/"+file, config.DataPath(file), hashAlgo, sha); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	return nil
}

func removeOldCoreOSArtifacts(oldVersion, arch string) {
	if oldVersion == "" || oldVersion == "0.0.0" {
		return
	}
	for _, file := range coreOSArtifactNames(oldVersion, arch) {
		path := config.DataPath(file)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("Could not remove old CoreOS artifact", "path", path, "error", err)
		}
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
	return fmt.Sprintf("https://builds.coreos.fedoraproject.org/streams/%s.json", viper.GetString(config.CoreOSChannel))
}

func extractCoreOSChecksum(body []byte, arch, artifactType string) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("no JSON body to extract checksum from")
	}
	sha, err := jsonparser.GetString(body, "architectures", arch, "artifacts", "metal", "formats", "pxe", artifactType, "sha256")
	if err != nil {
		return "", fmt.Errorf("SHA256 checksum not found for %s: %w", artifactType, err)
	}
	return sha, nil
}
