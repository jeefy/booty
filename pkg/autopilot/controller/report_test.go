package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/report"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

type fakePoster struct {
	mu    sync.Mutex
	err   error
	calls []report.Input
	done  chan struct{}
}

func newFakePoster() *fakePoster { return &fakePoster{done: make(chan struct{}, 16)} }

func (p *fakePoster) Post(_ context.Context, in report.Input, rep report.Report) (report.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, in)
	p.done <- struct{}{}
	if p.err != nil {
		return report.Result{}, p.err
	}
	if rep.Document() == nil || !strings.Contains(rep.Markdown, report.Marker(in.OS, in.Version, in.DMIHash)) {
		return report.Result{}, errors.New("poster got an unrendered report")
	}
	return report.Result{Action: report.ActionCreated, URL: "https://github.com/jeefy/scratch/issues/7", Number: 7}, nil
}

func (p *fakePoster) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("poster was not called")
	}
}

func (p *fakePoster) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

var reportPII = regexp.MustCompile(`([0-9a-f]{2}:){5}|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|ehrlitan|aren|[0-9a-f]{8}-[0-9a-f]{4}-`)

// bluefinQuarantine drives the canary through fail, fail and the healthy
// rollback, leaving the release in TIMEOUT with a draft report.
func bluefinQuarantine(t *testing.T, h *harness) {
	t.Helper()
	h.fleet.add(hardware.Host{MAC: macA, Hostname: "aren", IP: "192.168.1.57", OS: "bluefin", Booted: "2026-09-28T10:00:00Z", Running: "26.09.673", Canary: true, NetbootPlatform: hardware.PlatformEFI})
	h.healthyNode("aren", "Bluefin Server 26.09.673")
	h.tick()
	h.wantState(macA, hardware.AutopilotRolling, 1, "")
	journal := []string{"2026-09-28T12:02:11+0000 systemd[1]: kubelet.service: Failed with result 'exit-code'.", "2026-09-28T12:02:10+0000 kubelet[900]: node aren 192.168.1.57 40:a8:f0:12:34:56 product 4c4c4544-0031-3310-8052-b6c04f4d3732"}
	fail := func() {
		h.fetch(macA)
		h.c.ObserveBooted(macA, "")
		h.c.ObserveHealth(macA, &hardware.Health{Running: "27.01.100", FailedUnits: []string{"kubelet.service"}, JournalErrors: journal, DMI: hardware.DMI{Vendor: "HP", Product: "EliteDesk 800 G1", BIOSVersion: "L01 v02.78", ProductUUID: "4c4c4544-0031-3310-8052-b6c04f4d3732"}, Firmware: "uefi", Kernel: "6.17.1"})
	}
	if err := h.fleet.Update(macA, func(host *hardware.Host) {
		host.Health = &hardware.Health{JournalErrors: journal, DMI: hardware.DMI{Vendor: "HP", Product: "EliteDesk 800 G1", BIOSVersion: "L01 v02.78", ProductUUID: "4c4c4544-0031-3310-8052-b6c04f4d3732"}, Firmware: "uefi", Kernel: "6.17.1"}
	}); err != nil {
		t.Fatal(err)
	}
	fail()
	fail()
	h.wantState(macA, hardware.AutopilotRolledBack, 3, ClassFailedUnits)
	h.fetch(macA)
	h.up(macA, "26.09.673")
	h.tick()
	if r := h.release("bluefin", "27.01.100"); r.State != ReleaseTimeout {
		t.Fatalf("release after rollback: %+v", r)
	}
}

