//go:build unix

package delivery

import (
	"errors"
	"syscall"
)

// defaultClaimAlive reports whether pid is live: signal 0 succeeds or fails
// with EPERM, which still means alive.
func defaultClaimAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
