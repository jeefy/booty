package versions

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/rpm"
	"github.com/jeefy/booty/pkg/secureboot"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

// Secure Boot bundle members, relative to DataDir/secureboot/<bundle>/.
const (
	sbIPXEShim        = "ipxe-shimx64.efi"
	sbSnponlyShim     = "snponly-shimx64.efi"
	sbIPXE            = "ipxe.efi"
	sbSnponly         = "snponly.efi"
	sbFedoraShim      = config.SecureBootFedoraDir + "/shimx64.efi"
	sbFedoraGrub      = config.SecureBootFedoraDir + "/grubx64.efi"
	sbIPXETarball     = "ipxeboot.tar.gz"
	sbIPXETarMember   = "ipxeboot/x86_64-sb/"
	sbFlatcarShim     = "flatcar_production_image.shim"
	sbFlatcarCAInfo   = "flatcar-ca.json"
	flatcarCASourceFn = "flatcar_production_image.shim"
)

// Pinned digests for the default artefact versions, checked on 2026-09-26
// against the GitHub release asset digests (ipxe/shim, ipxe/ipxe) and the
// downloaded Fedora packages. Other versions fall back to the release
// API's asset digest (GitHub) or an unverified download with a warning
// (Fedora).
var (
	ipxeShimPins = map[string]map[string]string{
		config.DefaultSecureBootIPXEShim: {
			sbIPXEShim: "5eecca2780bd49c900565e124516a1bd666ec5e012825f34991b6ba1ef2fa6cf",
		},
	}
	ipxeTarballPins = map[string]string{
		config.DefaultSecureBootIPXE: "01a526d4cc791fc30362259c609d6c506cc64a7bdff51b9a5eb788354e17eee1",
	}
	// Per-member digests of the default tarball, recorded in the manifest
	// and checked after extraction so a tampered archive with a matching
	// outer hash is impossible by construction and a changed member is
	// still noticed.
	ipxeMemberPins = map[string]map[string]string{
		config.DefaultSecureBootIPXE: {
			sbIPXE:    "6558e37887516b246d6a97122e8d18bedfe4197b7ba7f67bf1bf102a16678d33",
			sbSnponly: "b1e67c3e4a1e8708ddfd0079ad4505e3a02245acb55ee9a95437ab3c507be82a",
		},
	}
	fedoraRPMPins = map[string]string{
		"shim-x64-16.1-7.x86_64.rpm":            "04b7132d6316bff71427120b6aba85eb4490b2621ccb2f2559bd321ccb25f028",
		"grub2-efi-x64-2.12-64.fc44.x86_64.rpm": "3ed403514d8a8973814d553acd230a25eb355e93ebbb5a9a7da91f8393f58802",
	}
	// Fedora releases whose trees are tried, newest first, for a package
	// whose version does not say (.fcNN) where it lives.
	fedoraReleases = []string{"45", "44"}
)

// Overridable in tests.
var (
	fedoraMirrorBase = "https://dl.fedoraproject.org/pub/fedora/linux"
	secureBootMu     sync.Mutex
)

// SecureBootFile is one member of the bundle manifest.
type SecureBootFile struct {
	Name   string `json:"name"`
	Sha256 string `json:"sha256"`
	Source string `json:"source"`
}

// SecureBootManifest is DataDir/secureboot/<bundle>/manifest.json.
type SecureBootManifest struct {
	BundleVersion     string           `json:"bundleVersion"`
	IPXEShimVersion   string           `json:"ipxeShimVersion"`
	IPXEVersion       string           `json:"ipxeVersion"`
	FedoraShimVersion string           `json:"fedoraShimVersion"`
	FedoraGrubVersion string           `json:"fedoraGrubVersion"`
	Files             []SecureBootFile `json:"files"`
}

// FileNames lists the bundle members the manifest expects on disk.
func (m SecureBootManifest) FileNames() []string {
	out := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		out = append(out, f.Name)
	}
	return out
}

// FlatcarCAInfo is DataDir/secureboot/flatcar-ca.json: which Flatcar release
// the CA next to it was extracted from and its fingerprint.
type FlatcarCAInfo struct {
	FlatcarVersion string `json:"flatcarVersion"`
	Sha256         string `json:"sha256"`
	Subject        string `json:"subject"`
	NotBefore      string `json:"notBefore"`
	NotAfter       string `json:"notAfter"`
	Source         string `json:"source"`
}

