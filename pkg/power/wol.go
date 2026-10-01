// Package power is Booty's view of whether each host is up and the three
// things an operator can do about it without a BMC: wake it over the LAN,
// reboot it, shut it down (docs/plans/2026-09-30-power.md). The Tracker
// derives a per-host state from the boot signals the server already
// records plus an active TCP probe; the Manager runs the actions behind
// POST /power/{mac}/{on|reboot|shutdown|cancel}.
package power

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/jeefy/booty/pkg/hardware"
)

// WoLPort is the UDP port magic packets go to.
const WoLPort = 9

// MagicPacket is the Wake-on-LAN frame for mac: six 0xFF bytes followed by
// the MAC sixteen times.
func MagicPacket(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", hardware.ErrInvalidMAC, mac)
	}
	if len(hw) != 6 {
		return nil, fmt.Errorf("%w: %q is not a 48-bit address", hardware.ErrInvalidMAC, mac)
	}
	pkt := bytes.Repeat([]byte{0xFF}, 6)
	for range 16 {
		pkt = append(pkt, hw...)
	}
	return pkt, nil
}

// Waker sends magic packets. The zero value sends to every destination
// Destinations lists, Rounds times, Spacing apart, from an unbound UDP
// socket.
type Waker struct {
	// Port defaults to WoLPort.
	Port int
	// Rounds (default 3) and Spacing (default 1 s): how often and how far
	// apart the packet is repeated.
	Rounds  int
	Spacing time.Duration
	// Interfaces lists the local interface addresses; nil reads them from
	// the kernel. Tests point it at a fixed list.
	Interfaces func() ([]net.Addr, error)
	// Extra destinations (host:port) sent to in addition to the derived
	// ones; tests aim at a local listener.
	Extra []string
	// Sleep is time.Sleep unless a test replaces it.
	Sleep func(context.Context, time.Duration) error
}

func (w *Waker) port() int {
	if w.Port > 0 {
		return w.Port
	}
	return WoLPort
}

func (w *Waker) rounds() int {
	if w.Rounds > 0 {
		return w.Rounds
	}
	return 3
}

func (w *Waker) spacing() time.Duration {
	if w.Spacing > 0 {
		return w.Spacing
	}
	return time.Second
}

func (w *Waker) sleep(ctx context.Context, d time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (w *Waker) interfaces() ([]net.Addr, error) {
	if w.Interfaces != nil {
		return w.Interfaces()
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var addrs []net.Addr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		list, err := iface.Addrs()
		if err != nil {
			continue
		}
		addrs = append(addrs, list...)
	}
	return addrs, nil
}

// Destinations is where the packet for a host at hostIP goes: the directed
// broadcast of every non-loopback IPv4 interface, the limited broadcast
// 255.255.255.255, and the host's own IP when known (the switch may still
// know the MAC). Sorted and deduplicated.
func (w *Waker) Destinations(hostIP string) []string {
	port := strconv.Itoa(w.port())
	set := map[string]bool{}
	addrs, err := w.interfaces()
	if err != nil {
		slog.Debug("Listing interfaces for Wake-on-LAN failed", "error", err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() {
			continue
		}
		mask := ipnet.Mask
		if len(mask) == 16 {
			mask = mask[12:]
		}
		if len(mask) != 4 {
			continue
		}
		bcast := make(net.IP, 4)
		for i := range bcast {
			bcast[i] = ip4[i] | ^mask[i]
		}
		set[net.JoinHostPort(bcast.String(), port)] = true
	}
	set[net.JoinHostPort("255.255.255.255", port)] = true
	if ip := net.ParseIP(hostIP); ip != nil && ip.To4() != nil && !ip.IsLoopback() {
		set[net.JoinHostPort(ip.String(), port)] = true
	}
	for _, e := range w.Extra {
		set[e] = true
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

// Wake sends the magic packet for mac to Destinations(hostIP), Rounds
// times. It returns the destinations it sent to and the first error that
// kept a round from reaching any of them; a single refused broadcast (a
// container without the right interface) is logged, not returned.
func (w *Waker) Wake(ctx context.Context, mac, hostIP string) ([]string, error) {
	pkt, err := MagicPacket(mac)
	if err != nil {
		return nil, err
	}
	dests := w.Destinations(hostIP)
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return dests, fmt.Errorf("wol: udp socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := enableBroadcast(conn); err != nil {
		slog.Debug("Could not enable SO_BROADCAST; broadcast sends may fail", "error", err)
	}
	var firstErr error
	for round := range w.rounds() {
		if round > 0 {
			if err := w.sleep(ctx, w.spacing()); err != nil {
				return dests, err
			}
		}
		sent := 0
		for _, d := range dests {
			addr, err := net.ResolveUDPAddr("udp4", d)
			if err != nil {
				continue
			}
			if _, err := conn.WriteTo(pkt, addr); err != nil {
				slog.Debug("Wake-on-LAN send failed", "to", d, "error", err)
				continue
			}
			sent++
		}
		if sent == 0 && firstErr == nil {
			firstErr = errors.New("wol: no destination accepted the packet")
		}
	}
	return dests, firstErr
}
