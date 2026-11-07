package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

var errTestGit = errors.New("test git failure")

func setBindingOwner(t *testing.T, rt Runtime, name, owner string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		b.Owner = owner
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("set Owner: %v", err)
	}
}

func setBindingRemote(t *testing.T, rt Runtime, name string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		b.Builder.Mode = store.ModeRemote
		b.Builder.Server = "zen"
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("set remote: %v", err)
	}
}

func storeRoundOpen(entries []store.LogEntry, round int) bool {
	return store.RoundOpen(entries, round)
}

func clearBaselineHead(t *testing.T, rt Runtime, name string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		b.RoundClosedBaselineHead = ""
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("clear baseline: %v", err)
	}
}

func isRoundOpenError(err error) bool {
	var open *RoundOpenError
	return errors.As(err, &open)
}

// TestResumeNoCommitGoesBackToCheck pins the no-commit reroute: a halt on a
// repair arm whose tree made no commit since the check ran goes back to that
// check on a plain resume, starting a new check run and no new member round.
func TestResumeNoCommitGoesBackToCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	beforeRound := chainBinding(t, rt, "shop").Round
	beforeBaseline := chainBinding(t, rt, "shop").RoundClosedBaselineHead
	if beforeBaseline == "" {
		t.Fatal("setup: the closed round kept no baseline HEAD")
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want running: the check must rerun, not re-halt", row.Status, row.Reason)
	}
	st, err := chain.WorkflowState(row)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "check" {
		t.Fatalf("engine at %q awaiting %+v, want the check step", st.At, st.Awaiting)
	}
	if _, check := flowCheckRun(t, rt, "shop"); check.Command != "make check" {
		t.Errorf("check command = %q, want the stored gate", check.Command)
	}
	if got := chainBinding(t, rt, "shop").Round; got != beforeRound {
		t.Errorf("builder round = %d, want %d: no builder round may be sent", got, beforeRound)
	}
}

// TestResumeWithCommitKeepsArm pins the commit guard: a commit between halt
// and resume keeps the arm, so a plain resume re-halts there instead of
// re-running the check.
func TestResumeWithCommitKeepsArm(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	fg.headCommitID = "commit-after-the-work"

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume with a commit = nil, want the halt refusal")
	}
	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want it left halted on the arm", row.Status, row.Reason)
	}
	st, err := chain.WorkflowState(row)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.At != "repair" {
		t.Errorf("engine at %q, want the repair arm: the commit keeps it", st.At)
	}
}

// TestResumeExplicitFromWinsOverNoCommit pins the --from rule: a human naming
// a step is never rerouted away from it, even when the tree made no commit.
func TestResumeExplicitFromWinsOverNoCommit(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", From: "build"}); err != nil {
		t.Fatalf("ChainResume --from build: %v", err)
	}
	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.At != "build" || st.Awaiting.Member != "builder" {
		t.Errorf("engine at %q awaiting %+v, want the named step build: --from wins", st.At, st.Awaiting)
	}
}

// TestResumeGateChangeStillReroutesWithAndWithoutCommit pins today's gate
// behavior: a gate-changing resume reroutes to the check whether or not the
// tree committed.
func TestResumeGateChangeStillReroutesWithAndWithoutCommit(t *testing.T) {
	t.Parallel()

	for _, commit := range []bool{false, true} {
		name := "no-commit"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rt, fg := chainRuntime(t)
			haltAtZeroBudgetRepair(t, rt)
			if commit {
				fg.headCommitID = "commit-after-the-work"
			}

			if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "new-cmd"}); err != nil {
				t.Fatalf("ChainResume --gate: %v", err)
			}
			st, err := chain.WorkflowState(flowChainRow(t, rt))
			if err != nil {
				t.Fatalf("chain.WorkflowState: %v", err)
			}
			if st.At != "check" || st.Awaiting.Step != "check" {
				t.Errorf("engine at %q awaiting %+v, want the check step", st.At, st.Awaiting)
			}
			if _, check := flowCheckRun(t, rt, "shop"); check.Command != "new-cmd" {
				t.Errorf("check command = %q, want the new gate", check.Command)
			}
		})
	}
}

