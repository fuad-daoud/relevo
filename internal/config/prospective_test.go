package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The three bodies below seed a document that resolves: a candidate named m, a
// custom agent an actor names, and that actor.
const (
	prospectiveCandidates = `[{"harness":"claude","provider":"p","model":"m"}]`
	prospectiveAgent      = `{"sec-consult":{"shape":"reader","native":{"claude":{"agent":"sec-consult"}}}}`
	prospectiveActor      = `{"security":{"agent":"sec-consult","candidates":["m"]}}`
)

func TestStoreRefusesRemovingAnAgentAnActorNames(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: prospectiveCandidates,
		Agents:     prospectiveAgent,
		Actors:     prospectiveActor,
	})

	beforeBody, ok, err := s.Body(Agents)
	if err != nil || !ok {
		t.Fatalf("Body(agents) = ok %v err %v, want present", ok, err)
	}
	beforeVersion, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	beforeRevs := revisionCount(t, s)

	writes := map[string]func() error{
		"Put": func() error {
			_, err := s.As("test", "remove agent").Put(Agents, []byte(`{}`))
			return err
		},
		"PutDoc": func() error {
			_, err := s.As("test", "remove agent").PutDoc(Doc{Agents: json.RawMessage(`{}`)})
			return err
		},
		"Delete": func() error {
			return s.As("test", "remove agent").Delete(Agents)
		},
	}
	for name, write := range writes {
		err := write()
		if err == nil {
			t.Errorf("%s: want a refusal, got nil", name)
			continue
		}
		if !strings.Contains(err.Error(), "actor security") || !strings.Contains(err.Error(), "sec-consult") {
			t.Errorf("%s: error = %q, want it to name actor security and sec-consult", name, err)
		}
		assertWriteLeftTheStoreAlone(t, s, beforeBody, beforeVersion, beforeRevs)
	}
}

func TestStoreRemovesAnAgentNoActorNames(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: prospectiveCandidates,
		Agents:     `{"sec-consult":{"shape":"reader","native":{"claude":{"agent":"sec-consult"}}},"spare":{"shape":"writer","native":{"claude":{"agent":"spare"}}}}`,
		Actors:     prospectiveActor,
	})

	// An agent no actor names can be Put away while the actor stays.
	if _, err := s.Put(Agents, []byte(prospectiveAgent)); err != nil {
		t.Fatalf("Put without the unused agent: %v", err)
	}
	body, ok, err := s.Body(Agents)
	if err != nil || !ok {
		t.Fatalf("Body(agents) = ok %v err %v, want present", ok, err)
	}
	if strings.Contains(string(body), "spare") {
		t.Errorf("agents body = %s, want the unused agent gone", body)
	}

	// Deleting a candidate a surviving actor names is tolerated.
	if err := s.Delete(Candidates); err != nil {
		t.Fatalf("Delete(candidates) with a surviving actor: %v", err)
	}

	// Once no actor names the agent, it too can be Put away.
	if err := s.Delete(Actors); err != nil {
		t.Fatalf("Delete(actors): %v", err)
	}
	if _, err := s.Put(Agents, []byte(`{}`)); err != nil {
		t.Fatalf("Put after Delete(actors): %v", err)
	}
}

func TestStoreAcceptsAWriteThatRepairsTheDocument(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	// A dangling reference is seeded under the store: Put would refuse it.
	if err := s.db.Tx(func(tx *db.Tx) error {
		if err := tx.ConfigPut(string(Candidates), []byte(prospectiveCandidates), s.now().UTC()); err != nil {
			return err
		}
		return tx.ConfigPut(string(Actors), []byte(prospectiveActor), s.now().UTC())
	}); err != nil {
		t.Fatalf("seed dangling reference: %v", err)
	}

	if _, err := s.Load(); err == nil {
		t.Fatal("Load with a dangling actor reference: want error, got nil")
	}

	// The repair names a shipped agent, so the document resolves again.
	fixed := `{"security":{"agent":"plan-executor","candidates":["m"]}}`
	if _, err := s.Put(Actors, []byte(fixed)); err != nil {
		t.Fatalf("Put(fixed actors): %v", err)
	}
	if _, err := s.Load(); err != nil {
		t.Fatalf("Load after the repair: %v", err)
	}
}

// assertWriteLeftTheStoreAlone pins that a refused write stored nothing: the
// agents body, the version and the revision rows are all as they were.
func assertWriteLeftTheStoreAlone(t *testing.T, s *Store, wantBody []byte, wantVersion int64, wantRevs int) {
	t.Helper()

	body, ok, err := s.Body(Agents)
	if err != nil || !ok {
		t.Fatalf("Body(agents) = ok %v err %v, want present", ok, err)
	}
	if !bytes.Equal(body, wantBody) {
		t.Errorf("agents body = %s, want the stored %s", body, wantBody)
	}
	if v, err := s.Version(); err != nil || v != wantVersion {
		t.Errorf("Version = (%d, %v), want (%d, nil)", v, err, wantVersion)
	}
	if n := revisionCount(t, s); n != wantRevs {
		t.Errorf("revision count = %d, want %d", n, wantRevs)
	}
}

func revisionCount(t *testing.T, s *Store) int {
	t.Helper()
	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	return len(rows)
}
