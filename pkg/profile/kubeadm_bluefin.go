package profile

import (
	"fmt"
	"strings"
)

// Bluefin Server kubeadm worker. The kubeadm sysext (kubeadm_<version>.raw)
// ships kubelet, kubeadm, kubectl, crictl, containerd, ctr and runc in
// /usr/bin, the CNI plugins in /usr/libexec/cni, containerd.service and
// kubelet.service with kubeadm's 10-kubeadm.conf drop-in, and seeds a
// writable /etc/containerd/config.toml (root /var/lib/containerd); it
// enables nothing. Booty adds what the Flatcar profile's chain does after
// its installs: kubelet extra args, the containerd disk and the join.
const (
	BluefinJoinScript = "/etc/booty/kubeadm-join.sh"
	BluefinUnitJoin   = "booty-kubeadm-join.service"
	BluefinCRISocket  = "unix:///run/containerd/containerd.sock"
	// BluefinJoinAttempts bounds the join retries: with the backoff below
	// they span roughly the default one-hour --joinTokenTTL.
	BluefinJoinAttempts = 30
	// BluefinJoinGiveUp is the join script's exit status once the attempts
	// are used up; RestartPreventExitStatus stops systemd retrying.
	BluefinJoinGiveUp = 3

	KubeletDefaults = "/etc/default/kubelet"
	// KubeletExtraArgs is what both the Flatcar profile and Bluefin write
	// to the kubelet's KUBELET_EXTRA_ARGS.
	KubeletExtraArgs = "--cgroup-driver=systemd --fail-swap-on=false"

	// BluefinContainerdDiskPath is where a Bluefin worker mounts
	// --containerdDisk. BluefinContainerdFormatUnit formats the device
	// ext4 (label ContainerdDiskLabel) once, if and only if it carries no
	// filesystem and no partition table; whatever is already on it is
	// mounted as it is and never wiped. There is nothing to preserve from
	// a Flatcar worker that shared the disk (it reformats --containerdDisk
	// on every boot), and the disk could be an installed system's by
	// mistake. Bluefin's containerd root lives in BluefinContainerdSubdir,
	// bind-mounted onto /var/lib/containerd, so the top level stays
	// whatever another OS left there.
	BluefinContainerdDiskPath   = "/var/lib/containerd-disk"
	BluefinContainerdSubdir     = BluefinContainerdDiskPath + "/bluefin"
	BluefinContainerdFormatUnit = "booty-containerd-disk-format.service"
	BluefinFormatScript         = "/etc/booty/containerd-disk-format.sh"
	BluefinContainerdDirUnit    = "booty-containerd-disk-dir.service"
	BluefinContainerdDropin     = "10-booty-containerd-disk.conf"
)

// BluefinFile is a file of the Bluefin worker config.
type BluefinFile struct {
	Path     string
	Mode     int
	Contents string
}

// BluefinDropin is a drop-in of a unit the sysext ships.
type BluefinDropin struct {
	Name     string
	Contents string
}

// BluefinUnit is a unit of the Bluefin worker config. Contents is empty
// for the sysext's own units, which only get enabled and drop-ins.
type BluefinUnit struct {
	Name     string
	Contents string
	Enabled  bool
	Dropins  []BluefinDropin
}

// BluefinWorker is what a Bluefin kubeadm worker gets besides the sysext.
type BluefinWorker struct {
	Files []BluefinFile
	Units []BluefinUnit
}

// BluefinWorkerOptions are the per-host inputs: the registered hostname
// (the Node name), the join string (as for Flatcar: static or minted, empty
// when unavailable), --containerdDisk, and the host's stateDisk and
// installDisk, which the containerd disk format unit refuses to touch.
type BluefinWorkerOptions struct {
	Hostname       string
	JoinString     string
	ContainerdDisk string
	StateDisk      string
	InstallDisk    string
}

// BluefinKubeadmWorker renders the Bluefin kubeadm worker.
func BluefinKubeadmWorker(o BluefinWorkerOptions) BluefinWorker {
	w := BluefinWorker{Files: []BluefinFile{
		{Path: KubeletDefaults, Mode: 0o644, Contents: "KUBELET_EXTRA_ARGS=" + KubeletExtraArgs + "\n"},
		{Path: BluefinJoinScript, Mode: 0o755, Contents: bluefinJoinScript},
	}}
	containerd := BluefinUnit{Name: "containerd.service", Enabled: true}
	if o.ContainerdDisk != "" {
		diskMount := SystemdEscapePath(BluefinContainerdDiskPath) + ".mount"
		w.Files = append(w.Files, BluefinFile{Path: BluefinFormatScript, Mode: 0o755, Contents: bluefinFormatScript})
		w.Units = append(w.Units,
			BluefinUnit{Name: BluefinContainerdFormatUnit, Contents: bluefinDiskFormat(o, diskMount)},
			BluefinUnit{Name: diskMount, Contents: bluefinDiskMount(o.ContainerdDisk)},
			BluefinUnit{Name: BluefinContainerdDirUnit, Contents: bluefinDiskDir(diskMount)},
			BluefinUnit{Name: ContainerdMountUnit, Contents: bluefinBindMount},
		)
		containerd.Dropins = []BluefinDropin{{Name: BluefinContainerdDropin, Contents: "[Unit]\nRequires=" + ContainerdMountUnit + "\nAfter=" + ContainerdMountUnit + "\n"}}
	}
	w.Units = append(w.Units,
		containerd,
		BluefinUnit{Name: "kubelet.service", Enabled: true},
		BluefinUnit{Name: BluefinUnitJoin, Enabled: true, Contents: bluefinJoinUnit(o.JoinString, o.Hostname)},
	)
	return w
}