// TestResumeSeveralChecksStillRefuses pins the ambiguous predecessor rule for
// plain resumes: two checks reaching one arm still refuse, naming --from.
func TestResumeSeveralChecksStillRefuses(t *testing.T) {
	t.Parallel()

	const body = `name: twogates
inputs: { plans: optional }
params: { gate: make check, budget: 0 }
start: build
steps:
  build: { run: builder, seed: "build it", on: { done: gate-a } }
  gate-a: { check: "{{params.gate}}", on: { green: gate-b, red: arm } }
  gate-b: { check: "{{params.gate}}", on: { green: done, red: arm } }
  arm: { run: builder, seed: "fix it",
         budget: { max: "{{params.budget}}", per: build, then: gate-a },
         on: { done: gate-a } }
`
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, body)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL\n")
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusHalted) {
		t.Fatalf("chain status = %q, want the zero-budget halt at arm", got)
	}

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume = nil, want the ambiguous-predecessor refusal")
	}
	for _, want := range []string{"gate-a", "gate-b", "--from"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

// TestResumeDirtyHaltKeepsArm pins the dirty precedence: a halt over
// uncommitted work never reroutes to the check, even with no new commit, so
// the dirty recovery keeps its own refusal.
func TestResumeDirtyHaltKeepsArm(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	fg.dirtyResult = true
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) || !chainHaltedOverUncommittedWork(c.Reason) {
		t.Fatalf("setup: status = %s (%q), want halted over uncommitted work", c.Status, c.Reason)
	}
	fg.dirtyResult = false

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil || !strings.Contains(err.Error(), chainUncommittedWorkClause) {
		t.Fatalf("ChainResume = %v, want the uncommitted-work refusal, not a check rerun", err)
	}
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusHalted) {
		t.Errorf("status = %s, want it left halted on the arm", got)
	}
}

// TestResumeServedOwnerGoesBackToCheck pins the served decision: a member
// with Owner set still reroutes, since the server reads its own worktree. The
// old Owner skip would have kept the arm on the server where the incident
// happened.
func TestResumeServedOwnerGoesBackToCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	setBindingOwner(t, rt, "shop", "alice")

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume with Owner set: %v", err)
	}
	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "check" {
		t.Errorf("engine at %q awaiting %+v, want the check step: Owner does not skip", st.At, st.Awaiting)
	}
}

// TestResumeRemoteMirrorKeepsArm pins the client-mirror skip: a binding whose
// builder runs remotely has no local tree, so a plain resume re-enters the
// arm instead of checking a tree it cannot read.
func TestResumeRemoteMirrorKeepsArm(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	setBindingRemote(t, rt, "shop")

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume on a remote mirror = nil, want the halt refusal")
	}
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusHalted) {
		t.Errorf("status = %s, want it left halted on the arm", got)
	}
	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.At != "repair" {
		t.Errorf("engine at %q, want the repair arm", st.At)
	}
}

// TestResumeOpenRoundKeepsArm pins the unfinished-round guard: when the arm
// member's newest round is still open with no report, the builder never got
// its chance, so a plain resume re-enters the arm instead of checking a tree
// it never touched. Today that re-entry meets the open round and refuses, so
// the test pins the refusal rather than a check run.
func TestResumeOpenRoundKeepsArm(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startRegateFlowChain(t, rt)
	before := chainBinding(t, rt, "shop").Round
	if !storeRoundOpen(chainLog(t, rt, "shop"), before) {
		t.Fatalf("setup: round %d is not open", before)
	}
	haltFlowAt(t, rt, "repair", "repair round abandoned",
		workflow.Awaiting{Step: "repair", Member: "builder", Round: before})

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume with an open repair round = nil, want the open-round refusal")
	}
	if !isRoundOpenError(err) {
		t.Fatalf("ChainResume = %v (%T), want the open-round refusal, not a check rerun", err, err)
	}
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusHalted) {
		t.Errorf("status = %s, want it left halted on the arm", got)
	}
}

// TestResumeEmptyBaselineKeepsArm pins the unknown-baseline guard: with no
// closed-round baseline the tree cannot be compared, so the resume keeps the
// arm.
func TestResumeEmptyBaselineKeepsArm(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	clearBaselineHead(t, rt, "shop")

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume with an empty baseline = nil, want the halt refusal")
	}
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusHalted) {
		t.Errorf("status = %s, want it left halted on the arm", got)
	}
}

// TestResumeGitFailureKeepsArm pins the unreadable-tree guard: a git failure
// reads as no commit, never as a clean reroute.
func TestResumeGitFailureKeepsArm(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	fg.headCommitErr = errTestGit

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume with an unreadable tree = nil, want the halt refusal")
	}
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusHalted) {
		t.Errorf("status = %s, want it left halted on the arm", got)
	}
}

