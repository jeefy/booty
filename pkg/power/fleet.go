package power

import "github.com/jeefy/booty/pkg/hardware"

// LiveFleet is the Fleet backed by the process-wide hardware map.
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
