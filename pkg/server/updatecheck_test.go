package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
)

// TestUpdateCheckTargetVersionTable is the os × targetVersion × running
// table of the autopilot plan: rebootRequired = effectiveTarget != running,
// false when no release is cached or running is unknown, and the answer
// without a targetVersion is byte-for-byte what the fleet target gave
// before targetVersion existed.
func TestUpdateCheckTargetVersionTable(t *testing.T) {
	srv, dir := newTestServer(t)
	t.Cleanup(func() {
		state.SetCurrentFlatcarVersion("")
		state.SetCurrentCoreOSVersion("")
	})
	writeBluefinRelease(t, dir, bluefinPrevVersion, "previous")
	installBluefinFixture(t, dir)
	for _, v := range []string{"4757.2.0", "4593.2.1"} {
		writeRelease(t, dir, "flatcar", v, "flatcar_production_pxe.vmlinuz", "flatcar_production_pxe_image.cpio.gz")
	}
	for _, v := range []string{"44.20260913.2.1", "44.20260801.1.0"} {
		writeRelease(t, dir, "coreos", v, "fedora-coreos-"+v+"-live-kernel-x86_64")
	}
	macs := map[string]string{"flatcar": "aa:bb:cc:dd:ee:01", "coreos": "aa:bb:cc:dd:ee:02", "bluefin": "aa:bb:cc:dd:ee:03"}
	unknown := map[string]string{"flatcar": "9.9.9", "coreos": "9.20990101.9.9", "bluefin": "9.9.9"}
	fleet := map[string]string{"flatcar": "4757.2.0", "coreos": "44.20260913.2.1", "bluefin": bluefinTestVersion}
	older := map[string]string{"flatcar": "4593.2.1", "coreos": "44.20260801.1.0", "bluefin": bluefinPrevVersion}
	reportOS := map[string]string{"flatcar": "flatcar", "coreos": "coreos", "bluefin": "bluefin-server"}

	check := func(t *testing.T, osName, target, running string, want bool, wantTarget string) updateCheckResponse {
		t.Helper()
		body := `{"mac":"` + macs[osName] + `","hostname":"h-` + osName + `","os":"` + osName + `"`
		if target != "" {
			body += `,"targetVersion":"` + target + `"`
		}
		register(t, srv.URL, body+"}")
		q := "mac=" + macs[osName] + "&os=" + reportOS[osName]
		if running != "" {
			q += "&version=" + running
		}
		r := do(t, http.MethodGet, srv.URL+"/update-check?"+q, "")
		if r.status != 200 {
			t.Fatalf("%+v", r)
		}
		var resp updateCheckResponse
		if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.RebootRequired != want || resp.Target != wantTarget {
			t.Fatalf("%s target=%q running=%q: got %+v, want rebootRequired=%v target=%q", osName, target, running, resp, want, wantTarget)
		}
		h, _ := hardware.Get(macs[osName])
		if h.RebootPending != want {
			t.Fatalf("%s: rebootPending must follow the answer: %+v", osName, h)
		}
		return resp
	}

	for _, osName := range []string{"flatcar", "coreos", "bluefin"} {
		t.Run(osName+" no release cached", func(t *testing.T) {
			if osName != "bluefin" {
				check(t, osName, "", fleet[osName], false, "")
			}
		})
	}
	state.SetCurrentFlatcarVersion("4757.2.0")
	state.SetCurrentCoreOSVersion("44.20260913.2.1")

	for _, osName := range []string{"flatcar", "coreos", "bluefin"} {
		t.Run(osName, func(t *testing.T) {
			baseline := check(t, osName, "", older[osName], true, fleet[osName])
			check(t, osName, "", fleet[osName], false, fleet[osName])
			check(t, osName, "", "", false, fleet[osName])
			check(t, osName, "", unknown[osName], true, fleet[osName])

			check(t, osName, older[osName], older[osName], false, older[osName])
			check(t, osName, older[osName], fleet[osName], true, older[osName])
			check(t, osName, older[osName], "", false, older[osName])
			check(t, osName, fleet[osName], fleet[osName], false, fleet[osName])
			pinned := check(t, osName, fleet[osName], older[osName], true, fleet[osName])
			if pinned.Reason != baseline.Reason+" (host targetVersion)" {
				t.Fatalf("a targetVersion equal to the fleet target answers like the fleet target, marked: %q vs %q", pinned.Reason, baseline.Reason)
			}

			again := check(t, osName, "", older[osName], true, fleet[osName])
			if again != baseline {
				t.Fatalf("clearing targetVersion restores the fleet answer: %+v vs %+v", again, baseline)
			}
		})
	}

	t.Run("flatcar reasons unchanged without targetVersion", func(t *testing.T) {
		if r := check(t, "flatcar", "", "4593.2.1", true, "4757.2.0"); r.Reason != "flatcar 4593.2.1 differs from served 4757.2.0" {
			t.Fatalf("%+v", r)
		}
		if r := check(t, "flatcar", "", "4757.2.0", false, "4757.2.0"); r.Reason != "flatcar up to date" {
			t.Fatalf("%+v", r)
		}
		if r := check(t, "coreos", "", "44.20260801.1.0", true, "44.20260913.2.1"); r.Reason != "coreos 44.20260801.1.0 differs from served 44.20260913.2.1" {
			t.Fatalf("%+v", r)
		}
		if r := check(t, "bluefin", "", bluefinPrevVersion, true, bluefinTestVersion); r.Reason != "bluefin: re-image on reboot into "+bluefinTestVersion {
			t.Fatalf("%+v", r)
		}
	})
}
