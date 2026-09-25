package hardware

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

type Host struct {
	MAC           string `json:"mac"`
	Hostname      string `json:"hostname"`
	IP            string `json:"ip"`
	Booted        string `json:"booted"`
	IgnitionFile  string `json:"ignitionFile,omitempty"`
	OS            string `json:"os,omitempty"`
	OSTreeImage   string `json:"ostreeImage,omitempty"`
	DoInstall     bool   `json:"doInstall,omitempty"`
	Running       string `json:"running"`
	LastCheck     string `json:"lastCheck"`
	RebootPending bool   `json:"rebootPending"`
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
	ErrNotFound   = errors.New("host not registered")
	ErrInvalidMAC = errors.New("invalid MAC address")
	ErrNoDatabase = errors.New("hardware database not loaded")
)

var validOS = map[string]bool{"flatcar": true, "coreos": true, "ublue": true}

func IsValidOS(os string) bool {
	return validOS[os]
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

// DB is an in-memory host database backed by a JSON file on disk. Every write
// is persisted atomically; reads pick up external edits of the file by
// comparing its size and mtime with the last load.
type DB struct {
	mu      sync.Mutex
	path    string
	hosts   map[string]*Host
	unknown map[string]*unknownEntry
	modTime time.Time
	size    int64
	now     func() time.Time
}

// Open loads the host database at path, creating an empty one if missing.
func Open(path string) (*DB, error) {
	db := &DB{
		path:    path,
		hosts:   map[string]*Host{},
		unknown: map[string]*unknownEntry{},
		now:     time.Now,
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
	}
}

func (db *DB) persist() error {
	data, err := json.MarshalIndent(db.hosts, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding hardware map: %w", err)
	}
	if err := config.WriteFileAtomic(db.path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing hardware map %s: %w", db.path, err)
	}
	return db.recordStat()
}

func copyHost(h *Host) *Host {
	c := *h
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

// Update applies fn to the registered host for mac and persists the result.
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
	fn(h)
	h.MAC = mac
	if err := db.persist(); err != nil {
		return nil, err
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
	defaultDB = db
	defaultMu.Unlock()
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

func Snapshot() BootyData {
	db := current()
	if db == nil {
		return BootyData{Hosts: map[string]*Host{}, UnknownHosts: map[string]*UnknownHost{}}
	}
	return db.Snapshot()
}