// SecureBootBundleVersion names the bundle directory after every pinned
// upstream version, so changing any flag produces a new bundle.
func SecureBootBundleVersion() string {
	return fmt.Sprintf("%s_%s_shim-%s_grub-%s",
		viper.GetString(config.SecureBootIPXEShim), viper.GetString(config.SecureBootIPXE),
		viper.GetString(config.FedoraShimVersion), viper.GetString(config.FedoraGrubVersion))
}

// SecureBootVersionCheck syncs the Secure Boot bundle for the configured
// versions and the Flatcar CA for the served Flatcar release. It does
// nothing unless --secureBoot is on, and concurrent runs are skipped.
func SecureBootVersionCheck() {
	if !viper.GetBool(config.SecureBoot) {
		return
	}
	if !secureBootMu.TryLock() {
		slog.Info("Secure Boot sync already in progress, skipping")
		return
	}
	defer secureBootMu.Unlock()
	ctx := context.Background()

	bundle := SecureBootBundleVersion()
	if m, ok := CurrentSecureBootManifest(); ok && m.BundleVersion == bundle && len(MissingSecureBootArtifacts(viper.GetString(config.DataDir))) == 0 {
		slog.Debug("Secure Boot bundle up to date", "bundle", bundle)
	} else {
		slog.Info("Syncing Secure Boot bundle", "bundle", bundle)
		if _, err := installSecureBootBundle(ctx, bundle); err != nil {
			slog.Error("Secure Boot bundle sync failed; HTTP Boot clients will get 404 until it succeeds", "bundle", bundle, "error", err)
		} else {
			slog.Info("Secure Boot bundle ready", "bundle", bundle)
		}
	}

	if err := syncFlatcarCA(ctx); err != nil {
		slog.Error("Flatcar Secure Boot CA sync failed", "error", err)
	}
}

var safeVersionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

func validVersionComponent(flag, v string) error {
	if !safeVersionRe.MatchString(v) {
		return fmt.Errorf("--%s %q is not a plain version string", flag, v)
	}
	return nil
}

// ValidateSecureBootFlags rejects version strings that could not be part
// of a URL path or directory name, and the trusted-CA list.
func ValidateSecureBootFlags() error {
	for flag, v := range map[string]string{
		config.SecureBootIPXEShim: viper.GetString(config.SecureBootIPXEShim),
		config.SecureBootIPXE:     viper.GetString(config.SecureBootIPXE),
		config.FedoraShimVersion:  viper.GetString(config.FedoraShimVersion),
		config.FedoraGrubVersion:  viper.GetString(config.FedoraGrubVersion),
	} {
		if err := validVersionComponent(flag, v); err != nil {
			return err
		}
	}
	_, err := config.ParseSecureBootTrusted(viper.GetString(config.SecureBootTrusted))
	return err
}

