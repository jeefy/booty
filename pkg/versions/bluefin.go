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
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

// Release file names, from projectbluefin/server's
// elements/oci/bluefin-server-image.bst.
const (
	bluefinTagPrefix     = "v"
	bluefinNetbootPrefix = "bluefin-server-netboot_"
	bluefinNetbootSuffix = ".efi"
	bluefinDDIPrefix     = "bluefin-server_"
	bluefinDDISuffix     = ".raw"
	bluefinZstSuffix     = ".zst"
	bluefinK0sPrefix     = "k0s-"
	bluefinNvidiaCTK     = "nvidia-container-toolkit-"
	bluefinReleasePages  = 20

	// BluefinSumsFile and BluefinSigFile are the release's checksum list and
	// its detached OpenPGP signature; the node's initrd fetches both next to
	// the netboot UKI and verifies the DDI with them.
	BluefinSumsFile = "SHA256SUMS"
	BluefinSigFile  = "SHA256SUMS.gpg"

	// Sysext names as they appear in BluefinManifest.Sysexts. NVIDIA driver
	// flavours (hardware.IsNvidiaDriverFlavour) appear under their own
	// name, nvidia-open-<branch>.
	BluefinSysextZFS                    = "zfs"
	BluefinSysextKubeStellar            = "kubestellar"
	BluefinSysextK0s                    = "k0s"
	BluefinSysextKubeadm                = "kubeadm"
	BluefinSysextNvidiaContainerToolkit = "nvidia-container-toolkit"
)

// Overridable in tests to point at an httptest server.
var (
	githubAPIBase      = "https://api.github.com"
	githubDownloadBase = "https://github.com"
)

