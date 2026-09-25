package dhcp

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

var (
	testMAC      = net.HardwareAddr{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xcc}
	testServerIP = net.IPv4(192, 168, 1, 10)
)

func testConfig() Config {
	return Config{ServerIP: testServerIP}
}

func pxeClass(arch iana.Arch) string {
	if arch == iana.INTEL_X86PC {
		return "PXEClient:Arch:00000:UNDI:002001"
	}
	return "PXEClient:Arch:00007:UNDI:003016"
}

func pxePacket(t *testing.T, mt dhcpv4.MessageType, arch iana.Arch, mods ...dhcpv4.Modifier) *dhcpv4.DHCPv4 {
	t.Helper()
	base := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier(pxeClass(arch))),
		dhcpv4.WithOption(dhcpv4.OptClientArch(arch)),
	}
	pkt, err := dhcpv4.NewDiscovery(testMAC, append(base, mods...)...)
	if err != nil {
		t.Fatalf("building packet: %v", err)
	}
	return pkt
}

func mustHandle(t *testing.T, pkt *dhcpv4.DHCPv4, c Config) *dhcpv4.DHCPv4 {
	t.Helper()
	reply, ok := handle(pkt, c)
	if !ok || reply == nil {
		t.Fatalf("expected a reply, got silence")
	}
	return reply
}

func mustBeSilent(t *testing.T, pkt *dhcpv4.DHCPv4, c Config) {
	t.Helper()
	if reply, ok := handle(pkt, c); ok || reply != nil {
		t.Fatalf("expected silence, got %s", reply.Summary())
	}
}

func subOptions(t *testing.T, reply *dhcpv4.DHCPv4) map[uint8][]byte {
	t.Helper()
	raw := reply.Options.Get(dhcpv4.OptionVendorSpecificInformation)
	if raw == nil {
		t.Fatal("reply has no option 43")
	}
	subs, err := parseSubOptions(raw)
	if err != nil {
		t.Fatalf("option 43 %x: %v", raw, err)
	}
	return subs
}

func TestDiscoverProducesPXEOffer(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC,
		dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, []byte{0, 1, 2, 3})))
	reply := mustHandle(t, pkt, testConfig())

	if reply.OpCode != dhcpv4.OpcodeBootReply {
		t.Errorf("opcode = %v, want BootReply", reply.OpCode)
	}
	if reply.MessageType() != dhcpv4.MessageTypeOffer {
		t.Errorf("message type = %v, want OFFER", reply.MessageType())
	}
	if reply.TransactionID != pkt.TransactionID {
		t.Errorf("xid = %v, want %v", reply.TransactionID, pkt.TransactionID)
	}
	if !bytes.Equal(reply.ClientHWAddr, testMAC) {
		t.Errorf("chaddr = %v, want %v", reply.ClientHWAddr, testMAC)
	}
	if !reply.YourIPAddr.IsUnspecified() {
		t.Errorf("yiaddr = %v, want 0.0.0.0", reply.YourIPAddr)
	}
	if !reply.ServerIPAddr.Equal(testServerIP) {
		t.Errorf("siaddr = %v, want %v", reply.ServerIPAddr, testServerIP)
	}
	if !reply.ServerIdentifier().Equal(testServerIP) {
		t.Errorf("option 54 = %v, want %v", reply.ServerIdentifier(), testServerIP)
	}
	if reply.ClassIdentifier() != "PXEClient" {
		t.Errorf("option 60 = %q, want PXEClient", reply.ClassIdentifier())
	}
	if got := reply.Options.Get(dhcpv4.OptionClientMachineIdentifier); !bytes.Equal(got, []byte{0, 1, 2, 3}) {
		t.Errorf("option 97 = %x, want echoed 00010203", got)
	}
	if reply.BootFileName != "" || reply.Options.Has(dhcpv4.OptionBootfileName) {
		t.Errorf("OFFER must not carry a boot file, got file=%q opt67=%v", reply.BootFileName, reply.Options.Has(dhcpv4.OptionBootfileName))
	}

	subs := subOptions(t, reply)
	if got := subs[pxeDiscoveryControl]; !bytes.Equal(got, []byte{0x07}) {
		t.Errorf("PXE_DISCOVERY_CONTROL = %x, want 03", got)
	}
	wantServers := append([]byte{0x00, 0x00, 0x01}, testServerIP.To4()...)
	if got := subs[pxeBootServers]; !bytes.Equal(got, wantServers) {
		t.Errorf("PXE_BOOT_SERVERS = %x, want %x", got, wantServers)
	}
	wantMenu := append([]byte{0x00, 0x00, byte(len("Booty"))}, "Booty"...)
	if got := subs[pxeBootMenu]; !bytes.Equal(got, wantMenu) {
		t.Errorf("PXE_BOOT_MENU = %x, want %x", got, wantMenu)
	}
	wantPrompt := append([]byte{0x00}, "Booty"...)
	if got := subs[pxeMenuPrompt]; !bytes.Equal(got, wantPrompt) {
		t.Errorf("PXE_MENU_PROMPT = %x, want %x", got, wantPrompt)
	}
	raw := reply.Options.Get(dhcpv4.OptionVendorSpecificInformation)
	if raw[len(raw)-1] != pxeEnd {
		t.Errorf("option 43 does not end with 0xff: %x", raw)
	}
	if _, ok := subs[pxeBootItem]; ok {
		t.Error("OFFER must not contain PXE_BOOT_ITEM")
	}
}

