package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The bodies below name one candidate per token form, so the guard's Resolve
// call -- not a string comparison -- is what decides whether a reference still
// resolves.
const (
	guardCandidates = `[{"harness":"claude","provider":"p","model":"m"},{"harness":"agy","provider":"q","model":"n"}]`
	guardAgents     = `{"w":{"shape":"writer","native":{"claude":{"agent":"w"}}}}`
)

func docOf(sections map[Section]string) Doc {
	doc := Doc{}
	for sec, body := range sections {
		doc[sec] = json.RawMessage(body)
	}
	return doc
}

func TestDroppedActorCandidatesNamesNewlyDropped(t *testing.T) {
	t.Parallel()

	old := docOf(map[Section]string{
		Candidates: guardCandidates,
		Agents:     guardAgents,
		Actors:     `{"builder":{"agent":"w","candidates":["m","agy/q/n"]}}`,
	})
	// A name entry is dropped, and so is a harness/provider/model token. An
	// off entry names its candidate just as an on one does, so it counts too.
	new := docOf(map[Section]string{
		Candidates: `[{"harness":"agy","provider":"q","model":"n"}]`,
		Actors:     `{"builder":{"agent":"w","candidates":["m","agy/q/n"]}}`,
	})

	drops, err := DroppedActorCandidates(old, new)
	if err != nil {
		t.Fatalf("DroppedActorCandidates: %v", err)
	}
	want := []string{"actor builder names candidate m"}
	if len(drops) != len(want) || drops[0] != want[0] {
		t.Fatalf("drops = %q, want %q", drops, want)
	}
}

func TestDroppedActorCandidatesCountsOffEntries(t *testing.T) {
	t.Parallel()

	old := docOf(map[Section]string{
		Candidates: guardCandidates,
		Actors:     `{"builder":{"agent":"w","candidates":[{"candidate":"m","off":true}]}}`,
	})
	new := docOf(map[Section]string{
		Candidates: `[{"harness":"agy","provider":"q","model":"n"}]`,
		Actors:     `{"builder":{"agent":"w","candidates":[{"candidate":"m","off":true}]}}`,
	})

	drops, err := DroppedActorCandidates(old, new)
	if err != nil {
		t.Fatalf("DroppedActorCandidates: %v", err)
	}
	if len(drops) != 1 || !strings.Contains(drops[0], "builder") || !strings.Contains(drops[0], "m") {
		t.Errorf("drops = %q, want the off entry's drop named", drops)
	}
}

func TestDroppedActorCandidatesIgnoresAlreadyDanglingRefs(t *testing.T) {
	t.Parallel()

	// "gone" resolved in neither document: a machine that predates the guard
	// must stay able to write its own repair, so an already-broken reference
	// is not a drop.
	old := docOf(map[Section]string{
		Candidates: guardCandidates,
		Actors:     `{"builder":{"agent":"w","candidates":["gone"]}}`,
	})
	new := docOf(map[Section]string{
		Candidates: guardCandidates,
		Actors:     `{"builder":{"agent":"w","candidates":["gone"]}}`,
	})

	drops, err := DroppedActorCandidates(old, new)
	if err != nil {
		t.Fatalf("DroppedActorCandidates: %v", err)
	}
	if len(drops) != 0 {
		t.Errorf("drops = %q, want none for a reference that never resolved", drops)
	}
}

func TestDroppedActorCandidatesAllowsAddingCandidatesBack(t *testing.T) {
	t.Parallel()

	// The reverse direction: a write that restores candidates an actor names is
	// always allowed, which is how a broken machine repairs itself.
	old := docOf(map[Section]string{
		Actors: `{"builder":{"agent":"w","candidates":["m"]}}`,
	})
	new := docOf(map[Section]string{
		Candidates: guardCandidates,
		Actors:     `{"builder":{"agent":"w","candidates":["m"]}}`,
	})

	drops, err := DroppedActorCandidates(old, new)
	if err != nil {
		t.Fatalf("DroppedActorCandidates: %v", err)
	}
	if len(drops) != 0 {
		t.Errorf("drops = %q, want none when the write adds candidates back", drops)
	}
}

