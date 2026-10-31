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
// and halts the member. It returns the member as the halt left it: NEEDS YOU
// with the pid kept and the process still running, since switchBuilder's
// resolve failure runs before its closeOld kill.
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
	member, _, ok := chain.Awaited(rt.Store, chainStoredRow(t, rt, "shop"))
	if !ok || member != "shop-rev" {
		t.Fatalf("awaited member = %q (ok=%v), want shop-rev", member, ok)
	}

	revAtHalt := chainBinding(t, rt, "shop-rev")
	if revAtHalt.State != store.StateNeedsYou || revAtHalt.Builder.PID == 0 {
		t.Fatalf("at the halt: state = %q pid = %d, want needs_you with a process kept",
			revAtHalt.State, revAtHalt.Builder.PID)
	}
	if !alivePID(rt, revAtHalt.Builder.PID) {
		t.Fatalf("at the halt: pid %d answers dead, want the round's process alive", revAtHalt.Builder.PID)
	}
	return revAtHalt
}

// alivePID reports whether the fake runner answers alive for pid.
func alivePID(rt Runtime, pid int) bool {
	return rt.Runner.(*fakeRunner).answerFor(pid)
}

// assertPIDKilled reports the fake runner's kill record for pid: Stop ends a
// live process on the way, and the record is the only proof it was told to.
func assertPIDKilled(t *testing.T, rt Runtime, pid int) {
	t.Helper()

	for _, h := range rt.Runner.(*fakeRunner).kills {
		if h.PID == pid {
			return
		}
	}
	t.Errorf("no kill recorded for pid %d, want the close to have ended the halted round's process", pid)
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

	// ChainResume with no flags closes the halted member's round and re-runs the
	// review: the human asking to resume is the same ask the refusal's
	// `relevo stop` made, so the close needs no manual stop first.
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
	assertPIDKilled(t, rt, revAtHalt.Builder.PID)
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
	assertPIDKilled(t, rt, revAtHalt.Builder.PID)
}

// TestChainResumeClosesTheHaltedMemberRoundWithALiveProcess pins the state arm
// of the rule on its own: NEEDS YOU is the state the halt writes, so the round
// is the human's to end and a resume ends it -- whatever pid it kept and
// whether that process is still running.
func TestChainResumeClosesTheHaltedMemberRoundWithALiveProcess(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	revAtHalt := gatedReviewerHalt(t, rt)
	revBefore := chainBinding(t, rt, "shop-rev")

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	revAfter := chainBinding(t, rt, "shop-rev")
	if revAfter.Round <= revBefore.Round {
		t.Errorf("reviewer round = %d, want past %d", revAfter.Round, revBefore.Round)
	}
	assertPIDKilled(t, rt, revAtHalt.Builder.PID)
}

// TestChainResumeRefusesLiveProcess pins the half of the rule that survives
// the state arm: a live process on a member that is NOT needs_you is not
// evidence of death and no halt left it for the human, so it is left to the
// resume's own refusal whatever state it is in.
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
}

// TestChainResumeRefusesAPidlessMemberOutsideNeedsYou pins the pidless half of
// the rule: with no process to probe there is no evidence the round died, and
// outside NEEDS YOU no halt ever left it for the human, so any other state
// stays a refusal.
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

// TestChainResumeProcessDeathAndProbeError pins the two process facts past the
// state arm: a pid whose process is gone is closed whatever state the member is
// in, and a probe that errors is an OS hiccup, not evidence of death.
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
		// The round's process has since exited, so its pid answers dead.
		rt.Runner.(*fakeRunner).script(revAtHalt.Builder.PID, false)
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
		revAtHalt := gatedReviewerHalt(t, rt)
		rt.Runner.(*fakeRunner).script(revAtHalt.Builder.PID, false)

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

		// Past the state arm the probe is the only evidence, so an active
		// member is what makes a probe error visible at all.
		mutateReviewer(t, rt, func(b store.Binding) store.Binding {
			b.State = store.StateActive
			return b
		})
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
