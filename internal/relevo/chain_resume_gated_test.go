package relevo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// gatedReviewerHalt drives a started chain to the halt the issue describes: the
// builder closes green, so the reviewer's round opens with a live process, and
// the reviewer's only candidate is rate-limited, so switchBuilder cannot switch
// and halts the member. It returns the member as the halt left it.
func gatedReviewerHalt(t *testing.T, rt Runtime) store.Binding {
	t.Helper()

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	revBefore := chainBinding(t, rt, "shop-rev")
	if !HasPromptEntry(chainLog(t, rt, "shop-rev"), revBefore.Round) {
		t.Fatal("test premise: the reviewer's round must be open")
	}

	// A rate-limit gate on the reviewer's provider, with no other candidate
	// serving its actor.
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testClaudeRef, time.Time{}, "rate-limited"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	chainReconcile(t, rt, "shop-rev")
	tickChains(context.Background(), rt)

	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	member, _, ok := chainAwaited(rt.Store, chainStoredRow(t, rt, "shop"))
	if !ok || member != "shop-rev" {
		t.Fatalf("awaited member = %q (ok=%v), want shop-rev", member, ok)
	}

	revAtHalt := chainBinding(t, rt, "shop-rev")
	// switchBuilder's resolve failure runs before its closeOld kill, so the halt
	// leaves the member NEEDS YOU with Builder.PID intact. The process has since
	// exited, so the resume must see it as gone.
	rt.Runner.(*fakeRunner).script(revAtHalt.Builder.PID, false)
	return revAtHalt
}

// mutateReviewer rewrites the awaited member's row under the store lock.
func mutateReviewer(t *testing.T, rt Runtime, mutate func(store.Binding) store.Binding) {
	t.Helper()

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		rev, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		return tx.Save(mutate(rev))
	}); err != nil {
		t.Fatalf("mutate shop-rev: %v", err)
	}
}

func TestChainResumeHaltedOnAllGatedActorResumes(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	revAtHalt := gatedReviewerHalt(t, rt)

	// ChainResume with no flags closes the dead round and re-runs the review.
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	revAfter := chainBinding(t, rt, "shop-rev")
	if revAfter.Round <= revAtHalt.Round {
		t.Errorf("reviewer round = %d, want a round after %d", revAfter.Round, revAtHalt.Round)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("chain status = %q, want running", row.Status)
	}
}

func TestChainResumeHaltedOnAllGatedActorSwapsRole(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	customRows := chainRows()
	newActor := "assistant"
	candJSON := `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"other","model":"alt-model","roles":["builder","reviewer"]}
]`
	rt.Candidates = candidateSet(t, candJSON)
	altCandidate := "claude/other/alt-model"
	customRows[newActor] = roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{altCandidate},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "reviewer"}},
	}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, customRows)

	startedChain(t, rt, ChainOptions{})

	revAtHalt := gatedReviewerHalt(t, rt)

	// Resume swapping to assistant, whose candidate is on provider "other".
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:          "shop",
		ReviewerActor: newActor,
	}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	revAfter := chainBinding(t, rt, "shop-rev")
	if revAfter.Round <= revAtHalt.Round {
		t.Errorf("reviewer round = %d, want a round after %d", revAfter.Round, revAtHalt.Round)
	}
	if revAfter.Role != newActor {
		t.Errorf("reviewer role = %q, want %q", revAfter.Role, newActor)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("chain status = %q, want running", row.Status)
	}

	members, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers: %v", err)
	}
	found := false
	for _, m := range members {
		if m.Binding == "shop-rev" {
			found = true
			if m.Actor != newActor {
				t.Errorf("chain_member Actor = %q, want %q", m.Actor, newActor)
			}
		}
	}
	if !found {
		t.Error("shop-rev not found in chain_members")
	}
}

