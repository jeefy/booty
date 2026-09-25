package state

import (
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
	OSTreeSyncMu    sync.Mutex
)

type runtimeState struct {
	mu                    sync.RWMutex
	currentFlatcarVersion string
	remoteFlatcarVersion  string
	currentCoreOSVersion  string
	remoteCoreOSVersion   string
	flatcarPin            string
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
