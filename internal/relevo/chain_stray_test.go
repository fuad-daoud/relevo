package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

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

// TestChainRunningStillSendsTheNextRound pins that a running chain's open round
// is the awaited one.
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

// TestChainResumeAfterHaltStillSends pins that a halt leaves the awaited round
// open, so a resume has a round to send.
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
	name := chain.MemberName(after, after.AwaitingMember)
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
