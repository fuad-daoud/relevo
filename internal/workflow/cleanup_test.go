package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCleanupStepParsesAndRoundTrips(t *testing.T) {
	def := mustParse(t, `{"steps": {"c": {"cleanup": "make clean", "on": {"done": "done"}}}}`)
	step := def.Steps["c"]
	if step.Cleanup != "make clean" {
		t.Fatalf("cleanup = %q, want the command", step.Cleanup)
	}
	if want := []string{"cleanup"}; !reflect.DeepEqual(step.Kinds(), want) {
		t.Fatalf("kinds = %v, want %v", step.Kinds(), want)
	}
	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"cleanup":"make clean"`) {
		t.Fatalf("marshalled step = %s, want the cleanup key", data)
	}
	def2 := mustParse(t, `{"name": "x", "start": "c", "steps": {"c": {"cleanup": "make clean", "on": {"done": "done"}}}}`)
	data2, err := json.Marshal(def2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	def3, err := Parse(data2)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if !reflect.DeepEqual(def2, def3) {
		t.Fatalf("round trip changed the definition:\n before %#v\n after  %#v", def2, def3)
	}
}

func TestCleanupKindsFollowTheFixedOrder(t *testing.T) {
	def := mustParse(t, `{"steps": {"a": {"check": "x", "cleanup": "y", "on": {"done": "done"}}}}`)
	if want := []string{"check", "cleanup"}; !reflect.DeepEqual(def.Steps["a"].Kinds(), want) {
		t.Fatalf("kinds = %v, want %v", def.Steps["a"].Kinds(), want)
	}
}

func TestCleanupEnterEmptySkipsWithNoAction(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Cleanup: "{{params.gate}}", On: map[string]Target{"done": StepTarget("g")}},
		"g": {Run: "builder"},
	})
	def.Params = map[string]Param{"gate": {Kind: ParamString, Str: ""}}
	s, actions := Start(def, StartInputs{})
	a := only(t, actions)
	if a.Kind != ActionSend || a.Step != "g" {
		t.Fatalf("action = %+v, want a send to g past the empty cleanup", a)
	}
	if _, ok := s.Results["c"]; ok {
		t.Fatalf("the empty cleanup recorded a result: %+v", s.Results["c"])
	}
}

func TestCleanupEnterEmptyRouteConsumesTheWalk(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Cleanup: "{{params.gate}}", On: map[string]Target{"done": StepTarget("c")}},
	})
	def.Params = map[string]Param{"gate": {Kind: ParamString, Str: ""}}
	s, actions := Start(def, StartInputs{})
	if s.Status != StatusHalted {
		t.Fatalf("status = %q, want halted", s.Status)
	}
	if !strings.Contains(s.Reason, "control walk exceeded") {
		t.Fatalf("reason = %q, want the cap reason", s.Reason)
	}
	if a := only(t, actions); a.Kind != ActionHalt {
		t.Fatalf("action = %+v, want a halt", a)
	}
}

func TestCleanupEnterNonEmptyRunsACheck(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Cleanup: "rm -rf {{params.dir}}", On: map[string]Target{"done": DoneTarget()}},
	})
	def.Params = map[string]Param{"dir": {Kind: ParamString, Str: "tmp"}}
	s, actions := Start(def, StartInputs{})
	a := only(t, actions)
	if a.Kind != ActionRunCheck || a.Step != "c" || a.Command != "rm -rf tmp" {
		t.Fatalf("action = %+v, want the rendered run_check for c", a)
	}
	if s.Awaiting.Step != "c" || s.Awaiting.Member != "" {
		t.Fatalf("awaiting = %+v, want a check-shaped wait on c", s.Awaiting)
	}
}

func TestCleanupCloseRoutesDoneOnAnyExit(t *testing.T) {
	// The red edge is deliberately not validated: it pins that the engine
	// ignores the exit code even when a red edge is present.
	def := tdef("c", map[string]Step{
		"c": {Cleanup: "make clean", On: map[string]Target{"done": StepTarget("g"), "red": StepTarget("trap")}},
		"g": {Run: "builder"},
	})
	for _, result := range []string{"green", "red", "boom"} {
		s := awaitingCheck("c", 4)
		s, _ = Next(def, s, Event{Kind: EventCheckClosed, Step: "c", Run: 4, Result: result, Log: "log-4"})
		if s.At != "g" {
			t.Fatalf("result %s: at = %q, want g", result, s.At)
		}
		if got := s.Results["c"]; got.Status != "done" {
			t.Fatalf("result %s: recorded status = %q, want done", result, got.Status)
		}
		if got := s.Results["c"].Artifacts["log"]; len(got) != 1 || got[0] != "log-4" {
			t.Fatalf("result %s: log = %v, want the check-shaped log", result, got)
		}
	}
}

func TestCleanupCloseFallsBackToElse(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Cleanup: "make clean", On: map[string]Target{"else": StepTarget("g")}},
		"g": {Run: "builder"},
	})
	s := awaitingCheck("c", 4)
	s, _ = Next(def, s, Event{Kind: EventCheckClosed, Step: "c", Run: 4, Result: "red", Log: "x"})
	if s.At != "g" {
		t.Fatalf("at = %q, want g via else", s.At)
	}
}

func TestCleanupRedCloseGatesNoPass(t *testing.T) {
	def := tdef("c", map[string]Step{
		"c": {Cleanup: "make clean", On: map[string]Target{"done": StepTarget("r")}},
		"r": {Run: "builder", On: map[string]Target{"done": DoneTarget()}},
	})
	s := awaitingCheck("c", 1)
	s, _ = Next(def, s, Event{Kind: EventCheckClosed, Step: "c", Run: 1, Result: "red", Log: "x"})
	if gate, ok := gatingRedCheck(def, s, "r"); ok {
		t.Fatalf("a red cleanup close gates %q, want no gate", gate)
	}
}

func TestValidateCleanupRejectsNonDoneMatches(t *testing.T) {
	env := shippedEnv()
	cases := []struct {
		name string
		yaml string
		rule string
	}{
		{"a green match is not valid", "name: sample\nstart: c\nsteps:\n  c: { cleanup: make clean, on: { green: done, done: done } }", RuleMatch},
		{"no done and no else is uncovered", "name: sample\nstart: c\nsteps:\n  c: { cleanup: make clean, on: { else: done } }", ""},
	}
	for _, tc := range cases {
		problems := Validate(mustParse(t, tc.yaml), env)
		if tc.rule == "" {
			if hasProblem(problems, RuleMatch, "c") {
				t.Fatalf("%s: Validate = %v, want an else to cover the step", tc.name, problems)
			}
			continue
		}
		if !hasProblem(problems, tc.rule, "c") {
			t.Fatalf("%s: Validate = %v, want %s at c", tc.name, problems, tc.rule)
		}
	}
	missing := Validate(mustParse(t, "name: sample\nstart: c\nsteps:\n  c: { cleanup: make clean, on: { green: done } }"), env)
	if !hasProblem(missing, RuleMatch, "c") {
		t.Fatalf("a cleanup with neither done nor else reports %v, want a rule 3 problem", missing)
	}
	empty := Validate(mustParse(t, "name: sample\nstart: c\nsteps:\n  c: { cleanup: make clean }"), env)
	if !hasProblem(empty, RuleMatch, "c") {
		t.Fatalf("a cleanup with no on reports %v, want a rule 3 problem", empty)
	}
}

func TestValidateCleanupReadsParamsOnly(t *testing.T) {
	env := shippedEnv()
	def := mustParse(t, "name: sample\nstart: c\nsteps:\n  c: { cleanup: \"{{task}}\", on: { done: done } }")
	if problems := Validate(def, env); !hasProblem(problems, RuleKind, "c") {
		t.Fatalf("a cleanup reading the task input reports %v, want a rule 2 problem", problems)
	}
	ok := mustParse(t, "name: sample\nparams: { gate: \"x\" }\nstart: c\nsteps:\n  c: { cleanup: \"{{params.gate}}\", on: { done: d } }\n  d: { run: builder, seed: \"{{c.log}}\", on: { done: done } }")
	if problems := Validate(ok, shippedEnv()); len(problems) != 0 {
		t.Fatalf("a params-only cleanup with a log reader reports %v, want clean", problems)
	}
}

func TestBoundedCyclesCountsCleanupLikeCheck(t *testing.T) {
	rejected := mustParse(t, `
name: sample
params: { flag: true }
start: w
steps:
  w: { when: "{{params.flag}}", on: { true: c, false: done } }
  c: { cleanup: make clean, on: { done: w } }
`)
	cycles := boundedCycles(rejected)
	if len(cycles) == 0 {
		t.Fatal("a when/cleanup cycle reports no cycle, want it rejected like a check")
	}
	accepted := mustParse(t, `
name: sample
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: c, empty: done } }
  c: { cleanup: make clean, on: { done: plans } }
