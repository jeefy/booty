package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jeefy/booty/pkg/config"
	bootyHTTP "github.com/jeefy/booty/pkg/http"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var Cmd = &cobra.Command{
	Use:  "booty",
	Long: "Easy iPXE server for Flatcar",
	RunE: run,
}

var args struct {
	debug          bool
	dataDir        string
	maxCacheAge    int
	cronSchedule   string
	httpPort       int
	architecture   string
	serverIP       string
	serverHttpPort int
	joinString     string
	channel        string
}

var (
	version   string
	timestamp string
)

func init() {
	flags := Cmd.Flags()

	flags.IntVar(
		&args.httpPort,
		"httpPort",
		8080,
		"Port to use for the HTTP server",
	)
	flags.BoolVar(
		&args.debug,
		"debug",
		false,
		"Enable debug logging",
	)
	flags.StringVar(
		&args.cronSchedule,
		"updateSchedule",
		"*/5 * * * *",
		"Cron schedule to use for cleaning up cache files",
	)

	flags.StringVar(
		&args.dataDir,
		"dataDir",
		"/data",
		"Directory to store stateful data",
	)

	flags.StringVar(
		&args.architecture,
		"architecture",
		"amd64",
		"Architecture to use for the iPXE server",
	)

	flags.StringVar(
		&args.channel,
		"channel",
		"stable",
		"Flatcar channel to look for updates",
	)

	flags.StringVar(
		&args.serverIP,
		"serverIP",
		"127.0.0.1",
		"IP address that clients can connect to",
	)
	flags.IntVar(
		&args.serverHttpPort,
		"serverHttpPort",
		80,
		"Alternative HTTP port to use for clients",
	)

	flags.StringVar(
		&args.joinString,
		"joinString",
		"",
		"The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH",
	)

	Cmd.RegisterFlagCompletionFunc("output-format", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"json", "prom"}, cobra.ShellCompDirectiveDefault
	})
	viper.BindPFlags(flags)

	viper.SetDefault("version", "dev")
	if version != "" {
		viper.Set("version", version)
	}
	viper.SetDefault("timestamp", time.Now().Format("2006-01-02 15:04:05.000000"))
	if timestamp != "" {
		viper.Set("timestamp", timestamp)
	}
}

func main() {
	log.SetFlags(log.Flags() | log.Lshortfile)

	if err := Cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(0)
}

func run(cmd *cobra.Command, argv []string) error {
	log.Println("Starting Booty!")
	config.LoadConfig(cmd)
	config.EnsureDeps()

	versions.StartCron()
	tftp.StartTFTP()

	// Start the HTTP server
	// This is a blocking operation
	bootyHTTP.StartHTTP()

	return nil
}
