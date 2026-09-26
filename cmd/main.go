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
	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/dhcp"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/ignition"
	"github.com/jeefy/booty/pkg/kubeadm"
	"github.com/jeefy/booty/pkg/profile"
	"github.com/jeefy/booty/pkg/server"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var Cmd = &cobra.Command{
	Use:           "booty",
	Long:          "Easy iPXE server for Flatcar, CoreOS, Bluefin Server, and more",
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
	flags.String(config.UpdateSchedule, "*/5 * * * *", "Cron schedule for the Flatcar/CoreOS/Bluefin version checks and OSTree image sync")
	flags.String(config.DataDir, "/data", "Directory to store stateful data")
	flags.String(config.WebDir, "./web/dist", "Directory with the built Web UI, used when no UI is embedded in the binary")
	flags.String(config.FlatcarArchitecture, "amd64", "Architecture to use for the Flatcar downloads")
	flags.String(config.CoreOSArchitecture, "x86_64", "Architecture to use for CoreOS downloads")
	flags.String(config.FlatcarChannel, "stable", "Flatcar channel to look for updates")
	flags.String(config.FlatcarVersion, "", "Pin a specific Flatcar version (e.g. 3815.2.0). When empty, tracks the latest version on the configured channel")
	flags.String(config.CoreOSChannel, "stable", "CoreOS channel to look for updates")
	flags.String(config.BluefinRepo, config.DefaultBluefinRepo, "GitHub repository whose installer-v* releases provide the Bluefin Server PXE kernel, initrd and DDI")
	flags.String(config.BluefinVersion, "", "Pin a specific Bluefin Server release (e.g. 26.08.0). When empty, tracks the newest installer-v* release")
	flags.String(config.GithubToken, "", "GitHub token sent as a bearer token to the releases API (raises the unauthenticated 60 requests/hour limit); no scopes needed")
	flags.String(config.ServerIP, "", "IP address that clients can connect to; autodetected from the default route when empty (set explicitly behind a VIP/NAT)")
	flags.Int(config.ServerHttpPort, 0, "HTTP port clients use to reach Booty when it differs from --httpPort (port mapping); 0 means same as --httpPort")
	flags.String(config.Builtin, config.DefaultBuiltin, "Comma separated builtin Ignition fragments merged into every registered host's config (hostname, update, booted, sshkeys), or 'none' to serve the user config as-is")
	flags.String(config.SSHAuthorizedKeysFl, "", "File with SSH public keys (one per line) added to the 'core' user by the sshkeys builtin")
	flags.StringSlice(config.SSHAuthorizedKeys, nil, "SSH public key added to the 'core' user by the sshkeys builtin (repeatable)")
	flags.Bool(config.OCIGC, true, "Delete unreferenced OCI blobs from the local registry after a fully successful image sync")
	flags.Bool(config.OCIGCEmpty, false, "Allow blob GC to wipe the whole OCI blob cache when no registered host references an ostree image")
	flags.String(config.DoInstallClearOn, config.ClearOnIgnition, "When to clear a host's pending doInstall: 'ignition' (first Ignition fetch), 'booted' (only on POST /booted from the installed system) or 'next-boot' (Bluefin: the first /booty.ipxe fetch at least --installMinDuration after the install stanza was served; other OSes behave like 'booted')")
	flags.Duration(config.InstallMinDuration, config.DefaultInstallMinDuration, "Minimum time between serving a Bluefin install stanza and the re-PXE that counts as 'install finished' for --doInstallClearOn=next-boot; earlier re-PXEs keep doInstall")
	flags.Bool(config.ProxyDHCP, false, "EXPERIMENTAL: answer PXE clients as a ProxyDHCP server (UDP 67 + 4011) so the network's DHCP server needs no next-server/filename")
	flags.String(config.ProxyDHCPListen, "", "IP or interface name the ProxyDHCP server binds to (default all interfaces)")
	flags.Bool(config.ProxyDHCPRelay, false, "Answer relayed PXE requests (giaddr set) on the ProxyDHCP server")
	flags.String(config.JoinString, "", "The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)")
	flags.String(config.AutoRegister, "", "Register unknown MACs on their first /booty.ipxe or /ignition.json fetch as this OS (flatcar, coreos or bluefin) instead of sending them to the brig; empty disables")
	flags.String(config.HostnameTemplate, config.DefaultHostnameTemplate, "Go template for auto-registered hostnames; fields: .MAC, .MACSuffix (last 3 bytes hex), .MACFlat (12 hex), .IP")
	flags.String(config.JoinStringFile, "", "File holding the kubeadm join string (e.g. a mounted Secret); re-read on every render and wins over --joinString")
	flags.String(config.KubeadmJoin, config.KubeadmJoinStatic, "Where the kubeadm join string comes from: 'static' (--joinString/--joinStringFile) or 'auto' (mint a short-lived bootstrap token through the Kubernetes API on every boot: in-cluster, via --kubeconfig, or with Booty's own CA when --controlPlane=managed)")
	flags.Duration(config.JoinTokenTTL, config.DefaultJoinTokenTTL, "Lifetime of bootstrap tokens minted with --kubeadmJoin=auto; expired ones are deleted on the --updateSchedule tick")
	flags.String(config.Profile, "", "Node profile appended to the builtin Ignition fragment for flatcar/coreos hosts: '' or 'kubeadm-worker' (CNI plugins, kubeadm/kubelet/kubectl/crictl, kubelet units, kubeadm join on every boot)")
	flags.String(config.K8sVersion, config.DefaultK8sVersion, "Kubernetes release installed by the kubeadm-worker profile")
	flags.String(config.CNIVersion, config.DefaultCNIVersion, "containernetworking/plugins release installed by the kubeadm-worker profile")
	flags.String(config.CrictlVersion, "", "cri-tools release installed by the kubeadm-worker profile; defaults to the --k8sVersion minor with patch 0 (cri-tools tags once per minor, e.g. v1.34.0)")
	flags.String(config.ContainerdDisk, "", "Block device the kubeadm-worker profile formats (ext4, wiped on every boot) and mounts at /var/lib/containerd, e.g. /dev/sda; empty keeps containerd on the root filesystem")
	flags.String(config.KubeletUnitsURL, config.DefaultKubeletUnitsURL, "Base URL the kubeadm-worker profile fetches kubelet/kubelet.service and kubeadm/10-kubeadm.conf from (pin or mirror it)")
	flags.String(config.ClusterDistribution, config.DefaultClusterDistribution, "Kubernetes distribution of the cluster Booty provisions: 'kubeadm' or 'k0s' (Bluefin hosts support k0s only)")
	flags.String(config.ControlPlane, config.DefaultControlPlane, "Who runs the control plane: 'external' (join-only, today's behaviour) or 'managed' (Booty generates the cluster CA under --dataDir/cluster/ and renders the role: control-plane host)")
	flags.String(config.ControlPlaneEndpt, "", "host[:port] every node uses for the API server (VIP or DNS name for HA); with --controlPlane=managed it defaults to the single role: control-plane host's IP")
	flags.String(config.ClusterCADir, "", "Bring-your-own cluster CA directory for --controlPlane=managed (kubeadm: ca.crt/ca.key; k0s: also sa.key, sa.pub, etcd/ca.crt, etcd/ca.key), read-only; empty generates one under --dataDir/cluster/pki")
	flags.String(config.CNI, config.DefaultCNI, "Network plugin installed from the first control plane: 'cilium', 'calico', 'flannel' or 'none'")
	flags.String(config.CNIRelease, "", "Overrides the pinned release of the selected --cni (--cniVersion keeps meaning containernetworking/plugins)")
	flags.String(config.PodCIDR, config.DefaultPodCIDR, "Pod network CIDR of the cluster")
	flags.String(config.ServiceCIDR, config.DefaultServiceCIDR, "Service network CIDR of the cluster")
	flags.String(config.K0sTokenFile, "", "File holding a pre-made k0s worker join token for an external k0s control plane")
	flags.String(config.Kubeconfig, "", "Kubeconfig for minting --kubeadmJoin=auto tokens against an external kubeadm control plane from outside the cluster")
	flags.String(config.ControlPlaneDisk, "", "Block device the managed kubeadm control plane formats once (ext4, label booty-cp, never wiped) and keeps /etc/kubernetes, /var/lib/etcd and /var/lib/kubelet on, e.g. /dev/vda; required for a role: control-plane host on PXE-booted Flatcar/CoreOS and must differ from --containerdDisk")

	if err := viper.BindPFlags(flags); err != nil {
		fmt.Fprintln(os.Stderr, "binding flags:", err)
		os.Exit(1)
	}
	config.FlagChanged = flags.Changed
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
	if err := profile.Validate(viper.GetString(config.Profile)); err != nil {
		return err
	}
	if err := config.ValidateKubeadmJoin(viper.GetString(config.KubeadmJoin)); err != nil {
		return err
	}
	if viper.GetDuration(config.JoinTokenTTL) <= 0 {
		return fmt.Errorf("--%s must be positive, got %s", config.JoinTokenTTL, viper.GetDuration(config.JoinTokenTTL))
	}
	if viper.GetDuration(config.InstallMinDuration) <= 0 {
		return fmt.Errorf("--%s must be positive, got %s", config.InstallMinDuration, viper.GetDuration(config.InstallMinDuration))
	}
	clusterSettings := cluster.FromConfig()
	if err := clusterSettings.Validate(); err != nil {
		return err
	}
	if err := config.ResolveServerAddress(); err != nil {
		return err
	}
	slog.Info("Client-facing address", "server", config.ServerHostPort(), "builtin", viper.GetString(config.Builtin), "profile", viper.GetString(config.Profile), "kubeadmJoin", viper.GetString(config.KubeadmJoin))
	slog.Info("Cluster settings", "distribution", clusterSettings.Distribution, "controlPlane", clusterSettings.ControlPlane, "endpoint", clusterSettings.Endpoint, "cni", clusterSettings.CNI, "controlPlaneDisk", clusterSettings.ControlPlaneDisk)
	if clusterSettings.ManagedKubeadm() {
		if clusterSettings.Profile == "" {
			slog.Info("Managed kubeadm control plane implies the kubeadm-worker profile for worker hosts")
		}
		if clusterSettings.ControlPlaneDisk == "" {
			slog.Warn("--controlPlaneDisk is not set; Flatcar/CoreOS control-plane hosts will be refused at render time (HTTP 400)")
		}
	}
	if !builtin.Enabled() {
		slog.Info("Builtin Ignition fragment disabled; serving user configs as-is")
	}
	if autoOS := viper.GetString(config.AutoRegister); autoOS != "" {
		slog.Warn("Auto-registration enabled: any unknown MAC that boots becomes a registered host", "os", autoOS, "hostnameTemplate", viper.GetString(config.HostnameTemplate))
	}
	if p := viper.GetString(config.Profile); p != "" && !builtin.Enabled() {
		slog.Warn("--profile has no effect with --builtin=none", "profile", p)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dataDir := viper.GetString(config.DataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir %s: %w", dataDir, err)
	}
	if keysFile := viper.GetString(config.SSHAuthorizedKeysFl); keysFile != "" {
		if _, err := ignition.LoadSSHKeys(keysFile, nil); err != nil {
			slog.Warn("SSH authorized keys file is not readable; sshkeys builtin will have no file keys", "file", keysFile, "error", err)
		}
	}
	state.Init()
	versions.VerifyLocalArtifacts()
	if err := hardware.Load(); err != nil {
		return err
	}
	clusterManager, err := cluster.New(clusterSettings)
	if err != nil {
		return err
	}
	if clusterManager.PKI != nil {
		slog.Warn("Booty holds the cluster CA; --dataDir/cluster/ is cluster-admin material", "dir", clusterManager.PKI.Dir())
	}
	minter, err := newJoinMinter(clusterManager)
	if err != nil {
		return err
	}
	if minter == nil {
		if file := viper.GetString(config.JoinStringFile); file != "" {
			if _, err := config.StaticJoinString(); err != nil {
				slog.Warn("Join string file is not readable; hosts will get an empty JOIN_STRING until it is", "file", file, "error", err)
			}
		}
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
	httpServer, err := server.Start(server.Options{WebFS: webFS, WebDir: viper.GetString(config.WebDir), BootFiles: bootFiles, Minter: minter, Cluster: clusterManager}, errCh)
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
		versions.BluefinVersionCheck()
		<-ready
		versions.ReplayStoredManifests(ctx, "http://"+config.LocalRegistry())
		versions.OSTreeImageSync()
	}()

	var extraJobs []versions.Job
	if minter != nil {
		extraJobs = append(extraJobs, versions.Job{Name: "kubeadm-token-cleanup", Fn: func() { cleanupJoinTokens(ctx, minter) }})
	}
	scheduler, err := versions.StartScheduler(viper.GetString(config.UpdateSchedule), extraJobs...)
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

// newJoinMinter picks where --kubeadmJoin=auto tokens are minted: through
// --kubeconfig when set, with Booty's own CA for a managed control plane,
// otherwise in-cluster as before. It returns nil in static mode.
func newJoinMinter(m *cluster.Manager) (*kubeadm.Minter, error) {
	if viper.GetString(config.KubeadmJoin) != config.KubeadmJoinAuto {
		return nil, nil
	}
	ttl := viper.GetDuration(config.JoinTokenTTL)
	switch {
	case m.Settings.Kubeconfig != "":
		minter, err := kubeadm.FromKubeconfig(m.Settings.Kubeconfig, ttl)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", config.Kubeconfig, err)
		}
		slog.Info("Minting kubeadm join tokens through --kubeconfig", "apiServer", minter.APIServer(), "ttl", ttl)
		return minter, nil
	case m.Settings.Managed() && m.PKI != nil:
		endpoint, err := m.Endpoint(hardware.Snapshot().Hosts)
		if err != nil {
			slog.Warn("--kubeadmJoin=auto with a managed control plane but no endpoint yet; join tokens cannot be minted until it resolves", "error", err)
			return kubeadm.New(kubeadm.KubeConfig{}, ttl), nil
		}
		minter, err := kubeadm.FromCA(m.PKI, endpoint, ttl)
		if err != nil {
			return nil, err
		}
		slog.Info("Minting kubeadm join tokens with the Booty cluster CA", "apiServer", minter.APIServer(), "ttl", ttl)
		return minter, nil
	}
	minter := kubeadm.InCluster(ttl)
	if minter.APIServer() == "" {
		slog.Warn("--kubeadmJoin=auto but not running in a cluster; join tokens cannot be minted and workers will boot without joining")
	} else {
		slog.Info("Minting kubeadm join tokens through the Kubernetes API", "apiServer", minter.APIServer(), "ttl", ttl)
	}
	return minter, nil
}

func cleanupJoinTokens(ctx context.Context, minter *kubeadm.Minter) {
	deleted, err := minter.Cleanup(ctx)
	if err != nil {
		slog.Warn("Expired kubeadm token cleanup incomplete", "deleted", deleted, "error", err)
		return
	}
	slog.Debug("Expired kubeadm token cleanup done", "deleted", deleted)
}

func configureLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}
