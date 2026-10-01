package workflow

import "fmt"

// RefTarget is a resolved reference: the value it names. Inline carries text
// rendered as it is, Key one path a caller can open, and List several. Exactly
// one of the three carries the value.
type RefTarget struct {
	Inline string
	Key    string
	List   []string
}

// ResolveRef resolves one reference against a run's definition and state: a
// param, a for-each step's iteration, or a step's latest artifacts. The task
// input and the chain's base, branch and diff are not workflow state; a caller
// resolves those from the chain record, so they are an error here.
func ResolveRef(def Definition, s State, ref Ref) (RefTarget, error) {
	switch ref.Root {
	case "params":
		p, ok := def.Params[ref.Attr]
		if !ok {
			return RefTarget{}, fmt.Errorf("workflow: unknown param %q", ref.Attr)
		}
		return RefTarget{Inline: p.render()}, nil
	case "task":
		return RefTarget{}, fmt.Errorf("workflow: the task input is not workflow state")
	case "chain":
		return RefTarget{}, fmt.Errorf("workflow: chain.%s is not workflow state", ref.Attr)
	}

	step, ok := def.Steps[ref.Root]
	if !ok {
		return RefTarget{}, fmt.Errorf("workflow: unknown step %q", ref.Root)
	}
	switch ref.Attr {
	case "current":
		it, ok := s.Iter[ref.Root]
		if !ok || it.Index < 0 || it.Index >= len(it.Items) {
			return RefTarget{}, fmt.Errorf("workflow: %s has no current item", ref.Root)
		}
		return RefTarget{Key: it.Items[it.Index]}, nil
	case "all":
		if step.ForEach == "" {
			return RefTarget{}, fmt.Errorf("workflow: %s.all is not a for-each list", ref.Root)
		}
		return RefTarget{List: append([]string(nil), s.Iter[ref.Root].Items...)}, nil
	}

	r, ok := s.Results[ref.Root]
	if !ok {
		return RefTarget{}, fmt.Errorf("workflow: %s has not run", ref.Root)
	}
	keys := r.Artifacts[ref.Attr]
	if len(keys) == 0 {
		return RefTarget{}, fmt.Errorf("workflow: %s exposes no artifact %q", ref.Root, ref.Attr)
	}
	if len(keys) == 1 {
		return RefTarget{Key: keys[0]}, nil
	}
	return RefTarget{List: append([]string(nil), keys...)}, nil
}