`)
	if cycles := boundedCycles(accepted); len(cycles) != 0 {
		t.Fatalf("a for-each/cleanup loop reports %v, want it accepted like a check", cycles)
	}
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"cleanup", `
name: sample
start: a
steps:
  a: { cleanup: make clean, on: { done: b } }
  b: { cleanup: make clean, on: { done: a } }
`},
		{"check", `
name: sample
start: a
steps:
  a: { check: make check, on: { green: b, red: b } }
  b: { check: make check, on: { green: a, red: a } }
`},
	} {
		cycles := boundedCycles(mustParse(t, tc.yaml))
		if len(cycles) != 1 || cycles[0].Detail != "an unbounded cycle: no for-each and no budget" {
			t.Fatalf("%s-only cycle reports %v, want one unbounded cycle like a check", tc.name, cycles)
		}
	}
}

func TestLegacyMapsCleanupStepsToBuilding(t *testing.T) {
	def := Default()
	for _, at := range []string{"cleanup-a", "cleanup-gate-log"} {
		s := State{Status: StatusRunning, At: at, Visits: map[string]int{}, Iter: map[string]Iter{"plans": {Index: 0, Items: []string{"p"}}}}
		got := LegacyView(def, s)
		if got.Phase != "build" || got.Step != "building" || got.AwaitingMember != "" {
			t.Errorf("LegacyView(%s) = %q/%q/%q, want build/building with no actor",
				at, got.Phase, got.Step, got.AwaitingMember)
		}
	}
	if got := legacyStepWord("cleanup-x"); got != legacyStepBuilding {
		t.Errorf("legacyStepWord(cleanup-x) = %q, want building", got)
	}
}

func TestPruneEmptyCleanupDropsEmptySteps(t *testing.T) {
	def := mustParse(t, `
