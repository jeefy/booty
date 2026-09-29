package main

import (
	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/spf13/cobra"
)

var nodeRebootCmd = &cobra.Command{
	Use:   "node-reboot",
	Short: "Reboot the host this runs on: sync, then SIGRTMIN+5 to PID 1 (systemd reboot.target)",
	Long: `Run inside the autopilot's reboot Pod (hostPID, privileged, pinned to the
node): flushes the page cache and sends SIGRTMIN+5 to the host's PID 1, which
makes systemd start reboot.target. It exits 0 as soon as the signal is
delivered; the node reboots moments later.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		configureLogging(false)
		return actuator.NodeReboot(1, nil, nil)
	},
}
