package config

import (
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// DroppedActorCandidates names every actor candidate reference that oldDoc
// resolves and newDoc does not, as one "actor <a> names candidate <c>" line per
// drop. An absent candidates section is the empty set, exactly what Load
// decodes, so a write that removes the section reads as a drop of everything an
// actor named. Actors come from newDoc, falling back to oldDoc when newDoc
// carries no actors section: a candidates-only write must not be excused by a
// write that also happens to drop the actors alongside.
//
// Both candidate names and harness/provider/model tokens go through
// candidate.Set.Resolve, which is the same resolution Load performs, so the
// guard agrees with the read path on what "resolves" means.
//
// A reference that already failed to resolve in oldDoc is not a drop: it was
// broken before this write, and refusing would make a machine that predates
// the guard unable to repair its own config. The reverse direction -- adding
// an actor that names a candidate nobody defines yet -- is allowed for the same
// reason: an actor may be provisioned before its candidate.
//
// Any parse error returns nil: the callers validate the document before they
// guard it, and Put re-validates inside the transaction, so a body this
// function cannot read is already refused upstream and reporting a drop from it
// would only add noise.
func DroppedActorCandidates(oldDoc, newDoc Doc) []string {
	oldSet := candidateSet(oldDoc)
	newSet := candidateSet(newDoc)

	body, ok := newDoc[Actors]
	if !ok {
		body, ok = oldDoc[Actors]
	}
	if !ok {
		return nil
	}
	actors, _, err := roles.ParseActors(body)
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(actors))
	for name := range actors {
		names = append(names, name)
	}
	sort.Strings(names)

	var drops []string
	for _, name := range names {
		for _, e := range actors[name].Candidates {
			// An off entry still names the candidate, so it counts.
			if _, err := oldSet.Resolve(e.Candidate); err != nil {
				continue
			}
			if _, err := newSet.Resolve(e.Candidate); err == nil {
				continue
			}
			drops = append(drops, fmt.Sprintf("actor %s names candidate %s", name, e.Candidate))
		}
	}
	return drops
}

// candidateSet parses doc's candidates section, treating an absent section as
// the empty set. A parse error yields the empty set too: the caller has already
// validated the document, and an unreadable body is refused there, not here.
func candidateSet(doc Doc) *candidate.Set {
	body, ok := doc[Candidates]
	if !ok {
		body = []byte("[]")
	}
	if set, _, err := candidate.Parse(FileName(Candidates), body); err == nil {
		return set
	}
	// An empty array is the section's missing-file form and cannot fail to
	// parse, so the fallback is the empty set.
	set, _, _ := candidate.Parse(FileName(Candidates), []byte("[]"))
	return set
}
