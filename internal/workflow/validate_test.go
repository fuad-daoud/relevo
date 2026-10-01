package workflow

import (
	"strings"
	"testing"
)

// shippedEnv is the actor environment every test validates against: the actors
// the shipped default names, with the outputs the spec declares.
func shippedEnv() Env {
	return Env{
		Actors: map[string]ActorInfo{
			"builder":      {Shape: ShapeWriter},
			"reviewer":     {Shape: ShapeReader, Outputs: Outputs{"verdict": {Kind: OutputOneOf, Values: []string{"pass", "changes"}}, "findings": {Kind: OutputArtifact}}},
			"lite-planner": {Shape: ShapeReader, Outputs: Outputs{"plan": {Kind: OutputArtifact}}},
			"planner":      {Shape: ShapeReader, Outputs: Outputs{"plan": {Kind: OutputArtifact}}},
			"security":     {Shape: ShapeReader, Outputs: Outputs{"findings": {Kind: OutputCount}, "report": {Kind: OutputArtifact}}},
		},
		Seeds: []string{"repair", "review", "correct", "scan", "fix"},
	}
}

func hasProblem(problems []Problem, rule, step string) bool {
	for _, p := range problems {
		if p.Rule == rule && p.Step == step {
			return true
		}
	}
	return false
}

func TestValidateDefaultIsClean(t *testing.T) {
	env := shippedEnv()
	env.Given = &Given{Plans: true}
	if problems := Validate(Default(), env); len(problems) != 0 {
		for _, p := range problems {
			t.Errorf("default: %s", p)
		}
	}
}

func TestValidateTriageFirstIsClean(t *testing.T) {
	env := shippedEnv()
	env.Actors["yes-no"] = ActorInfo{Shape: ShapeReader, Outputs: Outputs{"answer": {Kind: OutputOneOf, Values: []string{"yes", "no"}}}}
	env.Given = &Given{Task: true}
	if problems := Validate(mustParse(t, triageFirstYAML), env); len(problems) != 0 {
		for _, p := range problems {
			t.Errorf("triage-first: %s", p)
		}
	}
}

// validateCase is one positive test of the rules: a workflow, an environment,
// and the rule and step one failure must name.
type validateCase struct {
	name  string
	yaml  string
	env   func(*testing.T, Env) Env
	given *Given
	rule  string
	step  string
}

// validateCases is the per-rule table TestValidateRules runs. It is a package
// variable so the table can stay one list without making the test too long.
var validateCases = []validateCase{
	{
		name: "the start is missing",
		yaml: `name: sample
start: ghost
steps:
  a: { run: builder, on: { done: done } }`,
		rule: RuleStart, step: "",
	},
	{
		name: "a target is missing",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, on: { done: ghost } }`,
		rule: RuleStart, step: "a",
	},
	{
		name: "a step is unreachable",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, on: { done: done } }
  b: { run: builder, on: { done: done } }`,
		rule: RuleStart, step: "b",
	},
	{
		name: "a step has two kinds",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, check: "make check", on: { done: done } }`,
		rule: RuleKind, step: "a",
	},
	{
		name: "a step has no kind",
		yaml: `name: sample
start: a
steps:
  a: { on: { done: done } }`,
		rule: RuleKind, step: "a",
	},
	{
		name: "a run names an unknown actor",
		yaml: `name: sample
start: a
steps:
  a: { run: nobody, on: { done: done } }`,
		rule: RuleKind, step: "a",
	},
	{
		name: "a for-each has a bad source",
		yaml: `name: sample
inputs: { plans: required }
start: a
steps:
  a: { for-each: nope, on: { next: done, empty: done } }`,
		rule: RuleKind, step: "a",
	},
	{
		name: "a match is undeclared",
		yaml: `name: sample
start: a
steps:
  a: { run: reviewer, on: { nope=x: done, else: done } }`,
		rule: RuleMatch, step: "a",
	},
	{
		name: "a match is on an artifact",
		yaml: `name: sample
start: a
steps:
  a: { run: reviewer, on: { findings=1: done, else: done } }`,
		rule: RuleMatch, step: "a",
	},
	{
		name: "a one-of value is uncovered",
		yaml: `name: sample
start: a
steps:
  a: { run: reviewer, on: { verdict=pass: done } }`,
		rule: RuleMatch, step: "a",
	},
	{
		name: "a count arm is uncovered",
		yaml: `name: sample
start: a
steps:
  a: { run: security, on: { findings=0: done } }`,
		rule: RuleMatch, step: "a",
	},
	{
		name: "a reference is not dominated",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, seed: "{{b.report}}", on: { done: done } }
  b: { run: builder, on: { done: done } }`,
		rule: RuleRef, step: "a",
	},
	{
		name: "a reference has a bad attribute",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, on: { done: b } }
  b: { run: builder, seed: "{{a.nope}}", on: { done: done } }`,
		rule: RuleRef, step: "b",
	},
	{
		name: "a reference names a none input",
		yaml: `name: sample
inputs: { task: none }
start: a
steps:
  a: { run: builder, seed: "{{task}}", on: { done: done } }`,
		rule: RuleRef, step: "a",
	},
	{
		name: "a control-only cycle",
		yaml: `name: sample