// installSecureBootBundle downloads and extracts every member into
// DataDir/secureboot/<bundle>/, writes manifest.json, repoints current and
// prunes older bundles. Members already on disk with the expected digest
// are kept.
func installSecureBootBundle(ctx context.Context, bundle string) (SecureBootManifest, error) {
	m := SecureBootManifest{
		BundleVersion:     bundle,
		IPXEShimVersion:   viper.GetString(config.SecureBootIPXEShim),
		IPXEVersion:       viper.GetString(config.SecureBootIPXE),
		FedoraShimVersion: viper.GetString(config.FedoraShimVersion),
		FedoraGrubVersion: viper.GetString(config.FedoraGrubVersion),
	}
	dir := config.SecureBootPath(bundle)
	if err := os.MkdirAll(filepath.Join(dir, config.SecureBootFedoraDir), 0o755); err != nil {
		return m, err
	}

	shimSource, err := installIPXEShim(ctx, dir, m.IPXEShimVersion)
	if err != nil {
		return m, fmt.Errorf("ipxe shim: %w", err)
	}
	m.Files = append(m.Files, SecureBootFile{Name: sbIPXEShim, Source: shimSource}, SecureBootFile{Name: sbSnponlyShim, Source: shimSource + " (same binary; shim derives the loader name from its own)"})

	tarSource, err := installIPXEBinaries(ctx, dir, m.IPXEVersion)
	if err != nil {
		return m, fmt.Errorf("ipxe binaries: %w", err)
	}
	m.Files = append(m.Files, SecureBootFile{Name: sbIPXE, Source: tarSource + "#" + sbIPXETarMember + sbIPXE}, SecureBootFile{Name: sbSnponly, Source: tarSource + "#" + sbIPXETarMember + sbSnponly})

	shimRPM := fmt.Sprintf("shim-x64-%s.x86_64.rpm", m.FedoraShimVersion)
	shimRPMSource, err := installFedoraRPMMember(ctx, dir, shimRPM, fedoraReleaseOf(m.FedoraShimVersion), fmt.Sprintf("usr/lib/efi/shim/%s/EFI/fedora/shimx64.efi", m.FedoraShimVersion), sbFedoraShim)
	if err != nil {
		return m, fmt.Errorf("fedora shim: %w", err)
	}
	m.Files = append(m.Files, SecureBootFile{Name: sbFedoraShim, Source: shimRPMSource})

	grubRPM := fmt.Sprintf("grub2-efi-x64-%s.x86_64.rpm", m.FedoraGrubVersion)
	grubRPMSource, err := installFedoraRPMMember(ctx, dir, grubRPM, fedoraReleaseOf(m.FedoraGrubVersion), "usr/lib/efi/grub2/*/EFI/fedora/grubx64.efi", sbFedoraGrub)
	if err != nil {
		return m, fmt.Errorf("fedora grub: %w", err)
	}
	m.Files = append(m.Files, SecureBootFile{Name: sbFedoraGrub, Source: grubRPMSource})

	for i := range m.Files {
		sum, err := fileSHA256(filepath.Join(dir, m.Files[i].Name))
		if err != nil {
			return m, err
		}
		m.Files[i].Sha256 = sum
	}
	if err := writeSecureBootManifest(dir, m); err != nil {
		return m, err
	}
	if err := config.ReplaceSymlink(bundle, config.SecureBootPath(config.SecureBootCurrentLink)); err != nil {
		return m, fmt.Errorf("linking current: %w", err)
	}
	pruneSecureBootBundles(bundle)
	return m, nil
}

func ipxeShimURL(version, asset string) string {
	return fmt.Sprintf("%s/ipxe/shim/releases/download/%s/%s", githubDownloadBase, version, asset)
}

func ipxeTarballURL(version string) string {
	return fmt.Sprintf("%s/ipxe/ipxe/releases/download/%s/%s", githubDownloadBase, version, sbIPXETarball)
}

