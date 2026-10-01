package relevo

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// advanceRemoteChain moves a chain's builder member and its awaiting round to a
// later round -- the state a collected close leaves -- so a test can stage and
// ship the next round without driving the close path round 3 owns.
func advanceRemoteChain(t *testing.T, rt Runtime, round int) {
	t.Helper()
	b := chainBinding(t, rt, "shop")
	b.Round = round
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save shop at round %d: %v", round, err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		c.AwaitingMember = chain.MemberBuilder
		c.AwaitingRound = round
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("advance the chain to round %d: %v", round, err)
	}
}

// haltRemoteChain writes the halted status a resume exists for, without
// exercising the stop path a served binding does not take.
func haltRemoteChain(t *testing.T, rt Runtime) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		c.Status = string(chain.StatusHalted)
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("halt the chain: %v", err)
	}
}

// promptNoteFor reports whether entries hold a to-builder prompt entry for
// round carrying note.
func promptNoteFor(entries []store.LogEntry, round int, note string) bool {
	for _, e := range entries {
		if e.Direction == store.DirToBuilder && e.Round == round && e.Note == note {
			return true
		}
	}
	return false
}

// stageRemoteRoundFile writes text at a remote member's round prompt path, the
// file the step reads.
func stageRemoteRoundFile(t *testing.T, rt Runtime, name string, round int, text string) {
	t.Helper()
	if err := os.WriteFile(rt.Store.PromptPath(name, round), []byte(text), 0o644); err != nil {
		t.Fatalf("stage %s round %d: %v", name, round, err)
	}
}

// TestChainSendPendingShipsAStagedRoundOnce pins the step's happy path: the
// start staged plan 1 and one StartRound shipped it with verify off, the prompt
// entry carries the chain step note, the shipped facts landed, and a second pass
// ships and writes nothing.
func TestChainSendPendingShipsAStagedRoundOnce(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	startedChain(t, rt, ChainOptions{})

	if string(fr.startRoundPlan) != "build it" {
		t.Errorf("shipped plan = %q, want the plan copy", fr.startRoundPlan)
	}
	if fr.startRoundVerify == nil || *fr.startRoundVerify {
		t.Errorf("verify = %v, want an explicit false", fr.startRoundVerify)
	}
	if !promptNoteFor(chainLog(t, rt, "shop"), 1, "chain builder") {
		t.Errorf("log = %+v, want the prompt entry with the chain step note", chainLog(t, rt, "shop"))
	}
	b := chainBinding(t, rt, "shop")
	if b.Builder.LastShipped != "1111111111111111111111111111111111111111" {
		t.Errorf("LastShipped = %q, want the shipped head", b.Builder.LastShipped)
	}
	if b.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Errorf("RemoteStatus = %q, want running", b.Builder.RemoteStatus)
	}
	if b.State != store.StateActive {
		t.Errorf("State = %q, want active", b.State)
	}
	if !b.RoundStartedAt.Equal(baseTime) {
		t.Errorf("RoundStartedAt = %v, want the clock's stamp", b.RoundStartedAt)
	}

	before := len(chainLog(t, rt, "shop"))
	fr.calls = nil
	if err := chainSendPending(context.Background(), rt); err != nil {
		t.Fatalf("second chainSendPending: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Errorf("second pass calls = %v, want none", fr.calls)
	}
	if got := len(chainLog(t, rt, "shop")); got != before {
		t.Errorf("log length after the second pass = %d, want %d", got, before)
	}
}

// TestChainSendPendingSkipsLocalAndUnstaged pins the three silent skips: a
// local awaiting member, a remote member with no staged file, and a remote
// member in NEEDS YOU are all left alone.
func TestChainSendPendingSkipsLocalAndUnstaged(t *testing.T) {
	t.Parallel()

	t.Run("a local awaiting member", func(t *testing.T) {
		t.Parallel()

		fr := &fakeRemote{}
		rt, _ := chainRuntime(t)
		rt.Remote = fr
		rt.Transport = &fakeTransport{}
		startedChain(t, rt, ChainOptions{})
		// A later round staged for the local member: without the remote check
		// the step would try to ship it here.
		advanceRemoteChain(t, rt, 2)
		stageRemoteRoundFile(t, rt, "shop", 2, "the next plan")

		fr.calls = nil
		if err := chainSendPending(context.Background(), rt); err != nil {
			t.Fatalf("chainSendPending: %v", err)
		}
		if len(fr.calls) != 0 {
			t.Errorf("calls = %v, want none for a local member", fr.calls)
		}
		if b := chainBinding(t, rt, "shop"); b.State != store.StateActive {
			t.Errorf("local member state = %q, want it untouched", b.State)
		}
	})

	t.Run("a remote member with no staged file", func(t *testing.T) {
		t.Parallel()

		fr := chainRemoteFake()
		rt, _, _ := chainRemoteRuntime(t, fr)
		startedChain(t, rt, ChainOptions{})
		advanceRemoteChain(t, rt, 2)

		fr.calls = nil
		if err := chainSendPending(context.Background(), rt); err != nil {
			t.Fatalf("chainSendPending: %v", err)
		}
		if len(fr.calls) != 0 {
			t.Errorf("calls = %v, want none with no staged file", fr.calls)
		}
	})

	t.Run("a remote member in NEEDS YOU", func(t *testing.T) {
		t.Parallel()

		fr := chainRemoteFake()
		rt, _, _ := chainRemoteRuntime(t, fr)
		startedChain(t, rt, ChainOptions{})
		advanceRemoteChain(t, rt, 2)
		stageRemoteRoundFile(t, rt, "shop", 2, "the next plan")
		b := chainBinding(t, rt, "shop")
		b.State = store.StateNeedsYou
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save NEEDS YOU: %v", err)
		}

		fr.calls = nil
		if err := chainSendPending(context.Background(), rt); err != nil {
			t.Fatalf("chainSendPending: %v", err)
		}
		if len(fr.calls) != 0 {
			t.Errorf("calls = %v, want none for a member in NEEDS YOU", fr.calls)
		}
	})
}

// TestChainSendPendingFailureHaltsTheChain pins the failed ship: the member goes
// NEEDS YOU with the builder-send halt, and the chain halts with the
// member-could-not-start reason, one trace row and one delivery.
func TestChainSendPendingFailureHaltsTheChain(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	startedChain(t, rt, ChainOptions{})
	advanceRemoteChain(t, rt, 2)
	stageRemoteRoundFile(t, rt, "shop", 2, "the next plan")
	fr.startRoundErr = errors.New("socket closed")

	if err := chainSendPending(context.Background(), rt); err != nil {
		t.Fatalf("chainSendPending = %v, want the failure recorded, not returned", err)
	}

	b := chainBinding(t, rt, "shop")
	if b.State != store.StateNeedsYou {
		t.Errorf("member state = %q, want needs_you", b.State)
	}
	if !hasPrefix(b.Halt, "builder send failed: ") {
		t.Errorf("member halt = %q, want the builder-send wording", b.Halt)
	}
	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Errorf("chain status = %q, want halted", row.Status)
	}
	if row.Reason != "member shop could not start: socket closed" {
		t.Errorf("halt reason = %q, want the member-could-not-start wording", row.Reason)
	}
	if got := len(chainTrace(t, rt, "shop")); got != 1 {
		t.Errorf("trace rows = %d, want one", got)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending deliveries = %d, want the one end delivery", len(pending))
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
