//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// flockIsFree reports whether relevo's open lock on path can be taken
// non-blockingly right now, which is what proves no handle still holds it. It
// opens its own descriptor, so it sees a lock this process holds through
// another one.
func flockIsFree(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}
