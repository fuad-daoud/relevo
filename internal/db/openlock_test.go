package db

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The lock-helper env names, shared with main_test.go's dispatch. They are
// repeated here because an internal test cannot read the external test
// package's constants.
const (
	lockHelperEnv      = "RELEVO_LOCK_HELPER"
	lockHelperModeEnv  = "RELEVO_LOCK_HELPER_MODE"
	lockHelperPathEnv  = "RELEVO_LOCK_HELPER_PATH"
	lockHelperReadyEnv = "RELEVO_LOCK_HELPER_READY"
)

// startLockHelper runs the test binary as a second process that holds path open
// -- the engine's own pool in mode "raw", the relevo open lock in mode "open" --
// and waits until the ready file says it is held.
func startLockHelper(t *testing.T, mode, path string) {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(self)
	cmd.Env = envWithout(os.Environ(), "RELEVO_DBTEST_OWNER")
	cmd.Env = append(cmd.Env,
		lockHelperEnv+"=1",
		lockHelperModeEnv+"="+mode,
		lockHelperPathEnv+"="+path,
		lockHelperReadyEnv+"="+ready,
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the lock helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the lock helper never became ready")
}

// envWithout copies env with every entry named key removed, so a value this
// process sets cannot be shadowed by an inherited duplicate the child reads
// first.
func envWithout(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			continue
		}
		out = append(out, e)
	}
	return out
}

// shrinkOpenLockWait makes a test's wait for the open lock short and restores
// the production value afterwards.
func shrinkOpenLockWait(t *testing.T, d time.Duration) {
	t.Helper()
	restore := openLockWait
	openLockWait = d
	t.Cleanup(func() { openLockWait = restore })
}

// TestOpenLockedByAnotherProcessIsErrLocked pins the relevo open lock across
// processes: while another process holds it, a writable direct open fails with
// ErrLocked, under either engine.
func TestOpenLockedByAnotherProcessIsErrLocked(t *testing.T) {
	shrinkOpenLockWait(t, 200*time.Millisecond)

	path := filepath.Join(t.TempDir(), "relevo.db")
	startLockHelper(t, "flock", path)

	if _, err := openDirect(path, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("openDirect while another process holds the lock = %v, want ErrLocked", err)
	}
}

// TestWritableOpenWaitsForTheLockThenFails pins that a writable open polls for
// the lock up to openLockWait before it gives up.
func TestWritableOpenWaitsForTheLockThenFails(t *testing.T) {
	const wait = 300 * time.Millisecond
	shrinkOpenLockWait(t, wait)

	path := filepath.Join(t.TempDir(), "relevo.db")
	startLockHelper(t, "flock", path)

	start := time.Now()
	_, err := openDirect(path, Options{})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("openDirect while another process holds the lock = %v, want ErrLocked", err)
	}
	if elapsed < wait {
		t.Errorf("the writable open returned in %v, want it to wait about %v", elapsed, wait)
	}
}

// TestReadOnlyOpenTriesOnceForTheLock pins the read-only rule: a read-only open
// must not wait for a lock another process holds, because the caller needs an
// answer now to fall back to the owner. The plan's own mutation check names the
// peek test for this; that test injects ErrLocked through the config opener, so
// this pins the underlying one-shot behaviour directly.
func TestReadOnlyOpenTriesOnceForTheLock(t *testing.T) {
	shrinkOpenLockWait(t, 2*time.Second)

	path := filepath.Join(t.TempDir(), "relevo.db")
	startLockHelper(t, "flock", path)

	start := time.Now()
	_, err := OpenReadOnly(path)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("OpenReadOnly while another process holds the lock = %v, want ErrLocked", err)
	}
	if elapsed >= time.Second {
		t.Errorf("the read-only open waited %v for the lock, want it to try once", elapsed)
	}
}

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
