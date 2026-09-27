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

[[secure-boot-shim]]kernel ${BASEURL}/fedora-coreos-${VERSION}-live-kernel-${ARCH} enforcing=0 initrd=main coreos.live.rootfs_url=${BASEURL}/fedora-coreos-${VERSION}-live-rootfs.${ARCH}.img ignition.firstboot ignition.platform.id=metal ignition.firstboot=1 ignition.config.url=${CONFIGURL}
initrd --name main ${BASEURL}/fedora-coreos-${VERSION}-live-initramfs.${ARCH}.img
boot || goto shell
exit
:shell
echo Booty: boot failed - dropping to the iPXE shell
shell
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

	"secureboot-refused.ipxe": `#!ipxe
echo
echo Booty: this machine reached Booty through Secure Boot, and [[os-label]] cannot boot that way.
echo
[[secure-boot-refusal]]echo
set menu-timeout 30000
:start
menu Booty - Secure Boot: [[os-label]] refused - [[hostname]]
item --key d run-from-disk Boot from disk
item --key r reboot        Reboot
item --key s shell         iPXE shell
choose --timeout ${menu-timeout} --default run-from-disk selected || goto run-from-disk
set menu-timeout 0
goto ${selected}
:run-from-disk
exit
:reboot
reboot
:shell
shell
goto start
`,
}

const secureBootRefusedKey = "secureboot-refused"

var secureBootOSLabels = map[string]string{
	"flatcar": "Flatcar",
	"bluefin": "Bluefin Server",
}

type TemplateVars struct {
	Server        string
	Hostname      string
	MenuDefault   string
	CoreOSChannel string
	CoreOSArch    string
	CoreOSVersion string
	OSTreeImage   string
	// SecureBoot is set when the client arrived through the signed iPXE
	// (its autoexec.ipxe adds sb=1): kernels then go through firmware
	// verification, so the coreos script loads Fedora's shim first.
	SecureBoot bool
	// SecureBootTrustedFlatcar is the operator's --secureBootTrusted=flatcar
	// assertion that the fleet's firmware db holds the Flatcar CA, which is
	// what lets Flatcar-signed kernels pass firmware verification.
	SecureBootTrustedFlatcar bool
	// FlatcarCASha256 is the fingerprint of the Flatcar CA Booty serves at
	// /boot/secureboot/flatcar-ca.der, shown in the refusal menu.
	FlatcarCASha256 string
	Bluefin         BluefinVars
}

// secureBootShim is the iPXE line that makes the following kernel command
// verify against the Fedora shim's vendor certificate instead of the
// firmware db, which only holds Microsoft's keys.
func (v TemplateVars) secureBootShim() string {
	if !v.SecureBoot {
		return ""
	}
	return "shim http://" + v.Server + "/boot/secureboot/fedora/shimx64.efi || goto shell\n"
}

// SecureBootRefused reports whether a Secure Boot client registered as os
// gets the refusal menu instead of a kernel: Flatcar unless the operator
// asserted its CA is enrolled, Bluefin always (its installed UKI and
// systemd-boot are unsigned, so an install could never boot).
func SecureBootRefused(os string, v TemplateVars) bool {
	if !v.SecureBoot {
		return false
	}
	switch os {
	case "flatcar":
		return !v.SecureBootTrustedFlatcar
	case "bluefin":
		return true
	}
	return false
}

func (v TemplateVars) flatcarCALines() string {
	fingerprint := v.FlatcarCASha256
	if fingerprint == "" {
		fingerprint = "(not extracted yet: Booty has not synced a Flatcar release)"
	}
	return `echo The kernel is signed by the Flatcar Container Linux Secure Boot CA, which this
echo firmware does not trust (Booty was not started with --secureBootTrusted=flatcar).
echo   CA SHA256: ` + fingerprint + `
echo   Download:  http://` + v.Server + `/boot/secureboot/flatcar-ca.der
echo Enroll that certificate in the firmware db (firmware setup UI, or sbctl enroll-keys
echo --microsoft with it added as a custom db key), restart Booty with
echo --secureBootTrusted=flatcar, or disable Secure Boot on this machine.
`
}

const bluefinUnsignedLines = `echo Bluefin Server's installed system (systemd-boot and its UKI) is not signed, so an
echo installed Bluefin never boots with Secure Boot enabled. Booty refuses to install it on
echo a Secure Boot host. Disable Secure Boot to install Bluefin Server, or wait for
echo upstream (projectbluefin/server) to sign its images.
`

func (v TemplateVars) secureBootRefusal(os string) string {
	var sb strings.Builder
	if !v.SecureBootTrustedFlatcar {
		sb.WriteString(v.flatcarCALines())
	}
	if os == "bluefin" {
		if sb.Len() > 0 {
			sb.WriteString("echo\n")
		}
		sb.WriteString(bluefinUnsignedLines)
	}
	return sb.String()
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
		"[[secure-boot-shim]]", v.secureBootShim(),
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
// release is cached; Secure Boot clients whose OS cannot pass firmware
// verification get the refusal menu (see SecureBootRefused).
func IPXEScript(os string, v TemplateVars) string {
	if SecureBootRefused(os, v) {
		return Render(strings.NewReplacer(
			"[[os-label]]", secureBootOSLabels[os],
			"[[secure-boot-refusal]]", v.secureBootRefusal(os),
		).Replace(PXEConfig[secureBootRefusedKey+".ipxe"]), v)
	}
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
