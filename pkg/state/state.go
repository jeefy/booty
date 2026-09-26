package state

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/jeefy/booty/pkg/config"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

var (
	FlatcarUpdateMu sync.Mutex
	CoreOSUpdateMu  sync.Mutex
	BluefinUpdateMu sync.Mutex
	OSTreeSyncMu    sync.Mutex
)

type runtimeState struct {
	mu                    sync.RWMutex
	currentFlatcarVersion string
	remoteFlatcarVersion  string
	currentCoreOSVersion  string
	remoteCoreOSVersion   string
	currentBluefinVersion string
	remoteBluefinVersion  string
	flatcarPin            string
	bluefinPin            string
}

var s runtimeState

// Init seeds the runtime state from disk: the Flatcar version recorded in
// DataDir/version.txt and the pin from --flatcarVersion/FLATCAR_VERSION_PIN
// or, failing that, the pin persisted by the Web UI.
func Init() {
	if v := LoadLocalFlatcarVersion(); v != "" {
		SetCurrentFlatcarVersion(v)
		slog.Info("Local Flatcar version found", "version", v)
	}

	pin := strings.TrimSpace(viper.GetString(config.FlatcarVersion))
	if pin == "" {
		if data, err := os.ReadFile(config.FlatcarPinPath()); err == nil {
			pin = strings.TrimSpace(string(data))
			if pin != "" {
				slog.Info("Loaded persisted Flatcar version pin", "version", pin)
			}
		} else if !os.IsNotExist(err) {
			slog.Error("Error reading persisted Flatcar version pin", "error", err)
		}
	} else {
		slog.Info("Flatcar version pinned via flag/env", "version", pin)
	}
	s.mu.Lock()
	s.flatcarPin = pin
	s.mu.Unlock()

	if v := LoadLocalBluefinVersion(); v != "" {
		SetCurrentBluefinVersion(v)
		slog.Info("Local Bluefin version found", "version", v)
	}
	bluefinPin := strings.TrimSpace(viper.GetString(config.BluefinVersion))
	if bluefinPin != "" {
		slog.Info("Bluefin version pinned via flag/env", "version", bluefinPin)
	}
	s.mu.Lock()
	s.bluefinPin = bluefinPin
	s.mu.Unlock()

	LoadClusterReady()
}

// LoadLocalBluefinVersion reads the version recorded in
// DataDir/bluefin/current/manifest.json; "" when absent or unparseable.
func LoadLocalBluefinVersion() string {
	path := config.BluefinCurrentManifestPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("Error reading local Bluefin manifest", "path", path, "error", err)
		}
		return ""
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		slog.Warn("Local Bluefin manifest is not parseable", "path", path, "error", err)
		return ""
	}
	return strings.TrimSpace(m.Version)
}

// LoadLocalFlatcarVersion parses FLATCAR_VERSION from DataDir/version.txt.
// It returns "" when the file is missing or does not carry the key.
func LoadLocalFlatcarVersion() string {
	path := config.DataPath("version.txt")
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("Error reading local version.txt", "path", path, "error", err)
		}
		return ""
	}
	defer config.CloseQuietly(f, path)
	data, err := godotenv.Parse(f)
	if err != nil {
		slog.Warn("Local version.txt is not parseable", "path", path, "error", err)
		return ""
	}
	return data["FLATCAR_VERSION"]
}

func CurrentFlatcarVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentFlatcarVersion
}

func SetCurrentFlatcarVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentFlatcarVersion = v
}

func RemoteFlatcarVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remoteFlatcarVersion
}

func SetRemoteFlatcarVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteFlatcarVersion = v
}

func CurrentCoreOSVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentCoreOSVersion
}

func SetCurrentCoreOSVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentCoreOSVersion = v
}

func RemoteCoreOSVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remoteCoreOSVersion
}

func SetRemoteCoreOSVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteCoreOSVersion = v
}

func FlatcarPin() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.flatcarPin
}

// SetFlatcarPin updates the in-memory Flatcar version pin and persists it to
// disk so it survives restarts. An empty version clears the pin.
func SetFlatcarPin(version string) error {
	version = strings.TrimSpace(version)

	if version == "" {
		if err := os.Remove(config.FlatcarPinPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := config.WriteFileAtomic(config.FlatcarPinPath(), []byte(version+"\n"), 0o644); err != nil {
		return err
	}

	s.mu.Lock()
	s.flatcarPin = version
	s.mu.Unlock()
	return nil
}

func CurrentBluefinVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentBluefinVersion
}

func SetCurrentBluefinVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentBluefinVersion = v
}

func RemoteBluefinVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remoteBluefinVersion
}

func SetRemoteBluefinVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteBluefinVersion = v
}

// BluefinPin is the --bluefinVersion pin; it is flag/env only and has no
// persisted counterpart.
func BluefinPin() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bluefinPin
}
