package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// strayRound plants the shape a phantom takes on a member: a prompt with no
// report, so the round reads open.
func strayRound(t *testing.T, rt Runtime, member string, round int) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(member)
		if err != nil {
			return err
		}
		if round > b.Round {
			b.Round = round
		}
		if err := tx.Save(b); err != nil {
			return err
		}
		return tx.AppendLog(member, store.LogEntry{
			TS: baseTime, Round: round, Direction: store.DirToBuilder,
			Kind: store.KindPrompt, Confirmed: true,
		})
	}); err != nil {
		t.Fatalf("plant stray on %s: %v", member, err)
	}
}

// openRounds is which of a member's rounds read open.
func openRounds(t *testing.T, rt Runtime, member string) []int {
	t.Helper()
	b, err := rt.Store.Load(member)
	if err != nil {
		return nil
	}
	entries, err := rt.Store.ReadLog(member)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", member, err)
	}
	var out []int
	for r := 1; r <= b.Round; r++ {
		if store.RoundOpen(entries, r) {
			out = append(out, r)
		}
	}
	return out
}

// requireNoOpen fails when a member holds any round that reads open.
func requireNoOpen(t *testing.T, rt Runtime, members ...string) {
	t.Helper()
	for _, m := range members {
		if open := openRounds(t, rt, m); len(open) != 0 {
			t.Errorf("%s has open round(s) %v after the chain ended, want none", m, open)
		}
	}
}

// TestChainFinishLeavesNoOpenRound pins case (a): a chain that finishes on each
// part in turn leaves no open round on any of them.
//
// Mutation: sweep only the builder, and the reviewer or security member is left
// holding the round the chain ended on.
func TestChainFinishLeavesNoOpenRound(t *testing.T) {
	t.Parallel()

	t.Run("builder", func(t *testing.T) {
		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
		requireNoOpen(t, rt, "shop", "shop-rev")
	})

	t.Run("reviewer", func(t *testing.T) {
		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
		requireNoOpen(t, rt, "shop", "shop-rev")
	})

	t.Run("security", func(t *testing.T) {
		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{Security: ptr(true)})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
		chainReaderClose(t, rt, "shop-sec", chainFindingsBody(0))
		requireNoOpen(t, rt, "shop", "shop-rev", "shop-sec")
	})
}

// TestChainStopLeavesNoOpenRound pins case (b): stopping a chain closes what it
// left open.
func TestChainStopLeavesNoOpenRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	if _, err := ChainStop(t.Context(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	requireNoOpen(t, rt, "shop")
}

// TestChainRunningStillSendsTheNextRound pins case (c): the sweep touches only a
// chain that has ended, so a running chain still sends the round it awaits --
// its open round is the awaited one, not a stray.
func TestChainRunningStillSendsTheNextRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	if open := openRounds(t, rt, "shop"); len(open) != 1 || open[0] != 1 {
		t.Fatalf("running chain: open rounds = %v, want the awaited round 1", open)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("chain status = %s, want running", row.Status)
	}
}

// TestChainResumeAfterHaltStillSends pins case (d): a halt is the state a resume
// sends the next round from, so the sweep must leave a halted chain's rounds
// alone. Sweeping it would close the round the resume was about to hand the
// member, and the resume would find nothing to send.
//
// Mutation: sweep a halted chain too, and the resume cannot open the awaited
// member's round.
func TestChainResumeAfterHaltStillSends(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())

	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %s, want halted", row.Status)
	}
	if _, err := ChainResume(t.Context(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume after a halt: %v", err)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("chain status after the resume = %s, want running", row.Status)
	}
	after := chainStoredRow(t, rt, "shop")
	if after.AwaitingRound == 0 || after.AwaitingMember == "" {
		t.Errorf("the resume awaited no round: awaiting %q round %d", after.AwaitingMember, after.AwaitingRound)
	}
	name := chainMemberName(after, after.AwaitingMember)
	member, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("Load %s: %v", name, err)
	}
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	if !store.RoundOpen(entries, member.Round) {
		t.Errorf("the resume did not send %s's round %d", name, member.Round)
	}
}

// TestChainSweepClosesAStrayRound pins case (f): a round left open by a chain
// that already finished is closed, not deleted -- its log entries stay and the
// close says why the round ended.
//
// Mutation: delete the stray round's entries instead of closing them, and the
// round's history is gone rather than explained.
func TestChainSweepClosesAStrayRound(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)))
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")
	if open := openRounds(t, rt, "shop"); len(open) != 0 {
		t.Fatalf("a clean pull left round(s) %v open before the stray is planted", open)
	}

	strayRound(t, rt, "shop", 2)
	if open := openRounds(t, rt, "shop"); len(open) != 1 || open[0] != 2 {
		t.Fatalf("planted round 2, open rounds = %v, want [2]", open)
	}

	// The chain ends, and the next pull sees it ended.
	fr := rt.Remote.(*fakeRemote)
	fr.getChainResp = chainPullView("shop", string(chain.StatusDone), 1, 0, 0)
	pullRounds(t, rt, "shop")

	if open := openRounds(t, rt, "shop"); len(open) != 0 {
		t.Errorf("the finished chain left round(s) %v open, want none", open)
	}
	entries := chainLog(t, rt, "shop")
	if countLogEntries(entries, 2, store.DirToBuilder, store.KindPrompt) != 1 {
		t.Errorf("the stray round's prompt entry was deleted rather than closed: %+v", entries)
	}
	var note string
	for _, e := range entries {
		if e.Round == 2 && e.Kind == store.KindReport {
			note = e.Note
		}
	}
	if note == "" {
		t.Errorf("the stray round has no close saying why it ended: %+v", entries)
	}
}

// TestChainSweepNeverTouchesARunningChain pins the safety bound: the rule matches
// a round that has a prompt, no report, and a chain that has already ended. A
// round on a chain that is still running is the awaited one, and is left open
// however long it stays that way.
//
// Mutation: sweep on any status, and a running chain's live round is closed
// while its builder is still working.
func TestChainSweepNeverTouchesARunningChain(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)))
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")
	strayRound(t, rt, "shop", 2)

	pullRounds(t, rt, "shop")
	pullRounds(t, rt, "shop")

	if open := openRounds(t, rt, "shop"); len(open) != 1 || open[0] != 2 {
		t.Errorf("a running chain's open round = %v, want [2] left alone", open)
	}
}

// TestChainMirrorRePullKeepsAStrayClosed pins case (e): once a stray is closed,
// a later pull never resurrects it -- the round stays closed and its close stays
// where it was.
func TestChainMirrorRePullKeepsAStrayClosed(t *testing.T) {
	t.Parallel()

	rt := chainPullRuntime(t, chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)))
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")
	strayRound(t, rt, "shop", 2)

	fr := rt.Remote.(*fakeRemote)
	fr.getChainResp = chainPullView("shop", string(chain.StatusDone), 1, 0, 0)
	pullRounds(t, rt, "shop")
	first := countLogEntries(chainLog(t, rt, "shop"), 2, store.DirToMasterMind, store.KindReport)

	pullRounds(t, rt, "shop")
	again := countLogEntries(chainLog(t, rt, "shop"), 2, store.DirToMasterMind, store.KindReport)

	if first == 0 {
		t.Fatalf("the stray round was never closed: %+v", chainLog(t, rt, "shop"))
	}
	if again != first {
		t.Errorf("a re-pull wrote %d closes for the stray round, want the %d already there", again, first)
	}
	if open := openRounds(t, rt, "shop"); len(open) != 0 {
		t.Errorf("a re-pull reopened round(s) %v on a finished chain", open)
	}
}