func TestRequestAckBootFileByArch(t *testing.T) {
	cases := []struct {
		arch iana.Arch
		want string
	}{
		{iana.INTEL_X86PC, "undionly.kpxe"},
		{iana.EFI_IA32, "ipxe.efi"},
		{iana.EFI_X86_64, "ipxe.efi"},
		{iana.EFI_BC, "ipxe.efi"},
		{iana.EFI_ARM64, "ipxe-arm64.efi"},
		{iana.EFI_X86_64_HTTP, "undionly.kpxe"},
	}
	for _, tc := range cases {
		t.Run(tc.arch.String(), func(t *testing.T) {
			pkt := pxePacket(t, dhcpv4.MessageTypeRequest, tc.arch,
				dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 50)),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(testServerIP)))
			reply := mustHandle(t, pkt, testConfig())

			if reply.MessageType() != dhcpv4.MessageTypeAck {
				t.Fatalf("message type = %v, want ACK", reply.MessageType())
			}
			if reply.BootFileName != tc.want {
				t.Errorf("file = %q, want %q", reply.BootFileName, tc.want)
			}
			if got := reply.BootFileNameOption(); got != tc.want {
				t.Errorf("option 67 = %q, want %q", got, tc.want)
			}
			if !reply.ServerIPAddr.Equal(testServerIP) {
				t.Errorf("siaddr = %v, want %v", reply.ServerIPAddr, testServerIP)
			}
			if got := reply.TFTPServerName(); got != testServerIP.String() {
				t.Errorf("option 66 = %q, want %q", got, testServerIP)
			}
			if reply.ClassIdentifier() != "PXEClient" {
				t.Errorf("option 60 = %q, want PXEClient", reply.ClassIdentifier())
			}
			if !reply.ClientIPAddr.Equal(net.IPv4(192, 168, 1, 50)) {
				t.Errorf("ciaddr = %v, want copied from request", reply.ClientIPAddr)
			}
			subs := subOptions(t, reply)
			if got := subs[pxeBootItem]; !bytes.Equal(got, []byte{0, 0, 0, 0}) {
				t.Errorf("PXE_BOOT_ITEM = %x, want 00000000", got)
			}
			if len(subs) != 1 {
				t.Errorf("ACK option 43 should only carry PXE_BOOT_ITEM, got %v", subs)
			}
		})
	}
}

func TestRequestCopiesClientFields(t *testing.T) {
	relay := net.IPv4(10, 0, 0, 1)
	pkt := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64,
		dhcpv4.WithBroadcast(true), dhcpv4.WithGatewayIP(relay))
	c := testConfig()
	c.Relay = true
	reply := mustHandle(t, pkt, c)
	if reply.TransactionID != pkt.TransactionID {
		t.Errorf("xid not copied")
	}
	if !bytes.Equal(reply.ClientHWAddr, testMAC) {
		t.Errorf("chaddr not copied")
	}
	if !reply.IsBroadcast() {
		t.Errorf("broadcast flag not copied")
	}
	if !reply.GatewayIPAddr.Equal(relay) {
		t.Errorf("giaddr = %v, want %v", reply.GatewayIPAddr, relay)
	}
}

func TestSilentWithoutClassIdentifier(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC, dhcpv4.WithoutOption(dhcpv4.OptionClassIdentifier))
	mustBeSilent(t, pkt, testConfig())
}

func TestSilentForNonPXEClass(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC,
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("MSFT 5.0")))
	mustBeSilent(t, pkt, testConfig())
}

func TestSilentWithoutArchitecture(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC,
		dhcpv4.WithoutOption(dhcpv4.OptionClientSystemArchitectureType))
	mustBeSilent(t, pkt, testConfig())
}

