package workflow

import "strings"

// stepTemplate is one string field of a step that may carry a reference, with
// the name a problem names it by.
type stepTemplate struct {
	name string
	text string
}

// templates returns a step's reference-bearing fields, in a fixed order.
func (v *validator) templates(id string) []stepTemplate {
	step := v.def.Steps[id]
	var out []stepTemplate
	add := func(name, text string) {
		if text != "" {
			out = append(out, stepTemplate{name, text})
		}
	}
	add("run", step.Run)
	add("check", step.Check)
	add("for-each", step.ForEach)
	add("when", step.When)
	add("seed", step.Seed)
	if step.Budget != nil {
		add("budget.max", step.Budget.Max.Ref)
		add("budget.then", step.Budget.Then.Reason)
	}
	for _, key := range sortedKeys(step.On) {
		add("on "+key, step.On[key].Reason)
	}
	if step.Fork != nil {
		add("fork.each", step.Fork.Each)
	}
	return out
}

// checkReferences is rule 4: every reference resolves, and a step reference
// names a step that strictly dominates the referencing step. A halt reason
// reads params only.
func (v *validator) checkReferences() {
	for _, id := range sortedKeys(v.def.Steps) {
		for _, f := range v.templates(id) {
			refs, err := Refs(f.text)
			if err != nil {
				continue
			}
			halt := f.name == "budget.then" || strings.HasPrefix(f.name, "on ")
			for _, ref := range refs {
				if halt && ref.Root != "params" {
					v.add(id, RuleRef, "%s reads %s; a halt reason reads params only", f.name, refText(ref))
					continue
				}
				v.checkRef(id, f.name, ref)
			}
		}
	}
}

// checkRef resolves one reference against the workflow.
func (v *validator) checkRef(step, field string, ref Ref) {
	switch ref.Root {
	case "params":
		if _, ok := v.def.Params[ref.Attr]; !ok {
			v.add(step, RuleRef, "%s names unknown param %q", field, ref.Attr)
		}
	case "task":
		if v.def.Inputs.Task != InputRequired {
			v.add(step, RuleRef, "%s names the task input, which is not required", field)
		}
	case "chain":
		switch ref.Attr {
		case "diff", "base", "branch":
		default:
			v.add(step, RuleRef, "%s names chain.%s, which is not chain state", field, ref.Attr)
		}
	case "plans":
		if ref.Attr == "all" {
			if v.def.Inputs.Plans != InputRequired {
				v.add(step, RuleRef, "%s names plans.all, which needs a required plans input", field)
			}
			return
		}
		v.checkStepRef(step, field, ref)
	default:
		v.checkStepRef(step, field, ref)
	}
}

// checkStepRef resolves a <step>.<attr> reference: the step exists, the
// attribute fits its kind and shape, and the step strictly dominates the
// referencing step.
func (v *validator) checkStepRef(step, field string, ref Ref) {
	if _, ok := v.def.Steps[ref.Root]; !ok {
		v.add(step, RuleRef, "%s names unknown step %q", field, ref.Root)
		return
	}
	if !v.attrFits(ref.Root, ref.Attr) {
		v.add(step, RuleRef, "%s names %s.%s, which %s does not expose", field, ref.Root, ref.Attr, ref.Root)
		return
	}
	if dom := v.dominators(); dom != nil && !dominates(dom, ref.Root, step) {
		v.add(step, RuleRef, "%s names %s, which does not dominate %s", field, ref.Root, step)
	}
}

// attrFits reports whether a step exposes an attribute: a declared artifact, or
// the built-in artifact of its kind and shape.
func (v *validator) attrFits(id, attr string) bool {
	step := v.def.Steps[id]
	if actor, ok := v.actor(step); ok && actor.Outputs[attr].Kind == OutputArtifact {
		return true
	}
	switch kindOf(step) {
	case "run":
		actor, ok := v.actor(step)
		if !ok {
			return false
		}
		switch actor.Shape {
		case ShapeWriter:
			return attr == "report" || attr == "diff"
		case ShapeReader:
			return attr == "output"
		}
	case "check":
		return attr == "log"
	case "fork":
		return attr == "conflict"
	case "for-each":
		return attr == "current"
	}
	return false
}

// isArtifact reports whether a step's actor declares attr as an artifact.
func (v *validator) isArtifact(id, attr string) bool {
	step, ok := v.def.Steps[id]
	if !ok {
		return false
	}
	actor, ok := v.actor(step)
	return ok && actor.Outputs[attr].Kind == OutputArtifact
}

// refText renders a reference back into braces for a problem detail.
func refText(ref Ref) string {
	if ref.Attr == "" {
		return "{{" + ref.Root + "}}"
	}
	return "{{" + ref.Root + "." + ref.Attr + "}}"
}
