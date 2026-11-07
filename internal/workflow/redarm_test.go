package workflow

import (
	"strings"
	"testing"
)

// TestZeroBudgetReasonNamesTheResumeFlags pins the halt wording: it names the
// flags that change the run rather than telling a reader to rerun the check by
// hand, and it names no chain -- the workflow knows none.
func TestZeroBudgetReasonNamesTheResumeFlags(t *testing.T) {
	t.Parallel()

	b := &Budget{Max: Limit{Ref: "params.regate"}, Then: StepTarget("review")}
	got := zeroBudgetReason("repair", b)
	for _, want := range []string{"repair", "--gate", "--regate", "no budget left"} {
		if !strings.Contains(got, want) {
			t.Errorf("halt reason %q does not name %q", got, want)
		}
	}
	if strings.Contains(got, "rerun the check by hand") {
		t.Errorf("halt reason %q still tells a reader to rerun the check by hand", got)
	}
	if strings.Contains(got, "relevo chain") {
		t.Errorf("halt reason %q names a chain, which the workflow does not know", got)
	}
}

// TestRedArmChecksFindsTheChecksBehindAnArm pins the lookup the resume asks:
// the check whose red edge names the arm, for each of the shipped arms, and
// nothing for a step no check routes to.
func TestRedArmChecksFindsTheChecksBehindAnArm(t *testing.T) {
	t.Parallel()

	def := Default()
	for _, tt := range []struct{ arm, want string }{
		{"repair", "check"},
		{"fix-repair", "fix-check"},
	} {
		got := RedArmChecks(def, tt.arm)
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("RedArmChecks(%q) = %v, want [%s]", tt.arm, got, tt.want)
		}
	}
	if got := RedArmChecks(def, "review"); len(got) != 0 {
		t.Errorf("RedArmChecks(review) = %v, want none: no check routes a red verdict there", got)
	}
	if got := RedArmChecks(def, "nope"); len(got) != 0 {
		t.Errorf("RedArmChecks(nope) = %v, want none", got)
	}
}

// TestRedArmChecksOnACustomWorkflowNamesItsOwnCheck pins the same rule on a
// workflow whose steps are not the shipped names: C's red edge names R, so R's
// arm is C.
func TestRedArmChecksOnACustomWorkflowNamesItsOwnCheck(t *testing.T) {
	t.Parallel()

	def := mustParse(t, `name: custom
inputs: { plans: required }
params: { budget: 0 }
start: build
steps:
  build: { run: builder, on: { done: C } }
  C: { check: "true", on: { green: done, red: R } }
  R: { run: builder, budget: { max: "{{params.budget}}", per: build, then: C }, on: { done: C } }
`)
	got := RedArmChecks(def, "R")
	if len(got) != 1 || got[0] != "C" {
		t.Errorf("RedArmChecks(R) = %v, want [C]", got)
	}
	if !IsZeroBudgetArm(def, "R") {
		t.Error("IsZeroBudgetArm(R) = false, want true: budget renders to 0 and C routes red there")
	}
}

// TestGatingRedCheckNamesTheCheckAPassStoodOn pins the red-gate read: a
// reviewer passing while the check is still red halts, and the halted state
// names that check.
func TestGatingRedCheckNamesTheCheckAPassStoodOn(t *testing.T) {
	t.Parallel()

	def := gatedDefault()
	s, actions := Start(def, StartInputs{Plans: []string{"plan-1.md"}})
	if a := only(t, actions); a.Kind != ActionSend || a.Step != "build" {
		t.Fatalf("start action = %+v, want a send to build", a)
	}
	s = firstPhaseReview(t, def, s)
	pass := Event{Kind: EventStepClosed, Step: "review", Member: "reviewer", Status: "done",
		Outcomes: map[string]string{"verdict": "pass"}}
	halted, actions := Next(def, s, pass)
	if halted.Status != StatusHalted {
		t.Fatalf("status = %q, want halted on the pass over a red check", halted.Status)
	}
	if a := only(t, actions); a.Kind != ActionHalt {
		t.Fatalf("action = %+v, want a halt", a)
	}
	if got := GatingRedCheck(def, halted); got != "check" {
		t.Errorf("GatingRedCheck = %q, want check: the pass stood on its red", got)
	}
}

