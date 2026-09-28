package config

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	BluefinRepo         = "bluefinRepo"
	BluefinVersion      = "bluefinVersion"
	BluefinKeyring      = "bluefinKeyring"
	BluefinOCI          = "bluefinOCI"
	GithubToken         = "githubToken"
	InstallMinDuration  = "installMinDuration"
	ClusterDistribution = "clusterDistribution"
	ControlPlane        = "controlPlane"
	ControlPlaneEndpt   = "controlPlaneEndpoint"
	ClusterCADir        = "clusterCADir"
	CNI                 = "cni"
	CNIRelease          = "cniRelease"
	PodCIDR             = "podCIDR"
	ServiceCIDR         = "serviceCIDR"
	K0sTokenFile        = "k0sTokenFile"
	Kubeconfig          = "kubeconfig"
	ControlPlaneDisk    = "controlPlaneDisk"
	K0sVersion          = "k0sVersion"
	EFIBootloader       = "efiBootloader"
	SecureBoot          = "secureBoot"
	SecureBootIPXEShim  = "secureBootIPXEShimVersion"
	SecureBootIPXE      = "secureBootIPXEVersion"
	FedoraShimVersion   = "fedoraShimVersion"
	FedoraGrubVersion   = "fedoraGrubVersion"
	SecureBootTrusted   = "secureBootTrusted"
)

// Which x86-64 UEFI iPXE build clients get: ipxe.efi (iPXE's own NIC
// drivers) or snponly.efi (the firmware's SNP stack). ProxyDHCP hands the
// matching file to PXE clients and the matching Microsoft-signed shim
// (ipxe-shimx64.efi / snponly-shimx64.efi) to HTTP Boot clients.
const (
	EFIBootloaderIPXE    = "ipxe"
	EFIBootloaderSnponly = "snponly"
)

// ValidateEFIBootloader rejects anything but the two shipped builds.
func ValidateEFIBootloader(v string) error {
	switch v {
	case EFIBootloaderIPXE, EFIBootloaderSnponly:
		return nil
	}
	return fmt.Errorf("invalid --%s %q: must be %q or %q", EFIBootloader, v, EFIBootloaderIPXE, EFIBootloaderSnponly)
}

// Secure Boot artefact pins and layout. The bundle (signed iPXE shims, the
// iPXE-CA-signed iPXE binaries and the Fedora shim/GRUB) lives in
// DataDir/secureboot/<bundle>/ behind a "current" symlink; the Flatcar
// Secure Boot CA extracted from the tracked Flatcar release sits next to it
// as flatcar-ca.der/.pem. Everything is served read-only under /boot/sb/
// and /boot/secureboot/.
const (
	SecureBootDir          = "secureboot"
	SecureBootCurrentLink  = "current"
	SecureBootManifestFile = "manifest.json"
	SecureBootFlatcarCADER = "flatcar-ca.der"
	SecureBootFlatcarCAPEM = "flatcar-ca.pem"
	SecureBootFedoraDir    = "fedora"

	// https://github.com/ipxe/shim/releases/tag/ipxe-16.1 (Microsoft
	// UEFI CA 2011 + 2023 dual-signed) and
	// https://github.com/ipxe/ipxe/releases/tag/v2.0.0 (ipxeboot.tar.gz,
	// x86_64-sb/ members signed by the iPXE CA the shim trusts).
	DefaultSecureBootIPXEShim = "ipxe-16.1"
	DefaultSecureBootIPXE     = "v2.0.0"
	// Fedora 45 shim (dual-signed, same vendor cert as the FCOS kernel
	// signer) and the Fedora 44 GRUB it pairs with.
	DefaultFedoraShimVersion = "16.1-7"
	DefaultFedoraGrubVersion = "2.12-64.fc44"

	// Names accepted by --secureBootTrusted: the CAs the fleet's firmware
	// db contains. microsoft is always implied.
	SecureBootTrustMicrosoft = "microsoft"
	SecureBootTrustFlatcar   = "flatcar"
)

// SecureBootPath joins elem onto DataDir/secureboot.
func SecureBootPath(elem ...string) string {
	return DataPath(append([]string{SecureBootDir}, elem...)...)
}

// SecureBootURL is the URL directory HTTP Boot clients fetch the signed
// shim from (DHCP option 67 = SecureBootURL + "/<shim>"); the shim then
// loads ipxe.efi from the same directory. IP literal, :80 omitted.
func SecureBootURL() string {
	return "http://" + ServerHostPort() + "/boot/sb"
}

// ParseSecureBootTrusted normalises the --secureBootTrusted list: lower-cased,
// deduplicated, microsoft always present, unknown names rejected.
func ParseSecureBootTrusted(v string) ([]string, error) {
	out := []string{SecureBootTrustMicrosoft}
	for _, raw := range strings.Split(v, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch name {
		case "", SecureBootTrustMicrosoft:
		case SecureBootTrustFlatcar:
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		default:
			return nil, fmt.Errorf("invalid --%s entry %q: must be %q or %q", SecureBootTrusted, raw, SecureBootTrustMicrosoft, SecureBootTrustFlatcar)
		}
	}
	return out, nil
}