name: sample
params: { gate: "", dir: "tmp", cleanup_tag: "v1" }
start: a
steps:
  a: { run: builder, on: { done: c1 } }
  c1: { cleanup: "{{params.gate}}", on: { done: c2 } }
  c2: { cleanup: "rm {{params.dir}}", on: { done: done } }
`)
	got := PruneEmptyCleanup(def)
	if _, ok := got.Steps["c1"]; ok {
		t.Fatal("the empty cleanup survived the prune")
	}
	if _, ok := got.Steps["c2"]; !ok {
		t.Fatal("the set cleanup was pruned, want it kept")
	}
	if got.Steps["a"].On["done"] != StepTarget("c2") {
		t.Fatalf("a's done edge = %+v, want it retargeted past the empty cleanup", got.Steps["a"].On["done"])
	}
	if _, ok := got.Params["cleanup_tag"]; !ok {
		t.Fatal("a cleanup_tag param was dropped while a cleanup remains")
	}
}

func TestPruneEmptyCleanupDropsParamsWhenAllEmpty(t *testing.T) {
	def := mustParse(t, `
name: sample
params: { gate: "", cleanup_tag: "v1" }
start: a
steps:
  a: { run: builder, on: { done: c1 } }
  c1: { cleanup: "{{params.gate}}", on: { done: done } }
