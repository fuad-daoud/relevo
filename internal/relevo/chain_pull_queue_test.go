package relevo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// queuedAt is when a fixture's server accepted its round into the queue.
var queuedAt = serverStart.Add(-2 * time.Minute)

// chainPullQueuedView is a server view whose builder's round waited in the
// server's queue before it started.
func chainPullQueuedView(status string) remote.ChainView {
	v := chainPullView("shop", status, 1, 0, 0)
	v.Members[0].Rounds[0].RoundStartedAt = serverStart
	v.Members[0].Rounds[0].QueuedAt = queuedAt
	return v
}

// queueEntries is a pulled member's queue records.
func queueEntries(t *testing.T, rt Runtime, name string, round int) []store.LogEntry {
	t.Helper()
	var out []store.LogEntry
	for _, e := range chainLog(t, rt, name) {
		if e.Kind == store.KindQueue && e.Round == round {
			out = append(out, e)
		}
	}
	return out
}

// mirrorQueueEvents is the mirror database's queue rows for one binding.
func mirrorQueueEvents(t *testing.T, d *db.DB, name string) []db.EventLogRow {
	t.Helper()
	b, found, err := d.Binding(name)
	if err != nil || !found {
		t.Fatalf("mirror Binding %s: found=%v err=%v", name, found, err)
	}
	evs, err := d.Events(b.ID, 0)
	if err != nil {
		t.Fatalf("Events %s: %v", name, err)
	}
	out := []db.EventLogRow{}
	for _, e := range evs {
		if e.Kind != string(store.KindQueue) {
			continue
		}
		out = append(out, db.EventLogRow{Kind: e.Kind, Note: e.Note})
	}
	return out
}

// mirrorTheChain runs the pull and then mirrors the member into a database, so a
// test reads what history/status/site would read.
func mirrorTheChain(t *testing.T, rt Runtime, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	rt.DB = d
	if _, err := ingest.Ingest(context.Background(), ingest.StoreSource(rt.Store, name), d, IngestDeps(rt)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return d
}

// TestChainPullRecordsTheServersQueueWait pins case (a): a round the server
// queued arrives on the mirror with one queue record carrying its round, the
// server it waited on, and the wait itself -- so the queue wait is in `event`,
// and so in history/status/site, rather than only on the server that waited.
//
// Mutation: drop the chainPullQueueEntry call, and the mirror holds no queue row
// at all.
func TestChainPullRecordsTheServersQueueWait(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullQueuedView(string(chain.StatusRunning))))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	got := queueEntries(t, rt, "shop", 1)
	if len(got) != 1 {
		t.Fatalf("round 1 has %d queue records, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Round != 1 {
		t.Errorf("queue record round = %d, want 1", e.Round)
	}
	if !e.TS.Equal(queuedAt) {
		t.Errorf("queue record TS = %s, want the server's accept %s", e.TS, queuedAt)
	}
	if e.Direction != store.DirToMasterMind {
		t.Errorf("queue record direction = %s, want %s", e.Direction, store.DirToMasterMind)
	}
	if !strings.Contains(e.Note, "zen") {
		t.Errorf("queue record note = %q, want it to name the server it waited on", e.Note)
	}
	if !strings.Contains(e.Note, "2m") {
		t.Errorf("queue record note = %q, want the wait it sat for", e.Note)
	}
}

// TestChainPullQueueWaitIsQueryable pins case (e): the record reaches the mirror
// database's event table, so `history --json` and the site read the wait rather
// than only the log behind them.
//
// Mutation: keep the record out of `event` (or store it under a kind no reader
// collects), and the mirror shows no queue row.
func TestChainPullQueueWaitIsQueryable(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullQueuedView(string(chain.StatusRunning))))
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")

	d := mirrorTheChain(t, rt, "shop")
	rows := mirrorQueueEvents(t, d, "shop")
	if len(rows) != 1 {
		t.Fatalf("mirror holds %d queue events, want 1: %+v", len(rows), rows)
	}
	if rows[0].Note == nil || !strings.Contains(*rows[0].Note, "zen") {
		t.Errorf("mirrored queue note = %v, want it to name the server", rows[0].Note)
	}
}

// TestChainPullRecordsNoQueueWaitForAnUnqueuedRound pins case (b): a round the
// server handed straight to a builder never waited, ships no queue stamp, and
// writes no record -- the mirror states nothing the server did not.
func TestChainPullRecordsNoQueueWaitForAnUnqueuedRound(t *testing.T) {
	t.Parallel()

	view := chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)
	view.Members[0].Rounds[0].RoundStartedAt = serverStart
	rt := chainPullRuntime(t, chainPullFake(view))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	if got := queueEntries(t, rt, "shop", 1); len(got) != 0 {
		t.Errorf("an unqueued round recorded %d queue entries, want none: %+v", len(got), got)
	}
}

// TestChainPullRecordsTheQueueWaitOnce pins case (d): pulling the same view
// again records no second queue record, so a repeated pass never doubles the
// wait a reader sees.
//
// Mutation: drop the "already recorded" guard, and a re-pull doubles the record.
func TestChainPullRecordsTheQueueWaitOnce(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullQueuedView(string(chain.StatusRunning))))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")
	first := queueEntries(t, rt, "shop", 1)
	pullRounds(t, rt, "shop")
	again := queueEntries(t, rt, "shop", 1)

	if len(first) != 1 {
		t.Fatalf("first pull recorded %d queue entries, want 1: %+v", len(first), first)
	}
	if len(again) != 1 {
		t.Errorf("a re-pull left %d queue entries, want the 1 already recorded: %+v", len(again), again)
	}
	if first[0].Note != again[0].Note || !first[0].TS.Equal(again[0].TS) {
		t.Errorf("the queue record changed on a re-pull: %q then %q", first[0].Note, again[0].Note)
	}
}

// TestChainPullQueueWaitKeepsTheRoundClosed pins case (c): recording the wait
// installs nothing else. The round still closes, still carries its report, and
// the prompt entry the record sits beside is untouched -- a queue record is an
// addition to a round, never a substitute for its handoff.
func TestChainPullQueueWaitKeepsTheRoundClosed(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullQueuedView(string(chain.StatusRunning))))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	entries := chainLog(t, rt, "shop")
	if _, ok := pulledPromptEntry(t, rt, "shop", 1); !ok {
		t.Errorf("the queue record displaced the round's prompt entry: %+v", entries)
	}
	if _, ok := lastReportEntry(entries, 1); !ok {
		t.Errorf("the round has no report entry, so it did not close: %+v", entries)
	}
}
