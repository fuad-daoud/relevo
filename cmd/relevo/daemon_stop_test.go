//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// swapDaemonStopSeams saves the stop seams and restores them when the test
// ends, so no test ever signals a process or opens a real daemon lock. The
// record is read straight from the test's database: no test has a daemon
// socket to read it through.
func swapDaemonStopSeams(t *testing.T) {
	t.Helper()
	signal, running, sleep, info := daemonStopSignal, daemonStopRunning, daemonStopSleep, daemonStopInfo
	t.Cleanup(func() {
		daemonStopSignal, daemonStopRunning, daemonStopSleep, daemonStopInfo = signal, running, sleep, info
	})
	daemonStopInfo = func(root string) (store.DaemonInfo, bool, error) { return store.New(root).ReadDaemonInfo() }
}

// stopEnv points the state, config and home roots at fresh temporary
// directories so a stop test never reads or writes the real machine.
func stopEnv(t *testing.T) string {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	return root
}

// TestDaemonStopSignalsTheRecordedPIDAndWaits pins the happy path: the recorded
// pid is signalled once, the lock is polled more than once, and a stop is
// reported with the pid.
func TestDaemonStopSignalsTheRecordedPIDAndWaits(t *testing.T) {
	root := stopEnv(t)
	if err := store.New(root).WriteDaemonInfo(store.DaemonInfo{PID: 4242}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}

	swapDaemonStopSeams(t)
	signalled := make(chan int, 1)
	daemonStopSignal = func(pid int) error { signalled <- pid; return nil }
	var probes int32
	daemonStopRunning = func(string) (bool, error) {
		// The first probe is the pre-signal check; the next is the wait loop's,
		// which then sees the lock gone.
		return atomic.AddInt32(&probes, 1) < 3, nil
	}
	daemonStopSleep = func(time.Duration) {}

	stdout, _, err := captureOutput(t, daemonStop)
	if err != nil {
		t.Fatalf("daemonStop: %v", err)
	}
	select {
	case pid := <-signalled:
		if pid != 4242 {
			t.Errorf("signalled pid = %d, want 4242", pid)
		}
	default:
		t.Error("the signal seam was never called")
	}
	if got := atomic.LoadInt32(&probes); got <= 1 {
		t.Errorf("the lock was probed %d times, want more than once", got)
	}
	if !strings.Contains(string(stdout), "4242") {
		t.Errorf("stdout = %q, want it to name the pid", stdout)
	}
}

// TestDaemonStopReportsNotRunning pins the outcomes that signal nothing: a free
// lock prints one line without reading any record, and a held lock with no
// record is refused rather than reported as no daemon.
func TestDaemonStopReportsNotRunning(t *testing.T) {
	t.Run("no lock", func(t *testing.T) {
		stopEnv(t)
		swapDaemonStopSeams(t)
		var signals, reads int32
		daemonStopSignal = func(int) error { atomic.AddInt32(&signals, 1); return nil }
		daemonStopRunning = func(string) (bool, error) { return false, nil }
		daemonStopInfo = func(string) (store.DaemonInfo, bool, error) {
			atomic.AddInt32(&reads, 1)
			return store.DaemonInfo{PID: 4242}, true, nil
		}

		stdout, _, err := captureOutput(t, daemonStop)
		if err != nil {
			t.Fatalf("daemonStop: %v", err)
		}
		if !strings.Contains(string(stdout), "no daemon is running") {
			t.Errorf("stdout = %q, want the not-running line", stdout)
		}
		if got := atomic.LoadInt32(&signals); got != 0 {
			t.Errorf("the signal seam was called %d times, want 0", got)
		}
		if got := atomic.LoadInt32(&reads); got != 0 {
			t.Errorf("the record was read %d times with the lock free, want 0", got)
		}
	})

	t.Run("lock held, no record", func(t *testing.T) {
		stopEnv(t)
		swapDaemonStopSeams(t)
		var signals int32
		daemonStopSignal = func(int) error { atomic.AddInt32(&signals, 1); return nil }
		daemonStopRunning = func(string) (bool, error) { return true, nil }

		_, _, err := captureOutput(t, daemonStop)
		if err == nil || !strings.Contains(err.Error(), "recorded no pid") {
			t.Fatalf("daemonStop = %v, want the no-pid refusal", err)
		}
		if got := atomic.LoadInt32(&signals); got != 0 {
			t.Errorf("the signal seam was called %d times, want 0", got)
		}
	})

}