// TestResumeNoCommitKeepsArmVisits pins the visit preservation: a no-commit
// reroute keeps the arm's spent count, while a gate change resets it, so a
// repeat red meets the same budget it halted on.
func TestResumeNoCommitKeepsArmVisits(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	before, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	want := before.Visits["repair"]

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	after, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if got := after.Visits["repair"]; got != want {
		t.Errorf("visits[repair] = %d, want %d: a no-commit reroute keeps the arm", got, want)
	}

	rt2, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt2)
	if _, err := ChainResume(context.Background(), rt2, ResumeOptions{Name: "shop", Gate: "new-cmd"}); err != nil {
		t.Fatalf("ChainResume --gate: %v", err)
	}
	gated, err := chain.WorkflowState(flowChainRow(t, rt2))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if got := gated.Visits["repair"]; got != 0 {
		t.Errorf("visits[repair] = %d, want 0: a gate change keeps the fresh reset", got)
	}
}

// TestResumeNoCommitRehaltOpensNoNewRound pins the re-run: a red check after
// a no-commit reroute re-halts on the arm without opening a new repair round.
func TestResumeNoCommitRehaltOpensNoNewRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	round := chainBinding(t, rt, "shop").Round
	chainRedCheck(t, rt, "shop", "FAIL again\n")

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the repeat red to re-halt", c.Status, c.Reason)
	}
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.At != "repair" {
		t.Errorf("engine at %q, want the repair arm again", st.At)
	}
	if got := chainBinding(t, rt, "shop").Round; got != round {
		t.Errorf("builder round = %d, want %d: no new repair round", got, round)
	}
	if got := st.Visits["repair"]; got != 2 {
		t.Errorf("visits[repair] = %d, want 2: the re-run meets the kept budget", got)
	}
}

// TestResumeRegateRaisedBuysRepairRound pins the raised-budget path: with a
// commit present the resume re-enters the arm with a fresh budget, so the
// raised max buys a repair round.
func TestResumeRegateRaisedBuysRepairRound(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	fg.headCommitID = "commit-after-the-work"
	regate := 1

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Regate: &regate}); err != nil {
		t.Fatalf("ChainResume --regate 1: %v", err)
	}
	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want running: the raised budget buys a repair", row.Status, row.Reason)
	}
	st, err := chain.WorkflowState(row)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "repair" || st.Awaiting.Member != "builder" {
		t.Errorf("awaiting = %+v, want the repair round open on the builder", st.Awaiting)
	}
	if got := chainBinding(t, rt, "shop").Round; got != 2 {
		t.Errorf("builder round = %d, want 2: the repair round opened", got)
	}
}

// TestResumeExplicitFromArmOpensRepairAfterRed pins the way past a spent
// budget: after a no-commit reroute re-halts, an explicit --from arm resets
// the visits and sends the repair round again.
func TestResumeExplicitFromArmOpensRepairAfterRed(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	chainRedCheck(t, rt, "shop", "FAIL again\n")
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusHalted) {
		t.Fatalf("setup: status = %s, want the re-halt after the re-run check", got)
	}
	regate := 1

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", From: "repair", Regate: &regate}); err != nil {
		t.Fatalf("ChainResume --from repair: %v", err)
	}
	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want running: the repair round opened", row.Status, row.Reason)
	}
	st, err := chain.WorkflowState(row)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "repair" || st.Awaiting.Member != "builder" {
		t.Errorf("awaiting = %+v, want the repair round on the builder", st.Awaiting)
	}
	if got := chainBinding(t, rt, "shop").Round; got != 2 {
		t.Errorf("builder round = %d, want 2: the repair round opened", got)
	}
}

// TestHaltPayloadOnRepairArmNamesBothFroms pins the end delivery: a halt on a
// repair arm with one feeding check names --from the check and --from the arm
// with their real step names.
func TestHaltPayloadOnRepairArmNamesBothFroms(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	got := chainTerminalWFPayload(c, st, repairCheckOfRow(c, st))
	for _, want := range []string{
		"relevo chain --resume --name shop --from check",
		"--from repair",
		"`relevo done shop` closes it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("payload %q does not carry %q", got, want)
		}
	}
}

// TestEndDeliveryOnRepairArmNamesBothFroms pins the caller wire: the queued
// end delivery of a real repair-arm halt names the check's --from and the
// arm's, so the text the direct payload tests pin is what the halt queues.
func TestEndDeliveryOnRepairArmNamesBothFroms(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	entry, found, err := rt.Store.PendingForMasterMind("shop")
	if err != nil {
		t.Fatalf("PendingForMasterMind(shop): %v", err)
	}
	if !found || entry.Kind != store.KindChain {
		t.Fatalf("pending on the builder = %+v (found %v), want the halt's KindChain end delivery", entry, found)
	}
	for _, want := range []string{
		"relevo chain --resume --name shop --from check",
		"--from repair",
		"`relevo done shop` closes it",
	} {
		if !strings.Contains(entry.Payload, want) {
			t.Errorf("end delivery payload %q does not carry %q", entry.Payload, want)
		}
	}
}

