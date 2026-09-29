//go:build scopeaccept && unix

// This local acceptance check sits behind a non-default build tag: it needs a
// real systemd user manager and creates a real scope, its own relevo-accept-*
// one, and touches nothing else.
//
//	go test -tags scopeaccept -count=1 -run TestScopeAcceptReapsItsScope -v ./internal/proc
package proc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TestScopeAcceptReapsItsScope leaves a detached process in a scope of its own
// and asserts the scope empties: no active scope, no straggler, trailer last.
func TestScopeAcceptReapsItsScope(t *testing.T) {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("systemd-run is not installed")
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	unit := "relevo-accept-" + hex.EncodeToString(suffix)

	dir := t.TempDir()
	stream := filepath.Join(dir, "harness.jsonl")
	// The harness runs in dir, so its pid file is relative to it.
	pidFile := filepath.Join(dir, "sleep.pid")

	// The straggler records its pid before exec'ing sleep and the harness waits
	// for that record, so nothing lands in the stream after the trailer. The pid
	// comes from /proc/self/stat, never $$, which systemd-run rewrites.
	const script = "setsid sh -c 'read -r p _ </proc/self/stat; echo \"$p\" > sleep.pid; exec sleep 300' &\n" +
		"while [ ! -s sleep.pid ]; do sleep 0.05; done\n" +
		"exit 0\n"

	started := time.Now()
	r := New()
	h, err := r.Start(context.Background(), spawn.ProcSpec{
		Dir: dir, Argv: []string{"sh", "-c", script},
		LogPath: filepath.Join(dir, "harness.log"), StreamPath: stream,
		Scope: &spawn.ScopeSpec{Unit: unit, CPUWeight: 100},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Keep the machine clean if an assertion fails before the reap did its job.
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	// 1. The stream ends with relevo-exit:0 within 5 s.
	code, ok := -1, false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if code, ok = r.ExitCode(context.Background(), h, stream); ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok || code != 0 {
		data, _ := os.ReadFile(stream)
		t.Fatalf("stream after 5s = %q (code %d, ok %v); want it to end with %s0", data, code, ok, spawn.ExitTrailer)
	}
	t.Logf("stream ended with %s%d %s after the harness exit", spawn.ExitTrailer, code, time.Since(started).Round(time.Millisecond))

	// 2. The scope is no longer active within 5 s: it emptied, so --collect
	// removed it.
	active := ""
	for deadline := time.Now().Add(5 * time.Second); ; {
		active = scopeActive(t, ScopeUnitFileName(unit))
		if active != "active" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if active == "active" {
		t.Errorf("systemctl --user is-active %s = %q; want the scope not active after the harness exited", ScopeUnitFileName(unit), active)
	}

	// 3. The sleep the harness left behind is gone within 5 s.
	pid := readSleepPID(t, pidFile)
	gone := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			gone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gone {
		t.Errorf("sleep pid %d is still there; want the supervisor to have reaped it", pid)
	}

	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	t.Logf("acceptance: %s is %q after the round, sleep pid %d is gone; whole check took %s\nstream: %q",
		ScopeUnitFileName(unit), active, pid, time.Since(started).Round(time.Millisecond), data)
}

// scopeActive returns what `systemctl --user is-active` reports for the unit.
func scopeActive(t *testing.T, unit string) string {
	t.Helper()
	out, err := exec.Command("systemctl", "--user", "is-active", unit).Output()
	if err != nil && len(out) == 0 {
		return "unknown (" + err.Error() + ")"
	}
	return strings.TrimSpace(string(out))
}

func readSleepPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the harness's pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("pid file %q: %v", data, err)
	}
	return pid
}
