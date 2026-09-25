package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	booty "github.com/jeefy/booty"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/dhcp"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/ignition"
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
	flags.String(config.ServerIP, "", "IP address that clients can connect to; autodetected from the default route when empty (set explicitly behind a VIP/NAT)")
	flags.Int(config.ServerHttpPort, 0, "HTTP port clients use to reach Booty when it differs from --httpPort (port mapping); 0 means same as --httpPort")
	flags.String(config.Builtin, config.DefaultBuiltin, "Comma separated builtin Ignition fragments merged into every registered host's config (hostname, update, booted, sshkeys), or 'none' to serve the user config as-is")
	flags.String(config.SSHAuthorizedKeysFl, "", "File with SSH public keys (one per line) added to the 'core' user by the sshkeys builtin")
	flags.StringSlice(config.SSHAuthorizedKeys, nil, "SSH public key added to the 'core' user by the sshkeys builtin (repeatable)")
	flags.Bool(config.OCIGC, true, "Delete unreferenced OCI blobs from the local registry after a fully successful image sync")
	flags.Bool(config.OCIGCEmpty, false, "Allow blob GC to wipe the whole OCI blob cache when no registered host references an ostree image")
	flags.String(config.DoInstallClearOn, config.ClearOnIgnition, "When to clear a host's pending doInstall: 'ignition' (first Ignition fetch) or 'booted' (only on POST /booted from the installed system)")
	flags.Bool(config.ProxyDHCP, false, "EXPERIMENTAL: answer PXE clients as a ProxyDHCP server (UDP 67 + 4011) so the network's DHCP server needs no next-server/filename")
	flags.String(config.ProxyDHCPListen, "", "IP or interface name the ProxyDHCP server binds to (default all interfaces)")
	flags.Bool(config.ProxyDHCPRelay, false, "Answer relayed PXE requests (giaddr set) on the ProxyDHCP server")
	flags.String(config.JoinString, "", "The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)")
	flags.String(config.AutoRegister, "", "Register unknown MACs on their first /booty.ipxe or /ignition.json fetch as this OS (flatcar, coreos or ublue) instead of sending them to the brig; empty disables")
	flags.String(config.HostnameTemplate, config.DefaultHostnameTemplate, "Go template for auto-registered hostnames; fields: .MAC, .MACSuffix (last 3 bytes hex), .MACFlat (12 hex), .IP")

	if err := viper.BindPFlags(flags); err != nil {
		fmt.Fprintln(os.Stderr, "binding flags:", err)
		os.Exit(1)
	}
	Cmd.AddCommand(initCmd)

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
	if err := config.ValidateDoInstallClearOn(viper.GetString(config.DoInstallClearOn)); err != nil {
		return err
	}
	if err := hardware.ValidateAutoRegisterOS(viper.GetString(config.AutoRegister)); err != nil {
		return fmt.Errorf("--%s: %w", config.AutoRegister, err)
	}
	if _, err := hardware.ParseHostnameTemplate(viper.GetString(config.HostnameTemplate)); err != nil {
		return fmt.Errorf("--%s: %w", config.HostnameTemplate, err)
	}
	builtin, err := ignition.ParseFeatures(viper.GetString(config.Builtin))
	if err != nil {
		return err
	}
	if err := config.ResolveServerAddress(); err != nil {
		return err
	}
	slog.Info("Client-facing address", "server", config.ServerHostPort(), "builtin", viper.GetString(config.Builtin))
	if !builtin.Enabled() {
		slog.Info("Builtin Ignition fragment disabled; serving user configs as-is")
	}
	if autoOS := viper.GetString(config.AutoRegister); autoOS != "" {
		slog.Warn("Auto-registration enabled: any unknown MAC that boots becomes a registered host", "os", autoOS, "hostnameTemplate", viper.GetString(config.HostnameTemplate))
	}
	if keysFile := viper.GetString(config.SSHAuthorizedKeysFl); keysFile != "" {
		if _, err := ignition.LoadSSHKeys(keysFile, nil); err != nil {
			slog.Warn("SSH authorized keys file is not readable; sshkeys builtin will have no file keys", "file", keysFile, "error", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dataDir := viper.GetString(config.DataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir %s: %w", dataDir, err)
	}
	state.Init()
	versions.VerifyLocalArtifacts()
	if err := hardware.Load(); err != nil {
		return err
	}
	if server.DefaultTemplateInUse() {
		slog.Info("No Butane template found; serving the embedded default", "path", config.DataPath(config.DefaultIgnitionFile))
	}
	bootFiles, err := fs.Sub(booty.BootFiles, "boot")
	if err != nil {
		return fmt.Errorf("embedded boot files: %w", err)
	}
	ensureBootFilesInDataDir(bootFiles)
	if err := versions.EnsureOCIFolders(); err != nil {
		slog.Warn("Could not prepare OCI registry folders", "error", err)
	}

	errCh := make(chan error, 4)
	ready := make(chan struct{})

	tftpServer, err := tftp.Start(tftp.Config{
		Port:      viper.GetInt(config.TFTPPort),
		BlockSize: viper.GetInt(config.TFTPBlockSize),
		BootFiles: bootFiles,
	}, errCh)
	if err != nil {
		return err
	}

	webFS, err := fs.Sub(booty.WebDist, "web/dist")
	if err != nil {
		return fmt.Errorf("embedded web ui: %w", err)
	}
	httpServer, err := server.Start(server.Options{WebFS: webFS, WebDir: viper.GetString(config.WebDir), BootFiles: bootFiles}, errCh)
	if err != nil {
		tftpServer.Shutdown(5 * time.Second)
		return err
	}

	var dhcpServer *dhcp.Server
	if viper.GetBool(config.ProxyDHCP) {
		dhcpServer, err = startProxyDHCP(errCh)
		if err != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if herr := httpServer.Shutdown(shutdownCtx); herr != nil {
				slog.Warn("HTTP shutdown failed", "error", herr)
			}
			tftpServer.Shutdown(5 * time.Second)
			return err
		}
	}
	close(ready)

	go func() {
		versions.FlatcarVersionCheck()
		versions.CoreOSVersionCheck()
		<-ready
		versions.ReplayStoredManifests(ctx, "http://"+config.LocalRegistry())
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
	if dhcpServer != nil {
		dhcpServer.Shutdown(10 * time.Second)
	}
	slog.Info("Booty stopped")
	return runErr
}

// ensureBootFilesInDataDir copies the embedded iPXE binaries into DataDir when
// absent so DHCP setups pointing at /data/<name> keep working; the TFTP and
// /boot/ endpoints always serve the embedded copies.
func ensureBootFilesInDataDir(bootFiles fs.FS) {
	for _, name := range config.BootFileNames {
		data, err := fs.ReadFile(bootFiles, name)
		if err != nil {
			slog.Warn("Embedded boot file missing", "name", name, "error", err)
			continue
		}
		if err := config.EnsureFile(config.DataPath(name), data, 0o644); err != nil {
			slog.Warn("Could not write boot file to data dir", "name", name, "error", err)
		}
	}
}

func startProxyDHCP(errCh chan<- error) (*dhcp.Server, error) {
	port67, port4011, err := dhcp.ParsePorts(viper.GetString(config.ProxyDHCPPorts))
	if err != nil {
		return nil, err
	}
	return dhcp.Start(dhcp.Config{
		Listen:   viper.GetString(config.ProxyDHCPListen),
		ServerIP: net.ParseIP(viper.GetString(config.ServerIP)),
		Relay:    viper.GetBool(config.ProxyDHCPRelay),
		Port67:   port67,
		Port4011: port4011,
	}, errCh)
}

func configureLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}
