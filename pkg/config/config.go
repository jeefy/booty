package config

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
	JoinStringFile      = "joinStringFile"
	KubeadmJoin         = "kubeadmJoin"
	JoinTokenTTL        = "joinTokenTTL"
	Profile             = "profile"
	K8sVersion          = "k8sVersion"
	CNIVersion          = "cniVersion"
	CrictlVersion       = "crictlVersion"
	ContainerdDisk      = "containerdDisk"
	KubeletUnitsURL     = "kubeletUnitsURL"
	OCIGC               = "ociGC"
	OCIGCEmpty          = "ociGCEmpty"
	DoInstallClearOn    = "doInstallClearOn"
	Builtin             = "builtin"
	SSHAuthorizedKeys   = "sshAuthorizedKeys"
	SSHAuthorizedKeysFl = "sshAuthorizedKeysFile"
	ProxyDHCP           = "proxyDHCP"
	ProxyDHCPListen     = "proxyDHCPListen"
	ProxyDHCPRelay      = "proxyDHCPRelay"
	ProxyDHCPPorts      = "proxyDHCPPorts"
	Version             = "version"
	Timestamp           = "timestamp"
	AutoRegister        = "autoRegister"
	HostnameTemplate    = "hostnameTemplate"
)

// DefaultHostnameTemplate names auto-registered hosts after the last three
// bytes of their MAC.
const DefaultHostnameTemplate = "node-{{ .MACSuffix }}"

// BootFileNames are the iPXE binaries embedded in the Booty binary and served
// at the root of the TFTP namespace and under /boot/ over HTTP:
// undionly.kpxe for BIOS clients, ipxe.efi and snponly.efi for x86-64 UEFI.
var BootFileNames = []string{"undionly.kpxe", "ipxe.efi", "snponly.efi"}

// IsBootFile reports whether name is one of BootFileNames.
func IsBootFile(name string) bool {
	return slices.Contains(BootFileNames, name)
}

// FlatcarPinFile is the file (relative to DataDir) that persists a Flatcar
// version pin set via the Web UI.
const FlatcarPinFile = "flatcar_pin.txt"

// DefaultIgnitionFile is the Butane template (relative to DataDir) used when
// neither --ignitionFile nor a per-host ignitionFile is set. When it does
// not exist Booty renders an embedded minimal template instead.
const DefaultIgnitionFile = "config/ignition.yaml"

// DefaultBuiltin lists the fragments Booty merges into every registered
// host's Ignition config unless --builtin says otherwise.
const DefaultBuiltin = "hostname,update,booted,sshkeys"

// Values for DoInstallClearOn: clear a host's pending doInstall when it
// fetches its Ignition config, or only once it POSTs /booted.
const (
	ClearOnIgnition = "ignition"
	ClearOnBooted   = "booted"
)

// ValidateDoInstallClearOn rejects anything but the two known modes.
func ValidateDoInstallClearOn(v string) error {
	switch v {
	case ClearOnIgnition, ClearOnBooted:
		return nil
	}
	return fmt.Errorf("invalid --%s %q: must be %q or %q", DoInstallClearOn, v, ClearOnIgnition, ClearOnBooted)
}

// Values for KubeadmJoin: hand out --joinString/--joinStringFile as-is, or
// mint a short-lived bootstrap token per boot through the Kubernetes API.
const (
	KubeadmJoinStatic = "static"
	KubeadmJoinAuto   = "auto"
)

// ValidateKubeadmJoin rejects anything but the two known modes.
func ValidateKubeadmJoin(v string) error {
	switch v {
	case KubeadmJoinStatic, KubeadmJoinAuto:
		return nil
	}
	return fmt.Errorf("invalid --%s %q: must be %q or %q", KubeadmJoin, v, KubeadmJoinStatic, KubeadmJoinAuto)
}

// Defaults for the kubeadm-worker profile.
const (
	DefaultK8sVersion      = "v1.34.3"
	DefaultCNIVersion      = "v1.1.1"
	DefaultKubeletUnitsURL = "https://raw.githubusercontent.com/kubernetes/release/master/cmd/krel/templates/latest"
	DefaultJoinTokenTTL    = time.Hour
)

