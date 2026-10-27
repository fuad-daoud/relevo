package relevo

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The tests here drive the whole tick rather than the counter: a note removed
// from it fails them. What they pin is that every tick hands the sync runner
// the open-round count, with no bindings, an open round, and a reported one,
// and that the tick reaches no network to do it.

// roundsRunner returns a runner marked on but with nowhere to sync to: with no
// client the queued attempt returns before it opens a transport, so the tick's
// only sync effect is the note, which is taken only while sync is on. The
// caller installs it as rt.Sync.
func roundsRunner(t *testing.T, rt Runtime, on bool) *relevosync.Runner {
	t.Helper()
	mdb, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.MarkEnabled(local, on, time.Now()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	return &relevosync.Runner{Local: local}
}

// seedCountedBinding saves a binding whose log the tick's counter reads: a
// prompt for round 1, plus a report when closed.
func seedCountedBinding(t *testing.T, rt Runtime, name string, closed bool) {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: "/repo", Round: 1, State: store.StateActive,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder:    store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}
	if closed {
		entries = append(entries, store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		})
	}
	for _, e := range entries {
		if err := rt.Store.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
}

// TestTickWithNoBindingsNotesZeroRounds pins the empty tick: with no bindings
// the runner's count is known zero, so an attempt the next tick queues books
// idle rather than unknown.
func TestTickWithNoBindingsNotesZeroRounds(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	runner := roundsRunner(t, rt, true)
	rt.Sync = runner

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got, known := runner.RoundsOpen(); !known || got != 0 {
		t.Errorf("RoundsOpen = (%d, %v), want (0, true)", got, known)
	}
}

// TestTickWithOpenRoundNotesOneRound pins that a binding with a prompt and no
// report counts as one open round.
func TestTickWithOpenRoundNotesOneRound(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	runner := roundsRunner(t, rt, true)
	rt.Sync = runner
	seedCountedBinding(t, rt, "webshop", false)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got, known := runner.RoundsOpen(); !known || got != 1 {
		t.Errorf("RoundsOpen = (%d, %v), want (1, true)", got, known)
	}
}

// TestTickWithReportedRoundNotesZeroRounds pins that a binding whose round
// has its report counts as no open round.
func TestTickWithReportedRoundNotesZeroRounds(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	runner := roundsRunner(t, rt, true)
	rt.Sync = runner
	seedCountedBinding(t, rt, "webshop", true)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got, known := runner.RoundsOpen(); !known || got != 0 {
		t.Errorf("RoundsOpen = (%d, %v), want (0, true)", got, known)
	}
}

// TestTickWithSyncOffCountsNoRounds pins that a machine whose sync is off pays
// no log read per binding for a count nothing books: the count stays unknown.
func TestTickWithSyncOffCountsNoRounds(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	runner := roundsRunner(t, rt, false)
	rt.Sync = runner
	seedCountedBinding(t, rt, "webshop", false)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got, known := runner.RoundsOpen(); known {
		t.Errorf("RoundsOpen = (%d, %v) with sync off, want unknown", got, known)
	}
}
