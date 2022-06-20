package tftp

const (
	PXEConfigContents = `default flatcar
prompt 1
timeout 5

display boot.msg

label flatcar
	menu default
	kernel flatcar_production_pxe.vmlinuz
	initrd flatcar_production_pxe_image.cpio.gz
	append flatcar.first_boot=1 ignition.config.url=http://%s/ignition.json
`
)
