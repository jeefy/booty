package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/report"
	"github.com/jeefy/booty/pkg/hardware"
)

// canaryOrder sorts the Bluefin hosts the way the rollout visits them:
// canary:true hosts by MAC, then workers by MAC, then control planes.
func canaryOrder(hosts []*hardware.Host) []*hardware.Host {
	out := slices.Clone(hosts)
	rank := func(h *hardware.Host) int {
		switch {
		case h.Canary:
			return 0
		case !h.IsControlPlane():
			return 1
		}
		return 2
	}
	slices.SortStableFunc(out, func(a, b *hardware.Host) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return compareStrings(a.MAC, b.MAC)
	})
	return out
}

// rolloutCandidate is the release Bluefin hosts should move to under
// full: current unless it is in TIMEOUT or quarantined, then the newest
// cached release that is neither and is newer than lastGood; "" when the
// fleet should stay on lastGood.
func (c *Controller) rolloutCandidate(osName string) string {
	cur, lastGood := c.opts.Fleet.Current(osName), c.opts.Fleet.LastGood(osName)
	if cur == "" || cur == lastGood {
		return ""
	}
	st := c.state.osState(osName)
	if !st.Releases[cur].Bad() {
		return cur
	}
	for _, v := range c.opts.Fleet.Cached(osName) {
		if v == lastGood {
			return ""
		}
		if !st.Releases[v].Bad() {
			return v
		}
	}
	return ""
}

// driveRollouts is the Bluefin canary-serial rollout under full: one host
// at a time gets targetVersion=R and a reboot, the next one only after the
// previous passed the gate; when every host passed, pins are cleared and
// lastGood advances (advanceLastGood).
func (c *Controller) driveRollouts(ctx context.Context) {
	if !c.full() {
		return
	}
	const osName = "bluefin"
	hosts := c.hostsOf(osName)
	if len(hosts) == 0 {
		return
	}
	for _, h := range hosts {
		if e := c.activeEpisode(h.MAC); e != nil {
			return
		}
	}
	candidate := c.rolloutCandidate(osName)
	if candidate == "" {
		return
	}
	r := c.release(osName, candidate)
	if r.Failing {
		return
	}
	for _, h := range canaryOrder(hosts) {
		if c.healthyOn(h) == candidate || contains(r.Healthy, h.MAC) {
			continue
		}
		if hs := c.state.Hosts[h.MAC]; hs != nil && hs.Episode != nil && hs.Episode.Release == candidate && hs.Episode.State == hardware.AutopilotNeedsHands {
			continue
		}
		e := c.startEpisode(h, candidate, "canary-serial rollout of "+candidate)
		c.pin(h.MAC, candidate)
		c.takeBaseline(e)
		c.requestReboot(e, hardware.AutopilotRolling, "rollout: attempt 1 into "+candidate)
		return
	}
}

// startRetry issues the single retry of a TIMEOUT release: on the canary
// (Bluefin under full) or the host that failed (guard, Flatcar, CoreOS).
func (c *Controller) startRetry(r *Release) {
	hosts := c.hostsOf(r.OS)
	var target *hardware.Host
	if c.full() && r.OS == "bluefin" {
		for _, h := range canaryOrder(hosts) {
			if c.activeEpisode(h.MAC) == nil {
				target = h
				break
			}
		}
	} else {
		for _, h := range hosts {
			if h.MAC == r.FailedOn && c.activeEpisode(h.MAC) == nil {
				target = h
			}
		}
	}
	if target == nil {
		c.event(EventRelease, r.OS, r.Version, "", "TIMEOUT elapsed but no host is free for the retry; trying again next tick")
		return
	}
	r.Retried = true
	c.dirty = true
	e := c.startEpisode(target, r.Version, "TIMEOUT elapsed; single retry")
	e.Retry = true
	c.pin(target.MAC, r.Version)
	c.takeBaseline(e)
	c.requestReboot(e, hardware.AutopilotRetrying, "retry into "+r.Version)
	c.event(EventRelease, r.OS, r.Version, target.MAC, "release retried once after TIMEOUT")
}

// advanceLastGood moves lastGood to a rolling release once every host of
// its OS that has ever booted is healthy on it, then clears the pins the
// rollout set.
func (c *Controller) advanceLastGood() {
	for _, osName := range osNames() {
		st := c.state.osState(osName)
		lastGood := c.opts.Fleet.LastGood(osName)
		for _, v := range slices.Sorted(maps.Keys(st.Releases)) {
			r := st.Releases[v]
			if r.State != ReleaseRolling || len(r.Healthy) == 0 || v == lastGood || r.Failing {
				continue
			}
			if !c.fleetHealthyOn(osName, v) {
				continue
			}
			if err := c.opts.Fleet.SetLastGood(osName, v); err != nil {
				c.event(EventAlert, osName, v, "", "could not set lastGood: "+err.Error())
				continue
			}
			c.setReleaseState(r, ReleaseGood)
			c.event(EventRelease, osName, v, "", "every host is healthy on it: lastGood = "+v)
			c.applyHolds()
			for _, h := range c.hostsOf(osName) {
				if hs := c.state.Hosts[h.MAC]; hs != nil && hs.Pinned && h.TargetVersion == v {
					c.unpin(h.MAC)
				}
			}
		}
	}
}

