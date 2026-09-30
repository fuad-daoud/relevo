package db

import "sync"

// directHandles counts the open direct handles per database path. Vacuum swaps
// a handle's pool out from under it, so it needs to know that no other handle
// in this process is using the file; another process's handle is invisible here
// and is Round 2's problem.
var directHandles = struct {
	sync.Mutex
	counts map[string]int
}{counts: map[string]int{}}

// registerHandle records one more open direct handle on path.
func registerHandle(path string) {
	directHandles.Lock()
	directHandles.counts[path]++
	directHandles.Unlock()
}

// releaseHandle records one fewer open direct handle on path, forgetting the
// path when the last one closes.
func releaseHandle(path string) {
	directHandles.Lock()
	if n := directHandles.counts[path]; n > 1 {
		directHandles.counts[path] = n - 1
	} else {
		delete(directHandles.counts, path)
	}
	directHandles.Unlock()
}

// handleCount is how many direct handles this process holds on path.
func handleCount(path string) int {
	directHandles.Lock()
	defer directHandles.Unlock()
	return directHandles.counts[path]
}
