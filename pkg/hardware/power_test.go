package hardware

import "testing"

func TestPowerStateDefaultsToUnknown(t *testing.T) {
	var h *Host
	if got := h.PowerState(); got != PowerUnknown {
		t.Fatalf("nil host: %q", got)
	}
	h = &Host{}
	if got := h.PowerState(); got != PowerUnknown {
		t.Fatalf("no power block: %q", got)
	}
	h.Power = &HostPower{State: PowerUp}
	if got := h.PowerState(); got != PowerUp {
		t.Fatalf("up: %q", got)
	}
}

func TestPoweredOffByRequest(t *testing.T) {
	cases := []struct {
		name  string
		power *HostPower
		want  bool
	}{
		{"none", nil, false},
		{"off without request", &HostPower{State: PowerOff}, false},
		{"off after shutdown", &HostPower{State: PowerOff, Request: PowerRequestShutdown}, true},
		{"shutting down", &HostPower{State: PowerShuttingDown, Request: PowerRequestShutdown}, true},
		{"draining for shutdown", &HostPower{State: PowerDraining, Request: PowerRequestShutdown}, true},
		{"back up with the request still set", &HostPower{State: PowerUp, Request: PowerRequestShutdown}, false},
		{"rebooting", &HostPower{State: PowerRebooting, Request: PowerRequestReboot}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Host{Power: c.power}
			if got := h.PoweredOffByRequest(); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestHostPowerInFlightAndEqual(t *testing.T) {
	for _, state := range []string{PowerPoweringOn, PowerDraining, PowerRebooting, PowerShuttingDown} {
		if !(&HostPower{State: state}).InFlight() {
			t.Errorf("%s must be in flight", state)
		}
	}
	for _, state := range []string{PowerUnknown, PowerOff, PowerBooting, PowerUp, PowerUnreachable} {
		if (&HostPower{State: state}).InFlight() {
			t.Errorf("%s must not be in flight", state)
		}
	}
	var none *HostPower
	if none.InFlight() || !none.Equal(nil) || none.Equal(&HostPower{}) {
		t.Fatal("nil power block")
	}
	a := &HostPower{State: PowerUp, Probe: Probe{OK: true, Method: "tcp/22"}}
	b := *a
	if !a.Equal(&b) {
		t.Fatal("copies must be equal")
	}
	b.Probe.OK = false
	if a.Equal(&b) {
		t.Fatal("probe change must be noticed")
	}
}
