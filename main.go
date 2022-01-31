package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	_ "net/http/pprof"
)

var Cmd = &cobra.Command{
	Use:  "booty",
	Long: "Easy iPXE server for Flatcar",
	RunE: run,
}

var args struct {
	debug        bool
	dataDir      string
	maxCacheAge  int
	cronSchedule string
	httpPort     int
	architecture string
	serverIP     string
	channel      string
}

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
		"* */1 * * *",
		"Cron schedule to use for cleaning up cache files",
	)

	flags.StringVar(
		&args.dataDir,
		"dataDir",
		"/tmp",
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

	Cmd.RegisterFlagCompletionFunc("output-format", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"json", "prom"}, cobra.ShellCompDirectiveDefault
	})
	viper.BindPFlags(flags)
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
	LoadConfig(cmd)
	EnsureDeps()

	if viper.GetBool(Debug) {
		go func() {
			log.Println("Starting pprof server on port 6060")
			log.Println(http.ListenAndServe(":6060", nil))
		}()
	}

	StartCron()
	StartTFTP()
	StartHTTP()

	return nil
}