inputs: { plans: required }
params: { flag: true }
start: a
steps:
  a: { when: "{{params.flag}}", on: { true: b, false: done } }
  b: { for-each: plans, on: { next: a, empty: done } }`,
		rule: RuleBudget, step: "a",
	},
	{
		name: "a cycle with no budget",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, on: { done: b } }
  b: { run: builder, on: { done: a } }`,
		rule: RuleBudget, step: "a",
	},
	{
		name: "a budget resets inside its own cycle",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, budget: { max: 1, per: b, then: done }, on: { done: b } }
  b: { run: builder, on: { done: a } }`,
		rule: RuleBudget, step: "a",
	},
	{
		name: "a budget per is unknown",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, budget: { max: 1, per: ghost, then: done }, on: { done: done } }`,
		rule: RuleBudget, step: "a",
	},
	{
		name: "a budget then is unknown",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, budget: { max: 1, per: chain, then: ghost }, on: { done: done } }`,
		rule: RuleStart, step: "a",
	},
	{
		name: "the inputs disagree with what was given",
		yaml: `name: sample
inputs: { plans: required, task: none }
start: a
steps:
  a: { run: builder, on: { done: done } }`,
		given: &Given{},
		rule:  RuleInputs, step: "",
	},
	{
		name: "a fork child does not resolve",
		yaml: `name: sample
start: f
steps:
  f: { fork: { children: [ { workflow: ghost } ] }, on: { joined: done, conflict: done } }`,
		env: func(_ *testing.T, env Env) Env {
			env.Workflow = func(string) (Definition, bool) { return Definition{}, false }
			return env
		},
		rule: RuleFork, step: "f",
	},
	{
		name: "a fork child is invalid",
		yaml: `name: sample
start: f
steps:
  f: { fork: { children: [ { workflow: child } ] }, on: { joined: done, conflict: done } }`,
		env: func(t *testing.T, env Env) Env {
			child := mustParse(t, `name: child
start: a
steps:
  a: { run: nobody, on: { done: done } }`)
			env.Workflow = func(name string) (Definition, bool) {
				if name == "child" {
					return child, true
				}
				return Definition{}, false
			}
			return env
		},
		rule: RuleFork, step: "f",
	},
	{
		name: "a fork recurses into itself",
		yaml: `name: sample
start: f
steps:
  f: { fork: { children: [ { workflow: sample } ] }, on: { joined: done, conflict: done } }`,
		env: func(t *testing.T, env Env) Env {
			self := mustParse(t, `name: sample
start: f
steps:
  f: { fork: { children: [ { workflow: sample } ] }, on: { joined: done, conflict: done } }`)
			env.Workflow = func(name string) (Definition, bool) {
				if name == "sample" {
					return self, true
				}
				return Definition{}, false
			}
			return env
		},
		rule: RuleFork, step: "f",
	},
	{
		name: "a when on a non-bool param",
		yaml: `name: sample
params: { flag: "yes" }
start: a
steps:
  a: { when: "{{params.flag}}", on: { true: done, false: done } }`,
		rule: RuleWhen, step: "a",
	},
	{
		name: "a when on a runtime reference",
		yaml: `name: sample
params: { flag: true }
inputs: { task: required }
start: a
steps:
  a: { when: "{{task}}", on: { true: done, false: done } }`,
		rule: RuleWhen, step: "a",
	},
	{
		name: "a seed from an unknown shipped name",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, seed: shipped:nope, on: { done: done } }`,
		rule: RuleFormat, step: "a",
	},
	{
		name: "a file seed that escapes the workflow's directory",
		yaml: `name: sample
start: a
steps:
  a: { run: builder, seed: "file:../x.md", on: { done: done } }`,
		rule: RuleFormat, step: "a",
	},
}

func TestValidateRules(t *testing.T) {
	for _, tc := range validateCases {
		t.Run(tc.name, func(t *testing.T) {
			env := shippedEnv()
			if tc.env != nil {
				env = tc.env(t, env)
			}
			if tc.given != nil {
				env.Given = tc.given
			}
			problems := Validate(mustParse(t, tc.yaml), env)
			if !hasProblem(problems, tc.rule, tc.step) {
				t.Fatalf("Validate = %v, want %s at step %q", problems, tc.rule, tc.step)
			}
		})
	}
}

func TestValidateReportsEveryFailureNotTheFirst(t *testing.T) {
	def := mustParse(t, `
name: sample
start: a
steps:
  a: { run: nobody, on: { done: b } }
  b: { run: nobody2, on: { done: done } }
  c: { run: builder, on: { done: done } }
`)
	problems := Validate(def, shippedEnv())
	if len(problems) < 3 {
		t.Fatalf("Validate returned %d problems, want every failure", len(problems))
	}
	if !hasProblem(problems, RuleKind, "a") || !hasProblem(problems, RuleKind, "b") {
		t.Fatalf("Validate = %v, want problems at a and b", problems)
	}
	if !hasProblem(problems, RuleStart, "c") {
		t.Fatalf("Validate = %v, want c unreachable", problems)
	}
}

func TestValidateOrdersProblemsByStepThenRule(t *testing.T) {
	def := mustParse(t, `
name: sample
start: a
steps:
  b: { run: nobody, on: { done: done } }
  a: { run: builder, seed: shipped:nope, on: { done: done } }
`)
	problems := Validate(def, shippedEnv())
	if len(problems) < 2 {
		t.Fatalf("Validate = %v, want two problems", problems)
	}
	if problems[0].Step != "a" || problems[0].Rule != RuleFormat {
		t.Fatalf("problems[0] = %v, want the format problem at a first", problems[0])
	}
}

func TestProblemStringNamesTheStep(t *testing.T) {
	got := Problem{Step: "build", Rule: RuleKind, Detail: "bad"}.String()
	if got != "step build: rule 2: bad" {
		t.Fatalf("String = %q", got)
	}
	if got := (Problem{Rule: RuleInputs, Detail: "bad"}).String(); strings.Contains(got, "step ") {
		t.Fatalf("workflow-level String = %q, want no step prefix", got)
	}
}
