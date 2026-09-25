package versions

import (
	"bufio"
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

const (
	bluefinTagPrefix     = "installer-v"
	bluefinVmlinuzPrefix = "bluefin-server-pxe-vmlinuz-"
	bluefinInitrdPrefix  = "bluefin-server-pxe-initrd-"
	bluefinInitrdSuffix  = ".cpio.gz"
	bluefinDDIPrefix     = "bluefin-server-ddi-"
	bluefinDDISuffix     = ".raw.zst"
	bluefinSumsFile      = "SHA256SUMS"
	bluefinReleasePages  = 20
)

// Overridable in tests to point at an httptest server.
var (
	githubAPIBase      = "https://api.github.com"
	githubDownloadBase = "https://github.com"
)

// BluefinManifest is DataDir/bluefin/<version>/manifest.json: which files of
// the release Booty serves and the DDI hash the installer must be given.
type BluefinManifest struct {
	Version   string `json:"version"`
	Vmlinuz   string `json:"vmlinuz"`
	Initrd    string `json:"initrd"`
	DDI       string `json:"ddi"`
	DDISha256 string `json:"ddiSha256"`
}

// Files lists the artifacts the manifest references.
func (m BluefinManifest) Files() []string {
	return []string{m.Vmlinuz, m.Initrd, m.DDI}
}

type bluefinRelease struct {
	Tag        string
	Version    string
	Prerelease bool
	Draft      bool
	Assets     []string
}

func (r bluefinRelease) hasPXEAssets() bool {
	var vmlinuz, initrd, sums bool
	for _, a := range r.Assets {
		switch {
		case strings.HasPrefix(a, bluefinVmlinuzPrefix):
			vmlinuz = true
		case strings.HasPrefix(a, bluefinInitrdPrefix):
			initrd = true
		case a == bluefinSumsFile:
			sums = true
		}
	}
	return vmlinuz && initrd && sums
}

// parseBluefinReleases decodes the GitHub releases list into the fields
// selection needs.
func parseBluefinReleases(body []byte) ([]bluefinRelease, error) {
	var raw []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("releases JSON: %w", err)
	}
	out := make([]bluefinRelease, 0, len(raw))
	for _, r := range raw {
		rel := bluefinRelease{Tag: r.TagName, Prerelease: r.Prerelease, Draft: r.Draft}
		rel.Version = strings.TrimPrefix(r.TagName, bluefinTagPrefix)
		for _, a := range r.Assets {
			rel.Assets = append(rel.Assets, a.Name)
		}
		out = append(out, rel)
	}
	return out, nil
}

// selectBluefinRelease picks the newest (by version) published installer-v*
// release that carries PXE assets. Other tag families (bluefin-server-v*),
// drafts and prereleases are ignored.
func selectBluefinRelease(releases []bluefinRelease) (bluefinRelease, error) {
	var best *bluefinRelease
	for i := range releases {
		r := &releases[i]
		if !strings.HasPrefix(r.Tag, bluefinTagPrefix) || r.Draft || r.Prerelease || !r.hasPXEAssets() {
			continue
		}
		if best == nil || compareVersions(r.Version, best.Version) > 0 {
			best = r
		}
	}
	if best == nil {
		return bluefinRelease{}, errors.New("no installer-v* release with PXE kernel, initrd and SHA256SUMS assets")
	}
	return *best, nil
}

// compareVersions orders dotted numeric versions (26.08.0 > 25.08.15);
// non-numeric components fall back to string order.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		xi, xerr := strconv.Atoi(x)
		yi, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil && xi != yi:
			if xi < yi {
				return -1
			}
			return 1
		case (xerr != nil || yerr != nil) && x != y:
			return strings.Compare(x, y)
		}
	}
	return 0
}

// sha256Sums is a SHA256SUMS file: hashes by file name plus the file order,
// which the DDI tie-break relies on.
type sha256Sums struct {
	hashes map[string]string
	order  []string
}

// parseSHA256SUMS reads "<hex> [*]<name>" lines; the asterisk is the
// coreutils binary-mode marker.
func parseSHA256SUMS(r io.Reader) (sha256Sums, error) {
	sums := sha256Sums{hashes: map[string]string{}}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		hash := strings.ToLower(fields[0])
		if len(hash) != 64 {
			return sums, fmt.Errorf("SHA256SUMS: %q is not a sha256", fields[0])
		}
		name := strings.TrimPrefix(strings.Join(fields[1:], " "), "*")
		if _, dup := sums.hashes[name]; !dup {
			sums.order = append(sums.order, name)
		}
		sums.hashes[name] = hash
	}
	if err := sc.Err(); err != nil {
		return sums, err
	}
	if len(sums.order) == 0 {
		return sums, errors.New("SHA256SUMS is empty")
	}
	return sums, nil
}

