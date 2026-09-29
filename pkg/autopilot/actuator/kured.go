package actuator

import (
	"context"
	"log/slog"
)

// Kured leaves everything to kured: the node's booty-update.timer already
// touches /var/run/reboot-required while /update-check answers
// rebootRequired:true (P1), and kured drains, reboots and uncordons. Booty
// never touches kured's lock.
type Kured struct{}

func (Kured) Name() string { return NameKured }

func (Kured) Prepare(context.Context, Host) error { return nil }

// Reboot only records that the sentinel path is in charge.
func (Kured) Reboot(_ context.Context, host Host) error {
	slog.Info("Reboot delegated to kured", "host", host.Hostname, "mac", host.MAC, "how", "sentinel: the node's booty-update.timer touches /var/run/reboot-required; kured drains and reboots")
	return nil
}

func (Kured) Finish(context.Context, Host) error { return nil }
