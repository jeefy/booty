package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/hardware"
)

// ObserveFetch is L0: kind is FetchKernel for a kernel/UKI (or boot
// script) fetch and FetchIgnition for the Ignition config. A kernel fetch
// starts an episode when the host boots a release it has not passed the
// gate on, starts the gate clock of an attempt the controller is waiting
// for, and is a boot loop when it repeats before /booted.
func (c *Controller) ObserveFetch(mac, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.opts.Fleet.Host(mac)
	if !ok || h.Installed() {
		return
	}
	now := c.now()
	e := c.activeEpisode(mac)
	switch kind {
	case FetchIgnition:
		if e != nil && e.State == hardware.AutopilotGating && e.Signals.Ignition.IsZero() {
			e.Signals.Ignition = now
			c.timeline(e, "ignition fetched")
		}
	case FetchKernel:
		c.observeKernelFetch(h, e, now)
	}
	c.applyHolds()
	c.save()
}

func (c *Controller) observeKernelFetch(h *hardware.Host, e *Episode, now time.Time) {
	target := c.opts.Fleet.EffectiveTarget(h)
	if e == nil {
		if target == "" || target == c.healthyOn(h) {
			return
		}
		e = c.startEpisode(h, target, "host booted a release it has not passed the gate on")
		c.startGate(e, now)
		return
	}
	switch e.State {
	case hardware.AutopilotGating:
		e.Signals.Fetches++
		if e.Signals.Booted.IsZero() || e.Signals.Fetches >= 3 {
			c.timeline(e, "kernel fetched again")
			c.fail(e, ClassBootLoop, fmt.Sprintf("kernel fetched %d times before the OS came up", e.Signals.Fetches))
			return
		}
		c.timeline(e, "kernel fetched again after the OS came up; gate restarted")
		c.startGate(e, now)
	default:
		if target != e.Target {
			c.event(EventEpisode, e.OS, e.Release, e.MAC, fmt.Sprintf("host booted %s instead of the requested %s; gating what it boots", target, e.Target))
			e.Target = target
		}
		c.startGate(e, now)
	}
}

func (c *Controller) startEpisode(h *hardware.Host, release, why string) *Episode {
	now := c.now()
	hs := c.state.hostState(h.MAC)
	e := &Episode{MAC: h.MAC, OS: hostOS(h), Release: release, Target: release, Attempt: 1, State: hardware.AutopilotRolling, Since: now, Started: now}
	hs.Episode = e
	c.release(e.OS, release)
	c.dirty = true
	c.event(EventEpisode, e.OS, release, h.MAC, "episode started: "+why)
	return e
}

func (c *Controller) startGate(e *Episode, at time.Time) {
	fetches := 1
	if e.State == hardware.AutopilotGating {
		fetches = e.Signals.Fetches
	}
	e.Signals = Signals{Fetches: fetches}
	e.T0 = at
	e.State, e.Since = hardware.AutopilotGating, at
	c.timeline(e, "kernel fetched")
	if e.Baseline == nil {
		c.takeBaseline(e)
	}
	c.dirty = true
	c.event(EventEpisode, e.OS, e.Release, e.MAC, fmt.Sprintf("attempt %d into %s: reboot observed, health gate started", e.Attempt, e.Target))
}

func (c *Controller) takeBaseline(e *Episode) {
	node := c.nodeName(e.MAC)
	if c.opts.Cluster == nil || node == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := c.opts.Cluster.Sample(ctx, node)
	if err != nil {
		e.Note = "baseline unavailable: " + classifyErr(err)
		return
	}
	e.Baseline = &s
}

func (c *Controller) nodeName(mac string) string {
	h, ok := c.opts.Fleet.Host(mac)
	if !ok {
		return ""
	}
	return h.Hostname
}

func classifyErr(err error) string {
	switch {
	case k8s.IsNotFound(err):
		return "node not found"
	case k8s.IsForbidden(err):
		return "forbidden (RBAC)"
	case k8s.IsUnreachable(err):
		return "API server unreachable"
	}
	return "cluster error"
}

func (c *Controller) timeline(e *Episode, what string) {
	if len(e.Signals.Timeline) >= maxTimelineEntries {
		return
	}
	rel := time.Duration(0)
	if !e.T0.IsZero() {
		rel = c.now().Sub(e.T0).Round(time.Second)
	}
	e.Signals.Timeline = append(e.Signals.Timeline, fmt.Sprintf("t+%s %s", rel, what))
	c.dirty = true
}

