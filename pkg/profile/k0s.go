package profile

import (
	"fmt"
	"strings"

	"github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/cluster/k0s"
	ign "github.com/jeefy/booty/pkg/ignition"
)

// Paths and unit names of the k0s fragment on a PXE-booted OS.
const (
	K0sInstallScript = ScriptDir + "/k0s-install.sh"
	K0sSeedScript    = ScriptDir + "/k0s-seed.sh"

	// K0sDataMountUnit is systemd-escape(k0s.DataDir): the bind mount from
	// the control-plane disk on a controller, the wiped containerd disk on
	// a worker.
	K0sDataMountUnit = "var-lib-k0s.mount"
	// K0sContainerdMountUnit is systemd-escape(k0s.ContainerdDir), the
	// containerd disk of a controller.
	K0sContainerdMountUnit = "var-lib-k0s-containerd.mount"
)

// K0sOptions renders a role: control-plane or worker host as a k0s node.
// Node comes from cluster.Manager.K0sNodeFiles; Version and SHA256 pin the
// binary the install oneshot fetches (an empty SHA256 makes it verify
// against the release's sha256sums.txt); ControlPlaneDisk is required for a
// controller.
type K0sOptions struct {
	Node             *k0s.Node
	Version          string
	SHA256           string
	ControlPlaneDisk string
}

func (o *K0sOptions) validate(containerdDisk string) error {
	if o == nil || o.Node == nil {
		return fmt.Errorf("k0s options missing")
	}
	if err := k0s.ValidateVersion(o.Version); err != nil {
		return err
	}
	if o.SHA256 != "" && (len(o.SHA256) != 64 || strings.Trim(o.SHA256, "0123456789abcdef") != "") {
		return fmt.Errorf("k0s options: SHA256 %q is not hex sha256", o.SHA256)
	}
	if o.Node.Role == k0s.Controller {
		if o.ControlPlaneDisk == "" {
			return fmt.Errorf("k0s options: ControlPlaneDisk is required for a controller")
		}
		if o.ControlPlaneDisk == containerdDisk {
			return fmt.Errorf("control plane disk %s is also the containerd disk", o.ControlPlaneDisk)
		}
	}
	return nil
}

func k0sFragment(cfg types.Config, opts Options) (types.Config, error) {
	o := opts.K0s
	if err := o.validate(opts.ContainerdDisk); err != nil {
		return cfg, err
	}
	cfg.Storage.Files = append(cfg.Storage.Files, ign.InlineFile(K0sInstallScript, k0sInstallScript(o.Version, o.SHA256), 0o755))
	for _, f := range o.Node.Files {
		cfg.Storage.Files = append(cfg.Storage.Files, ign.InlineFile(f.Path, f.Contents, f.Mode))
	}
	if o.Node.Role == k0s.Controller {
		cfg.Storage.Files = append(cfg.Storage.Files, ign.InlineFile(K0sSeedScript, k0sSeedScript, 0o755))
		cfg.Storage.Filesystems = append(cfg.Storage.Filesystems, controlPlaneFilesystem(o.ControlPlaneDisk))
		cfg.Systemd.Units = append(cfg.Systemd.Units,
			ign.Unit(ControlPlaneMountUnit, true, controlPlaneMount),
			ign.Unit(UnitSeed, true, k0sSeedUnit),
			ign.Unit(K0sDataMountUnit, true, bindMountUnit(k0s.DataDir, "k0s")),
		)
		if opts.ContainerdDisk != "" {
			cfg.Storage.Filesystems = append(cfg.Storage.Filesystems, wipedFilesystem(opts.ContainerdDisk, k0s.ContainerdDir))
			cfg.Systemd.Units = append(cfg.Systemd.Units, ign.Unit(K0sContainerdMountUnit, true, wipedMount(k0s.ContainerdDir, "RequiresMountsFor="+k0s.DataDir+"\n")))
		}
	} else if opts.ContainerdDisk != "" {
		cfg.Storage.Filesystems = append(cfg.Storage.Filesystems, wipedFilesystem(opts.ContainerdDisk, k0s.DataDir))
		cfg.Systemd.Units = append(cfg.Systemd.Units, ign.Unit(K0sDataMountUnit, true, wipedMount(k0s.DataDir, "")))
	}
	cfg.Systemd.Units = append(cfg.Systemd.Units, ign.Unit(k0s.InstallUnit, true, k0sInstallUnit(o.Version)))
	for _, u := range o.Node.Units {
		cfg.Systemd.Units = append(cfg.Systemd.Units, ign.Unit(u.Name, u.WantedBy != "", u.Contents))
	}
	for _, d := range o.Node.DropIns {
		contents := d.Contents
		cfg.Systemd.Units = append(cfg.Systemd.Units, types.Unit{Name: d.Unit, Dropins: []types.Dropin{{Name: d.Name, Contents: &contents}}})
	}
	return cfg, nil
}

