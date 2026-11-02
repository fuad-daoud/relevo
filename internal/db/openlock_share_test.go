package db

import (
	"path/filepath"
	"testing"
)

// TestTwoHandlesInOneProcessShareTheLock pins that a second handle in one
// process shares the path's lock instead of deadlocking against itself.
func TestTwoHandlesInOneProcessShareTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d1 := directOpen(t, path, Options{})
	d2 := directOpen(t, path, Options{})
	if got := handleCount(path); got != 2 {
		t.Errorf("handleCount with two handles = %d, want 2", got)
	}
	// One close drops the count but keeps the lock: a third in-process handle
	// still opens, because it shares the lock the second handle holds.
	if err := d1.Close(); err != nil {
		t.Fatalf("close d1: %v", err)
	}
	if _, err := openDirect(path, Options{}); err != nil {
		t.Fatalf("a third in-process handle did not share the lock: %v", err)
	}
	_ = d2
}
