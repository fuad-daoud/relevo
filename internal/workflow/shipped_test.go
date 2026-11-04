package workflow

import (
	"reflect"
	"sort"
	"strings"
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
		"gate":            {Kind: ParamString, Str: ""},
		"cleanup_round":   {Kind: ParamString, Str: ""},
		"cleanup_chain":   {Kind: ParamString, Str: ""},
		"regate":          {Kind: ParamInt, Int: 1},
		"max_corrections": {Kind: ParamInt, Int: 3},
	}
	if !reflect.DeepEqual(def.Params, wantParams) {
		t.Fatalf("params = %+v, want %+v", def.Params, wantParams)
	}
	wantSteps := []string{
		"plans", "build", "check", "repair", "review", "correct", "build-fix",
		"cleanup-round", "scan-gate", "scan", "fix-plan", "fix-build", "fix-check", "fix-repair",
		"fix-review", "fix-correct", "fix-rebuild", "fix-cleanup-round", "cleanup-chain",
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

// TestShippedSeedsAllEmbedded pins ShippedSeeds to the templates the binary
// embeds: every name it lists has its own file, and no embedded file is left
// out.
func TestShippedSeedsAllEmbedded(t *testing.T) {
	entries, err := seedFS.ReadDir("seeds")
	if err != nil {
		t.Fatalf("read embedded seeds: %v", err)
	}
	var embedded []string
	for _, e := range entries {
		embedded = append(embedded, strings.TrimSuffix(e.Name(), ".md"))
	}
	got := append([]string(nil), ShippedSeeds()...)
	sort.Strings(got)
	sort.Strings(embedded)
	if !reflect.DeepEqual(got, embedded) {
		t.Fatalf("ShippedSeeds = %v, embedded = %v", got, embedded)
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

// TestDefaultCleanupPlacement pins where the cleanup steps sit: builder closes
// flow through the round cleanup before the check, fix closes through the fix
// round cleanup before the fix check, and every edge that finished the chain
// flows through the chain cleanup first. Pointing build's done edge straight
// at check is the mutation this test catches.
func TestDefaultCleanupPlacement(t *testing.T) {
	for _, param := range []string{"cleanup_round", "cleanup_chain"} {
		p, ok := Default().Params[param]
		if !ok || p != (Param{Kind: ParamString}) {
			t.Errorf("param %s = %+v, want an empty string param", param, p)
		}
	}
	def := Default()
	edge := func(step, match string) Target {
		t.Helper()
		on, ok := def.Steps[step].On[match]
		if !ok {
			t.Fatalf("step %s has no %s edge", step, match)
		}
		return on
	}
	for _, step := range []string{"build", "repair", "build-fix"} {
		if got := edge(step, "done"); got != StepTarget("cleanup-round") {
			t.Errorf("%s done = %+v, want the round cleanup", step, got)
		}
	}
	if got := edge("cleanup-round", "done"); got != StepTarget("check") {
		t.Errorf("cleanup-round done = %+v, want check", got)
	}
	for _, step := range []string{"fix-build", "fix-rebuild", "fix-repair"} {
		if got := edge(step, "done"); got != StepTarget("fix-cleanup-round") {
			t.Errorf("%s done = %+v, want the fix round cleanup", step, got)
		}
	}
	if got := edge("fix-cleanup-round", "done"); got != StepTarget("fix-check") {
		t.Errorf("fix-cleanup-round done = %+v, want fix-check", got)
	}
	for _, tc := range [][2]string{{"scan-gate", "false"}, {"scan", "findings=0"}, {"fix-review", "verdict=pass"}} {
		if got := edge(tc[0], tc[1]); got != StepTarget("cleanup-chain") {
			t.Errorf("%s %s = %+v, want the chain cleanup", tc[0], tc[1], got)
		}
	}
	if got := edge("cleanup-chain", "done"); got != DoneTarget() {
		t.Errorf("cleanup-chain done = %+v, want done", got)
	}
	for id, step := range def.Steps {
		for match, target := range step.On {
			switch {
			case target == StepTarget("check") && id != "cleanup-round":
				t.Errorf("%s %s targets check past the round cleanup", id, match)
			case target == StepTarget("fix-check") && id != "fix-cleanup-round":
				t.Errorf("%s %s targets fix-check past the fix round cleanup", id, match)
			case target == DoneTarget() && id != "cleanup-chain":
				t.Errorf("%s %s finishes past the chain cleanup", id, match)
			}
		}
	}
	if problems := Validate(def, shippedEnv()); len(problems) != 0 {
		t.Errorf("default validates with %v, want clean", problems)
	}
}

func TestDefaultWithNoGateGoesFromBuildToReview(t *testing.T) {
	t.Parallel()

	def := Default()
	s, actions := Start(def, StartInputs{Plans: []string{"plan-1.md"}})
	if len(actions) != 1 || actions[0].Step != "build" {
		t.Fatalf("start actions = %+v, want one send to build", actions)
	}
	_, actions = Next(def, s, Event{Kind: EventStepClosed, Step: "build", Member: "builder", Status: "done"})
	if len(actions) != 1 || actions[0].Kind != ActionSend || actions[0].Step != "review" {
		t.Fatalf("build close actions = %+v, want a send to review with no check run", actions)
	}
}
