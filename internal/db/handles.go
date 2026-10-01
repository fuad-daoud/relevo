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
	// created is closed when the first opener has finished its direct open, so a
	// later opener waits for it before it opens a pool of its own. The engine
	// keeps one database per file in this process, and a fresh file's first page
	// must be created once: two pools writing a fresh file at once race that
	// creation and leave a short or corrupt WAL. A nil channel means the first
	// open is done, so a later opener proceeds at once.
	created chan struct{}
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
// first reports whether this call created the path's entry, which makes it the
// only opener that performs the engine's first-time creation; it must call
// signalCreated when its direct open is done. Every other opener must call
// awaitCreated before it opens its own pool.
//
// The wait happens with directHandles unlocked: every other direct open or
// close in the process must not queue behind one path's long open-lock wait.
func acquireHandle(path string, writable bool) (first bool, err error) {
	key := canonicalPath(path)
	for {
		directHandles.Lock()
		h := directHandles.paths[key]
		switch {
		case h == nil:
			h = &pathHandle{ready: make(chan struct{}), created: make(chan struct{})}
			directHandles.paths[key] = h
			directHandles.Unlock()
			if err := openFirstHandle(key, writable, h); err != nil {
				return false, err
			}
			return true, nil
		case h.lock == nil:
			// Another goroutine is taking this path's lock. Wait for its
			// outcome rather than contending for a flock on the same file.
			ready := h.ready
			directHandles.Unlock()
			<-ready
		default:
			h.count++
			directHandles.Unlock()
			return false, nil
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

// signalCreated records that the first opener has finished its direct open, so
// a later opener on the same path may open its pool. Only the first opener
// calls it, so the channel is closed at most once. It runs before the entry is
// released, so a waiting opener still finds the entry and wakes.
func signalCreated(path string) {
	directHandles.Lock()
	h := directHandles.paths[canonicalPath(path)]
	var created chan struct{}
	if h != nil {
		created = h.created
		h.created = nil
	}
	directHandles.Unlock()
	if created != nil {
		close(created)
	}
}

// awaitCreated blocks a later opener until the first opener's direct open is
// done, so two pools never write a fresh file at once. It returns at once when
// the entry carries no open in progress, which is also the case for a path that
// already had a handle before this open arrived.
func awaitCreated(path string) {
	directHandles.Lock()
	var created chan struct{}
	if h := directHandles.paths[canonicalPath(path)]; h != nil {
		created = h.created
	}
	directHandles.Unlock()
	if created != nil {
		<-created
	}
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
