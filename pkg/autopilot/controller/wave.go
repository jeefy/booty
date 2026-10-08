package controller

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/versions"
)

func canariesOf(hosts []*hardware.Host) []*hardware.Host {
	var out []*hardware.Host
	for _, h := range hosts {
		if h.Canary {
			out = append(out, h)
		}
	}
	return out
}

// soaked reports whether non-canary hosts may move to r: the soak is off,
// the OS has no canary, or r has been healthy for the soak and a canary
// still counts as healthy on it (healthyOn r, and its last health report,
// if any, names r with no failed units). A canary that moved on keeps r
// soaked only through another canary; one that fell over un-soaks it.
func (c *Controller) soaked(r *Release, canaries []*hardware.Host) bool {
	if c.opts.Soak <= 0 || len(canaries) == 0 {
		return true
	}
	if r == nil || r.FirstHealthyAt.IsZero() || c.now().Sub(r.FirstHealthyAt) < c.opts.Soak {
		return false
	}
	for _, h := range canaries {
		if c.healthyOn(h) != r.Version {
			continue
		}
		if h.Health != nil && (len(h.Health.FailedUnits) > 0 || (h.Health.Running != "" && h.Health.Running != r.Version)) {
			continue
		}
		return true
	}
	return false
}

// driveWaves is the paced rollout (plan 2026-10-07-soak-cooldown), per
// paced OS: the canary step (a canary with no episode is pinned to the
// rollout candidate; Bluefin under full gets that from driveRollouts), the
// soak bookkeeping, and the wave the non-canary hosts move in. A wave ends
// when its release went bad (aborted, idle pinned hosts fall back to the
// held lastGood) or no host is left to move (done); a new one starts at
// most once per cooldown, into the newest soaked release.
func (c *Controller) driveWaves() {
	if !c.paced() {
		return
	}
	for _, osName := range osNames() {
		c.driveWave(osName)
	}
}

func (c *Controller) driveWave(osName string) {
	if c.opts.Fleet.Current(osName) == "" {
		return
	}
	hosts := c.hostsOf(osName)
	if len(hosts) == 0 {
		return
	}
	canaries := canariesOf(hosts)
	if !c.serial(osName) {
		c.pinCanaries(osName, canaries)
	}
	c.noteSoak(osName, canaries)
	st := c.state.osState(osName)
	now := c.now()
	if w := st.Wave; w != nil && w.EndedAt.IsZero() {
		r := st.Releases[w.Release]
		switch {
		case r.Bad():
			w.EndedAt, w.Outcome = now, WaveAborted
			c.dirty = true
			c.event(EventFleet, osName, w.Release, "", "wave aborted: "+w.Release+" is "+r.State)
			for _, h := range hosts {
				if h.TargetVersion == w.Release && c.activeEpisode(h.MAC) == nil {
					c.unpin(h.MAC)
				}
			}
		case len(c.waveEligible(osName, hosts, w.Release)) == 0:
			w.EndedAt, w.Outcome = now, WaveDone
			c.dirty = true
			c.event(EventFleet, osName, w.Release, "", "wave done: every host is on "+w.Release)
		default:
			c.driveWaveHosts(osName, hosts, w.Release)
			return
		}
	}
	if w := st.Wave; w != nil && c.opts.Cooldown > 0 && now.Sub(w.StartedAt) < c.opts.Cooldown {
		return
	}
	release := c.waveCandidate(osName, canaries)
	if release == "" {
		return
	}
	eligible := c.waveEligible(osName, hosts, release)
	if len(eligible) == 0 {
		return
	}
	st.Wave = &Wave{Release: release, StartedAt: now, Outcome: WaveRolling}
	c.dirty = true
	text := fmt.Sprintf("wave started into %s (%d host(s))", release, len(eligible))
	if c.opts.Cooldown > 0 {
		text += "; next wave not before " + now.Add(c.opts.Cooldown).UTC().Format(time.RFC3339)
	}
	c.event(EventFleet, osName, release, "", text)
	c.driveWaveHosts(osName, hosts, release)
}

// pinCanaries is the canary step outside the Bluefin serial rollout: a
// canary with no episode that is not on the rollout candidate is pinned
// to it, and kured (or the host's update timer) reboots it into the gate.
func (c *Controller) pinCanaries(osName string, canaries []*hardware.Host) {
	candidate := c.rolloutCandidate(osName)
	if candidate == "" {
		return
	}
	r := c.release(osName, candidate)
	if r.Failing {
		return
	}
	for _, h := range canaries {
		if c.activeEpisode(h.MAC) != nil || h.TargetVersion == candidate || c.passedOn(h, r) {
			continue
		}
		c.pin(h.MAC, candidate)
	}
}

