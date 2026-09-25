package tftp

import (
	"log/slog"
	"strings"

	"github.com/jeefy/booty/pkg/hardware"
)

const DefaultOS = "flatcar"

// PXEConfig holds the boot script templates. Keys ending in ".ipxe" are iPXE
// scripts (served over HTTP from /booty.ipxe), bare keys are legacy pxelinux
// configs (served over TFTP as pxelinux.cfg/*). Placeholders use [[name]].
var PXEConfig = map[string]string{
	"flatcar": `default flatcar
prompt 1
timeout 5

display boot.msg

label flatcar
	menu default
	kernel flatcar_production_pxe.vmlinuz
	initrd flatcar_production_pxe_image.cpio.gz
	append flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json
`,

	"unknown": `default local
prompt 1
timeout 300

label local
	menu default
	localboot 0
`,

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

	"ublue.ipxe": `#!ipxe
set BASEURL http://[[server]]/data/
set CONFIGURL http://[[server]]/ignition.json?mac=${mac}
set OSTREE_IMAGE [[ostree-image]]
set STREAM [[coreos-channel]]
set VERSION [[coreos-version]]
set menu-default [[menu-default]]

echo "Hello from Booty!"
chain http://[[server]]/data/ublue.ipxe
boot
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
	MenuDefault   string
	CoreOSChannel string
	CoreOSArch    string
	CoreOSVersion string
	OSTreeImage   string
}

func Render(template string, v TemplateVars) string {
	return strings.NewReplacer(
		"[[server]]", v.Server,
		"[[menu-default]]", v.MenuDefault,
		"[[coreos-channel]]", v.CoreOSChannel,
		"[[coreos-arch]]", v.CoreOSArch,
		"[[coreos-version]]", v.CoreOSVersion,
		"[[ostree-image]]", v.OSTreeImage,
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
// menu when no template exists.
func IPXEScript(os string, v TemplateVars) string {
	tmpl, ok := PXEConfig[os+".ipxe"]
	if !ok {
		slog.Warn("No iPXE template for OS, serving unknown-host menu", "os", os)
		tmpl = PXEConfig["unknown.ipxe"]
	}
	return Render(tmpl, v)
}

// LegacyPXEConfig renders the pxelinux config for os. Only flatcar has a
// legacy config; other registered OSes fall back to it with a warning.
func LegacyPXEConfig(os string, v TemplateVars) string {
	tmpl, ok := PXEConfig[os]
	if !ok {
		slog.Warn("No legacy PXE config for OS, falling back to flatcar", "os", os)
		tmpl = PXEConfig[DefaultOS]
	}
	return Render(tmpl, v)
}

const pxelinuxMACPrefix = "pxelinux.cfg/01-"

// ParsePXELinuxMAC extracts the client MAC from a pxelinux.cfg/01-<mac> request
// (dash-separated hex, as sent by pxelinux before falling back to "default").
func ParsePXELinuxMAC(filename string) (string, bool) {
	if !strings.HasPrefix(filename, pxelinuxMACPrefix) {
		return "", false
	}
	raw := strings.TrimPrefix(filename, pxelinuxMACPrefix)
	mac, err := hardware.NormalizeMAC(strings.ReplaceAll(raw, "-", ":"))
	if err != nil {
		return "", false
	}
	return mac, true
}
