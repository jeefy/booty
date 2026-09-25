package dhcp

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

// PXE vendor option (DHCP option 43) sub-option tags, PXE 2.1 spec table 2-1.
const (
	pxeDiscoveryControl uint8 = 6
	pxeBootServers      uint8 = 8
	pxeBootMenu         uint8 = 9
	pxeMenuPrompt       uint8 = 10
	pxeBootItem         uint8 = 71
	pxeEnd              uint8 = 255

	// PXE_DISCOVERY_CONTROL bits 0-2: disable broadcast and multicast
	// discovery and use only the boot-server list we supply (0x07, as
	// pixiecore does).
	pxeBootServerListOnly byte = 0x07

	pxeClientPrefix = "PXEClient"
	pxeMenuText     = "Booty"
	ipxeUserClass   = "iPXE"
)

const (
	DefaultBIOSFile   = "undionly.kpxe"
	DefaultEFIFile    = "ipxe.efi"
	DefaultARM64File  = "ipxe-arm64.efi"
	DefaultIPXEScript = "booty.ipxe"
)

// Config selects what the ProxyDHCP server listens on and hands out.
type Config struct {
	// Listen is an IPv4 address or an interface name; empty binds all
	// interfaces. A unicast address only receives unicast (port 4011)
	// traffic on Linux, so prefer an interface name on multi-homed hosts.
	Listen   string
	ServerIP net.IP
	Relay    bool

	BIOSFile   string
	EFIFile    string
	ARM64File  string
	IPXEScript string

	Port67   int
	Port4011 int
}

func (c Config) withDefaults() Config {
	if c.BIOSFile == "" {
		c.BIOSFile = DefaultBIOSFile
	}
	if c.EFIFile == "" {
		c.EFIFile = DefaultEFIFile
	}
	if c.ARM64File == "" {
		c.ARM64File = DefaultARM64File
	}
	if c.IPXEScript == "" {
		c.IPXEScript = DefaultIPXEScript
	}
	if c.Port67 == 0 {
		c.Port67 = dhcpv4.ServerPort
	}
	if c.Port4011 == 0 {
		c.Port4011 = 4011
	}
	return c
}

func (c Config) validate() error {
	if c.ServerIP == nil || c.ServerIP.To4() == nil || c.ServerIP.IsUnspecified() {
		return fmt.Errorf("proxydhcp: serverIP %q is not a usable IPv4 address", c.ServerIP)
	}
	return nil
}

// handle is the whole ProxyDHCP protocol: it returns the reply for pkt, or
// (nil, false) when Booty must stay silent. It never touches a socket.
func handle(pkt *dhcpv4.DHCPv4, c Config) (*dhcpv4.DHCPv4, bool) {
	c = c.withDefaults()
	if pkt == nil || pkt.OpCode != dhcpv4.OpcodeBootRequest {
		return nil, false
	}
	log := slog.With("xid", pkt.TransactionID.String(), "mac", pkt.ClientHWAddr.String())

	if !strings.HasPrefix(pkt.ClassIdentifier(), pxeClientPrefix) {
		log.Debug("ProxyDHCP ignoring non-PXE packet", "classIdentifier", pkt.ClassIdentifier())
		return nil, false
	}
	archs := pkt.ClientArch()
	if len(archs) == 0 {
		log.Debug("ProxyDHCP ignoring PXE packet without client architecture")
		return nil, false
	}
	if isSet(pkt.GatewayIPAddr) && !c.Relay {
		log.Debug("ProxyDHCP ignoring relayed packet", "giaddr", pkt.GatewayIPAddr)
		return nil, false
	}
	if sid := pkt.ServerIdentifier(); sid != nil && !sid.Equal(c.ServerIP) {
		log.Debug("ProxyDHCP ignoring packet addressed to another server", "serverIdentifier", sid)
		return nil, false
	}

	switch pkt.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		return buildOffer(pkt, c, log)
	case dhcpv4.MessageTypeRequest:
		return buildAck(pkt, c, archs[0], log)
	default:
		log.Debug("ProxyDHCP ignoring message type", "type", pkt.MessageType())
		return nil, false
	}
}

func baseReply(pkt *dhcpv4.DHCPv4, c Config, mt dhcpv4.MessageType) (*dhcpv4.DHCPv4, error) {
	return dhcpv4.NewReplyFromRequest(pkt,
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithServerIP(c.ServerIP),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(c.ServerIP)),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier(pxeClientPrefix)),
		dhcpv4.WithOptionCopied(pkt, dhcpv4.OptionClientMachineIdentifier),
	)
}

// buildOffer answers DHCPDISCOVER. No address is offered (yiaddr stays
// 0.0.0.0); the PXE vendor options point the client at Booty as the only
// boot server so it comes back with a DHCPREQUEST on port 4011.
func buildOffer(pkt *dhcpv4.DHCPv4, c Config, log *slog.Logger) (*dhcpv4.DHCPv4, bool) {
	reply, err := baseReply(pkt, c, dhcpv4.MessageTypeOffer)
	if err != nil {
		log.Error("ProxyDHCP could not build OFFER", "error", err)
		return nil, false
	}
	reply.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionVendorSpecificInformation, offerVendorOptions(c.ServerIP)))
	log.Info("ProxyDHCP OFFER", "siaddr", c.ServerIP)
	return reply, true
}