// passedOn reports whether h already counts as through the gate on r, or
// needs hands on it: either way the rollout has nothing to do with it.
func (c *Controller) passedOn(h *hardware.Host, r *Release) bool {
	if c.healthyOn(h) == r.Version || contains(r.Healthy, h.MAC) {
		return true
	}
	hs := c.state.Hosts[h.MAC]
	return hs != nil && hs.Episode != nil && hs.Episode.Release == r.Version && hs.Episode.State == hardware.AutopilotNeedsHands
}

// noteSoak emits the once-per-release soak events: no canary registered
// (the soak has no effect on this OS), and the release becoming soaked.
func (c *Controller) noteSoak(osName string, canaries []*hardware.Host) {
	if c.opts.Soak <= 0 {
		return
	}
	lastGood := c.opts.Fleet.LastGood(osName)
	st := c.state.osState(osName)
	for _, v := range c.opts.Fleet.Cached(osName) {
		r := st.Releases[v]
		if r == nil || r.State != ReleaseRolling || (lastGood != "" && versions.CompareVersions(v, lastGood) <= 0) {
			continue
		}
		switch {
		case len(canaries) == 0:
			if !r.SoakWarned {
				r.SoakWarned = true
				c.dirty = true
				c.event(EventRelease, osName, v, "", "no canary registered for "+osName+"; --autopilotSoak has no effect on it")
			}
		case r.SoakedAt.IsZero() && c.soaked(r, canaries):
			r.SoakedAt = c.now()
			c.dirty = true
			c.event(EventRelease, osName, v, "", fmt.Sprintf("release soaked: healthy on a canary for %s; eligible for the next wave", c.opts.Soak))
		}
	}
}

// waveCandidate is the newest cached release newer than lastGood that is
// neither bad nor failing and is soaked; "" when there is none. Skipping
// straight to the newest is what makes three releases in one cooldown
// cost one reboot per host.
func (c *Controller) waveCandidate(osName string, canaries []*hardware.Host) string {
	lastGood := c.opts.Fleet.LastGood(osName)
	if lastGood == "" {
		return ""
	}
	st := c.state.osState(osName)
	for _, v := range c.opts.Fleet.Cached(osName) {
		if versions.CompareVersions(v, lastGood) <= 0 {
			continue
		}
		r := st.Releases[v]
		if r.Bad() || (r != nil && r.Failing) || !c.soaked(r, canaries) {
			continue
		}
		return v
	}
	return ""
}

// waveEligible lists, in canary order, the non-canary hosts a wave into
// release still has to move: no active episode, not through the gate on
// it, not needing hands on it.
func (c *Controller) waveEligible(osName string, hosts []*hardware.Host, release string) []*hardware.Host {
	r := c.state.osState(osName).Releases[release]
	if r == nil {
		r = &Release{OS: osName, Version: release}
	}
	var out []*hardware.Host
	for _, h := range canaryOrder(hosts) {
		if h.Canary || c.activeEpisode(h.MAC) != nil || c.passedOn(h, r) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// driveWaveHosts moves the wave on: Bluefin under full starts one episode
// at a time, shared with the canary (startEpisode, pin, baseline, reboot
// exactly as the rollout does); everywhere else every eligible host is
// pinned to the release at once and kured orders the reboots, re-pinning
// a host whose pin an operator cleared while the wave is open.
func (c *Controller) driveWaveHosts(osName string, hosts []*hardware.Host, release string) {
	eligible := c.waveEligible(osName, hosts, release)
	if !c.serial(osName) {
		for _, h := range eligible {
			if h.TargetVersion != release {
				c.pin(h.MAC, release)
			}
		}
		return
	}
	for _, h := range hosts {
		if c.activeEpisode(h.MAC) != nil {
			return
		}
	}
	if len(eligible) == 0 {
		return
	}
	h := eligible[0]
	e := c.startEpisode(h, release, "wave into "+release)
	c.pin(h.MAC, release)
	c.takeBaseline(e)
	c.requestReboot(e, hardware.AutopilotRolling, "wave: attempt 1 into "+release)
}

// skipOlderReleases marks the rolling releases of osName older than v,
// the release lastGood just advanced to, as skipped: the fleet moved past
// them without ever moving to them. Bad, failing and still-gating
// releases keep their state.
func (c *Controller) skipOlderReleases(osName, v string) {
	st := c.state.osState(osName)
	for _, ver := range slices.Sorted(maps.Keys(st.Releases)) {
		r := st.Releases[ver]
		if r.State != ReleaseRolling || r.Failing || versions.CompareVersions(ver, v) >= 0 || c.episodeActiveOn(osName, ver) {
			continue
		}
		c.setReleaseState(r, ReleaseSkipped)
		c.event(EventRelease, osName, ver, "", "release skipped: the fleet moved to "+v+" without it")
	}
}

func (c *Controller) episodeActiveOn(osName, version string) bool {
	for _, hs := range c.state.Hosts {
		if e := hs.Episode; e.Active() && e.OS == osName && e.Release == version {
			return true
		}
	}
	return false
}
