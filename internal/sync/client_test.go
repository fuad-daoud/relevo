package sync

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// clientDriven is the order a tick works in: push first, so a pull has fewer
// unpushed local changes to roll back and replay.
var clientDriven = []string{"push", "pull", "stats", "checkpoint"}

// TestSyncClientFakeCallOrder pins that the fake records the calls in the order
// they were made and answers each with what it was scripted to, so an order a
// test asserts is the order the code drove rather than the order a table lists.
func TestSyncClientFakeCallOrder(t *testing.T) {
	t.Parallel()

	f := &Fake{
		Applied:  true,
		Reported: Stats{CdcOperations: 12, Revision: "rev-7"},
	}
	ctx := t.Context()

	if err := f.Push(ctx); err != nil {
		t.Fatalf("Push: %v", err)
	}
	applied, err := f.Pull(ctx)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !applied {
		t.Error("Pull applied = false, want the scripted true")
	}
	stats, err := f.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats != f.Reported {
		t.Errorf("Stats = %+v, want the scripted %+v", stats, f.Reported)
	}
	if err := f.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	if !slices.Equal(f.Calls, clientDriven) {
		t.Errorf("Calls = %v, want %v", f.Calls, clientDriven)
	}
}

// TestSyncClientFakeServesScriptedFailures pins that a scripted failure comes
// back from the call that was scripted to fail, with the calls around it still
// recorded: a retry test needs to see that the retry happened.
func TestSyncClientFakeServesScriptedFailures(t *testing.T) {
	t.Parallel()

	want := errors.New("remote refused")
	f := &Fake{PushErr: want, PullErr: want, StatsErr: want, CheckpointErr: want}
	ctx := t.Context()

	if err := f.Push(ctx); !errors.Is(err, want) {
		t.Errorf("Push = %v, want %v", err, want)
	}
	if applied, err := f.Pull(ctx); !errors.Is(err, want) || applied {
		t.Errorf("Pull = %v, %v; want false, %v", applied, err, want)
	}
	if _, err := f.Stats(ctx); !errors.Is(err, want) {
		t.Errorf("Stats error = %v, want %v", err, want)
	}
	if err := f.Checkpoint(ctx); !errors.Is(err, want) {
		t.Errorf("Checkpoint = %v, want %v", err, want)
	}
	if !slices.Equal(f.Calls, clientDriven) {
		t.Errorf("Calls = %v, want %v", f.Calls, clientDriven)
	}
}

// TestSyncClientFakeRecordsNothingElse pins the whole surface a fake offers: a
// call it was driven through leaves a name and a result behind, and nothing
// else, so a test cannot find a payload it did not script.
func TestSyncClientFakeRecordsNothingElse(t *testing.T) {
	t.Parallel()

	f := &Fake{}
	var client SyncClient = f
	ctx := t.Context()

	for _, call := range []func() error{
		func() error { return client.Push(ctx) },
		func() error { _, err := client.Pull(ctx); return err },
		func() error { _, err := client.Stats(ctx); return err },
		func() error { return client.Checkpoint(ctx) },
	} {
		if err := call(); err != nil {
			t.Fatalf("call: %v", err)
		}
	}
	if !slices.Equal(f.Calls, clientDriven) {
		t.Fatalf("Calls = %v, want %v", f.Calls, clientDriven)
	}
	for _, name := range f.Calls {
		if strings.ContainsAny(name, " \t") {
			t.Errorf("a recorded call name holds a payload: %q", name)
		}
	}
}