// TestChainResumeRefusesLiveProcess pins the half of the rule that a live
// process is not evidence of death: a member whose process is still running is
// left to the resume's own refusal whatever state it is in.
func TestChainResumeRefusesLiveProcess(t *testing.T) {
	t.Parallel()

	t.Run("state active", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		if _, err := availability.Unavailable(AvailabilityDeps(rt), testClaudeRef, time.Time{}, "rate-limited"); err != nil {
			t.Fatalf("Unavailable: %v", err)
		}
		chainReconcile(t, rt, "shop-rev")
		tickChains(context.Background(), rt)

		// Back to active while the process keeps running.
		mutateReviewer(t, rt, func(b store.Binding) store.Binding {
			b.State = store.StateActive
			return b
		})
		if b := chainBinding(t, rt, "shop-rev"); b.Builder.PID == 0 {
			t.Fatal("test premise: the reviewer's round must have a process")
		}

		assertResumeOpenRoundRefused(t, rt, chain.MemberReviewer)
	})

	t.Run("state needs_you", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		if _, err := availability.Unavailable(AvailabilityDeps(rt), testClaudeRef, time.Time{}, "rate-limited"); err != nil {
			t.Fatalf("Unavailable: %v", err)
		}
		chainReconcile(t, rt, "shop-rev")
		tickChains(context.Background(), rt)

		// The halt's own state: NEEDS YOU with the process still running.
		if b := chainBinding(t, rt, "shop-rev"); b.State != store.StateNeedsYou || b.Builder.PID == 0 {
			t.Fatalf("test premise: state = %q pid = %d, want needs_you with a process", b.State, b.Builder.PID)
		}

		assertResumeOpenRoundRefused(t, rt, chain.MemberReviewer)
	})
}

// TestChainResumeRefusesAPidlessMemberOutsideNeedsYou pins the pidless half of
// the rule: with no process to probe there is no evidence the round died, so
// only NEEDS YOU -- the state the halt leaves behind -- is closed, and any
// other state stays a refusal.
func TestChainResumeRefusesAPidlessMemberOutsideNeedsYou(t *testing.T) {
	t.Parallel()

	t.Run("state active", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		gatedReviewerHalt(t, rt)

		// The process is gone and so is the pid the halt kept: nothing is left
		// to probe, and the member is active rather than NEEDS YOU.
		mutateReviewer(t, rt, func(b store.Binding) store.Binding {
			b.State = store.StateActive
			b.Builder.PID = 0
			return b
		})

		assertResumeOpenRoundRefused(t, rt, chain.MemberReviewer)
	})
}

// TestChainResumeProcessDeathAndProbeError pins the two process facts: a pid
// whose process is gone is closed whatever state the member is in, and a probe
// that errors is an OS hiccup, not evidence of death.
func TestChainResumeProcessDeathAndProbeError(t *testing.T) {
	t.Parallel()

	t.Run("dead process closes the round", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		revAtHalt := gatedReviewerHalt(t, rt)
		if revAtHalt.State != store.StateNeedsYou {
			t.Fatalf("test premise: state = %q, want needs_you", revAtHalt.State)
		}
		revBefore := chainBinding(t, rt, "shop-rev")

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
			t.Fatalf("ChainResume: %v", err)
		}

		revAfter := chainBinding(t, rt, "shop-rev")
		if revAfter.Round <= revBefore.Round {
			t.Errorf("reviewer round = %d, want past %d", revAfter.Round, revBefore.Round)
		}
	})

	t.Run("dead process closes in a state that is not needs_you", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		gatedReviewerHalt(t, rt)

		// The close is a property of the process, not of NEEDS YOU: an active
		// member whose process is gone is closed too.
		mutateReviewer(t, rt, func(b store.Binding) store.Binding {
			b.State = store.StateActive
			return b
		})
		revBefore := chainBinding(t, rt, "shop-rev")

		if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
			t.Fatalf("ChainResume: %v", err)
		}

		revAfter := chainBinding(t, rt, "shop-rev")
		if revAfter.Round <= revBefore.Round {
			t.Errorf("reviewer round = %d, want past %d", revAfter.Round, revBefore.Round)
		}
	})

	t.Run("probe error leaves the round to the refusal", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		gatedReviewerHalt(t, rt)
		rt.Runner.(*fakeRunner).aliveErr = errors.New("liveness probe hiccup")

		assertResumeOpenRoundRefused(t, rt, chain.MemberReviewer)
	})
}

// TestChainResumeDonePausedAndQueuedMembersLeftToRefusal pins the three members
// the close must skip: Stop refuses done and paused, and a queued round has
// nothing to kill, so all three fall through to the resume's own refusal
// instead of failing the resume with Stop's error.
func TestChainResumeDonePausedAndQueuedMembersLeftToRefusal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(store.Binding) store.Binding
	}{
		{"done", func(b store.Binding) store.Binding { b.State = store.StateDone; return b }},
		{"paused", func(b store.Binding) store.Binding { b.State = store.StatePaused; return b }},
		{"queued", func(b store.Binding) store.Binding { b.QueuedAt = baseTime; return b }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rt, _ := chainRuntime(t)
			startedChain(t, rt, ChainOptions{})
			gatedReviewerHalt(t, rt)
			mutateReviewer(t, rt, tc.mutate)

			assertResumeOpenRoundRefused(t, rt, chain.MemberReviewer)
		})
	}
}
