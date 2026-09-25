package booty

import "embed"

// BootFiles holds the iPXE binaries built by `make ipxe` (see
// THIRD_PARTY_NOTICES.md): boot/undionly.kpxe (BIOS), boot/ipxe.efi and
// boot/snponly.efi (x86-64 UEFI). They are served over TFTP and /boot/.
//
//go:embed boot/undionly.kpxe boot/ipxe.efi boot/snponly.efi
var BootFiles embed.FS

//go:embed all:web/dist
var WebDist embed.FS
