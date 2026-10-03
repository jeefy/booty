package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/power"
)

type powerResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Power  *struct {
		State       string `json:"state"`
		Request     string `json:"request"`
		RequestedBy string `json:"requestedBy"`
		Reason      string `json:"reason"`
	} `json:"power"`
}

func decodePower(t *testing.T, r response) powerResponse {
	t.Helper()
	var out powerResponse
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	return out
}

// newPowerTestServer registers hosts and installs a tracker without a
// cluster and without an actuator, driven by a fake probe.
func newPowerTestServer(t *testing.T, chooser *actuator.Chooser) (string, *power.Tracker) {
	t.Helper()
	srv, _ := newTestServer(t)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"worker","os":"flatcar","ip":"10.0.0.1"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"cp","os":"flatcar","role":"control-plane","ip":"10.0.0.2"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:03","hostname":"other","os":"bluefin","ip":"10.0.0.3"}`)
	opts := power.Options{
		Fleet: power.LiveFleet{},
		Probe: func(context.Context, string) (bool, string) { return false, "tcp/10250" },
		Waker: &power.Waker{Interfaces: func() ([]net.Addr, error) { return nil, nil }, Sleep: func(context.Context, time.Duration) error { return nil }},
	}
	if chooser != nil {
		opts.Actuators = chooser
	}
	tr, err := power.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	setPowerTracker(tr)
	t.Cleanup(func() { tr.Wait(); setPowerTracker(nil) })
	return srv.URL, tr
}

func setPower(t *testing.T, mac string, p *hardware.HostPower) {
	t.Helper()
	if _, err := hardware.Update(mac, func(h *hardware.Host) { h.Power = p }); err != nil {
		t.Fatal(err)
	}
}