func TestRelayedPacketsNeedRelayFlag(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC, dhcpv4.WithGatewayIP(net.IPv4(10, 0, 0, 1)))
	mustBeSilent(t, pkt, testConfig())

	c := testConfig()
	c.Relay = true
	reply := mustHandle(t, pkt, c)
	if reply.MessageType() != dhcpv4.MessageTypeOffer {
		t.Errorf("message type = %v, want OFFER", reply.MessageType())
	}
}

func TestSilentForOtherServersAndOwnReplies(t *testing.T) {
	other := pxePacket(t, dhcpv4.MessageTypeRequest, iana.INTEL_X86PC,
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(192, 168, 1, 1))))
	mustBeSilent(t, other, testConfig())

	discover := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC)
	offer := mustHandle(t, discover, testConfig())
	mustBeSilent(t, offer, testConfig())

	inform := pxePacket(t, dhcpv4.MessageTypeInform, iana.INTEL_X86PC)
	mustBeSilent(t, inform, testConfig())
}

func TestIPXEUserClassGetsScriptNotBinary(t *testing.T) {
	for name, mod := range map[string]dhcpv4.Modifier{
		"raw":     dhcpv4.WithUserClass("iPXE", false),
		"rfc3004": dhcpv4.WithUserClass("iPXE", true),
	} {
		t.Run(name, func(t *testing.T) {
			pkt := pxePacket(t, dhcpv4.MessageTypeRequest, iana.INTEL_X86PC, mod)
			reply := mustHandle(t, pkt, testConfig())
			if reply.BootFileName != "booty.ipxe" {
				t.Errorf("file = %q, want booty.ipxe", reply.BootFileName)
			}
			if got := reply.BootFileNameOption(); got != "booty.ipxe" {
				t.Errorf("option 67 = %q, want booty.ipxe", got)
			}
			if reply.Options.Has(dhcpv4.OptionEtherboot) {
				t.Error("reply must not carry option 175")
			}
			if !reply.ServerIPAddr.Equal(testServerIP) {
				t.Errorf("siaddr = %v, want %v", reply.ServerIPAddr, testServerIP)
			}
		})
	}
}

func TestConfigFileOverrides(t *testing.T) {
	c := Config{ServerIP: testServerIP, BIOSFile: "custom.kpxe", EFIFile: "custom.efi", ARM64File: "arm.efi", IPXEScript: "menu.ipxe"}
	for _, tc := range []struct {
		arch iana.Arch
		mods []dhcpv4.Modifier
		want string
	}{
		{iana.INTEL_X86PC, nil, "custom.kpxe"},
		{iana.EFI_X86_64, nil, "custom.efi"},
		{iana.EFI_ARM64, nil, "arm.efi"},
		{iana.INTEL_X86PC, []dhcpv4.Modifier{dhcpv4.WithUserClass("iPXE", false)}, "menu.ipxe"},
	} {
		reply := mustHandle(t, pxePacket(t, dhcpv4.MessageTypeRequest, tc.arch, tc.mods...), c)
		if reply.BootFileName != tc.want {
			t.Errorf("arch %v: file = %q, want %q", tc.arch, reply.BootFileName, tc.want)
		}
	}
}