// Cluster bootstrap defaults and the directory (relative to DataDir) that
// holds the cluster CA, service-account keys and persisted bootstrap
// tokens. Everything under it is cluster-admin material and is never
// served over /data/.
const (
	ClusterDir                 = "cluster"
	ClusterPKIDir              = "pki"
	ClusterTokensFile          = "tokens.json"
	ClusterReadyFile           = "ready.json"
	DefaultClusterDistribution = "kubeadm"
	DefaultControlPlane        = "external"
	DefaultCNI                 = "cilium"
	DefaultPodCIDR             = "10.244.0.0/16"
	DefaultServiceCIDR         = "10.96.0.0/12"
	// DefaultK0sVersion is the k0s release Flatcar/CoreOS nodes download;
	// it matches the /usr/bin/k0s Bluefin Server 26.08.0 ships so a mixed
	// cluster runs one k0s version.
	DefaultK0sVersion = "v1.36.4+k0s.0"
)

// ClusterPath joins elem onto DataDir/cluster.
func ClusterPath(elem ...string) string {
	return DataPath(append([]string{ClusterDir}, elem...)...)
}

// ParseHostPort validates a host[:port] endpoint: host is an IP or a DNS
// name, port (when given) is 1-65535. It returns the host and the port, 0
// when absent.
func ParseHostPort(endpoint string) (host string, port int, err error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", 0, fmt.Errorf("endpoint is empty")
	}
	if strings.Contains(endpoint, "://") || strings.Contains(endpoint, "/") {
		return "", 0, fmt.Errorf("endpoint %q must be host[:port], not a URL", endpoint)
	}
	host = endpoint
	if h, p, splitErr := net.SplitHostPort(endpoint); splitErr == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, fmt.Errorf("endpoint %q has an invalid port", endpoint)
		}
		host, port = h, n
	} else if strings.Count(endpoint, ":") > 0 && net.ParseIP(endpoint) == nil {
		return "", 0, fmt.Errorf("endpoint %q must be host[:port]", endpoint)
	}
	if host == "" {
		return "", 0, fmt.Errorf("endpoint %q has no host", endpoint)
	}
	if net.ParseIP(host) == nil && !hostnameRe.MatchString(host) {
		return "", 0, fmt.Errorf("endpoint %q is neither an IP nor a hostname", endpoint)
	}
	return host, port, nil
}

var hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*\.?$`)

// WithDefaultPort appends :port to a host[:port] endpoint that has none.
func WithDefaultPort(endpoint string, port int) string {
	host, p, err := ParseHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	if p == 0 {
		p = port
	}
	return net.JoinHostPort(host, strconv.Itoa(p))
}

// Bluefin Server defaults: the GitHub repository whose v<version> releases
// carry the netboot UKI, the OS DDI and the sysexts, and how long after
// serving an install boot a new netboot UKI fetch counts as "the install
// finished" for --doInstallClearOn=next-boot.
const (
	DefaultBluefinRepo        = "projectbluefin/server"
	DefaultInstallMinDuration = 3 * time.Minute
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
// fetches its Ignition config, only once it POSTs /booted, or (Bluefin) on
// the first /booty.ipxe fetch at least InstallMinDuration after the install
// stanza was served.
const (
	ClearOnIgnition = "ignition"
	ClearOnBooted   = "booted"
	ClearOnNextBoot = "next-boot"
)

// ValidateDoInstallClearOn rejects anything but the known modes.
func ValidateDoInstallClearOn(v string) error {
	switch v {
	case ClearOnIgnition, ClearOnBooted, ClearOnNextBoot:
		return nil
	}
	return fmt.Errorf("invalid --%s %q: must be %q, %q or %q", DoInstallClearOn, v, ClearOnIgnition, ClearOnBooted, ClearOnNextBoot)
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
	viper.SetDefault(BluefinRepo, DefaultBluefinRepo)
	viper.SetDefault(BluefinVersion, "")
	viper.SetDefault(BluefinKeyring, "")
	viper.SetDefault(BluefinOCI, "")
	viper.SetDefault(GithubToken, "")
	viper.SetDefault(InstallMinDuration, DefaultInstallMinDuration)
	viper.SetDefault(ClusterDistribution, DefaultClusterDistribution)
	viper.SetDefault(ControlPlane, DefaultControlPlane)
	viper.SetDefault(ControlPlaneEndpt, "")
	viper.SetDefault(ClusterCADir, "")
	viper.SetDefault(CNI, DefaultCNI)
	viper.SetDefault(CNIRelease, "")
	viper.SetDefault(PodCIDR, DefaultPodCIDR)
	viper.SetDefault(ServiceCIDR, DefaultServiceCIDR)
	viper.SetDefault(K0sTokenFile, "")
	viper.SetDefault(Kubeconfig, "")
	viper.SetDefault(ControlPlaneDisk, "")
	viper.SetDefault(K0sVersion, DefaultK0sVersion)
	viper.SetDefault(EFIBootloader, EFIBootloaderIPXE)
	viper.SetDefault(SecureBoot, false)
	viper.SetDefault(SecureBootIPXEShim, DefaultSecureBootIPXEShim)
	viper.SetDefault(SecureBootIPXE, DefaultSecureBootIPXE)
	viper.SetDefault(FedoraShimVersion, DefaultFedoraShimVersion)
	viper.SetDefault(FedoraGrubVersion, DefaultFedoraGrubVersion)
	viper.SetDefault(SecureBootTrusted, "")
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

// Bluefin releases live in DataDir/bluefin/<version>/ with relative
// "current" and "previous" symlinks to the served one and the one it
// replaced (kept so hosts mid-boot can finish); manifest.json inside names
// the files.
const (
	BluefinDir          = "bluefin"
	BluefinCurrentLink  = "current"
	BluefinPreviousLink = "previous"
	BluefinManifestFile = "manifest.json"
)

// BluefinCurrentManifestPath is DataDir/bluefin/current/manifest.json.
func BluefinCurrentManifestPath() string {
	return DataPath(BluefinDir, BluefinCurrentLink, BluefinManifestFile)
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
