package versions

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

func StartFlatcarCron() {
	slog.Info("Starting CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).SingletonMode().Do(FlatcarVersionCheck)
	if err != nil {
		slog.Error("Error creating prune cronjob", "error", err)
		os.Exit(1)
	}
	cron.StartAsync()
}

func FlatcarVersionCheck() {
	if viper.GetBool(config.Updating) {
		slog.Info("Already updating, skipping version check")
		return
	}
	slog.Debug("Checking remote flatcar version")

	if viper.GetString(config.CurrentFlatcarVersion) == "" {
		// Check for an existing version.txt file
		versionFile := fmt.Sprintf("%s/version.txt", viper.GetString(config.DataDir))
		if oldVer, err := os.Open(versionFile); err == nil {
			data, _ := godotenv.Parse(oldVer)
			oldVer.Close()
			if v, ok := data["FLATCAR_VERSION"]; ok && v != "" {
				slog.Info("Found old version.txt, setting current version to that", "version", v)
				viper.Set(config.CurrentFlatcarVersion, v)
			} else {
				slog.Warn("Old version.txt file is invalid, setting current version to 0.0.0", "path", versionFile)
				viper.Set(config.CurrentFlatcarVersion, "0.0.0")
			}
		} else {
			slog.Info("version.txt not found, setting current version to 0.0.0", "path", versionFile)
			viper.Set(config.CurrentFlatcarVersion, "0.0.0")
		}
	}

	// Determine the version we want to be running. When a version is pinned,
	// that is always the target. Otherwise track the channel's latest version.
	var targetVersion string
	if pinned := viper.GetString(config.FlatcarVersion); pinned != "" {
		targetVersion = pinned
		slog.Debug("Flatcar version is pinned", "version", pinned)
	} else {
		LoadRemoteFlatcarVersion()
		targetVersion = viper.GetString(config.RemoteFlatcarVersion)
	}

	if targetVersion == "" {
		slog.Warn("Could not determine target Flatcar version, skipping update")
		return
	}

	if targetVersion != viper.GetString(config.CurrentFlatcarVersion) {
		viper.Set(config.Updating, true)
		defer viper.Set(config.Updating, false)
		slog.Info("Target flatcar version differs from local", "target", targetVersion, "local", viper.GetString(config.CurrentFlatcarVersion))

		// Only advance the current version once all artifacts have been
		// downloaded successfully. If any download fails (e.g. a 404 because
		// the remote hasn't published every artifact yet), keep the existing
		// version so we don't serve a half-updated release.
		if err := downloadFlatcarArtifacts(); err != nil {
			slog.Error("Flatcar artifact download failed, not advancing version", "target", targetVersion, "error", err)
			return
		}

		viper.Set(config.CurrentFlatcarVersion, targetVersion)
		slog.Info("Flatcar updated", "version", targetVersion)
	}
}

// downloadFlatcarArtifacts downloads version.txt and the PXE kernel/initrd for
// the currently targeted Flatcar release. It returns an error if any artifact
// fails to download (including HTTP 404s), so callers can avoid advancing the
// recorded version when a release is incomplete or unavailable.
func downloadFlatcarArtifacts() error {
	// version.txt is small metadata; download without checksum verification.
	if err := DownloadFlatcarFile("version.txt"); err != nil {
		return fmt.Errorf("version.txt: %w", err)
	}

	artifacts := []string{
		"flatcar_production_pxe_image.cpio.gz", // initrd
		"flatcar_production_pxe.vmlinuz",       // kernel
	}

	for _, artifact := range artifacts {
		if sum, err := LoadFlatcarDigest(artifact); err == nil {
			if err := config.DownloadFileWithChecksumSHA512(fmt.Sprintf(RemoteFlatcarURL()+"/%s", artifact), sum); err != nil {
				return fmt.Errorf("%s: %w", artifact, err)
			}
		} else {
			slog.Warn("Could not load digest, falling back to unverified download", "file", artifact, "error", err)
			if err := DownloadFlatcarFile(artifact); err != nil {
				return fmt.Errorf("%s: %w", artifact, err)
			}
		}
	}

	return nil
}

func LoadRemoteFlatcarVersion() {
	if resp, err := http.Get(RemoteFlatcarURL() + "/version.txt"); err == nil {
		data, _ := godotenv.Parse(resp.Body)
		if _, ok := data["FLATCAR_VERSION"]; !ok {
			slog.Error("Error retrieving remote flatcar version", "url", resp.Request.URL.String())
			return
		}
		viper.Set(config.RemoteFlatcarVersion, data["FLATCAR_VERSION"])
		slog.Debug("Remote flatcar version found", "version", data["FLATCAR_VERSION"])
	} else {
		slog.Error("Error retrieving remote flatcar version", "url", RemoteFlatcarURL(), "error", err)
	}
}

// RemoteFlatcarURL builds the remote release directory URL for Flatcar. When a
// specific version is pinned via config.FlatcarVersion, the URL points at that
// version's directory; otherwise it tracks the channel's "current" release.
func RemoteFlatcarURL() string {
	releaseDir := "current"
	if pinned := viper.GetString(config.FlatcarVersion); pinned != "" {
		releaseDir = pinned
	}
	return fmt.Sprintf(viper.GetString(config.FlatcarURL),
		viper.GetString(config.FlatcarChannel),
		viper.GetString(config.FlatcarArchitecture),
		releaseDir)
}

func DownloadFlatcarFile(filename string) error {
	return config.DownloadFile(fmt.Sprintf(RemoteFlatcarURL()+"/%s", filename))
}

// LoadFlatcarDigest downloads the .DIGESTS file for the given artifact from
// the remote Flatcar release URL, parses it, and returns the lowercase hex
// SHA512 checksum. The DIGESTS file format contains sections headed by
// "# <ALGO> HASH" followed by lines of "<hash>  <filename>".
// Returns an error if the DIGESTS file cannot be fetched or does not contain
// a SHA512 entry.
func LoadFlatcarDigest(filename string) (string, error) {
	digestURL := fmt.Sprintf("%s/%s.DIGESTS", RemoteFlatcarURL(), filename)
	resp, err := http.Get(digestURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch digest file %s: %w", digestURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("digest file returned HTTP %d: %s", resp.StatusCode, digestURL)
	}

	scanner := bufio.NewScanner(resp.Body)
	inSHA512Section := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "# SHA512 HASH" {
			inSHA512Section = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			inSHA512Section = false
			continue
		}
		if inSHA512Section && line != "" {
			// Line format: "<hash>  <filename>"
			fields := strings.Fields(line)
			if len(fields) >= 1 {
				return strings.ToLower(fields[0]), nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading digest file %s: %w", digestURL, err)
	}

	return "", fmt.Errorf("no SHA512 hash found in digest file %s", digestURL)
}
