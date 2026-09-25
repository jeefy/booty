package config

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	CurrentFlatcarVersion = "currentFlatcarVersion"
	RemoteFlatcarVersion  = "remoteFlatcarVersion"
	FlatcarChannel        = "flatcarChannel"
	FlatcarVersion        = "flatcarVersion"
	CurrentCoreOSVersion  = "currentCoreOSVersion"
	RemoteCoreOSVersion   = "remoteCoreOSVersion"
	CoreOSChannel         = "coreOSChannel"
	IgnitionFile          = "ignitionFile"
	HardwareMap           = "hardwareMap"
	CoreOSArchitecture    = "coreOSArchitecture"
	FlatcarArchitecture   = "flatcarArchitecture"
	Debug                 = "debug"
	UpdateSchedule        = "updateSchedule"
	HttpPort              = "httpPort"
	DataDir               = "dataDir"
	FlatcarURL            = "flatcarURL"
	CoreOSURL             = "coreOSURL"
	ServerIP              = "serverIP"
	ServerHttpPort        = "serverHttpPort"
	JoinString            = "joinString"
	Updating              = "updating"
	TFTPBlockSize         = "tftpBlockSize"
	DepsPxelinuxURL       = "depsPxelinuxURL"
	DepsLdlinuxURL        = "depsLdlinuxURL"
	DepsUndionlyURL       = "depsUndionlyURL"
)

func LoadConfig(cmd *cobra.Command) {
	viper.SetDefault(Debug, false)
	viper.SetDefault(Updating, false)
	viper.SetDefault(FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/%s")
	viper.SetDefault(CoreOSURL, "https://builds.coreos.fedoraproject.org/prod/streams/%s/builds/%s/%s")
	// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/39.20231101.3.0/x86_64/fedora-coreos-39.20231101.3.0-live-kernel-x86_64
	// https://stable.release.flatcar-linux.net/amd64-usr/current/version.txt

	viper.SetDefault(DepsPxelinuxURL, "http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20230607/images/netboot/pxelinux.0")
	viper.SetDefault(DepsLdlinuxURL, "http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20230607/images/netboot/debian-installer/amd64/boot-screens/ldlinux.c32")
	viper.SetDefault(DepsUndionlyURL, "https://raw.githubusercontent.com/jeefy/booty/main/undionly.kpxe")
	viper.BindEnv(DepsPxelinuxURL, "DEPS_PXELINUX_URL")
	viper.BindEnv(DepsLdlinuxURL, "DEPS_LDLINUX_URL")
	viper.BindEnv(DepsUndionlyURL, "DEPS_UNDIONLY_URL")

	if file, err := os.Open(fmt.Sprintf("%s/version.txt", viper.GetString(DataDir))); err == nil {
		data, _ := godotenv.Parse(file)
		file.Close()
		if v, ok := data["FLATCAR_VERSION"]; ok && v != "" {
			viper.Set(CurrentFlatcarVersion, v)
			slog.Info("Local version found", "version", v)
		}
	} else if !os.IsNotExist(err) {
		slog.Error("Error retrieving existing local version", "error", err)
	}

	viper.BindEnv(FlatcarVersion, "FLATCAR_VERSION_PIN")

	// Load a Flatcar version pin persisted via the Web UI, unless one was
	// already supplied via the --flatcarVersion flag or FLATCAR_VERSION_PIN env.
	if viper.GetString(FlatcarVersion) == "" {
		if pin, err := os.ReadFile(FlatcarPinPath()); err == nil {
			if v := strings.TrimSpace(string(pin)); v != "" {
				viper.Set(FlatcarVersion, v)
				slog.Info("Loaded persisted Flatcar version pin", "version", v)
			}
		}
	}

	viper.BindEnv(IgnitionFile, "IGNITION_FILE")
	viper.SetDefault(IgnitionFile, "config/ignition.yaml")
	viper.BindEnv(HardwareMap, "HARDWARE_MAP")
	viper.SetDefault(HardwareMap, "hardware.json")
}

func DownloadFile(url string) error {
	slog.Info("Downloading", "url", url)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed for %s: HTTP %d", url, resp.StatusCode)
	}
	filename := fmt.Sprintf("%s/%s", viper.GetString(DataDir), path.Base(url))
	slog.Info("Creating", "filename", filename)

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	fileInfo, err := f.Stat()
	if err != nil {
		return err
	}
	slog.Info("Download completed", "url", url, "size_bytes", fileInfo.Size())

	return nil
}

// DownloadFileWithChecksum downloads url to a temporary file, verifies its
// SHA256 digest against expectedSHA256 (a lowercase hex string), and only
// moves the file to its final destination (DataDir/basename(url)) when the
// digest matches. The temporary file is always removed on failure.
func DownloadFileWithChecksum(url string, expectedSHA256 string) error {
	slog.Info("Downloading with checksum verification", "url", url)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed for %s: HTTP %d", url, resp.StatusCode)
	}

	finalPath := fmt.Sprintf("%s/%s", viper.GetString(DataDir), path.Base(url))
	tmpPath := finalPath + ".tmp"

	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	h := sha256.New()
	writer := io.MultiWriter(tmpFile, h)

	_, err = io.Copy(writer, resp.Body)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	actualSHA256 := hex.EncodeToString(h.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		os.Remove(tmpPath)
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", url, expectedSHA256, actualSHA256)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	slog.Info("Download and checksum verification completed", "url", url)
	return nil
}

// DownloadFileWithChecksumSHA512 downloads url to a temporary file, verifies
// its SHA512 digest against expectedSHA512 (a lowercase hex string), and only
// moves the file to its final destination (DataDir/basename(url)) when the
// digest matches. The temporary file is always removed on failure.
func DownloadFileWithChecksumSHA512(url string, expectedSHA512 string) error {
	slog.Info("Downloading with SHA512 checksum verification", "url", url)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed for %s: HTTP %d", url, resp.StatusCode)
	}

	finalPath := fmt.Sprintf("%s/%s", viper.GetString(DataDir), path.Base(url))
	tmpPath := finalPath + ".tmp"

	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	h := sha512.New()
	writer := io.MultiWriter(tmpFile, h)

	_, err = io.Copy(writer, resp.Body)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	actualSHA512 := hex.EncodeToString(h.Sum(nil))
	if actualSHA512 != expectedSHA512 {
		os.Remove(tmpPath)
		return fmt.Errorf("SHA512 checksum mismatch for %s: expected %s, got %s", url, expectedSHA512, actualSHA512)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	slog.Info("Download and SHA512 checksum verification completed", "url", url)
	return nil
}

func EnsureDeps() {
	DownloadFile(viper.GetString(DepsPxelinuxURL))
	DownloadFile(viper.GetString(DepsLdlinuxURL))
	DownloadFile(viper.GetString(DepsUndionlyURL))
}

// FlatcarPinPath returns the path to the file used to persist a Flatcar version
// pin set via the Web UI across restarts.
func FlatcarPinPath() string {
	return fmt.Sprintf("%s/flatcar_pin.txt", viper.GetString(DataDir))
}

// SetFlatcarPin updates the in-memory Flatcar version pin and persists it to
// disk so it survives restarts. An empty version clears the pin (returning the
// server to tracking the latest version on the configured channel).
func SetFlatcarPin(version string) error {
	version = strings.TrimSpace(version)
	viper.Set(FlatcarVersion, version)

	if version == "" {
		if err := os.Remove(FlatcarPinPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	return os.WriteFile(FlatcarPinPath(), []byte(version+"\n"), 0o644)
}
