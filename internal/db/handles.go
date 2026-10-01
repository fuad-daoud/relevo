package db

import (
	"path/filepath"
	"sync"
)

// pathHandle is this process's state for one database path: how many direct
// handles are open on it, and the open lock they share. Every handle in this
// process on a path shares the one lock, because flock treats a second open of
// the same file as an independent lock and would deadlock the process against
// itself.
type pathHandle struct {
	count int
	lock  *openLock
	// ready is closed when the first opener has finished taking the lock, so a
	// second opener waits for that outcome instead of taking a second flock on
	// the same file. A nil lock with a ready channel means the path is being
	// opened; the map entry is removed and ready is closed on failure.
	ready chan struct{}
}

// directHandles is the per-path state for every open direct handle. Vacuum
// swaps a handle's pool out from under it, so it needs to know that no other
// handle in this process is using the file; another process's handle is
// invisible here and is refused by the open lock instead.
var directHandles = struct {
	sync.Mutex
	paths map[string]*pathHandle
}{paths: map[string]*pathHandle{}}

// canonicalPath is the identity directHandles keys on: the absolute path with
// every symlink resolved. Two spellings of one file -- a relative path, an
// absolute one, or a path through a symlink -- name one database and must share
// one lock, or the second open waits out flock on a lock its own process holds.
// The database exists by the time the lock is taken, so it can be resolved; a
// path that cannot be falls back to its cleaned absolute form, which still
// merges the relative and absolute spellings.
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

// acquireHandle takes the path's open lock and records one more open direct
// handle. A writable open polls up to openLockWait for the lock; a read-only
// one tries once. A lock another process holds returns ErrLocked. A second
// handle in this process on the same path shares the existing lock and count.
//
// The wait happens with directHandles unlocked: every other direct open or
// close in the process must not queue behind one path's ten-second wait.
func acquireHandle(path string, writable bool) error {
	key := canonicalPath(path)
	for {
		directHandles.Lock()
		h := directHandles.paths[key]
		switch {
		case h == nil:
			h = &pathHandle{ready: make(chan struct{})}
			directHandles.paths[key] = h
			directHandles.Unlock()
			return openFirstHandle(key, writable, h)
		case h.lock == nil:
			// Another goroutine is taking this path's lock. Wait for its
			// outcome rather than contending for a flock on the same file.
			ready := h.ready
			directHandles.Unlock()
			<-ready
		default:
			h.count++
			directHandles.Unlock()
			return nil
		}
	}
}

// openFirstHandle takes the path's lock for the first handle of a fresh entry
// and publishes it, or removes the entry on failure so the next caller tries
// again. It runs with directHandles unlocked, which is the whole point of the
// reservation: the flock may wait.
func openFirstHandle(key string, writable bool, h *pathHandle) error {
	lock, err := acquireOpenLock(key, writable)
	directHandles.Lock()
	defer directHandles.Unlock()
	if err != nil {
		delete(directHandles.paths, key)
		close(h.ready)
		return err
	}
	h.lock, h.count = lock, 1
	close(h.ready)
	return nil
}

// releaseHandle records one fewer open direct handle on path, releasing the
// path's open lock when the last one closes.
func releaseHandle(path string) {
	key := canonicalPath(path)
	directHandles.Lock()
	defer directHandles.Unlock()
	h := directHandles.paths[key]
	if h == nil {
		return
	}
	if h.count > 1 {
		h.count--
		return
	}
	delete(directHandles.paths, key)
	h.lock.release()
}

// handleCount is how many direct handles this process holds on path.
func handleCount(path string) int {
	directHandles.Lock()
	defer directHandles.Unlock()
	if h := directHandles.paths[canonicalPath(path)]; h != nil {
		return h.count
	}
	return 0
}
