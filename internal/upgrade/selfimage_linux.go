//go:build linux

package upgrade

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// procSelfExe is the kernel's link to the image this process exec'd; it names
// the inode, not the path.
const procSelfExe = "/proc/self/exe"

// SelfImageIdentity identifies the image this process is running, which is a
// different question from the one ExeIdentity answers: a daemon's baseline has
// to be its own image, else a replacement landing after exec becomes the
// baseline and the watcher sees no change to fire on.
func SelfImageIdentity() (store.FileID, error) {
	return selfImageIdentity(procSelfExe)
}

func selfImageIdentity(link string) (store.FileID, error) {
	return ExeIdentity(link)
}