func (c *Controller) fleetHealthyOn(osName, version string) bool {
	any := false
	for _, h := range c.hostsOf(osName) {
		if h.Booted == "" {
			continue
		}
		any = true
		if c.activeEpisode(h.MAC) != nil {
			return false
		}
		hs := c.state.Hosts[h.MAC]
		switch {
		case hs != nil && hs.HealthyOn == version:
		case hs != nil && hs.Episode != nil && hs.Episode.Release == version && hs.Episode.State == hardware.AutopilotNeedsHands:
			return false
		case (hs == nil || hs.HealthyOn == "") && h.Running == version && (h.Health == nil || len(h.Health.FailedUnits) == 0):
		default:
			return false
		}
	}
	return any
}

// draftReport records everything the redacting builder needs about a
// release the autopilot blamed, renders it to disk, and emits the event
// that says so. final marks the quarantine: the draft badge drops and,
// for Bluefin with a poster, the report is filed upstream.
func (c *Controller) draftReport(r *Release, e *Episode, rollback string, final bool) {
	key := report.Key(r.OS, r.Version)
	rep := c.state.Reports[key]
	if rep == nil {
		rep = &Report{OS: r.OS, Version: r.Version, Draft: true, CreatedAt: c.now(), Mode: c.opts.Mode}
		c.state.Reports[key] = rep
	}
	rep.LastGood = c.opts.Fleet.LastGood(r.OS)
	rep.Class = r.Class
	rep.RollbackResult = rollback
	rep.UpdatedAt = c.now()
	if final {
		rep.Draft = false
	}
	for _, a := range e.Attempts {
		if !slices.ContainsFunc(rep.Attempts, func(b Attempt) bool { return b.Ended.Equal(a.Ended) && b.Attempt == a.Attempt && b.Target == a.Target }) {
			rep.Attempts = append(rep.Attempts, a)
		}
	}
	if h, ok := c.opts.Fleet.Host(e.MAC); ok {
		firmware := ""
		if h.Health != nil {
			rep.Hardware.Vendor, rep.Hardware.Product, rep.Hardware.BIOSVersion = h.Health.DMI.Vendor, h.Health.DMI.Product, h.Health.DMI.BIOSVersion
			rep.Hardware.Firmware, rep.Hardware.Kernel = h.Health.Firmware, h.Health.Kernel
			firmware = h.Health.Firmware
			if h.Health.DMI.ProductUUID != "" {
				sum := sha256.Sum256([]byte(h.Health.DMI.ProductUUID))
				rep.DMIHash = hex.EncodeToString(sum[:8])
			}
		}
		rep.Hardware.BootPath = bootPath(h, firmware)
	}
	if len(e.Journal) > 0 {
		rep.JournalErrors = slices.Clone(e.Journal)
	}
	if e.Baseline != nil {
		ni := e.Baseline.Node.NodeInfo
		rep.Node.KubeletVersion, rep.Node.OSImage, rep.Node.ContainerRuntimeVersion, rep.Node.KernelVersion = ni.KubeletVersion, ni.OSImage, ni.ContainerRuntimeVersion, ni.KernelVersion
	}
	r.Report = key
	c.dirty = true
	c.writeReport(key, rep)
	state := "drafted"
	if final {
		state = "final"
	}
	c.event(EventReport, r.OS, r.Version, "", fmt.Sprintf("report %s (%d attempt(s), last class %s)", state, len(rep.Attempts), r.Class))
	if final {
		c.maybePost(key, rep)
	}
}

// bootPath derives how the host was served from what Booty recorded and
// the firmware its health report named.
func bootPath(h *hardware.Host, firmware string) string {
	if h.OS == "bluefin" {
		switch {
		case h.NetbootsBIOS(), firmware == hardware.FirmwareBIOS:
			return report.BootPathBIOSDiskless
		case h.SecureBoot:
			return report.BootPathUEFIPXE
		case h.NetbootPlatform == hardware.PlatformEFI, firmware == hardware.FirmwareUEFI:
			return report.BootPathUEFIHTTP
		}
		return report.BootPathUnknown
	}
	switch firmware {
	case hardware.FirmwareUEFI:
		return report.BootPathUEFIPXE
	case hardware.FirmwareBIOS:
		return report.BootPathBIOSPXE
	}
	if h.SecureBoot {
		return report.BootPathUEFIPXE
	}
	return report.BootPathUnknown
}

// OSStatus is the per-OS block of GET /autopilot.
type OSStatus struct {
	FleetTarget string     `json:"fleetTarget"`
	Current     string     `json:"current"`
	LastGood    string     `json:"lastGood"`
	Held        bool       `json:"held"`
	Releases    []*Release `json:"releases"`
}

