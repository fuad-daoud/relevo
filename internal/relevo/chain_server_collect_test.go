package relevo

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedServerChainRound plants the member state a manual round leaves behind: the
// chain row names zen with the given status, and its builder mirror is active on
// round 8 with a prompt sent and no report collected.
//
// With the row done this is the shape a `send --name sync-r01` after `chain
// done` leaves: chainPullServers skips a done mirror outright, so the
// per-binding sync is the only collector that can close the round (#1056). With
// the row running the chain pull is still the one collector, and the same member
// must observe only.
func seedServerChainRound(t *testing.T, rt Runtime, name string, status chain.Status, round int) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		c.Status = string(status)
		if err := tx.ChainPut(c); err != nil {
			return err
		}
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		b.Round = round
		b.State = store.StateActive
		if err := tx.Save(b); err != nil {
			return err
		}
		return tx.AppendLog(name, store.LogEntry{
			TS: baseTime, Round: round,
			Direction: store.DirToBuilder, Kind: store.KindPrompt,
			Path: rt.Store.PromptPath(name, round), Confirmed: true,
		})
	}); err != nil {
		t.Fatalf("seedServerChainRound: %v", err)
	}
}

// closedRoundServer is a server that reports round 8 closed, with a report to
// install and a bundle to absorb: the commit pull.
func closedRoundServer(round int) *fakeRemote {
	return &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: round, ResultCommit: "c0ffee",
		},
		roundBundleResp: io.NopCloser(strings.NewReader("bundle")),
		roundFileFunc:   roundFiles("Finished the manual round\n"),
	}
}

// TestDoneServerChainMemberCollectsCloseLikeAnyOther pins that the member of a
// done server chain collects its close like any other served binding: the report
// is installed, the round advances past it, the bundle is absorbed and the ack
// goes out. The chain pull owns a running chain's rounds and skips a done
// mirror entirely, so if the per-binding sync also observed this member the
// round would sit ACTIVE forever and `wait` would time out (#1056).
func TestDoneServerChainMemberCollectsCloseLikeAnyOther(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainServerRuntime(t, nil)
	const round = 8
	fr := closedRoundServer(round)
	rt.Remote = fr
	seedServerChain(t, rt, "shop")
	seedServerChainRound(t, rt, "shop", chain.StatusDone, round)

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	if _, err := os.Stat(rt.Store.ReportPath("shop", round)); err != nil {
		t.Fatalf("round %d report not installed: %v", round, err)
	}
	b := chainBinding(t, rt, "shop")
	if b.Round != round+1 {
		t.Errorf("Round = %d, want %d: the close advances the member past round %d", b.Round, round+1, round)
	}
	if b.Builder.LastKnown != "c0ffee" {
		t.Errorf("LastKnown = %q, want the view's result commit", b.Builder.LastKnown)
	}
	// A collected close leaves the binding idle, exactly as any other collected
	// round does: the round is behind it and nothing is in flight.
	if b.Builder.RemoteStatus != "idle" {
		t.Errorf("RemoteStatus = %q, want idle after the collected close", b.Builder.RemoteStatus)
	}
	if n := countCalls(fr, "Ack:"); n != 1 {
		t.Errorf("Ack calls = %d, want 1: a collected close is acked", n)
	}
	// Every member of the done chain collects its own close, so the bundle is
	// absorbed once per member rather than only for the builder.
	if n := countCalls(fr, "RoundBundle:"); n != 3 {
		t.Errorf("RoundBundle calls = %d, want 3, one per member of the done chain", n)
	}
	if entries := chainLog(t, rt, "shop"); !HasEntry(entries, round, store.DirToMasterMind, store.KindReport) {
		t.Errorf("log = %+v, want a round %d report entry", entries, round)
	}
}

// TestRunningServerChainMemberStillObservesOnly pins the gate's other side: a
// chain that is still running keeps the observe wall, because chainPullServers
// is the one collector there and two collectors would install and ack the same
// round twice.
func TestRunningServerChainMemberStillObservesOnly(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainServerRuntime(t, nil)
	const round = 8
	fr := closedRoundServer(round)
	rt.Remote = fr
	seedServerChain(t, rt, "shop")
	seedServerChainRound(t, rt, "shop", chain.StatusRunning, round)

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	if _, err := os.Stat(rt.Store.ReportPath("shop", round)); err == nil {
		t.Fatalf("a running chain's member collected its own round: the chain pull owns that")
	}
	if b := chainBinding(t, rt, "shop"); b.Round != round {
		t.Errorf("Round = %d, want %d: an observing member never advances", b.Round, round)
	}
	if n := countCalls(fr, "Ack:"); n != 0 {
		t.Errorf("Ack calls = %d, want 0 for a chain the pull still owns", n)
	}
}
