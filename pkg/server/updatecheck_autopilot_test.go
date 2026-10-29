package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/autopilot/controller"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
)

// TestUpdateCheckAutopilotRebootWanted drives /update-check against a live
// controller whose state holds a reboot it requested but has not seen
// begin (attempt 2 into the release the host already runs): the answer
// must flip to rebootRequired with the controller's reason, and only then.
func TestUpdateCheckAutopilotRebootWanted(t *testing.T) {
	srv, dir := newTestServer(t)
	t.Cleanup(func() { state.SetCurrentFlatcarVersion(""); setAutopilot(nil) })
	writeRelease(t, dir, "flatcar", "4757.2.0", "flatcar_production_pxe.vmlinuz", "flatcar_production_pxe_image.cpio.gz")
	state.SetCurrentFlatcarVersion("4757.2.0")
	const mac, other = "aa:bb:cc:dd:ee:41", "aa:bb:cc:dd:ee:42"
	register(t, srv.URL, `{"mac":"`+mac+`","hostname":"w1","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"`+other+`","hostname":"w2","os":"flatcar"}`)

	now := time.Now().UTC()
	st := controller.State{Version: 1, Hosts: map[string]*controller.HostState{
		mac: {Episode: &controller.Episode{MAC: mac, OS: "flatcar", Release: "4757.2.0", Target: "4757.2.0", Attempt: 2, State: hardware.AutopilotRetrying, Since: now, Started: now, RequestedAt: now}},
	}}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	ctrl, err := controller.New(controller.Options{Mode: config.AutopilotGuard, Fleet: controller.LiveFleet{}, StatePath: path, HealthWindow: 15 * time.Minute, RetryAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	setAutopilot(&autopilot.Autopilot{Settings: autopilot.Settings{Mode: config.AutopilotGuard}, Controller: ctrl})

	check := func(m, running string) updateCheckResponse {
		t.Helper()
		r := do(t, http.MethodGet, srv.URL+"/update-check?mac="+m+"&os=flatcar&version="+running, "")
		if r.status != http.StatusOK {
			t.Fatalf("%+v", r)
		}
		var resp updateCheckResponse
		if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if resp := check(mac, "4757.2.0"); !resp.RebootRequired || !strings.Contains(resp.Reason, "autopilot: attempt 2 into 4757.2.0") {
		t.Fatalf("on target but the controller wants a reboot: %+v", resp)
	}
	if h, _ := hardware.Get(mac); !h.RebootPending {
		t.Fatalf("rebootPending must follow the override: %+v", h)
	}
	if resp := check(other, "4757.2.0"); resp.RebootRequired {
		t.Fatalf("a host without an episode keeps the plain answer: %+v", resp)
	}
	if resp := check(mac, "4593.2.1"); !resp.RebootRequired || strings.HasPrefix(resp.Reason, "autopilot:") {
		t.Fatalf("a host behind its target keeps the update reason: %+v", resp)
	}

	ctrl.ObserveFetch(mac, controller.FetchKernel)
	if resp := check(mac, "4757.2.0"); resp.RebootRequired {
		t.Fatalf("once the reboot was observed the override drops: %+v", resp)
	}
}
