package workflow

import (
	"fmt"
	"testing"
)

// repeatBudgetDef is a check that repairs on red until a budget runs out, then
// finishes.
func repeatBudgetDef() Definition {
	return tdef("check", map[string]Step{
		"check": {Check: "make check", On: map[string]Target{"green": DoneTarget(), "red": StepTarget("repair")}},
		"repair": {
			Run:    "builder",
			Budget: &Budget{Max: Limit{Count: 3}, Per: Per{Steps: []string{"build"}}, Then: DoneTarget()},
			On:     map[string]Target{"done": StepTarget("check")},
		},
	})
}

// TestBudgetRedirectsOnRepeatedRedCheck pins the repeated-red rule: a budgeted
// step entered on a red check whose output repeats the previous red's counts as
// over budget, so its then target is taken rather than another repair. A red
// with room left and no repeat flag still enters the repair.
func TestBudgetRedirectsOnRepeatedRedCheck(t *testing.T) {
	t.Parallel()

	def := repeatBudgetDef()

	// The first red buys a repair.
	s, actions := Next(def, awaitingCheck("check", 1), Event{Kind: EventCheckClosed, Step: "check", Run: 1, Result: "red", Log: "L1"})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "repair" {
		t.Fatalf("first red action = %+v, want a send to repair", a)
	}

	// The repair's close runs the check again.
	s, actions = Next(def, awaitingRun("repair", "builder", 1), Event{Kind: EventStepClosed, Step: "repair", Member: "builder", Round: 1, Status: "done"})
	if a := only(t, actions); a.Kind != ActionRunCheck || a.Step != "check" {
		t.Fatalf("repair close action = %+v, want a check run", a)
	}

	// A second red that repeats the first takes the budget's then target.
	s, actions = Next(def, awaitingCheck("check", 2), Event{Kind: EventCheckClosed, Step: "check", Run: 2, Result: "red", Log: "L1", RepeatRed: true})
	if a := only(t, actions); a.Kind != ActionFinish {
		t.Fatalf("repeated red action = %+v, want finish", a)
	}
	if s.Status != StatusDone {
		t.Fatalf("status = %q, want done", s.Status)
	}
}

// TestBudgetStillRepairsOnADifferentRed pins the other half: a red whose output
// differs from the previous red, with budget left, still enters the repair.
func TestBudgetStillRepairsOnADifferentRed(t *testing.T) {
	t.Parallel()

	def := repeatBudgetDef()
	s, actions := Next(def, awaitingCheck("check", 2), Event{Kind: EventCheckClosed, Step: "check", Run: 2, Result: "red", Log: "L2"})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "repair" {
		t.Fatalf("a different red action = %+v, want a send to repair", a)
	}
	if s.Status != StatusRunning {
		t.Fatalf("status = %q, want running", s.Status)
	}
}

// noRegateDefault is the shipped default workflow with no repair budget: the
// regate param rendered to zero, so a red check has no repair round to buy and
// the budget's then target is taken on the red itself.
func noRegateDefault() Definition {
	def := Default()
	def.Params["regate"] = Param{Kind: ParamInt, Int: 0}
	return def
}

// redGateRouted reports whether an action is the one a red check with no repair
// budget may take: a halt asking for a human, or a round for the correction
// step. Anything else -- a send to a reviewing step above all -- is the silent
// pass this gate exists to close.
func redGateRouted(a Action, correction, review string) bool {
	if a.Kind == ActionHalt {
		return true
	}
	return a.Kind == ActionSend && a.Step == correction && a.Step != review
}