// githubAssetDigest resolves the sha256 of a release asset: the pin for the
// default version, otherwise the digest the releases API reports.
func githubAssetDigest(ctx context.Context, repo, tag, asset string, pins map[string]string) (string, error) {
	if sum, ok := pins[asset]; ok {
		return sum, nil
	}
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", githubAPIBase, repo, tag)
	body, err := githubGet(ctx, url, 4<<20)
	if err != nil {
		return "", err
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	for _, a := range rel.Assets {
		if a.Name == asset {
			if sum, ok := strings.CutPrefix(a.Digest, "sha256:"); ok && len(sum) == 64 {
				return sum, nil
			}
			return "", fmt.Errorf("%s: asset %s has no sha256 digest (%q)", url, asset, a.Digest)
		}
	}
	return "", fmt.Errorf("%s: no asset named %s", url, asset)
}

func installIPXEShim(ctx context.Context, dir, version string) (string, error) {
	url := ipxeShimURL(version, sbIPXEShim)
	sum, err := githubAssetDigest(ctx, "ipxe/shim", version, sbIPXEShim, ipxeShimPins[version])
	if err != nil {
		return url, err
	}
	dest := filepath.Join(dir, sbIPXEShim)
	if config.FileHashMatches(dest, crypto.SHA256, sum) {
		slog.Info("Artifact already present and verified", "path", dest)
	} else if err := config.Download(ctx, config.DownloadClient, url, dest, crypto.SHA256, sum); err != nil {
		return url, err
	}
	if err := config.ReplaceSymlink(sbIPXEShim, filepath.Join(dir, sbSnponlyShim)); err != nil {
		return url, fmt.Errorf("linking %s: %w", sbSnponlyShim, err)
	}
	return url, nil
}

// installIPXEBinaries fetches ipxeboot.tar.gz (verified against the pin or
// the release digest) and stream-extracts only the two x86_64-sb members.
func installIPXEBinaries(ctx context.Context, dir, version string) (string, error) {
	url := ipxeTarballURL(version)
	memberPins := ipxeMemberPins[version]
	if memberPins != nil && config.FileHashMatches(filepath.Join(dir, sbIPXE), crypto.SHA256, memberPins[sbIPXE]) && config.FileHashMatches(filepath.Join(dir, sbSnponly), crypto.SHA256, memberPins[sbSnponly]) {
		slog.Info("Artifacts already present and verified", "path", filepath.Join(dir, sbIPXE))
		return url, nil
	}
	sum, err := githubAssetDigest(ctx, "ipxe/ipxe", version, sbIPXETarball, ipxeTarballPins)
	if err != nil {
		return url, err
	}
	tarball := filepath.Join(dir, sbIPXETarball)
	if err := config.Download(ctx, config.DownloadClient, url, tarball, crypto.SHA256, sum); err != nil {
		return url, err
	}
	defer removeQuietly(tarball)

	members, err := extractTarGzMembers(tarball, sbIPXETarMember+sbIPXE, sbIPXETarMember+sbSnponly)
	if err != nil {
		return url, err
	}
	for member, data := range members {
		name := path.Base(member)
		if want, ok := memberPins[name]; ok {
			got := sha256Hex(data)
			if !strings.EqualFold(got, want) {
				return url, fmt.Errorf("%s in %s: sha256 %s, want %s", member, sbIPXETarball, got, want)
			}
		}
		if err := config.WriteFileAtomic(filepath.Join(dir, name), data, 0o644); err != nil {
			return url, err
		}
	}
	return url, nil
}

// extractTarGzMembers returns the named members of a .tar.gz, all of which
// must be present as regular files.
func extractTarGzMembers(archive string, names ...string) (map[string][]byte, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer config.CloseQuietly(f, archive)
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", archive, err)
	}
	defer config.CloseQuietly(gz, archive)

	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	out := make(map[string][]byte, len(names))
	tr := tar.NewReader(gz)
	for len(out) < len(names) {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", archive, err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if !want[name] || hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 64<<20))
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", archive, name, err)
		}
		out[name] = data
	}
	for _, n := range names {
		if _, ok := out[n]; !ok {
			return nil, fmt.Errorf("%s: member %s not found", archive, n)
		}
	}
	return out, nil
}

// fedoraReleaseOf pulls the NN out of a ".fcNN" version suffix; empty when
// the version does not carry one (the shim package does not).
func fedoraReleaseOf(version string) string {
	if i := strings.LastIndex(version, ".fc"); i >= 0 {
		rel := version[i+3:]
		if rel != "" && strings.Trim(rel, "0123456789") == "" {
			return rel
		}
	}
	return ""
}

// fedoraRPMURLs lists the mirror paths a package may live at, most likely
// first: the release named by its version (if any), then every release in
// fedoraReleases, each as updates/, releases/ and development/.
func fedoraRPMURLs(rpmName, release string) []string {
	letter := strings.ToLower(rpmName[:1])
	releases := fedoraReleases
	if release != "" {
		releases = append([]string{release}, fedoraReleases...)
	}
	var urls []string
	seen := map[string]bool{}
	for _, rel := range releases {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		urls = append(urls,
			fmt.Sprintf("%s/updates/%s/Everything/x86_64/Packages/%s/%s", fedoraMirrorBase, rel, letter, rpmName),
			fmt.Sprintf("%s/releases/%s/Everything/x86_64/os/Packages/%s/%s", fedoraMirrorBase, rel, letter, rpmName),
			fmt.Sprintf("%s/development/%s/Everything/x86_64/os/Packages/%s/%s", fedoraMirrorBase, rel, letter, rpmName),
		)
	}
	return urls
}

