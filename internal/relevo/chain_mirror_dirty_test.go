package relevo

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// The mirrored-diff patch a dirty round's sealed diff holds: headers in the
// shape capture writes, naming the files left uncommitted.
const mirrorDiffPatch = `diff --git a/cmd/one.go b/cmd/one.go
--- a/cmd/one.go
+++ b/cmd/one.go
@@ -1,2 +1,3 @@
line
+added
diff --git a/cmd/two.go b/cmd/two.go
--- a/cmd/two.go
+++ b/cmd/two.go
`

// mirrorDirtyFixture is a local chain whose builder was placed on a server:
// the mirror row's own tree is this machine's repo, so the close answers on
// the pulled facts, not on a tree stat.
func mirrorDirtyFixture(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("shop")
		if err != nil {
			return err
		}
		b.Builder = store.Endpoint{Mode: store.ModeRemote, Server: "zen", Kind: "claude", AgentName: "shop"}
		return tx.Save(b)
	})
	return rt, chainBinding(t, rt, "shop")
}

// mirrorSealedRound stages the round's report and sealed diff, the facts a
// catch-up installs before its close queues the report. The diff goes through
// the row the catch-up writes it to: the store reads a round file from the
// recorded copy, not from a bare disk write.
func mirrorSealedRound(t *testing.T, rt Runtime, b store.Binding) {
	t.Helper()
	if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte(chainDoneBody()), 0o644); err != nil {
		t.Fatalf("write the round report: %v", err)
	}
	diffPath := rt.Store.DiffPath(b.Name, b.Round)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.AppendLog(b.Name, store.LogEntry{
			Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindDiff,
			Path: diffPath, Note: "2 files changed, +1 -0", Tree: "dirty", Confirmed: true,
		}); err != nil {
			return err
		}
		return tx.PutRoundFile(b.Name, b.Round, diffPath, []byte(mirrorDiffPatch))
	}); err != nil {
		t.Fatal(err)
	}
}

// remoteDoneViewOf is the closed-round view a catch-up carries: the report's
// outcome, the tree it closed over and the snapshot commit the server left
// behind, empty for the clean case.
func remoteDoneViewOf(b store.Binding, dirtyCommit string) remote.BindingView {
	return remote.BindingView{
		Name:          b.Name,
		RoundState:    remote.RoundClosed,
		ClosedRound:   b.Round,
		ReportOutcome: "done",
		DiffNote:      "2 files changed, +1 -0",
		DiffTree:      "dirty",
		DirtyCommit:   dirtyCommit,
	}
}

// mirrorCatchUp runs the catch-up's report half under the lock, the way the
// pull and the sync install one closed round.
func mirrorCatchUp(t *testing.T, rt Runtime, b store.Binding, dirtyCommit string) {
	t.Helper()
	a := &catchUpAck{
		Server: "zen", Name: b.Name, Round: b.Round, BindingRound: b.Round,
		View:       remoteDoneViewOf(b, dirtyCommit),
		HaveReport: true, HaveDiff: true,
	}
	rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		next, err := applyCatchUpReport(context.Background(), rt, tx, cur, a)
		if err != nil {
			return err
		}
		return tx.Save(next)
	})
}

// TestMirrorDirtyDoneCloseHaltsTheChain runs the real catch-up close: a placed
// writer's round closed done and its facts name a DirtyCommit, so queueReport
// passes the fact into the chain event and the event halts the step instead of
// seeding the review that would read a finished round that is not one.
func TestMirrorDirtyDoneCloseHaltsTheChain(t *testing.T) {
	t.Parallel()

	rt, b := mirrorDirtyFixture(t)
	mirrorSealedRound(t, rt, b)
	mirrorCatchUp(t, rt, b, "deadbeefcafe")

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want halted: a dirty done close must not advance", c.Status, c.Reason)
	}
	for _, want := range []string{
		"refs/relevo/shop/round-1",
		rt.Store.DiffPath("shop", 1),
		"cmd/one.go",
		"cmd/two.go",
	} {
		if !strings.Contains(c.Reason, want) {
			t.Errorf("halt reason %q does not name %s", c.Reason, want)
		}
	}
}

// TestMirrorCleanDoneCloseStillAdvances pins the other half: a mirror round
// whose facts name no DirtyCommit advances to the next step exactly as before.
func TestMirrorCleanDoneCloseStillAdvances(t *testing.T) {
	t.Parallel()

	rt, b := mirrorDirtyFixture(t)
	mirrorSealedRound(t, rt, b)
	mirrorCatchUp(t, rt, b, "")

	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "review" || st.Awaiting.Member != "assistant" {
		t.Errorf("status = %s (%q), awaiting %+v, want the review step from a clean close", c.Status, c.Reason, st.Awaiting)
	}
}

// TestPulledDirtyRoundHaltsOncePerRound pins one halt per round: a chain the
// server drives is copy-only on this machine, so a pulled round that closed
// dirty stays the round the server's own halt already answered for, and the
// client's close of it halts nothing a second time.
func TestPulledDirtyRoundHaltsOncePerRound(t *testing.T) {
	t.Parallel()

	rt, b := mirrorDirtyFixture(t)
	rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		c.Server = "zen"
		return tx.ChainPut(c)
	})

	rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		got, err := chainApply(context.Background(), rt, tx, cur, &chainCloseWF{
			Body: []byte(chainDoneBody()), Outcome: "done", Round: 1, DirtyCommit: "deadbeefcafe",
		})
		if err != nil {
			return err
		}
		if got.Halt != "" || got.State == store.StateNeedsYou {
			t.Errorf("the copy was halted a second time: halt %q state %s", got.Halt, got.State)
		}
		return tx.Save(got)
	})

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusRunning) || c.Reason != "" {
		t.Errorf("status = %s (%q), want the copy running with no second reason: the server owns the halt", c.Status, c.Reason)
	}
}