func TestReplyDestination(t *testing.T) {
	src := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 50), Port: 4011}
	cases := []struct {
		name string
		mods []dhcpv4.Modifier
		src  *net.UDPAddr
		want *net.UDPAddr
	}{
		{"broadcast flag", []dhcpv4.Modifier{dhcpv4.WithBroadcast(true)}, nil, &net.UDPAddr{IP: net.IPv4bcast, Port: 68}},
		{"no source address", nil, &net.UDPAddr{IP: net.IPv4zero, Port: 68}, &net.UDPAddr{IP: net.IPv4bcast, Port: 68}},
		{"unicast source", nil, src, src},
		{"ciaddr", []dhcpv4.Modifier{dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 60))}, src, &net.UDPAddr{IP: net.IPv4(192, 168, 1, 60), Port: 68}},
		{"relay", []dhcpv4.Modifier{dhcpv4.WithGatewayIP(net.IPv4(10, 0, 0, 1)), dhcpv4.WithBroadcast(true)}, nil, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 67}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC, tc.mods...)
			got := replyDestination(pkt, tc.src)
			if !got.IP.Equal(tc.want.IP) || got.Port != tc.want.Port {
				t.Errorf("destination = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseSubOptionsRejectsMalformed(t *testing.T) {
	for _, bad := range [][]byte{{6}, {6, 5, 1}, {6, 1, 3}} {
		if _, err := parseSubOptions(bad); err == nil {
			t.Errorf("parseSubOptions(%x) accepted malformed input", bad)
		}
	}
	subs, err := parseSubOptions(offerVendorOptions(testServerIP))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 4 {
		t.Errorf("offer sub-options = %v, want 4 entries", subs)
	}
}

func TestParsePorts(t *testing.T) {
	if a, b, err := ParsePorts(""); err != nil || a != 0 || b != 0 {
		t.Errorf("empty: got %d,%d,%v", a, b, err)
	}
	if a, b, err := ParsePorts(" 1067, 5011 "); err != nil || a != 1067 || b != 5011 {
		t.Errorf("valid: got %d,%d,%v", a, b, err)
	}
	for _, bad := range []string{"67", "67,68,69", "x,4011", "0,4011", "67,67", "67,70000"} {
		if _, _, err := ParsePorts(bad); err == nil {
			t.Errorf("ParsePorts(%q) accepted invalid input", bad)
		}
	}
}

func TestStartRejectsBadServerIP(t *testing.T) {
	for _, ip := range []net.IP{nil, net.IPv4zero, net.ParseIP("fe80::1")} {
		if _, err := Start(Config{ServerIP: ip, Listen: "127.0.0.1", Port67: 1, Port4011: 2}, make(chan error, 1)); err == nil {
			t.Errorf("Start accepted serverIP %v", ip)
		}
	}
	if _, err := Start(Config{ServerIP: testServerIP, Listen: "no-such-iface-xyz", Port67: 1, Port4011: 2}, make(chan error, 1)); err == nil {
		t.Error("Start accepted an unknown interface")
	}
}

func freePorts(t *testing.T) (int, int) {
	t.Helper()
	var ports []int
	for range 2 {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, c.LocalAddr().(*net.UDPAddr).Port)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return ports[0], ports[1]
}

func TestStartServesDiscoverAndShutsDown(t *testing.T) {
	p67, p4011 := freePorts(t)
	errCh := make(chan error, 2)
	srv, err := Start(Config{ServerIP: net.IPv4(127, 0, 0, 1), Listen: "127.0.0.1", Port67: p67, Port4011: p4011}, errCh)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	addrs := srv.Addrs()
	if len(addrs) != 2 || addrs[0].Port != p67 || addrs[1].Port != p4011 {
		t.Fatalf("Addrs = %v, want ports %d and %d", addrs, p67, p4011)
	}

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()

	discover := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC)
	if _, err := client.WriteToUDP(discover.ToBytes(), addrs[0]); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, maxPacket)
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _, err := client.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no OFFER received on loopback: %v", err)
	}
	offer, err := dhcpv4.FromBytes(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if offer.MessageType() != dhcpv4.MessageTypeOffer || offer.TransactionID != discover.TransactionID {
		t.Fatalf("unexpected reply: %s", offer.Summary())
	}
	if !offer.ServerIPAddr.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("siaddr = %v", offer.ServerIPAddr)
	}

	request := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64)
	if _, err := client.WriteToUDP(request.ToBytes(), addrs[1]); err != nil {
		t.Fatal(err)
	}
	n, _, err = client.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no ACK received on port 4011: %v", err)
	}
	ack, err := dhcpv4.FromBytes(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if ack.MessageType() != dhcpv4.MessageTypeAck || ack.BootFileName != "ipxe.efi" {
		t.Fatalf("unexpected reply: %s", ack.Summary())
	}

	if _, err := client.WriteToUDP([]byte("not dhcp"), addrs[0]); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		srv.Shutdown(2 * time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return")
	}
	select {
	case err := <-errCh:
		t.Fatalf("unexpected error on errCh after clean shutdown: %v", err)
	default:
	}
	if _, err := net.ListenUDP("udp4", addrs[0]); err != nil {
		t.Errorf("port %d still bound after Shutdown: %v", p67, err)
	}
}

func TestOfferVendorOptionsLayout(t *testing.T) {
	b := offerVendorOptions(testServerIP)
	want := []byte{
		6, 1, 0x07,
		8, 7, 0, 0, 1, 192, 168, 1, 10,
		9, 8, 0, 0, 5, 'B', 'o', 'o', 't', 'y',
		10, 6, 0, 'B', 'o', 'o', 't', 'y',
		255,
	}
	if !bytes.Equal(b, want) {
		t.Errorf("offerVendorOptions =\n%x\nwant\n%x", b, want)
	}
	if binary.BigEndian.Uint16(want[5:7]) != 0 {
		t.Error("boot server type must be 0x0000")
	}
}
