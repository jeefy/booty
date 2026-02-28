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

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	CurrentFlatcarVersion = "currentFlatcarVersion"
	RemoteFlatcarVersion  = "remoteFlatcarVersion"
	FlatcarChannel        = "flatcarChannel"
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
)

func LoadConfig(cmd *cobra.Command) {
	viper.SetDefault(Debug, false)
	viper.SetDefault(Updating, false)
	viper.SetDefault(FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/current")
	viper.SetDefault(CoreOSURL, "https://builds.coreos.fedoraproject.org/prod/streams/%s/builds/%s/%s")
	// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/39.20231101.3.0/x86_64/fedora-coreos-39.20231101.3.0-live-kernel-x86_64
	// https://stable.release.flatcar-linux.net/amd64-usr/current/version.txt

	if file, err := os.Open(fmt.Sprintf("%s/version.txt", viper.GetString(DataDir))); err == nil {
		data, _ := godotenv.Parse(file)
		if _, ok := data["FLATCAR_VERSION"]; !ok {
			viper.Set(CurrentFlatcarVersion, data["FLATCAR_VERSION"])
			slog.Info("Local version found", "version", data["FLATCAR_VERSION"])
		}
	} else {
		slog.Error("Error retrieving existing local version", "error", err)
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
	DownloadFile("http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20230607/images/netboot/pxelinux.0")
	DownloadFile("http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20230607/images/netboot/debian-installer/amd64/boot-screens/ldlinux.c32")
	DownloadFile("https://raw.githubusercontent.com/jeefy/booty/main/undionly.kpxe")
}
