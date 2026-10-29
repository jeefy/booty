package hardware

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

type Host struct {
	MAC             string `json:"mac"`
	Hostname        string `json:"hostname"`
	IP              string `json:"ip"`
	Booted          string `json:"booted"`
	IgnitionFile    string `json:"ignitionFile,omitempty"`
	OS              string `json:"os,omitempty"`
	OSTreeImage     string `json:"ostreeImage,omitempty"`
	InstallDisk     string `json:"installDisk,omitempty"`
	DoInstall       bool   `json:"doInstall,omitempty"`
	InstallServedAt string `json:"installServedAt,omitempty"`
	// Role is the host's place in the cluster: RoleControlPlane or
	// RoleWorker; empty means worker.
	Role string `json:"role,omitempty"`
	// StateDisk, Extensions and Mode are Bluefin Server only (see
	// bluefin.go): the disk Ignition keeps /var on, the opt-in sysexts and
	// whether the host boots diskless or from its installed disk.
	StateDisk  string   `json:"stateDisk,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	// NetbootPlatform is the firmware the last Bluefin netboot came
	// through: PlatformEFI (UKI) or PlatformPCBIOS (kernel and initrd).
	NetbootPlatform string `json:"netbootPlatform,omitempty"`
	// NetbootVersion is the release whose netboot UKI or kernel the host
	// fetched last: what its initrd is booting right now, which may differ
	// from its target while the autopilot moves it (boot loop, rollback).
	NetbootVersion string `json:"netbootVersion,omitempty"`
	// SecureBoot records that the host's last /booty.ipxe fetch arrived
	// through the Secure Boot path: the signed iPXE that only UEFI HTTP Boot
	// hands out (its autoexec adds sb=1). It is not a firmware attestation;
	// a plain PXE fetch clears it again.
	SecureBoot bool `json:"secureBoot,omitempty"`
	// TargetVersion is the cached release of the host's OS Booty serves
	// this MAC; empty means the fleet target (the OS's current release).
	TargetVersion string `json:"targetVersion,omitempty"`
	// Canary marks the host the autopilot rolls a new Bluefin release to
	// first under --autopilot=full (and retries a TIMEOUT release on).
	Canary bool `json:"canary,omitempty"`
	// Autopilot is the controller's read-only summary of the host's
	// current episode, kept on the record so /hosts and the UI show it.
	Autopilot *HostAutopilot `json:"autopilot,omitempty"`
	// Power is the power tracker's read-only summary of the host (state,
	// request in flight, last probe); pkg/power owns it.
	Power         *HostPower `json:"power,omitempty"`
	Running       string     `json:"running"`
	LastCheck     string     `json:"lastCheck"`
	RebootPending bool       `json:"rebootPending"`
	// Health is the last report from the node's booty-health.service.
	Health *Health `json:"health,omitempty"`
}

// HostAutopilot is the autopilot's view of a host: the state of its
// current episode (AutopilotIdle when none), the attempt it is on, the last
// failure class, when it entered that state, the release under test and
// whether the controller set the host's targetVersion (Pinned). The
// controller owns it; /register echoes it back unchanged.
type HostAutopilot struct {
	State   string `json:"state"`
	Attempt int    `json:"attempt,omitempty"`
	Class   string `json:"class,omitempty"`
	Since   string `json:"since,omitempty"`
	Release string `json:"release,omitempty"`
	Target  string `json:"target,omitempty"`
	Pinned  bool   `json:"pinned,omitempty"`
}

// Per-host autopilot states, as the plan names them.
const (
	AutopilotIdle       = "idle"
	AutopilotRolling    = "rolling"
	AutopilotGating     = "gating"
	AutopilotRetrying   = "retrying"
	AutopilotRolledBack = "rolled-back"
	AutopilotNeedsHands = "needs-hands"
)

// Equal reports whether two summaries carry the same values.
func (a *HostAutopilot) Equal(b *HostAutopilot) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

type UnknownHost struct {
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
	Count     int    `json:"count"`
}

type BootyData struct {
	Hosts        map[string]*Host        `json:"hosts"`
	UnknownHosts map[string]*UnknownHost `json:"unknownHosts"`
}

const MaxUnknownHosts = 512

var (
	ErrNotFound       = errors.New("host not registered")
	ErrInvalidMAC     = errors.New("invalid MAC address")
	ErrNoDatabase     = errors.New("hardware database not loaded")
	ErrInvalidInstall = errors.New("invalid installDisk")
	ErrInvalidRole    = errors.New("invalid role")
)

// Host roles in the cluster Booty provisions. An empty Role is a worker.
const (
	RoleControlPlane = "control-plane"
	RoleWorker       = "worker"
)

// ValidateRole accepts an empty role, RoleControlPlane or RoleWorker.
func ValidateRole(role string) error {
	switch role {
	case "", RoleControlPlane, RoleWorker:
		return nil
	}
	return fmt.Errorf("%w %q: must be empty, %q or %q", ErrInvalidRole, role, RoleControlPlane, RoleWorker)
}

// IsControlPlane reports whether h is registered as a control-plane host.
func (h *Host) IsControlPlane() bool {
	return h != nil && h.Role == RoleControlPlane
}

// ValidOSNames are the operating systems a host can be registered with.
var ValidOSNames = []string{"flatcar", "coreos", "bluefin"}

func IsValidOS(os string) bool {
	return slices.Contains(ValidOSNames, os)
}

// ValidOSList renders ValidOSNames for error messages.
func ValidOSList() string {
	return strings.Join(ValidOSNames, ", ")
}

// ValidateInstallDisk accepts an empty value or an absolute /dev path
// without whitespace or ".." segments; it ends up verbatim on a kernel
// command line.
func ValidateInstallDisk(disk string) error {
	return validateDevicePath(ErrInvalidInstall, disk)
}

func validateDevicePath(kind error, disk string) error {
	if disk == "" {
		return nil
	}
	switch {
	case !strings.HasPrefix(disk, "/dev/"), len(disk) == len("/dev/"):
		return fmt.Errorf("%w %q: must start with /dev/", kind, disk)
	case strings.ContainsAny(disk, " \t\r\n\x00"):
		return fmt.Errorf("%w %q: must not contain whitespace", kind, disk)
	case strings.Contains(disk, ".."):
		return fmt.Errorf("%w %q: must not contain '..'", kind, disk)
	}
	return nil
}

// NormalizeMAC parses s with net.ParseMAC and returns the canonical
// lowercase, colon-separated form.
func NormalizeMAC(s string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrInvalidMAC, s)
	}
	return hw.String(), nil
}

type unknownEntry struct {
	ip        string
	firstSeen time.Time
	lastSeen  time.Time
	count     int
}

// PersistInterval bounds how long a batched change (Update, MarkBooted,
// SetSecureBoot) may sit in memory before the background flush writes it.
const PersistInterval = 2 * time.Second

// DB is an in-memory host database backed by a JSON file on disk.
//
// Persistence: operator-visible changes (Put, Delete) and one-shot
// transitions made through Update (doInstall, mode, installServedAt,
// targetVersion, the autopilot pin) are written synchronously, together
// with any pending batched change. Runtime bookkeeping (other Update
// changes, MarkBooted, SetSecureBoot) only marks the map dirty;
// a single flush writes it at most PersistInterval later, and Flush/Close
// write it at once. Every write is atomic and records the file's size and
// mtime, so our own writes never trigger a reload.
//
// External edits: reads compare the file's size and mtime with the last
// load or write. A change on disk is an operator edit and wins: the file is
// reloaded. If batched changes were still pending they are discarded with
// a warning naming the hosts they touched (the next boot, check or report
// records them again); they are never written over the edit.
type DB struct {
	mu      sync.Mutex
	path    string
	hosts   map[string]*Host
	unknown map[string]*unknownEntry
	modTime time.Time
	size    int64
	now     func() time.Time

	// dirty names the hosts with batched changes not yet on disk.
	dirty    map[string]bool
	flushing *time.Timer
	interval time.Duration
	closed   bool
}

// Open loads the host database at path, creating an empty one if missing.
func Open(path string) (*DB, error) {
	db := &DB{
		path:     path,
		hosts:    map[string]*Host{},
		unknown:  map[string]*unknownEntry{},
		now:      time.Now,
		dirty:    map[string]bool{},
		interval: PersistInterval,
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := config.WriteFileAtomic(path, []byte("{}\n"), 0o644); err != nil {
			return nil, fmt.Errorf("creating hardware map %s: %w", path, err)
		}
		slog.Info("Created empty hardware map", "path", path)
	} else if err != nil {
		return nil, fmt.Errorf("stat hardware map %s: %w", path, err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.reload(); err != nil {
		return nil, err
	}
	return db, nil
}

func (db *DB) reload() error {
	data, err := os.ReadFile(db.path)
	if err != nil {
		return fmt.Errorf("reading hardware map %s: %w", db.path, err)
	}
	fresh := map[string]*Host{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &fresh); err != nil {
			return fmt.Errorf("parsing hardware map %s: %w", db.path, err)
		}
	}
	normalized := make(map[string]*Host, len(fresh))
	for key, h := range fresh {
		if h == nil {
			continue
		}
		mac, err := NormalizeMAC(key)
		if err != nil {
			slog.Warn("Hardware map contains an unparseable MAC key; keeping as-is", "key", key)
			mac = key
		}
		h.MAC = mac
		normalized[mac] = h
	}
	db.hosts = normalized
	for mac := range db.unknown {
		if _, registered := db.hosts[mac]; registered {
			delete(db.unknown, mac)
		}
	}
	return db.recordStat()
}

func (db *DB) recordStat() error {
	info, err := os.Stat(db.path)
	if err != nil {
		return fmt.Errorf("stat hardware map %s: %w", db.path, err)
	}
	db.modTime = info.ModTime()
	db.size = info.Size()
	return nil
}

func (db *DB) maybeReload() {
	info, err := os.Stat(db.path)
	if err != nil {
		slog.Warn("Hardware map missing or unreadable; using in-memory copy", "path", db.path, "error", err)
		return
	}
	if info.ModTime().Equal(db.modTime) && info.Size() == db.size {
		return
	}
	slog.Info("Hardware map changed on disk, reloading", "path", db.path)
	if err := db.reload(); err != nil {
		slog.Error("Reloading hardware map failed; keeping in-memory copy", "error", err)
		return
	}
	if len(db.dirty) > 0 {
		macs := make([]string, 0, len(db.dirty))
		for mac := range db.dirty {
			macs = append(macs, mac)
		}
		slices.Sort(macs)
		slog.Warn("Hardware map edited on disk while runtime updates were pending; the edit wins and the pending updates are dropped", "path", db.path, "hosts", macs)
		clear(db.dirty)
	}
}

// markDirty records a batched change of mac and schedules the flush. After
// Close there is no background flush, so the change is written at once.
func (db *DB) markDirty(mac string) {
	db.dirty[mac] = true
	if db.closed {
		if err := db.persist(); err != nil {
			slog.Error("Persisting hardware map after close failed", "mac", mac, "error", err)
		}
		return
	}
	db.scheduleFlush()
}

func (db *DB) scheduleFlush() {
	if db.flushing != nil || db.closed {
		return
	}
	db.flushing = time.AfterFunc(db.interval, db.backgroundFlush)
}

func (db *DB) backgroundFlush() {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.flushing = nil
	if err := db.flushLocked(); err != nil {
		slog.Error("Persisting hardware map failed; retrying", "error", err)
		if !db.closed {
			db.flushing = time.AfterFunc(db.interval, db.backgroundFlush)
		}
	}
}

// flushLocked writes pending batched changes. An external edit seen first
// wins (see DB), which leaves nothing to write.
func (db *DB) flushLocked() error {
	if len(db.dirty) == 0 {
		return nil
	}
	db.maybeReload()
	if len(db.dirty) == 0 {
		return nil
	}
	return db.persist()
}

// Flush writes pending batched changes now.
func (db *DB) Flush() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.flushLocked()
}

// Close stops the background flush and writes pending changes. Later
// changes are written synchronously.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.closed = true
	if db.flushing != nil {
		db.flushing.Stop()
		db.flushing = nil
	}
	return db.flushLocked()
}

func (db *DB) persist() error {
	data, err := json.MarshalIndent(db.hosts, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding hardware map: %w", err)
	}
	if err := config.WriteFileAtomic(db.path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing hardware map %s: %w", db.path, err)
	}
	clear(db.dirty)
	return db.recordStat()
}

// copyHost deep-copies h: no slice or pointer is shared with the result.
func copyHost(h *Host) *Host {
	c := *h
	c.Extensions = slices.Clone(h.Extensions)
	if h.Autopilot != nil {
		a := *h.Autopilot
		c.Autopilot = &a
	}
	if h.Power != nil {
		p := *h.Power
		c.Power = &p
	}
	if h.Health != nil {
		hl := *h.Health
		hl.FailedUnits = slices.Clone(h.Health.FailedUnits)
		hl.JournalErrors = slices.Clone(h.Health.JournalErrors)
		c.Health = &hl
	}
	return &c
}

// Get returns a copy of the registered host for mac. It has no side effects.
func (db *DB) Get(mac string) (*Host, bool) {
	mac, err := NormalizeMAC(mac)
	if err != nil {
		return nil, false
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.maybeReload()
	h, ok := db.hosts[mac]
	if !ok {
		return nil, false
	}
	return copyHost(h), true
}

// Observe records a boot attempt from an unregistered MAC. Registered MACs
// and unparseable values are ignored. The table is bounded to
// MaxUnknownHosts entries, evicting the least recently seen one when full.
func (db *DB) Observe(mac, ip string) {
	mac, err := NormalizeMAC(mac)
	if err != nil {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.maybeReload()
	if _, registered := db.hosts[mac]; registered {
		return
	}
	now := db.now()
	if e, ok := db.unknown[mac]; ok {
		e.lastSeen = now
		e.count++
		if ip != "" {
			e.ip = ip
		}
		return
	}
	if len(db.unknown) >= MaxUnknownHosts {
		db.evictOldestUnknown()
	}
	db.unknown[mac] = &unknownEntry{ip: ip, firstSeen: now, lastSeen: now, count: 1}
}

func (db *DB) evictOldestUnknown() {
	var oldestMAC string
	var oldest time.Time
	for mac, e := range db.unknown {
		if oldestMAC == "" || e.lastSeen.Before(oldest) {
			oldestMAC, oldest = mac, e.lastSeen
		}
	}
	if oldestMAC != "" {
		delete(db.unknown, oldestMAC)
	}
}

// Put registers or updates a host. The MAC is normalized in place.
func (db *DB) Put(h Host) (*Host, error) {
	mac, err := NormalizeMAC(h.MAC)
	if err != nil {
		return nil, err
	}
	h.MAC = mac
	db.mu.Lock()
	defer db.mu.Unlock()
	db.maybeReload()
	db.hosts[mac] = copyHost(&h)
	delete(db.unknown, mac)
	if err := db.persist(); err != nil {
		return nil, err
	}
	return copyHost(&h), nil
}

// oneShotChanged reports whether an update changed a field that drives a
// one-shot transition (an install, the boot mode, the served release, the
// autopilot pin, the power request and cordon). Losing such a change to the
// batch window, a crash or an external edit can reinstall a host and wipe
// its disk, repeat a reboot/shutdown or leave a node cordoned, so it is
// written synchronously.
func oneShotChanged(before, after *Host) bool {
	return before.DoInstall != after.DoInstall ||
		before.Mode != after.Mode ||
		before.InstallServedAt != after.InstallServedAt ||
		before.TargetVersion != after.TargetVersion ||
		autopilotPinned(before) != autopilotPinned(after) ||
		powerCordoned(before) != powerCordoned(after) ||
		powerRequest(before) != powerRequest(after)
}

func powerCordoned(h *Host) bool {
	return h.Power != nil && h.Power.Cordoned
}

func powerRequest(h *Host) string {
	if h.Power == nil {
		return ""
	}
	return h.Power.Request
}

func autopilotPinned(h *Host) bool {
	return h.Autopilot != nil && h.Autopilot.Pinned
}

// Update applies fn to the registered host for mac. A change to a one-shot
// field (see oneShotChanged) is persisted before Update returns. A write
// failure is returned together with the updated host: the change stays
// applied in memory and the background flush retries the write, so an
// install that was cleared is not served again. Any other change is
// persisted in a
// batch (see DB). An fn that changes nothing writes nothing.
func (db *DB) Update(mac string, fn func(*Host)) (*Host, error) {
	mac, err := NormalizeMAC(mac)
	if err != nil {
		return nil, err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.maybeReload()
	h, ok := db.hosts[mac]
	if !ok {
		return nil, ErrNotFound
	}
	before := copyHost(h)
	fn(h)
	h.MAC = mac
	switch {
	case oneShotChanged(before, h):
		if err := db.persist(); err != nil {
			db.dirty[mac] = true
			db.scheduleFlush()
			return copyHost(h), err
		}
	case !reflect.DeepEqual(before, h):
		db.markDirty(mac)
	}
	return copyHost(h), nil
}

func (db *DB) Delete(mac string) error {
	mac, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.maybeReload()
	if _, ok := db.hosts[mac]; !ok {
		return ErrNotFound
	}
	delete(db.hosts, mac)
	return db.persist()
}

// MarkBooted records that mac fetched its ignition config from ip at time at.
func (db *DB) MarkBooted(mac, ip string, at time.Time) error {
	_, err := db.Update(mac, func(h *Host) {
		h.Booted = at.UTC().Format(time.RFC3339)
		if ip != "" {
			h.IP = ip
		}
	})
	return err
}

// SetSecureBoot records whether mac's last boot-script fetch came through
// the Secure Boot path. Only a change is recorded (batched, see DB), so the
// per-boot call does not rewrite the map. changed reports a change.
func (db *DB) SetSecureBoot(mac string, secureBoot bool) (changed bool, err error) {
	mac, err = NormalizeMAC(mac)
	if err != nil {
		return false, err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.maybeReload()
	h, ok := db.hosts[mac]
	if !ok {
		return false, ErrNotFound
	}
	if h.SecureBoot == secureBoot {
		return false, nil
	}
	h.SecureBoot = secureBoot
	db.markDirty(mac)
	return true, nil
}

// Snapshot returns a deep copy of the registered and unknown hosts.
func (db *DB) Snapshot() BootyData {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.maybeReload()
	out := BootyData{
		Hosts:        make(map[string]*Host, len(db.hosts)),
		UnknownHosts: make(map[string]*UnknownHost, len(db.unknown)),
	}
	for mac, h := range db.hosts {
		out.Hosts[mac] = copyHost(h)
	}
	for mac, e := range db.unknown {
		out.UnknownHosts[mac] = &UnknownHost{
			MAC:       mac,
			IP:        e.ip,
			FirstSeen: e.firstSeen.UTC().Format(time.RFC3339),
			LastSeen:  e.lastSeen.UTC().Format(time.RFC3339),
			Count:     e.count,
		}
	}
	return out
}

var (
	defaultMu sync.RWMutex
	defaultDB *DB
)

// Load opens the hardware map configured via viper as the process-wide
// default database.
func Load() error {
	db, err := Open(config.DataPath(viper.GetString(config.HardwareMap)))
	if err != nil {
		return err
	}
	defaultMu.Lock()
	old := defaultDB
	defaultDB = db
	defaultMu.Unlock()
	if old != nil {
		if err := old.Close(); err != nil {
			slog.Error("Flushing the previous hardware map failed", "path", old.path, "error", err)
		}
	}
	slog.Info("Hardware map loaded", "path", db.path, "hosts", len(db.hosts))
	return nil
}

func current() *DB {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultDB
}

func Get(mac string) (*Host, bool) {
	db := current()
	if db == nil {
		return nil, false
	}
	return db.Get(mac)
}

func Observe(mac, ip string) {
	if db := current(); db != nil {
		db.Observe(mac, ip)
	}
}

func Put(h Host) (*Host, error) {
	db := current()
	if db == nil {
		return nil, ErrNoDatabase
	}
	return db.Put(h)
}

func Update(mac string, fn func(*Host)) (*Host, error) {
	db := current()
	if db == nil {
		return nil, ErrNoDatabase
	}
	return db.Update(mac, fn)
}

func Delete(mac string) error {
	db := current()
	if db == nil {
		return ErrNoDatabase
	}
	return db.Delete(mac)
}

func MarkBooted(mac, ip string, at time.Time) error {
	db := current()
	if db == nil {
		return ErrNoDatabase
	}
	return db.MarkBooted(mac, ip, at)
}

func SetSecureBoot(mac string, secureBoot bool) (bool, error) {
	db := current()
	if db == nil {
		return false, ErrNoDatabase
	}
	return db.SetSecureBoot(mac, secureBoot)
}

// Flush writes the default database's pending changes; a no-op when none
// is loaded.
func Flush() error {
	if db := current(); db != nil {
		return db.Flush()
	}
	return nil
}

// Close stops the default database's background flush and writes its
// pending changes. Call it on shutdown.
func Close() error {
	if db := current(); db != nil {
		return db.Close()
	}
	return nil
}

func Snapshot() BootyData {
	db := current()
	if db == nil {
		return BootyData{Hosts: map[string]*Host{}, UnknownHosts: map[string]*UnknownHost{}}
	}
	return db.Snapshot()
}
