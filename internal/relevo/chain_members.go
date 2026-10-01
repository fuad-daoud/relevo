package relevo

import (
	"slices"

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
// and "<chain>-<actor>" for every other actor. An actor the registry does not
// define is refused, because a member without one cannot be resolved.
func chainMemberNames(chain string, def workflow.Definition, actors map[string]workflow.ActorInfo) ([]plannedMember, error) {
	used := workflow.UsedActors(def)
	for _, actor := range used {
		if _, ok := actors[actor]; !ok {
			return nil, refuse("workflow %s runs actor %q, which is not configured", def.Name, actor)
		}
	}
	keeper := chainWriterKeeper(def, used, actors)
	out := make([]plannedMember, 0, len(used))
	for _, actor := range used {
		name := chain + "-" + actor
		if actor == keeper {
			name = chain
		}
		out = append(out, plannedMember{Actor: actor, Name: name, Writer: actors[actor].Shape == workflow.ShapeWriter})
	}
	return out, nil
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
// name cap, less the "<chain>-" prefix and the longest actor the chain runs.
// The longest actor decides it, because every non-writer member name carries
// that actor as its suffix.
func chainNameCap(members []plannedMember) int {
	longest := 0
	for _, m := range members {
		if len(m.Actor) > longest {
			longest = len(m.Actor)
		}
	}
	return store.MaxAgentNameLen - 1 - longest
}
