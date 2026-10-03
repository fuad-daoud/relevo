package workflow

// checkForks is rule 7: each fork child resolves and validates under the same
// rules with its own inputs, each source is valid, and the workflow has a
// writer run step.
func (v *validator) checkForks() {
	hasWriter := v.hasWriterRunStep()
	for _, id := range sortedKeys(v.def.Steps) {
		step := v.def.Steps[id]
		if step.Fork == nil || len(step.Kinds()) != 1 {
			continue
		}
		if !hasWriter {
			v.add(id, RuleFork, "workflow has no writer run step")
		}
		if step.Fork.Each != "" {
			v.checkForEachSource(id, step.Fork.Each)
			v.checkForkChild(id, step.Fork.Workflow, &Given{Plans: true})
			continue
		}
		for _, child := range step.Fork.Children {
			v.checkForkChild(id, child.Workflow, &Given{Plans: len(child.Plans) > 0, Task: child.Task != ""})
		}
	}
}

// hasWriterRunStep reports whether the workflow contains at least one run step
// with a writer actor.
func (v *validator) hasWriterRunStep() bool {
	for _, step := range v.def.Steps {
		if kindOf(step) == "run" {
			if actor, ok := v.actor(step); ok && actor.Shape == ShapeWriter {
				return true
			}
		}
	}
	return false
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