func TestPowerEndpointWithoutTracker(t *testing.T) {
	srv, _ := newTestServer(t)
	setPowerTracker(nil)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"w","os":"flatcar"}`)
	r := do(t, http.MethodGet, srv.URL+"/power", "")
	if r.status != 200 || !strings.Contains(r.body, `"hosts":{}`) || !strings.Contains(r.body, `"summary"`) {
		t.Fatalf("%+v", r)
	}
	r = do(t, http.MethodPost, srv.URL+"/power/aa:bb:cc:dd:ee:01/reboot", "")
	if r.status != 409 {
		t.Fatalf("no tracker: %+v", r)
	}
}

func TestPowerActionsGuardsAndCodes(t *testing.T) {
	base, tr := newPowerTestServer(t, nil)
	tr.Tick(context.Background())

	r := do(t, http.MethodGet, base+"/power", "")
	if r.status != 200 {
		t.Fatalf("%+v", r)
	}
	var st struct {
		Hosts        map[string]struct{ State string }
		Capabilities struct {
			WoL      bool
			Actuator string
		}
		Summary struct{ Unreachable, Up int }
		Events  []struct{ Kind string }
	}
	if err := json.Unmarshal([]byte(r.body), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Hosts) != 3 || !st.Capabilities.WoL || st.Capabilities.Actuator != actuator.NameNone || st.Summary.Unreachable != 3 || st.Events == nil {
		t.Fatalf("%s", r.body)
	}

	for _, c := range []struct {
		method, path, body string
		status             int
		contains           string
	}{
		{http.MethodGet, "/power/aa:bb:cc:dd:ee:01/reboot", "", 405, "method not allowed"},
		{http.MethodPost, "/power/not-a-mac/reboot", "", 400, "invalid MAC"},
		{http.MethodPost, "/power/aa:bb:cc:dd:ee:ff/reboot", "", 404, "host not registered"},
		{http.MethodPost, "/power/aa:bb:cc:dd:ee:01/explode", "", 404, "action must be"},
		{http.MethodPost, "/power/aa:bb:cc:dd:ee:01", "", 404, "not found"},
		{http.MethodPost, "/power/aa:bb:cc:dd:ee:01/reboot", "{bad", 400, "invalid JSON"},
		{http.MethodPost, "/power/aa:bb:cc:dd:ee:01/reboot", "", 409, "host is unreachable"},
	} {
		r := do(t, c.method, base+c.path, c.body)
		if r.status != c.status || !strings.Contains(r.body, c.contains) {
			t.Errorf("%s %s: want %d %q, got %+v", c.method, c.path, c.status, c.contains, r)
		}
	}

	setPower(t, "aa:bb:cc:dd:ee:01", &hardware.HostPower{State: hardware.PowerUp})
	setPower(t, "aa:bb:cc:dd:ee:02", &hardware.HostPower{State: hardware.PowerUp})
	setPower(t, "aa:bb:cc:dd:ee:03", &hardware.HostPower{State: hardware.PowerUp})

	r = do(t, http.MethodPost, base+"/power/aa:bb:cc:dd:ee:02/reboot", `{"reason":"x"}`)
	if r.status != 409 || !strings.Contains(r.body, "control-plane") {
		t.Fatalf("control plane guard: %+v", r)
	}
	r = do(t, http.MethodPost, base+"/power/aa:bb:cc:dd:ee:01/reboot", `{"force":true}`)
	if r.status != 409 || !strings.Contains(r.body, actuator.ErrNoOperatorActuator.Error()) {
		t.Fatalf("no actuator: %+v", r)
	}
	if p := decodePower(t, r); p.Power == nil || p.Power.State != hardware.PowerUp {
		t.Fatalf("409 carries the current state: %s", r.body)
	}

	r = do(t, http.MethodPost, base+"/power/aa:bb:cc:dd:ee:01/on", "")
	if r.status != 409 || !strings.Contains(r.body, "host is up") {
		t.Fatalf("power on an up host: %+v", r)
	}
	setPower(t, "aa:bb:cc:dd:ee:01", &hardware.HostPower{State: hardware.PowerOff})
	r = do(t, http.MethodPost, base+"/power/aa:bb:cc:dd:ee:01/on", `{"reason":"maintenance over"}`)
	if r.status != 202 {
		t.Fatalf("power on: %+v", r)
	}
	p := decodePower(t, r)
	if p.Status != "ok" || p.Power.State != hardware.PowerPoweringOn || p.Power.Request != "on" || p.Power.RequestedBy != "127.0.0.1" || p.Power.Reason != "maintenance over" {
		t.Fatalf("%s", r.body)
	}
	r = do(t, http.MethodPost, base+"/power/aa:bb:cc:dd:ee:01/on", "")
	if r.status != 202 {
		t.Fatalf("idempotent within the window: %+v", r)
	}
	r = do(t, http.MethodPost, base+"/power/aa:bb:cc:dd:ee:01/shutdown", "")
	if r.status != 409 || !strings.Contains(r.body, "powering-on") {
		t.Fatalf("other action while in flight: %+v", r)
	}
	tr.Wait()
	tr.Tick(context.Background())

	r = do(t, http.MethodPost, base+"/power/aa:bb:cc:dd:ee:01/cancel", "")
	if r.status != 200 || decodePower(t, r).Power.State != hardware.PowerUnreachable {
		t.Fatalf("cancel: %+v", r)
	}
	h, _ := hardware.Get("aa:bb:cc:dd:ee:01")
	if h.Power.Request != "" {
		t.Fatalf("%+v", h.Power)
	}

	r = do(t, http.MethodGet, base+"/power", "")
	if !strings.Contains(r.body, `"kind":"power"`) || !strings.Contains(r.body, "power on requested") {
		t.Fatalf("events: %s", r.body)
	}
	if strings.Contains(r.body, "10.0.0.1\"") && strings.Contains(r.body, `"text":"`) {
		for _, line := range strings.Split(r.body, `"text":"`)[1:] {
			text := line[:strings.Index(line, `"`)]
			if strings.Contains(text, "10.0.0.") || strings.Contains(text, "aa:bb:cc") || strings.Contains(text, "worker") {
				t.Fatalf("event text carries PII: %s", text)
			}
		}
	}

	r = do(t, http.MethodGet, base+"/info", "")
	if !strings.Contains(r.body, `"power":{"up":0,"off":0,"unreachable":3,"inFlight":0}`) {
		t.Fatalf("/info power: %s", r.body)
	}
}

