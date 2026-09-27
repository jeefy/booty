package dhcp

import (
	"encoding/hex"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden ProxyDHCP replies under testdata/golden")

// goldenCases are deterministic PXE and iPXE packets whose replies are
// pinned byte-for-byte in testdata/golden/<name>.hex. They guard the
// legacy PXE (option 60 PXEClient) and iPXE user-class paths against
// accidental changes while the HTTP-Boot path is added next to them.
func goldenCases(t *testing.T) map[string]*dhcpv4.DHCPv4 {
	t.Helper()
	xid := dhcpv4.WithTransactionID(dhcpv4.TransactionID{0xde, 0xad, 0xbe, 0xef})
	sid := dhcpv4.WithOption(dhcpv4.OptServerIdentifier(testServerIP))
	ciaddr := dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 50))
	guid := dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	return map[string]*dhcpv4.DHCPv4{
		"discover-bios":          pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC, xid, guid),
		"discover-efi-x64":       pxePacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64, xid, guid),
		"discover-efi-bc":        pxePacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_BC, xid),
		"request-bios":           pxePacket(t, dhcpv4.MessageTypeRequest, iana.INTEL_X86PC, xid, sid, ciaddr, guid),
		"request-efi-x64":        pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64, xid, sid, ciaddr, guid),
		"request-efi-bc":         pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_BC, xid, sid, ciaddr),
		"request-efi-arm64":      pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_ARM64, xid, sid, ciaddr),
		"request-ipxe-userclass": pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64, xid, sid, ciaddr, dhcpv4.WithUserClass("iPXE", false)),
		"request-ipxe-rfc3004":   pxePacket(t, dhcpv4.MessageTypeRequest, iana.INTEL_X86PC, xid, sid, ciaddr, dhcpv4.WithUserClass("iPXE", true)),
	}
}

func TestGoldenPXEReplies(t *testing.T) {
	dir := filepath.Join("testdata", "golden")
	if *updateGolden {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, pkt := range goldenCases(t) {
		t.Run(name, func(t *testing.T) {
			reply := mustHandle(t, pkt, testConfig())
			got := hex.EncodeToString(reply.ToBytes())
			file := filepath.Join(dir, name+".hex")
			if *updateGolden {
				if err := os.WriteFile(file, []byte(got+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("missing golden file (run with -update): %v", err)
			}
			if strings.TrimSpace(string(want)) != got {
				t.Errorf("reply bytes changed for %s\n got: %s\nwant: %s", name, got, strings.TrimSpace(string(want)))
			}
		})
	}
}