// installFedoraRPMMember downloads rpmName from the first mirror path that
// has it, extracts member into dir/dest and removes the package. A pinned
// package is verified; others are downloaded with a warning.
func installFedoraRPMMember(ctx context.Context, dir, rpmName, release, member, dest string) (string, error) {
	destPath := filepath.Join(dir, dest)
	sum, pinned := fedoraRPMPins[rpmName]
	if pinned {
		if source, ok := existingSourceFor(dir, dest); ok {
			slog.Info("Artifact already present", "path", destPath)
			return source, nil
		}
	} else {
		slog.Warn("No pinned sha256 for this Fedora package; downloading unverified", "rpm", rpmName)
	}

	rpmPath := filepath.Join(dir, rpmName)
	var url string
	var lastErr error
	for _, candidate := range fedoraRPMURLs(rpmName, release) {
		hashAlgo := crypto.Hash(0)
		if pinned {
			hashAlgo = crypto.SHA256
		}
		if err := config.Download(ctx, config.DownloadClient, candidate, rpmPath, hashAlgo, sum); err != nil {
			lastErr = err
			if strings.Contains(err.Error(), "checksum mismatch") {
				return candidate, err
			}
			slog.Debug("Fedora mirror path did not have the package", "url", candidate, "error", err)
			continue
		}
		url = candidate
		break
	}
	if url == "" {
		return "", fmt.Errorf("%s not found on any mirror path: %w", rpmName, lastErr)
	}
	defer removeQuietly(rpmPath)

	f, err := os.Open(rpmPath)
	if err != nil {
		return url, err
	}
	defer config.CloseQuietly(f, rpmPath)
	files, err := rpm.Extract(f, member)
	if err != nil {
		return url, fmt.Errorf("%s: %w", rpmName, err)
	}
	if err := config.WriteFileAtomic(destPath, files[member], 0o644); err != nil {
		return url, err
	}
	return url + "#" + member, nil
}

// existingSourceFor reports whether dest already exists in dir and, from
// the bundle's previous manifest, where it came from.
func existingSourceFor(dir, dest string) (string, bool) {
	if !artifactPresent(filepath.Join(dir, dest)) {
		return "", false
	}
	m, err := LoadSecureBootManifest(dir)
	if err != nil {
		return "", false
	}
	for _, f := range m.Files {
		if f.Name == dest && config.FileHashMatches(filepath.Join(dir, dest), crypto.SHA256, f.Sha256) {
			return f.Source, true
		}
	}
	return "", false
}

// syncFlatcarCA extracts the Flatcar Secure Boot CA from the shim of the
// Flatcar release Booty serves and stores it as flatcar-ca.der/.pem with a
// JSON sidecar. Nothing happens until a Flatcar release is on disk.
func syncFlatcarCA(ctx context.Context) error {
	version := state.CurrentFlatcarVersion()
	if !versionIsSet(version) {
		slog.Debug("No Flatcar release yet; Flatcar Secure Boot CA not extracted")
		return nil
	}
	prev, hadPrev := CurrentFlatcarCA()
	if hadPrev && prev.FlatcarVersion == version && artifactPresent(config.SecureBootPath(config.SecureBootFlatcarCADER)) && artifactPresent(config.SecureBootPath(config.SecureBootFlatcarCAPEM)) {
		return nil
	}

	base := flatcarReleaseURL(version)
	url := base + "/" + flatcarCASourceFn
	shimPath := config.SecureBootPath(sbFlatcarShim)
	hashAlgo := crypto.Hash(0)
	digest, err := LoadFlatcarDigest(ctx, base, flatcarCASourceFn)
	if err != nil {
		slog.Warn("Could not load digest for the Flatcar shim, falling back to unverified download", "error", err)
	} else {
		hashAlgo = crypto.SHA512
	}
	if err := config.Download(ctx, config.DownloadClient, url, shimPath, hashAlgo, digest); err != nil {
		return err
	}
	defer removeQuietly(shimPath)

	f, err := os.Open(shimPath)
	if err != nil {
		return err
	}
	defer config.CloseQuietly(f, shimPath)
	cert, err := secureboot.ExtractVendorCert(f)
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	info := FlatcarCAInfo{FlatcarVersion: version, Sha256: cert.SHA256(), Subject: cert.Subject, NotBefore: cert.NotBefore, NotAfter: cert.NotAfter, Source: url}
	if err := config.WriteFileAtomic(config.SecureBootPath(config.SecureBootFlatcarCADER), cert.DER, 0o644); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(config.SecureBootPath(config.SecureBootFlatcarCAPEM), cert.PEM(), 0o644); err != nil {
		return err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(config.SecureBootPath(sbFlatcarCAInfo), append(data, '\n'), 0o644); err != nil {
		return err
	}
	if hadPrev && prev.Sha256 != info.Sha256 {
		slog.Warn("Flatcar rotated its Secure Boot CA; machines that enrolled the previous certificate must enroll the new one", "previous", prev.Sha256, "current", info.Sha256, "flatcarVersion", version)
	} else {
		slog.Info("Flatcar Secure Boot CA extracted", "flatcarVersion", version, "sha256", info.Sha256, "subject", info.Subject, "notAfter", info.NotAfter)
	}
	return nil
}

// CurrentFlatcarCA reads flatcar-ca.json; ok=false when it does not exist.
func CurrentFlatcarCA() (FlatcarCAInfo, bool) {
	var info FlatcarCAInfo
	data, err := os.ReadFile(config.SecureBootPath(sbFlatcarCAInfo))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("Flatcar CA sidecar unreadable", "error", err)
		}
		return info, false
	}
	if err := json.Unmarshal(data, &info); err != nil || info.Sha256 == "" {
		slog.Warn("Flatcar CA sidecar malformed", "error", err)
		return info, false
	}
	return info, true
}

