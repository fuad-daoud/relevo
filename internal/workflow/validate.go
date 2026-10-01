package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The validation rules, one stable exported constant each. The value carries
// the spec rule number the check implements.
const (
	RuleFormat = "format"
	RuleStart  = "rule 1"
	RuleKind   = "rule 2"
	RuleMatch  = "rule 3"
	RuleRef    = "rule 4"
	RuleBudget = "rule 5"
	RuleInputs = "rule 6"
	RuleFork   = "rule 7"
	RuleWhen   = "rule 8"
)

// paramNamePattern is the shape a workflow param name shares.
var paramNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// reservedSteps are step ids a workflow may not use, because a reference or a
// target reads them as something else.
var reservedSteps = map[string]bool{
	"done":   true,
	"chain":  true,
	"params": true,
	"task":   true,
	"else":   true,
}

// statuses are the run statuses a status edge may match.
var statuses = map[string]bool{
	"done":     true,
	"halted":   true,
	"blocked":  true,
	"deferred": true,
}

// Shape says whether an actor writes the chain's one tree or reads a copy.
type Shape string

// The shapes an actor may have.
const (
	ShapeReader Shape = "reader"
	ShapeWriter Shape = "writer"
)

// ActorInfo is what validation knows about an actor: its shape and the outputs
// it declares.
type ActorInfo struct {
	Shape   Shape
	Outputs Outputs
}

// Given says which chain inputs the caller has. A nil Given skips rule 6, as
// saving a workflow does.
type Given struct {
	Plans bool
	Task  bool
}

// Env is what validation needs from outside the workflow: the actors present,
// the inputs given, the shipped seed names, and a resolver for fork children.
type Env struct {
	Actors   map[string]ActorInfo
	Given    *Given
	Seeds    []string
	Workflow func(name string) (Definition, bool)
}

// Problem is one validation failure: the step it names (empty for a
// workflow-level failure), the rule, and the detail.
type Problem struct {
	Step   string
	Rule   string
	Detail string
}

// String renders a problem as "step <id>: <rule>: <detail>", dropping the step
// prefix for a workflow-level problem.
func (p Problem) String() string {
	if p.Step == "" {
		return p.Rule + ": " + p.Detail
	}
	return "step " + p.Step + ": " + p.Rule + ": " + p.Detail
}

// Validate reports every way def fails the format rules and rules 1 to 8,
// against env. It never stops at the first failure: it returns one Problem per
// failure, sorted by step then rule.
func Validate(def Definition, env Env) []Problem {
	return validate(def, env, map[string]bool{def.Name: true})
}