`)
	got := PruneEmptyCleanup(def)
	if _, ok := got.Steps["c1"]; ok {
		t.Fatal("the empty cleanup survived the prune")
	}
	if got.Steps["a"].On["done"] != DoneTarget() {
		t.Fatalf("a's done edge = %+v, want done past the pruned cleanup", got.Steps["a"].On["done"])
	}
	if _, ok := got.Params["cleanup_tag"]; ok {
		t.Fatal("the cleanup_tag param survived a prune with no cleanup left")
	}
}

func TestPruneEmptyCleanupMatchesTheShippedShape(t *testing.T) {
	pruned := PruneEmptyCleanup(Default())
	frozen := mustParse(t, frozenDefaultYAML)
	prunedData, err := json.Marshal(pruned)
	if err != nil {
		t.Fatalf("marshal pruned default: %v", err)
	}
	frozenData, err := json.Marshal(frozen)
	if err != nil {
		t.Fatalf("marshal frozen default: %v", err)
	}
	if string(prunedData) != string(frozenData) {
		t.Fatalf("pruned default differs from the frozen shape:\n got %s\nwant %s", prunedData, frozenData)
	}
	for id, step := range pruned.Steps {
		if step.Cleanup != "" {
			t.Fatalf("pruned default keeps a cleanup step %q", id)
		}
	}
	for name := range pruned.Params {
		if strings.HasPrefix(name, "cleanup_") {
			t.Fatalf("pruned default keeps param %q", name)
		}
	}
}

// frozenDefaultYAML is today's shipped default, frozen so the prune test pins
// the stored shape a chain with no cleanup keeps.
const frozenDefaultYAML = `name: default
description: plans built, checked and reviewed, then one security scan
inputs:
  plans: required          # none | optional | required
  task: none
params:                    # the only values a CLI flag changes
  builder: builder
  reviewer: reviewer
  planner: lite-planner
  security: security
  scan: true
  gate: ""                 # no check; a repo without one still runs
  regate: 1
  max_corrections: 3
start: plans
steps:
  plans:      { for-each: plans, on: { next: build, empty: scan-gate } }
  build:      { run: "{{params.builder}}", seed: "{{plans.current}}", on: { done: check } }
  check:      { check: "{{params.gate}}", on: { green: review, red: repair } }
  repair:     { run: "{{params.builder}}", seed: shipped:repair,
                budget: { max: "{{params.regate}}", per: [build, build-fix], then: review },
                on: { done: check } }
  review:     { run: "{{params.reviewer}}", seed: shipped:review,
                on: { verdict=pass: plans, verdict=changes: correct } }
  correct:    { run: "{{params.planner}}", seed: shipped:correct,
                budget: { max: "{{params.max_corrections}}", per: plans,
                          then: { halt: "reviewer still wants changes after {{params.max_corrections}} corrections" } },
                on: { done: build-fix } }
  build-fix:  { run: "{{params.builder}}", seed: "{{correct.plan}}", on: { done: check } }
  scan-gate:  { when: "{{params.scan}}", on: { true: scan, false: done } }
  scan:       { run: "{{params.security}}", seed: shipped:scan,
                on: { findings=0: done, findings>0: fix-plan } }
  fix-plan:   { run: "{{params.planner}}", seed: shipped:fix, on: { done: fix-build } }
  fix-build:  { run: "{{params.builder}}", seed: "{{fix-plan.plan}}", on: { done: fix-check } }
  fix-check:  { check: "{{params.gate}}", on: { green: fix-review, red: fix-repair } }
  fix-repair: { run: "{{params.builder}}", seed: shipped:repair,
                budget: { max: "{{params.regate}}", per: [fix-build, fix-rebuild], then: fix-review },
                on: { done: fix-check } }
  fix-review: { run: "{{params.reviewer}}", seed: shipped:review,
                on: { verdict=pass: done, verdict=changes: fix-correct } }
  fix-correct: { run: "{{params.planner}}", seed: shipped:correct,
                 budget: { max: "{{params.max_corrections}}", per: fix-plan,
                           then: { halt: "reviewer still wants changes after {{params.max_corrections}} corrections" } },
                 on: { done: fix-rebuild } }
  fix-rebuild: { run: "{{params.builder}}", seed: "{{fix-correct.plan}}", on: { done: fix-check } }
`