// selectBluefinArtifacts resolves the kernel, initrd and DDI for version
// from SHA256SUMS. Every file must be listed: the installer refuses a DDI
// without inst.ddi_sha256, and Booty does not serve unverified images. When
// several DDIs are listed the one matching version wins, otherwise the last
// listed (26.08.0 ships a legacy FSDK image outside SHA256SUMS and the
// Flatcar-LTS one inside it).
func selectBluefinArtifacts(version string, sums sha256Sums) (BluefinManifest, error) {
	m := BluefinManifest{
		Version: version,
		Vmlinuz: bluefinVmlinuzPrefix + version,
		Initrd:  bluefinInitrdPrefix + version + bluefinInitrdSuffix,
	}
	for _, f := range []string{m.Vmlinuz, m.Initrd} {
		if _, ok := sums.hashes[f]; !ok {
			return m, fmt.Errorf("%s not listed in SHA256SUMS", f)
		}
	}
	var ddis []string
	for _, name := range sums.order {
		if strings.HasPrefix(name, bluefinDDIPrefix) && strings.HasSuffix(name, bluefinDDISuffix) {
			ddis = append(ddis, name)
		}
	}
	if len(ddis) == 0 {
		return m, errors.New("no bluefin-server-ddi-*.raw.zst listed in SHA256SUMS")
	}
	m.DDI = ddis[len(ddis)-1]
	for _, d := range ddis {
		if d == bluefinDDIPrefix+version+bluefinDDISuffix {
			m.DDI = d
		}
	}
	m.DDISha256 = sums.hashes[m.DDI]
	return m, nil
}

// BluefinVersionCheck brings the served Bluefin Server release in line with
// --bluefinVersion or the newest installer release. Concurrent invocations
// are skipped and the version only advances once every file is on disk.
func BluefinVersionCheck() {
	if !state.BluefinUpdateMu.TryLock() {
		slog.Info("Bluefin update already in progress, skipping version check")
		return
	}
	defer state.BluefinUpdateMu.Unlock()
	ctx := context.Background()
	slog.Debug("Checking Bluefin version")

	current := state.CurrentBluefinVersion()
	if current == "" {
		current = state.LoadLocalBluefinVersion()
		if current == "" {
			slog.Info("No local Bluefin version found, starting from 0.0.0")
			current = unsetVersion
		}
		state.SetCurrentBluefinVersion(current)
	}

	target := state.BluefinPin()
	if target != "" {
		slog.Debug("Bluefin version is pinned", "version", target)
	} else {
		remote, err := LoadRemoteBluefinVersion(ctx)
		if err != nil {
			slog.Error("Could not determine remote Bluefin version, skipping update", "error", err)
			return
		}
		target = remote
	}

	if target == current {
		return
	}
	slog.Info("Target Bluefin version differs from local", "target", target, "local", current)
	manifest, err := installBluefinRelease(ctx, target)
	if err != nil {
		slog.Error("Bluefin release install failed, not advancing version", "target", target, "error", err)
		return
	}
	state.SetCurrentBluefinVersion(target)
	slog.Info("Bluefin updated", "version", target, "vmlinuz", manifest.Vmlinuz, "initrd", manifest.Initrd, "ddi", manifest.DDI)
}

func bluefinRepo() string {
	return strings.Trim(viper.GetString(config.BluefinRepo), "/")
}

func bluefinReleasesURL() string {
	return fmt.Sprintf("%s/repos/%s/releases?per_page=%d", githubAPIBase, bluefinRepo(), bluefinReleasePages)
}

func bluefinAssetURL(version, asset string) string {
	return fmt.Sprintf("%s/%s/releases/download/%s%s/%s", githubDownloadBase, bluefinRepo(), bluefinTagPrefix, version, asset)
}

func githubGet(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := strings.TrimSpace(viper.GetString(config.GithubToken)); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := config.MetadataClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	defer config.CloseQuietly(resp.Body, url)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return body, nil
}