// buildAck answers DHCPREQUEST with the boot file for the client's
// architecture. iPXE (user class "iPXE") already runs iPXE, so it gets the
// booty.ipxe stub instead of another iPXE binary.
func buildAck(pkt *dhcpv4.DHCPv4, c Config, arch iana.Arch, log *slog.Logger) (*dhcpv4.DHCPv4, bool) {
	reply, err := baseReply(pkt, c, dhcpv4.MessageTypeAck)
	if err != nil {
		log.Error("ProxyDHCP could not build ACK", "error", err)
		return nil, false
	}
	reply.ClientIPAddr = pkt.ClientIPAddr

	var file string
	if hasUserClass(pkt, ipxeUserClass) {
		file = c.IPXEScript
	} else {
		file = bootFileForArch(arch, c, log)
	}
	reply.BootFileName = file
	reply.UpdateOption(dhcpv4.OptBootFileName(file))
	reply.UpdateOption(dhcpv4.OptTFTPServerName(c.ServerIP.String()))
	reply.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionVendorSpecificInformation, ackVendorOptions()))
	log.Info("ProxyDHCP ACK", "arch", arch, "file", file, "siaddr", c.ServerIP)
	return reply, true
}

// bootFileForArch maps the RFC 4578 client architecture to the iPXE binary
// Booty serves over TFTP. Only x86 BIOS and x86-64 UEFI are shipped.
func bootFileForArch(arch iana.Arch, c Config, log *slog.Logger) string {
	switch arch {
	case iana.INTEL_X86PC:
		return c.BIOSFile
	case iana.EFI_X86_64, iana.EFI_BC:
		return c.EFIFile
	case iana.EFI_IA32:
		log.Warn("ProxyDHCP: 32-bit UEFI client, sending the x86-64 binary which will not run on it", "arch", arch, "file", c.EFIFile)
		return c.EFIFile
	case iana.EFI_ARM64:
		log.Warn("ProxyDHCP: ARM64 UEFI client; Booty does not ship this file so the boot will fail", "arch", arch, "file", c.ARM64File)
		return c.ARM64File
	default:
		log.Warn("ProxyDHCP: unknown client architecture, falling back to the BIOS binary", "arch", arch, "file", c.BIOSFile)
		return c.BIOSFile
	}
}

func hasUserClass(pkt *dhcpv4.DHCPv4, want string) bool {
	raw := pkt.Options.Get(dhcpv4.OptionUserClassInformation)
	if string(raw) == want {
		return true
	}
	for _, uc := range pkt.UserClass() {
		if uc == want {
			return true
		}
	}
	return false
}

func isSet(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified()
}

// offerVendorOptions encodes the PXE sub-options of a ProxyDHCP OFFER:
// discovery control, a boot server list with only Booty, a one-entry boot
// menu and a zero-timeout prompt so the client does not wait for a keypress.
func offerVendorOptions(serverIP net.IP) []byte {
	server := make([]byte, 0, 7)
	server = binary.BigEndian.AppendUint16(server, 0)
	server = append(server, 1)
	server = append(server, serverIP.To4()...)

	menu := make([]byte, 0, 3+len(pxeMenuText))
	menu = binary.BigEndian.AppendUint16(menu, 0)
	menu = append(menu, byte(len(pxeMenuText)))
	menu = append(menu, pxeMenuText...)

	prompt := append([]byte{0}, pxeMenuText...)

	var b []byte
	b = appendSubOption(b, pxeDiscoveryControl, []byte{pxeBootServerListOnly})
	b = appendSubOption(b, pxeBootServers, server)
	b = appendSubOption(b, pxeBootMenu, menu)
	b = appendSubOption(b, pxeMenuPrompt, prompt)
	return append(b, pxeEnd)
}

// ackVendorOptions encodes PXE_BOOT_ITEM type 0 layer 0, telling the client
// which menu entry it was given.
func ackVendorOptions() []byte {
	b := appendSubOption(nil, pxeBootItem, []byte{0, 0, 0, 0})
	return append(b, pxeEnd)
}

func appendSubOption(b []byte, tag uint8, value []byte) []byte {
	b = append(b, tag, byte(len(value)))
	return append(b, value...)
}

// parseSubOptions decodes a PXE option 43 payload into tag -> value. It
// stops at the end tag and reports malformed input.
func parseSubOptions(b []byte) (map[uint8][]byte, error) {
	out := map[uint8][]byte{}
	for i := 0; i < len(b); {
		tag := b[i]
		if tag == pxeEnd {
			return out, nil
		}
		if i+1 >= len(b) {
			return nil, fmt.Errorf("sub-option %d truncated", tag)
		}
		n := int(b[i+1])
		if i+2+n > len(b) {
			return nil, fmt.Errorf("sub-option %d length %d exceeds data", tag, n)
		}
		out[tag] = b[i+2 : i+2+n]
		i += 2 + n
	}
	return nil, fmt.Errorf("missing end tag")
}

// replyDestination applies RFC 2131 section 4.1: relays get the reply on
// port 67, clients with an address are unicast, and clients that have no
// address yet (or asked for it) are answered on the limited broadcast.
func replyDestination(pkt *dhcpv4.DHCPv4, src *net.UDPAddr) *net.UDPAddr {
	if isSet(pkt.GatewayIPAddr) {
		return &net.UDPAddr{IP: pkt.GatewayIPAddr, Port: dhcpv4.ServerPort}
	}
	if isSet(pkt.ClientIPAddr) {
		return &net.UDPAddr{IP: pkt.ClientIPAddr, Port: dhcpv4.ClientPort}
	}
	if pkt.IsBroadcast() || src == nil || !isSet(src.IP) {
		return &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort}
	}
	return &net.UDPAddr{IP: src.IP, Port: src.Port}
}
