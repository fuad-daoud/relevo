package config

import (
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// DroppedActorCandidates names every actor candidate reference oldDoc
// resolves and newDoc does not, as one "actor <a> names candidate <c>" line
// per drop. An absent candidates section is the empty set Load decodes, so a
// write removing the section reads as dropping everything an actor named.
// Actors come from newDoc, falling back to oldDoc when newDoc carries no
// actors section: a candidates-only write must not be excused by a write also
// dropping the actors alongside.
//
// Candidate names and harness/provider/model tokens resolve through
// candidate.Set.Resolve, the same resolution Load performs, so the guard
// agrees with the read path on what "resolves" means.
//
// A reference already broken in oldDoc is not a drop: it was broken before
// this write, and refusing would leave a machine from before the guard unable
// to repair itself. Adding an actor naming a candidate nobody defines yet is
// allowed for the same reason: an actor may precede its candidate.
//
// A new list that does not parse is refused with its parse error before any
// drop is computed, so a bad write is never read as dropping what actors
// name. An old list that does not parse instead reads as the empty set, so
// every actor reference counts as already broken and a repair write stays
// allowed through to the store's own refusal.
func DroppedActorCandidates(oldDoc, newDoc Doc) ([]string, error) {
	oldSet, err := candidateSet(oldDoc)
	if err != nil {
		// An empty array is the section's missing-file form and cannot fail
		// to parse, so the fallback is the empty set.
		oldSet, _, _ = candidate.Parse(FileName(Candidates), []byte("[]"))
	}
	newSet, err := candidateSet(newDoc)
	if err != nil {
		return nil, err
	}

	body, ok := newDoc[Actors]
	if !ok {
		body, ok = oldDoc[Actors]
	}
	if !ok {
		return nil, nil
	}
	actors, _, err := roles.ParseActors(body)
	if err != nil {
		return nil, nil
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
	return drops, nil
}

// candidateSet parses doc's candidates section, an absent section being the
// empty set. A present section that does not parse returns its error
// unchanged, so the refusal names the candidate it derives from.
func candidateSet(doc Doc) (*candidate.Set, error) {
	body, ok := doc[Candidates]
	if !ok {
		body = []byte("[]")
	}
	set, _, err := candidate.Parse(FileName(Candidates), body)
	if err != nil {
		return nil, invalidValueOf(err)
	}
	return set, nil
}
