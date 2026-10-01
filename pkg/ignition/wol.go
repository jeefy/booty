package ignition

// WoLLinkPath is the systemd .link file the wol builtin writes: udev's
// net_setup_link applies it to the NIC with the host's MAC on the add
// event the real root re-triggers, so the NIC is armed for Wake-on-LAN
// on every boot whatever the driver's default (the homelab's report
// "Wake-on: d"). It works under networkd and NetworkManager alike and
// needs no ethtool, which Bluefin does not ship.
const WoLLinkPath = "/etc/systemd/network/10-booty-wol.link"

// WoLLink is the contents of WoLLinkPath for mac; "" when mac is empty.
func WoLLink(mac string) string {
	if mac == "" {
		return ""
	}
	return `[Match]
MACAddress=` + mac + `

[Link]
WakeOnLan=magic
`
}
