//go:build linux

package upgrade

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// procSelfExe is the kernel's link to the image this process exec'd. It names
// the inode, not the path, so a replacement renamed over the path leaves it
// pointing at the running image while the path already reads as the new one.
const procSelfExe = "/proc/self/exe"

// SelfImageIdentity identifies the image this process is running, which is a
// different question from the one ExeIdentity answers. A daemon's baseline has
// to be its own image: read from the path, a replacement that lands after exec
// becomes the baseline, and the watcher then sees no change to fire on.
func SelfImageIdentity() (store.FileID, error) {
	return selfImageIdentity(procSelfExe)
}

// selfImageIdentity is SelfImageIdentity with the link supplied, so the
// failure path is reachable from a test without an unreadable proc.
func selfImageIdentity(link string) (store.FileID, error) {
	return ExeIdentity(link)
}
