package versions

import (
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

// storageTTL bounds how often GET /storage walks the data directory; a
// sync or prune invalidates the cached walk early.
const storageTTL = 60 * time.Second

// Asset kinds, by what the top-level file is for.
const (
	AssetIPXE     = "ipxe"
	AssetShim     = "shim"
	AssetSyslinux = "syslinux"
	AssetGrub     = "grub"
	AssetOther    = "other"
)

// Retention reasons of a release directory, in the order one is picked.
const (
	RetainedCurrent  = "current"
	RetainedPrevious = "previous"
	RetainedLastGood = "lastGood"
	RetainedPinned   = "pinned"
	RetainedNone     = "-"
)

type Bytes struct {
	Bytes int64 `json:"bytes"`
}

// StorageRelease is one DataDir/<os>/<version>/ directory.
type StorageRelease struct {
	Version  string       `json:"version"`
	Bytes    int64        `json:"bytes"`
	Files    int          `json:"files"`
	Modified time.Time    `json:"modified"`
	Links    []string     `json:"links"`
	Hosts    StorageHosts `json:"hosts"`
	Retained string       `json:"retained"`
	Cached   bool         `json:"cached"`
}

// StorageHosts names the hosts that reference a release: those whose
// last update check reported it running and those pinned to it by
// targetVersion.
type StorageHosts struct {
	Running []string `json:"running"`
	Pinned  []string `json:"pinned"`
}

// StorageOS is the per-OS block of GET /storage.
type StorageOS struct {
	Tracked  bool             `json:"tracked"`
	Channel  string           `json:"channel"`
	Pin      string           `json:"pin"`
	Source   string           `json:"source,omitempty"`
	Bytes    int64            `json:"bytes"`
	Releases []StorageRelease `json:"releases"`
}

// StorageAsset is a top-level file under DataDir that belongs to no
// release: the iPXE binaries, Secure Boot shims, pins and version files.
type StorageAsset struct {
	Name     string    `json:"name"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
	Kind     string    `json:"kind"`
}

// StorageEntry is any other top-level directory (config, cluster,
// secureboot, a leftover registry) with its size.
type StorageEntry struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type StorageAutopilot struct {
	StateBytes   int64 `json:"stateBytes"`
	ReportsBytes int64 `json:"reportsBytes"`
}

// Storage is GET /storage: what the data directory holds and why.
type Storage struct {
	DataDir    string               `json:"dataDir"`
	Total      Bytes                `json:"total"`
	Free       Bytes                `json:"free"`
	Used       Bytes                `json:"used"`
	OS         map[string]StorageOS `json:"os"`
	Assets     []StorageAsset       `json:"assets"`
	Other      []StorageEntry       `json:"other"`
	Autopilot  StorageAutopilot     `json:"autopilot"`
	ComputedAt time.Time            `json:"computedAt"`
}

// storageWalk is the expensive part of GET /storage: every size under
// DataDir. Links, hosts and retention are joined at request time so they
// are never stale.
type storageWalk struct {
	total, free, used int64
	releases          map[string]map[string]StorageRelease
	assets            []StorageAsset
	other             []StorageEntry
	autopilot         StorageAutopilot
	at                time.Time
}

var storageCache struct {
	mu      sync.Mutex
	walk    *storageWalk
	expires time.Time
}

// InvalidateStorage drops the cached walk so the next GET /storage
// recomputes; the release links and prunes call it.
func InvalidateStorage() {
	storageCache.mu.Lock()
	storageCache.expires = time.Time{}
	storageCache.mu.Unlock()
}

func cachedWalk() *storageWalk {
	storageCache.mu.Lock()
	defer storageCache.mu.Unlock()
	if storageCache.walk == nil || !time.Now().Before(storageCache.expires) {
		storageCache.walk = walkStorage(viper.GetString(config.DataDir))
		storageCache.expires = time.Now().Add(storageTTL)
	}
	return storageCache.walk
}

// CurrentStorage is the data directory's storage view: sizes from a walk
// recomputed at most every storageTTL, joined with the current links and
// hosts.
func CurrentStorage() Storage {
	w := cachedWalk()
	hosts := hardware.Snapshot().Hosts
	s := Storage{DataDir: viper.GetString(config.DataDir), OS: map[string]StorageOS{}, Assets: slices.Clone(w.assets), Other: slices.Clone(w.other), Autopilot: w.autopilot, ComputedAt: w.at}
	s.Total.Bytes, s.Free.Bytes, s.Used.Bytes = w.total, w.free, w.used
	for _, osName := range ReleaseOSes {
		s.OS[osName] = osStorage(osName, w.releases[osName], hosts)
	}
	return s
}

func walkStorage(dataDir string) *storageWalk {
	w := &storageWalk{releases: map[string]map[string]StorageRelease{}, assets: []StorageAsset{}, other: []StorageEntry{}, at: time.Now().UTC()}
	w.total, w.free = statfs(dataDir)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		slog.Warn("Could not list the data directory for /storage", "dir", dataDir, "error", err)
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case slices.Contains(ReleaseOSes, name) && e.IsDir():
			w.releases[name] = map[string]StorageRelease{}
			for _, version := range releaseDirs(name) {
				bytes, files, modified := dirSize(filepath.Join(dataDir, name, version))
				w.releases[name][version] = StorageRelease{Version: version, Bytes: bytes, Files: files, Modified: modified}
				w.used += bytes
			}
		case name == config.AutopilotDir && e.IsDir():
			w.autopilot = autopilotStorage(filepath.Join(dataDir, name))
			w.used += w.autopilot.StateBytes + w.autopilot.ReportsBytes
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				continue
			}
			w.assets = append(w.assets, StorageAsset{Name: name, Bytes: info.Size(), Modified: info.ModTime().UTC(), Kind: assetKind(name)})
			w.used += info.Size()
		case e.IsDir():
			bytes, _, _ := dirSize(filepath.Join(dataDir, name))
			w.other = append(w.other, StorageEntry{Name: name, Bytes: bytes})
			w.used += bytes
		}
	}
	sort.Slice(w.assets, func(i, j int) bool { return w.assets[i].Name < w.assets[j].Name })
	sort.Slice(w.other, func(i, j int) bool { return w.other[i].Name < w.other[j].Name })
	return w
}

func osStorage(osName string, sizes map[string]StorageRelease, hosts map[string]*hardware.Host) StorageOS {
	block := StorageOS{Tracked: OSTracked(osName), Releases: []StorageRelease{}}
	switch osName {
	case OSFlatcar:
		block.Channel, block.Pin = viper.GetString(config.FlatcarChannel), state.FlatcarPin()
	case OSCoreOS:
		block.Channel = viper.GetString(config.CoreOSChannel)
	case OSBluefin:
		block.Pin = state.BluefinPin()
		block.Source = strings.TrimSpace(viper.GetString(config.BluefinOCI))
		if block.Source == "" {
			block.Source = viper.GetString(config.BluefinRepo)
		}
	}
	links := map[string][]string{}
	for _, link := range releaseLinks {
		if target, err := os.Readlink(config.DataPath(osName, link)); err == nil {
			v := filepath.Base(target)
			links[v] = append(links[v], link)
		}
	}
	versions := slices.SortedFunc(maps.Keys(sizes), func(a, b string) int { return compareVersions(b, a) })
	for _, version := range versions {
		rel := sizes[version]
		rel.Links, rel.Hosts = []string{}, StorageHosts{Running: []string{}, Pinned: []string{}}
		rel.Cached = ReleaseCached(osName, version)
		for _, link := range releaseLinks {
			if slices.Contains(links[version], link) {
				rel.Links = append(rel.Links, link)
			}
		}
		for _, mac := range slices.Sorted(maps.Keys(hosts)) {
			h := hosts[mac]
			if hostOS(h) != osName {
				continue
			}
			if h.Running == version {
				rel.Hosts.Running = append(rel.Hosts.Running, mac)
			}
			if h.TargetVersion == version {
				rel.Hosts.Pinned = append(rel.Hosts.Pinned, mac)
			}
		}
		rel.Retained = RetainedNone
		if block.Tracked {
			rel.Retained = retainedBy(rel)
		}
		block.Releases = append(block.Releases, rel)
		block.Bytes += rel.Bytes
	}
	return block
}

func retainedBy(rel StorageRelease) string {
	for _, link := range releaseLinks {
		if slices.Contains(rel.Links, link) {
			return link
		}
	}
	if len(rel.Hosts.Pinned) > 0 {
		return RetainedPinned
	}
	return RetainedNone
}

// dirSize sums the regular files below dir (symlinks are not followed,
// so a release is counted once however many names point at it) and
// reports the newest modification time among them.
func dirSize(dir string) (bytes int64, files int, modified time.Time) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		bytes += info.Size()
		files++
		if info.ModTime().After(modified) {
			modified = info.ModTime()
		}
		return nil
	})
	return bytes, files, modified.UTC()
}

func autopilotStorage(dir string) StorageAutopilot {
	var a StorageAutopilot
	if info, err := os.Stat(filepath.Join(dir, config.AutopilotStateFile)); err == nil {
		a.StateBytes = info.Size()
	}
	a.ReportsBytes, _, _ = dirSize(filepath.Join(dir, config.AutopilotReportsDir))
	return a
}

func assetKind(name string) string {
	lower := strings.ToLower(name)
	switch {
	case config.IsBootFile(name):
		return AssetIPXE
	case strings.Contains(lower, "shim") && strings.HasSuffix(lower, ".efi"):
		return AssetShim
	case strings.HasPrefix(lower, "grub") && strings.HasSuffix(lower, ".efi"):
		return AssetGrub
	case lower == "pxelinux.0" || strings.HasSuffix(lower, ".c32"):
		return AssetSyslinux
	}
	return AssetOther
}

func statfs(dir string) (total, free int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		slog.Debug("statfs failed", "dir", dir, "error", err)
		return 0, 0
	}
	bsize := int64(st.Bsize) //nolint:unconvert // Bsize is int64 on linux/amd64 but int32 on other ports
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize
}
