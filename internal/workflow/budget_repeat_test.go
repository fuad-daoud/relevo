package workflow

import (
	"fmt"
	"strings"
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

// TestPassAfterRedNeedsATraceVisibleOverride pins the override's own half: a
// reviewer may pass over a red check, but only by recording why, and the value
// must be readable from every record the trace keeps -- the state, the encoded
// event, and the action with its reason.
func TestPassAfterRedNeedsATraceVisibleOverride(t *testing.T) {
	t.Parallel()

	def := tdef("check", map[string]Step{
		"check":  {Check: "make check", On: map[string]Target{"green": DoneTarget(), "red": StepTarget("review")}},
		"review": {Run: "reviewer", On: map[string]Target{"verdict=pass": DoneTarget(), "verdict=changes": DoneTarget()}},
	})
	s, _ := Next(def, awaitingCheck("check", 1), Event{Kind: EventCheckClosed, Step: "check", Run: 1, Result: "red", Log: "L1"})
	pass := Event{Kind: EventStepClosed, Step: "review", Member: "reviewer", Status: "done",
		Outcomes: map[string]string{"verdict": "pass"}, Override: "the failure is in the vendored fixture, not the plan"}

	next, actions := Next(def, s, pass)
	if next.Status != StatusDone {
		t.Fatalf("status = %q, want done: the override answered the gate", next.Status)
	}
	a := only(t, actions)
	if a.Kind != ActionFinish {
		t.Fatalf("action = %+v, want finish", a)
	}
	const why = "the failure is in the vendored fixture, not the plan"
	if got := next.Results["review"].Override; got != why {
		t.Errorf("results override = %q, want %q", got, why)
	}
	if a.Override != why {
		t.Errorf("action override = %q, want %q", a.Override, why)
	}
	if !strings.Contains(a.Reason, why) {
		t.Errorf("action reason = %q, want it to name the override", a.Reason)
	}

	// The same close without the override halts, and says which check is red.
	plain := pass
	plain.Override = ""
	halted, actions := Next(def, s, plain)
	if halted.Status != StatusHalted {
		t.Fatalf("status = %q, want halted without an override", halted.Status)
	}
	if !strings.Contains(halted.Reason, "check") {
		t.Errorf("halt reason = %q, want it to name the red check", halted.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt {
		t.Fatalf("action = %+v, want a halt", a)
	}

	// A verdict of changes is not a pass: it routes on with the red unread.
	changes := Event{Kind: EventStepClosed, Step: "review", Member: "reviewer", Status: "done",
		Outcomes: map[string]string{"verdict": "changes"}}
	if next, _ := Next(def, s, changes); next.Status != StatusDone {
		t.Errorf("changes after a red check: status = %q, want done", next.Status)
	}
}

// advance feeds one event to Next and asserts the single action it produced is
// the kind and step the walk expects.
func advance(t *testing.T, def Definition, s State, e Event, wantKind ActionKind, wantStep string) State {
	t.Helper()
	s, actions := Next(def, s, e)
	a := only(t, actions)
	if a.Kind != wantKind || a.Step != wantStep {
		t.Fatalf("%s action = %+v, want a %s to %s", e.Step, a, wantKind, wantStep)
	}
	return s
}

// checkRun feeds a check result and asserts the send it routed to, using the
// check's run id the way the chain engine fills it in.
func checkRun(t *testing.T, def Definition, s State, step string, run int, result, send string) State {
	t.Helper()
	s.Awaiting.Run = run
	e := Event{Kind: EventCheckClosed, Step: step, Run: run, Result: result, Log: fmt.Sprintf("L%d", run)}
	return advance(t, def, s, e, ActionSend, send)
}

// stepClose feeds a run close for a step whose actor is the given member, and
// asserts the send it routed to.
func stepClose(t *testing.T, def Definition, s State, step, member string, outcomes map[string]string, send string) State {
	t.Helper()
	e := Event{Kind: EventStepClosed, Step: step, Member: member, Status: "done", Outcomes: outcomes}
	return advance(t, def, s, e, ActionSend, send)
}

// firstPhaseReview walks the shipped default with one plan up to the first
// reviewer's send: the plan is built, its check goes red twice, and the single
// repair round the budget buys runs out on the second red, so the run reaches
// the reviewer with the gate still red.
func firstPhaseReview(t *testing.T, def Definition, s State) State {
	t.Helper()
	e := Event{Kind: EventStepClosed, Step: "build", Member: "builder", Round: 0, Status: "done"}
	s = advance(t, def, s, e, ActionRunCheck, "check")
	s = checkRun(t, def, s, "check", 1, "red", "repair")
	s = advance(t, def, s, Event{Kind: EventStepClosed, Step: "repair", Member: "builder", Status: "done"}, ActionRunCheck, "check")
	return checkRun(t, def, s, "check", 2, "red", "review")
}

// securityPhaseReview walks on from the first reviewer's send into the security
// phase, up to the security phase's own fix-check running.
func securityPhaseReview(t *testing.T, def Definition, s State) State {
	t.Helper()
	pass := Event{Kind: EventStepClosed, Step: "review", Member: "reviewer", Status: "done",
		Outcomes: map[string]string{"verdict": "pass"}, Override: "the failure is in the vendored fixture"}
	s = advance(t, def, s, pass, ActionSend, "scan")
	s = stepClose(t, def, s, "scan", "security", map[string]string{"findings": "1"}, "fix-plan")
	s = stepClose(t, def, s, "fix-plan", "lite-planner", nil, "fix-build")
	e := Event{Kind: EventStepClosed, Step: "fix-build", Member: "builder", Status: "done"}
	return advance(t, def, s, e, ActionRunCheck, "fix-check")
}

// fixReviewState walks the shipped default through a whole review phase and
// into the security phase, up to the fix-review send. Its fixCheck argument is
// what the security phase's own check reports: green reaches fix-review
// directly, red buys one fix-repair round and reaches it over that budget.
//
// The first phase's check is left recorded red, answered by the reviewer's
// override: the reviewer passed over it, so the entry is stale by the time the
// security phase runs its own gate.
func fixReviewState(t *testing.T, fixCheck string) (Definition, State) {
	t.Helper()
	def := Default()
	s, actions := Start(def, StartInputs{Plans: []string{"plan-1.md"}})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "build" {
		t.Fatalf("start action = %+v, want a send to build", a)
	}
	s = firstPhaseReview(t, def, s)
	s = securityPhaseReview(t, def, s)
	if fixCheck == "green" {
		return def, checkRun(t, def, s, "fix-check", 3, "green", "fix-review")
	}
	s = checkRun(t, def, s, "fix-check", 3, "red", "fix-repair")
	repair := Event{Kind: EventStepClosed, Step: "fix-repair", Member: "builder", Status: "done"}
	s = advance(t, def, s, repair, ActionRunCheck, "fix-check")
	return def, checkRun(t, def, s, "fix-check", 4, "red", "fix-review")
}

// TestCrossPhasePassAfterRedAdvancesOnItsOwnGreenCheck pins the gate's scope
// across two review phases: a red the first reviewer already answered on an
// override is stale, so it must not gate a later pass whose own check is green,
// and a red that is still unanswered gates that pass and names itself.
func TestCrossPhasePassAfterRedAdvancesOnItsOwnGreenCheck(t *testing.T) {
	t.Parallel()

	// A green security check: the only red left in the state was answered, so
	// the reviewer's bare pass advances the run.
	def, green := fixReviewState(t, "green")
	if got := green.Results["check"].Status; got != "red" {
		t.Fatalf("check result = %q, want the red the reviewer overrode", got)
	}
	pass := Event{Kind: EventStepClosed, Step: "fix-review", Member: "reviewer", Status: "done",
		Outcomes: map[string]string{"verdict": "pass"}}
	next, actions := Next(def, green, pass)
	if next.Status != StatusDone {
		t.Fatalf("status = %q, reason = %q, want done: fix-check is green", next.Status, next.Reason)
	}
	if a := only(t, actions); a.Kind != ActionFinish {
		t.Fatalf("action = %+v, want finish", a)
	}

	// A red security check is unanswered, so the same bare pass halts on it --
	// naming that check, not the stale one from the first phase.
	def, red := fixReviewState(t, "red")
	halted, actions := Next(def, red, pass)
	if halted.Status != StatusHalted {
		t.Fatalf("status = %q, want halted on the red fix-check", halted.Status)
	}
	if !strings.Contains(halted.Reason, "check fix-check is red") {
		t.Fatalf("halt reason = %q, want it to name fix-check", halted.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt {
		t.Fatalf("action = %+v, want a halt", a)
	}

	// The other narrowing: a red the closing step never stood behind. The two
	// checks here sit on separate branches, so the red on the branch this pass
	// did not come through is not in front of it.
	branched := tdef("left-check", map[string]Step{
		"left-check":   {Check: "make check", On: map[string]Target{"red": StepTarget("left-review"), "green": DoneTarget()}},
		"left-review":  {Run: "reviewer", On: map[string]Target{"verdict=pass": DoneTarget()}},
		"right-check":  {Check: "make check", On: map[string]Target{"red": StepTarget("right-review"), "green": DoneTarget()}},
		"right-review": {Run: "reviewer", On: map[string]Target{"verdict=pass": DoneTarget()}},
	})
	s := awaitingRun("right-review", "reviewer", 1)
	s.Results["left-check"] = Result{Status: "red"}
	closed := Event{Kind: EventStepClosed, Step: "right-review", Member: "reviewer", Round: 1, Status: "done",
		Outcomes: map[string]string{"verdict": "pass"}}
	if next, actions := Next(branched, s, closed); next.Status != StatusDone {
		t.Fatalf("status = %q, reason = %q, want done: the red is on the other branch", next.Status, next.Reason)
	} else if a := only(t, actions); a.Kind != ActionFinish {
		t.Fatalf("action = %+v, want finish", a)
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
	// The budget is spent: the red still reaches the reviewer, exactly as
	// before the gate, and the reviewer is the one who cannot pass it.
	s, actions = Next(def, s, Event{Kind: EventStepClosed, Step: "repair", Member: "builder", Status: "done"})
	if a := only(t, actions); a.Kind != ActionRunCheck || a.Step != "check" {
		t.Fatalf("last repair close action = %+v, want a check run", a)
	}
	s.Awaiting.Run = 4
	s, actions = Next(def, s, Event{Kind: EventCheckClosed, Step: "check", Run: 4, Result: "red", Log: "L4"})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "review" {
		t.Fatalf("red past the budget action = %+v, want a send to review", a)
	}

	// The reviewer's pass then halts: a red check is not passed by a verdict.
	_, actions = Next(def, s, Event{Kind: EventStepClosed, Step: "review", Member: "reviewer", Status: "done",
		Outcomes: map[string]string{"verdict": "pass"}})
	if a := only(t, actions); a.Kind != ActionHalt {
		t.Fatalf("pass after a red check = %+v, want a halt", a)
	}
}
