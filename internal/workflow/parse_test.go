package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const everyFieldJSON = `{
  "name": "sample",
  "description": "a sample workflow",
  "inputs": {"plans": "required", "task": "optional"},
  "params": {"builder": "builder", "regate": 2, "scan": true},
  "start": "plans",
  "steps": {
    "plans": {"for-each": "plans", "on": {"next": "build", "empty": "done"}},
    "build": {"run": "{{params.builder}}", "seed": "{{plans.current}}", "on": {"done": "check"}},
    "check": {"check": "make check", "on": {"green": "done", "red": {"halt": "check red"}}},
    "gate": {"when": "{{params.scan}}", "on": {"true": "build", "false": "done"}},
    "split": {"fork": {"each": "plans", "workflow": "default"}, "on": {"joined": "done", "conflict": {"halt": "merge"}}},
    "merge": {"fork": {"children": [{"workflow": "default", "plans": ["r1.md"], "task": "audit"}]}, "on": {"joined": "done"}},
    "repair": {"run": "{{params.builder}}", "seed": "shipped:repair", "budget": {"max": "{{params.regate}}", "per": ["build", "check"], "then": "done"}, "on": {"done": "done"}}
  }
}`

const sampleYAML = `
name: sample
description: a sample workflow
inputs:
  plans: required
  task: optional
params:
  builder: builder
  regate: 2
  scan: true
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: "{{params.builder}}", seed: "{{plans.current}}", on: { done: check } }
  check: { check: "make check", on: { green: done, red: { halt: "check red" } } }
  gate: { when: "{{params.scan}}", on: { true: build, false: done } }
  split: { fork: { each: plans, workflow: default }, on: { joined: done, conflict: { halt: merge } } }
  merge: { fork: { children: [{ workflow: default, plans: [r1.md], task: audit }] }, on: { joined: done } }
  repair: { run: "{{params.builder}}", seed: shipped:repair, budget: { max: "{{params.regate}}", per: [build, check], then: done }, on: { done: done } }
`

const triageFirstYAML = `
name: triage-first
inputs: { task: required }
start: triage
steps:
  triage: { run: yes-no, seed: "Should we build this? {{task}}",
            on: { answer=yes: build, answer=no: { halt: "triage said no" } } }
  build:  { run: builder, seed: "{{task}}", on: { done: check } }
  check:  { check: "make check", on: { green: done, red: { halt: "check red" } } }
`

func mustParse(t *testing.T, data string) Definition {
	t.Helper()
	def, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return def
}

func TestParseJSONReadsEveryField(t *testing.T) {
	def := mustParse(t, everyFieldJSON)
	if def.Name != "sample" || def.Description != "a sample workflow" || def.Start != "plans" {
		t.Fatalf("header = %q, %q, %q", def.Name, def.Description, def.Start)
	}
	if want := (Inputs{Plans: InputRequired, Task: InputOptional}); def.Inputs != want {
		t.Fatalf("inputs = %+v, want %+v", def.Inputs, want)
	}
	if want := (Param{Kind: ParamString, Str: "builder"}); def.Params["builder"] != want {
		t.Fatalf("builder = %+v, want %+v", def.Params["builder"], want)
	}
	if want := (Param{Kind: ParamInt, Int: 2}); def.Params["regate"] != want {
		t.Fatalf("regate = %+v, want %+v", def.Params["regate"], want)
	}
	if want := (Param{Kind: ParamBool, Bool: true}); def.Params["scan"] != want {
		t.Fatalf("scan = %+v, want %+v", def.Params["scan"], want)
	}

	plans := def.Steps["plans"]
	if plans.ForEach != "plans" || plans.On["next"] != StepTarget("build") || plans.On["empty"] != DoneTarget() {
		t.Fatalf("plans step = %+v", plans)
	}
	if build := def.Steps["build"]; build.Run != "{{params.builder}}" || build.Seed != "{{plans.current}}" || build.On["done"] != StepTarget("check") {
		t.Fatalf("build step = %+v", build)
	}
	if check := def.Steps["check"]; check.Check != "make check" || check.On["red"] != HaltTarget("check red") {
		t.Fatalf("check step = %+v", check)
	}
	if gate := def.Steps["gate"]; gate.When != "{{params.scan}}" || gate.On["true"] != StepTarget("build") {
		t.Fatalf("gate step = %+v", gate)
	}
	split := def.Steps["split"]
	if split.Fork == nil || split.Fork.Each != "plans" || split.Fork.Workflow != "default" || split.On["conflict"] != HaltTarget("merge") {
		t.Fatalf("split step = %+v", split)
	}
	merge := def.Steps["merge"]
	if merge.Fork == nil || len(merge.Fork.Children) != 1 || !reflect.DeepEqual(merge.Fork.Children[0], ForkChild{Workflow: "default", Plans: []string{"r1.md"}, Task: "audit"}) {
		t.Fatalf("merge fork = %+v", merge.Fork)
	}
	repair := def.Steps["repair"]
	if repair.Budget == nil || repair.Budget.Max != (Limit{Ref: "{{params.regate}}"}) || repair.Budget.Then != DoneTarget() {
		t.Fatalf("repair budget = %+v", repair.Budget)
	}
	if want := (Per{Steps: []string{"build", "check"}}); !reflect.DeepEqual(repair.Budget.Per, want) {
		t.Fatalf("repair per = %+v, want %+v", repair.Budget.Per, want)
	}
}