func TestReportsAreWrittenDraftThenFinalAndPostedForBluefin(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	dir := filepath.Join(t.TempDir(), "reports")
	poster := newFakePoster()
	h.opts.ReportsDir, h.opts.Poster, h.opts.CNI = dir, poster, report.CNI{Name: "cilium", Version: "v1.20.2"}
	h.start()
	bluefinQuarantine(t, h)

	key := "bluefin-27.01.100"
	md, js := report.Paths(dir, key)
	data, err := os.ReadFile(md)
	if err != nil {
		t.Fatalf("draft must be on disk: %v", err)
	}
	for _, want := range []string{"draft (release in TIMEOUT", "| Boot path | `uefi-http` |", "| CNI | cilium v1.20.2 |", "kubelet.service", "<!-- booty-autopilot: bluefin 27.01.100 ", "node <host> <ip> <mac> product <uuid>"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("draft lacks %q:\n%s", want, data)
		}
	}
	if m := reportPII.FindAllString(string(data), -1); len(m) != 0 {
		t.Fatalf("draft leaks %q", m)
	}
	if poster.count() != 0 {
		t.Fatal("drafts are never posted")
	}
	st := h.c.Status()
	if len(st.Reports) != 1 || !st.Reports[0].Draft || st.Reports[0].Path != "/autopilot/reports/"+key+".md" || st.Reports[0].UpdatedAt.IsZero() || st.Reports[0].PostedURL != "" {
		t.Fatalf("draft summary: %+v", st.Reports)
	}

	h.advance(61 * time.Minute)
	h.tick()
	h.wantState(macA, hardware.AutopilotRetrying, 1, "")
	h.fetch(macA)
	h.up(macA, "27.01.100", "kubelet.service")
	if r := h.release("bluefin", "27.01.100"); r.State != ReleaseQuarantined {
		t.Fatalf("release after failed retry: %+v", r)
	}
	poster.wait(t)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st = h.c.Status()
		if st.Reports[0].PostedURL != "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	rs := st.Reports[0]
	if rs.Draft || rs.PostedURL != "https://github.com/jeefy/scratch/issues/7" || rs.PostAction != report.ActionCreated || rs.PostedAt.IsZero() || rs.Attempts != 4 {
		t.Fatalf("final summary: %+v", rs)
	}
	if !h.hasEvent("report-posted https://github.com/jeefy/scratch/issues/7") || !h.hasEvent("report final") {
		t.Fatalf("events: %+v", st.Events)
	}
	data, _ = os.ReadFile(md)
	if !strings.Contains(string(data), "Status: **quarantined**") || strings.Contains(string(data), "draft (") {
		t.Fatalf("final markdown:\n%s", data)
	}
	if _, err := os.Stat(js); err != nil {
		t.Fatal(err)
	}
	poster.mu.Lock()
	in := poster.calls[0]
	poster.mu.Unlock()
	if in.OS != "bluefin" || in.Version != "27.01.100" || in.Draft || len(in.Names) != 1 || in.Names[0] != "aren" || in.DMIHash == "" || in.CNI.Name != "cilium" {
		t.Fatalf("poster input: %+v", in)
	}

	h.tick()
	h.tick()
	if poster.count() != 1 {
		t.Fatal("a posted report is not posted again")
	}

	raw, _ := os.ReadFile(h.path)
	if !strings.Contains(string(raw), "192.168.1.57") || !strings.Contains(string(raw), `"posted"`) {
		t.Fatal("state.json keeps the raw excerpt and the posted record")
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	h.start()
	if !report.Exists(dir, key) {
		t.Fatal("start-up must regenerate a missing report")
	}
	data, _ = os.ReadFile(md)
	if !strings.Contains(string(data), "Status: **quarantined**") || reportPII.MatchString(string(data)) {
		t.Fatalf("regenerated report:\n%s", data)
	}
	if poster.count() != 1 {
		t.Fatal("restart must not repost")
	}
}