// TestDaemonStopGivesUpAfterTheBoundedWait pins the timeout: a lock that never
// clears is an error naming the pid and the bound.
func TestDaemonStopGivesUpAfterTheBoundedWait(t *testing.T) {
	root := stopEnv(t)
	if err := store.New(root).WriteDaemonInfo(store.DaemonInfo{PID: 4242}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}

	swapDaemonStopSeams(t)
	daemonStopSignal = func(int) error { return nil }
	daemonStopRunning = func(string) (bool, error) { return true, nil }
	daemonStopSleep = func(time.Duration) {}

	_, _, err := captureOutput(t, daemonStop)
	if err == nil {
		t.Fatal("daemonStop = nil, want the bounded-wait error")
	}
	if !strings.Contains(err.Error(), "4242") || !strings.Contains(err.Error(), daemonStopBound.String()) {
		t.Errorf("daemonStop error = %v, want it to name the pid and %s", err, daemonStopBound)
	}
}

// TestDaemonStopRefusesWhenTheServiceOwnsIt pins the service-managed refusal:
// with the systemd unit or the launchd plist installed, the error names the
// service command and the signal seam is never called.
func TestDaemonStopRefusesWhenTheServiceOwnsIt(t *testing.T) {
	swapDaemonStopSeams(t)
	var signals int32
	daemonStopSignal = func(int) error { atomic.AddInt32(&signals, 1); return nil }

	t.Run("systemd", func(t *testing.T) {
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		t.Setenv("HOME", t.TempDir())
		unitDir := filepath.Join(configHome, "systemd", "user")
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", unitDir, err)
		}
		if err := os.WriteFile(filepath.Join(unitDir, "relevo.service"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write unit: %v", err)
		}

		err := daemonStop()
		if err == nil || !strings.Contains(err.Error(), "systemctl --user stop relevo") {
			t.Fatalf("daemonStop with the systemd unit = %v, want the systemctl command", err)
		}
	})

	t.Run("launchd", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		home := t.TempDir()
		t.Setenv("HOME", home)
		plistDir := filepath.Join(home, "Library", "LaunchAgents")
		if err := os.MkdirAll(plistDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", plistDir, err)
		}
		if err := os.WriteFile(filepath.Join(plistDir, daemonLabel+".plist"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write plist: %v", err)
		}

		err := daemonStop()
		if err == nil || !strings.Contains(err.Error(), "launchctl kill SIGTERM gui/") {
			t.Fatalf("daemonStop with the plist = %v, want the launchctl command", err)
		}
	})

	if got := atomic.LoadInt32(&signals); got != 0 {
		t.Errorf("the signal seam was called %d times, want 0", got)
	}
}

// TestDaemonStopReadsTheRecordWhileTheDaemonHoldsTheDatabase pins that a stop
// reads the pid through the daemon rather than opening relevo.db, which a
// running daemon holds: the direct open would be refused as locked.
func TestDaemonStopReadsTheRecordWhileTheDaemonHoldsTheDatabase(t *testing.T) {
	root := stopEnv(t)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create state root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "relevo.db"), nil, 0o600); err != nil {
		t.Fatalf("seed relevo.db: %v", err)
	}
	holder, err := os.OpenFile(filepath.Join(root, "relevo.db.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold the database lock: %v", err)
	}

	swapDaemonStopSeams(t)
	daemonStopInfo = func(string) (store.DaemonInfo, bool, error) { return store.DaemonInfo{PID: 4242}, true, nil }
	signalled := make(chan int, 1)
	daemonStopSignal = func(pid int) error { signalled <- pid; return nil }
	var probes int32
	daemonStopRunning = func(string) (bool, error) { return atomic.AddInt32(&probes, 1) < 2, nil }
	daemonStopSleep = func(time.Duration) {}

	if _, _, err := captureOutput(t, daemonStop); err != nil {
		t.Fatalf("daemonStop with the database held: %v", err)
	}
	select {
	case pid := <-signalled:
		if pid != 4242 {
			t.Errorf("signalled pid = %d, want 4242", pid)
		}
	default:
		t.Error("the signal seam was never called")
	}
}
