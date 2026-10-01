package db

import "sync"

// pathHandle is this process's state for one database path: how many direct
// handles are open on it, and the open lock they share. Every handle in this
// process on a path shares the one lock, because flock treats a second open of
// the same file as an independent lock and would deadlock the process against
// itself.
type pathHandle struct {
	count int
	lock  *openLock
}

// directHandles is the per-path state for every open direct handle. Vacuum
// swaps a handle's pool out from under it, so it needs to know that no other
// handle in this process is using the file; another process's handle is
// invisible here and is refused by the open lock instead.
var directHandles = struct {
	sync.Mutex
	paths map[string]*pathHandle
}{paths: map[string]*pathHandle{}}

// acquireHandle takes the path's open lock and records one more open direct
// handle. A writable open polls up to openLockWait for the lock; a read-only
// one tries once. A lock another process holds returns ErrLocked. A second
// handle in this process on the same path shares the existing lock and count.
func acquireHandle(path string, writable bool) error {
	directHandles.Lock()
	defer directHandles.Unlock()
	if h := directHandles.paths[path]; h != nil {
		h.count++
		return nil
	}
	lock, err := acquireOpenLock(path, writable)
	if err != nil {
		return err
	}
	directHandles.paths[path] = &pathHandle{count: 1, lock: lock}
	return nil
}

// releaseHandle records one fewer open direct handle on path, releasing the
// path's open lock when the last one closes.
func releaseHandle(path string) {
	directHandles.Lock()
	defer directHandles.Unlock()
	h := directHandles.paths[path]
	if h == nil {
		return
	}
	if h.count > 1 {
		h.count--
		return
	}
	delete(directHandles.paths, path)
	h.lock.release()
}

// handleCount is how many direct handles this process holds on path.
func handleCount(path string) int {
	directHandles.Lock()
	defer directHandles.Unlock()
	if h := directHandles.paths[path]; h != nil {
		return h.count
	}
	return 0
}
