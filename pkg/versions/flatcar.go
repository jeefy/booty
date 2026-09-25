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
	if !state.FlatcarUpdateMu.TryLock() {
		slog.Info("Flatcar update already in progress, skipping version check")
		return
	}
	defer state.FlatcarUpdateMu.Unlock()
	ctx := context.Background()
	slog.Debug("Checking Flatcar version")

	current := state.CurrentFlatcarVersion()
	if current == "" {
		current = state.LoadLocalFlatcarVersion()
		if current == "" {
			slog.Info("No local Flatcar version found, starting from 0.0.0")
			current = "0.0.0"
		}
		state.SetCurrentFlatcarVersion(current)
	}

	target := state.FlatcarPin()
	if target != "" {
		slog.Debug("Flatcar version is pinned", "version", target)
	} else {
		remote, err := LoadRemoteFlatcarVersion(ctx)
		if err != nil {
			slog.Error("Could not determine remote Flatcar version, skipping update", "error", err)
			return
		}
		target = remote
	}

	if target == current {
		return
	}
	slog.Info("Target Flatcar version differs from local", "target", target, "local", current)
	if err := installFlatcarRelease(ctx, target); err != nil {
		slog.Error("Flatcar release install failed, not advancing version", "target", target, "error", err)
		return
	}
	state.SetCurrentFlatcarVersion(target)
	slog.Info("Flatcar updated", "version", target)
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
// DataDir/flatcar/<version>/, then atomically repoints the public symlinks,
// writes version.txt and prunes older release directories.
func installFlatcarRelease(ctx context.Context, version string) error {
	base := flatcarReleaseURL(version)
	dir := config.DataPath(flatcarDir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	_, versionTxt, err := fetchVersionTxt(ctx, base+"/version.txt")
	if err != nil {
		return fmt.Errorf("version.txt: %w", err)
	}

	for _, artifact := range flatcarArtifacts {
		dest := filepath.Join(dir, artifact)
		hashAlgo := crypto.Hash(0)
		digest, err := LoadFlatcarDigest(ctx, base, artifact)
		if err != nil {
			slog.Warn("Could not load digest, falling back to unverified download", "file", artifact, "error", err)
		} else {
			hashAlgo = crypto.SHA512
			if config.FileHashMatches(dest, hashAlgo, digest) {
				slog.Info("Artifact already present and verified", "path", dest)
				continue
			}
		}
		if err := config.Download(ctx, config.DownloadClient, base+"/"+artifact, dest, hashAlgo, digest); err != nil {
			return fmt.Errorf("%s: %w", artifact, err)
		}
	}

	for _, artifact := range flatcarArtifacts {
		target := filepath.Join(flatcarDir, version, artifact)
		if err := config.ReplaceSymlink(target, config.DataPath(artifact)); err != nil {
			return fmt.Errorf("linking %s: %w", artifact, err)
		}
	}

	if err := config.WriteFileAtomic(config.DataPath("version.txt"), versionTxt, 0o644); err != nil {
		return fmt.Errorf("version.txt: %w", err)
	}

	pruneFlatcarReleases(version)
	return nil
}

func pruneFlatcarReleases(keep string) {
	root := config.DataPath(flatcarDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		slog.Warn("Could not list Flatcar release directories", "path", root, "error", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("Could not remove old Flatcar release", "path", path, "error", err)
			continue
		}
		slog.Info("Removed old Flatcar release", "path", path)
	}
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
