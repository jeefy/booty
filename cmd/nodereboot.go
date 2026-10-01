package main

import (
	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/spf13/cobra"
)

var nodeRebootPoweroff bool

var nodeRebootCmd = &cobra.Command{
	Use:   "node-reboot",
	Short: "Reboot (or, with --poweroff, power off) the host this runs on: sync, then SIGRTMIN+5 (or +4) to PID 1",
	Long: `Run inside the autopilot's reboot Pod (hostPID, privileged, pinned to the
node): flushes the page cache and sends SIGRTMIN+5 to the host's PID 1, which
makes systemd start reboot.target; with --poweroff it sends SIGRTMIN+4 for
poweroff.target instead. It exits 0 as soon as the signal is delivered; the
node reboots or powers off moments later.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		configureLogging(false)
		if nodeRebootPoweroff {
			return actuator.NodePowerOff(1, nil, nil)
		}
		return actuator.NodeReboot(1, nil, nil)
	},
}

func init() {
	nodeRebootCmd.Flags().BoolVar(&nodeRebootPoweroff, "poweroff", false, "Power the host off (SIGRTMIN+4, poweroff.target) instead of rebooting it")
}
