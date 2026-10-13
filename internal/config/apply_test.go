package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// What one arriving key does to this machine's config: it lands or it does not,
// and either way the config Load reads still resolves.

// TestConfigApplyRefusesAnInvalidItem pins the refusal of an item that would
// leave the merged config invalid. The entry is newer than what this machine
// holds, so the choice is between taking a config that does not resolve and
// keeping one that does; it keeps, and says so in the revision log rather than
// silently dropping the entry.
func TestConfigApplyRefusesAnInvalidItem(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)
	before, _, err := s.Body(Candidates)
	if err != nil {
		t.Fatalf("Body(candidates): %v", err)
	}

	// A candidate with no provider is a row candidate.Parse refuses, so the
	// merged document would not resolve.
	err = s.Doc().Apply("candidates/broken", json.RawMessage(`{"name":"broken","harness":"claude","model":"broken"}`))
	if err == nil {
		t.Fatal("Apply of an unparseable candidate = nil, want a refusal")
	}

	after, _, err := s.Body(Candidates)
	if err != nil {
		t.Fatalf("Body(candidates): %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("candidates = %s after a refused apply, want the unchanged %s", after, before)
	}
	if _, err := s.Load(); err != nil {
		t.Errorf("Load after a refused apply: %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	found := false
	for _, row := range rows {
		if strings.HasPrefix(row.Source, supersededSourcePrefix+"candidates/broken") {
			found = true
		}
	}
	if !found {
		t.Errorf("no revision under %s: a refused apply has to say why", supersededSourcePrefix)
	}
}

// TestAppliedItemNeverBreaksLoad pins that a landed item goes through the same
// path a local write does, so Load keeps resolving and sees it, and that the
// removal of a key is a tombstone rather than a section rewrite.
func TestAppliedItemNeverBreaksLoad(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)
	doc := s.Doc()

	// An agent an actor then names has to resolve: the actor names an agent
	// the document has not held yet, which is what an arriving item does.
	if err := doc.Apply("agents/later", json.RawMessage(`{"shape":"reader","native":{"claude":{"agent":"later"}}}`)); err != nil {
		t.Fatalf("Apply(agents/later): %v", err)
	}
	if err := doc.Apply("actors/builder-1", json.RawMessage(`{"shape":"writer","agent":"later"}`)); err != nil {
		t.Fatalf("Apply(actors/builder-1): %v", err)
	}
	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load after two applies: %v", err)
	}
	if _, ok := L.Agents["later"]; !ok {
		t.Error("the applied agent is not in the loaded config")
	}
	if actor, ok := L.Actors["builder-1"]; !ok || actor.Agent != "later" {
		t.Errorf("builder-1 = %+v, want the applied agent", actor)
	}

	// A tombstone removes the item and leaves the rest of the section alone.
	if err := doc.Apply("actors/builder-1", nil); err != nil {
		t.Fatalf("Apply(actors/builder-1, nil): %v", err)
	}
	body, ok, err := s.Body(Actors)
	if err != nil || !ok {
		t.Fatalf("Body(actors) = ok %v err %v, want the section still there", ok, err)
	}
	if strings.Contains(string(body), "builder-1") {
		t.Errorf("actors = %s, want the removed item gone", body)
	}
	if _, err := s.Load(); err != nil {
		t.Errorf("Load after a tombstone: %v", err)
	}
}

// TestApplyRefusesAKeyItHasNoShapeFor pins that the key vocabulary is checked
// before anything is written. An entry naming an unknown section, an item of a
// whole-section section or a section it names with two separators is an entry
// this machine has no writer for, and applying one would write somewhere the
// entry never named.
func TestApplyRefusesAKeyItHasNoShapeFor(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)
	doc := s.Doc()
	before, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	bad := []string{
		"nosuchsection",
		"nosuchsection/item",
		"policy/serve",
		"candidates",
		"candidates/",
		"candidates/a/b",
		"",
	}
	for _, key := range bad {
		if err := doc.Apply(key, json.RawMessage(`{}`)); err == nil {
			t.Errorf("Apply(%q) = nil, want a refusal", key)
		}
	}

	after, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	for sec, body := range after {
		if string(body) != string(before[sec]) {
			t.Errorf("%s = %s after %d refused applies, want the unchanged %s", sec, body, len(bad), before[sec])
		}
	}
}

// TestApplyNeverWritesTheLocalLayer pins that an arriving item cannot redirect
// this machine's own directories or serve policy: the machine fields are
// routed local by every local write, and a doc apply must not be a way around
// that.
func TestApplyNeverWritesTheLocalLayer(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)

	if err := s.Doc().Apply("accounts/work", json.RawMessage(
		`{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/somewhere/else"}`)); err != nil {
		t.Fatalf("Apply(accounts/work): %v", err)
	}

	body, ok, err := s.Body(Accounts)
	if err != nil || !ok {
		t.Fatalf("Body(accounts) = ok %v err %v, want the section", ok, err)
	}
	if !strings.Contains(string(body), "/tmp/claude-work") {
		t.Errorf("accounts = %s, want this machine's own config_dir to win", body)
	}
	if strings.Contains(string(body), "/somewhere/else") {
		t.Errorf("accounts = %s, want the applied directory to have landed nowhere", body)
	}

	var patch string
	err = s.db.Tx(func(t *db.Tx) error {
		raw, ok, err := t.ConfigLocalGet("accounts/work")
		if err != nil || !ok {
			return err
		}
		patch = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("ConfigLocalGet: %v", err)
	}
	if !strings.Contains(patch, "/tmp/claude-work") {
		t.Errorf("the local override for accounts/work = %s, want the machine's own directory", patch)
	}
}