func TestReportPostFailureIsAnEventAndRetriesLater(t *testing.T) {
	h := newHarness(t, config.AutopilotFull)
	poster := newFakePoster()
	poster.err = &report.HTTPError{Status: 403, URL: "POST /repos/x/y/issues", Body: "rate limit exceeded"}
	h.opts.ReportsDir, h.opts.Poster = filepath.Join(t.TempDir(), "reports"), poster
	h.start()
	bluefinQuarantine(t, h)
	h.advance(61 * time.Minute)
	h.tick()
	h.fetch(macA)
	h.up(macA, "27.01.100", "kubelet.service")
	poster.wait(t)
	deadline := time.Now().Add(5 * time.Second)
	var rs ReportSummary
	for time.Now().Before(deadline) {
		rs = h.c.Status().Reports[0]
		if rs.PostError != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rs.PostedURL != "" || !strings.Contains(rs.PostError, "403") {
		t.Fatalf("failed post: %+v", rs)
	}
	if !h.hasEvent("report-post-failed 403") {
		t.Fatalf("events: %+v", h.c.Status().Events)
	}
	h.tick()
	if poster.count() != 1 {
		t.Fatal("no retry before the backoff elapsed")
	}
	poster.err = nil
	h.advance(PostRetryAfter + time.Minute)
	h.tick()
	poster.wait(t)
	for time.Now().Before(deadline) {
		if rs = h.c.Status().Reports[0]; rs.PostedURL != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rs.PostedURL == "" || rs.PostError != "" {
		t.Fatalf("retry after backoff: %+v", rs)
	}
}

func TestFlatcarReportsAreDraftsOnly(t *testing.T) {
	h := newHarness(t, config.AutopilotGuard)
	poster := newFakePoster()
	dir := filepath.Join(t.TempDir(), "reports")
	h.opts.ReportsDir, h.opts.Poster = dir, poster
	h.start()
	h.flatcarHost(macA, "ehrlitan")
	if err := h.fleet.Update(macA, func(host *hardware.Host) {
		host.Health = &hardware.Health{Firmware: "uefi", Kernel: "6.1", DMI: hardware.DMI{Vendor: "HP", Product: "EliteDesk"}}
	}); err != nil {
		t.Fatal(err)
	}
	h.healthyNode("ehrlitan", "Flatcar Container Linux by Kinvolk 4800.0.0")
	h.fetch(macA)
	h.up(macA, "4800.0.0", "kubelet.service")
	h.fetch(macA)
	h.up(macA, "4800.0.0", "kubelet.service")
	h.fetch(macA)
	h.up(macA, "4757.2.0")
	h.healthyNode("ehrlitan", "Flatcar Container Linux by Kinvolk 4757.2.0")
	h.tick()
	h.advance(61 * time.Minute)
	h.tick()
	h.fetch(macA)
	h.up(macA, "4800.0.0", "kubelet.service")
	if r := h.release("flatcar", "4800.0.0"); r.State != ReleaseQuarantined {
		t.Fatalf("release: %+v", r)
	}
	h.tick()
	time.Sleep(20 * time.Millisecond)
	if poster.count() != 0 {
		t.Fatal("Flatcar reports must never be posted")
	}
	rs := h.c.Status().Reports[0]
	if rs.Draft || rs.PostedURL != "" || !report.Exists(dir, "flatcar-4800.0.0") {
		t.Fatalf("flatcar report: %+v", rs)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "flatcar-4800.0.0.md"))
	if !strings.Contains(string(data), "| Boot path | `uefi-pxe` |") || reportPII.MatchString(string(data)) {
		t.Fatalf("flatcar report:\n%s", data)
	}
}

func TestBootPath(t *testing.T) {
	cases := []struct {
		h        hardware.Host
		firmware string
		want     string
	}{
		{hardware.Host{OS: "bluefin", NetbootPlatform: hardware.PlatformPCBIOS}, "", report.BootPathBIOSDiskless},
		{hardware.Host{OS: "bluefin"}, hardware.FirmwareBIOS, report.BootPathBIOSDiskless},
		{hardware.Host{OS: "bluefin", SecureBoot: true, NetbootPlatform: hardware.PlatformEFI}, "uefi", report.BootPathUEFIPXE},
		{hardware.Host{OS: "bluefin", NetbootPlatform: hardware.PlatformEFI}, "", report.BootPathUEFIHTTP},
		{hardware.Host{OS: "bluefin"}, "uefi", report.BootPathUEFIHTTP},
		{hardware.Host{OS: "bluefin"}, "", report.BootPathUnknown},
		{hardware.Host{OS: "flatcar"}, "uefi", report.BootPathUEFIPXE},
		{hardware.Host{OS: "coreos"}, "bios", report.BootPathBIOSPXE},
		{hardware.Host{OS: "flatcar", SecureBoot: true}, "", report.BootPathUEFIPXE},
		{hardware.Host{OS: "flatcar"}, "", report.BootPathUnknown},
	}
	for _, tc := range cases {
		if got := bootPath(&tc.h, tc.firmware); got != tc.want {
			t.Errorf("bootPath(%+v, %q) = %q, want %q", tc.h, tc.firmware, got, tc.want)
		}
	}
}
