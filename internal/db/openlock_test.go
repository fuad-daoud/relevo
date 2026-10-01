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

// TestAWaitingOpenDoesNotBlockAnotherPath pins that waiting for the open lock
// never holds the process-wide handle map: a writable open that is polling for a
// lock another process holds must not queue every other path's open behind it.
// The second path is created and migrated, then closed, before the timed window
// starts, so the open inside it is a plain lock acquisition; the assertion is
// ordering, not a clock: the second path's open returns while the first is
// still polling.
func TestAWaitingOpenDoesNotBlockAnotherPath(t *testing.T) {
	shrinkOpenLockWait(t, 5*time.Second)

	held := filepath.Join(t.TempDir(), "held.db")
	startLockHelper(t, "flock", held)

	other := filepath.Join(t.TempDir(), "other.db")
	seed := directOpen(t, other, Options{})
	if err := seed.Close(); err != nil {
		t.Fatalf("close the second path's seed handle: %v", err)
	}

	waited := make(chan error, 1)
	go func() {
		_, err := openDirect(held, Options{})
		waited <- err
	}()
	// Let the waiting open reach the lock before the second path is opened.
	time.Sleep(100 * time.Millisecond)

	d, err := openDirect(other, Options{})
	if err != nil {
		t.Fatalf("openDirect on a second path while the first waits: %v", err)
	}
	_ = d.Close()

	select {
	case werr := <-waited:
		t.Fatalf("the waiting open ended before the second path's open returned: %v", werr)
	default:
	}

	if err := <-waited; !errors.Is(err, ErrLocked) {
		t.Errorf("the waiting open = %v, want ErrLocked", err)
	}
}

// TestOpenLockWaitOutlastsTheReadOnlyHoldBudget pins the relation the wait and
// the deadline share: a read-only caller may hold the database for its whole
// budget, so the writable open's lock wait must outlast that budget or a slow
// reader would make a daemon start fail its open.
func TestOpenLockWaitOutlastsTheReadOnlyHoldBudget(t *testing.T) {
	if openLockWait <= ReadOnlyHoldBudget {
		t.Errorf("openLockWait = %v, want it to outlast ReadOnlyHoldBudget = %v", openLockWait, ReadOnlyHoldBudget)
	}
}

// TestTwoSpellingsOfOnePathShareTheLock pins the handle map's key: a path
// through a symlink and a path that reaches the same file through a `..` both
// name one database, so they must share the open lock instead of waiting out
// flock against this process's own handle.
func TestTwoSpellingsOfOnePathShareTheLock(t *testing.T) {
	shrinkOpenLockWait(t, 3*time.Second)

	dir := t.TempDir()
	path := filepath.Join(dir, "relevo.db")
	first, err := openDirect(path, Options{})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = first.Close() })

	link := filepath.Join(dir, "linked.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatalf("symlink %s: %v", link, err)
	}
	// An absolute path with a `..` in it: a second spelling the handle map must
	// fold onto the first. filepath.Join would clean it away, so it is built by
	// hand.
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	indirect := dir + "/sub/../relevo.db"

	start := time.Now()
	second, err := openDirect(link, Options{})
	if err != nil {
		t.Fatalf("open through a symlink while the file is open: %v", err)
	}
	third, err := openDirect(indirect, Options{})
	if err != nil {
		t.Fatalf("open through %s while the file is open: %v", indirect, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the two spellings waited %v, want them to share the lock at once", elapsed)
	}
	if got := handleCount(link); got != 3 {
		t.Errorf("handleCount of the symlink = %d, want the three handles on one file", got)
	}
	_ = second
	_ = third
}