func writeSecureBootManifest(dir string, m SecureBootManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(filepath.Join(dir, config.SecureBootManifestFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("%s: %w", config.SecureBootManifestFile, err)
	}
	return nil
}

// LoadSecureBootManifest reads manifest.json from dir.
func LoadSecureBootManifest(dir string) (SecureBootManifest, error) {
	var m SecureBootManifest
	data, err := os.ReadFile(filepath.Join(dir, config.SecureBootManifestFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("%s: %w", config.SecureBootManifestFile, err)
	}
	if m.BundleVersion == "" || len(m.Files) == 0 {
		return m, fmt.Errorf("%s: incomplete", config.SecureBootManifestFile)
	}
	return m, nil
}

// CurrentSecureBootManifest returns the manifest behind secureboot/current,
// or ok=false when no bundle has been installed yet.
func CurrentSecureBootManifest() (SecureBootManifest, bool) {
	m, err := LoadSecureBootManifest(config.SecureBootPath(config.SecureBootCurrentLink))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("Secure Boot manifest unreadable", "error", err)
		}
		return SecureBootManifest{}, false
	}
	return m, true
}

// MissingSecureBootArtifacts lists the bundle members named by
// secureboot/current/manifest.json that are absent from dataDir. It is
// nil when there is no manifest.
func MissingSecureBootArtifacts(dataDir string) []string {
	dir := filepath.Join(dataDir, config.SecureBootDir, config.SecureBootCurrentLink)
	m, err := LoadSecureBootManifest(dir)
	if err != nil {
		return nil
	}
	return missingArtifacts(dir, m.FileNames())
}

// SecureBootReady reports whether HTTP Boot clients can be served: the
// flag is on and the current bundle is complete.
func SecureBootReady() bool {
	if !viper.GetBool(config.SecureBoot) {
		return false
	}
	_, ok := CurrentSecureBootManifest()
	return ok && len(MissingSecureBootArtifacts(viper.GetString(config.DataDir))) == 0
}

func pruneSecureBootBundles(keep string) {
	root := config.SecureBootPath()
	entries, err := os.ReadDir(root)
	if err != nil {
		slog.Warn("Could not list Secure Boot bundle directories", "path", root, "error", err)
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && e.Name() != keep {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		p := filepath.Join(root, name)
		if err := os.RemoveAll(p); err != nil {
			slog.Warn("Could not remove old Secure Boot bundle", "path", p, "error", err)
			continue
		}
		slog.Info("Removed old Secure Boot bundle", "path", p)
	}
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer config.CloseQuietly(f, p)
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func removeQuietly(p string) {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("Could not remove file", "path", p, "error", err)
	}
}