// HostStatus is the per-host block of GET /autopilot.
type HostStatus struct {
	MAC       string   `json:"mac"`
	OS        string   `json:"os"`
	HealthyOn string   `json:"healthyOn,omitempty"`
	Pinned    bool     `json:"pinned,omitempty"`
	Episode   *Episode `json:"episode,omitempty"`
}

// ReportSummary is a report as GET /autopilot lists it: the journal
// excerpt stays on disk; Path is where the rendered Markdown is served.
type ReportSummary struct {
	Key            string    `json:"key"`
	OS             string    `json:"os"`
	Version        string    `json:"version"`
	LastGood       string    `json:"lastGood"`
	Draft          bool      `json:"draft"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt,omitzero"`
	Class          string    `json:"class,omitempty"`
	Attempts       int       `json:"attempts"`
	RollbackResult string    `json:"rollbackResult,omitempty"`
	Path           string    `json:"path,omitempty"`
	PostedURL      string    `json:"postedURL,omitempty"`
	PostedAt       time.Time `json:"postedAt,omitzero"`
	PostAction     string    `json:"postAction,omitempty"`
	PostError      string    `json:"postError,omitempty"`
}

// ReportsPath is the URL prefix the server serves rendered reports under.
const ReportsPath = "/autopilot/reports/"

// Status is the controller's part of GET /autopilot.
type Status struct {
	OS         map[string]OSStatus `json:"os"`
	Hosts      []HostStatus        `json:"hosts"`
	Events     []Event             `json:"events"`
	Reports    []ReportSummary     `json:"reports"`
	Held       []string            `json:"held"`
	Quarantine int                 `json:"quarantined"`
	NeedsHands int                 `json:"needsHands"`
}

// Status snapshots the controller for the API.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{OS: map[string]OSStatus{}, Hosts: []HostStatus{}, Events: slices.Clone(c.state.Events), Reports: []ReportSummary{}, Held: []string{}}
	if st.Events == nil {
		st.Events = []Event{}
	}
	for _, osName := range osNames() {
		os := c.state.osState(osName)
		cur := c.opts.Fleet.Current(osName)
		o := OSStatus{Current: cur, LastGood: c.opts.Fleet.LastGood(osName), FleetTarget: cur, Held: os.Held != "", Releases: []*Release{}}
		if os.Held != "" {
			o.FleetTarget = os.Held
			st.Held = append(st.Held, osName)
		}
		cached := c.opts.Fleet.Cached(osName)
		for _, v := range slices.Sorted(maps.Keys(os.Releases)) {
			r := *os.Releases[v]
			r.Cached = slices.Contains(cached, v)
			o.Releases = append(o.Releases, &r)
			if r.State == ReleaseQuarantined {
				st.Quarantine++
			}
		}
		slices.SortFunc(o.Releases, func(a, b *Release) int { return compareStrings(b.Version, a.Version) })
		st.OS[osName] = o
	}
	for _, mac := range slices.Sorted(maps.Keys(c.state.Hosts)) {
		hs := c.state.Hosts[mac]
		h := HostStatus{MAC: mac, HealthyOn: hs.HealthyOn, Pinned: hs.Pinned}
		if hs.Episode != nil {
			e := *hs.Episode
			e.Baseline, e.Journal = nil, nil
			h.Episode = &e
			h.OS = e.OS
			if e.State == hardware.AutopilotNeedsHands {
				st.NeedsHands++
			}
		}
		if h.OS == "" {
			if host, ok := c.opts.Fleet.Host(mac); ok {
				h.OS = hostOS(host)
			}
		}
		st.Hosts = append(st.Hosts, h)
	}
	for _, key := range slices.Sorted(maps.Keys(c.state.Reports)) {
		r := c.state.Reports[key]
		s := ReportSummary{Key: key, OS: r.OS, Version: r.Version, LastGood: r.LastGood, Draft: r.Draft, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Class: r.Class, Attempts: len(r.Attempts), RollbackResult: r.RollbackResult, PostError: r.PostError}
		if c.opts.ReportsDir != "" {
			s.Path = ReportsPath + key + ".md"
		}
		if r.Posted != nil {
			s.PostedURL, s.PostedAt, s.PostAction = r.Posted.URL, r.Posted.At, r.Posted.Action
		}
		st.Reports = append(st.Reports, s)
	}
	return st
}

// Summary is the /info.autopilot block.
type Summary struct {
	Mode       string   `json:"mode"`
	Actuator   string   `json:"actuator"`
	Held       []string `json:"held"`
	Quarantine int      `json:"quarantined"`
	NeedsHands int      `json:"needsHands"`
}

// Summary counts held OSes, quarantined releases and hosts needing hands.
func (c *Controller) Summary() Summary {
	st := c.Status()
	return Summary{Mode: c.opts.Mode, Actuator: c.Actuator(), Held: st.Held, Quarantine: st.Quarantine, NeedsHands: st.NeedsHands}
}
