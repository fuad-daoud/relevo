package chain

import (
	"fmt"
	"slices"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// PartOf names the part a member fills, from the chain row's columns.
func PartOf(c db.ChainRow, name string) string {
	if name == "" {
		return ""
	}
	switch name {
	case c.Builder:
		return MemberBuilder
	case c.Reviewer:
		return MemberReviewer
	case c.Planner:
		return MemberPlanner
	case c.Security:
		return MemberSecurity
	}
	return ""
}

// MemberName is PartOf's inverse: the binding name the row holds for
// a part. "" when the part is not on this chain (the security member of a chain
// whose phase is off).
func MemberName(c db.ChainRow, part string) string {
	switch part {
	case MemberBuilder:
		return c.Builder
	case MemberReviewer:
		return c.Reviewer
	case MemberPlanner:
		return c.Planner
	case MemberSecurity:
		return c.Security
	}
	return ""
}

// StepNote names the chain step a member round runs, for its prompt entry's
// note: the part the member fills. A binding that is not a chain member reads
// as the plain word.
func StepNote(tx *store.Tx, name string) string {
	c, err := tx.ChainByMember(name)
	if err == nil {
		if part := PartOf(c, name); part != "" {
			return "chain " + part
		}
	}
	return "chain"
}

// MemberChain resolves member to the chain that names it, reading through
// byMember (production: Store.ChainByMember). ok is false for a name that is
// in no chain and for every read failure alike, so callers treat both as "no
// member row to consult" instead of a fault they must handle -- a hint or a
// status may degrade to the ordinary names, never fail on it.
func MemberChain(byMember func(string) (db.ChainRow, error), member string) (db.ChainRow, bool) {
	c, err := byMember(member)
	if err != nil {
		return db.ChainRow{}, false
	}
	return c, true
}

// MemberHint picks the hint one waiting row names for member. A member whose
// owning chain is finished -- done or stopped -- is no longer the chain's to
// hold: `relevo done <member>` is allowed on such a member, so the hint names
// both the look and the clear, in the order a person runs them. A running or
// halted chain still owns its members' rounds -- done on a running-chain
// member is refused -- so their rows, like every non-member's, keep the
// status-only hint.
func MemberHint(member string, c db.ChainRow, memberOf bool) string {
	if memberOf {
		switch Status(c.Status) {
		case StatusDone, StatusStopped:
			return fmt.Sprintf("relevo status --name %s; relevo done %s clears it", member, member)
		}
	}
	return "relevo status --name " + member
}

// PlannedMember is one binding a chain would create: the actor it runs and the
// name it takes. Writer marks a writer actor, whose steps take turns on the
// chain's single tree.
type PlannedMember struct {
	Actor  string
	Name   string
	Writer bool
}

// MemberNames names the members a workflow would create for a chain: one
// binding per actor its run steps reach, the chain's own name for its writer
// and "<chain>-<actor>" for every other actor. The shipped default names its
// reviewer, planner and security actors with the -rev, -plan and -sec
// suffixes. An actor the registry does not define is refused, because a
// member without one cannot be resolved.
func MemberNames(name string, def workflow.Definition, actors map[string]workflow.ActorInfo) ([]PlannedMember, error) {
	used := workflow.UsedActors(def)
	for _, actor := range used {
		if _, ok := actors[actor]; !ok {
			return nil, refusal.New("workflow %s runs actor %q, which is not configured", def.Name, actor)
		}
	}
	keeper := WriterKeeper(def, used, actors)
	parts := DefaultParts(def)
	out := make([]PlannedMember, 0, len(used))
	for _, actor := range used {
		memberName := name + "-" + actor
		if actor == keeper {
			memberName = name
		} else if part := parts[actor]; part != "" {
			memberName = name + defaultSuffix(part)
		}
		out = append(out, PlannedMember{Actor: actor, Name: memberName, Writer: actors[actor].Shape == workflow.ShapeWriter})
	}
	return out, nil
}

// DefaultParts maps each actor the shipped default names to the part its
// member fills: the reviewer, planner and security params, in the order a
// legacy row's columns walk them. The first part an actor fills wins, so an
// actor named by two parts keeps the reviewer. Any other definition has no
// legacy parts, so every one of its members keeps its actor as its part.
func DefaultParts(def workflow.Definition) map[string]string {
	if def.Name != workflow.Default().Name {
		return nil
	}
	parts := map[string]string{}
	for _, part := range []string{MemberReviewer, MemberPlanner, MemberSecurity} {
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

// defaultSuffix is the name suffix a default workflow's part carries.
func defaultSuffix(part string) string {
	switch part {
	case MemberReviewer:
		return "-rev"
	case MemberPlanner:
		return "-plan"
	case MemberSecurity:
		return "-sec"
	}
	return ""
}

// WriterKeeper names the writer member that takes the chain's own name.
// A single writer keeps it; with several, the one the builder param names when
// the workflow has that param; otherwise the first in sorted actor order. No
// writer leaves the name free, so every member takes the "<chain>-<actor>"
// form.
func WriterKeeper(def workflow.Definition, used []string, actors map[string]workflow.ActorInfo) string {
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
	if HasParam(def, "builder") {
		named := workflow.RenderParams(def, "{{params.builder}}")
		if actors[named].Shape == workflow.ShapeWriter && slices.Contains(writers, named) {
			return named
		}
	}
	return writers[0]
}

func HasParam(def workflow.Definition, name string) bool {
	_, ok := def.Params[name]
	return ok
}
