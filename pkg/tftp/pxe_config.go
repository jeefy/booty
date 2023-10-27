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
			kernel http://[[server]]/flatcar_production_pxe.vmlinuz initrd=flatcar_production_pxe_image.cpio.gz
			initrd http://[[server]]/flatcar_production_pxe_image.cpio.gz
			imgargs flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json`

	PXEConfig["ucore"] = `default ucore
	prompt 1
	timeout 5
	
	display boot.msg
	
	label ucore
		menu default
		kernel vmlinuz
		initrd initrd.img
		append imageurl=ghcr.io/ublue-os/ucore:stable ignition.config.url=http://[[server]]/ignition.json`
	PXEConfig["ipxe"] = `#!ipxe
	echo Hello from Booty!
	kernel http://[[server]]/flatcar_production_pxe.vmlinuz initrd=flatcar_production_pxe_image.cpio.gz
	initrd http://[[server]]/flatcar_production_pxe_image.cpio.gz
	imgargs flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json
	`
}
