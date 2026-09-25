package tftp

import (
	"log/slog"
	"strings"

	"github.com/jeefy/booty/pkg/hardware"
)

const DefaultOS = "flatcar"

// PXEConfig holds the iPXE script templates served from /booty.ipxe, keyed
// by "<os>.ipxe". Placeholders use [[name]].
var PXEConfig = map[string]string{
	"flatcar.ipxe": `#!ipxe
echo Hello from Booty!
kernel http://[[server]]/data/flatcar_production_pxe.vmlinuz flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json?mac=${mac}
initrd http://[[server]]/data/flatcar_production_pxe_image.cpio.gz
boot
`,

	"coreos.ipxe": `#!ipxe
echo Hello from Booty!
set BASEURL http://[[server]]/data/
set CONFIGURL http://[[server]]/ignition.json?mac=${mac}
set OSTREE_IMAGE [[ostree-image]]
set STREAM [[coreos-channel]]
set VERSION [[coreos-version]]
set ARCH [[coreos-arch]]

kernel ${BASEURL}/fedora-coreos-${VERSION}-live-kernel-${ARCH} enforcing=0 initrd=main coreos.live.rootfs_url=${BASEURL}/fedora-coreos-${VERSION}-live-rootfs.${ARCH}.img ignition.firstboot ignition.platform.id=metal ignition.firstboot=1 ignition.config.url=${CONFIGURL}
initrd --name main ${BASEURL}/fedora-coreos-${VERSION}-live-initramfs.${ARCH}.img
boot
`,

	"bluefin.ipxe": `#!ipxe
iseq ${platform} efi || goto not-efi
set BASEURL http://[[server]]/data/bluefin/current
set menu-timeout 5000
:start
menu Booty - Bluefin Server [[bluefin-version]] - [[hostname]]
item --key i install       Install Bluefin Server to [[install-disk-label]] (wipes it)
item --key d run-from-disk Boot from disk
item shell                 iPXE shell
item reboot                Reboot
choose --timeout ${menu-timeout} --default [[menu-default]] selected || goto run-from-disk
set menu-timeout 0
goto ${selected}
:install
kernel ${BASEURL}/[[bluefin-vmlinuz]] systemd.unit=system-install.target console=tty0 console=ttyS0,115200 rw unattended inst.ddi_url=${BASEURL}/[[bluefin-ddi]] inst.ddi_sha256=[[bluefin-ddi-sha256]] [[install-disk-arg]] [[creds-args]]
initrd ${BASEURL}/[[bluefin-initrd]]
boot || goto shell
:run-from-disk
exit
:shell
shell
goto start
:reboot
reboot
:not-efi
echo Bluefin Server needs UEFI (this machine booted iPXE in ${platform} mode); dropping to shell
shell
`,

	"bluefin-pending.ipxe": `#!ipxe
echo Bluefin artifacts not downloaded yet
set menu-timeout 5000
:start
menu Booty - Bluefin Server (no release cached yet) - [[hostname]]
item --key d run-from-disk Boot from disk
item shell                 iPXE shell
item reboot                Reboot
choose --timeout ${menu-timeout} --default run-from-disk selected || goto run-from-disk
set menu-timeout 0
goto ${selected}
:run-from-disk
exit
:shell
shell
goto start
:reboot
reboot
`,

	"unknown.ipxe": `#!ipxe
menu Booty - Unknown Host (MAC: ${mac})
item --key b boot    Boot from local disk
item --key r reboot  Reboot
choose --default boot --timeout 30000 selected
goto ${selected}

:boot
exit

:reboot
reboot
`,
}

type TemplateVars struct {
	Server        string
	Hostname      string
	MenuDefault   string
	CoreOSChannel string
	CoreOSArch    string
	CoreOSVersion string
	OSTreeImage   string
	Bluefin       BluefinVars
}

// BluefinVars fills the bluefin.ipxe template. Vmlinuz/Initrd/DDI/DDISha256
// come from the served release's manifest; an empty Vmlinuz means no release
// is cached and the pending menu is served instead.
type BluefinVars struct {
	Version   string
	Vmlinuz   string
	Initrd    string
	DDI       string
	DDISha256 string
	// InstallDisk is the host's installDisk; empty lets the installer pick
	// the first writable disk.
	InstallDisk string
	// CredsURL/CredsSha256 point the installer at the host's credentials
	// bundle; both empty omits inst.creds_*.
	CredsURL    string
	CredsSha256 string
}

func (b BluefinVars) installDiskArg() string {
	if b.InstallDisk == "" {
		return ""
	}
	return "inst.target_disk=" + b.InstallDisk
}

func (b BluefinVars) installDiskLabel() string {
	if b.InstallDisk == "" {
		return "the first writable disk"
	}
	return b.InstallDisk
}

func (b BluefinVars) credsArgs() string {
	if b.CredsURL == "" || b.CredsSha256 == "" {
		return ""
	}
	return "inst.creds_url=" + b.CredsURL + " inst.creds_sha256=" + b.CredsSha256
}

func Render(template string, v TemplateVars) string {
	return strings.NewReplacer(
		"[[server]]", v.Server,
		"[[hostname]]", v.Hostname,
		"[[menu-default]]", v.MenuDefault,
		"[[coreos-channel]]", v.CoreOSChannel,
		"[[coreos-arch]]", v.CoreOSArch,
		"[[coreos-version]]", v.CoreOSVersion,
		"[[ostree-image]]", v.OSTreeImage,
		"[[bluefin-version]]", v.Bluefin.Version,
		"[[bluefin-vmlinuz]]", v.Bluefin.Vmlinuz,
		"[[bluefin-initrd]]", v.Bluefin.Initrd,
		"[[bluefin-ddi]]", v.Bluefin.DDI,
		"[[bluefin-ddi-sha256]]", v.Bluefin.DDISha256,
		"[[install-disk-arg]]", v.Bluefin.installDiskArg(),
		"[[install-disk-label]]", v.Bluefin.installDiskLabel(),
		"[[creds-args]]", v.Bluefin.credsArgs(),
	).Replace(template)
}

// OSForHost picks the OS key to serve for host: "unknown" for unregistered
// hosts, the host's OS when valid, otherwise DefaultOS with a warning.
func OSForHost(host *hardware.Host) string {
	if host == nil {
		return "unknown"
	}
	if host.OS == "" {
		return DefaultOS
	}
	if !hardware.IsValidOS(host.OS) {
		slog.Warn("Host has unknown OS, falling back to default", "mac", host.MAC, "os", host.OS, "default", DefaultOS)
		return DefaultOS
	}
	return host.OS
}

func MenuDefaultForHost(host *hardware.Host) string {
	if host != nil && host.DoInstall {
		return "install"
	}
	return "run-from-disk"
}

// IPXEScript renders the iPXE script for os, falling back to the unknown-host
// menu when no template exists. Bluefin hosts get the pending menu until a
// release is cached.
func IPXEScript(os string, v TemplateVars) string {
	if os == "bluefin" && v.Bluefin.Vmlinuz == "" {
		os = "bluefin-pending"
	}
	tmpl, ok := PXEConfig[os+".ipxe"]
	if !ok {
		slog.Warn("No iPXE template for OS, serving unknown-host menu", "os", os)
		tmpl = PXEConfig["unknown.ipxe"]
	}
	return Render(tmpl, v)
}
