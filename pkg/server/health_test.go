package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/hardware"
)

// healthFixture is a report as a homelab node would send it, with the
// hostnames, addresses and MACs the plan forbids at info level.
const healthFixture = `{
  "running": "26.09.673",
  "failedUnits": ["booty-kubeadm-join.service", "var-lib-containerd.mount"],
  "journalErrors": [
    "2026-09-28T10:00:00+0000 kubelet[1234]: node aren (192.168.1.57) failed to register with ehrlitan:6443",
    "2026-09-28T10:00:01+0000 kernel: e1000e 40:a8:f0:af:39:8d link is down"
  ],
  "dmi": {"vendor": "HP", "product": "HP EliteDesk 800 G1 DM", "biosVersion": "L01 v02.78", "productUUID": "0CCCFB00-A3F6-11E4-9504-40A8F0AF398D"},
  "firmware": "uefi",
  "kernel": "6.17.1-300.fc44.x86_64",
  "bootID": "5b1c6b2e-4a9e-4e7b-9a0e-2d3f4a5b6c7d"
}`

func captureInfoLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestHealthReport(t *testing.T) {
	srv, _ := newTestServer(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })
	logs := captureInfoLogs(t)

	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/health?mac=aa:bb:cc:dd:ee:01", healthFixture), http.StatusMethodNotAllowed)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/health", healthFixture), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/health?mac=zz", healthFixture), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/health?mac=aa:bb:cc:dd:ee:01", healthFixture), http.StatusNotFound)
	if len(hardware.Snapshot().UnknownHosts) != 0 {
		t.Fatal("/health must not record unknown hosts")
	}

	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"aren","os":"bluefin"}`)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/health?mac=aa:bb:cc:dd:ee:01", `{"running":`), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/health?mac=aa:bb:cc:dd:ee:01", `{"firmware":"coreboot"}`), http.StatusBadRequest)
	big := `{"journalErrors":["` + strings.Repeat("x", 70<<10) + `"]}`
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/health?mac=aa:bb:cc:dd:ee:01", big), http.StatusRequestEntityTooLarge)
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:01"); h.Health != nil {
		t.Fatal("a refused report must not be stored")
	}

	for i := 0; i < 2; i++ {
		r := do(t, http.MethodPost, srv.URL+"/health?mac=AA-BB-CC-DD-EE-01", healthFixture)
		if r.status != 200 {
			t.Fatalf("post %d: %+v", i, r)
		}
	}
	h, _ := hardware.Get("aa:bb:cc:dd:ee:01")
	if h.Health == nil {
		t.Fatal("health not stored")
	}
	var want hardware.Health
	if err := json.Unmarshal([]byte(healthFixture), &want); err != nil {
		t.Fatal(err)
	}
	want.ReceivedAt = "2026-09-28T12:00:00Z"
	want.DMI.ProductUUID = strings.ToLower(want.DMI.ProductUUID)
	got, _ := json.Marshal(h.Health)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Fatalf("stored health\n got %s\nwant %s", got, wantJSON)
	}
	if h.Running != "26.09.673" {
		t.Fatalf("running must be set from the report when empty, got %q", h.Running)
	}
	if h.Booted != "" || h.RebootPending || h.LastCheck != "" {
		t.Fatalf("health must not touch the boot or update-check fields: %+v", h)
	}

	for _, endpoint := range []string{"/hosts?mac=aa:bb:cc:dd:ee:01", "/booty.json"} {
		r := do(t, http.MethodGet, srv.URL+endpoint, "")
		if r.status != 200 || !strings.Contains(r.body, `"health":{"receivedAt":"2026-09-28T12:00:00Z"`) || !strings.Contains(r.body, `"biosVersion":"L01 v02.78","productUUID":"0cccfb00-a3f6-11e4-9504-40a8f0af398d"`) {
			t.Fatalf("%s must show the health report: %+v", endpoint, r)
		}
	}

	out := logs.String()
	if !strings.Contains(out, "Host health reported") || !strings.Contains(out, "failedUnits=2") {
		t.Fatalf("expected an info log for the first report:\n%s", out)
	}
	if strings.Count(out, "Host health reported") != 1 {
		t.Fatalf("a repeated report from the same boot is debug only:\n%s", out)
	}
	for _, pii := range []string{"aren", "ehrlitan", "192.168.1.57", "40:a8:f0:af:39:8d", "kubelet[1234]"} {
		if strings.Contains(strings.ReplaceAll(out, `hostname=aren`, ""), pii) {
			t.Fatalf("journal contents (%q) leaked into the info-level log:\n%s", pii, out)
		}
	}
}

func TestHealthReportSetsRunningOnlyWhenEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:02","hostname":"n2","os":"flatcar","running":"4757.2.0"}`)
	r := do(t, http.MethodPost, srv.URL+"/health?mac=aa:bb:cc:dd:ee:02", `{"running":"4593.2.1","bootID":"b1","journalErrors":["`+strings.Repeat("y", 400)+`"]}`)
	if r.status != 200 {
		t.Fatalf("%+v", r)
	}
	h, _ := hardware.Get("aa:bb:cc:dd:ee:02")
	if h.Running != "4757.2.0" {
		t.Fatalf("running from the update check wins, got %q", h.Running)
	}
	if len(h.Health.JournalErrors) != 1 || len(h.Health.JournalErrors[0]) > hardware.MaxHealthJournalLine || h.Health.Firmware != "" {
		t.Fatalf("server-side caps: %+v", h.Health)
	}
	r = do(t, http.MethodPost, srv.URL+"/health?mac=aa:bb:cc:dd:ee:02", `{"running":"4593.2.1","bootID":"b2"}`)
	if r.status != 200 {
		t.Fatalf("%+v", r)
	}
	if h, _ = hardware.Get("aa:bb:cc:dd:ee:02"); h.Health.BootID != "b2" || len(h.Health.JournalErrors) != 0 {
		t.Fatalf("a new boot replaces the report: %+v", h.Health)
	}
}