// LoadRemoteBluefinVersion asks the GitHub releases API for the newest
// installer release and records it in state.
func LoadRemoteBluefinVersion(ctx context.Context) (string, error) {
	url := bluefinReleasesURL()
	body, err := githubGet(ctx, url, 8<<20)
	if err != nil {
		return "", err
	}
	releases, err := parseBluefinReleases(body)
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	rel, err := selectBluefinRelease(releases)
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	state.SetRemoteBluefinVersion(rel.Version)
	slog.Debug("Remote Bluefin version found", "version", rel.Version, "tag", rel.Tag)
	return rel.Version, nil
}

// installBluefinRelease downloads SHA256SUMS, the PXE kernel, initrd and the
// selected DDI for version into DataDir/bluefin/<version>/, writes the
// manifest, repoints bluefin/current and prunes other release directories.
func installBluefinRelease(ctx context.Context, version string) (BluefinManifest, error) {
	sumsBody, err := githubGet(ctx, bluefinAssetURL(version, bluefinSumsFile), 1<<20)
	if err != nil {
		return BluefinManifest{}, fmt.Errorf("SHA256SUMS: %w", err)
	}
	sums, err := parseSHA256SUMS(strings.NewReader(string(sumsBody)))
	if err != nil {
		return BluefinManifest{}, err
	}
	manifest, err := selectBluefinArtifacts(version, sums)
	if err != nil {
		return BluefinManifest{}, err
	}
	slog.Info("Selected Bluefin artifacts", "version", version, "vmlinuz", manifest.Vmlinuz, "initrd", manifest.Initrd, "ddi", manifest.DDI, "ddiSha256", manifest.DDISha256)

	dir := config.DataPath(config.BluefinDir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return manifest, err
	}
	for _, file := range manifest.Files() {
		dest := filepath.Join(dir, file)
		hash := sums.hashes[file]
		if config.FileHashMatches(dest, crypto.SHA256, hash) {
			slog.Info("Artifact already present and verified", "path", dest)
			continue
		}
		if err := config.Download(ctx, config.DownloadClient, bluefinAssetURL(version, file), dest, crypto.SHA256, hash); err != nil {
			return manifest, fmt.Errorf("%s: %w", file, err)
		}
	}

	if err := config.WriteFileAtomic(filepath.Join(dir, bluefinSumsFile), sumsBody, 0o644); err != nil {
		return manifest, fmt.Errorf("SHA256SUMS: %w", err)
	}
	if err := writeBluefinManifest(dir, manifest); err != nil {
		return manifest, err
	}
	if err := config.ReplaceSymlink(version, config.DataPath(config.BluefinDir, config.BluefinCurrentLink)); err != nil {
		return manifest, fmt.Errorf("linking current: %w", err)
	}
	pruneBluefinReleases(version)
	return manifest, nil
}

func writeBluefinManifest(dir string, m BluefinManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(filepath.Join(dir, config.BluefinManifestFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("manifest.json: %w", err)
	}
	return nil
}

// LoadBluefinManifest reads manifest.json from dir.
func LoadBluefinManifest(dir string) (BluefinManifest, error) {
	var m BluefinManifest
	data, err := os.ReadFile(filepath.Join(dir, config.BluefinManifestFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("%s: %w", config.BluefinManifestFile, err)
	}
	if m.Version == "" || m.Vmlinuz == "" || m.Initrd == "" || m.DDI == "" || m.DDISha256 == "" {
		return m, fmt.Errorf("%s: incomplete", config.BluefinManifestFile)
	}
	return m, nil
}

// CurrentBluefinManifest returns the manifest of the served release, or
// ok=false when no release has been installed yet.
func CurrentBluefinManifest() (BluefinManifest, bool) {
	m, err := LoadBluefinManifest(config.DataPath(config.BluefinDir, config.BluefinCurrentLink))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("Bluefin manifest unreadable", "error", err)
		}
		return BluefinManifest{}, false
	}
	return m, true
}

func pruneBluefinReleases(keep string) {
	root := config.DataPath(config.BluefinDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		slog.Warn("Could not list Bluefin release directories", "path", root, "error", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("Could not remove old Bluefin release", "path", path, "error", err)
			continue
		}
		slog.Info("Removed old Bluefin release", "path", path)
	}
}

// MissingBluefinArtifacts lists the files named by bluefin/current/manifest.json
// that are absent from dataDir. It is nil when there is no manifest.
func MissingBluefinArtifacts(dataDir string) []string {
	dir := filepath.Join(dataDir, config.BluefinDir, config.BluefinCurrentLink)
	m, err := LoadBluefinManifest(dir)
	if err != nil {
		return nil
	}
	return missingArtifacts(dir, m.Files())
}
