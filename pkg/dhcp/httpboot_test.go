package dhcp

import (
	"bytes"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

const testHTTPBootBase = "http://192.168.1.10:8080/boot/sb"

func httpBootConfig() Config {
	c := testConfig()
	c.HTTPBoot = true
	c.HTTPBootBase = testHTTPBootBase
	return c
}

// ovmfDiscover is a DHCPDISCOVER captured from OVMF (edk2-ovmf-20260508)
// "UEFI HTTPv4" on the lab bridge: option 60 HTTPClient:Arch:00016:UNDI:003001,
// option 93 = 0x0010, option 57 = 1472, option 97 GUID.
func ovmfDiscover(t *testing.T) *dhcpv4.DHCPv4 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "ovmf-httpv4-discover.hex"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := dhcpv4.FromBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func httpPacket(t *testing.T, mt dhcpv4.MessageType, arch iana.Arch, mods ...dhcpv4.Modifier) *dhcpv4.DHCPv4 {
	t.Helper()
	base := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("HTTPClient:Arch:00016:UNDI:003001")),
		dhcpv4.WithOption(dhcpv4.OptClientArch(arch)),
	}
	pkt, err := dhcpv4.NewDiscovery(testMAC, append(base, mods...)...)
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func TestOVMFDiscoverFixtureIsHTTPBoot(t *testing.T) {
	pkt := ovmfDiscover(t)
	if pkt.MessageType() != dhcpv4.MessageTypeDiscover {
		t.Fatalf("fixture is a %v", pkt.MessageType())
	}
	if got := pkt.ClassIdentifier(); got != "HTTPClient:Arch:00016:UNDI:003001" {
		t.Errorf("option 60 = %q", got)
	}
	if archs := pkt.ClientArch(); len(archs) != 1 || archs[0] != iana.EFI_X86_64_HTTP {
		t.Errorf("option 93 = %v", archs)
	}
	if got, err := pkt.MaxMessageSize(); err != nil || got != 1472 {
		t.Errorf("option 57 = %d (%v), want 1472", got, err)
	}
	if !isHTTPBootClient(pkt) {
		t.Error("fixture not classified as HTTP Boot")
	}
}

func TestHTTPBootDisabledStaysSilent(t *testing.T) {
	mustBeSilent(t, ovmfDiscover(t), testConfig())
	c := httpBootConfig()
	c.HTTPBoot = false
	mustBeSilent(t, ovmfDiscover(t), c)
}

func TestHTTPBootOfferForOVMF(t *testing.T) {
	pkt := ovmfDiscover(t)
	reply := mustHandle(t, pkt, httpBootConfig())

	if reply.OpCode != dhcpv4.OpcodeBootReply || reply.MessageType() != dhcpv4.MessageTypeOffer {
		t.Fatalf("not an OFFER: %s", reply.Summary())
	}
	if reply.TransactionID != pkt.TransactionID || !bytes.Equal(reply.ClientHWAddr, pkt.ClientHWAddr) {
		t.Error("xid/chaddr not copied")
	}
	if !reply.YourIPAddr.IsUnspecified() {
		t.Errorf("yiaddr = %v, want 0.0.0.0", reply.YourIPAddr)
	}
	if !reply.ServerIdentifier().Equal(testServerIP) {
		t.Errorf("option 54 = %v", reply.ServerIdentifier())
	}
	if got := reply.ClassIdentifier(); got != "HTTPClient" {
		t.Errorf("option 60 = %q, want HTTPClient", got)
	}
	want := testHTTPBootBase + "/ipxe-shimx64.efi"
	if got := reply.BootFileNameOption(); got != want {
		t.Errorf("option 67 = %q, want %q", got, want)
	}
	if reply.Options.Has(dhcpv4.OptionVendorSpecificInformation) {
		t.Error("HTTP Boot OFFER must not carry option 43")
	}
	if reply.Options.Has(dhcpv4.OptionTFTPServerName) {
		t.Error("HTTP Boot OFFER must not carry option 66")
	}
	if got := reply.Options.Get(dhcpv4.OptionClientMachineIdentifier); !bytes.Equal(got, pkt.Options.Get(dhcpv4.OptionClientMachineIdentifier)) {
		t.Errorf("option 97 not echoed: %x", got)
	}
	if !reply.IsBroadcast() {
		t.Error("broadcast flag not copied")
	}
	if n := len(reply.ToBytes()); n > maxHTTPBootReply {
		t.Errorf("OFFER is %d bytes, max %d", n, maxHTTPBootReply)
	}
}

