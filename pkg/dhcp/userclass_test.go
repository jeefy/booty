package dhcp

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

// wirePacket encodes a PXE packet carrying opts and decodes it again, so
// option 77 reaches hasUserClass exactly as it would off the wire.
func wirePacket(t *testing.T, opts ...dhcpv4.Option) *dhcpv4.DHCPv4 {
	t.Helper()
	var mods []dhcpv4.Modifier
	for _, o := range opts {
		mods = append(mods, dhcpv4.WithOption(o))
	}
	pkt, err := dhcpv4.FromBytes(pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64, mods...).ToBytes())
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func TestHasUserClass(t *testing.T) {
	userClass := func(raw []byte) dhcpv4.Option {
		return dhcpv4.OptGeneric(dhcpv4.OptionUserClassInformation, raw)
	}
	for name, tc := range map[string]struct {
		raw  []byte
		want bool
	}{
		"bare string (iPXE)":          {[]byte("iPXE"), true},
		"RFC 3004 single entry":       {[]byte{4, 'i', 'P', 'X', 'E'}, true},
		"RFC 3004 second of two":      {[]byte{3, 'f', 'o', 'o', 4, 'i', 'P', 'X', 'E'}, true},
		"RFC 3004 other class":        {[]byte{5, 'g', 'P', 'X', 'E', '2'}, false},
		"RFC 3004 prefix only":        {[]byte{3, 'i', 'P', 'X'}, false},
		"bare string other class":     {[]byte("iPXE-legacy"), false},
		"RFC 3004 length runs past":   {[]byte{9, 'i', 'P', 'X', 'E'}, false},
		"RFC 3004 zero-length entry":  {[]byte{0, 4, 'i', 'P', 'X', 'E'}, false},
		"RFC 3004 entry is substring": {[]byte{6, 'x', 'i', 'P', 'X', 'E', 'x'}, false},
	} {
		pkt := wirePacket(t, userClass(tc.raw))
		if got := hasUserClass(pkt, ipxeUserClass); got != tc.want {
			t.Errorf("%s (% x): hasUserClass = %v, want %v (decoded %q)", name, tc.raw, got, tc.want, pkt.UserClass())
		}
	}
	if hasUserClass(wirePacket(t), ipxeUserClass) {
		t.Error("a packet without option 77 has no user class")
	}
}

func TestRFC3004UserClassGetsScript(t *testing.T) {
	pkt := wirePacket(t, dhcpv4.OptGeneric(dhcpv4.OptionUserClassInformation, []byte{4, 'i', 'P', 'X', 'E'}))
	reply, ok := handle(pkt, Config{ServerIP: testServerIP}.withDefaults(), 4011)
	if !ok {
		t.Fatal("no reply")
	}
	if reply.BootFileName != DefaultIPXEScript {
		t.Fatalf("boot file = %q, want %q", reply.BootFileName, DefaultIPXEScript)
	}
}

func TestWarnTruncatedIsRateLimited(t *testing.T) {
	var s Server
	src := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 68}
	t0 := time.Unix(1_700_000_000, 0)
	if !s.warnTruncated(src, maxPacket, t0) {
		t.Fatal("first full read must warn")
	}
	if s.warnTruncated(src, maxPacket, t0.Add(truncatedWarnEvery-time.Second)) {
		t.Fatal("second full read within the window must not warn")
	}
	if !s.warnTruncated(src, maxPacket, t0.Add(truncatedWarnEvery)) {
		t.Fatal("a full read after the window must warn again")
	}
}

// A DISCOVER larger than an Ethernet MTU (padded with site-specific
// options) is read whole and answered.
func TestServeAnswersDatagramsLargerThan1500(t *testing.T) {
	p67, p4011 := freePorts(t)
	srv, err := Start(Config{ServerIP: net.IPv4(127, 0, 0, 1), Listen: "127.0.0.1", Port67: p67, Port4011: p4011}, make(chan error, 2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Shutdown(2 * time.Second) })

	var mods []dhcpv4.Modifier
	for code := uint8(224); code < 232; code++ {
		mods = append(mods, dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.GenericOptionCode(code), bytes.Repeat([]byte{code}, 250))))
	}
	discover := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC, mods...)
	wire := discover.ToBytes()
	if len(wire) <= 1500 || len(wire) >= maxPacket {
		t.Fatalf("test packet is %d bytes, want 1500 < n < %d", len(wire), maxPacket)
	}

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.WriteToUDP(wire, srv.Addrs()[0]); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, maxPacket)
	n, _, err := client.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no OFFER for a %d-byte DISCOVER: %v", len(wire), err)
	}
	offer, err := dhcpv4.FromBytes(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if offer.MessageType() != dhcpv4.MessageTypeOffer || offer.TransactionID != discover.TransactionID {
		t.Fatalf("unexpected reply: %s", offer.Summary())
	}
}
