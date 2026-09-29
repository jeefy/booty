package controller

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/report"
)

func (c *Controller) reportInput(rep *Report) report.Input {
	in := report.Input{
		OS: rep.OS, Version: rep.Version, LastGood: rep.LastGood, Mode: rep.Mode, Class: rep.Class,
		RollbackResult: rep.RollbackResult, DMIHash: rep.DMIHash, Draft: rep.Draft,
		CreatedAt: rep.CreatedAt, UpdatedAt: rep.UpdatedAt,
		Hardware:      report.Hardware{Vendor: rep.Hardware.Vendor, Product: rep.Hardware.Product, BIOSVersion: rep.Hardware.BIOSVersion, Firmware: rep.Hardware.Firmware, Kernel: rep.Hardware.Kernel, BootPath: rep.Hardware.BootPath},
		Node:          report.Node{KubeletVersion: rep.Node.KubeletVersion, OSImage: rep.Node.OSImage, ContainerRuntimeVersion: rep.Node.ContainerRuntimeVersion, KernelVersion: rep.Node.KernelVersion},
		CNI:           c.opts.CNI,
		JournalErrors: slices.Clone(rep.JournalErrors),
	}
	for _, a := range rep.Attempts {
		in.Attempts = append(in.Attempts, report.Attempt{Attempt: a.Attempt, Target: a.Target, Outcome: a.Outcome, Class: a.Class, Note: a.Note, T0: a.T0, Ended: a.Ended, FailedUnits: slices.Clone(a.Signals.FailedUnits), Timeline: slices.Clone(a.Signals.Timeline)})
	}
	slices.SortStableFunc(in.Attempts, func(a, b report.Attempt) int { return a.Ended.Compare(b.Ended) })
	for _, h := range c.opts.Fleet.Hosts() {
		if h.Hostname != "" {
			in.Names = append(in.Names, h.Hostname)
		}
	}
	return in
}

// writeReport renders the stub and stores both files; a write failure is
// an alert, never a stop.
func (c *Controller) writeReport(key string, rep *Report) (report.Input, report.Report) {
	in := c.reportInput(rep)
	built := report.Build(in)
	if c.opts.ReportsDir == "" {
		return in, built
	}
	if err := report.Write(c.opts.ReportsDir, key, built); err != nil {
		c.event(EventAlert, rep.OS, rep.Version, "", "report could not be written: "+err.Error())
	}
	return in, built
}

// regenerateReports rewrites, at start-up, every report whose files are
// missing (a wiped reports directory, or state from before P4).
func (c *Controller) regenerateReports() {
	if c.opts.ReportsDir == "" {
		return
	}
	for key, rep := range c.state.Reports {
		if report.Exists(c.opts.ReportsDir, key) {
			continue
		}
		c.writeReport(key, rep)
		slog.Info("Autopilot report regenerated", "key", key, "draft", rep.Draft)
	}
}

// postable reports whether rep should be filed now: Bluefin, final, a
// poster configured, not yet posted, not in flight and past its backoff.
func (c *Controller) postable(key string, rep *Report) bool {
	return c.opts.Poster != nil && rep.OS == "bluefin" && !rep.Draft && rep.Posted == nil && !c.posting[key] && !c.now().Before(rep.NextPostAt)
}

func (c *Controller) postPendingReports() {
	for key, rep := range c.state.Reports {
		c.maybePost(key, rep)
	}
}

// maybePost files the report in the background. The controller's lock
// is never held across the network: the goroutine takes it again only to
// record the outcome as events and on the stub.
func (c *Controller) maybePost(key string, rep *Report) {
	if !c.postable(key, rep) {
		return
	}
	in, built := c.writeReport(key, rep)
	c.posting[key] = true
	c.spawn(func() { c.post(key, in, built) })
}

func (c *Controller) post(key string, in report.Input, built report.Report) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := c.opts.Poster.Post(ctx, in, built)
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.posting, key)
	rep := c.state.Reports[key]
	if rep == nil {
		return
	}
	c.dirty = true
	if err != nil {
		rep.PostError = err.Error()
		rep.NextPostAt = c.now().Add(PostRetryAfter)
		c.event(EventReport, rep.OS, rep.Version, "", "report-post-failed "+report.StatusText(err)+" ("+err.Error()+"); retrying in "+PostRetryAfter.String())
		c.save()
		return
	}
	rep.PostError = ""
	rep.NextPostAt = time.Time{}
	rep.Posted = &Posted{URL: res.URL, Number: res.Number, Action: res.Action, At: c.now()}
	c.event(EventReport, rep.OS, rep.Version, "", "report-posted "+res.URL+" ("+res.Action+")")
	c.save()
}