func TestHTTPBootClassification(t *testing.T) {
	cases := []struct {
		name string
		pkt  *dhcpv4.DHCPv4
		want bool
	}{
		{"HTTPClient class arch 16", httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64_HTTP), true},
		{"HTTPClient class arch 7", httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64), true},
		{"HTTPClient class no arch", httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64_HTTP, dhcpv4.WithoutOption(dhcpv4.OptionClientSystemArchitectureType)), true},
		{"no class arch 16", httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64_HTTP, dhcpv4.WithoutOption(dhcpv4.OptionClassIdentifier)), true},
		{"no class arch 15", httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_HTTP, dhcpv4.WithoutOption(dhcpv4.OptionClassIdentifier)), true},
		{"no class arch 19", httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_ARM64_HTTP, dhcpv4.WithoutOption(dhcpv4.OptionClassIdentifier)), true},
		{"PXEClient arch 16", pxePacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64_HTTP), false},
		{"PXEClient arch 7", pxePacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64), false},
		{"iPXE user class", pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64, dhcpv4.WithUserClass("iPXE", false)), false},
		{"plain DHCP client", httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64, dhcpv4.WithOption(dhcpv4.OptClassIdentifier("MSFT 5.0"))), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHTTPBootClient(tc.pkt); got != tc.want {
				t.Errorf("isHTTPBootClient = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHTTPBootFileByArch(t *testing.T) {
	cases := []struct {
		arch iana.Arch
		mods []dhcpv4.Modifier
		cfg  func(Config) Config
		want string
	}{
		{iana.EFI_X86_64_HTTP, nil, nil, "ipxe-shimx64.efi"},
		{iana.EFI_X86_HTTP, nil, nil, "ipxe-shimx64.efi"},
		{iana.EFI_ARM64_HTTP, nil, nil, "ipxe-shimaa64.efi"},
		{iana.EFI_X86_64_HTTP, []dhcpv4.Modifier{dhcpv4.WithoutOption(dhcpv4.OptionClientSystemArchitectureType)}, nil, "ipxe-shimx64.efi"},
		{iana.EFI_X86_64_HTTP, nil, func(c Config) Config { c.HTTPBootEFIFile = SnponlyHTTPBootEFIFile; return c }, "snponly-shimx64.efi"},
		{iana.EFI_X86_64_HTTP, nil, func(c Config) Config { c.HTTPBootBase = testHTTPBootBase + "/"; return c }, "ipxe-shimx64.efi"},
	}
	for _, tc := range cases {
		t.Run(tc.arch.String()+"/"+tc.want, func(t *testing.T) {
			c := httpBootConfig()
			if tc.cfg != nil {
				c = tc.cfg(c)
			}
			reply := mustHandle(t, httpPacket(t, dhcpv4.MessageTypeDiscover, tc.arch, tc.mods...), c)
			if got := reply.BootFileNameOption(); got != testHTTPBootBase+"/"+tc.want {
				t.Errorf("option 67 = %q, want %s/%s", got, testHTTPBootBase, tc.want)
			}
		})
	}
}

func TestHTTPBootIgnoresRequestsRelaysAndOtherServers(t *testing.T) {
	c := httpBootConfig()
	mustBeSilent(t, httpPacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64_HTTP, dhcpv4.WithOption(dhcpv4.OptServerIdentifier(testServerIP))), c)
	mustBeSilent(t, httpPacket(t, dhcpv4.MessageTypeInform, iana.EFI_X86_64_HTTP), c)
	mustBeSilent(t, httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64_HTTP, dhcpv4.WithGatewayIP(net.IPv4(10, 0, 0, 1))), c)
	mustBeSilent(t, httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64_HTTP, dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(192, 168, 1, 1)))), c)

	c.Relay = true
	reply := mustHandle(t, httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64_HTTP, dhcpv4.WithGatewayIP(net.IPv4(10, 0, 0, 1))), c)
	if !reply.GatewayIPAddr.Equal(net.IPv4(10, 0, 0, 1)) {
		t.Errorf("giaddr = %v", reply.GatewayIPAddr)
	}

	c.HTTPBootBase = ""
	mustBeSilent(t, httpPacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64_HTTP), c)
}

func TestHTTPBootOfferOwnReplyIsIgnored(t *testing.T) {
	c := httpBootConfig()
	offer := mustHandle(t, ovmfDiscover(t), c)
	mustBeSilent(t, offer, c)
}