func TestPowerRebootIsRefusedWhileTheAutopilotDrives(t *testing.T) {
	srv, _ := newTestServer(t)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"w","os":"flatcar","ip":"10.0.0.1"}`)
	setPower(t, "aa:bb:cc:dd:ee:01", &hardware.HostPower{State: hardware.PowerUp})
	tr, err := power.New(power.Options{
		Fleet:     power.LiveFleet{},
		Autopilot: drivingAutopilot{},
		Actuators: actuator.NewChooser(actuator.Options{SSHKeyPath: "/dev/null"}),
		Probe:     func(context.Context, string) (bool, string) { return true, "tcp/22" },
	})
	if err != nil {
		t.Fatal(err)
	}
	setPowerTracker(tr)
	t.Cleanup(func() { tr.Wait(); setPowerTracker(nil) })
	r := do(t, http.MethodPost, srv.URL+"/power/aa:bb:cc:dd:ee:01/reboot", "")
	if r.status != 409 || !strings.Contains(r.body, "autopilot is driving this host") {
		t.Fatalf("%+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/power", "")
	if !strings.Contains(r.body, `"actuator":"ssh"`) {
		t.Fatalf("%s", r.body)
	}
}

type drivingAutopilot struct{}

func (drivingAutopilot) RebootWanted(string) (bool, string) { return false, "" }
func (drivingAutopilot) Driving(string) bool                { return true }
func (drivingAutopilot) Event(string, string, string)       {}

// TestBootSignalsReachThePowerTracker: the kernel, Ignition, /booted,
// /health and /update-check handlers feed the tracker. The kernel fetch
// counts only when it comes from the host's own address.
func TestBootSignalsReachThePowerTracker(t *testing.T) {
	base, _ := newPowerTestServer(t, nil)
	mac := "aa:bb:cc:dd:ee:01"
	setPower(t, mac, &hardware.HostPower{State: hardware.PowerOff, Request: hardware.PowerRequestShutdown})

	if r := do(t, http.MethodGet, base+"/booty.ipxe?mac="+mac, ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	h, _ := hardware.Get(mac)
	if h.Power.State != hardware.PowerOff || h.Power.Request != hardware.PowerRequestShutdown || h.IP != "10.0.0.1" {
		t.Fatalf("a kernel fetch from an address that is not the host's (10.0.0.1) must change nothing: %+v ip=%s", h.Power, h.IP)
	}

	register(t, base, `{"mac":"`+mac+`","hostname":"worker","os":"flatcar","ip":"127.0.0.1"}`)
	if r := do(t, http.MethodGet, base+"/booty.ipxe?mac="+mac, ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	h, _ = hardware.Get(mac)
	if h.Power.State != hardware.PowerBooting || h.Power.Request != "" {
		t.Fatalf("kernel fetch: %+v", h.Power)
	}
	if r := do(t, http.MethodGet, base+"/ignition.json?mac="+mac, ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	if r := do(t, http.MethodPost, base+"/booted?mac="+mac, ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	h, _ = hardware.Get(mac)
	if h.Power.State != hardware.PowerUp {
		t.Fatalf("/booted: %+v", h.Power)
	}

	setPower(t, mac, &hardware.HostPower{State: hardware.PowerUnreachable})
	if r := do(t, http.MethodPost, base+"/health?mac="+mac, `{"running":"4800.0.0"}`); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	h, _ = hardware.Get(mac)
	if h.Power.State != hardware.PowerUp || h.Power.LastSeen == "" {
		t.Fatalf("/health: %+v", h.Power)
	}

	setPower(t, mac, &hardware.HostPower{State: hardware.PowerUnknown})
	if r := do(t, http.MethodGet, base+"/update-check?mac="+mac+"&os=flatcar&version=4800.0.0", ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	h, _ = hardware.Get(mac)
	if h.Power.State != hardware.PowerUp {
		t.Fatalf("/update-check: %+v", h.Power)
	}
	if since, err := time.Parse(time.RFC3339, h.Power.LastSeen); err != nil || time.Since(since) > time.Minute {
		t.Fatalf("lastSeen %q", h.Power.LastSeen)
	}

	r := do(t, http.MethodPost, base+"/register", `{"mac":"`+mac+`","hostname":"worker","os":"flatcar","ip":"10.0.0.1"}`)
	if r.status != 200 || !strings.Contains(r.body, `"power":{"state":"up"`) {
		t.Fatalf("/register must keep the power block: %+v", r)
	}
}
