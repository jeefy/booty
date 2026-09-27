package server

import (
	"strings"

	"github.com/jeefy/booty/pkg/config"
)

const (
	bluefinPathPrefix = "/bluefin/"
	// bluefinBootFile is the name Booty hands UEFI HTTP Boot clients; any
	// *.efi below /bluefin/<mac>/ serves the current netboot UKI.
	bluefinBootFile = "bluefin-server-netboot.efi"
)

// bluefinBootURL is the UEFI HTTP Boot URL of mac's netboot UKI. The MAC is
// written with dashes so no firmware URL parser trips over colons in the
// path; the route accepts either form.
func bluefinBootURL(mac string) string {
	return "http://" + config.ServerHostPort() + bluefinPathPrefix + strings.ReplaceAll(mac, ":", "-") + "/" + bluefinBootFile
}
