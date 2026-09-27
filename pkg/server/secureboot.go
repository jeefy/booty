package server

import (
	"fmt"
	"log/slog"
	"slices"
	"sort"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func secureBootTrusted() []string {
	trusted, err := config.ParseSecureBootTrusted(viper.GetString(config.SecureBootTrusted))
	if err != nil {
		return []string{config.SecureBootTrustMicrosoft}
	}
	return trusted
}

func secureBootTrustedFlatcar() bool {
	return slices.Contains(secureBootTrusted(), config.SecureBootTrustFlatcar)
}

// recordSecureBoot stamps the host with the path its /booty.ipxe fetch took
// (sb=1 only ever comes from the signed iPXE's autoexec) and returns the
// host as it should be rendered. Previews leave the record alone.
func recordSecureBoot(mac string, host *hardware.Host, secureBoot, preview bool) *hardware.Host {
	if host == nil || preview {
		return host
	}
	changed, err := hardware.SetSecureBoot(mac, secureBoot)
	switch {
	case err != nil:
		slog.Error("Could not record Secure Boot state", "mac", mac, "error", err)
		return host
	case changed:
		slog.Info("Host Secure Boot state changed", "mac", mac, "secureBoot", secureBoot)
	}
	host.SecureBoot = secureBoot
	return host
}

// secureBootWarnings lists the registered hosts last seen through the Secure
// Boot path whose OS Booty cannot boot that way, sorted by MAC. It feeds both
// /info.secureBoot.warnings and /cluster.warnings.
func secureBootWarnings(hosts map[string]*hardware.Host) []string {
	warnings := []string{}
	trustedFlatcar := secureBootTrustedFlatcar()
	macs := make([]string, 0, len(hosts))
	for mac, h := range hosts {
		if h != nil && h.SecureBoot {
			macs = append(macs, mac)
		}
	}
	sort.Strings(macs)
	for _, mac := range macs {
		h := hosts[mac]
		switch h.OS {
		case "flatcar":
			if !trustedFlatcar {
				warnings = append(warnings, fmt.Sprintf("host %s (flatcar): Secure Boot host; the Flatcar CA is not in --%s, boot refused", mac, config.SecureBootTrusted))
			}
		case "bluefin":
			warnings = append(warnings, fmt.Sprintf("host %s (bluefin): reached Booty through the signed iPXE; Bluefin boots its signed netboot UKI only through UEFI HTTP Boot (%s)", mac, bluefinBootURL(mac)))
		}
	}
	return warnings
}
