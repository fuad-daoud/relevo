package config

import (
	"encoding/json"
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

	drops := DroppedActorCandidates(old, new)
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

	drops := DroppedActorCandidates(old, new)
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

	if drops := DroppedActorCandidates(old, new); len(drops) != 0 {
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

	if drops := DroppedActorCandidates(old, new); len(drops) != 0 {
		t.Errorf("drops = %q, want none when the write adds candidates back", drops)
	}
}

func TestDroppedActorCandidatesWithoutActorsIsNil(t *testing.T) {
	t.Parallel()

	old := docOf(map[Section]string{Candidates: guardCandidates})
	new := docOf(map[Section]string{Candidates: `[]`})

	if drops := DroppedActorCandidates(old, new); len(drops) != 0 {
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

	drops := DroppedActorCandidates(old, new)
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

	drops := DroppedActorCandidates(old, new)
	if len(drops) != 2 {
		t.Fatalf("drops = %q, want two", drops)
	}
	if !strings.HasPrefix(drops[0], "actor alpha ") || !strings.HasPrefix(drops[1], "actor zulu ") {
		t.Errorf("drops = %q, want them in sorted actor order", drops)
	}
}