// ObserveBooted is L1: POST /booted from the running OS. running is the
// version the node reports when it says so (optional).
func (c *Controller) ObserveBooted(mac, running string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.activeEpisode(mac)
	if e == nil || e.State != hardware.AutopilotGating {
		return
	}
	if e.Signals.Booted.IsZero() {
		e.Signals.Booted = c.now()
		c.timeline(e, "OS up (booted)")
	}
	if running != "" {
		e.Signals.Running = running
	}
	c.dirty = true
	c.save()
}

// ObserveHealth is the rest of L1: the node's health report. Failed units
// fail the attempt at once; otherwise the gate waits for L2.
func (c *Controller) ObserveHealth(mac string, report *hardware.Health) {
	if report == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.activeEpisode(mac)
	if e == nil || e.State != hardware.AutopilotGating {
		return
	}
	now := c.now()
	if e.Signals.Booted.IsZero() {
		e.Signals.Booted = now
		c.timeline(e, "OS up (health report)")
	}
	e.Signals.Health = now
	e.Signals.Running = report.Running
	e.Signals.FailedUnits = append([]string(nil), report.FailedUnits...)
	if e.Target == e.Release {
		e.Journal = append([]string(nil), report.JournalErrors...)
	}
	c.timeline(e, fmt.Sprintf("health reported: %d failed unit(s)", len(report.FailedUnits)))
	c.dirty = true
	if len(report.FailedUnits) > 0 {
		c.fail(e, ClassFailedUnits, fmt.Sprintf("%d failed unit(s): %s", len(report.FailedUnits), strings.Join(report.FailedUnits, ", ")))
		c.applyHolds()
	}
	c.save()
}

// evaluateGates is the time-driven half of the gate: no-ignition, the
// cluster poll, the window, and hung detection for reboots that never
// came.
func (c *Controller) evaluateGates(ctx context.Context) {
	for mac, hs := range c.state.Hosts {
		e := hs.Episode
		if !e.Active() {
			continue
		}
		if e.MAC == "" {
			e.MAC = mac
		}
		switch e.State {
		case hardware.AutopilotGating:
			c.evaluateGate(ctx, e)
		default:
			c.evaluatePendingReboot(ctx, e)
		}
	}
}

func (c *Controller) evaluateGate(ctx context.Context, e *Episode) {
	now := c.now()
	elapsed := now.Sub(e.T0)
	if e.Signals.Ignition.IsZero() && e.Signals.Booted.IsZero() && elapsed >= c.opts.IgnitionGrace {
		c.fail(e, ClassNoIgnition, fmt.Sprintf("no Ignition fetch %s after the kernel", elapsed.Round(time.Second)))
		return
	}
	l1 := c.l1Passed(e)
	var l2 gateResult
	if l1 {
		l2 = c.evaluateL2(ctx, e)
		e.Note = l2.note
		if l2.pass {
			c.healthy(e)
			return
		}
	} else {
		e.Note = c.l1Note(e)
	}
	if elapsed >= c.opts.HealthWindow {
		class := ClassTimeout
		if l1 && l2.class != "" {
			class = l2.class
		}
		c.fail(e, class, "health window elapsed: "+e.Note)
	}
}

func (c *Controller) l1Passed(e *Episode) bool {
	if e.Signals.Booted.IsZero() || e.Signals.Health.IsZero() || len(e.Signals.FailedUnits) > 0 {
		return false
	}
	return e.Signals.Running == "" || e.Signals.Running == e.Target
}

func (c *Controller) l1Note(e *Episode) string {
	switch {
	case e.Signals.Booted.IsZero():
		return "waiting for the OS to come up (/booted)"
	case e.Signals.Health.IsZero():
		return "waiting for the health report"
	case e.Signals.Running != "" && e.Signals.Running != e.Target:
		return "health report says the host runs " + e.Signals.Running + ", not " + e.Target
	}
	return ""
}

type gateResult struct {
	pass  bool
	class string
	note  string
}

