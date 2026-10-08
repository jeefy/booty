// Package controller is the autopilot's state machine (plan P3): it
// watches the signals P1 records (kernel/UKI and Ignition fetches, POST
// /booted, POST /health) and the cluster (P2's k8s client), runs the
// health gate per host, retries, rolls back to lastGood, holds the fleet
// target, puts bad releases in TIMEOUT and quarantine, drives the Bluefin
// canary-serial rollout under --autopilot=full and drafts the report stub
// P4 renders. Every actuator call in Booty comes from here.
package controller

import (
	"time"

	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/hardware"
)

// Release states, per OS release. Skipped is a rolling release the fleet
// moved past (lastGood advanced to a newer one without it); it is kept
// like good.
const (
	ReleaseRolling     = "rolling"
	ReleaseTimeout     = "timeout"
	ReleaseQuarantined = "quarantined"
	ReleaseGood        = "good"
	ReleaseSkipped     = "skipped"
)

// Wave outcomes, per OS wave.
const (
	WaveRolling = "rolling"
	WaveDone    = "done"
	WaveAborted = "aborted"
)

// Failure classes of a health-gate attempt.
const (
	ClassBootLoop      = "boot-loop"
	ClassNoIgnition    = "no-ignition"
	ClassFailedUnits   = "failed-units"
	ClassNodeNotReady  = "node-not-ready"
	ClassWorkloads     = "workloads-unhealthy"
	ClassHung          = "hung"
	ClassTimeout       = "timeout"
	OutcomeHealthy     = "healthy"
	OutcomeFailed      = "failed"
	ActuatorNone       = "none"
	FetchKernel        = "kernel"
	FetchIgnition      = "ignition"
	maxEvents          = 200
	maxTimelineEntries = 64
	// maxAttempts bounds the attempts an episode keeps (the newest stay).
	maxAttempts = 10
	// maxGoodHistory caps the good release records per OS whose files
	// are gone: beyond it the oldest folded ones are dropped, the only
	// deletion the controller ever does to its history.
	maxGoodHistory = 50
	// DefaultEvents and DefaultGoodHistory bound GET /autopilot without
	// ?events=all / ?releases=all: the newest events of the ring, and the
	// newest good records whose files are gone on top of every live one.
	DefaultEvents      = 100
	DefaultGoodHistory = 10
)

// Release is one cached version of an OS as the controller sees it.
type Release struct {
	OS       string    `json:"os"`
	Version  string    `json:"version"`
	State    string    `json:"state"`
	Since    time.Time `json:"since"`
	Attempts int       `json:"attempts"`
	// Class is the last failure class an attempt into it ended with.
	Class string `json:"class,omitempty"`
	// Report names the report stub in State.Reports ("<os>-<version>").
	Report string `json:"report,omitempty"`
	// Healthy lists the MACs that passed the gate on this release.
	Healthy []string `json:"healthy,omitempty"`
	// FailedOn is the MAC whose rollback blamed the release; the TIMEOUT
	// retry runs there under guard.
	FailedOn string `json:"failedOn,omitempty"`
	// Failing is set from a host's first failed attempt until that host
	// passes or is written off: it holds the fleet target so a bad release
	// stops after one node.
	Failing bool `json:"failing,omitempty"`
	// Retried is set once the single TIMEOUT retry has been issued.
	Retried bool `json:"retried,omitempty"`
	// FirstHealthyAt is when the first host passed the gate on it: the
	// soak clock runs from here. SoakedAt is when it first counted as
	// soaked and SoakWarned that the no-canary warning for it went out;
	// both keep the events to one per release across restarts.
	FirstHealthyAt time.Time `json:"firstHealthyAt,omitzero"`
	SoakedAt       time.Time `json:"soakedAt,omitzero"`
	SoakWarned     bool      `json:"soakWarned,omitempty"`
	// Cached says whether the release's files are still on disk. Records
	// outlive their releases on purpose (the verdict is the history); Status
	// computes it from the fleet's cached list, the persisted value is
	// meaningless. Soaked (may non-canary hosts move to it) is computed the
	// same way, and only reported while a soak is configured.
	Cached bool `json:"cached"`
	Soaked bool `json:"soaked,omitempty"`
}

// Bad reports whether hosts should be kept off the release.
func (r *Release) Bad() bool {
	return r != nil && (r.State == ReleaseTimeout || r.State == ReleaseQuarantined)
}

// settled reports whether the fleet is past the release (good, or skipped
// on the way to a newer good one): history, not a live record.
func (r *Release) settled() bool {
	return r.State == ReleaseGood || r.State == ReleaseSkipped
}

