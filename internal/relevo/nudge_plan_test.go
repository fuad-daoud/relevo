package relevo

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// exitedChainReader starts a workflow chain, advances it to the step whose
// reader is member, and leaves that reader's process exited cleanly with its
// marker down and text as its final message.
func exitedChainReader(t *testing.T, body, advanceStep, advanceMember, member, text string) (Runtime, store.Binding, *fakeRunner) {
	t.Helper()
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, body)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: advanceStep, Member: advanceMember, Round: 1, Status: "done"})
	b, err := rt.Store.Load(member)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, b.Name, b.Round, text)
	if err := os.WriteFile(rt.Store.DonePath(b.Name, b.Round), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(rt.Store.DonePath(b.Name, b.Round), old, old); err != nil {
		t.Fatal(err)
	}
	return at(rt, 3*time.Hour), b, fr
}

func planWriterReader(t *testing.T, text string) (Runtime, store.Binding, *fakeRunner) {
	t.Helper()
	return exitedChainReader(t, flowPlanWorkflow, "build", "builder", "shop-lite-planner", text)
}

func TestPlanWritingReaderWithoutStepsIsNudgedOnce(t *testing.T) {
	t.Parallel()
	rt, b, fr := planWriterReader(t, "The plan is at /tmp/plan.md.")

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) < 2 {
		t.Fatalf("specs = %d, want the resume after the start", len(fr.specs))
	}
	if got.Builder.PID != fr.handles[len(fr.handles)-1].PID {
		t.Errorf("PID = %d, want the resumed handle", got.Builder.PID)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active: the round is running again", got.State)
	}
	if _, err := os.Stat(rt.Store.DonePath(b.Name, b.Round)); err == nil {
		t.Error("marker still on disk after the nudge: the round would close on it at once")
	}
	notes := switchNotes(t, rt, b.Name)
	if len(notes) != 1 {
		t.Fatalf("switch notes = %v, want exactly one nudge", notes)
	}
}

func TestPlanWritingReaderNudgePromptSaysPlanIsFinalMessage(t *testing.T) {
	t.Parallel()
	rt, b, fr := planWriterReader(t, "The plan is at /tmp/plan.md.")
	before := len(fr.specs)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != before+1 {
		t.Fatalf("specs = %d, want %d", len(fr.specs), before+1)
	}
	resume := fr.specs[before].Argv
	if !anyArgContains(resume, planNudgeNote) {
		t.Errorf("resume argv = %v, want the plan sentence %q", resume, planNudgeNote)
	}
	if !anyArgContains(resume, "you stopped before your deliverable") {
		t.Errorf("resume argv = %v, want the generic continuation text too", resume)
	}
}

func TestPlanWritingReaderStillWithoutStepsAfterNudgeClosesForTheChainToHalt(t *testing.T) {
	t.Parallel()
	rt, b, fr := planWriterReader(t, "The plan is at /tmp/plan.md.\n\nrelevo\nstatus: done\n")

	next, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	before := len(fr.specs)
	fr.script(next.Builder.PID, false)
	fr.exit(next.Builder.PID, 0)
	if err := os.WriteFile(rt.Store.DonePath(next.Name, next.Round), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(rt.Store.DonePath(next.Name, next.Round), old, old); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, next)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != before {
		t.Errorf("specs = %d, want %d: a second nudge was sent", len(fr.specs), before)
	}
	if got.Builder.PID != 0 {
		t.Errorf("PID = %d, want the process cleared: the round closed", got.Builder.PID)
	}
	if c := flowChainRow(t, rt); c.Status != "halted" {
		t.Errorf("chain status = %q, want halted on the plan with no steps", c.Status)
	}
}

func TestReviewerWithoutStepsIsNotNudgedForPlanSteps(t *testing.T) {
	t.Parallel()
	rt, b, fr := exitedChainReader(t, flowReviewWorkflow, "build", "builder", "shop-assistant",
		"Looks fine.\n\n```relevo\nstatus: done\nverdict: pass\n```\n")
	before := len(fr.specs)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != before {
		t.Errorf("specs = %d, want %d: only plan-writing steps are gated on steps", len(fr.specs), before)
	}
	if notes := switchNotes(t, rt, b.Name); len(notes) != 0 {
		t.Errorf("switch notes = %v, want none", notes)
	}
}

func TestPlanWritingReaderWithStepsClosesWithoutNudge(t *testing.T) {
	t.Parallel()
	rt, b, fr := planWriterReader(t, "1. Fix the thing\n2. Test it\n\n```relevo\nstatus: done\n```\n")
	before := len(fr.specs)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != before {
		t.Errorf("specs = %d, want %d: a plan with steps needs no nudge", len(fr.specs), before)
	}
}

func TestPlanWritingReaderExitedWithoutMarkerIsNudgedOnce(t *testing.T) {
	t.Parallel()
	rt, b, fr := planWriterReader(t, "The plan is at /tmp/plan.md.\n\n```relevo\nstatus: done\n```\n")
	if err := os.Remove(rt.Store.DonePath(b.Name, b.Round)); err != nil {
		t.Fatal(err)
	}

	next, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) < 2 || !argvMentions(fr.specs[len(fr.specs)-1].Argv, planNudgeNote) {
		t.Fatalf("specs = %d, want a resume whose prompt carries the plan note", len(fr.specs))
	}
	if next.State != store.StateActive || next.Builder.PID != fr.handles[len(fr.handles)-1].PID {
		t.Errorf("state = %s pid = %d, want active on the resumed handle", next.State, next.Builder.PID)
	}
	if notes := switchNotes(t, rt, b.Name); len(notes) != 1 {
		t.Errorf("switch notes = %v, want exactly one nudge", notes)
	}
	if c := flowChainRow(t, rt); c.Status != "running" {
		t.Errorf("chain status = %q, want running while the planner is nudged", c.Status)
	}

	// The resumed runner announces its session, so only the one-nudge limit can
	// stop a second resume.
	next.Builder.StreamSessionID = "S2"
	if err := rt.Store.Save(next); err != nil {
		t.Fatal(err)
	}
	before := len(fr.specs)
	fr.script(next.Builder.PID, false)
	fr.exit(next.Builder.PID, 0)
	if _, err := reconcile(t, rt, next); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != before {
		t.Errorf("specs = %d, want %d: the one nudge is spent", len(fr.specs), before)
	}
}

// argvMentions reports whether any argument contains s, for a prompt passed as
// one argument among the flags.
func argvMentions(argv []string, s string) bool {
	for _, a := range argv {
		if strings.Contains(a, s) {
			return true
		}
	}
	return false
}