func TestParseYAMLAndJSONGiveTheSameDefinition(t *testing.T) {
	fromJSON := mustParse(t, everyFieldJSON)
	fromYAML := mustParse(t, sampleYAML)
	if !reflect.DeepEqual(fromJSON, fromYAML) {
		t.Fatalf("yaml and json differ:\n json: %#v\n yaml: %#v", fromJSON, fromYAML)
	}
}

func TestParseKeepsOnYesNoAsStrings(t *testing.T) {
	def := mustParse(t, `
steps:
  pick: { run: a, on: { yes: done, no: { halt: "no" } } }
`)
	on := def.Steps["pick"].On
	if len(on) != 2 || on["yes"] != DoneTarget() || on["no"] != HaltTarget("no") {
		t.Fatalf("on = %+v", on)
	}
}

func TestParseReadsTrueFalseKeysUnderWhen(t *testing.T) {
	def := mustParse(t, `
steps:
  gate: { when: "{{params.scan}}", on: { true: build, false: done } }
`)
	on := def.Steps["gate"].On
	if len(on) != 2 || on["true"] != StepTarget("build") || on["false"] != DoneTarget() {
		t.Fatalf("on = %+v", on)
	}
}

func TestParseRejectsAnUnknownField(t *testing.T) {
	_, err := Parse([]byte(`{"name": "x", "steps": {"a": {"run": "b", "bogus": 1}}}`))
	if err == nil || !strings.Contains(err.Error(), "steps") || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("err = %v, want an unknown field naming its path", err)
	}
	if _, err := Parse([]byte(`{"name": "x", "extra": 1}`)); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("err = %v, want the top-level unknown field named", err)
	}
}

func TestParseRejectsAWrongType(t *testing.T) {
	if _, err := Parse([]byte(`{"name": 5}`)); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("err = %v, want a wrong-type error naming name", err)
	}
	_, err := Parse([]byte(`{"steps": {"a": {"on": {"done": 5}}}}`))
	if err == nil || !strings.Contains(err.Error(), `steps["a"]`) {
		t.Fatalf("err = %v, want a wrong-type error naming the on entry", err)
	}
}

func TestParseKeepsEveryKindKeyOfAStep(t *testing.T) {
	def := mustParse(t, `{"steps": {"a": {"run": "x", "check": "y", "on": {"done": "done"}}}}`)
	step := def.Steps["a"]
	if step.Run != "x" || step.Check != "y" {
		t.Fatalf("step = %+v", step)
	}
	if want := []string{"run", "check"}; !reflect.DeepEqual(step.Kinds(), want) {
		t.Fatalf("kinds = %v, want %v", step.Kinds(), want)
	}
}

func TestTargetDecodesStepDoneAndHalt(t *testing.T) {
	def := mustParse(t, `{"steps": {"a": {"run": "x", "on": {"x": "build", "y": "done", "z": {"halt": "why"}}}}}`)
	on := def.Steps["a"].On
	if on["x"] != StepTarget("build") {
		t.Fatalf("x = %+v", on["x"])
	}
	if on["y"] != DoneTarget() {
		t.Fatalf("y = %+v", on["y"])
	}
	if on["z"] != HaltTarget("why") {
		t.Fatalf("z = %+v", on["z"])
	}
}

func TestBudgetPerAcceptsStepListAndChain(t *testing.T) {
	def := mustParse(t, `{"steps": {
	  "a": {"run": "x", "budget": {"max": 3, "per": "b", "then": "done"}, "on": {"done": "done"}},
	  "b": {"run": "x", "budget": {"max": "{{params.n}}", "per": ["c", "d"], "then": {"halt": "stop"}}, "on": {"done": "done"}},
	  "c": {"run": "x", "budget": {"max": 1, "per": "chain", "then": "done"}, "on": {"done": "done"}}
	}}`)
	if a := def.Steps["a"].Budget; a.Max.Count != 3 || !reflect.DeepEqual(a.Per, Per{Steps: []string{"b"}}) || a.Then != DoneTarget() {
		t.Fatalf("budget a = %+v", a)
	}
	if b := def.Steps["b"].Budget; b.Max.Ref != "{{params.n}}" || !reflect.DeepEqual(b.Per, Per{Steps: []string{"c", "d"}}) || b.Then != HaltTarget("stop") {
		t.Fatalf("budget b = %+v", b)
	}
	if c := def.Steps["c"].Budget; !c.Per.Chain {
		t.Fatalf("budget c = %+v", c)
	}
}

func TestDefinitionRoundTripsThroughJSON(t *testing.T) {
	for _, def := range []Definition{Default(), mustParse(t, triageFirstYAML)} {
		data, err := json.Marshal(def)
		if err != nil {
			t.Fatalf("marshal %s: %v", def.Name, err)
		}
		got, err := Parse(data)
		if err != nil {
			t.Fatalf("parse %s: %v", data, err)
		}
		if !reflect.DeepEqual(def, got) {
			t.Fatalf("round trip changed %s:\n before %#v\n after  %#v", def.Name, def, got)
		}
	}
}