// Wave is one pass of an OS's non-canary hosts into one release; it is
// open while EndedAt is zero.
type Wave struct {
	Release   string    `json:"release"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	Outcome   string    `json:"outcome"`
}

// Signals are what an attempt has seen so far, all relative to T0.
type Signals struct {
	Fetches     int       `json:"fetches"`
	Ignition    time.Time `json:"ignition,omitzero"`
	Booted      time.Time `json:"booted,omitzero"`
	Health      time.Time `json:"health,omitzero"`
	Running     string    `json:"running,omitempty"`
	FailedUnits []string  `json:"failedUnits,omitempty"`
	// L2 is the last cluster-side note (what the gate is waiting for).
	L2 string `json:"l2,omitempty"`
	// PendingSince remembers when a pod was first seen Pending, so
	// Pending>5m can be told from a pod that is just starting.
	PendingSince map[string]time.Time `json:"pendingSince,omitempty"`
	// Timeline is the relative boot timeline ("t+38s ignition fetched").
	Timeline []string `json:"timeline,omitempty"`
}

// Attempt is one finished reboot into a target.
type Attempt struct {
	Attempt  int       `json:"attempt"`
	Target   string    `json:"target"`
	Outcome  string    `json:"outcome"`
	Class    string    `json:"class,omitempty"`
	T0       time.Time `json:"t0,omitzero"`
	Ended    time.Time `json:"ended"`
	Note     string    `json:"note,omitempty"`
	Signals  Signals   `json:"signals"`
	Actuator string    `json:"actuator,omitempty"`
}

// Episode is the attempts of one host into one release.
type Episode struct {
	MAC     string `json:"mac"`
	OS      string `json:"os"`
	Release string `json:"release"`
	// Target is what the current attempt boots: Release, or lastGood when
	// RollingBack.
	Target  string    `json:"target"`
	Attempt int       `json:"attempt"`
	State   string    `json:"state"`
	Class   string    `json:"class,omitempty"`
	Since   time.Time `json:"since"`
	Started time.Time `json:"started"`
	// T0 is the gate clock: the first kernel/UKI fetch of the attempt.
	T0 time.Time `json:"t0,omitzero"`
	// RequestedAt is when the controller asked for a reboot, IssuedAt when
	// an actuator reported the reboot under way.
	RequestedAt time.Time `json:"requestedAt,omitzero"`
	IssuedAt    time.Time `json:"issuedAt,omitzero"`
	Actuator    string    `json:"actuator,omitempty"`
	RollingBack bool      `json:"rollingBack,omitempty"`
	Retry       bool      `json:"retry,omitempty"`
	// Done marks a terminal episode (healthy, rolled back healthy, or
	// needs-hands); the record stays for the UI until the next one.
	Done bool `json:"done,omitempty"`
	// Baseline is the node sample taken at t0 (or when the controller
	// initiated the reboot); pods already unhealthy in it never fail the
	// gate.
	Baseline *k8s.Sample `json:"baseline,omitempty"`
	// Journal is the error-level journal excerpt of the last health report
	// from a boot of the blamed release (never of the lastGood rollback),
	// raw; the report builder redacts it. Kept out of GET /autopilot.
	Journal  []string  `json:"journal,omitempty"`
	Signals  Signals   `json:"signals"`
	Attempts []Attempt `json:"attempts,omitempty"`
	Note     string    `json:"note,omitempty"`
}

// Active reports whether the episode still has an attempt in flight.
func (e *Episode) Active() bool {
	if e == nil || e.Done {
		return false
	}
	switch e.State {
	case hardware.AutopilotRolling, hardware.AutopilotGating, hardware.AutopilotRetrying, hardware.AutopilotRolledBack:
		return true
	}
	return false
}

// canRegate reports whether the host can revive the episode by coming
// back healthy on its target: a reboot the controller requested and has
// not seen begin (rolling, retrying, rolled-back), or a needs-hands
// verdict. A rolled-back episode that is done (healthy on lastGood) is
// terminal.
func (e *Episode) canRegate() bool {
	if e == nil {
		return false
	}
	switch e.State {
	case hardware.AutopilotRolling, hardware.AutopilotRetrying, hardware.AutopilotRolledBack:
		return !e.Done
	case hardware.AutopilotNeedsHands:
		return true
	}
	return false
}

// HostState is the controller's per-host record.
type HostState struct {
	// HealthyOn is the release the host last passed the gate on.
	HealthyOn string `json:"healthyOn,omitempty"`
	// Pinned is set when the controller wrote the host's targetVersion.
	Pinned  bool     `json:"pinned,omitempty"`
	Episode *Episode `json:"episode,omitempty"`
}

// OSState is the controller's per-OS record.
type OSState struct {
	Releases map[string]*Release `json:"releases"`
	// Held is the version the fleet target is currently held at ("" when
	// the fleet target is current), re-applied on load.
	Held      string    `json:"held,omitempty"`
	HeldSince time.Time `json:"heldSince,omitzero"`
	// Wave is the last wave of the OS (open while its EndedAt is zero);
	// the cooldown counts from its StartedAt.
	Wave *Wave `json:"wave,omitempty"`
}

// Event is one line of the timeline GET /autopilot serves. Text never
// carries hostnames, IPs or MACs; the MAC is a field for the UI to map.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	OS      string    `json:"os,omitempty"`
	Release string    `json:"release,omitempty"`
	MAC     string    `json:"mac,omitempty"`
	Text    string    `json:"text"`
}

// Event kinds.
const (
	EventEpisode  = "episode"
	EventRelease  = "release"
	EventFleet    = "fleet"
	EventActuator = "actuator"
	EventAlert    = "alert"
	EventReport   = "report"
	// EventPower is the power tracker's kind (pkg/power.EventKind).
	EventPower = "power"
)

// Report is the stub P4's builder renders: everything the redacting
// builder needs, kept as fields (never free text with identifiers).
// Hardware and node facts are copied from the host's health report and
// the node sample. Draft is true from the TIMEOUT draft until quarantine.
type Report struct {
	OS        string    `json:"os"`
	Version   string    `json:"version"`
	LastGood  string    `json:"lastGood"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"createdAt"`
	Mode      string    `json:"mode"`
	// Class is the class of the last failed attempt into Version.
	Class    string    `json:"class,omitempty"`
	Attempts []Attempt `json:"attempts"`
	Hardware struct {
		Vendor      string `json:"vendor,omitempty"`
		Product     string `json:"product,omitempty"`
		BIOSVersion string `json:"biosVersion,omitempty"`
		Firmware    string `json:"firmware,omitempty"`
		Kernel      string `json:"kernel,omitempty"`
		BootPath    string `json:"bootPath,omitempty"`
	} `json:"hardware"`
	Node struct {
		KubeletVersion          string `json:"kubeletVersion,omitempty"`
		OSImage                 string `json:"osImage,omitempty"`
		ContainerRuntimeVersion string `json:"containerRuntimeVersion,omitempty"`
		KernelVersion           string `json:"kernelVersion,omitempty"`
	} `json:"node"`
	// JournalErrors is the raw excerpt from the failing boot's health
	// report; P4 redacts it. It is not served by GET /autopilot.
	JournalErrors []string `json:"journalErrors,omitempty"`
	// RollbackResult says what happened on lastGood on the same hardware.
	RollbackResult string `json:"rollbackResult,omitempty"`
	// DMIHash identifies the machine for the issue dedupe marker without
	// naming it (sha256 of the product UUID), empty when unknown.
	DMIHash string `json:"dmiHash,omitempty"`
	// UpdatedAt is when the stub last changed (and the files were
	// rewritten); Posted is the GitHub issue the final report went to,
	// NextPostAt when a failed post is tried again.
	UpdatedAt  time.Time `json:"updatedAt,omitzero"`
	Posted     *Posted   `json:"posted,omitempty"`
	NextPostAt time.Time `json:"nextPostAt,omitzero"`
	PostError  string    `json:"postError,omitempty"`
}

// Posted is where a report was filed.
type Posted struct {
	URL    string    `json:"url"`
	Number int       `json:"number,omitempty"`
	Action string    `json:"action"`
	At     time.Time `json:"at"`
}

// State is what data/autopilot/state.json holds.
type State struct {
	Version int       `json:"version"`
	Saved   time.Time `json:"saved"`
	// Revision counts the saves, across restarts; GET /autopilot carries it
	// so the UI can tell an unchanged answer from a new one.
	Revision uint64                `json:"revision"`
	OS       map[string]*OSState   `json:"os"`
	Hosts    map[string]*HostState `json:"hosts"`
	Events   []Event               `json:"events"`
	Reports  map[string]*Report    `json:"reports"`
}

const stateVersion = 1

func newState() *State {
	return &State{Version: stateVersion, OS: map[string]*OSState{}, Hosts: map[string]*HostState{}, Reports: map[string]*Report{}}
}

func (s *State) osState(osName string) *OSState {
	st := s.OS[osName]
	if st == nil {
		st = &OSState{Releases: map[string]*Release{}}
		s.OS[osName] = st
	}
	if st.Releases == nil {
		st.Releases = map[string]*Release{}
	}
	return st
}

func (s *State) hostState(mac string) *HostState {
	hs := s.Hosts[mac]
	if hs == nil {
		hs = &HostState{}
		s.Hosts[mac] = hs
	}
	return hs
}
