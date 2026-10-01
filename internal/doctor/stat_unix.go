//go:build unix

package doctor

import (
	"os"
	"syscall"
)

// statOwner returns the numeric uid and gid that own fi, and ok reports whether
// the platform's FileInfo carries them. The owner-root check needs the numbers
// to name a binding root owned by the wrong user or group.
func statOwner(fi os.FileInfo) (uid, gid uint32, ok bool) {
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return sys.Uid, sys.Gid, true
}
