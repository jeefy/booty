package config

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Viper keys. After LoadConfig returns viper is treated as read-only; all
// runtime state (current/remote versions, pin, update locks) lives in
// pkg/state.
const (
	FlatcarChannel      = "flatcarChannel"
	FlatcarVersion      = "flatcarVersion"
	CoreOSChannel       = "coreOSChannel"
	IgnitionFile        = "ignitionFile"
	HardwareMap         = "hardwareMap"
	CoreOSArchitecture  = "coreOSArchitecture"
	FlatcarArchitecture = "flatcarArchitecture"
	Debug               = "debug"
	UpdateSchedule      = "updateSchedule"
	HttpPort            = "httpPort"
	TFTPPort            = "tftpPort"
	TFTPBlockSize       = "tftpBlockSize"
	WebDir              = "webDir"
	DataDir             = "dataDir"
	FlatcarURL          = "flatcarURL"
	CoreOSURL           = "coreOSURL"
	ServerIP            = "serverIP"
	ServerHttpPort      = "serverHttpPort"
	JoinString          = "joinString"
	DepsPxelinuxURL     = "depsPxelinuxURL"
	DepsLdlinuxURL      = "depsLdlinuxURL"
	Version             = "version"
	Timestamp           = "timestamp"
)

// FlatcarPinFile is the file (relative to DataDir) that persists a Flatcar
// version pin set via the Web UI.
const FlatcarPinFile = "flatcar_pin.txt"

// MetadataClient is used for small metadata fetches (version.txt, streams
// JSON, DIGESTS files). It has a short overall timeout.
var MetadataClient = &http.Client{Timeout: 30 * time.Second}

// DownloadClient is used for large artifact downloads. Individual phases are
// bounded (dial, TLS, response headers) while the overall timeout is generous
// enough for multi-hundred-megabyte images on slow links.
var DownloadClient = &http.Client{
	Timeout: 60 * time.Minute,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	},
}

// LoadConfig installs defaults and environment bindings. Every flag can be
// set through a BOOTY_<FLAG> environment variable (e.g. BOOTY_HTTPPORT); the
// historical explicit names are kept for compatibility.
func LoadConfig() {
	viper.SetEnvPrefix("BOOTY")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	viper.SetDefault(Debug, false)
	viper.SetDefault(FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/%s")
	viper.SetDefault(CoreOSURL, "https://builds.coreos.fedoraproject.org/prod/streams/%s/builds/%s/%s")
	// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/39.20231101.3.0/x86_64/fedora-coreos-39.20231101.3.0-live-kernel-x86_64
	// https://stable.release.flatcar-linux.net/amd64-usr/current/version.txt

	viper.SetDefault(DepsPxelinuxURL, "http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20230607/images/netboot/pxelinux.0")
	viper.SetDefault(DepsLdlinuxURL, "http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20230607/images/netboot/debian-installer/amd64/boot-screens/ldlinux.c32")

	bindEnv(DepsPxelinuxURL, "DEPS_PXELINUX_URL")
	bindEnv(DepsLdlinuxURL, "DEPS_LDLINUX_URL")
	bindEnv(FlatcarVersion, "FLATCAR_VERSION_PIN")
	bindEnv(IgnitionFile, "IGNITION_FILE")
	bindEnv(HardwareMap, "HARDWARE_MAP")

	viper.SetDefault(IgnitionFile, "config/ignition.yaml")
	viper.SetDefault(HardwareMap, "hardware.json")
	viper.SetDefault(TFTPPort, 69)
	viper.SetDefault(TFTPBlockSize, 1468)
	viper.SetDefault(WebDir, "./web/dist")
}

func bindEnv(key, env string) {
	if err := viper.BindEnv(key, env); err != nil {
		slog.Warn("Could not bind environment variable", "key", key, "env", env, "error", err)
	}
}

// DataPath joins elem onto the configured data directory.
func DataPath(elem ...string) string {
	return filepath.Join(append([]string{viper.GetString(DataDir)}, elem...)...)
}

// FlatcarPinPath returns the path to the file used to persist a Flatcar version
// pin set via the Web UI across restarts.
func FlatcarPinPath() string {
	return DataPath(FlatcarPinFile)
}

// ServerHostPort returns the host[:port] clients should use to reach the HTTP
// server, omitting the port when it is the default 80.
func ServerHostPort() string {
	host := viper.GetString(ServerIP)
	if port := viper.GetInt(ServerHttpPort); port != 80 {
		return net.JoinHostPort(host, fmt.Sprint(port))
	}
	return host
}

// CloseQuietly closes c and logs (at debug level) any error. Use it for
// read-only handles where a close failure carries no information for the
// caller.
func CloseQuietly(c io.Closer, what string) {
	if err := c.Close(); err != nil {
		slog.Debug("Close failed", "what", what, "error", err)
	}
}

// CleanRelPath validates that p is a clean, relative path that cannot escape
// the directory it is resolved against. It returns the cleaned path.
func CleanRelPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("path contains NUL byte")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("path must be relative")
	}
	clean := filepath.Clean(p)
	if clean == "." {
		return "", fmt.Errorf("path must name a file")
	}
	for _, seg := range strings.Split(filepath.ToSlash(clean), "/") {
		if seg == ".." {
			return "", fmt.Errorf("path must not contain '..'")
		}
	}
	return clean, nil
}

// EnsureDeps makes sure the legacy PXE bootloader files (pxelinux.0 and
// ldlinux.c32) are present in DataDir, downloading them when missing. Failures
// are not fatal: Booty keeps working for iPXE clients (undionly.kpxe is
// embedded in the binary) and only legacy PXE becomes unavailable.
func EnsureDeps(ctx context.Context) {
	legacyOK := true
	for _, key := range []string{DepsPxelinuxURL, DepsLdlinuxURL} {
		url := viper.GetString(key)
		if url == "" {
			continue
		}
		dest := DataPath(path.Base(url))
		if _, err := os.Stat(dest); err == nil {
			slog.Debug("Dependency already present", "path", dest)
			continue
		}
		if err := Download(ctx, DownloadClient, url, dest, 0, ""); err != nil {
			slog.Debug("Dependency download failed", "url", url, "error", err)
			legacyOK = false
		}
	}
	if !legacyOK {
		slog.Warn("Legacy PXE unavailable: could not fetch pxelinux.0/ldlinux.c32 (iPXE via undionly.kpxe still works)")
	}
}

// EnsureFile writes data to path only when the file does not exist yet.
func EnsureFile(path string, data []byte, perm os.FileMode) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return WriteFileAtomic(path, data, perm)
}
