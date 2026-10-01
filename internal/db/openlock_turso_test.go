//go:build !modernc

package db

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestTursoLockHeldByAnotherProcessIsErrLocked pins the engine-level lock: a
// second process opening a file whose Turso pool is held elsewhere is refused,
// and the refusal reaches the caller as ErrLocked so it can fall back to the
// owner.
func TestTursoLockHeldByAnotherProcessIsErrLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	startLockHelper(t, "raw", path)

	if _, err := openDirect(path, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("openDirect while a Turso pool holds the file = %v, want ErrLocked", err)
	}
}