func TestDroppedActorCandidatesWithoutActorsIsNil(t *testing.T) {
	t.Parallel()

	old := docOf(map[Section]string{Candidates: guardCandidates})
	new := docOf(map[Section]string{Candidates: `[]`})

	drops, err := DroppedActorCandidates(old, new)
	if err != nil {
		t.Fatalf("DroppedActorCandidates: %v", err)
	}
	if len(drops) != 0 {
		t.Errorf("drops = %q, want nil when no actor names anything", drops)
	}
}

func TestDroppedActorCandidatesFallsBackToOldActors(t *testing.T) {
	t.Parallel()

	// A candidates-only write carries no actors section, so the stored actors
	// still stand and still name what the write drops.
	old := docOf(map[Section]string{
		Candidates: guardCandidates,
		Actors:     `{"builder":{"agent":"w","candidates":["m"]}}`,
	})
	new := docOf(map[Section]string{Candidates: `[{"harness":"agy","provider":"q","model":"n"}]`})

	drops, err := DroppedActorCandidates(old, new)
	if err != nil {
		t.Fatalf("DroppedActorCandidates: %v", err)
	}
	if len(drops) != 1 || !strings.Contains(drops[0], "builder") {
		t.Errorf("drops = %q, want the stored actor's drop", drops)
	}
}

func TestDroppedActorCandidatesOrdersByActorName(t *testing.T) {
	t.Parallel()

	old := docOf(map[Section]string{
		Candidates: guardCandidates,
		Actors:     `{"zulu":{"agent":"w","candidates":["m"]},"alpha":{"agent":"w","candidates":["m"]}}`,
	})
	new := docOf(map[Section]string{Candidates: `[]`})

	drops, err := DroppedActorCandidates(old, new)
	if err != nil {
		t.Fatalf("DroppedActorCandidates: %v", err)
	}
	if len(drops) != 2 {
		t.Fatalf("drops = %q, want two", drops)
	}
	if !strings.HasPrefix(drops[0], "actor alpha ") || !strings.HasPrefix(drops[1], "actor zulu ") {
		t.Errorf("drops = %q, want them in sorted actor order", drops)
	}
}

func TestDroppedActorCandidatesRefusesAnUnparseableNewListNamingTheCandidate(t *testing.T) {
	t.Parallel()

	old := docOf(map[Section]string{
		Candidates: guardCandidates,
		Actors:     `{"builder":{"agent":"w","candidates":["m","agy/q/n"]}}`,
	})
	new := docOf(map[Section]string{
		Candidates: `[{"harness":"claude","provider":"p","model":"m"},{"harness":"agy","provider":"q","model":"n"},{"name":"inkling-2-small-free-high","harness":"opencode","provider":"opencode","model":"inkling-2-small-free#high"}]`,
		Actors:     `{"builder":{"agent":"w","candidates":["m","agy/q/n"]}}`,
	})

	drops, err := DroppedActorCandidates(old, new)
	if err == nil {
		t.Fatalf("err = nil, want the new list's parse error naming the candidate (drops = %q)", drops)
	}
	if !errors.Is(err, ErrInvalidValue) {
		t.Errorf("err = %v, want it to carry ErrInvalidValue", err)
	}
	if !strings.Contains(err.Error(), "inkling-2-small-free-high") {
		t.Errorf("err = %q, want it to name the candidate", err)
	}
	if len(drops) != 0 {
		t.Errorf("drops = %q, want none: an unparseable new list is never read as dropped", drops)
	}
}

func TestDroppedActorCandidatesAllowsRepairWhenOldListIsUnparseable(t *testing.T) {
	t.Parallel()

	// A stored list that does not parse reads as the empty set, so every
	// actor reference counts as already broken and the repair write is
	// allowed through to the store's own check.
	old := docOf(map[Section]string{
		Candidates: `[{"name":"inkling-2-small-free-high","harness":"opencode","provider":"opencode","model":"x"}]`,
		Actors:     `{"builder":{"agent":"w","candidates":["m"]}}`,
	})
	new := docOf(map[Section]string{
		Candidates: guardCandidates,
		Actors:     `{"builder":{"agent":"w","candidates":["m"]}}`,
	})

	drops, err := DroppedActorCandidates(old, new)
	if err != nil {
		t.Fatalf("DroppedActorCandidates: %v", err)
	}
	if len(drops) != 0 {
		t.Errorf("drops = %q, want none when the old list never resolved", drops)
	}
}
