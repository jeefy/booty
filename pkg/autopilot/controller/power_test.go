package controller

import (
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

// TestRequestedOffHostIsExcluded is the power plan's rule: a host an
// operator shut down starts no episode, does not hold lastGood back, is
// skipped by the rollout, and is back in once it boots again.
func TestRequestedOffHostIsExcluded(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.flatcarHost(macA, "ehrlitan")
	h.flatcarHost(macB, "aren")
	h.healthyNode("ehrlitan", "Flatcar 4800.0.0")
	off := &hardware.HostPower{State: hardware.PowerOff, Request: hardware.PowerRequestShutdown}
	if err := h.fleet.Update(macB, func(x *hardware.Host) { x.Power = off }); err != nil {
		t.Fatal(err)
	}

	h.fetch(macB)
	if h.episode(macB) != nil {
		t.Fatal("a kernel fetch from a requested-off host (the record not yet updated by the tracker) starts no episode")
	}

	h.fetch(macA)
	h.up(macA, "4800.0.0")
	h.tick()
	if h.fleet.lastGood["flatcar"] != "4800.0.0" {
		t.Fatalf("lastGood must not wait for a host that was shut down on request: %s", h.fleet.lastGood["flatcar"])
	}

	if err := h.fleet.Update(macB, func(x *hardware.Host) { x.Power = &hardware.HostPower{State: hardware.PowerBooting} }); err != nil {
		t.Fatal(err)
	}
	h.fetch(macB)
	if e := h.episode(macB); e == nil || e.Release != "4800.0.0" {
		t.Fatalf("once back, the host is gated like any other: %+v", e)
	}
	if !h.c.Driving(macB) || h.c.Driving(macA) {
		t.Fatal("Driving must follow the active episode")
	}
}

func TestRolloutSkipsRequestedOffHost(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	add := func(mac, name string, power *hardware.HostPower) {
		h.fleet.add(hardware.Host{MAC: mac, Hostname: name, OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673", Power: power})
		h.healthyNode(name, "Bluefin Server 26.09.673")
	}
	add(macA, "a", &hardware.HostPower{State: hardware.PowerOff, Request: hardware.PowerRequestShutdown})
	add(macB, "b", nil)
	h.tick()
	if h.episode(macA) != nil {
		t.Fatal("the rollout must not pick a host that was shut down on request")
	}
	h.wantState(macB, hardware.AutopilotRolling, 1, "")
}

func TestExternalEventLandsInTheRing(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	h.c.Event(EventPower, macA, "reboot requested through the ssh actuator")
	events := h.c.Status().Events
	last := events[len(events)-1]
	if last.Kind != EventPower || last.MAC != macA || last.Text == "" || last.OS != "" {
		t.Fatalf("%+v", last)
	}
}
