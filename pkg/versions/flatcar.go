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
	if viper.GetBool("debug") {
		slog.Info("Checking remote flatcar version")
	}

	if viper.GetString(config.CurrentFlatcarVersion) == "" {
		// Check for an existing version.txt file
		if oldVer, err := os.Open(fmt.Sprintf("%s/version.txt", viper.GetString(config.DataDir))); err == nil {
			slog.Info("Found old version.txt, setting current version to that")
			data, _ := godotenv.Parse(oldVer)
			if _, ok := data["FLATCAR_VERSION"]; !ok {
				slog.Warn("Old version.txt file is invalid")
			}
			slog.Info("Flatcar version set", "version", data["FLATCAR_VERSION"])
			viper.Set(config.CurrentFlatcarVersion, data["FLATCAR_VERSION"])
		} else {
			slog.Info("version.txt not found, setting current version to 0.0.0", "path", fmt.Sprintf("%s/version.txt", viper.GetString(config.DataDir)))
			viper.Set(config.CurrentFlatcarVersion, "0.0.0")
		}
	}

	LoadRemoteFlatcarVersion()
	if viper.GetString(config.RemoteFlatcarVersion) != viper.GetString(config.CurrentFlatcarVersion) {
		viper.Set(config.Updating, true)
		slog.Info("Remote flatcar version differs from local", "remote", viper.GetString(config.RemoteFlatcarVersion), "local", viper.GetString(config.CurrentFlatcarVersion))

		// version.txt is small metadata; download without checksum verification.
		if err := DownloadFlatcarFile("version.txt"); err != nil {
			slog.Error("Error downloading version.txt", "error", err)
		}

		// Download PXE initrd with SHA512 checksum verification.
		pxeInitrd := "flatcar_production_pxe_image.cpio.gz"
		if sum, err := LoadFlatcarDigest(pxeInitrd); err == nil {
			if err := config.DownloadFileWithChecksumSHA512(fmt.Sprintf(RemoteFlatcarURL()+"/%s", pxeInitrd), sum); err != nil {
				slog.Error("Error downloading file with checksum", "file", pxeInitrd, "error", err)
			}
		} else {
			slog.Warn("Could not load digest, falling back to unverified download", "file", pxeInitrd, "error", err)
			if err := DownloadFlatcarFile(pxeInitrd); err != nil {
				slog.Error("Error downloading file", "file", pxeInitrd, "error", err)
			}
		}

		// Download PXE kernel with SHA512 checksum verification.
		pxeKernel := "flatcar_production_pxe.vmlinuz"
		if sum, err := LoadFlatcarDigest(pxeKernel); err == nil {
			if err := config.DownloadFileWithChecksumSHA512(fmt.Sprintf(RemoteFlatcarURL()+"/%s", pxeKernel), sum); err != nil {
				slog.Error("Error downloading file with checksum", "file", pxeKernel, "error", err)
			}
		} else {
			slog.Warn("Could not load digest, falling back to unverified download", "file", pxeKernel, "error", err)
			if err := DownloadFlatcarFile(pxeKernel); err != nil {
				slog.Error("Error downloading file", "file", pxeKernel, "error", err)
			}
		}

		viper.Set(config.CurrentFlatcarVersion, viper.GetString(config.RemoteFlatcarVersion))
		viper.Set(config.Updating, false)
	}

}

func LoadRemoteFlatcarVersion() {
	if resp, err := http.Get(RemoteFlatcarURL() + "/version.txt"); err == nil {
		data, _ := godotenv.Parse(resp.Body)
		if _, ok := data["FLATCAR_VERSION"]; !ok {
			slog.Error("Error retrieving remote flatcar version", "url", resp.Request.URL.String())
			return
		}
		viper.Set(config.RemoteFlatcarVersion, data["FLATCAR_VERSION"])
		if viper.GetBool("debug") {
			slog.Info("Remote flatcar version found", "version", data["FLATCAR_VERSION"])
		}
	} else {
		slog.Error("Error retrieving remote flatcar version", "url", RemoteFlatcarURL(), "error", err)
	}
}

func RemoteFlatcarURL() string {
	return fmt.Sprintf(viper.GetString(config.FlatcarURL), viper.GetString(config.FlatcarChannel), viper.GetString(config.FlatcarArchitecture))
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