// TestRedCheckWithNoRegateNeverReachesReviewer pins the red-budget gate on the
// shipped workflow: with regate rendered to zero a red check buys no repair, so
// the budget's then target decides. Routing to review there would hand a failed
// gate straight to a reviewer whose pass verdict finishes the plan, so the red
// must halt or go to correct instead.
func TestRedCheckWithNoRegateNeverReachesReviewer(t *testing.T) {
	t.Parallel()

	def := noRegateDefault()
	s, actions := Start(def, StartInputs{Plans: []string{"plan-1.md"}})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "build" {
		t.Fatalf("start action = %+v, want a send to build", a)
	}

	// The plan is built and its check starts.
	s, actions = Next(def, s, Event{Kind: EventStepClosed, Step: "build", Member: "builder", Status: "done"})
	if a := only(t, actions); a.Kind != ActionRunCheck || a.Step != "check" {
		t.Fatalf("build close action = %+v, want a check run", a)
	}

	// The check is red with no repair budget behind it. The run id is the
	// caller's to fill, exactly as the chain engine fills it.
	s.Awaiting.Run = 1
	s, actions = Next(def, s, Event{Kind: EventCheckClosed, Step: "check", Run: 1, Result: "red", Log: "L1"})
	a := only(t, actions)
	if a.Kind == ActionSend && a.Step == "review" {
		t.Fatalf("a red check with no regate sent the plan to review: %+v", a)
	}
	if !redGateRouted(a, "correct", "review") {
		t.Fatalf("red action = %+v, want a halt or a send to correct", a)
	}
	if a.Kind == ActionSend && s.Results["check"].Status != "red" {
		t.Errorf("results = %+v, want the red recorded", s.Results)
	}
}

// TestRedFixCheckWithNoRegateNeverReachesFixReview pins the same gate on the
// security phase's own arm: fix-check and fix-repair mirror check and repair, so
// a red there with no repair budget halts or corrects rather than reaching a
// reviewer whose pass finishes the chain.
func TestRedFixCheckWithNoRegateNeverReachesFixReview(t *testing.T) {
	t.Parallel()

	def := noRegateDefault()
	_, actions := Next(def, awaitingCheck("fix-check", 3), Event{Kind: EventCheckClosed, Step: "fix-check", Run: 3, Result: "red", Log: "L3"})
	a := only(t, actions)
	if a.Kind == ActionSend && a.Step == "fix-review" {
		t.Fatalf("a red fix-check with no regate sent the chain to fix-review: %+v", a)
	}
	if !redGateRouted(a, "fix-correct", "fix-review") {
		t.Fatalf("red fix-check action = %+v, want a halt or a send to fix-correct", a)
	}
}

// TestGreenCheckStillReachesTheReviewer pins the arm the gate must leave alone:
// a green check goes to the reviewer whether or not a repair budget is left.
func TestGreenCheckStillReachesTheReviewer(t *testing.T) {
	t.Parallel()

	def := noRegateDefault()
	_, actions := Next(def, awaitingCheck("check", 1), Event{Kind: EventCheckClosed, Step: "check", Run: 1, Result: "green", Log: "L1"})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "review" {
		t.Fatalf("green action = %+v, want a send to review", a)
	}
}

// TestRegateStillBuysARepair pins the preserved arm: with room for a repair, a
// red check buys one and re-runs the check, and a further red buys the next one
// until the budget runs out. The gate only takes the budget's then target; it
// does not change what a budget with room buys.
func TestRegateStillBuysARepair(t *testing.T) {
	t.Parallel()

	def := Default()
	def.Params["regate"] = Param{Kind: ParamInt, Int: 2}
	s, actions := Next(def, awaitingCheck("check", 1), Event{Kind: EventCheckClosed, Step: "check", Run: 1, Result: "red", Log: "L1"})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "repair" {
		t.Fatalf("first red action = %+v, want a send to repair", a)
	}
	for run := 2; run <= 2; run++ {
		s, actions = Next(def, s, Event{Kind: EventStepClosed, Step: "repair", Member: "builder", Status: "done"})
		if a := only(t, actions); a.Kind != ActionRunCheck || a.Step != "check" {
			t.Fatalf("repair close action = %+v, want a check run", a)
		}
		s.Awaiting.Run = run
		s, actions = Next(def, s, Event{Kind: EventCheckClosed, Step: "check", Run: run, Result: "red", Log: fmt.Sprintf("L%d", run)})
		if a := only(t, actions); a.Kind != ActionSend || a.Step != "repair" {
			t.Fatalf("red run %d action = %+v, want a send to repair", run, a)
		}
	}
	// The budget is spent: the next red takes its then target, which is the arm
	// the gate rewires only when no repair is left to buy.
	s, _ = Next(def, s, Event{Kind: EventStepClosed, Step: "repair", Member: "builder", Status: "done"})
	s.Awaiting.Run = 4
	_, actions = Next(def, s, Event{Kind: EventCheckClosed, Step: "check", Run: 4, Result: "red", Log: "L4"})
	if a := only(t, actions); !redGateRouted(a, "correct", "review") {
		t.Fatalf("red past the budget action = %+v, want a halt or a send to correct", a)
	}
}
