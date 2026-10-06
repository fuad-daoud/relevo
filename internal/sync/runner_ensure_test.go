package sync

import (
	"context"
	"errors"
	"testing"
)

// TestRunnerEnsureBuildsAClientOnce pins the lazy-build contract: a clientless
// runner whose mark is on builds exactly one client through open, then reuses
// it; a machine whose mark is off, or whose open fails, reports false with
// nothing cached and nothing dialed twice.
func TestRunnerEnsureBuildsAClientOnce(t *testing.T) {
	_, _, local := openSplit(t)
	if err := MarkEnabled(local, true, runnerNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	r := &Runner{Local: local}
	calls := 0
	open := func(context.Context) (SyncClient, error) {
		calls++
		return &Fake{}, nil
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

func TestRunnerEnsureOnAMissingRunnerReportsFalse(t *testing.T) {
	var r *Runner
	called := false
	if r.Ensure(context.Background(), func(context.Context) (SyncClient, error) {
		called = true
		return &Fake{}, nil
	}) {
		t.Fatal("Ensure on a nil runner reported true")
	}
	if called {
		t.Fatal("Ensure on a nil runner ran open")
	}
}

func TestRunnerEnsureOnAMarkedOffMachineBuildsNothing(t *testing.T) {
	_, _, local := openSplit(t)
	if err := MarkEnabled(local, false, runnerNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	r := &Runner{Local: local}
	called := false
	if r.Ensure(context.Background(), func(context.Context) (SyncClient, error) {
		called = true
		return &Fake{}, nil
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

func TestRunnerEnsureShortCircuitsOnAPresetClient(t *testing.T) {
	// A preset client drives without consulting the mark: the short-circuit
	// keeps every existing caller — and every existing test — on today's
	// path, and disable's explicit drop is what retires a stale client, not
	// this check. Pinned so a future edit cannot silently move the mark read
	// in front of it.
	_, _, local := openSplit(t)
	if err := MarkEnabled(local, false, runnerNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	r := &Runner{Client: &Fake{}, Local: local}
	called := false
	if !r.Ensure(context.Background(), func(context.Context) (SyncClient, error) {
		called = true
		return &Fake{}, nil
	}) {
		t.Fatal("Ensure with a preset client reported false")
	}
	if called {
		t.Fatal("Ensure with a preset client re-ran open")
	}
}

func TestRunnerEnsureOnAFailingOpenReportsFalse(t *testing.T) {
	_, _, local := openSplit(t)
	if err := MarkEnabled(local, true, runnerNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	r := &Runner{Local: local}
	want := errors.New("sync: dial refused in test")
	if r.Ensure(context.Background(), func(context.Context) (SyncClient, error) {
		return nil, want
	}) {
		t.Fatal("Ensure with a failing open reported true")
	}
	if r.Client != nil {
		t.Fatal("Ensure cached a client from a failed open")
	}
}

func TestRunnerEnsureWithoutALocalFileReportsFalse(t *testing.T) {
	r := &Runner{}
	called := false
	if r.Ensure(context.Background(), func(context.Context) (SyncClient, error) {
		called = true
		return &Fake{}, nil
	}) {
		t.Fatal("Ensure with no local file reported true")
	}
	if called {
		t.Fatal("Ensure with no local file ran open")
	}
}
