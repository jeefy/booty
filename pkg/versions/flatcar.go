package versions

import (
	"bufio"
	"context"
	"crypto"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

const (
	flatcarKernel = "flatcar_production_pxe.vmlinuz"
	flatcarInitrd = "flatcar_production_pxe_image.cpio.gz"
	flatcarDir    = "flatcar"
)

var flatcarArtifacts = []string{flatcarInitrd, flatcarKernel}

// FlatcarVersionCheck brings the served Flatcar release in line with the pin
// (if set) or the channel's latest version. Concurrent invocations are
// skipped.
func FlatcarVersionCheck() {
	runReleaseSync(releaseSync{
		os:      OSFlatcar,
		label:   "Flatcar",
		tracked: config.FlatcarTracked,
		tryLock: state.TryLockFlatcarUpdate,
		local: func() string {
			current := state.CurrentFlatcarVersion()
			if current == "" {
				current = state.LoadLocalFlatcarVersion()
				if current == "" {
					slog.Info("No local Flatcar version found, starting from 0.0.0")
					current = "0.0.0"
				}
				state.SetCurrentFlatcarVersion(current)
			}
			return current
		},
		target: func(ctx context.Context) (string, error) {
			if pin := state.FlatcarPin(); pin != "" {
				slog.Debug("Flatcar version is pinned", "version", pin)
				return pin, nil
			}
			return LoadRemoteFlatcarVersion(ctx)
		},
		install: func(ctx context.Context, target, _ string) error {
			return installFlatcarRelease(ctx, target)
		},
		setCurrent: state.SetCurrentFlatcarVersion,
	})
}

func flatcarReleaseURL(release string) string {
	return fmt.Sprintf(viper.GetString(config.FlatcarURL),
		viper.GetString(config.FlatcarChannel),
		viper.GetString(config.FlatcarArchitecture),
		release)
}

func fetchVersionTxt(ctx context.Context, url string) (map[string]string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := config.MetadataClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer config.CloseQuietly(resp.Body, url)
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, nil, err
	}
	data, err := godotenv.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", url, err)
	}
	if data["FLATCAR_VERSION"] == "" {
		return nil, nil, fmt.Errorf("%s: FLATCAR_VERSION missing", url)
	}
	return data, body, nil
}

// LoadRemoteFlatcarVersion fetches the channel's current version.txt and
// records the version in state.
func LoadRemoteFlatcarVersion(ctx context.Context) (string, error) {
	data, _, err := fetchVersionTxt(ctx, flatcarReleaseURL("current")+"/version.txt")
	if err != nil {
		return "", err
	}
	v := data["FLATCAR_VERSION"]
	state.SetRemoteFlatcarVersion(v)
	slog.Debug("Remote Flatcar version found", "version", v)
	return v, nil
}

// installFlatcarRelease downloads the PXE kernel and initrd for version into
// DataDir/flatcar/<version>/, then atomically repoints flatcar/current (and
// the compatibility symlinks at the top of DataDir), writes version.txt
// (the caller prunes the release directories nothing refers to any more).
func installFlatcarRelease(ctx context.Context, version string) error {
	base := flatcarReleaseURL(version)
	dir := ReleaseDir(OSFlatcar, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	_, versionTxt, err := fetchVersionTxt(ctx, base+"/version.txt")
	if err != nil {
		return fmt.Errorf("version.txt: %w", err)
	}

	for _, artifact := range flatcarArtifacts {
		dest := filepath.Join(dir, artifact)
		digest, err := retryMetadata(ctx, artifact+".DIGESTS", func(ctx context.Context) (string, error) {
			return LoadFlatcarDigest(ctx, base, artifact)
		})
		if err != nil {
			return fmt.Errorf("%s: refusing to install without its SHA512 digest: %w", artifact, err)
		}
		if config.FileHashMatches(dest, crypto.SHA512, digest) {
			slog.Info("Artifact already present and verified", "path", dest)
			continue
		}
		if err := config.Download(ctx, config.DownloadClient, base+"/"+artifact, dest, crypto.SHA512, digest); err != nil {
			return fmt.Errorf("%s: %w", artifact, err)
		}
	}

	if err := config.WriteFileAtomic(filepath.Join(dir, "version.txt"), versionTxt, 0o644); err != nil {
		return fmt.Errorf("version.txt: %w", err)
	}
	if err := linkFlatcarRelease(version); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(config.DataPath("version.txt"), versionTxt, 0o644); err != nil {
		return fmt.Errorf("version.txt: %w", err)
	}
	return nil
}

// linkFlatcarRelease makes version the current Flatcar release and keeps
// the pre-release-directory names at the top of DataDir
// (flatcar_production_pxe.vmlinuz, ...) as symlinks into flatcar/current,
// so DHCP setups and iPXE scripts that still use them keep working.
func linkFlatcarRelease(version string) error {
	if err := linkRelease(OSFlatcar, version); err != nil {
		return err
	}
	for _, artifact := range flatcarArtifacts {
		target := filepath.Join(flatcarDir, CurrentLink, artifact)
		if err := config.ReplaceSymlink(target, config.DataPath(artifact)); err != nil {
			return fmt.Errorf("linking %s: %w", artifact, err)
		}
	}
	return nil
}

// FlatcarDataDir is the /data/ path prefix ("flatcar/<version>/") of the
// release a host boots, "" while no release is cached (the compatibility
// names at the top of DataDir then).
func FlatcarDataDir(version string) string {
	if version == "" {
		return ""
	}
	return flatcarDir + "/" + version + "/"
}

// LoadFlatcarDigest fetches <base>/<filename>.DIGESTS and returns the
// lowercase hex SHA512 checksum it lists.
func LoadFlatcarDigest(ctx context.Context, base, filename string) (string, error) {
	digestURL := fmt.Sprintf("%s/%s.DIGESTS", base, filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, digestURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := config.MetadataClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch digest file %s: %w", digestURL, err)
	}
	defer config.CloseQuietly(resp.Body, digestURL)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("digest file returned HTTP %d: %s", resp.StatusCode, digestURL)
	}
	sum, err := parseFlatcarDigests(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%s: %w", digestURL, err)
	}
	return sum, nil
}

// parseFlatcarDigests extracts the SHA512 hash from a Flatcar .DIGESTS file,
// whose sections are headed by "# <ALGO> HASH" followed by "<hash>  <file>".
func parseFlatcarDigests(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	inSHA512 := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "# SHA512 HASH":
			inSHA512 = true
		case strings.HasPrefix(line, "#"):
			inSHA512 = false
		case inSHA512 && line != "":
			if fields := strings.Fields(line); len(fields) >= 1 {
				return strings.ToLower(fields[0]), nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading digest file: %w", err)
	}
	return "", fmt.Errorf("no SHA512 hash found in digest file")
}
