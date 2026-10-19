package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// haltFlowAt plants a halted chain row: the state at a named step with the
// reason it halted there, and the row itself halted so the resume's own status
// refusal lets it through. The planted halts stand for walks that would take
// several rounds to reach on their own; the check that follows is a pure read
// of the stored definition, so the shape is the whole of what is under test.
func haltFlowAt(t *testing.T, rt Runtime, at, reason string, awaiting workflow.Awaiting) db.ChainRow {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		st, err := chainWorkflowState(c)
		if err != nil {
			return err
		}
		st.Status = workflow.StatusHalted
		st.At = at
		st.Reason = reason
		st.Awaiting = awaiting
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		c.StateJSON = raw
		c.Status = string(workflow.StatusHalted)
		c.Reason = reason
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("halt shop at %s: %v", at, err)
	}
	return flowChainRow(t, rt)
}

// haltAtZeroBudgetRepair drives a chain on the shipped workflow to the halt the
// reroute exists for: a red check with regate at zero halts at repair, because
// the arm's budget buys no round. The gate is a command so the check really
// runs, and the scan is off so the walk stops inside the plan.
func haltAtZeroBudgetRepair(t *testing.T, rt Runtime) {
	t.Helper()
	startNoRegateFlowChain(t, rt)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL the gate\n")
	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("chain status = %q (%q), want the zero-budget halt", c.Status, c.Reason)
	}
	st, err := chainWorkflowState(c)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.At != "repair" {
		t.Fatalf("halted at %q, want the repair arm", st.At)
	}
}

// TestResumeWithANewGateRerunsTheHaltedArmsCheck pins the whole reroute: a
// chain halted at a zero-budget repair arm, resumed with a new gate, runs the
// check again under that command instead of halting on the very budget it had.
// The mutation it kills is the resume's own --from: pass at opts.From, which
// re-enters the arm and re-halts.
func TestResumeWithANewGateRerunsTheHaltedArmsCheck(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		opts ResumeOptions
		// wantAwait is the step the resumed chain parks on. --no-gate clears
		// the check command, and an empty command is green with no run at all,
		// so that one walks past the check rather than awaiting it: the
		// reroute is the same, the gate it reran under is simply empty.
		wantAwait string
	}{
		{"--gate", ResumeOptions{Name: "shop", Gate: "new-cmd"}, "check"},
		{"--no-gate", ResumeOptions{Name: "shop", NoGate: true}, "review"},
		{"--param gate", ResumeOptions{Name: "shop", Params: map[string]string{"gate": "new-cmd"}}, "check"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt, _ := chainRuntime(t)
			haltAtZeroBudgetRepair(t, rt)

			if _, err := ChainResume(context.Background(), rt, tt.opts); err != nil {
				t.Fatalf("ChainResume(%+v): %v", tt.opts, err)
			}

			row := flowChainRow(t, rt)
			if row.Status != string(workflow.StatusRunning) {
				t.Fatalf("status = %q (%q), want running: the check must rerun, not re-halt", row.Status, row.Reason)
			}
			st, err := chainWorkflowState(row)
			if err != nil {
				t.Fatalf("chainWorkflowState: %v", err)
			}
			if st.Awaiting.Step != tt.wantAwait {
				t.Errorf("engine at %q awaiting %+v, want it on %q", st.At, st.Awaiting, tt.wantAwait)
			}
			if tt.wantAwait != "check" {
				return
			}
			run, check := flowCheckRun(t, rt, "shop")
			if check.Command != "new-cmd" {
				t.Errorf("check run %d command = %q, want the new gate", run, check.Command)
			}
		})
	}
}

// TestResumeWithANewGateOnTheFixArmRerunsFixCheck pins the same reroute on the
// shipped workflow's other arm: a halt at fix-repair, whose budget is the same
// zero, goes back to fix-check rather than re-entering the arm.
func TestResumeWithANewGateOnTheFixArmRerunsFixCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	haltFlowAt(t, rt, "fix-repair", "fix-repair has no budget left ({{params.regate}} renders to 0)",
		workflow.Awaiting{Step: "fix-repair", Member: "builder", Round: 1})

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "new-cmd"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.At != "fix-check" || st.Awaiting.Step != "fix-check" {
		t.Errorf("engine at %q awaiting %+v, want the fix-check step", st.At, st.Awaiting)
	}
}

