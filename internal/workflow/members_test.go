package workflow

import (
	"reflect"
	"testing"
)

// TestUsedActorsSkipsWhenCutBranch pins that a when the params cut hides the
// actors behind it: with the scan branch off the security actor is unused, and
// with it on the actor returns.
func TestUsedActorsSkipsWhenCutBranch(t *testing.T) {
	def := Default()

	cut, err := WithParams(def, map[string]string{"scan": "false"})
	if err != nil {
		t.Fatalf("WithParams(scan=false): %v", err)
	}
	if got, want := UsedActors(cut), []string{"builder", "lite-planner", "reviewer"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UsedActors(scan=false) = %v, want %v", got, want)
	}

	on, err := WithParams(def, map[string]string{"scan": "true"})
	if err != nil {
		t.Fatalf("WithParams(scan=true): %v", err)
	}
	if got, want := UsedActors(on), []string{"builder", "lite-planner", "reviewer", "security"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UsedActors(scan=true) = %v, want %v", got, want)
	}
}

// TestUsedActorsForEachAndElse pins the walk over a for-each and a when's else
// fallback: a for-each offers both its arms, and a when whose value has no edge
// falls to else, so the actor behind the untaken value stays unused.
func TestUsedActorsForEachAndElse(t *testing.T) {
	loop := func(goOn bool) Definition {
		return Definition{
			Name:   "loop",
			Params: map[string]Param{"go": {Kind: ParamBool, Bool: goOn}},
			Start:  "walk",
			Steps: map[string]Step{
				"walk":  {ForEach: "plans", On: map[string]Target{"next": StepTarget("build"), "empty": StepTarget("gate")}},
				"build": {Run: "builder", On: map[string]Target{"done": DoneTarget()}},
				"gate":  {When: "{{params.go}}", On: map[string]Target{"true": StepTarget("scan"), "else": DoneTarget()}},
				"scan":  {Run: "security", On: map[string]Target{"done": DoneTarget()}},
			},
		}
	}
	if got, want := UsedActors(loop(true)), []string{"builder", "security"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UsedActors(go=true) = %v, want %v", got, want)
	}
	if got, want := UsedActors(loop(false)), []string{"builder"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UsedActors(go=false) = %v, want %v (the else edge cuts scan)", got, want)
	}
}