// validate is Validate with the set of workflow names already being validated,
// so a fork that reaches itself is refused instead of recursing.
func validate(def Definition, env Env, visited map[string]bool) []Problem {
	v := &validator{def: def, env: env, visited: visited}
	v.checkFormat()
	v.checkStart()
	v.checkKinds()
	v.checkMatches()
	v.checkReferences()
	v.checkBudgets()
	v.checkInputs()
	v.checkForks()
	v.checkWhens()
	sort.SliceStable(v.problems, func(i, j int) bool {
		a, b := v.problems[i], v.problems[j]
		if a.Step != b.Step {
			return a.Step < b.Step
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Detail < b.Detail
	})
	return v.problems
}

// validator carries the definition, the environment, and the problems found so
// far. The dominator set and the edges are computed once, on demand.
type validator struct {
	def      Definition
	env      Env
	visited  map[string]bool
	problems []Problem
	dom      map[string]map[string]bool
	domDone  bool
}

func (v *validator) add(step, rule, format string, args ...any) {
	v.problems = append(v.problems, Problem{Step: step, Rule: rule, Detail: fmt.Sprintf(format, args...)})
}

// dominators returns the dominator sets of the definition, computing them once.
func (v *validator) dominators() map[string]map[string]bool {
	if !v.domDone {
		v.dom = dominators(v.def, v.def.Start)
		v.domDone = true
	}
	return v.dom
}

// actor returns the actor a run step names, after its params are rendered.
func (v *validator) actor(step Step) (ActorInfo, bool) {
	if step.Run == "" {
		return ActorInfo{}, false
	}
	actor, ok := v.env.Actors[RenderParams(v.def, step.Run)]
	return actor, ok
}

// checkFormat applies the format rules: names, reserved ids, param names,
// reference syntax, and the three seed forms.
func (v *validator) checkFormat() {
	if err := ValidName(v.def.Name); err != nil {
		v.add("", RuleFormat, "name %q is not a valid name", v.def.Name)
	}
	for _, id := range sortedKeys(v.def.Steps) {
		if err := ValidName(id); err != nil {
			v.add(id, RuleFormat, "step id %q is not a valid name", id)
		}
		if reservedSteps[id] {
			v.add(id, RuleFormat, "step id %q is reserved", id)
		}
	}
	for _, key := range sortedKeys(v.def.Params) {
		if !paramNamePattern.MatchString(key) {
			v.add("", RuleFormat, "param name %q is not a valid name", key)
		}
	}
	for _, actor := range sortedKeys(v.env.Actors) {
		for _, key := range sortedKeys(v.env.Actors[actor].Outputs) {
			if err := ValidName(key); err != nil {
				v.add("", RuleFormat, "actor %s: output key %q is not a valid name", actor, key)
			}
		}
	}
	for _, id := range sortedKeys(v.def.Steps) {
		v.checkSeed(id)
		for _, f := range v.templates(id) {
			if _, err := Refs(f.text); err != nil {
				v.add(id, RuleFormat, "%s: %s", f.name, err)
			}
		}
	}
}

// checkSeed applies the seed format rules: inline, file:<relative path>, or
// shipped:<name> with the name in the environment's seeds.
func (v *validator) checkSeed(id string) {
	seed := v.def.Steps[id].Seed
	switch {
	case seed == "":
		return
	case strings.HasPrefix(seed, "file:"):
		path := strings.TrimPrefix(seed, "file:")
		if path == "" {
			v.add(id, RuleFormat, "seed file: names no path")
			return
		}
		if strings.HasPrefix(path, "/") {
			v.add(id, RuleFormat, "seed file %q is absolute", path)
		}
		for _, part := range strings.Split(path, "/") {
			if part == ".." {
				v.add(id, RuleFormat, "seed file %q escapes the workflow's directory", path)
				break
			}
		}
	case strings.HasPrefix(seed, "shipped:"):
		name := strings.TrimPrefix(seed, "shipped:")
		if !containsStr(v.env.Seeds, name) {
			v.add(id, RuleFormat, "seed shipped:%s is not a shipped seed", name)
		}
	}
}

// checkStart is rule 1: start and every target exist, and every step is
// reachable from start.
func (v *validator) checkStart() {
	if _, ok := v.def.Steps[v.def.Start]; !ok {
		v.add("", RuleStart, "start %q is not a step", v.def.Start)
	}
	for _, id := range sortedKeys(v.def.Steps) {
		step := v.def.Steps[id]
		for _, key := range sortedKeys(step.On) {
			if t := step.On[key]; t.Kind == TargetStep {
				if _, ok := v.def.Steps[t.Step]; !ok {
					v.add(id, RuleStart, "on %s targets unknown step %q", key, t.Step)
				}
			}
		}
		if step.Budget != nil && step.Budget.Then.Kind == TargetStep {
			if _, ok := v.def.Steps[step.Budget.Then.Step]; !ok {
				v.add(id, RuleStart, "budget then targets unknown step %q", step.Budget.Then.Step)
			}
		}
	}
	if _, ok := v.def.Steps[v.def.Start]; !ok {
		return
	}
	reach := reachable(v.def, v.def.Start)
	for _, id := range sortedKeys(v.def.Steps) {
		if !reach[id] {
			v.add(id, RuleStart, "unreachable from start")
		}
	}
}

// checkKinds is rule 2: exactly one kind, a run names a known actor, a for-each
// names a list, and run, check, when and budget.max read params only.
func (v *validator) checkKinds() {
	for _, id := range sortedKeys(v.def.Steps) {
		step := v.def.Steps[id]
		switch kinds := step.Kinds(); len(kinds) {
		case 1:
		case 0:
			v.add(id, RuleKind, "a step needs exactly one kind")
			continue
		default:
			v.add(id, RuleKind, "a step has more than one kind: %s", strings.Join(kinds, ", "))
			continue
		}
		switch kindOf(step) {
		case "run":
			name := RenderParams(v.def, step.Run)
			if _, ok := v.env.Actors[name]; !ok {
				v.add(id, RuleKind, "run names unknown actor %q", name)
			}
		case "for-each":
			v.checkForEachSource(id, step.ForEach)
		}
		v.checkParamsOnly(id, step)
	}
}

// checkForEachSource is rule 2's for-each clause: the source is the plans input
// when it is not none, or a declared list artifact.
func (v *validator) checkForEachSource(id, source string) {
	if source == "plans" {
		if v.def.Inputs.Plans == InputNone {
			v.add(id, RuleKind, "for-each walks plans, but the plans input is none")
		}
		return
	}
	refs, err := Refs(source)
	if err != nil || len(refs) != 1 || !IsSingleRef(source) {
		v.add(id, RuleKind, "for-each source %q is neither plans nor a reference", source)
		return
	}
	if !v.isArtifact(refs[0].Root, refs[0].Attr) {
		v.add(id, RuleKind, "for-each source %s is not a declared artifact", refText(refs[0]))
	}
}

// checkParamsOnly is rule 2's params clause for a step's run, check, when and
// budget.max fields.
func (v *validator) checkParamsOnly(id string, step Step) {
	fields := make([]stepTemplate, 0, 4)
	add := func(name, text string) {
		if text != "" {
			fields = append(fields, stepTemplate{name, text})
		}
	}
	add("run", step.Run)
	add("check", step.Check)
	add("when", step.When)
	if step.Budget != nil {
		add("budget.max", step.Budget.Max.Ref)
	}
	for _, f := range fields {
		refs, err := Refs(f.text)
		if err != nil {
			continue
		}
		for _, ref := range refs {
			if ref.Root != "params" {
				v.add(id, RuleKind, "%s reads %s; only params are allowed", f.name, refText(ref))
			}
		}
	}
}

// checkBudgets is rule 5: the budget fields are sound, and no cycle is
// unbounded.
func (v *validator) checkBudgets() {
	for _, id := range sortedKeys(v.def.Steps) {
		budget := v.def.Steps[id].Budget
		if budget == nil {
			continue
		}
		if budget.Max.Ref == "" {
			if budget.Max.Count < 0 {
				v.add(id, RuleBudget, "budget max %d is negative", budget.Max.Count)
			}
		} else {
			v.checkBudgetMax(id, budget.Max.Ref)
		}
		for _, per := range budget.Per.Steps {
			if _, ok := v.def.Steps[per]; !ok {
				v.add(id, RuleBudget, "budget per names unknown step %q", per)
			}
		}
	}
	for _, c := range boundedCycles(v.def) {
		v.add(c.Step, RuleBudget, "%s", c.Detail)
	}
}

// checkBudgetMax is rule 5's max clause: a param reference to an int.
func (v *validator) checkBudgetMax(id, ref string) {
	refs, err := Refs(ref)
	if err != nil || len(refs) != 1 || !IsSingleRef(ref) || refs[0].Root != "params" {
		v.add(id, RuleBudget, "budget max %q is not an int param", ref)
		return
	}
	p, ok := v.def.Params[refs[0].Attr]
	if !ok || p.Kind != ParamInt {
		v.add(id, RuleBudget, "budget max %s is not an int param", refText(refs[0]))
	}
}

// checkInputs is rule 6: each input agrees with what was given.
func (v *validator) checkInputs() {
	given := v.env.Given
	if given == nil {
		return
	}
	v.checkInput("plans", v.def.Inputs.Plans, given.Plans)
	v.checkInput("task", v.def.Inputs.Task, given.Task)
}

func (v *validator) checkInput(name string, mode InputMode, given bool) {
	switch {
	case mode == InputRequired && !given:
		v.add("", RuleInputs, "%s is required but was not given", name)
	case mode == InputNone && given:
		v.add("", RuleInputs, "%s is none but was given", name)
	}
}

// checkForks is rule 7: each fork child resolves and validates under the same
// rules with its own inputs.
func (v *validator) checkForks() {
	for _, id := range sortedKeys(v.def.Steps) {
		step := v.def.Steps[id]
		if step.Fork == nil || len(step.Kinds()) != 1 {
			continue
		}
		if step.Fork.Each != "" {
			v.checkForkChild(id, step.Fork.Workflow, &Given{Plans: true})
			continue
		}
		for _, child := range step.Fork.Children {
			v.checkForkChild(id, child.Workflow, &Given{Plans: len(child.Plans) > 0, Task: child.Task != ""})
		}
	}
}

// checkForkChild resolves one fork child and wraps its problems under the fork
// step.
func (v *validator) checkForkChild(id, name string, given *Given) {
	if v.env.Workflow == nil {
		v.add(id, RuleFork, "fork-child: %s: does not resolve", name)
		return
	}
	child, ok := v.env.Workflow(name)
	if !ok {
		v.add(id, RuleFork, "fork-child: %s: does not resolve", name)
		return
	}
	if v.visited[name] {
		v.add(id, RuleFork, "fork-child: %s: a workflow cannot fork itself", name)
		return
	}
	visited := make(map[string]bool, len(v.visited)+1)
	for k := range v.visited {
		visited[k] = true
	}
	visited[name] = true
	childEnv := v.env
	childEnv.Given = given
	for _, p := range validate(child, childEnv, visited) {
		v.add(id, RuleFork, "fork-child: %s: %s", name, p.String())
	}
}

// checkWhens is rule 8: a when is exactly one reference to a bool param.
func (v *validator) checkWhens() {
	for _, id := range sortedKeys(v.def.Steps) {
		step := v.def.Steps[id]
		if kindOf(step) != "when" {
			continue
		}
		if !IsSingleRef(step.When) {
			v.add(id, RuleWhen, "when is not exactly one reference")
			continue
		}
		refs, err := Refs(step.When)
		if err != nil || len(refs) != 1 {
			v.add(id, RuleWhen, "when is not exactly one reference")
			continue
		}
		ref := refs[0]
		if ref.Root != "params" {
			v.add(id, RuleWhen, "when reads %s; only a param is allowed", refText(ref))
			continue
		}
		p, ok := v.def.Params[ref.Attr]
		if !ok {
			v.add(id, RuleWhen, "when names unknown param %q", ref.Attr)
			continue
		}
		if p.Kind != ParamBool {
			v.add(id, RuleWhen, "when names %q, which is not a bool", ref.Attr)
		}
	}
}

// cut splits s at the first instance of sep.
func cut(s, sep string) (string, string) {
	i := strings.Index(s, sep)
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+len(sep):]
}

// containsStr reports whether a list carries a string.
func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
