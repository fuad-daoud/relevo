package board

import (
	"embed"
	"io/fs"
)

// embedded is the built page tree. scripts/board-assets.sh regenerates it and
// its integrity manifest; CI only reads it.
//
//go:embed assets
var embedded embed.FS

// Assets returns the embedded page tree, rooted at the assets directory: the
// page, the bundle, the stylesheet and the self-hosted fonts.
func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}
