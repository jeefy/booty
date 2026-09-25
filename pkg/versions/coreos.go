package versions

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/buger/jsonparser"
	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func StartCoreOSCron() {
	slog.Info("Starting CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(CoreOSVersionCheck)
	if err != nil {
		slog.Error("Error creating prune cronjob", "error", err)
		os.Exit(1)
	}
	cron.StartAsync()
}

func CoreOSVersionCheck() {
	if viper.GetBool(config.Updating) {
		slog.Info("Already updating, skipping version check")
		return
	}
	slog.Debug("Checking remote coreos version")

	if viper.GetString(config.CurrentCoreOSVersion) == "" {
		// Check for an existing coreos.json file
		if b, err := os.ReadFile(fmt.Sprintf("%s/%s.json", viper.GetString(config.DataDir), viper.GetString(config.CoreOSChannel))); err == nil {
			slog.Info("Found old coreos json, setting current version to that")
			oldVersion, err := jsonparser.GetString(b, "architectures", viper.GetString(config.CoreOSArchitecture), "artifacts", "metal", "release")
			if err != nil {
				slog.Error("Old coreos json file is invalid", "channel", viper.GetString(config.CoreOSChannel))
				slog.Error("JSON parse error", "error", err)
			}
			viper.Set(config.CurrentCoreOSVersion, oldVersion)
			slog.Info("CoreOS version set", "version", oldVersion)
		} else {
			slog.Info("CoreOS json not found, setting current version to 0.0.0", "path", fmt.Sprintf("%s/%s.json", viper.GetString(config.DataDir), viper.GetString(config.CoreOSChannel)))
			viper.Set(config.CurrentCoreOSVersion, "0.0.0")
		}
	}

	// Fetch the streams JSON once into memory. This both sets
	// RemoteCoreOSVersion and returns the raw body for checksum extraction.
	body := LoadRemoteCoreOSVersion()
	remoteVersion := viper.GetString(config.RemoteCoreOSVersion)
	if body == nil || remoteVersion == "" {
		slog.Warn("Could not determine remote CoreOS version, skipping update")
		return
	}

	oldVersion := viper.GetString(config.CurrentCoreOSVersion)
	if remoteVersion == oldVersion {
		return
	}

	viper.Set(config.Updating, true)
	defer viper.Set(config.Updating, false)
	slog.Info("Remote coreos version differs from local", "remote", remoteVersion, "local", oldVersion)

	// Only advance the current version once every artifact has been
	// downloaded successfully, so a half-published release never gets served.
	if err := downloadCoreOSArtifacts(body, remoteVersion); err != nil {
		slog.Error("CoreOS artifact download failed, not advancing version", "target", remoteVersion, "error", err)
		return
	}

	// Write the already-fetched streams JSON to disk (no second request).
	if err := saveCoreOSJSON(body); err != nil {
		slog.Error("Error saving coreos json", "error", err)
	}

	viper.Set(config.CurrentCoreOSVersion, remoteVersion)
	slog.Info("CoreOS updated", "version", remoteVersion)

	removeOldCoreOSArtifacts(oldVersion)
}

// downloadCoreOSArtifacts fetches the PXE initramfs, kernel and rootfs for
// version, verifying SHA256 checksums from the streams JSON when available.
func downloadCoreOSArtifacts(body []byte, version string) error {
	arch := viper.GetString(config.CoreOSArchitecture)
	artifacts := map[string]string{
		"initramfs": fmt.Sprintf("fedora-coreos-%s-live-initramfs.%s.img", version, arch),
		"kernel":    fmt.Sprintf("fedora-coreos-%s-live-kernel-%s", version, arch),
		"rootfs":    fmt.Sprintf("fedora-coreos-%s-live-rootfs.%s.img", version, arch),
	}

	for _, artifactType := range []string{"initramfs", "kernel", "rootfs"} {
		file := artifacts[artifactType]
		url := fmt.Sprintf(RemoteCoreOSURL()+"/%s", file)
		sha, err := extractCoreOSChecksum(body, artifactType)
		if err != nil {
			slog.Warn("Could not extract checksum, falling back to unverified download", "artifact", artifactType, "error", err)
			if err := config.DownloadFile(url); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			continue
		}
		if err := config.DownloadFileWithChecksum(url, sha); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	return nil
}

// removeOldCoreOSArtifacts deletes the PXE files of a superseded release from
// DataDir. Missing files are not an error.
func removeOldCoreOSArtifacts(oldVersion string) {
	if oldVersion == "" || oldVersion == "0.0.0" {
		return
	}
	arch := viper.GetString(config.CoreOSArchitecture)
	dataDir := viper.GetString(config.DataDir)
	for _, file := range []string{
		fmt.Sprintf("fedora-coreos-%s-live-initramfs.%s.img", oldVersion, arch),
		fmt.Sprintf("fedora-coreos-%s-live-kernel-%s", oldVersion, arch),
		fmt.Sprintf("fedora-coreos-%s-live-rootfs.%s.img", oldVersion, arch),
	} {
		path := filepath.Join(dataDir, file)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("Could not remove old CoreOS artifact", "path", path, "error", err)
		}
	}
}

// LoadRemoteCoreOSVersion fetches the Fedora CoreOS streams JSON, sets the
// RemoteCoreOSVersion in viper, and returns the raw JSON body so callers can
// extract additional fields (such as SHA256 checksums) without a second request.
func LoadRemoteCoreOSVersion() []byte {
	resp, err := http.Get(RemoteCoreOSJSONURL())
	if err != nil {
		slog.Error("Error retrieving remote coreos version", "url", RemoteCoreOSURL(), "error", err)
		return nil
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("Error reading response body", "error", err)
		return nil
	}

	remoteVersion, err := jsonparser.GetString(b, "architectures", viper.GetString(config.CoreOSArchitecture), "artifacts", "metal", "release")
	if err != nil {
		slog.Error("Error retrieving remote coreos version", "url", resp.Request.URL.String())
		slog.Error("JSON parse error", "error", err)
		return nil
	}
	viper.Set(config.RemoteCoreOSVersion, remoteVersion)
	slog.Debug("Remote coreos version found", "version", remoteVersion)
	return b
}

// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/39.20231101.3.0/x86_64/fedora-coreos-39.20231101.3.0-live-kernel-x86_64
// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/0.0.0/x86_64//fedora-coreos-39.20231101.3.0-live-kernel-x86_64
func RemoteCoreOSURL() string {
	return fmt.Sprintf(viper.GetString(config.CoreOSURL), viper.GetString(config.CoreOSChannel), viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
}

func DownloadCoreOSFile(filename string) error {
	return config.DownloadFile(fmt.Sprintf(RemoteCoreOSURL()+"/%s", filename))
}

func RemoteCoreOSJSONURL() string {
	return fmt.Sprintf("https://builds.coreos.fedoraproject.org/streams/%s.json", viper.GetString(config.CoreOSChannel))
}

func DownloadCoreOSJSON() error {
	return config.DownloadFile(RemoteCoreOSJSONURL())
}

// saveCoreOSJSON writes the pre-fetched streams JSON body to
// <DataDir>/<channel>.json, avoiding a redundant HTTP request.
func saveCoreOSJSON(body []byte) error {
	if body == nil {
		return fmt.Errorf("no JSON body to save")
	}
	filename := fmt.Sprintf("%s/%s.json", viper.GetString(config.DataDir), viper.GetString(config.CoreOSChannel))
	return os.WriteFile(filename, body, 0644)
}

// extractCoreOSChecksum extracts the SHA256 checksum for a given PXE artifact
// type ("kernel", "initramfs", or "rootfs") from the Fedora CoreOS streams
// JSON body. Returns the lowercase hex checksum string or an error if the
// field is missing.
func extractCoreOSChecksum(body []byte, artifactType string) (string, error) {
	if body == nil {
		return "", fmt.Errorf("no JSON body to extract checksum from")
	}
	arch := viper.GetString(config.CoreOSArchitecture)
	sha, err := jsonparser.GetString(body, "architectures", arch, "artifacts", "metal", "formats", "pxe", artifactType, "sha256")
	if err != nil {
		return "", fmt.Errorf("SHA256 checksum not found for %s: %w", artifactType, err)
	}
	return sha, nil
}
