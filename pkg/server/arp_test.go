package server

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func stubARP(t *testing.T, fn func(net.IP) (net.HardwareAddr, error)) {
	t.Helper()
	orig := arpLookup
	arpLookup = fn
	arpFailures.Lock()
	clear(arpFailures.m)
	arpFailures.Unlock()
	t.Cleanup(func() {
		arpLookup = orig
		arpFailures.Lock()
		clear(arpFailures.m)
		arpFailures.now = time.Now
		arpFailures.Unlock()
	})
}

func TestResolveARPTimesOut(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stubARP(t, func(net.IP) (net.HardwareAddr, error) {
		<-release
		return nil, errors.New("late")
	})
	start := time.Now()
	if _, err := resolveARP(net.ParseIP("10.0.0.9")); !errors.Is(err, errARPTimeout) {
		t.Fatalf("want a timeout, got %v", err)
	}
	if took := time.Since(start); took > 5*arpTimeout {
		t.Fatalf("lookup was not bounded: %s", took)
	}
}

func TestResolveARPNegativeCache(t *testing.T) {
	var calls atomic.Int32
	stubARP(t, func(net.IP) (net.HardwareAddr, error) {
		calls.Add(1)
		return nil, errors.New("no answer")
	})
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	arpFailures.Lock()
	arpFailures.now = func() time.Time { return now }
	arpFailures.Unlock()
	ip := net.ParseIP("10.0.0.10")
	for range 5 {
		if _, err := resolveARP(ip); err == nil {
			t.Fatal("want an error")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("a failed IP must be looked up once per TTL, got %d lookups", calls.Load())
	}
	now = now.Add(arpNegativeTTL)
	_, _ = resolveARP(ip)
	if calls.Load() != 2 {
		t.Fatalf("the negative entry must expire, got %d lookups", calls.Load())
	}
}

func TestResolveARPNegativeCacheIsBounded(t *testing.T) {
	stubARP(t, func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no") })
	for i := range arpNegativeMax + 50 {
		_, _ = resolveARP(net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)))
	}
	arpFailures.Lock()
	n := len(arpFailures.m)
	arpFailures.Unlock()
	if n > arpNegativeMax {
		t.Fatalf("negative cache grew to %d", n)
	}
}

func TestResolveARPSuccessIsNotCached(t *testing.T) {
	var calls atomic.Int32
	want := net.HardwareAddr{2, 0, 0, 0, 0, 1}
	stubARP(t, func(net.IP) (net.HardwareAddr, error) { calls.Add(1); return want, nil })
	for range 2 {
		hw, err := resolveARP(net.ParseIP("10.0.0.11"))
		if err != nil || hw.String() != want.String() {
			t.Fatalf("hw=%v err=%v", hw, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("successes are not cached (a MAC may change hands), got %d", calls.Load())
	}
}
