package power

import (
	"bytes"
	"context"
	"net"
	"slices"
	"testing"
	"time"
)

func TestMagicPacketBytes(t *testing.T) {
	pkt, err := MagicPacket("AA-BB-CC-DD-EE-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt) != 102 {
		t.Fatalf("length %d, want 102", len(pkt))
	}
	if !bytes.Equal(pkt[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		t.Fatalf("sync stream: % x", pkt[:6])
	}
	mac := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}
	for i := range 16 {
		if got := pkt[6+6*i : 12+6*i]; !bytes.Equal(got, mac) {
			t.Fatalf("repeat %d: % x", i, got)
		}
	}
	for _, bad := range []string{"", "not-a-mac", "01:02:03:04:05:06:07:08"} {
		if _, err := MagicPacket(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func fixedInterfaces() ([]net.Addr, error) {
	return []net.Addr{
		&net.IPNet{IP: net.IPv4(192, 168, 1, 10), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.IPv4(10, 0, 0, 5).To4(), Mask: net.CIDRMask(16, 32)},
		&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
	}, nil
}

func TestDestinationsCoverBroadcastsAndTheHost(t *testing.T) {
	w := &Waker{Interfaces: fixedInterfaces}
	got := w.Destinations("192.168.1.42")
	want := []string{"10.0.255.255:9", "192.168.1.255:9", "192.168.1.42:9", "255.255.255.255:9"}
	if !slices.Equal(got, want) {
		t.Fatalf("destinations %v, want %v", got, want)
	}
	if got := w.Destinations(""); slices.Contains(got, "192.168.1.42:9") || len(got) != 3 {
		t.Fatalf("no host IP: %v", got)
	}
	if got := w.Destinations("::1"); len(got) != 3 {
		t.Fatalf("IPv6 host IP must be ignored: %v", got)
	}
}

func TestWakeSendsThreeRoundsToAListener(t *testing.T) {
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var slept []time.Duration
	w := &Waker{
		Interfaces: func() ([]net.Addr, error) { return nil, nil },
		Extra:      []string{ln.LocalAddr().String()},
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	dests, err := w.Wake(context.Background(), "aa:bb:cc:dd:ee:01", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dests, ln.LocalAddr().String()) || !slices.Contains(dests, "255.255.255.255:9") {
		t.Fatalf("destinations %v", dests)
	}
	if !slices.Equal(slept, []time.Duration{time.Second, time.Second}) {
		t.Fatalf("spacing %v", slept)
	}
	want, _ := MagicPacket("aa:bb:cc:dd:ee:01")
	buf := make([]byte, 256)
	for i := range 3 {
		_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := ln.ReadFrom(buf)
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if !bytes.Equal(buf[:n], want) {
			t.Fatalf("round %d: packet differs", i)
		}
	}
}

func TestWakeStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &Waker{Interfaces: func() ([]net.Addr, error) { return nil, nil }}
	if _, err := w.Wake(ctx, "aa:bb:cc:dd:ee:01", ""); err == nil {
		t.Fatal("cancelled context must stop the rounds")
	}
}
