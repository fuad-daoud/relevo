//go:build unix

package store

import (
	"errors"
	"os"
	"syscall"
)

// tryLockExclusive takes a non-blocking exclusive flock on f; a lock held by
// someone else is (false, nil), not an error. The kernel drops the lock when a
// holder dies, so there is never a stale lock file to reap by hand.
func tryLockExclusive(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}
