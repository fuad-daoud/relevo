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
