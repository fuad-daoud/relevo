//go:build unix

package delivery

import (
	"errors"
	"syscall"
)

// defaultClaimAlive reports whether pid names a live process: signal 0
// succeeds, or fails with EPERM (a process we cannot signal is still
// alive).
func defaultClaimAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
