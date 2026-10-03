package board

import (
	"embed"
	"io/fs"
)

// shell is the embedded shell page: the no-token page the browser loads, and its
// script. It lives outside assets/ on purpose -- assets/ is the built
// Excalidraw page with an integrity manifest CI checks, and the shell is
// hand-written with no build step.
//
//go:embed shell
var shellTree embed.FS

// Shell returns the embedded shell page tree, rooted at the shell directory.
func Shell() fs.FS {
	sub, err := fs.Sub(shellTree, "shell")
	if err != nil {
		panic(err)
	}
	return sub
}

// readShellFile reads one file out of the embedded shell. It is a seam the
// offline check runs against: the shell must have no external reference of its
// own, which is a property of the bytes we ship.
func readShellFile(name string) ([]byte, error) {
	return fs.ReadFile(Shell(), name)
}
