package relevo

import (
	"slices"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// plannedMember is one binding a chain would create: the actor it runs and the
// name it takes. Writer marks a writer actor, whose steps take turns on the
// chain's single tree.
type plannedMember struct {
	Actor  string
	Name   string
	Writer bool
}

// chainMemberNames names the members a workflow would create for a chain: one
// binding per actor its run steps reach, the chain's own name for its writer
// and "<chain>-<actor>" for every other actor. The shipped default names its
// reviewer, planner and security actors with the suffixes a legacy chain
// always wrote. An actor the registry does not define is refused, because a
// member without one cannot be resolved.
func chainMemberNames(name string, def workflow.Definition, actors map[string]workflow.ActorInfo) ([]plannedMember, error) {
	used := workflow.UsedActors(def)
	for _, actor := range used {
		if _, ok := actors[actor]; !ok {
			return nil, refuse("workflow %s runs actor %q, which is not configured", def.Name, actor)
		}
	}
	keeper := chainWriterKeeper(def, used, actors)
	parts := chainDefaultParts(def)
	out := make([]plannedMember, 0, len(used))
	for _, actor := range used {
		memberName := name + "-" + actor
		if actor == keeper {
			memberName = name
		} else if part := parts[actor]; part != "" {
			memberName = name + chainDefaultSuffix(part)
		}
		out = append(out, plannedMember{Actor: actor, Name: memberName, Writer: actors[actor].Shape == workflow.ShapeWriter})
	}
	return out, nil
}

// chainDefaultParts maps each actor the shipped default names to the part its
// member fills: the reviewer, planner and security params, in the order a
// legacy row's columns walk them. The first part an actor fills wins, so an
// actor named by two parts keeps the reviewer. Any other definition has no
// legacy parts, so every one of its members keeps its actor as its part.
func chainDefaultParts(def workflow.Definition) map[string]string {
	if def.Name != workflow.Default().Name {
		return nil
	}
	parts := map[string]string{}
	for _, part := range []string{chain.MemberReviewer, chain.MemberPlanner, chain.MemberSecurity} {
		p, ok := def.Params[part]
		if !ok || p.Kind != workflow.ParamString || p.Str == "" {
			continue
		}
		if _, seen := parts[p.Str]; !seen {
			parts[p.Str] = part
		}
	}
	return parts
}

// chainDefaultSuffix is the name suffix a default workflow's part carries.
func chainDefaultSuffix(part string) string {
	switch part {
	case chain.MemberReviewer:
		return "-rev"
	case chain.MemberPlanner:
		return "-plan"
	case chain.MemberSecurity:
		return "-sec"
	}
	return ""
}

// chainWriterKeeper names the writer member that takes the chain's own name.
// A single writer keeps it; with several, the one the builder param names when
// the workflow has that param; otherwise the first in sorted actor order. No
// writer leaves the name free, so every member takes the "<chain>-<actor>"
// form.
func chainWriterKeeper(def workflow.Definition, used []string, actors map[string]workflow.ActorInfo) string {
	var writers []string
	for _, actor := range used {
		if actors[actor].Shape == workflow.ShapeWriter {
			writers = append(writers, actor)
		}
	}
	switch len(writers) {
	case 0:
		return ""
	case 1:
		return writers[0]
	}
	if hasChainParam(def, "builder") {
		named := workflow.RenderParams(def, "{{params.builder}}")
		if actors[named].Shape == workflow.ShapeWriter && slices.Contains(writers, named) {
			return named
		}
	}
	return writers[0]
}

// chainNameCap is the longest chain name a set of members allows: the binding
// name cap, less the longest member suffix. The members' own names decide it,
// because a member name is the chain's name plus a fixed suffix: "-plan" for a
// default chain's planner, an actor's own name for a custom workflow's reader.
func chainNameCap(name string, members []plannedMember) int {
	return store.MaxAgentNameLen - len(longestMemberSuffix(name, members))
}

// longestMemberSuffix is the longest suffix beyond the chain's own name that a
// set of member names carries: what the chain-name cap leaves room for, and
// the suffix a refusal names.
func longestMemberSuffix(name string, members []plannedMember) string {
	longest := ""
	for _, m := range members {
		if len(m.Name) < len(name) {
			continue
		}
		if suffix := m.Name[len(name):]; len(suffix) > len(longest) {
			longest = suffix
		}
	}
	return longest
}