// StaticJoinString resolves the static kubeadm join string: the contents of
// --joinStringFile (trimmed, re-read on every call so a rotated Secret is
// picked up) win over --joinString.
func StaticJoinString() (string, error) {
	if file := viper.GetString(JoinStringFile); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading --%s: %w", JoinStringFile, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(viper.GetString(JoinString)), nil
}

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

	bindEnv(FlatcarVersion, "FLATCAR_VERSION_PIN")
	bindEnv(IgnitionFile, "IGNITION_FILE")
	bindEnv(HardwareMap, "HARDWARE_MAP")

	viper.SetDefault(IgnitionFile, DefaultIgnitionFile)
	viper.SetDefault(HardwareMap, "hardware.json")
	viper.SetDefault(TFTPPort, 69)
	viper.SetDefault(TFTPBlockSize, 1468)
	viper.SetDefault(WebDir, "./web/dist")
	viper.SetDefault(OCIGC, true)
	viper.SetDefault(OCIGCEmpty, false)
	viper.SetDefault(DoInstallClearOn, ClearOnIgnition)
	viper.SetDefault(Builtin, DefaultBuiltin)
	viper.SetDefault(HttpPort, 8080)
	viper.SetDefault(ProxyDHCP, false)
	viper.SetDefault(ProxyDHCPListen, "")
	viper.SetDefault(ProxyDHCPRelay, false)
	// Test-only override of the two ProxyDHCP listen ports ("67,4011"), so
	// the server can be exercised without root. Deliberately not a flag.
	viper.SetDefault(ProxyDHCPPorts, "")
	viper.SetDefault(HostnameTemplate, DefaultHostnameTemplate)
	viper.SetDefault(KubeadmJoin, KubeadmJoinStatic)
	viper.SetDefault(JoinTokenTTL, DefaultJoinTokenTTL)
	viper.SetDefault(K8sVersion, DefaultK8sVersion)
	viper.SetDefault(CNIVersion, DefaultCNIVersion)
	viper.SetDefault(KubeletUnitsURL, DefaultKubeletUnitsURL)
}

func bindEnv(key, env string) {
	if err := viper.BindEnv(key, env); err != nil {
		slog.Warn("Could not bind environment variable", "key", key, "env", env, "error", err)
	}
}

// ResolveServerAddress fills in the client-facing address when the operator
// left it to Booty: an empty --serverIP is autodetected from the default
// route and a zero --serverHttpPort means "same as --httpPort". Explicit
// values are left untouched. It must run once at startup, before viper
// becomes read-only.
func ResolveServerAddress() error {
	if viper.GetString(ServerIP) == "" {
		ip, err := DetectServerIP()
		if err != nil {
			return fmt.Errorf("--%s not set and autodetection failed: %w", ServerIP, err)
		}
		viper.Set(ServerIP, ip)
		slog.Info("serverIP autodetected", "ip", ip)
	}
	if viper.GetInt(ServerHttpPort) == 0 {
		viper.Set(ServerHttpPort, viper.GetInt(HttpPort))
	}
	return nil
}

// DetectServerIP returns the local address the kernel would use to reach a
// public IP. UDP "dialing" only selects a route; no packet is sent.
func DetectServerIP() (string, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return "", err
	}
	defer CloseQuietly(conn, "route probe")
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsUnspecified() {
		return "", fmt.Errorf("no usable local address on the default route")
	}
	return addr.IP.String(), nil
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

// EffectiveServerHttpPort is the port booting clients use to reach Booty:
// --serverHttpPort when set, otherwise --httpPort.
func EffectiveServerHttpPort() int {
	if port := viper.GetInt(ServerHttpPort); port != 0 {
		return port
	}
	return viper.GetInt(HttpPort)
}

// ServerHostPort returns the host[:port] clients should use to reach the HTTP
// server, omitting the port when it is the default 80.
func ServerHostPort() string {
	host := viper.GetString(ServerIP)
	if port := EffectiveServerHttpPort(); port != 80 {
		return net.JoinHostPort(host, fmt.Sprint(port))
	}
	return host
}

// LocalRegistry is how Booty reaches its own embedded OCI registry from
// inside the process. It must stay loopback: the server may be behind a
// port mapping (ServerHttpPort) that is only valid for booting clients.
func LocalRegistry() string {
	return fmt.Sprintf("127.0.0.1:%d", viper.GetInt(HttpPort))
}

// ClientRegistry is the registry address rendered into Ignition/iPXE for
// booting machines.
func ClientRegistry() string {
	return fmt.Sprintf("%s:%d", viper.GetString(ServerIP), EffectiveServerHttpPort())
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

// EnsureFile writes data to path only when the file does not exist yet.
func EnsureFile(path string, data []byte, perm os.FileMode) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return WriteFileAtomic(path, data, perm)
}
