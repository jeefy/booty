package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	booty "github.com/jeefy/booty"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/server"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var Cmd = &cobra.Command{
	Use:           "booty",
	Long:          "Easy iPXE server for Flatcar, CoreOS, and more",
	RunE:          run,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	version   string
	timestamp string
)

func init() {
	flags := Cmd.Flags()

	flags.Int(config.HttpPort, 8080, "Port to use for the HTTP server")
	flags.Int(config.TFTPPort, 69, "UDP port to use for the TFTP server")
	flags.Int(config.TFTPBlockSize, 1468, "TFTP block size to negotiate with clients")
	flags.Bool(config.Debug, false, "Enable debug logging")
	flags.String(config.UpdateSchedule, "*/5 * * * *", "Cron schedule for the Flatcar/CoreOS version checks and OSTree image sync")
	flags.String(config.DataDir, "/data", "Directory to store stateful data")
	flags.String(config.WebDir, "./web/dist", "Directory with the built Web UI, used when no UI is embedded in the binary")
	flags.String(config.FlatcarArchitecture, "amd64", "Architecture to use for the Flatcar downloads")
	flags.String(config.CoreOSArchitecture, "x86_64", "Architecture to use for CoreOS downloads")
	flags.String(config.FlatcarChannel, "stable", "Flatcar channel to look for updates")
	flags.String(config.FlatcarVersion, "", "Pin a specific Flatcar version (e.g. 3815.2.0). When empty, tracks the latest version on the configured channel")
	flags.String(config.CoreOSChannel, "stable", "CoreOS channel to look for updates")
	flags.String(config.ServerIP, "127.0.0.1", "IP address that clients can connect to")
	flags.Int(config.ServerHttpPort, 80, "Alternative HTTP port to use for clients")
	flags.Bool(config.OCIGC, true, "Delete unreferenced OCI blobs from the local registry after a fully successful image sync")
	flags.String(config.JoinString, "", "The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)")

	if err := viper.BindPFlags(flags); err != nil {
		fmt.Fprintln(os.Stderr, "binding flags:", err)
		os.Exit(1)
	}

	viper.SetDefault(config.Version, "dev")
	if version != "" {
		viper.Set(config.Version, version)
	}
	viper.SetDefault(config.Timestamp, time.Now().Format("2006-01-02 15:04:05.000000"))
	if timestamp != "" {
		viper.Set(config.Timestamp, timestamp)
	}
}

func main() {
	if err := Cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, argv []string) error {
	configureLogging(viper.GetBool(config.Debug))
	slog.Info("Starting Booty!", "version", viper.GetString(config.Version))
	config.LoadConfig()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dataDir := viper.GetString(config.DataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir %s: %w", dataDir, err)
	}
	state.Init()
	if err := hardware.Load(); err != nil {
		return err
	}
	if err := config.EnsureFile(config.DataPath("undionly.kpxe"), booty.UndionlyKPXE, 0o644); err != nil {
		slog.Warn("Could not write undionly.kpxe to data dir", "error", err)
	}
	depsCtx, cancelDeps := context.WithTimeout(ctx, 2*time.Minute)
	config.EnsureDeps(depsCtx)
	cancelDeps()
	if err := versions.EnsureOCIFolders(); err != nil {
		slog.Warn("Could not prepare OCI registry folders", "error", err)
	}

	errCh := make(chan error, 2)
	ready := make(chan struct{})

	tftpServer, err := tftp.Start(tftp.Config{
		Port:         viper.GetInt(config.TFTPPort),
		BlockSize:    viper.GetInt(config.TFTPBlockSize),
		UndionlyKPXE: booty.UndionlyKPXE,
	}, errCh)
	if err != nil {
		return err
	}

	webFS, err := fs.Sub(booty.WebDist, "web/dist")
	if err != nil {
		return fmt.Errorf("embedded web ui: %w", err)
	}
	httpServer, err := server.Start(server.Options{WebFS: webFS, WebDir: viper.GetString(config.WebDir)}, errCh)
	if err != nil {
		tftpServer.Shutdown(5 * time.Second)
		return err
	}
	close(ready)

	go func() {
		versions.FlatcarVersionCheck()
		versions.CoreOSVersionCheck()
		<-ready
		versions.OSTreeImageSync()
	}()

	scheduler, err := versions.StartScheduler(viper.GetString(config.UpdateSchedule))
	if err != nil {
		slog.Error("Scheduler failed to start; periodic checks disabled", "error", err)
	}

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("Shutdown signal received")
	case runErr = <-errCh:
		slog.Error("Server failed", "error", runErr)
	}

	if scheduler != nil {
		if err := scheduler.Shutdown(); err != nil {
			slog.Warn("Scheduler shutdown failed", "error", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP shutdown failed", "error", err)
		runErr = errors.Join(runErr, err)
	} else {
		slog.Info("HTTP server stopped")
	}
	tftpServer.Shutdown(10 * time.Second)
	slog.Info("Booty stopped")
	return runErr
}

func configureLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}
