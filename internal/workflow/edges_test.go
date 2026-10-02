package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// This file pins the branches the parsing, validation and engine tests above
// leave unexercised. Each case names the behaviour it pins, not just the line
// it reaches.

// jsonValue decodes one JSON document into the generic shape the decoders
// read, with numbers as json.Number.
func jsonValue(t *testing.T, doc string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", doc, err)
	}
	return v
}

func TestKeyStringRendersEveryMapKeyKind(t *testing.T) {
	cases := []struct {
		key  any
		want string
	}{
		{"x", "x"},
		{true, "true"},
		{3, "3"},
		{int64(4), "4"},
		{uint64(5), "5"},
		{1.5, "1.5"},
		{nil, "null"},
		{[]int{1}, "[1]"},
	}
	for _, tc := range cases {
		if got := keyString(tc.key); got != tc.want {
			t.Errorf("keyString(%#v) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestTypeNameNamesEveryDecodedShape(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{nil, "null"},
		{true, "bool"},
		{"x", "string"},
		{json.Number("1"), "number"},
		{[]any{}, "list"},
		{map[string]any{}, "object"},
		{int(1), "int"},
	}
	for _, tc := range cases {
		if got := typeName(tc.v); got != tc.want {
			t.Errorf("typeName(%#v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestParseRejectsWhitespaceAndMalformedDocuments(t *testing.T) {
	for _, in := range []string{
		"   \n\t",
		`{"name":`,
		`{"name": "x"} {"name": "y"}`,
		"name: [1,\n",
	} {
		if got, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", in, got)
		}
	}
}

func TestParseRejectsEachWrongShape(t *testing.T) {
	cases := []string{
		`{"inputs": []}`,
		`{"inputs": {"plans": 5}}`,
		`{"inputs": {"plans": "sometimes"}}`,
		`{"inputs": {"bogus": "required"}}`,
		`{"params": []}`,
		`{"params": {"x": 1.5}}`,
		`{"params": {"x": []}}`,
		`{"steps": []}`,
		`{"steps": {"a": 5}}`,
		`{"steps": {"a": {"run": 5, "on": {"done": "done"}}}}`,
		`{"steps": {"a": {"run": "x", "fork": {"each": 5, "workflow": "w"}, "on": {"done": "done"}}}}`,
		`{"steps": {"a": {"run": "x", "budget": [], "on": {"done": "done"}}}}`,
		`{"steps": {"a": {"run": "x", "on": []}}}`,
		`{"steps": {"a": {"run": "x", "on": {"done": []}}}}`,
	}
	for _, in := range cases {
		if got, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%s) = %+v, want an error", in, got)
		}
	}
}

func TestDecodeTargetAcceptsStepDoneHalt(t *testing.T) {
	if got, err := decodeTarget(jsonValue(t, `"build"`), ""); err != nil || got != StepTarget("build") {
		t.Fatalf("step target = %+v, err %v", got, err)
	}
	if got, err := decodeTarget(jsonValue(t, `"done"`), ""); err != nil || got != DoneTarget() {
		t.Fatalf("done target = %+v, err %v", got, err)
	}
	if got, err := decodeTarget(jsonValue(t, `{"halt":"why"}`), ""); err != nil || got != HaltTarget("why") {
		t.Fatalf("halt target = %+v, err %v", got, err)
	}
	for _, in := range []string{`{"other":1}`, `{"halt":5}`, `5`, `true`, `[]`} {
		if got, err := decodeTarget(jsonValue(t, in), ""); err == nil {
			t.Errorf("decodeTarget(%s) = %+v, want an error", in, got)
		}
	}
}

func TestDecodeBudgetRequiresEveryField(t *testing.T) {
	if got, err := decodeBudget(jsonValue(t, `{"max":1,"per":"chain","then":"done"}`), ""); err != nil {
		t.Fatalf("decodeBudget: %v", err)
	} else if got.Max.Count != 1 || !got.Per.Chain || got.Then != DoneTarget() {
		t.Fatalf("budget = %+v", got)
	}
	for _, in := range []string{
		`{"per":"chain","then":"done"}`,
		`{"max":1,"then":"done"}`,
		`{"max":1,"per":"chain"}`,
		`{"max":1,"per":"chain","then":"done","extra":1}`,
		`[]`,
	} {
		if got, err := decodeBudget(jsonValue(t, in), ""); err == nil {
			t.Errorf("decodeBudget(%s) = %+v, want an error", in, got)
		}
	}
}

func TestDecodeLimitPerAndChildrenRejectBadShapes(t *testing.T) {
	for _, in := range []string{`1.5`, `true`, `[]`} {
		if got, err := decodeLimit(jsonValue(t, in), ""); err == nil {
			t.Errorf("decodeLimit(%s) = %+v, want an error", in, got)
		}
	}
	if got, err := decodeLimit(jsonValue(t, `3`), ""); err != nil || got.Count != 3 {
		t.Fatalf("decodeLimit(3) = %+v, err %v", got, err)
	}
	if got, err := decodeLimit(jsonValue(t, `"{{params.n}}"`), ""); err != nil || got.Ref != "{{params.n}}" {
		t.Fatalf("decodeLimit(ref) = %+v, err %v", got, err)
	}

	for _, in := range []string{`["c",2]`, `5`, `true`} {
		if got, err := decodePer(jsonValue(t, in), ""); err == nil {
			t.Errorf("decodePer(%s) = %+v, want an error", in, got)
		}
	}
	if got, err := decodePer(jsonValue(t, `["c","d"]`), ""); err != nil || !reflect.DeepEqual(got.Steps, []string{"c", "d"}) {
		t.Fatalf("decodePer(list) = %+v, err %v", got, err)
	}

	for _, in := range []string{
		`{"each":"a","workflow":"b","children":[]}`,
		`{}`,
		`{"each":5,"workflow":"b"}`,
		`[]`,
	} {
		if got, err := decodeFork(jsonValue(t, in), ""); err == nil {
			t.Errorf("decodeFork(%s) = %+v, want an error", in, got)
		}
	}
	if _, err := decodeFork(jsonValue(t, `{"each":"plans","workflow":"default"}`), ""); err != nil {
		t.Fatalf("decodeFork(each): %v", err)
	}
}

func TestDecodeChildrenRejectsBadEntries(t *testing.T) {
	if got, err := decodeChildren(jsonValue(t, `[{"workflow":"x"}]`), ""); err != nil || len(got) != 1 || got[0].Workflow != "x" {
		t.Fatalf("decodeChildren = %+v, err %v", got, err)
	}
	for _, in := range []string{
		`{"workflow":"x"}`,
		`[["x"]]`,
		`[{"workflow":"x","bogus":1}]`,
		`[{"workflow":5}]`,
		`[{"workflow":"x","plans":5}]`,
		`[{"workflow":"x","task":5}]`,
	} {
		if got, err := decodeChildren(jsonValue(t, in), ""); err == nil {
			t.Errorf("decodeChildren(%s) = %+v, want an error", in, got)
		}
	}
}

func TestObjectListAndStringsAtRejectBadShapes(t *testing.T) {
	if got, err := objectAt(jsonValue(t, `[]`), ""); err == nil {
		t.Errorf("objectAt(list) = %+v, want an error", got)
	}
	if got, err := listAt(jsonValue(t, `{}`), ""); err == nil {
		t.Errorf("listAt(object) = %+v, want an error", got)
	}
	obj := map[string]any{"plans": []any{"a", 2}}
	if got, err := stringsAt(obj, "plans", ""); err == nil {
		t.Errorf("stringsAt(a non-string item) = %+v, want an error", got)
	}
	if got, err := stringsAt(map[string]any{"plans": 5}, "plans", ""); err == nil {
		t.Errorf("stringsAt(a non-list) = %+v, want an error", got)
	}
	if got, err := stringsAt(map[string]any{}, "plans", ""); err != nil || got != nil {
		t.Fatalf("stringsAt(absent) = %+v, err %v", got, err)
	}
}

func TestInputAtAcceptsOnlyTheThreeModes(t *testing.T) {
	for _, mode := range []string{"none", "optional", "required"} {
		got, err := inputAt(map[string]any{"plans": mode}, "plans", "")
		if err != nil || string(got) != mode {
			t.Fatalf("inputAt(%q) = %q, err %v", mode, got, err)
		}
	}
	if got, err := inputAt(map[string]any{"plans": "sometimes"}, "plans", ""); err == nil {
		t.Errorf("inputAt(sometimes) = %q, want an error", got)
	}
	if got, err := inputAt(map[string]any{"plans": 5}, "plans", ""); err == nil {
		t.Errorf("inputAt(5) = %q, want an error", got)
	}
	if got, err := inputAt(map[string]any{}, "plans", ""); err != nil || got != InputNone {
		t.Fatalf("inputAt(absent) = %q, err %v", got, err)
	}
}

func TestDecodeParamRejectsAnUnknownValueShape(t *testing.T) {
	if got, err := decodeParam(jsonValue(t, `1.5`), ""); err == nil {
		t.Errorf("decodeParam(1.5) = %+v, want an error", got)
	}
	if got, err := decodeParam(jsonValue(t, `[]`), ""); err == nil {
		t.Errorf("decodeParam(list) = %+v, want an error", got)
	}
}

func TestParseOutputsRejectsBadDocuments(t *testing.T) {
	for _, in := range []string{
		`{"a":`,
		`{"a":"count"} {"b":"count"}`,
		"a: [1,\n",
		`[1]`,
		`{"a": {"one-of": ["x"], "extra": 1}}`,
		`{"a": {}}`,
		`{"a": 5}`,
	} {
		if got, err := ParseOutputs([]byte(in)); err == nil {
			t.Errorf("ParseOutputs(%q) = %+v, want an error", in, got)
		}
	}
}

func TestOutcomeValueValidRejectsAnArtifact(t *testing.T) {
	if outcomeValueValid(Output{Kind: OutputArtifact}, "x") {
		t.Fatal("an artifact accepted a block value")
	}
}

func TestSnippetBoundsALongUnterminatedReference(t *testing.T) {
	_, err := Refs("{{" + strings.Repeat("a", 40))
	if err == nil {
		t.Fatal("a long unterminated reference parsed without error")
	}
	if !strings.Contains(err.Error(), "...") {
		t.Fatalf("err = %v, want the snippet truncated", err)
	}
}

func TestRenderParamsLeavesAnUnterminatedReferenceAlone(t *testing.T) {
	if got := RenderParams(Default(), "before {{params.gate"); got != "before {{params.gate" {
		t.Fatalf("RenderParams = %q, want the text unchanged", got)
	}
}

func TestParamRenderAndParseRejectAnUnknownKind(t *testing.T) {
	if got := (Param{Kind: ParamKind("nope")}).render(); got != "" {
		t.Fatalf("render = %q, want empty", got)
	}
	if _, err := (Param{Kind: ParamKind("nope")}).withValue("x"); err == nil {
		t.Fatal("withValue accepted an unknown kind")
	}
	if _, err := WithParams(Default(), nil); err != nil {
		t.Fatalf("WithParams(nil) = %v, want the definition unchanged", err)
	}
}

func TestDefinitionMarshalsForksAndRejectsUnknownKinds(t *testing.T) {
	def := mustParse(t, everyFieldJSON)
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal a definition with forks: %v", err)
	}
	if !strings.Contains(string(data), `"children"`) || !strings.Contains(string(data), `"each"`) {
		t.Fatalf("marshalled definition lacks a fork form: %s", data)
	}
	if _, err := (Param{Kind: ParamKind("nope")}).MarshalJSON(); err == nil {
		t.Error("a param with an unknown kind marshalled without error")
	}
	if _, err := (Target{Kind: TargetKind("nope")}).MarshalJSON(); err == nil {
		t.Error("a target with an unknown kind marshalled without error")
	}
	if _, err := (Per{}).MarshalJSON(); err == nil {
		t.Error("an empty per marshalled without error")
	}
	if got, err := (Per{Steps: []string{"a", "b"}}).MarshalJSON(); err != nil || string(got) != `["a","b"]` {
		t.Fatalf("per list = %s, err %v", got, err)
	}
	if _, err := json.Marshal(Budget{Max: Limit{Ref: "{{params.regate}}"}, Per: Per{Chain: true}, Then: DoneTarget()}); err != nil {
		t.Fatalf("marshal a budget: %v", err)
	}
}

func TestResumeClosedRejectsForeignCloses(t *testing.T) {
	def := Default()
	base := State{
		Status:   StatusHalted,
		At:       "build",
		Awaiting: Awaiting{Step: "build", Member: "builder", Round: 1},
	}
	if _, _, err := Resume(def, base, ResumeOpts{Closed: &Event{Kind: EventCheckClosed}}); err == nil {
		t.Error("a check close resumed a halted chain")
	}
	unknown := base
	unknown.At = "ghost"
	if _, _, err := Resume(def, unknown, ResumeOpts{Closed: &Event{Kind: EventStepClosed, Step: "ghost", Member: "builder", Round: 2}}); err == nil {
		t.Error("a close on an unknown step resumed a halted chain")
	}
	notRun := base
	notRun.At = "check"
	if _, _, err := Resume(def, notRun, ResumeOpts{Closed: &Event{Kind: EventStepClosed, Step: "check", Member: "builder", Round: 2}}); err == nil {
		t.Error("a close on a check step resumed a halted chain")
	}
}

// wantProblem parses a workflow and asserts Validate reports the rule at the
// step.
func wantProblem(t *testing.T, yaml string, env Env, rule, step string) {
	t.Helper()
	problems := Validate(mustParse(t, yaml), env)
	if !hasProblem(problems, rule, step) {
		t.Fatalf("Validate = %v, want %s at %q", problems, rule, step)
	}
}

func TestValidateFormatNamesAndSeeds(t *testing.T) {
	env := shippedEnv()
	env.Actors["odd"] = ActorInfo{Shape: ShapeReader, Outputs: Outputs{"Bad": {Kind: OutputCount}}}
	wantProblem(t, "name: Bad\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }", env, RuleFormat, "")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }\n  Bad: { run: builder, on: { done: done } }", env, RuleFormat, "Bad")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }\n  done: { run: builder, on: { done: a } }", env, RuleFormat, "done")
	wantProblem(t, "name: sample\nparams: { Bad: 1 }\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }", env, RuleFormat, "")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, seed: \"{{oops\", on: { done: done } }", env, RuleFormat, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, seed: \"file:\", on: { done: done } }", env, RuleFormat, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, seed: \"file:/x.md\", on: { done: done } }", env, RuleFormat, "a")
}

