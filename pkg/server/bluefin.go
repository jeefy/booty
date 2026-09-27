package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	v3_6 "github.com/coreos/ignition/v2/config/v3_6"
	ign36 "github.com/coreos/ignition/v2/config/v3_6/types"
	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/cluster/k0s"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	ign "github.com/jeefy/booty/pkg/ignition"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

const (
	bluefinPathPrefix = "/bluefin/"
	// bluefinBootFile is the name Booty hands UEFI HTTP Boot clients; any
	// *.efi below /bluefin/<mac>/ serves the current netboot UKI.
	bluefinBootFile = "bluefin-server-netboot.efi"
	// bluefinNodeConfig is the name the netboot initrd HEADs next to the
	// UKI's URL when no Ignition credential is present.
	bluefinNodeConfig = "bluefin-node.ign"

	bluefinInstallUnit   = "booty-install.service"
	bluefinVarLabel      = "bluefin-var"
	bluefinVarMountUnit  = "var.mount"
	bluefinExtensionsDir = "/etc/extensions"
	bluefinK0sSysext     = "/var/lib/k0s/k0s.raw"
	bluefinK0sFirstBoot  = "k0s-first-boot.service"
	// bluefinESP is where run-bluefin-boot.mount mounts the ESP of the
	// running DDI, the installer's source.
	bluefinESP      = "/run/bluefin/boot"
	bluefinESPMount = "run-bluefin-boot.mount"
)

// bluefinBootURL is the UEFI HTTP Boot URL of mac's netboot UKI. The MAC is
// written with dashes so no firmware URL parser trips over colons in the
// path; the route accepts either form.
func bluefinBootURL(mac string) string {
	return "http://" + config.ServerHostPort() + bluefinPathPrefix + strings.ReplaceAll(mac, ":", "-") + "/" + bluefinBootFile
}

// BluefinHTTPBoot is the ProxyDHCP HostHTTPBoot hook: a registered Bluefin
// host (or any unknown MAC under --autoRegister=bluefin) is offered its
// netboot UKI, an installed one that is not being reinstalled is offered
// nothing so its firmware boots the disk, and everyone else keeps the
// default answer.
func BluefinHTTPBoot(hw net.HardwareAddr) (string, bool) {
	mac := hw.String()
	host, ok := hardware.Get(mac)
	switch {
	case !ok:
		if viper.GetString(config.AutoRegister) == "bluefin" {
			return bluefinBootURL(mac), true
		}
		return "", false
	case host.OS != "bluefin":
		return "", false
	case host.Installed() && !host.DoInstall:
		return "", true
	}
	return bluefinBootURL(mac), true
}

