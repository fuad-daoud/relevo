package relevo

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// serverStart is the start a server reports for a round it ran: distinct from
// baseTime, so a mirror that stamped its own clock instead is visible.
var serverStart = baseTime.Add(-3 * time.Hour)

// chainPullStampedView is a server view whose builder ran one round, started at
// serverStart.
func chainPullStampedView(status string) remote.ChainView {
	v := chainPullView("shop", status, 1, 0, 0)
	v.Members[0].Rounds[0].RoundStartedAt = serverStart
	return v
}

// pulledPromptEntry is round r's prompt entry in a pulled member's log.
func pulledPromptEntry(t *testing.T, rt Runtime, name string, r int) (store.LogEntry, bool) {
	t.Helper()
	for _, e := range chainLog(t, rt, name) {
		if e.Round == r && store.IsPromptKind(e.Kind) {
			return e, true
		}
	}
	return store.LogEntry{}, false
}

// mirrorRoundStartedAt is what the mirror database holds as round n's start.
func mirrorRoundStartedAt(t *testing.T, d *db.DB, name string, n int) time.Time {
	t.Helper()
	b, found, err := d.Binding(name)
	if err != nil || !found {
		t.Fatalf("mirror Binding %s: found=%v err=%v", name, found, err)
	}
	rounds, err := d.Rounds(b.ID)
	if err != nil {
		t.Fatalf("Rounds %s: %v", name, err)
	}
	for _, r := range rounds {
		if r.Number == n {
			return r.StartedAt
		}
	}
	t.Fatalf("mirror holds no round %d for %s", n, name)
	return time.Time{}
}

// TestChainPullStampsTheServersRoundStart pins that a pulled round carries the
// start the server reported, not the moment the pull installed it: the prompt
// entry, the mirror's own binding and the mirrored round row all read the
// server's stamp, so a duration is the round's real one.
//
// Mutation: drop the start copy in chainRoundView (or in chainRoundFacts), and
// the prompt entry and the round row read baseTime instead of serverStart.
func TestChainPullStampsTheServersRoundStart(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullStampedView(string(chain.StatusRunning)))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	// The prompt entry is the durable half: it is what the mirror derives
	// round.started_at from, and it survives the round's close. The binding's
	// own stamp is not asserted here because a close clears it by design, so a
	// closed round never keeps one.
	e, ok := pulledPromptEntry(t, rt, "shop", 1)
	if !ok {
		t.Fatalf("no prompt entry for round 1: %+v", chainLog(t, rt, "shop"))
	}
	if !e.TS.Equal(serverStart) {
		t.Errorf("prompt entry TS = %s, want the server's start %s", e.TS, serverStart)
	}
}

// TestChainPullRoundStartReachesTheMirrorRow pins the last hop: the pull stamps
// the prompt entry, and the mirror derives round.started_at from it, so
// `history --json` shows the round's real span rather than the pull's own
// ~30ms.
//
// Mutation: stamp chainPullPromptEntry with rt.Now() instead of the server's
// start, and the mirrored start reads baseTime rather than serverStart.
func TestChainPullRoundStartReachesTheMirrorRow(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullStampedView(string(chain.StatusRunning)))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	rt.DB = d
	if _, err := ingest.Ingest(context.Background(), ingest.StoreSource(rt.Store, "shop"), d, IngestDeps(rt)); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if got := mirrorRoundStartedAt(t, d, "shop", 1); !got.Equal(serverStart) {
		t.Errorf("mirrored round 1 started_at = %s, want the server's start %s", got, serverStart)
	}
}

// TestChainPullFallsBackToTheClockOnAPreStartServer pins case (b): a server
// older than the start field sends none, and the mirror installs the round with
// its own clock -- the ingest time it always recorded -- rather than refusing
// the round or stamping a zero.
//
// Mutation: treat a missing start as an error, or write a zero TS, and the
// fallback round installs nothing or an unset start.
func TestChainPullFallsBackToTheClockOnAPreStartServer(t *testing.T) {
	t.Parallel()

	view := chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)
	if !view.Members[0].Rounds[0].RoundStartedAt.IsZero() {
		t.Fatal("fixture ships a start; a pre-start server sends none")
	}
	fr := chainPullFake(view)
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	e, ok := pulledPromptEntry(t, rt, "shop", 1)
	if !ok {
		t.Fatalf("no prompt entry for round 1: %+v", chainLog(t, rt, "shop"))
	}
	if e.TS.IsZero() {
		t.Error("prompt entry TS is zero; a server that sends no start falls back to the clock")
	}
	if !e.TS.Equal(baseTime) {
		t.Errorf("prompt entry TS = %s, want the mirror's own clock %s", e.TS, baseTime)
	}
}

