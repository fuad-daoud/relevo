package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireDaemonLockExcludesSecondHolder(t *testing.T) {
	s := New(t.TempDir())

	first, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = first.Close() }()

	if _, err := s.AcquireDaemonLock(); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("second acquire: got %v, want ErrDaemonRunning", err)
	}
}

func TestDaemonRunningReflectsTheLock(t *testing.T) {
	s := New(t.TempDir())

	running, err := s.DaemonRunning()
	if err != nil {
		t.Fatalf("check before acquire: %v", err)
	}
	if running {
		t.Fatal("reported a daemon running before one acquired the lock")
	}

	held, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	running, err = s.DaemonRunning()
	if err != nil {
		t.Fatalf("check while held: %v", err)
	}
	if !running {
		t.Fatal("reported no daemon while the lock was held")
	}

	if err := held.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	running, err = s.DaemonRunning()
	if err != nil {
		t.Fatalf("check after release: %v", err)
	}
	if running {
		t.Fatal("reported a daemon running after the lock was released")
	}
}

// TestDaemonLockDoesNotBlockStateOperations guards the regression that
// AcquireDaemonLock must not take the mutex WithLock uses: the daemon holds
// its lock for its whole life, so it would deadlock every state operation.
func TestDaemonLockDoesNotBlockStateOperations(t *testing.T) {
	s := New(t.TempDir())

	held, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = held.Close() }()

	if err := s.WithLock(func(tx *Tx) error { return nil }); err != nil {
		t.Fatalf("WithLock while the daemon lock was held: %v", err)
	}
}

func TestDaemonLockDisplacedOnceTheRootIsReCreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s := New(root)
	held, err := s.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = held.Close() }()
	if held.Displaced() {
		t.Fatal("a freshly held lock reported displaced")
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	if !held.Displaced() {
		t.Fatal("a lock whose file was deleted reported in place")
	}

	// A probe re-creates the root and a new lock file nobody holds.
	running, err := s.DaemonRunning()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if running {
		t.Fatal("the re-created lock file reported held")
	}
	if !held.Displaced() {
		t.Fatal("a lock on the unlinked file reported in place after the root came back")
	}
}

func TestNilDaemonLockIsNeverDisplaced(t *testing.T) {
	var l *DaemonLock
	if l.Displaced() {
		t.Fatal("a nil lock reported displaced")
	}
}