// bluefinDiskFormat runs bluefinFormatScript once per boot, before the
// disk mount, which Requires= it. DefaultDependencies=no: it is ordered
// before a mount unit. The device unit is what the mount itself waits for
// and is active once udev has processed the device, so it stands in for
// the deprecated systemd-udev-settle.service.
func bluefinDiskFormat(o BluefinWorkerOptions, diskMount string) string {
	device := SystemdEscapePath(o.ContainerdDisk) + ".device"
	return `[Unit]
Description=Booty: format the containerd disk ` + o.ContainerdDisk + ` once if it is blank
DefaultDependencies=no
Requires=` + device + `
After=` + device + `
Before=` + diskMount + `

[Service]
Type=oneshot
RemainAfterExit=yes
Environment="DEVICE=` + systemdQuote(o.ContainerdDisk) + `"
Environment="LABEL=` + ContainerdDiskLabel + `"
Environment="STATE_DISK=` + systemdQuote(o.StateDisk) + `"
Environment="INSTALL_DISK=` + systemdQuote(o.InstallDisk) + `"
ExecStart=/usr/bin/bash ` + BluefinFormatScript + `
`
}

// bluefinFormatScript formats the containerd disk if and only if blkid's
// low-level probe finds nothing on it: exit status 2 with no output, which
// is also what a missing device gets, hence the -b check first. A
// filesystem or a partition table (exit 0), an ambivalent result (8) or an
// error (4) all leave the device alone; only the ambivalent and error
// cases fail the unit. The state and install disks are compared by
// canonical path, so a by-id alias of either is refused too.
const bluefinFormatScript = `#!/bin/bash
set -uo pipefail
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
if [ ! -b "${DEVICE}" ]; then
  echo "${DEVICE} is not a block device; cannot use it as the containerd disk" >&2
  exit 1
fi
dev=$(readlink -f "${DEVICE}")
for protected in "stateDisk=${STATE_DISK:-}" "installDisk=${INSTALL_DISK:-}"; do
  path=${protected#*=}
  [ -n "${path}" ] || continue
  if [ "${dev}" = "$(readlink -f "${path}")" ]; then
    echo "refusing to use ${DEVICE} as the containerd disk: it is this host's ${protected%%=*} (${path})" >&2
    exit 1
  fi
done
signature=$(blkid -p -o export "${dev}")
case $? in
  0)
    echo "${DEVICE} already carries a filesystem or partition table; leaving it as it is:"
    printf '%s\n' "${signature}" | grep -E '^(TYPE|PTTYPE|LABEL)='
    exit 0
    ;;
  2) ;;
  *)
    echo "blkid -p ${dev} failed; not touching ${DEVICE}" >&2
    exit 1
    ;;
esac
echo "${DEVICE} carries no filesystem or partition table; formatting it ext4 with label ${LABEL}"
exec mkfs.ext4 -F -L "${LABEL}" "${dev}"
`

// bluefinDiskMount mounts the containerd disk as it is, once the format
// unit has made sure it carries a filesystem. nofail keeps it out of
// local-fs.target: only containerd.service pulls it in, so a missing disk
// fails the join, never the boot.
func bluefinDiskMount(device string) string {
	return `[Unit]
Description=Booty: containerd disk ` + device + `
Requires=` + BluefinContainerdFormatUnit + `
After=` + BluefinContainerdFormatUnit + `

[Mount]
What=` + device + `
Where=` + BluefinContainerdDiskPath + `
Type=ext4
Options=defaults,nofail
`
}

// bluefinDiskDir creates the bluefin/ subdirectory on the mounted disk. It
// asserts the mount point so it can never create it in the tmpfs root, and
// refuses a symlink, which would bind-mount some other directory.
// DefaultDependencies=no: it is ordered before a mount unit.
func bluefinDiskDir(diskMount string) string {
	return `[Unit]
Description=Booty: containerd root directory on the containerd disk
DefaultDependencies=no
Requires=` + diskMount + `
After=` + diskMount + `
AssertPathIsMountPoint=` + BluefinContainerdDiskPath + `

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/bin/sh -c 'if [ -L ` + BluefinContainerdSubdir + ` ]; then echo "` + BluefinContainerdSubdir + ` is a symlink; refusing to use it" >&2; exit 1; fi; exec mkdir -p -m 0711 ` + BluefinContainerdSubdir + `'
`
}