func TestValidateKindsAndParamsOnly(t *testing.T) {
	env := shippedEnv()
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { for-each: plans, on: { next: done, empty: done } }", env, RuleKind, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { for-each: nope, on: { next: done, empty: done } }", env, RuleKind, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { for-each: \"{{a.nope}}\", on: { next: done, empty: done } }", env, RuleKind, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { check: \"{{task}}\", on: { green: done, red: done } }", env, RuleKind, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { check: \"{{oops\", on: { green: done, red: done } }", env, RuleFormat, "a")
}

func TestValidateForEachWalksAnArtifact(t *testing.T) {
	def := mustParse(t, `
name: sample
start: a
steps:
  a: { run: planner, on: { done: b } }
  b: { for-each: "{{a.plan}}", on: { next: done, empty: done } }
`)
	problems := Validate(def, shippedEnv())
	if hasProblem(problems, RuleKind, "b") {
		t.Fatalf("Validate = %v, want a declared artifact to be a valid source", problems)
	}
}

func TestValidateBudgetForms(t *testing.T) {
	env := shippedEnv()
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, budget: { max: -1, per: chain, then: done }, on: { done: done } }", env, RuleBudget, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, budget: { max: nope, per: chain, then: done }, on: { done: done } }", env, RuleBudget, "a")
	wantProblem(t, "name: sample\nparams: { name: \"x\" }\nstart: a\nsteps:\n  a: { run: builder, budget: { max: \"{{params.name}}\", per: chain, then: done }, on: { done: done } }", env, RuleBudget, "a")
}