// TestGatingRedCheckEmptyWithoutARedGate pins every shape that is not a
// pass-while-red halt: a green check, an override answering the red, a close
// that took no pass edge, a halt on the correction step whose own done close
// is not a pass, and a run that never halted.
func TestGatingRedCheckEmptyWithoutARedGate(t *testing.T) {
	t.Parallel()

	def := tdef("check", map[string]Step{
		"check":   {Check: "make check", On: map[string]Target{"green": StepTarget("review"), "red": StepTarget("repair")}},
		"repair":  {Run: "builder", On: map[string]Target{"done": StepTarget("check")}},
		"review":  {Run: "reviewer", On: map[string]Target{"verdict=pass": DoneTarget(), "verdict=changes": StepTarget("correct")}},
		"correct": {Run: "planner", On: map[string]Target{"done": StepTarget("repair")}},
	})
	pass := Result{Status: "done", Outcomes: map[string]string{"verdict": "pass"}}
	haltedAt := func(at string, results map[string]Result) State {
		return State{Status: StatusHalted, At: at, Reason: at + " halted", Results: results}
	}

	for _, tt := range []struct {
		name  string
		state State
	}{
		{"green check", haltedAt("review", map[string]Result{
			"check": {Status: "green"}, "review": pass})},
		{"recorded override", haltedAt("review", map[string]Result{
			"check":  {Status: "red"},
			"review": {Status: "done", Outcomes: map[string]string{"verdict": "pass"}, Override: "the fixture is at fault"}})},
		{"changes close", haltedAt("review", map[string]Result{
			"check":  {Status: "red"},
			"review": {Status: "done", Outcomes: map[string]string{"verdict": "changes"}}})},
		{"correction halt", haltedAt("correct", map[string]Result{
			"check":   {Status: "red"},
			"review":  {Status: "done", Outcomes: map[string]string{"verdict": "changes"}},
			"correct": {Status: "done"}})},
		{"no recorded close", haltedAt("review", map[string]Result{
			"check": {Status: "red"}})},
		{"running", State{Status: StatusRunning, At: "review", Results: map[string]Result{
			"check": {Status: "red"}, "review": pass}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := GatingRedCheck(def, tt.state); got != "" {
				t.Errorf("GatingRedCheck = %q, want empty: %s is not a pass-while-red halt", got, tt.name)
			}
		})
	}
}

// TestGatingRedCheckEmptyWithTwoGates pins the ambiguity rule: two red checks
// in front of the passed step name no single gate, so the halt keeps today's
// routing.
func TestGatingRedCheckEmptyWithTwoGates(t *testing.T) {
	t.Parallel()

	def := tdef("left", map[string]Step{
		"left":   {Check: "make check", On: map[string]Target{"green": StepTarget("review"), "red": StepTarget("review")}},
		"right":  {Check: "make check", On: map[string]Target{"green": StepTarget("review"), "red": StepTarget("review")}},
		"review": {Run: "reviewer", On: map[string]Target{"verdict=pass": DoneTarget()}},
	})
	st := State{Status: StatusHalted, At: "review", Reason: "review halted", Results: map[string]Result{
		"left":   {Status: "red"},
		"right":  {Status: "red"},
		"review": {Status: "done", Outcomes: map[string]string{"verdict": "pass"}},
	}}
	if got := GatingRedCheck(def, st); got != "" {
		t.Errorf("GatingRedCheck = %q, want empty with two gating checks", got)
	}
}

// TestIsZeroBudgetArmSpellsOutTheRule pins the three ways a step is not the
// halt's arm: no budget at all, a budget with a round left, and a budget whose
// then target is not a step.
func TestIsZeroBudgetArmSpellsOutTheRule(t *testing.T) {
	t.Parallel()

	if IsZeroBudgetArm(Default(), "repair") {
		t.Error("IsZeroBudgetArm(repair) on the shipped default = true, want false: its budget renders to 1")
	}
	def := mustParse(t, `name: custom
inputs: { plans: required }
params: { budget: 0 }
start: build
steps:
  build: { run: builder, on: { done: C } }
  C: { check: "true", on: { green: done, red: R } }
  R: { run: builder, budget: { max: "{{params.budget}}", per: build, then: { halt: "done for" } }, on: { done: C } }
  plain: { run: builder, on: { done: C } }
`)
	if IsZeroBudgetArm(def, "R") {
		t.Error("IsZeroBudgetArm(R) with a halt then-target = true, want false")
	}
	if IsZeroBudgetArm(def, "plain") {
		t.Error("IsZeroBudgetArm(plain) = true, want false: it carries no budget")
	}
}
