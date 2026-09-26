package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Keys lists every viper key Booty reads, in declaration order. GET /config
// renders each of them with its effective value and where it came from.
var Keys = []string{
	FlatcarChannel, FlatcarVersion, CoreOSChannel, IgnitionFile, HardwareMap,
	CoreOSArchitecture, FlatcarArchitecture, Debug, UpdateSchedule, HttpPort,
	TFTPPort, TFTPBlockSize, WebDir, DataDir, FlatcarURL, CoreOSURL, ServerIP,
	ServerHttpPort, JoinString, JoinStringFile, KubeadmJoin, JoinTokenTTL,
	Profile, K8sVersion, CNIVersion, CrictlVersion, ContainerdDisk,
	KubeletUnitsURL, OCIGC, OCIGCEmpty, DoInstallClearOn, Builtin,
	SSHAuthorizedKeys, SSHAuthorizedKeysFl, ProxyDHCP, ProxyDHCPListen,
	ProxyDHCPRelay, ProxyDHCPPorts, Version, Timestamp, AutoRegister,
	HostnameTemplate, BluefinRepo, BluefinVersion, GithubToken,
	InstallMinDuration, ClusterDistribution, ControlPlane, ControlPlaneEndpt,
	ClusterCADir, CNI, CNIRelease, PodCIDR, ServiceCIDR, K0sTokenFile,
	Kubeconfig, ControlPlaneDisk, K0sVersion,
}

// Where a setting's effective value comes from, mirroring viper's
// precedence: an explicitly passed flag beats the environment, which beats
// the built-in default.
const (
	SourceFlag    = "flag"
	SourceEnv     = "env"
	SourceDefault = "default"
)

// FlagChanged reports whether the operator passed the flag for key on the
// command line. cmd/main wires it to the root command's
// pflag.FlagSet.Changed; while it is nil (tests, library use) nothing is
// attributed to a flag.
var FlagChanged func(key string) bool

// legacyEnv are the pre-BOOTY_ environment names LoadConfig still binds.
var legacyEnv = map[string][]string{
	FlatcarVersion: {"FLATCAR_VERSION_PIN"},
	IgnitionFile:   {"IGNITION_FILE"},
	HardwareMap:    {"HARDWARE_MAP"},
}

// EnvName is the BOOTY_<KEY> environment variable viper reads for key.
func EnvName(key string) string {
	return "BOOTY_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// SettingSource reports where the effective value of key comes from. Values
// Booty computes itself at startup (the autodetected serverIP, the derived
// serverHttpPort, the build stamp) count as defaults.
func SettingSource(key string) string {
	if FlagChanged != nil && FlagChanged(key) {
		return SourceFlag
	}
	if _, ok := os.LookupEnv(EnvName(key)); ok {
		return SourceEnv
	}
	for _, name := range legacyEnv[key] {
		if _, ok := os.LookupEnv(name); ok {
			return SourceEnv
		}
	}
	return SourceDefault
}

// SettingValue renders the effective value of key for display: bools and
// numbers via fmt, durations in Go notation, slices comma-joined.
func SettingValue(key string) string {
	switch v := viper.Get(key).(type) {
	case nil:
		return ""
	case string:
		return v
	case time.Duration:
		return v.String()
	case []string:
		return strings.Join(v, ", ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, fmt.Sprint(item))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(v)
	}
}