func TestValidateMatchForms(t *testing.T) {
	env := shippedEnv()
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: reviewer, on: { nope: done, else: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: reviewer, on: { findings: done, else: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: reviewer, on: { verdict: done, else: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: reviewer, on: { verdict=maybe: done, else: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: security, on: { findings=2: done, else: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: security, on: { findings>0: done, findings=0: done, nope>0: done, else: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, on: { status=nope: done, status=halted: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, on: { status=halted: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { check: make check, on: { green: done } }", env, RuleMatch, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { check: make check, on: { bogus: done, green: done, red: done } }", env, RuleMatch, "a")
}

func TestValidateMatchSortsByDetail(t *testing.T) {
	env := shippedEnv()
	def := mustParse(t, "name: sample\nstart: a\nsteps:\n  a: { run: reviewer, on: { nope=x: done, other=y: done, else: done } }")
	problems := Validate(def, env)
	if len(problems) < 2 || problems[0].Step != "a" || problems[0].Rule != RuleMatch {
		t.Fatalf("Validate = %v, want two rule 3 problems at a", problems)
	}
	if problems[0].Detail > problems[1].Detail {
		t.Fatalf("Validate = %v, want the details sorted", problems)
	}
}

func TestValidateReferenceAttributes(t *testing.T) {
	env := shippedEnv()
	// A check's log, a fork's conflict, and a writer's report and diff.
	ok := []string{
		"name: sample\nstart: c\nsteps:\n  c: { check: make check, on: { green: b, red: b } }\n  b: { run: builder, seed: \"{{c.log}}\", on: { done: done } }",
		"name: sample\nstart: f\nsteps:\n  f: { fork: { children: [ { workflow: child } ] }, on: { joined: b, conflict: b } }\n  b: { run: builder, seed: \"{{f.conflict}}\", on: { done: done } }",
		"name: sample\nstart: a\nsteps:\n  a: { run: builder, on: { done: b } }\n  b: { run: builder, seed: \"{{a.report}}\", on: { done: c } }\n  c: { run: builder, seed: \"{{a.diff}}\", on: { done: done } }",
		"name: sample\ninputs: { plans: required }\nstart: p\nsteps:\n  p: { for-each: plans, on: { next: b, empty: done } }\n  b: { run: builder, seed: \"{{p.current}}\", on: { done: p } }",
	}
	env.Workflow = func(string) (Definition, bool) {
		return mustParse(t, "name: child\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }"), true
	}
	for _, yaml := range ok {
		if problems := Validate(mustParse(t, yaml), env); hasProblem(problems, RuleRef, "") {
			t.Errorf("Validate(%s) = %v, want the reference accepted", yaml, problems)
		}
	}
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, seed: \"{{chain.nope}}\", on: { done: done } }", env, RuleRef, "a")
	wantProblem(t, "name: sample\ninputs: { task: none }\nstart: a\nsteps:\n  a: { run: builder, seed: \"{{plans.all}}\", on: { done: done } }", env, RuleRef, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, seed: \"{{ghost.report}}\", on: { done: done } }", env, RuleRef, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, seed: \"{{a.report}}\", on: { done: done } }", env, RuleRef, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { run: builder, seed: \"{{params.nope}}\", on: { done: done } }", env, RuleRef, "a")
}

func TestValidateWhenForms(t *testing.T) {
	env := shippedEnv()
	wantProblem(t, "name: sample\nparams: { flag: true }\ninputs: { task: required }\nstart: a\nsteps:\n  a: { when: \"x {{params.flag}}\", on: { true: done, false: done } }", env, RuleWhen, "a")
	wantProblem(t, "name: sample\nstart: a\nsteps:\n  a: { when: \"{{params.nope}}\", on: { true: done, false: done } }", env, RuleWhen, "a")
}

func TestValidateInputsAgreesWithWhatWasGiven(t *testing.T) {
	env := shippedEnv()
	env.Given = &Given{Plans: true}
	wantProblem(t, "name: sample\ninputs: { plans: none }\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }", env, RuleInputs, "")
}

func TestValidateForkEachAndMissingResolver(t *testing.T) {
	env := shippedEnv()
	env.Given = &Given{Plans: true}
	env.Workflow = func(string) (Definition, bool) {
		return mustParse(t, "name: child\ninputs: { plans: required }\nstart: a\nsteps:\n  a: { run: builder, on: { done: done } }"), true
	}
	each := "name: sample\ninputs: { plans: required }\nstart: f\nsteps:\n  f: { fork: { each: plans, workflow: child }, on: { joined: done, conflict: done } }\n  b: { run: builder, on: { done: done } }"
	if problems := Validate(mustParse(t, each), env); hasProblem(problems, RuleFork, "f") {
		t.Fatalf("Validate = %v, want the each fork child to resolve", problems)
	}
	env.Workflow = nil
	wantProblem(t, each, env, RuleFork, "f")
}

func TestCutAndSameSetEdges(t *testing.T) {
	if left, right := cut("abc", "="); left != "abc" || right != "" {
		t.Fatalf("cut = %q, %q, want the whole string and an empty rest", left, right)
	}
	if sameSet(map[string]bool{"x": true, "y": true}, map[string]bool{"x": true, "z": true}) {
		t.Fatal("sameSet reported two different sets equal")
	}
}

func TestGraphHelpersCoverTheirEdges(t *testing.T) {
	if got := dominators(Default(), "ghost"); got != nil {
		t.Fatalf("dominators(ghost) = %v, want nil", got)
	}
	if got := intersectPreds(map[string]map[string]bool{}, nil); len(got) != 0 {
		t.Fatalf("intersectPreds(no preds) = %v, want empty", got)
	}
	if dominates(dominators(Default(), "plans"), "build", "build") {
		t.Fatal("a step dominates itself, want false")
	}
	def := mustParse(t, `
name: sample
inputs: { plans: required }
start: f
steps:
  f: { fork: { each: plans, workflow: child }, on: { joined: g, conflict: g } }
  g: { fork: { each: plans, workflow: child }, on: { joined: f, conflict: f } }
`)
	cycles := boundedCycles(def)
	want := false
	for _, c := range cycles {
		if c.Detail == "a cycle with no run or check step" {
			want = true
		}
	}
	if !want {
		t.Fatalf("boundedCycles = %v, want a cycle with no run or check step", cycles)
	}
}

func TestEnterRejectsUnknownAndKindlessSteps(t *testing.T) {
	def := tdef("a", map[string]Step{"a": {Run: "builder"}})
	if _, acts := enter(def, runningState(), "ghost", 0); len(acts) != 1 || acts[0].Kind != ActionHalt {
		t.Fatalf("enter(ghost) = %+v, want a halt", acts)
	}
	kindless := tdef("a", map[string]Step{"a": {}})
	if _, acts := enter(kindless, runningState(), "a", 0); len(acts) != 1 || acts[0].Kind != ActionHalt {
		t.Fatalf("enter(kindless) = %+v, want a halt", acts)
	}
}

func TestRenderLimitTreatsANonIntParamAsZero(t *testing.T) {
	def := Default()
	if got := renderLimit(def, Limit{Ref: "{{params.gate}}"}); got != 0 {
		t.Fatalf("renderLimit(a string param) = %d, want 0", got)
	}
	if got := renderLimit(def, Limit{Count: 4}); got != 4 {
		t.Fatalf("renderLimit(4) = %d, want 4", got)
	}
}

func TestForEachReadsAnArtifactAndItsCap(t *testing.T) {
	def := tdef("f", map[string]Step{
		"f": {ForEach: "{{a.rounds}}", On: map[string]Target{"next": StepTarget("a"), "empty": DoneTarget()}},
		"a": {Run: "builder", On: map[string]Target{"done": StepTarget("f")}},
	})
	if root, attr, ok := artifactSource("{{a.rounds}}"); !ok || root != "a" || attr != "rounds" {
		t.Fatalf("artifactSource = %q, %q, %v", root, attr, ok)
	}
	if _, _, ok := artifactSource("nope"); ok {
		t.Fatal("artifactSource accepted a non-reference")
	}
	s := runningState()
	s.Results["a"] = Result{Artifacts: map[string][]string{"rounds": {"r1.md", "r2.md"}}}
	it := forEachIter(def, s, "f", def.Steps["f"])
	if !reflect.DeepEqual(it.Items, []string{"r1.md", "r2.md"}) {
		t.Fatalf("forEachIter = %+v, want the recorded rounds", it)
	}
	if _, acts := walkForEach(def, s, "f", def.Steps["f"], len(def.Steps)+2); len(acts) != 1 || acts[0].Kind != ActionHalt {
		t.Fatalf("walkForEach past the cap = %+v, want a halt", acts)
	}
}

func TestWhenTrueAndControlRouteEdges(t *testing.T) {
	def := tdef("w", map[string]Step{"w": {When: "{{params.b}}"}, "t": {Run: "builder"}})
	def.Params = map[string]Param{"b": {Kind: ParamBool, Bool: true}}
	if !whenTrue(def, "{{params.b}}") {
		t.Fatal("whenTrue did not read a true bool")
	}
	if whenTrue(def, "{{task}}") {
		t.Fatal("whenTrue read a non-params reference as true")
	}
	if whenTrue(def, "garbage") {
		t.Fatal("whenTrue read a non-reference as true")
	}
	if _, acts := controlRoute(def, runningState(), "w", map[string]Target{}, "next", 0); len(acts) != 1 || acts[0].Kind != ActionHalt {
		t.Fatalf("controlRoute with no edge = %+v, want a halt", acts)
	}
}

func TestNextUnmatchedDoneAndHaltedReasons(t *testing.T) {
	nothing := tdef("a", map[string]Step{"a": {Run: "builder", On: map[string]Target{"status=halted": StepTarget("a")}}})
	e := Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 1, Status: "done"}
	s, acts := Next(nothing, awaitingRun("a", "builder", 1), e)
	if s.Status != StatusHalted || s.Reason != "a done: no edge matches" {
		t.Fatalf("done with no edge: reason %q", s.Reason)
	}
	if len(acts) != 1 || acts[0].Kind != ActionHalt {
		t.Fatalf("actions = %+v, want a halt", acts)
	}

	outcomes := tdef("a", map[string]Step{"a": {Run: "reviewer", On: map[string]Target{"verdict=pass": StepTarget("a")}}})
	e = Event{Kind: EventStepClosed, Step: "a", Member: "reviewer", Round: 1, Status: "done",
		Outcomes: map[string]string{"verdict": "changes"}}
	s, _ = Next(outcomes, awaitingRun("a", "reviewer", 1), e)
	if want := "a done: no edge matches outcomes verdict=changes"; s.Reason != want {
		t.Fatalf("reason = %q, want %q", s.Reason, want)
	}

	reasonless := tdef("a", map[string]Step{"a": {Run: "builder", On: map[string]Target{"status=halted": StepTarget("a")}}})
	e = Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 1, Status: "blocked"}
	s, _ = Next(reasonless, awaitingRun("a", "builder", 1), e)
	if s.Reason != "a blocked" {
		t.Fatalf("reason = %q, want the bare step and status", s.Reason)
	}
}

func TestNextMatchFallbacks(t *testing.T) {
	statusDone := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"status=done": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	e := Event{Kind: EventStepClosed, Step: "a", Member: "builder", Round: 1, Status: "done"}
	if s, _ := Next(statusDone, awaitingRun("a", "builder", 1), e); s.At != "b" {
		t.Fatalf("status=done edge: at = %q, want b", s.At)
	}

	elseOn := tdef("a", map[string]Step{
		"a": {Run: "builder", On: map[string]Target{"else": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	if s, _ := Next(elseOn, awaitingRun("a", "builder", 1), e); s.At != "b" {
		t.Fatalf("else edge: at = %q, want b", s.At)
	}

	count := tdef("a", map[string]Step{
		"a": {Run: "security", On: map[string]Target{"n=0": StepTarget("b")}},
		"b": {Run: "builder"},
	})
	countEvent := Event{Kind: EventStepClosed, Step: "a", Member: "security", Round: 1, Status: "done",
		Outcomes: map[string]string{"n": "0"}}
	if s, _ := Next(count, awaitingRun("a", "security", 1), countEvent); s.At != "b" {
		t.Fatalf("count =0 arm: at = %q, want b", s.At)
	}
}

func TestCheckClosedFallbacks(t *testing.T) {
	none := tdef("c", map[string]Step{"c": {Check: "make check", On: map[string]Target{"green": DoneTarget()}}})
	e := Event{Kind: EventCheckClosed, Step: "c", Run: 1, Result: "red"}
	if s, _ := Next(none, awaitingCheck("c", 1), e); s.Reason != "c red: no edge matches" {
		t.Fatalf("no edge: reason = %q", s.Reason)
	}
	bare := tdef("c", map[string]Step{"c": {Check: "make check", On: map[string]Target{"red": DoneTarget()}}})
	if s, _ := Next(bare, awaitingCheck("c", 1), e); s.Status != StatusDone {
		t.Fatalf("bare result: status = %q, want done", s.Status)
	}
	elseOn := tdef("c", map[string]Step{"c": {Check: "make check", On: map[string]Target{"else": DoneTarget()}}})
	if s, _ := Next(elseOn, awaitingCheck("c", 1), e); s.Status != StatusDone {
		t.Fatalf("else: status = %q, want done", s.Status)
	}
}

func TestNextGuardsAndNilMaps(t *testing.T) {
	def := tdef("a", map[string]Step{"a": {Run: "builder", On: map[string]Target{"done": StepTarget("b")}}, "b": {Run: "builder"}})

	// A needs_you while a check is awaited matches the check.
	checking := tdef("c", map[string]Step{"c": {Check: "make check", On: map[string]Target{"green": DoneTarget()}}})
	e := Event{Kind: EventNeedsYou, Step: "c", Run: 4, Reason: "stop"}
	if s, _ := Next(checking, awaitingCheck("c", 4), e); s.Status != StatusHalted || s.Reason != "stop" {
		t.Fatalf("needs_you on a check: %q %q", s.Status, s.Reason)
	}

	// An unknown kind changes nothing.
	before := awaitingRun("a", "builder", 1)
	if s, acts := Next(def, before, Event{Kind: EventKind("bogus")}); len(acts) != 0 || !reflect.DeepEqual(s, before) {
		t.Fatalf("unknown kind: state %+v, actions %+v", s, acts)
	}

	// A state with nil maps gains them when a step is entered.
	sent, acts := enter(def, State{Status: StatusRunning}, "b", 0)
	if len(acts) != 1 || acts[0].Kind != ActionSend {
		t.Fatalf("enter on nil maps: actions %+v, want a send", acts)
	}
	if sent.Results == nil || sent.Visits == nil || sent.Iter == nil {
		t.Fatalf("enter left a state map nil: %+v", sent)
	}
	if m := ensureResults(nil); m == nil || len(m) != 0 {
		t.Fatalf("ensureResults(nil) = %v, want an empty map", m)
	}
	if m := ensureResults(map[string]Result{"x": {}}); len(m) != 1 {
		t.Fatalf("ensureResults(one) = %v, want the map back", m)
	}
}
