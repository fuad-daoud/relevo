//go:build unix

package syncpipe

import (
	"errors"
	"syscall"
)

// workerPidAlive reports whether pid names a live process, and whether the
// probe applies here: signal 0 succeeds or fails with EPERM, which still
// means alive.
func workerPidAlive(pid int) (bool, bool) {
	if pid <= 0 {
		return false, true
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM), true
}
