package workflow

import "testing"

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
