//go:build unix

package db

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// openLockWait is how long a writable open polls for a lock another process
// holds before it gives up; it is a var so a test can shrink it. A read-only
// open never waits.
var openLockWait = 10 * time.Second

// openLockPoll is the pause between two lock attempts.
const openLockPoll = 50 * time.Millisecond

// openLock is this process's flock on <path>.lock. It is held from the moment
// the first direct handle opens the path until the last one closes. Go opens
// the file with close-on-exec by default, so the lock never leaks into a
// re-exec'd image, which would then find its own file locked.
type openLock struct {
	f *os.File
}

// acquireOpenLock locks <path>.lock, waiting for a writable open and trying
// once for a read-only one. A lock another process holds returns ErrLocked.
func acquireOpenLock(path string, writable bool) (*openLock, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("db: open lock %s: %w", lockPath, err)
	}
	deadline := time.Now().Add(openLockWait)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &openLock{f: f}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("db: open lock %s: %w", lockPath, err)
		}
		if !writable || !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, ErrLocked
		}
		time.Sleep(openLockPoll)
	}
}

// release drops the lock and closes the descriptor that carried it.
func (l *openLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