// TestHaltPayloadOffRepairArmKeepsTodayText pins the fallback: a halt away
// from a repair arm names today's resume, never a --from pair.
func TestHaltPayloadOffRepairArmKeepsTodayText(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	haltFlowAt(t, rt, "review", "reviewer wants changes",
		workflow.Awaiting{Step: "review", Member: "reviewer", Round: 1})
	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	got := chainTerminalWFPayload(c, st, repairCheckOfRow(c, st))
	if !strings.Contains(got, "relevo chain --resume --name shop resumes it") {
		t.Errorf("payload %q does not carry today's resume", got)
	}
	if strings.Contains(got, "--from") {
		t.Errorf("payload %q names --from off a repair arm", got)
	}
}

// TestHaltPayloadWithTwoChecksKeepsTodayText pins the multi-check fallback: an
// arm two checks reach names no --from pair.
func TestHaltPayloadWithTwoChecksKeepsTodayText(t *testing.T) {
	t.Parallel()

	const body = `name: twogates
inputs: { plans: optional }
params: { gate: make check, budget: 0 }
start: build
steps:
  build: { run: builder, seed: "build it", on: { done: gate-a } }
  gate-a: { check: "{{params.gate}}", on: { green: gate-b, red: arm } }
  gate-b: { check: "{{params.gate}}", on: { green: done, red: arm } }
  arm: { run: builder, seed: "fix it",
         budget: { max: "{{params.budget}}", per: build, then: gate-a },
         on: { done: gate-a } }
`
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, body)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL\n")
	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	got := chainTerminalWFPayload(c, st, repairCheckOfRow(c, st))
	if strings.Contains(got, "--from") {
		t.Errorf("payload %q names --from with two feeding checks", got)
	}
}

// TestHaltPayloadNamesACustomCheckStep pins the real step names: a repair arm
// one custom-named check feeds names that check's --from and the arm's, never
// a literal "check".
func TestHaltPayloadNamesACustomCheckStep(t *testing.T) {
	t.Parallel()

	const body = `name: onegate
inputs: { plans: optional }
params: { gate: make check, budget: 0 }
start: build
steps:
  build: { run: builder, seed: "build it", on: { done: verify } }
  verify: { check: "{{params.gate}}", on: { green: done, red: fix } }
  fix: { run: builder, seed: "fix it",
         budget: { max: "{{params.budget}}", per: build, then: verify },
         on: { done: verify } }
`
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, body)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL\n")
	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the zero-budget halt", c.Status, c.Reason)
	}
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	got := chainTerminalWFPayload(c, st, repairCheckOfRow(c, st))
	for _, want := range []string{
		"relevo chain --resume --name shop --from verify",
		"--from fix",
		"`relevo done shop` closes it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("payload %q does not carry %q", got, want)
		}
	}
	if strings.Contains(got, "--from check") {
		t.Errorf("payload %q names a literal check step", got)
	}
}

