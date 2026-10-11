package relevo

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// traceStep is one server-side transition a pull copies onto the mirror.
func traceStep(seq int, step, member string, round int) remote.ChainEventView {
	return remote.ChainEventView{
		Seq: seq, TS: serverStart.Add(time.Duration(seq) * time.Minute),
		Phase: "build", Step: step, Member: member, Round: round, Plan: 1,
		Event:  `{"kind":"builder_closed","gate":"green"}`,
		Action: `{"kind":"send","member":"` + member + `"}`,
	}
}

// chainPullTraceView is a running server chain whose trace holds the given rows.
func chainPullTraceView(trace ...remote.ChainEventView) remote.ChainView {
	v := chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)
	v.Trace = trace
	return v
}

// TestChainPullCopiesTheServerTraceInSeqOrder pins case (a): a running chain's
// trace rows arrive on the mirror with the server's own seq, timestamp, step,
// member, round, plan, event, action and reason -- so `show --trace` shows the
// server's run rather than a bare final row.
//
// Mutation: drop the ChainEventPut loop from chainPullTrace, and the mirror
// holds no trace row at all.
func TestChainPullCopiesTheServerTraceInSeqOrder(t *testing.T) {
	t.Parallel()

	rows := []remote.ChainEventView{
		traceStep(1, "building", "shop", 1),
		traceStep(2, "reviewing", "shop-rev", 1),
	}
	rows[1].Reason = "check red after regate"
	rt := chainPullRuntime(t, chainPullFake(chainPullTraceView(rows...)))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	got := chainTrace(t, rt, "shop")
	if len(got) != 2 {
		t.Fatalf("mirror trace holds %d rows, want the server's 2: %+v", len(got), got)
	}
	for i, want := range rows {
		e := got[i]
		if e.Seq != want.Seq {
			t.Errorf("row %d seq = %d, want %d", i, e.Seq, want.Seq)
		}
		if !e.TS.Equal(want.TS) {
			t.Errorf("row %d ts = %s, want %s", i, e.TS, want.TS)
		}
		if e.Step != want.Step || e.Member != want.Member || e.Round != want.Round || e.Plan != want.Plan {
			t.Errorf("row %d = step %s member %s round %d plan %d, want step %s member %s round %d plan %d",
				i, e.Step, e.Member, e.Round, e.Plan, want.Step, want.Member, want.Round, want.Plan)
		}
		if e.Event != want.Event || e.Action != want.Action || e.Reason != want.Reason {
			t.Errorf("row %d event/action/reason = %s/%s/%q, want %s/%s/%q",
				i, e.Event, e.Action, e.Reason, want.Event, want.Action, want.Reason)
		}
	}
}

// TestChainPullTraceAppendsNothingOnRePull pins case (b): a terminal view
// pulls its final rows once, and pulling the same view again adds nothing and
// moves no seq.
//
// Mutation: append on every pass instead of skipping a stored seq, and the
// mirror's trace grows past the server's own count.
func TestChainPullTraceAppendsNothingOnRePull(t *testing.T) {
	t.Parallel()

	view := chainPullTraceView(traceStep(1, "building", "shop", 1), traceStep(2, "reviewing", "shop-rev", 1))
	view.Status = string(chain.StatusDone)
	rt := chainPullRuntime(t, chainPullFake(view))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")
	first := chainTrace(t, rt, "shop")
	pullRounds(t, rt, "shop")
	again := chainTrace(t, rt, "shop")

	if len(first) != 2 {
		t.Fatalf("first pull stored %d trace rows, want 2: %+v", len(first), first)
	}
	if len(again) != len(first) {
		t.Errorf("a re-pull left %d trace rows, want the %d already stored", len(again), len(first))
	}
	for i := range first {
		if first[i].Seq != again[i].Seq || first[i].Step != again[i].Step {
			t.Errorf("row %d changed on a re-pull: %+v then %+v", i, first[i], again[i])
		}
	}
}

