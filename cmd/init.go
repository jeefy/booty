package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/ignition"
	"github.com/spf13/cobra"
)

const initDefaultDir = "./data"

var initCmd = &cobra.Command{
	Use:   "init [dir]",
	Short: "Create a data directory with a starter Butane template and an empty hardware map",
	Long: `Create <dir> (default: --dataDir, BOOTY_DATADIR or ./data) with
config/ignition.yaml (a commented starter Butane template) and hardware.json,
then print the DHCP settings and the command to start Booty. Existing files
are never overwritten.`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString(config.DataDir)
		if len(args) == 1 {
			dir = args[0]
		}
		return runInit(cmd.OutOrStdout(), dir, config.DetectServerIP)
	},
}

func init() {
	initCmd.Flags().String(config.DataDir, initDataDirDefault(), "Directory to initialize")
}

func initDataDirDefault() string {
	if env := os.Getenv("BOOTY_DATADIR"); env != "" {
		return env
	}
	return initDefaultDir
}

type initFile struct {
	rel  string
	data string
}

var initFiles = []initFile{
	{config.DefaultIgnitionFile, ignition.StarterButane},
	{"hardware.json", "{}\n"},
}

func runInit(out io.Writer, dir string, detectIP func() (string, error)) error {
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(config.DefaultIgnitionFile)), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	for _, f := range initFiles {
		path := filepath.Join(dir, f.rel)
		if _, err := os.Stat(path); err == nil {
			say("%s exists, skipped\n", path)
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
		if err := config.WriteFileAtomic(path, []byte(f.data), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		say("%s created\n", path)
	}

	ip, err := detectIP()
	if err != nil {
		ip = "<serverIP>"
		say("\nCould not detect the server IP (%v); replace %s below.\n", err, ip)
	}
	say(`
Next steps:

  1. Edit %s (SSH keys, DNS, ...).
  2. Point DHCP at Booty:
       next-server %s
       filename    undionly.kpxe   (BIOS clients)
       filename    ipxe.efi        (UEFI clients)
  3. Start Booty:
       booty --dataDir %s --serverIP %s
  4. Register hosts in the UI: http://%s:8080/
     (or start with --autoRegister=flatcar to register every booting MAC)
`, filepath.Join(dir, config.DefaultIgnitionFile), ip, dir, ip, ip)
	return nil
}