// TestNoCommitCheckKeepsArmOnUnreadableCases pins every guard that keeps the
// arm: a missing run template, no matching member, a reader, an empty store,
// a missing chain, a missing binding, an empty baseline, an empty head, and
// the uncommitted-work reason falling back to the row.
func TestNoCommitCheckKeepsArmOnUnreadableCases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	newDef := func(body string) workflow.Definition {
		def, err := workflow.Parse([]byte(body))
		if err != nil {
			t.Fatalf("parse workflow: %v", err)
		}
		return def
	}
	baseBody := `name: t
inputs: { plans: optional }
params: { gate: make check }
start: build
steps:
  build: { run: builder, seed: "b", on: { done: check } }
  check: { check: "{{params.gate}}", on: { green: done, red: repair } }
  repair: { run: builder, seed: "f", budget: { max: "0", per: build, then: check }, on: { done: check } }
`

	t.Run("no run template", func(t *testing.T) {
		t.Parallel()
		rt, _ := chainRuntime(t)
		haltAtZeroBudgetRepair(t, rt)
		c := flowChainRow(t, rt)
		def := newDef(`name: t
inputs: { plans: optional }
params: { gate: make check }
start: build
steps:
  build: { run: builder, seed: "b", on: { done: check } }
  check: { check: "{{params.gate}}", on: { green: done, red: repair } }
  repair: { check: "{{params.gate}}", on: { green: done, red: done } }
`)
		st, err := chain.WorkflowState(c)
		if err != nil {
			t.Fatalf("chain.WorkflowState: %v", err)
		}
		if _, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, "check"); ok {
			t.Error("no Run template rerouted, want the arm kept")
		}
	})

	t.Run("no matching member", func(t *testing.T) {
		t.Parallel()
		rt, _ := chainRuntime(t)
		haltAtZeroBudgetRepair(t, rt)
		c := flowChainRow(t, rt)
		def := newDef(`name: t
inputs: { plans: optional }
params: { gate: make check }
start: build
steps:
  build: { run: builder, seed: "b", on: { done: check } }
  check: { check: "{{params.gate}}", on: { green: done, red: repair } }
  repair: { run: scout, seed: "f", budget: { max: "0", per: build, then: check }, on: { done: check } }
`)
		st, err := chain.WorkflowState(c)
		if err != nil {
			t.Fatalf("chain.WorkflowState: %v", err)
		}
		if _, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, "check"); ok {
			t.Error("no matching member rerouted, want the arm kept")
		}
	})

	t.Run("reader arm", func(t *testing.T) {
		t.Parallel()
		rt, _ := chainRuntime(t)
		startFlowChain(t, rt, flowReviewWorkflow)
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		c := flowChainRow(t, rt)
		def := newDef(flowReviewWorkflow)
		st, err := chain.WorkflowState(c)
		if err != nil {
			t.Fatalf("chain.WorkflowState: %v", err)
		}
		st.At = "review"
		st.Reason = "stuck"
		c.Reason = "stuck"
		if _, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, "check"); ok {
			t.Error("reader arm rerouted, want the arm kept")
		}
	})

	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		rt, _ := chainRuntime(t)
		haltAtZeroBudgetRepair(t, rt)
		c := flowChainRow(t, rt)
		def := newDef(baseBody)
		st, err := chain.WorkflowState(c)
		if err != nil {
			t.Fatalf("chain.WorkflowState: %v", err)
		}
		rt.Store = nil
		if _, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, "check"); ok {
			t.Error("nil store rerouted, want the arm kept")
		}
	})

	t.Run("missing chain", func(t *testing.T) {
		t.Parallel()
		rt, _ := chainRuntime(t)
		haltAtZeroBudgetRepair(t, rt)
		c := flowChainRow(t, rt)
		c.Name = "missing-chain"
		def := newDef(baseBody)
		st, err := chain.WorkflowState(flowChainRow(t, rt))
		if err != nil {
			t.Fatalf("chain.WorkflowState: %v", err)
		}
		if _, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, "check"); ok {
			t.Error("missing chain rerouted, want the arm kept")
		}
	})

	t.Run("empty head", func(t *testing.T) {
		t.Parallel()
		rt, fg := chainRuntime(t)
		haltAtZeroBudgetRepair(t, rt)
		c := flowChainRow(t, rt)
		def := newDef(baseBody)
		st, err := chain.WorkflowState(c)
		if err != nil {
			t.Fatalf("chain.WorkflowState: %v", err)
		}
		fg.headCommitID = ""
		if _, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, "check"); ok {
			t.Error("empty head rerouted, want the arm kept")
		}
	})

	t.Run("row reason fallback", func(t *testing.T) {
		t.Parallel()
		rt, _ := chainRuntime(t)
		haltAtZeroBudgetRepair(t, rt)
		c := flowChainRow(t, rt)
		def := newDef(baseBody)
		st, err := chain.WorkflowState(c)
		if err != nil {
			t.Fatalf("chain.WorkflowState: %v", err)
		}
		st.Reason = ""
		c.Reason = "round 1 of shop closed done with changes left uncommitted: " + chainUncommittedWorkClause
		if _, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, "check"); ok {
			t.Error("row dirty reason rerouted, want the arm kept")
		}
	})
}

// TestResumeVisitsHelperPinsFreshAndKept pins the visit helper: fresh on every
// path but a no-commit reroute, which keeps only the arm's count.
func TestResumeVisitsHelperPinsFreshAndKept(t *testing.T) {
	t.Parallel()

	before := workflow.State{At: "repair", Visits: map[string]int{"repair": 1, "check": 3}}
	if got := chainResumeVisits(before, true); got["repair"] != 1 || len(got) != 1 {
		t.Errorf("kept visits = %+v, want only repair=1", got)
	}
	if got := chainResumeVisits(before, false); len(got) != 0 {
		t.Errorf("fresh visits = %+v, want empty", got)
	}
	if got := chainResumeVisits(workflow.State{At: "repair"}, true); len(got) != 0 {
		t.Errorf("kept visits with no map = %+v, want empty", got)
	}
}