// wipedFilesystem is the --containerdDisk formatted on every boot (label
// ssd, like the kubeadm worker's) mounted at where.
func wipedFilesystem(device, where string) types.Filesystem {
	format, label, path, wipe := "ext4", ContainerdDiskLabel, where, true
	return types.Filesystem{Device: device, Format: &format, Label: &label, Path: &path, WipeFilesystem: &wipe}
}

func wipedMount(where, extra string) string {
	return `[Unit]
Description=Mount the ephemeral containerd disk at ` + where + `
Before=local-fs.target
` + extra + `
[Mount]
What=/dev/disk/by-label/` + ContainerdDiskLabel + `
Where=` + where + `
Type=ext4

[Install]
WantedBy=local-fs.target
`
}

// k0sSeedUnit mirrors seedUnit for /var/lib/k0s: Ignition writes the PKI
// and the tokens manifest to the RAM root, this copies them onto the disk
// before the bind mount hides them.
const k0sSeedUnit = `[Unit]
Description=Seed the control-plane disk with the k0s state directory and the cluster CA
DefaultDependencies=no
RequiresMountsFor=` + ControlPlaneMount + `
After=local-fs-pre.target
Before=local-fs.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + K0sSeedScript + `

[Install]
WantedBy=local-fs.target
`

// k0sSeedScript keeps whatever k0s already wrote on the disk (cp -an) but
// refreshes Booty's own manifests directory, so a controller reboot applies
// the bootstrap Secret Booty currently renders.
const k0sSeedScript = `#!/bin/bash
set -euo pipefail
mkdir -p ` + ControlPlaneMount + `/k0s ` + k0s.DataDir + `
cp -an ` + k0s.DataDir + `/. ` + ControlPlaneMount + `/k0s/
if [ -d ` + k0s.ManifestsDir + ` ]; then
  mkdir -p ` + ControlPlaneMount + `/k0s/manifests/booty
  cp -a ` + k0s.ManifestsDir + `/. ` + ControlPlaneMount + `/k0s/manifests/booty/
fi
chmod 0600 ` + ControlPlaneMount + `/k0s/pki/*.key ` + ControlPlaneMount + `/k0s/pki/etcd/ca.key
echo "control-plane disk seeded for k0s"
`

func k0sInstallUnit(version string) string {
	return `[Unit]
Description=Install k0s ` + version + ` to ` + k0s.PXEBinary + `
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
Restart=on-failure
RestartSec=15s
ExecStart=` + K0sInstallScript + `

[Install]
WantedBy=multi-user.target
`
}

// k0sInstallScript fetches the release binary once (a matching binary is
// left alone, so a disk-installed node does not re-download) and refuses
// to install anything whose sha256 does not match the pin or, for an
// unpinned --k0sVersion, the release's sha256sums.txt.
func k0sInstallScript(version, sha256 string) string {
	return `#!/bin/bash
set -euo pipefail
K0S_VERSION="` + version + `"
K0S_SHA256="` + sha256 + `"
URL="` + k0s.DownloadURL(version) + `"
SUMS_URL="` + k0s.ChecksumsURL(version) + `"
BIN=` + k0s.PXEBinary + `
mkdir -p "$(dirname "$BIN")"
if [ -z "$K0S_SHA256" ]; then
  K0S_SHA256=$(curl -fsSL "$SUMS_URL" | awk -v name="*k0s-${K0S_VERSION}-amd64" '$2 == name { print $1 }')
  if [ -z "$K0S_SHA256" ]; then echo "k0s ${K0S_VERSION}: no amd64 checksum in ${SUMS_URL}" >&2; exit 1; fi
fi
if [ -x "$BIN" ] && echo "${K0S_SHA256}  $BIN" | sha256sum -c - >/dev/null 2>&1; then
  echo "k0s ${K0S_VERSION} already installed at $BIN"
  exit 0
fi
TMP=$(mktemp "$(dirname "$BIN")/.k0s.XXXXXX"); trap 'rm -f "$TMP"' EXIT
curl -fsSL -o "$TMP" "$URL"
echo "${K0S_SHA256}  $TMP" | sha256sum -c -
chmod 0755 "$TMP"
mv -f "$TMP" "$BIN"
echo "k0s ${K0S_VERSION} installed at $BIN"
`
}