// TestResumeRerouteOnACustomWorkflowNamesItsOwnCheck pins the rule on a
// workflow whose steps are not the shipped names: C's red edge names R, so a
// halt at R with a zero budget resumes at C.
func TestResumeRerouteOnACustomWorkflowNamesItsOwnCheck(t *testing.T) {
	t.Parallel()

	const body = `name: customflow
inputs: { plans: optional }
params: { gate: make check, budget: 0 }
start: build
steps:
  build: { run: builder, seed: "build it", on: { done: guard } }
  guard: { check: "{{params.gate}}", on: { green: review, red: arm } }
  review: { run: reviewer, seed: "review it", on: { verdict=pass: done, verdict=changes: done } }
  arm: { run: builder, seed: "fix it",
         budget: { max: "{{params.budget}}", per: build, then: guard },
         on: { done: guard } }
`
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, body)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL\n")

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("chain status = %q (%q), want the zero-budget halt at arm", row.Status, row.Reason)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "new-cmd"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.At != "guard" {
		t.Errorf("engine at %q, want guard: the check whose red edge named arm", st.At)
	}
}

// TestResumeFromWinsOverTheGateReroute pins the --from rule: a human naming a
// step is never rerouted away from it, however the gate was named.
func TestResumeFromWinsOverTheGateReroute(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		opts ResumeOptions
	}{
		{"--from", ResumeOptions{Name: "shop", Gate: "new-cmd", From: "build"}},
		{"--from with --param gate", ResumeOptions{Name: "shop", Params: map[string]string{"gate": "new-cmd"}, From: "build"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt, _ := chainRuntime(t)
			haltAtZeroBudgetRepair(t, rt)

			if _, err := ChainResume(context.Background(), rt, tt.opts); err != nil {
				t.Fatalf("ChainResume(%+v): %v", tt.opts, err)
			}
			st, err := chainWorkflowState(flowChainRow(t, rt))
			if err != nil {
				t.Fatalf("chainWorkflowState: %v", err)
			}
			if st.At != "build" || st.Awaiting.Member != "builder" {
				t.Errorf("engine at %q awaiting %+v, want the named step build: --from wins", st.At, st.Awaiting)
			}
		})
	}
}

// TestResumeGateChangeOffARedArmDoesNotReroute pins the case where there is no
// arm to reroute: a chain halted at review with a new gate resumes exactly at
// review, is not refused, and stores the gate for the checks still to come.
func TestResumeGateChangeOffARedArmDoesNotReroute(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	haltFlowAt(t, rt, "review", "reviewer wants changes",
		workflow.Awaiting{Step: "review", Member: "reviewer", Round: 1})

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "new-cmd"}); err != nil {
		t.Fatalf("ChainResume off a red arm: %v", err)
	}
	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %q (%q), want running", row.Status, row.Reason)
	}
	st, err := chainWorkflowState(row)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.At != "review" {
		t.Errorf("engine at %q, want review: no reroute off a red arm", st.At)
	}
	if got := storedFlowParam(t, row, "gate"); got != "new-cmd" {
		t.Errorf("stored gate = %q, want the new one to apply to future checks", got)
	}
}

// TestResumeWithoutAGateChangeStaysHaltedAndRefused pins the answer to a
// resume that changes nothing: the chain stays halted, the reason still travels
// with the message, and the error is a refusal -- never a plain error, which
// the CLI would code internal and send a reader to file a bug on a row they can
// simply fix.
func TestResumeWithoutAGateChangeStaysHaltedAndRefused(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtZeroBudgetRepair(t, rt)
	reason := flowChainRow(t, rt).Reason

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume with no gate change = nil, want the halt refusal")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("ChainResume = %v, want ErrRefused: a plain error is coded internal by the CLI", err)
	}
	if !strings.Contains(err.Error(), "no budget left") {
		t.Errorf("refusal %q does not carry the halt reason", err)
	}
	if row := flowChainRow(t, rt); row.Status != string(workflow.StatusHalted) || row.Reason != reason {
		t.Errorf("chain = %s (%q), want it left halted with its own reason", row.Status, row.Reason)
	}
}

// TestResumeRerouteRefusesAnAmbiguousPredecessor pins the refusal a reroute
// makes when the arm's predecessor is not exactly one check: nothing is
// guessed, and the refusal is a refusal rather than an internal failure.
func TestResumeRerouteRefusesAnAmbiguousPredecessor(t *testing.T) {
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
		t.Fatalf("chain status = %q, want the zero-budget halt at R", got)
	}

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "new-cmd"})
	if err == nil {
		t.Fatal("ChainResume = nil, want the ambiguous-predecessor refusal")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("ChainResume = %v, want ErrRefused", err)
	}
	for _, want := range []string{"gate-a", "gate-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name the check %q it would have had to choose", err, want)
		}
	}
	if row := flowChainRow(t, rt); row.Status != string(workflow.StatusHalted) {
		t.Errorf("chain status = %q, want the refusal to have changed nothing", row.Status)
	}
}

