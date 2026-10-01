package workflow

import (
	"reflect"
	"testing"
)

func TestDefaultIsTheSpecDefault(t *testing.T) {
	def := Default()
	if def.Name != "default" || def.Start != "plans" {
		t.Fatalf("name, start = %q, %q", def.Name, def.Start)
	}
	if want := (Inputs{Plans: InputRequired, Task: InputNone}); def.Inputs != want {
		t.Fatalf("inputs = %+v, want %+v", def.Inputs, want)
	}
	wantParams := map[string]Param{
		"builder":         {Kind: ParamString, Str: "builder"},
		"reviewer":        {Kind: ParamString, Str: "reviewer"},
		"planner":         {Kind: ParamString, Str: "lite-planner"},
		"security":        {Kind: ParamString, Str: "security"},
		"scan":            {Kind: ParamBool, Bool: true},
		"gate":            {Kind: ParamString, Str: "make check"},
		"regate":          {Kind: ParamInt, Int: 1},
		"max_corrections": {Kind: ParamInt, Int: 3},
	}
	if !reflect.DeepEqual(def.Params, wantParams) {
		t.Fatalf("params = %+v, want %+v", def.Params, wantParams)
	}
	wantSteps := []string{
		"plans", "build", "check", "repair", "review", "correct", "build-fix",
		"scan-gate", "scan", "fix-plan", "fix-build", "fix-check", "fix-repair",
		"fix-review", "fix-correct", "fix-rebuild",
	}
	for _, name := range wantSteps {
		if _, ok := def.Steps[name]; !ok {
			t.Fatalf("missing step %q", name)
		}
	}
	if len(def.Steps) != len(wantSteps) {
		t.Fatalf("steps = %d, want %d", len(def.Steps), len(wantSteps))
	}
}

func TestShippedSeeds(t *testing.T) {
	want := []string{"repair", "review", "correct", "scan", "fix"}
	if got := ShippedSeeds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ShippedSeeds = %v, want %v", got, want)
	}
}

func TestDefaultCheckRoutesGreenToReviewAndRedToRepair(t *testing.T) {
	def := Default()
	if got, want := def.Steps["check"].On, map[string]Target{
		"green": StepTarget("review"),
		"red":   StepTarget("repair"),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("check on = %+v, want %+v", got, want)
	}
	if got, want := def.Steps["fix-check"].On, map[string]Target{
		"green": StepTarget("fix-review"),
		"red":   StepTarget("fix-repair"),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fix-check on = %+v, want %+v", got, want)
	}
}
