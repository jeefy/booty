package tftp

import (
	"fmt"
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
kernel http://[[server]]/data/[[flatcar-dir]]flatcar_production_pxe.vmlinuz flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json?mac=${mac}
initrd http://[[server]]/data/[[flatcar-dir]]flatcar_production_pxe_image.cpio.gz
boot
`,

	"coreos.ipxe": `#!ipxe
echo Hello from Booty!
set BASEURL http://[[server]]/data/[[coreos-dir]]
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
iseq ${platform} pcbios && goto bios ||
iseq ${platform} efi || goto not-efi
echo
echo Booty: [[hostname]] is a Bluefin Server host, and Booty cannot chainload it from iPXE:
[[bluefin-reason]]echo Switch this machine's network boot to UEFI HTTP Boot (IPv4) in the firmware setup; Booty's
echo ProxyDHCP (--proxyDHCP) answers it with, or point your DHCP server's HTTPClient boot file at:
echo   [[bluefin-boot-url]]
echo
set menu-timeout 30000
:start
menu Booty - Bluefin Server: switch to UEFI HTTP Boot - [[hostname]]
item --key d run-from-disk Boot from disk
item --key s shell         iPXE shell
item --key r reboot        Reboot
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
:not-efi
echo Bluefin Server needs UEFI (this machine booted iPXE in ${platform} mode); dropping to shell
shell
[[bluefin-bios]]`,

	"bluefin-chain.ipxe": `#!ipxe
iseq ${platform} pcbios && goto bios ||
iseq ${platform} efi || goto not-efi
iseq ${buildarch} x86_64 || goto not-efi
set menu-timeout 5000
:start
menu Booty - Bluefin Server [[bluefin-version]] (diskless) - [[hostname]]
item --key n netboot       Boot Bluefin Server [[bluefin-version]] diskless
item --key d run-from-disk Boot from disk
item --key s shell         iPXE shell
item --key r reboot        Reboot
choose --timeout ${menu-timeout} --default netboot selected || goto run-from-disk
set menu-timeout 0
goto ${selected}
:netboot
chain [[bluefin-chain-url]] [[bluefin-chain-cmdline]] || goto chain-failed
:chain-failed
echo Booty: chainloading the Bluefin Server netboot UKI failed
prompt --timeout 5000 Press any key for the menu, or wait to boot from disk || goto run-from-disk
goto start
:run-from-disk
exit
:shell
shell
goto start
:reboot
reboot
:not-efi
echo Bluefin Server needs x86-64 UEFI (this machine booted iPXE ${buildarch} in ${platform} mode); dropping to shell
shell
[[bluefin-bios]]`,

	"bluefin-installed.ipxe": `#!ipxe
echo Booty: [[hostname]] is an installed Bluefin Server host; booting from disk
exit
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

	"untracked.ipxe": `#!ipxe
echo
echo Booty: [[untracked-reason]]; nothing can be served to [[hostname]].
echo Register this host as another OS, or start Booty with a channel for it.
echo
set menu-timeout 30000
:start
menu Booty - OS not tracked - [[hostname]]
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

const (
	secureBootRefusedKey = "secureboot-refused"
	untrackedKey         = "untracked"
)

var secureBootOSLabels = map[string]string{
	"flatcar": "Flatcar",
}

type TemplateVars struct {
	Server      string
	Hostname    string
	MenuDefault string
	// FlatcarDir and CoreOSDir are the /data/ path prefixes of the release
	// the host boots ("flatcar/<version>/", "coreos/<version>"); empty
	// keeps the pre-release-directory top-level names.
	FlatcarDir    string
	CoreOSDir     string
	CoreOSChannel string
	CoreOSArch    string
	CoreOSVersion string
	OSTreeImage   string
	// UntrackedReason, when set, replaces the host's boot script with the
	// refusal menu: its OS has no channel (--coreOSChannel=none).
	UntrackedReason string
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
	// BluefinBootURL is the UEFI HTTP Boot URL of a Bluefin host's netboot
	// UKI, printed by the bluefin menu.
	BluefinBootURL string
	// BluefinReason explains, one echo line each, why the bluefin menu
	// asks for UEFI HTTP Boot instead of chainloading.
	BluefinReason []string
	// BluefinVersion, BluefinChainURL and BluefinChainCmdline select the
	// chainload menu: iPXE boots the netboot UKI at BluefinChainURL with
	// BluefinChainCmdline, which systemd-stub uses instead of the UKI's own
	// .cmdline when Secure Boot is off.
	BluefinVersion      string
	BluefinChainURL     string
	BluefinChainCmdline string
	// BluefinInstalled makes a Bluefin host boot its disk.
	BluefinInstalled bool
	// BluefinBIOS is the legacy BIOS (iPXE pcbios) branch of the Bluefin
	// menus.
	BluefinBIOS BluefinBIOS
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
// asserted its CA is enrolled. Bluefin never boots through iPXE, so its own
// menu (switch to UEFI HTTP Boot) is served either way.
func SecureBootRefused(os string, v TemplateVars) bool {
	return v.SecureBoot && os == "flatcar" && !v.SecureBootTrustedFlatcar
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

func (v TemplateVars) secureBootRefusal() string {
	if v.SecureBootTrustedFlatcar {
		return ""
	}
	return v.flatcarCALines()
}

// BluefinBIOS boots Bluefin Server diskless on legacy BIOS: iPXE loads the
// netboot UKI's .linux as a bzImage with Cmdline and the InitrdURLs in
// order. An empty KernelURL offers only disk/shell/reboot and prints
// Reason.
type BluefinBIOS struct {
	Version    string
	KernelURL  string
	InitrdURLs []string
	Cmdline    string
	Reason     []string
	// InstallPending explains that doInstall is ignored here (installing
	// needs UEFI) for InstallDisk; PreferDisk makes boot-from-disk the
	// default (an installed host that is to be reinstalled).
	InstallPending bool
	InstallDisk    string
	PreferDisk     bool
}

// biosFragment is the :bios branch appended to the Bluefin menus. Its
// labels are prefixed bios- so they never collide with the EFI menu's.
func (v TemplateVars) biosFragment() string {
	b := v.BluefinBIOS
	var sb strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&sb, format+"\n", args...) }
	line(":bios")
	if b.KernelURL == "" {
		reason := b.Reason
		if len(reason) == 0 {
			reason = []string{"no Bluefin Server release is cached yet."}
		}
		line("echo")
		line("echo Booty: %s is a Bluefin Server host, and Booty cannot boot it in BIOS mode:", v.Hostname)
		for _, r := range reason {
			line("echo %s", r)
		}
		line("echo")
		line("set menu-timeout 30000")
		line(":bios-start")
		line("menu Booty - Bluefin Server: cannot boot in BIOS mode - %s", v.Hostname)
	} else {
		if b.InstallPending {
			disk := "its disk"
			if b.InstallDisk != "" {
				disk = b.InstallDisk
			}
			line("echo")
			line("echo Booty: an install of Bluefin Server to %s is pending for %s, but installing", disk, v.Hostname)
			line("echo needs UEFI (systemd-boot); this BIOS boot runs diskless and installs nothing.")
			line("echo")
		}
		line("set menu-timeout 5000")
		line(":bios-start")
		line("menu Booty - Bluefin Server %s (diskless, BIOS) - %s", b.Version, v.Hostname)
		line("item --key n bios-netboot   Boot Bluefin Server %s diskless", b.Version)
	}
	line("item --key d bios-disk      Boot from disk")
	line("item --key s bios-shell     iPXE shell")
	line("item --key r bios-reboot    Reboot")
	def := "bios-disk"
	if b.KernelURL != "" && !b.PreferDisk {
		def = "bios-netboot"
	}
	line("choose --timeout ${menu-timeout} --default %s selected || goto bios-disk", def)
	line("set menu-timeout 0")
	line("goto ${selected}")
	if b.KernelURL != "" {
		line(":bios-netboot")
		line("cpuid --ext 29 || goto bios-not64")
		line("imgfree")
		line("kernel %s %s || goto bios-failed", b.KernelURL, b.Cmdline)
		for _, u := range b.InitrdURLs {
			line("initrd %s || goto bios-failed", u)
		}
		line("boot || goto bios-failed")
		line(":bios-failed")
		line("echo Booty: booting the Bluefin Server kernel failed")
		line("prompt --timeout 5000 Press any key for the menu, or wait to boot from disk || goto bios-disk")
		line("goto bios-start")
		line(":bios-not64")
		line("echo Bluefin Server needs a 64-bit (x86-64) CPU; this one has no long mode. Dropping to shell")
		line("shell")
		line("goto bios-start")
	}
	line(":bios-disk")
	line("exit")
	line(":bios-shell")
	line("shell")
	line("goto bios-start")
	line(":bios-reboot")
	line("reboot")
	return sb.String()
}

func (v TemplateVars) bluefinReason() string {
	var sb strings.Builder
	for _, line := range v.BluefinReason {
		sb.WriteString("echo " + line + "\n")
	}
	return sb.String()
}

// bluefinScript picks the Bluefin menu: boot from disk once installed, the
// chainload menu when Secure Boot is off and a command line was derived,
// the switch-to-UEFI-HTTP-Boot menu otherwise.
func bluefinScript(v TemplateVars) string {
	switch {
	case v.BluefinInstalled:
		return "bluefin-installed"
	case !v.SecureBoot && v.BluefinChainURL != "" && v.BluefinChainCmdline != "":
		return "bluefin-chain"
	}
	return "bluefin"
}

func Render(template string, v TemplateVars) string {
	return strings.NewReplacer(
		"[[server]]", v.Server,
		"[[hostname]]", v.Hostname,
		"[[menu-default]]", v.MenuDefault,
		"[[flatcar-dir]]", v.FlatcarDir,
		"[[coreos-dir]]", v.CoreOSDir,
		"[[coreos-channel]]", v.CoreOSChannel,
		"[[coreos-arch]]", v.CoreOSArch,
		"[[coreos-version]]", v.CoreOSVersion,
		"[[ostree-image]]", v.OSTreeImage,
		"[[bluefin-boot-url]]", v.BluefinBootURL,
		"[[bluefin-reason]]", v.bluefinReason(),
		"[[bluefin-version]]", v.BluefinVersion,
		"[[bluefin-chain-url]]", v.BluefinChainURL,
		"[[bluefin-chain-cmdline]]", v.BluefinChainCmdline,
		"[[bluefin-bios]]", v.biosFragment(),
		"[[secure-boot-shim]]", v.secureBootShim(),
		"[[untracked-reason]]", v.UntrackedReason,
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
// menu when no template exists. Secure Boot clients whose OS cannot pass
// firmware verification get the refusal menu (see SecureBootRefused).
func IPXEScript(os string, v TemplateVars) string {
	if v.UntrackedReason != "" {
		return Render(PXEConfig[untrackedKey+".ipxe"], v)
	}
	if SecureBootRefused(os, v) {
		return Render(strings.NewReplacer(
			"[[os-label]]", secureBootOSLabels[os],
			"[[secure-boot-refusal]]", v.secureBootRefusal(),
		).Replace(PXEConfig[secureBootRefusedKey+".ipxe"]), v)
	}
	if os == "bluefin" {
		os = bluefinScript(v)
	}
	tmpl, ok := PXEConfig[os+".ipxe"]
	if !ok {
		slog.Warn("No iPXE template for OS, serving unknown-host menu", "os", os)
		tmpl = PXEConfig["unknown.ipxe"]
	}
	return Render(tmpl, v)
}