const bluefinBindMount = `[Unit]
Description=Booty: Bluefin containerd root on the containerd disk
Requires=` + BluefinContainerdDirUnit + `
After=` + BluefinContainerdDirUnit + `

[Mount]
What=` + BluefinContainerdSubdir + `
Where=` + ContainerdPath + `
Type=none
Options=bind,nofail
`

// bluefinJoinUnit runs the join once the sysext is merged. Restart with
// backoff covers a slow network, a slow disk and, above all, the Node of
// the previous boot still being Ready (kubeadm refuses to join then);
// the script bounds the attempts. StartLimitIntervalSec=0 leaves the bound
// to the script.
func bluefinJoinUnit(joinString, hostname string) string {
	return `[Unit]
Description=Booty: join the Kubernetes cluster with kubeadm
Wants=network-online.target containerd.service
After=network-online.target systemd-sysext.service containerd.service
StartLimitIntervalSec=0

[Service]
Type=oneshot
RemainAfterExit=yes
Restart=on-failure
RestartSec=15s
RestartSteps=4
RestartMaxDelaySec=2min
RestartPreventExitStatus=` + fmt.Sprint(BluefinJoinGiveUp) + `
TimeoutStartSec=15min
Environment="JOIN_STRING=` + systemdQuote(joinString) + `"
Environment="NODE_NAME=` + systemdQuote(hostname) + `"
Environment="CRI_SOCKET=` + BluefinCRISocket + `"
Environment="MAX_ATTEMPTS=` + fmt.Sprint(BluefinJoinAttempts) + `"
ExecStart=/usr/bin/bash ` + BluefinJoinScript + `

[Install]
WantedBy=multi-user.target
`
}

// bluefinJoinScript is the Flatcar join.sh for the kubeadm sysext: the
// tools are in /usr/bin, not /opt/bin; containerd and the kubelet are
// started here because their units only exist once systemd-sysext has
// merged the image, after the boot's presets were applied; the Node name
// and CRI socket are explicit; and each attempt is counted and logged.
const bluefinJoinScript = `#!/bin/bash
set -uo pipefail
export PATH=/usr/bin:/usr/sbin
if [ -f /etc/kubernetes/kubelet.conf ]; then
  echo "already joined (/etc/kubernetes/kubelet.conf exists); not joining again"
  exit 0
fi
if [ -z "${JOIN_STRING:-}" ]; then
  echo "JOIN_STRING is empty: Booty could not provide a kubeadm join token; not joining" >&2
  exit 0
fi
mkdir -p /run/booty
attempt=$(( $(cat /run/booty/kubeadm-join.attempts 2>/dev/null || echo 0) + 1 ))
echo "$attempt" > /run/booty/kubeadm-join.attempts
if [ "$attempt" -gt "${MAX_ATTEMPTS}" ]; then
  echo "giving up after ${MAX_ATTEMPTS} failed kubeadm join attempts; reboot to retry with a fresh token" >&2
  exit 3
fi
echo "kubeadm join attempt ${attempt}/${MAX_ATTEMPTS} as ${NODE_NAME:-<hostname>}"
if [ ! -x /usr/bin/kubeadm ]; then
  echo "/usr/bin/kubeadm missing: the kubeadm sysext is not merged" >&2
  exit 1
fi
if [ -z "$(systemctl show -P FragmentPath containerd.service)" ]; then systemctl daemon-reload; fi
systemctl enable --now containerd.service kubelet.service || exit 1
modprobe br_netfilter
sysctl net.bridge.bridge-nf-call-iptables=1
sysctl net.ipv4.ip_forward=1
kubeadm reset -f --cri-socket "${CRI_SOCKET}" || true
args=()
case " ${JOIN_STRING} " in
  *" --config"*) ;;
  *)
    args+=(--cri-socket "${CRI_SOCKET}")
    if [ -n "${NODE_NAME:-}" ]; then args+=(--node-name "${NODE_NAME}"); fi
    ;;
esac
${JOIN_STRING} "${args[@]}" 2>&1 | tee /run/booty/kubeadm-join.log
rc=${PIPESTATUS[0]}
if [ "$rc" -eq 0 ]; then
  echo "joined the cluster as ${NODE_NAME:-<hostname>} on attempt ${attempt}"
  exit 0
fi
if grep -q "already exists in the cluster" /run/booty/kubeadm-join.log; then
  echo "attempt ${attempt}: Node ${NODE_NAME:-<hostname>} is still Ready from its previous boot; retrying until the node controller marks it NotReady" >&2
fi
exit 1
`

// SystemdEscapePath is systemd-escape --path: the unit name stem of the
// mount unit for p.
func SystemdEscapePath(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return "-"
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '/':
			if i > 0 && p[i-1] == '/' {
				continue
			}
			b.WriteByte('-')
		case c == '.' && (i == 0 || p[i-1] == '/'):
			fmt.Fprintf(&b, `\x%02x`, c)
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '.':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String()
}