// evaluateL2 is the cluster side of the gate: Node Ready and stable, its
// osImage not naming another cached release, DaemonSet pods Ready, and
// no pod newly in CrashLoopBackOff/Error/ImagePullBackOff or Pending for
// longer than PendingGrace. Without a cluster or a hostname L2 passes with
// a note.
func (c *Controller) evaluateL2(ctx context.Context, e *Episode) gateResult {
	node := c.nodeName(e.MAC)
	if c.opts.Cluster == nil {
		return gateResult{pass: true, note: "no cluster client; gate is L1 only"}
	}
	if node == "" {
		return gateResult{pass: true, note: "host has no hostname, so no node to check; gate is L1 only"}
	}
	s, err := c.opts.Cluster.Sample(ctx, node)
	if err != nil {
		return gateResult{class: ClassNodeNotReady, note: "node sample: " + classifyErr(err)}
	}
	now := c.now()
	c.notePending(e, s, now)
	if !s.Node.Ready {
		return gateResult{class: ClassNodeNotReady, note: "node not Ready (" + s.Node.ReadyReason + ")"}
	}
	if stale := c.staleOSImage(e, s.Node.NodeInfo.OSImage); stale != "" {
		return gateResult{class: ClassNodeNotReady, note: "node still reports osImage of " + stale}
	}
	if c.opts.NodeStable > 0 && !s.Node.ReadySince.IsZero() && now.Sub(s.Node.ReadySince) < c.opts.NodeStable {
		return gateResult{class: ClassNodeNotReady, note: fmt.Sprintf("node Ready for %s, waiting for %s of stability", now.Sub(s.Node.ReadySince).Round(time.Second), c.opts.NodeStable)}
	}
	for _, key := range slices.Sorted(maps.Keys(s.Pods)) {
		p := s.Pods[key]
		if p.Owner == "Job" || p.Phase == "Succeeded" {
			continue
		}
		var base *k8s.Pod
		if e.Baseline != nil {
			if b, ok := e.Baseline.Pods[key]; ok {
				base = &b
			}
		}
		switch {
		case p.Owner == "DaemonSet" && !p.Healthy():
			if base != nil && !base.Healthy() {
				continue
			}
			return gateResult{class: ClassWorkloads, note: "DaemonSet pod " + key + " not Ready (" + podState(p) + ")"}
		case p.Waiting == "CrashLoopBackOff" || p.Waiting == "ImagePullBackOff" || p.Waiting == "ErrImagePull" || p.Phase == "Failed" || p.Waiting == "Error":
			if base != nil && (base.Waiting == p.Waiting || base.Phase == p.Phase) {
				continue
			}
			return gateResult{class: ClassWorkloads, note: "pod " + key + " " + podState(p)}
		case p.Phase == "Pending":
			if base != nil && base.Phase == "Pending" {
				continue
			}
			if first := e.Signals.PendingSince[key]; now.Sub(first) >= c.opts.PendingGrace {
				return gateResult{class: ClassWorkloads, note: fmt.Sprintf("pod %s Pending for %s", key, now.Sub(first).Round(time.Second))}
			}
		}
	}
	return gateResult{pass: true, note: "node Ready, workloads healthy"}
}

func (c *Controller) notePending(e *Episode, s k8s.Sample, now time.Time) {
	if e.Signals.PendingSince == nil {
		e.Signals.PendingSince = map[string]time.Time{}
	}
	for key, p := range s.Pods {
		if p.Phase != "Pending" {
			delete(e.Signals.PendingSince, key)
			continue
		}
		if _, seen := e.Signals.PendingSince[key]; !seen {
			e.Signals.PendingSince[key] = now
			c.dirty = true
		}
	}
}

func podState(p k8s.Pod) string {
	if p.Waiting != "" {
		return p.Waiting
	}
	return fmt.Sprintf("%s %d/%d ready", p.Phase, p.Ready, p.Containers)
}

// staleOSImage returns the cached release the node's osImage names when
// that is not the target: the kubelet has not reported the new boot yet.
func (c *Controller) staleOSImage(e *Episode, osImage string) string {
	if osImage == "" {
		return ""
	}
	if strings.Contains(osImage, e.Target) {
		return ""
	}
	for _, v := range c.opts.Fleet.Cached(e.OS) {
		if v != e.Target && strings.Contains(osImage, v) {
			return v
		}
	}
	return ""
}

// evaluatePendingReboot watches a host the controller asked to reboot but
// has not seen fetch a kernel yet: a node NotReady for a whole window, or a
// reboot an actuator issued a window ago with nothing fetched since, is
// hung.
func (c *Controller) evaluatePendingReboot(ctx context.Context, e *Episode) {
	now := c.now()
	if e.RequestedAt.IsZero() {
		return
	}
	waited := now.Sub(e.RequestedAt)
	if !e.IssuedAt.IsZero() && now.Sub(e.IssuedAt) >= c.opts.HealthWindow {
		c.fail(e, ClassHung, fmt.Sprintf("nothing fetched %s after the %s actuator rebooted the host", now.Sub(e.IssuedAt).Round(time.Second), e.Actuator))
		return
	}
	if waited >= c.opts.HealthWindow && (e.Class == ClassBootLoop || e.Class == ClassNoIgnition) {
		c.fail(e, ClassHung, fmt.Sprintf("nothing fetched %s after %s; the machine did not come back on its own", waited.Round(time.Second), e.Class))
		return
	}
	node := c.nodeName(e.MAC)
	if c.opts.Cluster == nil || node == "" || waited < c.opts.HealthWindow {
		return
	}
	s, err := c.opts.Cluster.Sample(ctx, node)
	if err != nil {
		e.Note = "waiting for the reboot; node sample: " + classifyErr(err)
		return
	}
	if !s.Node.Ready && now.Sub(s.Node.ReadySince) >= c.opts.HealthWindow {
		c.fail(e, ClassHung, fmt.Sprintf("node NotReady for %s and nothing fetched since the reboot was requested", now.Sub(s.Node.ReadySince).Round(time.Second)))
		return
	}
	e.Note = "waiting for the host to reboot (" + e.Actuator + ")"
}