// TestChainResumeChangesGateCountsTheParam pins the one definition of a gate
// change: the --param that fills the same slot as --gate counts, which is what
// the reroute and the staged-prompt walkback both read.
func TestChainResumeChangesGateCountsTheParam(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		opts ResumeOptions
		want bool
	}{
		{"--gate", ResumeOptions{Gate: "x"}, true},
		{"--no-gate", ResumeOptions{NoGate: true}, true},
		{"--param gate", ResumeOptions{Params: map[string]string{"gate": "x"}}, true},
		{"--param other", ResumeOptions{Params: map[string]string{"regate": "1"}}, false},
		{"nothing", ResumeOptions{}, false},
	} {
		if got := chainResumeChangesGate(tt.opts); got != tt.want {
			t.Errorf("chainResumeChangesGate(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestResumeStagedTextWalksBackOnAParamGate pins the walkback's use of the
// same definition. A repair prompt's bytes name the check that failed, so a
// resume that replaced the gate must not re-hand them, and the earlier prompt
// the walkback lands on is not a repair plan either, so nothing is re-handed.
func TestResumeStagedTextWalksBackOnAParamGate(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL the gate\n")

	// The halt's own state awaits the check, so the walkback's premise -- a
	// builder round whose staged bytes are a repair plan -- is planted here
	// rather than driven: what is under test is which prompt the flag decides
	// to re-hand, not the walk that got to it.
	row := haltFlowAt(t, rt, "repair", "repair has no budget left",
		workflow.Awaiting{Step: "repair", Member: "builder", Round: 2})
	before, err := chainWorkflowState(row)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if before.Awaiting.Member != "builder" || before.Awaiting.Round != 2 {
		t.Fatalf("awaiting %+v, want the builder on round 2", before.Awaiting)
	}
	// The repair round's staged bytes, and the seed one round back that the
	// walkback lands on when the gate was replaced.
	if err := writeStagedPrompt(rt, "shop", before.Awaiting.Round, repairPlanPrefix+"2\n\nfix it\n"); err != nil {
		t.Fatalf("stage the repair prompt: %v", err)
	}
	if err := writeStagedPrompt(rt, "shop", before.Awaiting.Round-1, "work on the plan\n"); err != nil {
		t.Fatalf("stage the earlier seed prompt: %v", err)
	}

	act := workflow.Action{Kind: workflow.ActionSend, Step: before.At, Actor: before.Awaiting.Member}
	var text string
	var ok bool
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, text, ok = chainResumeStagedText(rt, tx, row, before, act, true)
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if ok {
		t.Fatalf("chainResumeStagedText = (%q, ok), want no staged text: the walk-back landed on a prompt that is not a repair plan", text)
	}
	// The same call without the gate change hands back the repair round's own
	// bytes, which is what makes the walkback -- and not the flag test -- the
	// thing under test.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, text, ok = chainResumeStagedText(rt, tx, row, before, act, false)
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !ok || text != repairPlanPrefix+"2\n\nfix it\n" {
		t.Errorf("unchanged-gate re-send = (%v, %q), want the repair round's own prompt", ok, text)
	}
	_ = chainResumeChangesGate(ResumeOptions{Params: map[string]string{"gate": "x"}})
}

// writeStagedPrompt plants the bytes a round's staged prompt file carries.
func writeStagedPrompt(rt Runtime, name string, round int, body string) error {
	return os.WriteFile(rt.Store.PromptPath(name, round), []byte(body), 0o644)
}

// startRegateFlowChain starts the shipped workflow with one repair round to
// spend and walks it to the repair arm: the build closes, the check goes red,
// and the repair round opens on the builder.
func startRegateFlowChain(t *testing.T, rt Runtime) {
	t.Helper()
	body, err := json.Marshal(workflow.Default())
	if err != nil {
		t.Fatalf("encode the shipped workflow: %v", err)
	}
	startedChain(t, rt, ChainOptions{
		Workflow: writeWorkflowFile(t, string(body)),
		Params:   map[string]string{"regate": "1", "gate": "make check", "scan": "false"},
	})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL the gate\n")
	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.At != "repair" || st.Awaiting.Member != "builder" {
		t.Fatalf("engine at %q awaiting %+v, want the repair round open on the builder", st.At, st.Awaiting)
	}
}

// builderRound is the round counter of shop's builder binding.
func builderRound(t *testing.T, rt Runtime) int {
	t.Helper()
	b, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("Load shop: %v", err)
	}
	return b.Round
}

// TestResumeWithANewGateRerunsAStoppedRepairsCheck pins the stopped half of the
// reroute: a repair round stopped mid-flight and resumed with a new gate starts
// a check run under that command and sends no builder round, rather than
// re-sending the chain's build plan.
func TestResumeWithANewGateRerunsAStoppedRepairsCheck(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		opts ResumeOptions
	}{
		{"--gate", ResumeOptions{Name: "shop", Gate: "new-cmd"}},
		{"--param gate", ResumeOptions{Name: "shop", Params: map[string]string{"gate": "new-cmd"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt, _ := chainRuntime(t)
			startRegateFlowChain(t, rt)
			if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
				t.Fatalf("ChainStop: %v", err)
			}
			rounds := builderRound(t, rt)

			if _, err := ChainResume(context.Background(), rt, tt.opts); err != nil {
				t.Fatalf("ChainResume(%+v): %v", tt.opts, err)
			}
			row := flowChainRow(t, rt)
			if row.Status != string(workflow.StatusRunning) {
				t.Fatalf("status = %q (%q), want running", row.Status, row.Reason)
			}
			st, err := chainWorkflowState(row)
			if err != nil {
				t.Fatalf("chainWorkflowState: %v", err)
			}
			if st.Awaiting.Step != "check" {
				t.Fatalf("engine at %q awaiting %+v, want the check step", st.At, st.Awaiting)
			}
			if _, check := flowCheckRun(t, rt, "shop"); check.Command != "new-cmd" {
				t.Errorf("check command = %q, want the new gate", check.Command)
			}
			if got := builderRound(t, rt); got != rounds {
				t.Errorf("builder round = %d, want %d: no builder round may be sent", got, rounds)
			}
		})
	}
}

// TestResumeWithANewGateRerunsAHaltedRepairWithBudgetLeft pins that the reroute
// is not tied to a spent budget: a halt at a repair arm that still has a round
// left, resumed with a new gate, runs the check again.
func TestResumeWithANewGateRerunsAHaltedRepairWithBudgetLeft(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startRegateFlowChain(t, rt)
	haltFlowAt(t, rt, "repair", "the repair round was abandoned",
		workflow.Awaiting{Step: "repair", Member: "builder", Round: 2})

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "new-cmd"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "check" {
		t.Errorf("engine at %q awaiting %+v, want the check step", st.At, st.Awaiting)
	}
}

// TestResumeStagedTextYieldsNothingWhenTheWalkBackLandsOffARepairPlan pins the
// guard on the walk-back: when it leaves the awaited round for a prompt that is
// not a repair plan, that prompt is not the step's own and is not re-sent.
func TestResumeStagedTextYieldsNothingWhenTheWalkBackLandsOffARepairPlan(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startRegateFlowChain(t, rt)
	row := flowChainRow(t, rt)
	before, err := chainWorkflowState(row)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	round := before.Awaiting.Round
	if err := writeStagedPrompt(rt, "shop", round, repairPlanPrefix+"2\n\nfix it\n"); err != nil {
		t.Fatalf("stage the repair prompt: %v", err)
	}
	if err := writeStagedPrompt(rt, "shop", round-1, "the build plan\n"); err != nil {
		t.Fatalf("stage the build plan: %v", err)
	}
	act := workflow.Action{Kind: workflow.ActionSend, Step: before.At, Actor: before.Awaiting.Member}
	var ok bool
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, _, ok = chainResumeStagedText(rt, tx, row, before, act, true)
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if ok {
		t.Error("chainResumeStagedText = ok after a walk-back onto the build plan, want no staged text")
	}
}

// TestResumeFromWinsOverTheGateOnAStoppedRepair pins --from on a stopped chain:
// the named step is entered even though a new gate would otherwise reroute.
func TestResumeFromWinsOverTheGateOnAStoppedRepair(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startRegateFlowChain(t, rt)
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "new-cmd", From: "build"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.At != "build" || st.Awaiting.Member != "builder" {
		t.Errorf("engine at %q awaiting %+v, want the named step build", st.At, st.Awaiting)
	}
}
