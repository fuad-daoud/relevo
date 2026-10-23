package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

func TestPlanHasStepsAcceptsNumberedList(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"1. Edit the seeds\n",
		"Plan:\n  2) do the thing\n",
		"## Step 1\nedit it",
		"STEP 3: finish",
	} {
		if !planHasSteps([]byte(body)) {
			t.Errorf("planHasSteps(%q) = false, want true", body)
		}
	}
}

func TestPlanHasStepsRejectsPathPointerSentence(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"The plan is at /tmp/plan.md.",
		"The plan is at /tmp/plan.md.\n\nRead it.\n",
		"- only one bullet\n",
	} {
		if planHasSteps([]byte(body)) {
			t.Errorf("planHasSteps(%q) = true, want false", body)
		}
	}
}

func TestPlanHasStepsRejectsEmptyText(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"", " \n\t\n  "} {
		if planHasSteps([]byte(body)) {
			t.Errorf("planHasSteps(%q) = true, want false", body)
		}
	}
}

func TestPlanHasStepsAcceptsABulletedPlan(t *testing.T) {
	t.Parallel()
	if !planHasSteps([]byte("- edit a\n* edit b\n")) {
		t.Error("two bullets must count as a plan")
	}
}

func TestChainStepWritesPlanForCorrectAndFixOnly(t *testing.T) {
	t.Parallel()
	def := workflow.Definition{Steps: map[string]workflow.Step{
		"correct": {Seed: "shipped:correct"},
		"fix":     {Seed: "shipped:fix"},
		"review":  {Seed: "shipped:review"},
		"build":   {Seed: "{{plans.current}}"},
	}}
	for step, want := range map[string]bool{"correct": true, "fix": true, "review": false, "build": false, "missing": false} {
		if got := chainStepWritesPlan(def, step); got != want {
			t.Errorf("chainStepWritesPlan(%q) = %v, want %v", step, got, want)
		}
	}
}