func (c *Controller) finishAttempt(e *Episode, outcome, class, note string) {
	e.Attempts = append(e.Attempts, Attempt{Attempt: e.Attempt, Target: e.Target, Outcome: outcome, Class: class, T0: e.T0, Ended: c.now(), Note: note, Signals: e.Signals, Actuator: e.Actuator})
	r := c.release(e.OS, e.Release)
	if e.Target == e.Release {
		r.Attempts++
		if class != "" {
			r.Class = class
		}
	}
	c.dirty = true
}

// fail ends the current attempt with class and decides what comes next.
func (c *Controller) fail(e *Episode, class, note string) {
	c.finishAttempt(e, OutcomeFailed, class, note)
	e.Class = class
	r := c.release(e.OS, e.Release)
	c.event(EventEpisode, e.OS, e.Release, e.MAC, fmt.Sprintf("attempt %d into %s failed: %s (%s)", e.Attempt, e.Target, class, note))
	switch {
	case e.RollingBack:
		c.needsHands(e, "failed on lastGood "+e.Target+" too: the node is sick, not the release")
	case class == ClassHung:
		c.needsHands(e, "the node fetched nothing within the window; no actuator can reach a machine that is not up, and it has no BMC")
	case e.Retry:
		c.quarantine(r, e)
	case e.Attempt == 1:
		r.Failing = true
		e.Attempt = 2
		c.pin(e.MAC, e.Target)
		c.requestReboot(e, hardware.AutopilotRetrying, "attempt 2 into "+e.Target)
	default:
		c.rollback(e, r)
	}
}

func (c *Controller) rollback(e *Episode, r *Release) {
	lastGood := c.opts.Fleet.LastGood(e.OS)
	if lastGood == "" || lastGood == e.Release {
		c.needsHands(e, "failed twice and there is no other lastGood to roll back to")
		return
	}
	e.RollingBack = true
	e.Target = lastGood
	e.Attempt++
	c.pin(e.MAC, lastGood)
	r.Failing = true
	c.requestReboot(e, hardware.AutopilotRolledBack, "rolling back to lastGood "+lastGood)
}

func (c *Controller) quarantine(r *Release, e *Episode) {
	c.setReleaseState(r, ReleaseQuarantined)
	r.Failing = false
	c.event(EventRelease, r.OS, r.Version, "", "release quarantined: the retry failed too; the fleet stays on lastGood")
	c.draftReport(r, e, "retry failed with "+e.Class, true)
	if e.Release != c.opts.Fleet.LastGood(e.OS) && c.opts.Fleet.LastGood(e.OS) != "" {
		e.RollingBack = true
		e.Target = c.opts.Fleet.LastGood(e.OS)
		e.Attempt++
		c.pin(e.MAC, e.Target)
		c.requestReboot(e, hardware.AutopilotRolledBack, "back to lastGood "+e.Target+" after the failed retry")
		return
	}
	c.needsHands(e, "the retry failed and there is no lastGood to fall back to")
}

