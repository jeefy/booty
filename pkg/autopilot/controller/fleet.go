package controller

import (
	"context"

	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/versions"
)

// Fleet is what the controller reads and writes about hosts and releases;
// the real one wraps pkg/hardware and pkg/versions, tests use an in-memory
// one.
type Fleet interface {
	Hosts() []*hardware.Host
	Host(mac string) (*hardware.Host, bool)
	Update(mac string, fn func(*hardware.Host)) error
	Current(osName string) string
	LastGood(osName string) string
	SetLastGood(osName, version string) error
	// Cached lists the cached releases of an OS, newest first.
	Cached(osName string) []string
	// Hold pins the fleet target of an OS at version ("" releases it).
	Hold(osName, version string)
	// SerialRollout makes the OS's fleet target follow lastGood whenever
	// current differs from it, synchronously with a release landing.
	SerialRollout(osName string)
	// EffectiveTarget is the release the host boots right now.
	EffectiveTarget(h *hardware.Host) string
}

// Cluster is the read side of the k8s client the gate needs; nil means no
// cluster (L2 is skipped).
type Cluster interface {
	Sample(ctx context.Context, nodeName string) (k8s.Sample, error)
}

// Actuators picks the reboot mechanism; *actuator.Chooser implements it.
type Actuators interface {
	Choose(ctx context.Context) (actuator.Rebooter, error)
}

// LiveFleet is the Fleet backed by the process-wide hardware map and the
// release directories.
type LiveFleet struct{}

func (LiveFleet) Hosts() []*hardware.Host {
	snap := hardware.Snapshot().Hosts
	out := make([]*hardware.Host, 0, len(snap))
	for _, h := range snap {
		out = append(out, h)
	}
	return out
}

func (LiveFleet) Host(mac string) (*hardware.Host, bool) { return hardware.Get(mac) }

func (LiveFleet) Update(mac string, fn func(*hardware.Host)) error {
	_, err := hardware.Update(mac, fn)
	return err
}

func (LiveFleet) Current(osName string) string  { return versions.CurrentTarget(osName) }
func (LiveFleet) LastGood(osName string) string { return versions.LastGood(osName) }
func (LiveFleet) SetLastGood(osName, version string) error {
	return versions.SetLastGood(osName, version)
}
func (LiveFleet) Cached(osName string) []string { return versions.CachedReleases(osName) }
func (LiveFleet) Hold(osName, version string)   { versions.HoldFleetTarget(osName, version) }
func (LiveFleet) SerialRollout(osName string)   { versions.SerialRollout(osName, true) }
func (LiveFleet) EffectiveTarget(h *hardware.Host) string {
	return versions.EffectiveTarget(h)
}

func hostOS(h *hardware.Host) string {
	if h == nil {
		return ""
	}
	if h.OS == "" {
		return versions.OSFlatcar
	}
	return h.OS
}