// handleBluefinRequest serves /bluefin/<mac>/<name> to registered Bluefin
// hosts: any *.efi is the current netboot UKI, bluefin-server_<version>.raw
// the DDI of the current or previous release, SHA256SUMS(.gpg) the current
// release's, and bluefin-node.ign the host's Ignition config. It is routed
// before the mux (firmware never follows the mux's clean-path redirects)
// and answers HEAD like GET.
func handleBluefinRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if slices.Contains(strings.Split(r.URL.Path, "/"), "..") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	rawMAC, name, ok := strings.Cut(strings.TrimPrefix(path.Clean(r.URL.Path), bluefinPathPrefix), "/")
	if !ok || name == "" || strings.Contains(name, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	mac, err := hardware.NormalizeMAC(rawMAC)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch {
	case strings.EqualFold(path.Ext(name), ".efi"):
		serveBluefinUKI(w, r, mac)
		return
	case name == bluefinNodeConfig:
		serveBluefinNodeConfig(w, r, mac)
		return
	}
	if host, ok := hardware.Get(mac); !ok || host.OS != "bluefin" {
		writeError(w, http.StatusNotFound, "host not registered as bluefin")
		return
	}
	switch {
	case name == versions.BluefinSumsFile || name == versions.BluefinSigFile:
		if _, ok := versions.CurrentBluefinManifest(); !ok {
			writeError(w, http.StatusNotFound, "no Bluefin release cached yet")
			return
		}
		serveBluefinFile(w, r, config.BluefinCurrentLink, name, "application/octet-stream")
	case strings.HasPrefix(name, "bluefin-server_") && strings.HasSuffix(name, ".raw"):
		for _, link := range []string{config.BluefinCurrentLink, config.BluefinPreviousLink} {
			if m, ok := bluefinManifest(link); ok && m.DDI == name {
				serveBluefinFile(w, r, link, name, "application/octet-stream")
				return
			}
		}
		writeError(w, http.StatusNotFound, "not the current or previous Bluefin release")
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func bluefinManifest(link string) (versions.BluefinManifest, bool) {
	if link == config.BluefinPreviousLink {
		return versions.PreviousBluefinManifest()
	}
	return versions.CurrentBluefinManifest()
}

// bluefinHost resolves a boot request to a registered Bluefin host. A real
// boot from an unknown MAC auto-registers it under --autoRegister=bluefin
// and is recorded as an unknown host otherwise; other --autoRegister values
// never register through this path.
func bluefinHost(r *http.Request, mac string) (*hardware.Host, bool) {
	host, ok := hardware.Get(mac)
	if !ok && !isPreview(r) {
		if viper.GetString(config.AutoRegister) == "bluefin" {
			host = autoRegister(mac, remoteIP(r))
			ok = host != nil
		} else {
			hardware.Observe(mac, remoteIP(r))
			slog.Warn("Unknown host requested a Bluefin netboot UKI", "mac", mac, "ip", remoteIP(r))
		}
	}
	return host, ok && host.OS == "bluefin"
}

// serveBluefinUKI answers the firmware's fetch of the netboot UKI. An
// installed host that is not being reinstalled gets 404 so the firmware
// moves on to the disk. A real boot records the host's IP and boot time,
// finishes a pending install (see bluefinInstallDone) and stamps
// installServedAt when this boot will install.
func serveBluefinUKI(w http.ResponseWriter, r *http.Request, mac string) {
	host, ok := bluefinHost(r, mac)
	if !ok {
		writeError(w, http.StatusNotFound, "host not registered as bluefin")
		return
	}
	now := time.Now()
	if !isPreview(r) {
		host = bluefinInstallDone(mac, host, now)
	}
	if host.Installed() && !host.DoInstall {
		slog.Info("Installed Bluefin host asked for the netboot UKI; answering 404 so it boots its disk", "mac", mac)
		writeError(w, http.StatusNotFound, "host is installed; set doInstall to reinstall it")
		return
	}
	m, ok := versions.CurrentBluefinManifest()
	if !ok {
		writeError(w, http.StatusNotFound, "no Bluefin release cached yet")
		return
	}
	if !isPreview(r) {
		if r.Method == http.MethodGet {
			if err := hardware.MarkBooted(mac, remoteIP(r), now); err != nil {
				slog.Error("Could not record boot", "mac", mac, "error", err)
			}
		}
		recordInstallServed(mac, host, now)
		slog.Info("Serving Bluefin netboot UKI", "mac", mac, "version", m.Version, "method", r.Method, "doInstall", host.DoInstall)
	}
	serveBluefinFile(w, r, config.BluefinCurrentLink, m.NetbootUKI, efiContentType)
}

// bluefinInstallDone implements --doInstallClearOn=next-boot for Bluefin:
// the first netboot UKI fetch at least --installMinDuration after an
// install boot was served means systemd-sysinstall finished and rebooted,
// so doInstall is cleared and the host marked installed; an earlier one
// means the install failed and it is served again. It returns the host as
// it should be answered. 'booted' behaves the same for Bluefin: the
// installed disk runs no Booty Ignition, so it never POSTs /booted, and
// waiting for it would reinstall on every boot.
func bluefinInstallDone(mac string, host *hardware.Host, now time.Time) *hardware.Host {
	if !host.DoInstall || viper.GetString(config.DoInstallClearOn) == config.ClearOnIgnition {
		return host
	}
	served, err := time.Parse(time.RFC3339, host.InstallServedAt)
	if err != nil {
		return host
	}
	if elapsed := now.Sub(served); elapsed < viper.GetDuration(config.InstallMinDuration) {
		slog.Info("Netboot again too soon after the install boot; keeping doInstall", "mac", mac, "elapsed", elapsed.Round(time.Second), "min", viper.GetDuration(config.InstallMinDuration))
		return host
	}
	updated, err := markBluefinInstalled(mac, "next boot after install")
	if err != nil {
		return host
	}
	return updated
}

// recordInstallServed stamps installServedAt when this boot of a Bluefin
// host will install, so bluefinInstallDone can measure from it.
func recordInstallServed(mac string, host *hardware.Host, now time.Time) {
	if host == nil || !host.DoInstall || host.OS != "bluefin" {
		return
	}
	stamp := now.UTC().Format(time.RFC3339)
	if _, err := hardware.Update(mac, func(h *hardware.Host) { h.InstallServedAt = stamp }); err != nil {
		slog.Error("Could not record install served", "mac", mac, "error", err)
	}
}

// markBluefinInstalled clears doInstall and switches the host to mode
// installed, after which it gets no netboot answer until doInstall is set
// again.
func markBluefinInstalled(mac, trigger string) (*hardware.Host, error) {
	updated, err := hardware.Update(mac, func(h *hardware.Host) {
		h.DoInstall, h.InstallServedAt, h.Mode = false, "", hardware.ModeInstalled
	})
	if err != nil {
		slog.Error("Could not mark Bluefin host installed", "mac", mac, "error", err)
		return nil, err
	}
	slog.Info("Cleared doInstall; Bluefin host now boots from disk", "mac", mac, "trigger", trigger)
	return updated, nil
}

// serveBluefinNodeConfig answers the initrd's HEAD and GET of
// bluefin-node.ign with the host's rendered Ignition config, 404 when there
// is nothing to configure. Only a real GET records the boot and, under
// --doInstallClearOn=ignition, marks an installing host installed: the
// config it just received carries the install unit.
func serveBluefinNodeConfig(w http.ResponseWriter, r *http.Request, mac string) {
	host, ok := hardware.Get(mac)
	if !ok || host.OS != "bluefin" {
		writeError(w, http.StatusNotFound, "host not registered as bluefin")
		return
	}
	preview := isPreview(r)
	cfg, err := renderBluefinNode(r.Context(), mac, host, !preview)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to render ignition config")
		return
	}
	if bluefinNodeEmpty(cfg) {
		writeError(w, http.StatusNotFound, "nothing to configure for this host")
		return
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode ignition config")
		return
	}
	if !preview && r.Method == http.MethodGet {
		if err := hardware.MarkBooted(mac, remoteIP(r), time.Now()); err != nil {
			slog.Error("Could not record boot", "mac", mac, "error", err)
		}
		if host.DoInstall && host.InstallDisk != "" && viper.GetString(config.DoInstallClearOn) == config.ClearOnIgnition {
			_, _ = markBluefinInstalled(mac, "ignition fetch")
		}
		slog.Info("Serving Bluefin node Ignition", "mac", mac, "bytes", len(body))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(body); err != nil {
		slog.Debug("Writing Bluefin node Ignition failed", "mac", mac, "error", err)
	}
}

func serveBluefinFile(w http.ResponseWriter, r *http.Request, link, name, contentType string) {
	p := config.DataPath(config.BluefinDir, link, name)
	f, err := os.Open(p)
	if err != nil {
		slog.Error("Bluefin release file missing", "path", p, "error", err)
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer config.CloseQuietly(f, p)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// bluefinExtensions is what the host runs: its own extensions plus k0s
// when Booty provisions a k0s cluster, which every Bluefin host joins.
func bluefinExtensions(host *hardware.Host) []string {
	exts := slices.Clone(host.Extensions)
	if m := clusterManager; m != nil && m.Settings.Distribution == cluster.K0s && !slices.Contains(exts, hardware.ExtensionK0s) {
		exts = append(exts, hardware.ExtensionK0s)
	}
	slices.Sort(exts)
	return exts
}

// renderBluefinNode builds the Ignition config (spec 3.6.0) a Bluefin host
// boots with. Diskless nodes run Ignition on every boot on a fresh tmpfs
// root, so everything is idempotent: files overwrite, the state disk is
// created once and never wiped. mint says whether a k0s worker token may
// be minted (false for previews).
func renderBluefinNode(ctx context.Context, mac string, host *hardware.Host, mint bool) (ign36.Config, error) {
	var cfg ign36.Config
	cfg.Ignition.Version = ign36.MaxVersion.String()
	features := builtinFeatures()
	if features[ign.FeatureHostname] && host.Hostname != "" {
		cfg.Storage.Files = append(cfg.Storage.Files, bluefinInlineFile("/etc/hostname", host.Hostname+"\n", 0o644))
	}
	if features[ign.FeatureSSHKeys] {
		keys, err := ign.LoadSSHKeys(viper.GetString(config.SSHAuthorizedKeysFl), viper.GetStringSlice(config.SSHAuthorizedKeys))
		if err != nil {
			slog.Warn("Could not read SSH authorized keys file", "file", viper.GetString(config.SSHAuthorizedKeysFl), "error", err)
		}
		if len(keys) > 0 {
			root := ign36.PasswdUser{Name: "root"}
			for _, k := range keys {
				root.SSHAuthorizedKeys = append(root.SSHAuthorizedKeys, ign36.SSHAuthorizedKey(k))
			}
			cfg.Passwd.Users = append(cfg.Passwd.Users, root)
		}
	}
	if host.StateDisk != "" {
		addBluefinStateDisk(&cfg, host.StateDisk)
	}

	release, haveRelease := versions.CurrentBluefinManifest()
	if exts := bluefinExtensions(host); len(exts) > 0 {
		if !haveRelease {
			slog.Warn("No Bluefin release cached; serving the node config without its extensions", "mac", mac, "extensions", exts)
		} else {
			addBluefinExtensions(&cfg, mac, release, exts)
		}
	}
	if m := clusterManager; m != nil && m.Settings.Distribution == cluster.K0s {
		node, err := m.K0sNodeFiles(ctx, hardware.Snapshot().Hosts, host, config.ServerHostPort(), mint)
		if err != nil {
			slog.Error("k0s node unavailable; serving the Bluefin node config without it", "mac", mac, "role", cluster.RoleOf(host), "error", err)
		}
		addK0sNode(&cfg, node)
	}
	if host.DoInstall {
		switch {
		case host.InstallDisk == "":
			slog.Warn("doInstall set without installDisk; booting diskless without installing", "mac", mac)
		case !haveRelease:
			slog.Warn("doInstall set but no Bluefin release cached", "mac", mac)
		default:
			cfg.Systemd.Units = append(cfg.Systemd.Units, bluefinUnit(bluefinInstallUnit, bluefinInstallUnitContents(release.Version, host.InstallDisk)))
		}
	}

	if host.IgnitionFile == "" {
		return cfg, nil
	}
	user, err := renderUserIgnition(mac, host, "")
	if err != nil {
		return cfg, err
	}
	child, rpt, err := v3_6.ParseCompatibleVersion(user)
	if err != nil {
		slog.Error("Host Ignition template does not parse as Ignition 3.x", "mac", mac, "file", host.IgnitionFile, "error", err, "report", rpt.String())
		return cfg, err
	}
	return v3_6.Merge(cfg, child), nil
}

func bluefinNodeEmpty(cfg ign36.Config) bool {
	return len(cfg.Storage.Files) == 0 && len(cfg.Storage.Disks) == 0 && len(cfg.Storage.Filesystems) == 0 &&
		len(cfg.Storage.Directories) == 0 && len(cfg.Storage.Links) == 0 && len(cfg.Systemd.Units) == 0 &&
		len(cfg.Passwd.Users) == 0 && len(cfg.Passwd.Groups) == 0 && cfg.Ignition.Config.Merge == nil && cfg.Ignition.Config.Replace.Source == nil
}

func boolPtr(b bool) *bool         { return &b }
func strPtr(s string) *string      { return &s }
func intPtr(i int) *int            { return &i }
func bluefinOverwrite() ign36.Node { return ign36.Node{Overwrite: boolPtr(true)} }

// bluefinInlineFile overwrites path on every boot: Ignition refuses to
// write over an existing file otherwise, and /var may be persistent.
func bluefinInlineFile(p, contents string, mode int) ign36.File {
	n := bluefinOverwrite()
	n.Path = p
	return ign36.File{Node: n, FileEmbedded1: ign36.FileEmbedded1{Contents: ign36.Resource{Source: strPtr(ign.DataURL(contents))}, Mode: intPtr(mode)}}
}

func bluefinRemoteFile(p, source, sha256 string) ign36.File {
	n := bluefinOverwrite()
	n.Path = p
	return ign36.File{Node: n, FileEmbedded1: ign36.FileEmbedded1{
		Contents: ign36.Resource{Source: strPtr(source), Verification: ign36.Verification{Hash: strPtr("sha256-" + sha256)}},
		Mode:     intPtr(0o644),
	}}
}

func bluefinUnit(name, contents string) ign36.Unit {
	return ign36.Unit{Name: name, Enabled: boolPtr(true), Contents: strPtr(contents)}
}

// addBluefinStateDisk keeps /var on disk: partition and xfs labelled
// bluefin-var, created when missing and never wiped, mounted by the real
// root's var.mount (as tests/fixtures/ignition/var-on-disk.ign in
// projectbluefin/server). The filesystem also names path /var so
// Ignition's mount stage writes the config's /var files onto the disk
// instead of the tmpfs var.mount later covers.
func addBluefinStateDisk(cfg *ign36.Config, disk string) {
	cfg.Storage.Disks = append(cfg.Storage.Disks, ign36.Disk{
		Device:     disk,
		WipeTable:  boolPtr(false),
		Partitions: []ign36.Partition{{Number: 1, Label: strPtr(bluefinVarLabel), WipePartitionEntry: boolPtr(false)}},
	})
	cfg.Storage.Filesystems = append(cfg.Storage.Filesystems, ign36.Filesystem{
		Device:         "/dev/disk/by-partlabel/" + bluefinVarLabel,
		Format:         strPtr("xfs"),
		Label:          strPtr(bluefinVarLabel),
		Path:           strPtr("/var"),
		WipeFilesystem: boolPtr(false),
	})
	cfg.Systemd.Units = append(cfg.Systemd.Units, bluefinUnit(bluefinVarMountUnit, `[Unit]
Description=Persistent /var on the Ignition-provisioned disk

[Mount]
What=/dev/disk/by-label/`+bluefinVarLabel+`
Where=/var
Type=xfs
Options=defaults

[Install]
WantedBy=local-fs.target
`))
}

// addBluefinExtensions downloads the opted-in sysexts from Booty's copy of
// the release. zfs and kubestellar land in /etc/extensions under their
// versioned name (their extension-release file is
// extension-release.<name>_<version>); k0s is placed where
// k0s-first-boot.service picks it up.
func addBluefinExtensions(cfg *ign36.Config, mac string, release versions.BluefinManifest, exts []string) {
	for _, name := range exts {
		sysext, ok := release.Sysexts[name]
		if !ok {
			slog.Warn("Bluefin release has no such sysext; skipping it", "mac", mac, "extension", name, "version", release.Version)
			continue
		}
		source := "http://" + config.ServerHostPort() + "/data/" + path.Join(config.BluefinDir, release.Version, sysext.File)
		if name == hardware.ExtensionK0s {
			cfg.Storage.Files = append(cfg.Storage.Files, bluefinRemoteFile(bluefinK0sSysext, source, sysext.Sha256))
			cfg.Systemd.Units = append(cfg.Systemd.Units, ign36.Unit{Name: bluefinK0sFirstBoot, Enabled: boolPtr(true)})
			continue
		}
		cfg.Storage.Files = append(cfg.Storage.Files, bluefinRemoteFile(filepath.Join(bluefinExtensionsDir, sysext.File), source, sysext.Sha256))
	}
}

func addK0sNode(cfg *ign36.Config, node *k0s.Node) {
	if node == nil {
		return
	}
	for _, f := range node.Files {
		cfg.Storage.Files = append(cfg.Storage.Files, bluefinInlineFile(f.Path, f.Contents, f.Mode))
	}
	for _, u := range node.Units {
		unit := ign36.Unit{Name: u.Name, Contents: strPtr(u.Contents)}
		if u.WantedBy != "" {
			unit.Enabled = boolPtr(true)
		}
		cfg.Systemd.Units = append(cfg.Systemd.Units, unit)
	}
}

// bluefinInstallUnitContents installs the running DDI to disk with
// systemd-sysinstall and reboots into it; the disk UKI comes from the ESP
// of the DDI the node booted.
func bluefinInstallUnitContents(version, disk string) string {
	kernel := fmt.Sprintf("%s/EFI/Linux/bluefin-server-%s.efi", bluefinESP, version)
	return `[Unit]
Description=Booty: install Bluefin Server ` + version + ` to ` + disk + `
Wants=network-online.target
After=network-online.target ` + bluefinESPMount + `
Requires=` + bluefinESPMount + `

[Service]
Type=oneshot
ExecStart=/usr/bin/systemd-sysinstall --erase=yes --confirm=no --variables=yes --reboot=yes --definitions=` + bluefinESP + `/bluefin/repart.d --kernel=` + kernel + ` ` + disk + `

[Install]
WantedBy=multi-user.target
`
}