// TestChainPullTraceKeepsTheServersSeqs pins the ordering a renumber would
// break: the mirror stores the seqs the server named, in that order, rather than
// appending its own.
//
// Mutation: renumber on copy (or allocate the seq locally), and the stored seqs
// no longer match the server's.
func TestChainPullTraceKeepsTheServersSeqs(t *testing.T) {
	t.Parallel()

	// A gap in the seqs: the server shipped three rows numbered 2, 4 and 7.
	rt := chainPullRuntime(t, chainPullFake(chainPullTraceView(
		traceStep(2, "building", "shop", 1),
		traceStep(4, "reviewing", "shop-rev", 1),
		traceStep(7, "shipping", "shop", 2),
	)))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	got := chainTrace(t, rt, "shop")
	wantSeqs := []int{2, 4, 7}
	if len(got) != len(wantSeqs) {
		t.Fatalf("mirror trace holds %d rows, want %d: %+v", len(got), len(wantSeqs), got)
	}
	for i, want := range wantSeqs {
		if got[i].Seq != want {
			t.Errorf("row %d seq = %d, want the server's %d", i, got[i].Seq, want)
		}
	}
}

// TestChainPullTraceRefusesAMalformedRow pins case (d): a row whose event or
// action is not the document those columns hold is refused, never stored as a
// guess and never repaired -- the well-formed rows beside it still land, so one
// bad row does not cost the whole trace.
//
// Mutation: accept any string into the columns, and the malformed row is stored.
func TestChainPullTraceRefusesAMalformedRow(t *testing.T) {
	t.Parallel()

	bad := traceStep(2, "reviewing", "shop-rev", 1)
	bad.Event = `{"kind":"builder_closed"` // truncated document
	good := traceStep(3, "shipping", "shop", 2)
	rt := chainPullRuntime(t, chainPullFake(chainPullTraceView(traceStep(1, "building", "shop", 1), bad, good)))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	got := chainTrace(t, rt, "shop")
	for _, e := range got {
		if e.Seq == bad.Seq {
			t.Errorf("a malformed row was stored: %+v", e)
		}
	}
	if len(got) != 2 {
		t.Errorf("mirror trace holds %d rows, want the two well-formed ones: %+v", len(got), got)
	}
}

// TestChainPullTraceFabricatesNothingForAnOldChain pins case (c): a chain from a
// server that ships no trace keeps the mirror as it was -- no rows invented for
// a trace the server never sent.
func TestChainPullTraceFabricatesNothingForAnOldChain(t *testing.T) {
	t.Parallel()

	view := chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)
	if len(view.Trace) != 0 {
		t.Fatalf("fixture ships a trace; an old server sends none: %+v", view.Trace)
	}
	rt := chainPullRuntime(t, chainPullFake(view))
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	if got := chainTrace(t, rt, "shop"); len(got) != 0 {
		t.Errorf("mirror holds %d trace rows for a chain the server traced none: %+v", len(got), got)
	}
}

// TestChainEventPutKeepsTheNamedSeq pins the store half a mirror copy needs: the
// row is stored under the seq it names, and storing the same seq twice is a
// no-op that reports nothing new.
//
// Mutation: allocate the seq in the store (or overwrite), and a second put
// renumbers or replaces the row.
func TestChainEventPutKeepsTheNamedSeq(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullView("shop", string(chain.StatusRunning), 0, 0, 0)))
	seedServerChain(t, rt, "shop")

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, ok := chainTraceRow(traceStep(4, "reviewing", "shop-rev", 1))
		if !ok {
			t.Fatal("a well-formed wire row was refused")
		}
		added, aerr := tx.ChainEventPut("shop", row)
		if aerr != nil {
			return aerr
		}
		if !added {
			t.Error("first put reported nothing new")
		}
		again, aerr := tx.ChainEventPut("shop", row)
		if aerr != nil {
			return aerr
		}
		if again {
			t.Error("a second put of the same seq reported a new row")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChainEventPut: %v", err)
	}

	got := chainTrace(t, rt, "shop")
	if len(got) != 1 || got[0].Seq != 4 {
		t.Errorf("stored trace = %+v, want the single row at the server's seq 4", got)
	}
}
