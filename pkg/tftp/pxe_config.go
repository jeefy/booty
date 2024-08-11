package tftp

var PXEConfig map[string]string

func init() {
	PXEConfig = make(map[string]string)
	PXEConfig["flatcar"] = `default flatcar
	prompt 1
	timeout 5
	
	display boot.msg
	
	label flatcar
		menu default
		kernel flatcar_production_pxe.vmlinuz
		initrd flatcar_production_pxe_image.cpio.gz
		append flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json`

	PXEConfig["flatcar.ipxe"] = `#!ipxe
	echo Hello from Booty!
	kernel http://[[server]]/data/flatcar_production_pxe.vmlinuz flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json
	initrd http://[[server]]/data/flatcar_production_pxe_image.cpio.gz
	boot`

	PXEConfig["flatcar_booty.ipxe"] = `#!ipxe
	echo "Hello from Booty!"
	chain http://[[server]]/data/flatcar_booty.ipxe
	boot`

	PXEConfig["coreos.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL http://[[server]]/data/
	set CONFIGURL http://[[server]]/ignition.json
	set OSTREE_IMAGE [[ostree-image]]
	set STREAM [[coreos-channel]]
	set VERSION [[coreos-version]]
	set ARCH [[coreos-arch]]

	kernel ${BASEURL}/fedora-coreos-${VERSION}-live-kernel-${ARCH} enforcing=0 initrd=main coreos.live.rootfs_url=${BASEURL}/fedora-coreos-${VERSION}-live-rootfs.${ARCH}.img ignition.firstboot ignition.platform.id=metal ignition.firstboot=1 ignition.config.url=${CONFIGURL}
	initrd --name main ${BASEURL}/fedora-coreos-${VERSION}-live-initramfs.${ARCH}.img
	boot`

	PXEConfig["ublue.ipxe"] = `#!ipxe
	set BASEURL http://[[server]]/data/
	set CONFIGURL http://[[server]]/ignition.json
	set OSTREE_IMAGE [[ostree-image]]
	set STREAM [[coreos-channel]]
	set VERSION [[coreos-version]]
	set menu-default [[menu-default]]

	echo "Hello from Booty!"
	chain http://[[server]]/data/ublue.ipxe
	boot`
}
