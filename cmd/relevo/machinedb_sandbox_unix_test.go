//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// blockedOwnerRoot points the machine root at a state home this user cannot
// enter: the socket path resolves under it, so every dial of it earns EACCES
// the way a sandboxed shell's socket refusal does -- no socket, no listener
// and no spawned process involved.
func blockedOwnerRoot(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root traverses a 000 directory, so the permission denial never happens")
	}
	home := shortStateRoot(t)
	if err := os.MkdirAll(filepath.Join(home, "relevo"), 0o700); err != nil {
		t.Fatalf("mkdir the machine root: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", home)
	if err := os.Chmod(home, 0o000); err != nil {
		t.Fatalf("chmod the state home: %v", err)
	}
	// This runs ahead of shortStateRoot's own RemoveAll, which could not
	// recurse into the blocked directory.
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	return home
}

func TestOpenDBRouteSandboxedDialMatchesTheSandboxSentinel(t *testing.T) {
	blockedOwnerRoot(t)
	noDaemon(t)
	const budget = 250 * time.Millisecond
	withOwnerRoute(t, budget)

	start := time.Now()
	_, ok, err := openDBRoute(machineDBPath())
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("openDBRoute declined the machine path on the owner route")
	}
	if err == nil {
		t.Fatal("openDBRoute through a blocked state home succeeded")
	}
	if !errors.Is(err, errOwnerSandboxDenied) {
		t.Errorf("openDBRoute error = %v, want it to wrap errOwnerSandboxDenied", err)
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("openDBRoute error = %v, want the first dial's EACCES preserved through the loop", err)
	}
	if elapsed > budget+2*time.Second {
		t.Errorf("openDBRoute took %v, want at most the %v budget plus slack", elapsed, budget)
	}
}

func TestOpenDBRouteMissingSocketStillMatchesOnlyOwnerUnavailable(t *testing.T) {
	home := shortStateRoot(t)
	if err := os.MkdirAll(filepath.Join(home, "relevo"), 0o700); err != nil {
		t.Fatalf("mkdir the machine root: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", home)
	noDaemon(t)
	withOwnerRoute(t, 250*time.Millisecond)

	_, ok, err := openDBRoute(machineDBPath())
	if !ok {
		t.Fatal("openDBRoute declined the machine path on the owner route")
	}
	if err == nil {
		t.Fatal("openDBRoute with a missing socket succeeded")
	}
	if !errors.Is(err, errOwnerUnavailable) {
		t.Errorf("openDBRoute error = %v, want it to wrap errOwnerUnavailable", err)
	}
	if errors.Is(err, errOwnerSandboxDenied) {
		t.Errorf("openDBRoute error = %v, want no sandbox sentinel on a missing socket", err)
	}
	if !strings.Contains(err.Error(), "relevo owner unavailable") {
		t.Errorf("openDBRoute error %q must keep naming the owner unavailable", err)
	}
}

// TestStatuslineSilentOnSandboxedDial pins the silence: a statusline whose
// dial the sandbox refused prints nothing at all, exactly as it does for an
// owner that never answers.
func TestStatuslineSilentOnSandboxedDial(t *testing.T) {
	blockedOwnerRoot(t)
	noDaemon(t)

	mode, budget, startWait := routeForArgs([]string{"status", "--line"})
	installDBRoute(mode, budget, startWait)
	t.Cleanup(func() { installDBRoute(routeNone, verbDialBudget, 0) })

	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	origStdin := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() {
		os.Stdin = origStdin
		_ = devnull.Close()
	})

	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"status", "--line"}) })
	if err != nil {
		t.Fatalf("status --line: %v", err)
	}
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Errorf("stdout = %q, stderr = %q; want both empty", stdout, stderr)
	}
}

// TestDialWithStartReturnsAtOnceOnSandboxDenial pins the fail-fast rule: an
// EPERM first dial returns the sandbox error without calling the daemon
// starter and well inside the budget, while a missing socket still takes
// today's start-and-retry path.
func TestDialWithStartReturnsAtOnceOnSandboxDenial(t *testing.T) {
	const budget = 10 * time.Second
	withOwnerRoute(t, budget)

	var starts int32
	daemonStarter = func() error { atomic.AddInt32(&starts, 1); return nil }
	t.Cleanup(func() { daemonStarter = startDaemon })

	origDial := firstDial
	t.Cleanup(func() { firstDial = origDial })
	refused := sandboxDialError(syscall.EPERM)
	firstDial = func(context.Context, string) (net.Conn, error) { return nil, refused }

	start := time.Now()
	_, err := dialWithStart(context.Background(), sandboxTestSock)
	elapsed := time.Since(start)

	if !errors.Is(err, errOwnerSandboxDenied) {
		t.Errorf("dialWithStart error = %v, want it to wrap errOwnerSandboxDenied", err)
	}
	if !errors.Is(err, syscall.EPERM) {
		t.Errorf("dialWithStart error = %v, want the refusal errno preserved", err)
	}
	if got := atomic.LoadInt32(&starts); got != 0 {
		t.Errorf("the starter ran %d times on a sandbox denial, want 0", got)
	}
	if elapsed > budget/5 {
		t.Errorf("dialWithStart took %v against a %v budget, want the denial returned at once", elapsed, budget)
	}

	missing := sandboxDialError(syscall.ENOENT)
	firstDial = func(context.Context, string) (net.Conn, error) { return nil, missing }
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err = dialWithStart(ctx, sandboxTestSock)
	if got := atomic.LoadInt32(&starts); got != 1 {
		t.Errorf("the starter ran %d times after a missing-socket first dial, want 1", got)
	}
	if !errors.Is(err, errOwnerUnavailable) {
		t.Errorf("dialWithStart error = %v, want the missing socket to end unavailable", err)
	}
	if errors.Is(err, errOwnerSandboxDenied) {
		t.Errorf("dialWithStart error = %v, want no sandbox sentinel on a missing socket", err)
	}
}

// TestStatusOnSandboxDeniedOwnerPrintsSandboxCode pins the real verb path:
// `relevo status` turns its newRuntime failure into fail(codeInternal, ...),
// and the constructor rule must still classify that sandbox denial as
// owner_sandbox_denied -- not internal with a bugreport hint.
func TestStatusOnSandboxDeniedOwnerPrintsSandboxCode(t *testing.T) {
	blockedOwnerRoot(t)
	noDaemon(t)
	withOwnerRoute(t, 250*time.Millisecond)

	var runErr error
	stdout, _, _ := captureOutput(t, func() error {
		runErr = run([]string{"status"})
		if runErr == nil {
			t.Error("status through a blocked state home succeeded")
		}
		return nil
	})

	var ce *cliError
	if !errors.As(runErr, &ce) {
		t.Fatalf("status error = %v (%T), want a coded error", runErr, runErr)
	}
	if ce.code != codeOwnerSandboxDenied {
		t.Errorf("status code = %q, want %q", ce.code, codeOwnerSandboxDenied)
	}
	if ce.next != catalog[codeOwnerSandboxDenied].next {
		t.Errorf("status next = %q, want the catalog hint", ce.next)
	}

	var human bytes.Buffer
	if code := report(&human, runErr, false); code != 1 {
		t.Errorf("human exit = %d, want 1", code)
	}
	line := human.String()
	if !strings.HasPrefix(line, "relevo: owner_sandbox_denied: ") {
		t.Errorf("human line = %q, want the sandbox code first", line)
	}
	if strings.Contains(line, "bugreport") {
		t.Errorf("human line = %q, want no bugreport hint", line)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want it clean", stdout)
	}
}
