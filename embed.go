package booty

import "embed"

//go:embed undionly.kpxe
var UndionlyKPXE []byte

//go:embed all:web/dist
var WebDist embed.FS
