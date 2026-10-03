// Package workflow owns a user-defined chain workflow: its definition types,
// the YAML/JSON parser that reads them, references and params, and the shipped
// default. It is pure and does no I/O.
package workflow

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// InputMode says whether a chain input is required, optional or absent.
type InputMode string

// The input modes a workflow declares.
const (
	InputNone     InputMode = "none"
	InputOptional InputMode = "optional"
	InputRequired InputMode = "required"
)

// Inputs declares the two inputs a chain can be given.
type Inputs struct {
	Plans InputMode `json:"plans"`
	Task  InputMode `json:"task"`
}

// ParamKind is the type of a param's value.
type ParamKind string

// The kinds a param may have.
const (
	ParamBool   ParamKind = "bool"
	ParamInt    ParamKind = "int"
	ParamString ParamKind = "string"
)

// Param is a named workflow value with a fixed kind.
type Param struct {
	Kind ParamKind
	Bool bool
	Int  int
	Str  string
}

// MarshalJSON writes a param as its bare value, the form a workflow stores.
func (p Param) MarshalJSON() ([]byte, error) {
	switch p.Kind {
	case ParamBool:
		return json.Marshal(p.Bool)
	case ParamInt:
		return json.Marshal(p.Int)
	case ParamString:
		return json.Marshal(p.Str)
	default:
		return nil, fmt.Errorf("workflow: param has unknown kind %q", string(p.Kind))
	}
}

// Definition is one workflow: what it is called, what it takes, and its steps.
type Definition struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Inputs      Inputs           `json:"inputs"`
	Params      map[string]Param `json:"params,omitempty"`
	Start       string           `json:"start"`
	Steps       map[string]Step  `json:"steps"`
}

// Step is one node of a workflow graph. It keeps every field that was present,
// so a step with more than one kind parses and Validate reports it by name.
type Step struct {
	Run     string
	Check   string
	ForEach string
	When    string
	Fork    *Fork
	Seed    string
	Budget  *Budget
	On      map[string]Target
}

// Kinds lists the kind keys this step carries, in a fixed order.
func (s Step) Kinds() []string {
	kinds := make([]string, 0, 5)
	if s.Run != "" {
		kinds = append(kinds, "run")
	}
	if s.Check != "" {
		kinds = append(kinds, "check")
	}
	if s.ForEach != "" {
		kinds = append(kinds, "for-each")
	}
	if s.Fork != nil {
		kinds = append(kinds, "fork")
	}
	if s.When != "" {
		kinds = append(kinds, "when")
	}
	return kinds
}

// MarshalJSON writes only the fields a step carries, so a stored workflow
// parses back to the same step.
func (s Step) MarshalJSON() ([]byte, error) {
	obj := map[string]any{}
	if s.Run != "" {
		obj["run"] = s.Run
	}
	if s.Check != "" {
		obj["check"] = s.Check
	}
	if s.ForEach != "" {
		obj["for-each"] = s.ForEach
	}
	if s.When != "" {
		obj["when"] = s.When
	}
	if s.Fork != nil {
		obj["fork"] = s.Fork
	}
	if s.Seed != "" {
		obj["seed"] = s.Seed
	}
	if s.Budget != nil {
		obj["budget"] = s.Budget
	}
	if s.On != nil {
		obj["on"] = s.On
	}
	return json.Marshal(obj)
}

// TargetKind tells a step target from done and from a halt.
type TargetKind string

// The kinds of target an on entry may name.
const (
	TargetStep TargetKind = "step"
	TargetDone TargetKind = "done"
	TargetHalt TargetKind = "halt"
)

// Target is what an on entry does: go to a step, finish, or halt with a reason.
type Target struct {
	Kind   TargetKind
	Step   string
	Reason string
}

// StepTarget returns a target that goes to the named step.
func StepTarget(step string) Target { return Target{Kind: TargetStep, Step: step} }

// DoneTarget returns the target that finishes the chain.
func DoneTarget() Target { return Target{Kind: TargetDone} }

// HaltTarget returns a target that halts the chain with a reason.
func HaltTarget(reason string) Target { return Target{Kind: TargetHalt, Reason: reason} }

// MarshalJSON writes a target as a step id, the word done, or {halt: reason}.
func (t Target) MarshalJSON() ([]byte, error) {
	switch t.Kind {
	case TargetStep:
		return json.Marshal(t.Step)
	case TargetDone:
		return json.Marshal("done")
	case TargetHalt:
		return json.Marshal(struct {
			Halt string `json:"halt"`
		}{t.Reason})
	default:
		return nil, fmt.Errorf("workflow: target has unknown kind %q", string(t.Kind))
	}
}

// Limit is a budget's max: a count, or a reference to a param holding one.
type Limit struct {
	Count int
	Ref   string
}

// Text renders a limit as it is written in a definition, so a wording that
// quotes a budget quotes its own expression rather than a resolved number.
func (l Limit) Text() string {
	if l.Ref == "" {
		return strconv.Itoa(l.Count)
	}
	return l.Ref
}

// Per says which step entries reset a budget's count.
type Per struct {
	Chain bool
	Steps []string
}

// MarshalJSON writes per as chain, a step id, or a list of step ids.
func (p Per) MarshalJSON() ([]byte, error) {
	switch {
	case p.Chain:
		return json.Marshal("chain")
	case len(p.Steps) == 1:
		return json.Marshal(p.Steps[0])
	case len(p.Steps) > 1:
		return json.Marshal(p.Steps)
	default:
		return nil, fmt.Errorf("workflow: budget per names no step")
	}
}

// Budget bounds a step's visits: past Max it goes to Then, and Per resets the
// count whenever one of its steps is entered.
type Budget struct {
	Max  Limit
	Per  Per
	Then Target
}

// MarshalJSON writes a budget as the max/per/then object a workflow stores.
func (b Budget) MarshalJSON() ([]byte, error) {
	max := any(b.Max.Count)
	if b.Max.Ref != "" {
		max = b.Max.Ref
	}
	return json.Marshal(map[string]any{"max": max, "per": b.Per, "then": b.Then})
}

// Fork is a step that runs sub-chains: one per item of a list, or a fixed list
// of children.
type Fork struct {
	Each     string
	Workflow string
	Children []ForkChild
}

// ForkChild is one fixed child of a fork: a workflow plus its inputs.
type ForkChild struct {
	Workflow string   `json:"workflow"`
	Plans    []string `json:"plans,omitempty"`
	Task     string   `json:"task,omitempty"`
}

// MarshalJSON writes a fork in whichever of its two forms it carries.
func (f Fork) MarshalJSON() ([]byte, error) {
	if f.Children != nil {
		return json.Marshal(struct {
			Children []ForkChild `json:"children"`
		}{f.Children})
	}
	return json.Marshal(struct {
		Each     string `json:"each"`
		Workflow string `json:"workflow"`
	}{f.Each, f.Workflow})
}
