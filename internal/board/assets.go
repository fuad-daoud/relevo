package board

import (
	"embed"
	"io/fs"
)

// embedded is the built page tree with its integrity manifest, which
// scripts/board-assets.sh regenerates; CI only reads it.
//
//go:embed assets
var embedded embed.FS

// Assets returns the embedded page tree, rooted at the assets directory.
func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}