// TestChainPullInstallsNoPromptWhenTheServerHasNone pins case (c): a prompt
// fetch that 404s installs no prompt entry and starts nothing, so a round the
// server cannot hand over never becomes an open round on the mirror.
func TestChainPullInstallsNoPromptWhenTheServerHasNone(t *testing.T) {
	t.Parallel()

	view := chainPullStampedView(string(chain.StatusRunning))
	fr := chainPullFake(view)
	fr.roundFileFunc = func(context.Context, string, string, int, string) (io.ReadCloser, error) {
		return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound}}
	}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	if e, ok := pulledPromptEntry(t, rt, "shop", 1); ok {
		t.Errorf("prompt entry installed at %s for a round whose prompt the server does not have", e.TS)
	}
}

// TestChainPullRoundStartDoesNotMoveOnRePull pins case (d): a second pull of the
// same view changes nothing -- the prompt entry keeps the server's first start
// and is not written a second time.
func TestChainPullRoundStartDoesNotMoveOnRePull(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullStampedView(string(chain.StatusRunning)))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")
	first, ok := pulledPromptEntry(t, rt, "shop", 1)
	if !ok {
		t.Fatalf("no prompt entry for round 1: %+v", chainLog(t, rt, "shop"))
	}
	pullRounds(t, rt, "shop")
	entries := chainLog(t, rt, "shop")

	n := countLogEntries(entries, 1, store.DirToBuilder, store.KindPrompt)
	if n != 1 {
		t.Errorf("round 1 has %d prompt entries after a re-pull, want 1", n)
	}
	again, _ := pulledPromptEntry(t, rt, "shop", 1)
	if !again.TS.Equal(first.TS) {
		t.Errorf("round 1 start moved from %s to %s on a re-pull", first.TS, again.TS)
	}
}

// TestServedRoundStampsAreThatRounds pins that the start and queue stamp are
// read from the round's own entries: each of two rounds reports its own, and a
// round the server never queued ships no queue stamp of its own.
//
// Mutation: read the newest entry of either kind regardless of round, and both
// rounds report the same (wrong) start.
func TestServedRoundStampsAreThatRounds(t *testing.T) {
	t.Parallel()

	start1 := baseTime.Add(-4 * time.Hour)
	queued1 := start1.Add(-30 * time.Minute)
	start2 := baseTime.Add(-1 * time.Hour)
	entries := []store.LogEntry{
		{TS: queued1, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{TS: queued1.Add(time.Second), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Note: "queued (1/2 builders busy)"},
		{TS: start1, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Note: "started after 30s queued"},
		{TS: start2, Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}

	one, oneQueued := servedRoundStamps(entries, 1)
	if !one.Equal(start1) {
		t.Errorf("round 1 start = %s, want the admit %s", one, start1)
	}
	if !oneQueued.Equal(queued1.Add(time.Second)) {
		t.Errorf("round 1 queued = %s, want the accept %s", oneQueued, queued1.Add(time.Second))
	}

	two, twoQueued := servedRoundStamps(entries, 2)
	if !two.Equal(start2) {
		t.Errorf("round 2 start = %s, want the unqueued handoff %s", two, start2)
	}
	if !twoQueued.IsZero() {
		t.Errorf("round 2 queued = %s, want zero: it was never queued behind a builder", twoQueued)
	}
}

// TestServedViewRoundStartReachesTheClosedRound pins that the binding view's
// closed round carries the server's start, so a single-round client reads it
// without asking for the chain view.
//
// Mutation: drop the stamps from servedRoundFacts, and the closed round's start
// reads zero.
func TestServedViewRoundStartReachesTheClosedRound(t *testing.T) {
	t.Parallel()

	start := baseTime.Add(-2 * time.Hour)
	entries := []store.LogEntry{
		{TS: start.Add(-time.Minute), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{TS: start, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Note: "started after 1m0s queued"},
	}
	b := store.Binding{
		Name: "shop", State: store.StateActive, Round: 2,
		Serve: &store.ServeFacts{ClosedRound: 1, AckedRound: 0},
	}

	view := ServedView(b, entries, "record-id", "install-id")
	if !view.RoundStartedAt.Equal(start) {
		t.Errorf("ServedView RoundStartedAt = %s, want the round's own start %s", view.RoundStartedAt, start)
	}

	facts := servedRoundFacts(entries, 1)
	if !facts.RoundStartedAt.Equal(start) {
		t.Errorf("servedRoundFacts RoundStartedAt = %s, want %s", facts.RoundStartedAt, start)
	}
}
