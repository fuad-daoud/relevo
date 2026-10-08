package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// runnerTransport is the transport a runner test hands Ensure, so no test opens
// a pipe or a remote to exercise the lazy build.
func runnerTransport() synclog.LogTransport { return synclog.NewMemTransport("origin-a") }

// TestRunnerEnsureBuildsAClientOnce pins the lazy-build contract: a clientless
// runner whose mark is on builds exactly one client through open, then reuses
// it. It is the one construction site for a runner's transport, so a daemon
// that calls Ensure from two triggers still holds one pipe client.
func TestRunnerEnsureBuildsAClientOnce(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	if err := MarkEnabled(local, true, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	r := &Runner{Local: local}
	calls := 0
	open := func(context.Context) (synclog.LogTransport, error) {
		calls++
		return runnerTransport(), nil
	}
	if !r.Ensure(context.Background(), open) {
		t.Fatal("Ensure with the mark on and a working open reported false")
	}
	if calls != 1 {
		t.Fatalf("open ran %d times, want exactly once", calls)
	}
	if !r.Ensure(context.Background(), open) {
		t.Fatal("second Ensure with a cached client reported false")
	}
	if calls != 1 {
		t.Fatalf("second Ensure re-ran open (%d calls), want reuse", calls)
	}
	if !r.Enabled() {
		t.Fatal("runner with a built client is not Enabled")
	}
}

// TestRunnerEnsureOnAMissingRunnerReportsFalse pins that a nil runner is the
// same as a machine with nothing installed: no open, no client.
func TestRunnerEnsureOnAMissingRunnerReportsFalse(t *testing.T) {
	t.Parallel()

	var r *Runner
	called := false
	if r.Ensure(context.Background(), func(context.Context) (synclog.LogTransport, error) {
		called = true
		return runnerTransport(), nil
	}) {
		t.Fatal("Ensure on a nil runner reported true")
	}
	if called {
		t.Fatal("Ensure on a nil runner ran open")
	}
	if r.Enabled() {
		t.Fatal("a nil runner reports Enabled")
	}
}

// TestRunnerEnsureOnAMarkedOffMachineBuildsNothing pins the mark gate: a
// machine that never turned sync on opens nothing on a trigger.
func TestRunnerEnsureOnAMarkedOffMachineBuildsNothing(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	if err := MarkEnabled(local, false, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	r := &Runner{Local: local}
	called := false
	if r.Ensure(context.Background(), func(context.Context) (synclog.LogTransport, error) {
		called = true
		return runnerTransport(), nil
	}) {
		t.Fatal("Ensure with the mark off reported true")
	}
	if called {
		t.Fatal("Ensure with the mark off ran open")
	}
	if r.Client != nil {
		t.Fatal("Ensure with the mark off cached a client")
	}
}

// TestRunnerEnsureShortCircuitsOnAPresetClient pins that a preset client drives
// without consulting the mark, so an already-built runner is reused rather than
// rebuilt and only a disable drops it.
func TestRunnerEnsureShortCircuitsOnAPresetClient(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	if err := MarkEnabled(local, false, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	r := &Runner{Client: runnerTransport(), Local: local}
	called := false
	if !r.Ensure(context.Background(), func(context.Context) (synclog.LogTransport, error) {
		called = true
		return runnerTransport(), nil
	}) {
		t.Fatal("Ensure with a preset client reported false")
	}
	if called {
		t.Fatal("Ensure with a preset client re-ran open")
	}
}

// TestRunnerEnsureOnAFailingOpenReportsFalse pins that a failed open caches
// nothing, so a later attempt retries rather than driving a half-built client.
func TestRunnerEnsureOnAFailingOpenReportsFalse(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	if err := MarkEnabled(local, true, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	r := &Runner{Local: local}
	want := errors.New("sync: dial refused in test")
	if r.Ensure(context.Background(), func(context.Context) (synclog.LogTransport, error) {
		return nil, want
	}) {
		t.Fatal("Ensure with a failing open reported true")
	}
	if r.Client != nil {
		t.Fatal("Ensure cached a client from a failed open")
	}
}

// TestRunnerEnsureWithoutALocalFileReportsFalse pins that a runner with no
// machine-local file has no row sync could own, so it opens nothing.
func TestRunnerEnsureWithoutALocalFileReportsFalse(t *testing.T) {
	t.Parallel()

	r := &Runner{}
	called := false
	if r.Ensure(context.Background(), func(context.Context) (synclog.LogTransport, error) {
		called = true
		return runnerTransport(), nil
	}) {
		t.Fatal("Ensure with no local file reported true")
	}
	if called {
		t.Fatal("Ensure with no local file ran open")
	}
	if r.Enabled() {
		t.Fatal("a runner with no local file reports Enabled")
	}
}