// bluefinVersionPattern bounds what a version may look like: it ends up in
// file names, URLs and the tag (v<version>).
var bluefinVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z._+~-]{0,63}$`)

// ValidBluefinVersion reports whether v is usable as a Bluefin release
// version (20260927.123, 2026.09.0, 0.42).
func ValidBluefinVersion(v string) bool {
	return bluefinVersionPattern.MatchString(v) && !strings.Contains(v, "..")
}

// BluefinNetbootUKI, BluefinDDI and the sysext helpers name the release
// files of version.
func BluefinNetbootUKI(version string) string {
	return bluefinNetbootPrefix + version + bluefinNetbootSuffix
}

func BluefinDDI(version string) string {
	return bluefinDDIPrefix + version + bluefinDDISuffix
}

func bluefinVersionedSysext(name, version string) string {
	return name + "_" + version + bluefinDDISuffix + bluefinZstSuffix
}

// BluefinSysext is one decompressed sysext image in a release directory;
// Sha256 is the digest of the decompressed .raw.
type BluefinSysext struct {
	File   string `json:"file"`
	Sha256 string `json:"sha256"`
}

// BluefinManifest is DataDir/bluefin/<version>/manifest.json: the files of
// the release Booty serves. SHA256Sums holds the SHA256SUMS entries of every
// file Booty downloaded (the sysexts under their .raw.zst names).
type BluefinManifest struct {
	Version    string                   `json:"version"`
	NetbootUKI string                   `json:"netbootUKI"`
	DDI        string                   `json:"ddi"`
	Sysexts    map[string]BluefinSysext `json:"sysexts,omitempty"`
	SHA256Sums map[string]string        `json:"sha256sums"`
}

// Files lists the artifacts the manifest references.
func (m BluefinManifest) Files() []string {
	files := []string{m.NetbootUKI, m.DDI, BluefinSumsFile, BluefinSigFile}
	for _, s := range m.Sysexts {
		files = append(files, s.File)
	}
	return files
}

type bluefinRelease struct {
	Tag        string
	Version    string
	Prerelease bool
	Draft      bool
	Assets     []string
}

func (r bluefinRelease) hasRequiredAssets() bool {
	want := map[string]bool{BluefinNetbootUKI(r.Version): false, BluefinDDI(r.Version): false, BluefinSumsFile: false, BluefinSigFile: false}
	for _, a := range r.Assets {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	for _, found := range want {
		if !found {
			return false
		}
	}
	return true
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

// selectBluefinRelease picks the newest (by version) published v<version>
// release carrying the netboot UKI, the DDI, SHA256SUMS and its signature.
// Other tag families (installer-v*, bluefin-server-v*), drafts and
// prereleases are ignored.
func selectBluefinRelease(releases []bluefinRelease) (bluefinRelease, error) {
	var best *bluefinRelease
	for i := range releases {
		r := &releases[i]
		if !strings.HasPrefix(r.Tag, bluefinTagPrefix) || !ValidBluefinVersion(r.Version) || r.Draft || r.Prerelease || !r.hasRequiredAssets() {
			continue
		}
		if best == nil || compareVersions(r.Version, best.Version) > 0 {
			best = r
		}
	}
	if best == nil {
		return bluefinRelease{}, errors.New("no v<version> release with the netboot UKI, DDI, SHA256SUMS and SHA256SUMS.gpg assets")
	}
	return *best, nil
}

// compareVersions orders dotted numeric versions (26.08.0 > 25.08.15,
// 20260927.123 > 20260926.200); non-numeric components fall back to string
// order.
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

// sha256Sums is a SHA256SUMS file: hashes by file name plus the file order.
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

// bluefinSysextSource is an optional sysext as SHA256SUMS lists it.
type bluefinSysextSource struct {
	name   string
	asset  string
	sha256 string
}

// raw is the decompressed file name Booty stores and serves.
func (s bluefinSysextSource) raw() string { return strings.TrimSuffix(s.asset, bluefinZstSuffix) }

// selectBluefinArtifacts resolves the netboot UKI and DDI of version (both
// must be listed in SHA256SUMS: Booty does not serve unverified images)
// and the optional sysexts: zfs_<version>.raw.zst,
// kubestellar_<version>.raw.zst, kubeadm_<version>.raw.zst, every NVIDIA
// driver flavour nvidia-open-<branch>_<version>.raw.zst, and the single
// k0s-*.raw.zst and nvidia-container-toolkit-*.raw.zst (their own version
// axis).
func selectBluefinArtifacts(version string, sums sha256Sums) (BluefinManifest, []bluefinSysextSource, error) {
	m := BluefinManifest{
		Version:    version,
		NetbootUKI: BluefinNetbootUKI(version),
		DDI:        BluefinDDI(version),
		SHA256Sums: map[string]string{},
	}
	for _, f := range []string{m.NetbootUKI, m.DDI} {
		hash, ok := sums.hashes[f]
		if !ok {
			return m, nil, fmt.Errorf("%s not listed in SHA256SUMS", f)
		}
		m.SHA256Sums[f] = hash
	}
	var sysexts []bluefinSysextSource
	for _, name := range []string{BluefinSysextKubeadm, BluefinSysextKubeStellar, BluefinSysextZFS} {
		asset := bluefinVersionedSysext(name, version)
		if hash, ok := sums.hashes[asset]; ok {
			sysexts = append(sysexts, bluefinSysextSource{name: name, asset: asset, sha256: hash})
		}
	}
	versioned := "_" + version + bluefinDDISuffix + bluefinZstSuffix
	for _, asset := range sums.order {
		if flavour, ok := strings.CutSuffix(asset, versioned); ok && hardware.IsNvidiaDriverFlavour(flavour) {
			sysexts = append(sysexts, bluefinSysextSource{name: flavour, asset: asset, sha256: sums.hashes[asset]})
		}
	}
	for _, own := range []struct{ name, prefix string }{
		{BluefinSysextK0s, bluefinK0sPrefix},
		{BluefinSysextNvidiaContainerToolkit, bluefinNvidiaCTK},
	} {
		if s, ok := selectOwnVersionSysext(version, sums, own.name, own.prefix); ok {
			sysexts = append(sysexts, s)
		}
	}
	return m, sysexts, nil
}

// selectOwnVersionSysext finds the one <prefix><any version>.raw.zst in
// SHA256SUMS; several are ambiguous and skip the sysext.
func selectOwnVersionSysext(version string, sums sha256Sums, name, prefix string) (bluefinSysextSource, bool) {
	var found []string
	for _, asset := range sums.order {
		if strings.HasPrefix(asset, prefix) && strings.HasSuffix(asset, bluefinDDISuffix+bluefinZstSuffix) {
			found = append(found, asset)
		}
	}
	switch len(found) {
	case 0:
		return bluefinSysextSource{}, false
	case 1:
		return bluefinSysextSource{name: name, asset: found[0], sha256: sums.hashes[found[0]]}, true
	}
	slog.Warn("SHA256SUMS lists several "+name+" sysexts; skipping "+name, "version", version, "files", found)
	return bluefinSysextSource{}, false
}

// errBluefinAssetMissing marks a release file the source does not have, so
// an optional sysext can be skipped while any other failure aborts.
var errBluefinAssetMissing = errors.New("not in the release")

// bluefinSource is where a release comes from: GitHub releases or an OCI
// artifact.
type bluefinSource interface {
	// latest is the newest release's version.
	latest(ctx context.Context) (string, error)
	// fetch reads a small file (at most limit bytes) of version.
	fetch(ctx context.Context, version, name string, limit int64) ([]byte, error)
	// download stores name of version at dest, verified against sha256.
	download(ctx context.Context, version, name, dest, sha256 string) error
}

type githubBluefinSource struct{}

func (githubBluefinSource) latest(ctx context.Context) (string, error) {
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
	slog.Debug("Remote Bluefin version found", "version", rel.Version, "tag", rel.Tag)
	return rel.Version, nil
}

func (githubBluefinSource) fetch(ctx context.Context, version, name string, limit int64) ([]byte, error) {
	return githubGet(ctx, bluefinAssetURL(version, name), limit)
}

func (githubBluefinSource) download(ctx context.Context, version, name, dest, sha256 string) error {
	err := config.Download(ctx, config.DownloadClient, bluefinAssetURL(version, name), dest, crypto.SHA256, sha256)
	var status *config.HTTPStatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %w", name, errBluefinAssetMissing)
	}
	return err
}

// newBluefinSource picks the OCI artifact when --bluefinOCI is set, GitHub
// releases of --bluefinRepo otherwise.
func newBluefinSource() (bluefinSource, error) {
	if ref := strings.TrimSpace(viper.GetString(config.BluefinOCI)); ref != "" {
		return newOCIBluefinSource(ref)
	}
	return githubBluefinSource{}, nil
}

// ValidateBluefinFlags checks --bluefinVersion, --bluefinKeyring and
// --bluefinOCI at startup: a pin must be a plain version, the keyring must
// parse (signature checks fail closed) and the OCI reference must be a
// repository.
func ValidateBluefinFlags() error {
	if pin := strings.TrimSpace(viper.GetString(config.BluefinVersion)); pin != "" && !ValidBluefinVersion(pin) {
		return fmt.Errorf("--%s %q: not a Bluefin Server version such as 20260927.123", config.BluefinVersion, pin)
	}
	if path := strings.TrimSpace(viper.GetString(config.BluefinKeyring)); path != "" {
		if _, err := loadBluefinKeyring(path); err != nil {
			return fmt.Errorf("--%s: %w", config.BluefinKeyring, err)
		}
	}
	if ref := strings.TrimSpace(viper.GetString(config.BluefinOCI)); ref != "" {
		if _, err := newOCIBluefinSource(ref); err != nil {
			return fmt.Errorf("--%s: %w", config.BluefinOCI, err)
		}
	}
	return nil
}

// BluefinVersionCheck brings the served Bluefin Server release in line with
// --bluefinVersion or the newest release. Concurrent invocations are
// skipped and the version only advances once every file is on disk and
// verified.
func BluefinVersionCheck() {
	if !state.BluefinUpdateMu.TryLock() {
		slog.Info("Bluefin update already in progress, skipping version check")
		return
	}
	defer state.BluefinUpdateMu.Unlock()
	ctx := context.Background()
	slog.Debug("Checking Bluefin version")

	src, err := newBluefinSource()
	if err != nil {
		slog.Error("Bluefin release source unusable, skipping update", "error", err)
		return
	}

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
		remote, err := src.latest(ctx)
		if err != nil {
			slog.Error("Could not determine remote Bluefin version, skipping update", "error", err)
			return
		}
		state.SetRemoteBluefinVersion(remote)
		target = remote
	}
	if !ValidBluefinVersion(target) {
		slog.Error("Bluefin version is not usable, skipping update", "version", target)
		return
	}

	if target == current {
		return
	}
	slog.Info("Target Bluefin version differs from local", "target", target, "local", current)
	manifest, err := installBluefinRelease(ctx, src, target)
	if err != nil {
		slog.Error("Bluefin release install failed, not advancing version", "target", target, "error", err)
		return
	}
	state.SetCurrentBluefinVersion(target)
	slog.Info("Bluefin updated", "version", target, "netbootUKI", manifest.NetbootUKI, "ddi", manifest.DDI, "sysexts", len(manifest.Sysexts))
	for _, gap := range missingRequestedSysexts(manifest, hardware.Snapshot().Hosts) {
		slog.Error("Bluefin release lacks sysexts a host requests; it boots without them", "version", manifest.Version, "mac", gap.mac, "hostname", gap.hostname, "missing", gap.missing)
	}
}

type sysextGap struct {
	mac, hostname string
	missing       []string
}

// missingRequestedSysexts lists the Bluefin hosts that follow m (no
// targetVersion, or m's) and request extensions m does not carry. A release
// without an optional sysext still installs; this only names who is
// affected.
func missingRequestedSysexts(m BluefinManifest, hosts map[string]*hardware.Host) []sysextGap {
	var gaps []sysextGap
	for mac, h := range hosts {
		if h == nil || h.OS != OSBluefin || (h.TargetVersion != "" && h.TargetVersion != m.Version) {
			continue
		}
		var missing []string
		for _, e := range h.Extensions {
			if _, ok := m.Sysexts[e]; !ok {
				missing = append(missing, e)
			}
		}
		if len(missing) > 0 {
			gaps = append(gaps, sysextGap{mac: mac, hostname: h.Hostname, missing: missing})
		}
	}
	slices.SortFunc(gaps, func(a, b sysextGap) int { return strings.Compare(a.mac, b.mac) })
	return gaps
}

// LoadRemoteBluefinVersion asks the configured source for the newest
// release and records it in state.
func LoadRemoteBluefinVersion(ctx context.Context) (string, error) {
	src, err := newBluefinSource()
	if err != nil {
		return "", err
	}
	v, err := src.latest(ctx)
	if err != nil {
		return "", err
	}
	state.SetRemoteBluefinVersion(v)
	return v, nil
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

// installBluefinRelease fetches SHA256SUMS and its signature (checked
// against --bluefinKeyring when set), downloads the netboot UKI, the DDI
// and every listed sysext (decompressed) into DataDir/bluefin/<version>/,
// writes the manifest, repoints bluefin/current (and bluefin/previous to
// the release it replaces) and prunes every other release directory.
func installBluefinRelease(ctx context.Context, src bluefinSource, version string) (BluefinManifest, error) {
	sumsBody, err := src.fetch(ctx, version, BluefinSumsFile, 1<<20)
	if err != nil {
		return BluefinManifest{}, fmt.Errorf("%s: %w", BluefinSumsFile, err)
	}
	sigBody, err := src.fetch(ctx, version, BluefinSigFile, 1<<20)
	if err != nil {
		return BluefinManifest{}, fmt.Errorf("%s: %w", BluefinSigFile, err)
	}
	if keyring := strings.TrimSpace(viper.GetString(config.BluefinKeyring)); keyring != "" {
		signer, err := verifyBluefinSums(keyring, sumsBody, sigBody)
		if err != nil {
			return BluefinManifest{}, err
		}
		slog.Info("Bluefin SHA256SUMS signature verified", "version", version, "signer", signer)
	}
	sums, err := parseSHA256SUMS(strings.NewReader(string(sumsBody)))
	if err != nil {
		return BluefinManifest{}, err
	}
	manifest, sysexts, err := selectBluefinArtifacts(version, sums)
	if err != nil {
		return BluefinManifest{}, err
	}
	slog.Info("Selected Bluefin artifacts", "version", version, "netbootUKI", manifest.NetbootUKI, "ddi", manifest.DDI, "sysexts", len(sysexts))

	dir := config.DataPath(config.BluefinDir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return manifest, err
	}
	for _, file := range []string{manifest.NetbootUKI, manifest.DDI} {
		dest := filepath.Join(dir, file)
		hash := manifest.SHA256Sums[file]
		if config.FileHashMatches(dest, crypto.SHA256, hash) {
			slog.Info("Artifact already present and verified", "path", dest)
			continue
		}
		if err := src.download(ctx, version, file, dest, hash); err != nil {
			return manifest, fmt.Errorf("%s: %w", file, err)
		}
	}
	previous, _ := LoadBluefinManifest(dir)
	for _, s := range sysexts {
		sysext, err := installBluefinSysext(ctx, src, version, dir, s, previous.Sysexts[s.name])
		if errors.Is(err, errBluefinAssetMissing) {
			slog.Warn("Bluefin sysext listed in SHA256SUMS but not published; skipping it", "version", version, "sysext", s.name, "file", s.asset)
			continue
		}
		if err != nil {
			return manifest, fmt.Errorf("%s: %w", s.asset, err)
		}
		if manifest.Sysexts == nil {
			manifest.Sysexts = map[string]BluefinSysext{}
		}
		manifest.Sysexts[s.name] = sysext
		manifest.SHA256Sums[s.asset] = s.sha256
	}

	if err := config.WriteFileAtomic(filepath.Join(dir, BluefinSumsFile), sumsBody, 0o644); err != nil {
		return manifest, fmt.Errorf("%s: %w", BluefinSumsFile, err)
	}
	if err := config.WriteFileAtomic(filepath.Join(dir, BluefinSigFile), sigBody, 0o644); err != nil {
		return manifest, fmt.Errorf("%s: %w", BluefinSigFile, err)
	}
	if err := writeBluefinManifest(dir, manifest); err != nil {
		return manifest, err
	}
	if err := linkRelease(OSBluefin, version); err != nil {
		return manifest, err
	}
	pruneReleases(OSBluefin)
	return manifest, nil
}

// installBluefinSysext downloads s (verified against SHA256SUMS) and
// decompresses it next to the other release files, since Ignition's files
// stage cannot decompress zstd. have is what an earlier install of the same
// version recorded; a .raw still matching it is kept.
func installBluefinSysext(ctx context.Context, src bluefinSource, version, dir string, s bluefinSysextSource, have BluefinSysext) (BluefinSysext, error) {
	raw := filepath.Join(dir, s.raw())
	if have.File == s.raw() && config.FileHashMatches(raw, crypto.SHA256, have.Sha256) {
		slog.Info("Sysext already present and verified", "path", raw)
		return have, nil
	}
	compressed := filepath.Join(dir, s.asset)
	if !config.FileHashMatches(compressed, crypto.SHA256, s.sha256) {
		if err := src.download(ctx, version, s.asset, compressed, s.sha256); err != nil {
			return BluefinSysext{}, err
		}
	}
	sum, err := decompressZstd(compressed, raw)
	if err != nil {
		return BluefinSysext{}, err
	}
	if err := os.Remove(compressed); err != nil {
		slog.Warn("Could not remove compressed sysext", "path", compressed, "error", err)
	}
	slog.Info("Sysext decompressed", "path", raw, "sha256", sum)
	return BluefinSysext{File: s.raw(), Sha256: sum}, nil
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
	if !ValidBluefinVersion(m.Version) || m.NetbootUKI != BluefinNetbootUKI(m.Version) || m.DDI != BluefinDDI(m.Version) {
		return m, fmt.Errorf("%s: incomplete or not a netboot release manifest", config.BluefinManifestFile)
	}
	return m, nil
}

func bluefinManifestAt(link string) (BluefinManifest, bool) {
	m, err := LoadBluefinManifest(config.DataPath(config.BluefinDir, link))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("Bluefin manifest unreadable", "link", link, "error", err)
		}
		return BluefinManifest{}, false
	}
	return m, true
}

// CurrentBluefinManifest returns the manifest of the served release, or
// ok=false when no release has been installed yet.
func CurrentBluefinManifest() (BluefinManifest, bool) {
	return bluefinManifestAt(config.BluefinCurrentLink)
}

// PreviousBluefinManifest returns the manifest of the release the current
// one replaced, still served to hosts that booted it.
func PreviousBluefinManifest() (BluefinManifest, bool) {
	return bluefinManifestAt(config.BluefinPreviousLink)
}

// BluefinManifestFor returns the manifest of a cached release by version,
// ok=false when it is not on disk.
func BluefinManifestFor(version string) (BluefinManifest, bool) {
	if !ReleaseCached(OSBluefin, version) {
		return BluefinManifest{}, false
	}
	return bluefinManifestAt(version)
}

// CachedBluefinManifests lists the manifests of every cached release,
// newest first.
func CachedBluefinManifests() []BluefinManifest {
	var out []BluefinManifest
	for _, v := range CachedReleases(OSBluefin) {
		if m, ok := bluefinManifestAt(v); ok {
			out = append(out, m)
		}
	}
	return out
}

// MissingBluefinArtifacts lists the files named by bluefin/current/manifest.json
// that are absent from dataDir. It is nil when there is no manifest, and
// names the manifest itself when it is unreadable or from the retired
// installer-v* format, so the next check downloads a release afresh.
func MissingBluefinArtifacts(dataDir string) []string {
	dir := filepath.Join(dataDir, config.BluefinDir, config.BluefinCurrentLink)
	m, err := LoadBluefinManifest(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []string{config.BluefinManifestFile}
	}
	return missingArtifacts(dir, m.Files())
}