// requestReboot asks for the host to boot e.Target: through the chosen
// actuator when one exists, else by waiting for the host's own update
// timer (RebootWanted makes /update-check say so).
func (c *Controller) requestReboot(e *Episode, state, why string) {
	now := c.now()
	e.State, e.Since, e.RequestedAt, e.IssuedAt, e.T0 = state, now, now, time.Time{}, time.Time{}
	e.Signals = Signals{}
	e.Note = "waiting for the host to reboot"
	c.dirty = true
	if c.opts.Actuators == nil {
		e.Actuator = ActuatorNone
		c.event(EventActuator, e.OS, e.Release, e.MAC, why+": no actuator configured; the controller cannot force a reboot and waits for the host's update timer")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := c.opts.Actuators.Choose(ctx)
	if err != nil {
		e.Actuator = ActuatorNone
		c.event(EventActuator, e.OS, e.Release, e.MAC, why+": no actuator available ("+err.Error()+"); waiting for the host's update timer")
		return
	}
	e.Actuator = r.Name()
	c.actuator = e.Actuator
	h, ok := c.opts.Fleet.Host(e.MAC)
	if !ok {
		return
	}
	target := actuator.FromHardware(h)
	if r.Name() == actuator.NameKured {
		c.event(EventActuator, e.OS, e.Release, e.MAC, why+": reboot delegated to kured (update-check answers rebootRequired until the host boots "+e.Target+")")
		return
	}
	c.event(EventActuator, e.OS, e.Release, e.MAC, why+": draining and rebooting through the "+r.Name()+" actuator")
	mac, attempt := e.MAC, e.Attempt
	c.spawn(func() { c.actuate(r, target, mac, attempt) })
}

func (c *Controller) actuate(r actuator.Rebooter, host actuator.Host, mac string, attempt int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	err := r.Prepare(ctx, host)
	if err == nil {
		err = r.Reboot(ctx, host)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.activeEpisode(mac)
	if e == nil || e.Attempt != attempt {
		return
	}
	if err != nil {
		e.Note = "actuator failed: " + err.Error()
		c.event(EventActuator, e.OS, e.Release, mac, r.Name()+" actuator failed: "+err.Error()+"; waiting for the host's update timer instead")
	} else {
		e.IssuedAt = c.now()
		c.event(EventActuator, e.OS, e.Release, mac, r.Name()+" actuator: reboot under way")
	}
	c.save()
}

func (c *Controller) finishActuator(e *Episode) {
	if e.Actuator != actuator.NameAPI && e.Actuator != actuator.NameSSH || c.opts.Actuators == nil {
		return
	}
	h, ok := c.opts.Fleet.Host(e.MAC)
	if !ok {
		return
	}
	name, host := e.Actuator, actuator.FromHardware(h)
	os, release, mac := e.OS, e.Release, e.MAC
	c.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		r, err := c.opts.Actuators.Choose(ctx)
		if err != nil || r.Name() != name {
			return
		}
		if err := r.Finish(ctx, host); err != nil {
			c.mu.Lock()
			c.event(EventActuator, os, release, mac, name+" actuator: uncordon failed: "+err.Error())
			c.save()
			c.mu.Unlock()
		}
	})
}

// healthy ends the current attempt as passed.
func (c *Controller) healthy(e *Episode) {
	c.finishAttempt(e, OutcomeHealthy, "", e.Note)
	r := c.release(e.OS, e.Release)
	hs := c.state.hostState(e.MAC)
	hs.HealthyOn = e.Target
	e.Done = true
	e.Since = c.now()
	c.finishActuator(e)
	if e.RollingBack {
		e.State = hardware.AutopilotRolledBack
		c.event(EventEpisode, e.OS, e.Release, e.MAC, "healthy on lastGood "+e.Target+": the release is bad, not the node")
		c.unpin(e.MAC)
		if r.State != ReleaseQuarantined {
			c.setReleaseState(r, ReleaseTimeout)
			r.FailedOn = e.MAC
			r.Failing = false
			c.event(EventRelease, e.OS, e.Release, "", fmt.Sprintf("release enters TIMEOUT for %s; fleet target held at lastGood %s", c.opts.RetryAfter, e.Target))
			c.draftReport(r, e, "lastGood "+e.Target+" healthy on the same hardware", false)
		}
		return
	}
	e.State = hardware.AutopilotIdle
	if !contains(r.Healthy, e.MAC) {
		r.Healthy = append(r.Healthy, e.MAC)
	}
	if r.FailedOn == e.MAC || e.Attempt > 1 {
		r.Failing = false
	}
	if e.Retry {
		c.setReleaseState(r, ReleaseRolling)
		r.FailedOn = ""
		c.event(EventRelease, e.OS, e.Release, "", "retry healthy: release leaves TIMEOUT, rollout resumes")
	}
	c.event(EventEpisode, e.OS, e.Release, e.MAC, fmt.Sprintf("attempt %d into %s healthy", e.Attempt, e.Target))
	c.dirty = true
	c.applyHolds()
	if c.state.osState(e.OS).Held == "" {
		c.unpin(e.MAC)
	}
}

func (c *Controller) needsHands(e *Episode, why string) {
	e.State, e.Since, e.Done = hardware.AutopilotNeedsHands, c.now(), true
	c.release(e.OS, e.Release).Failing = false
	c.dirty = true
	c.event(EventAlert, e.OS, e.Release, e.MAC, "NEEDS HANDS: "+why)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
