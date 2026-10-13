package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

func mustLoadAndBody(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.Load(); err != nil {
		t.Errorf("Load: %v", err)
	}
	for _, sec := range Sections {
		if _, _, err := s.Body(sec); err != nil {
			t.Errorf("Body(%s): %v", sec, err)
		}
	}
}

func localKeyCount(t *testing.T, s *Store) int {
	t.Helper()
	keys, err := s.db.LocalOrSelf().ConfigLocalKeys("")
	if err != nil {
		t.Fatalf("ConfigLocalKeys: %v", err)
	}
	return len(keys)
}

// TestApplyDropsPeerMachineFieldsFromTheSharedBody pins that machine fields on
// an arriving account item or policy body land nowhere: not in the shared row
// and not in what Items re-exports.
func TestApplyDropsPeerMachineFieldsFromTheSharedBody(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)
	if err := s.Doc().Apply("accounts/work", json.RawMessage(
		`{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/peer/dir","home":"/peer/home"}`)); err != nil {
		t.Fatalf("Apply(accounts/work): %v", err)
	}
	if err := s.Doc().Apply("policy", json.RawMessage(
		`{"order":{"builder":["claude/anthropic/sonnet"]},"serve":{"max_builders":9}}`)); err != nil {
		t.Fatalf("Apply(policy): %v", err)
	}

	shared, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	for _, sec := range []Section{Accounts, Policy} {
		for _, field := range []string{"/peer/dir", "/peer/home", "serve"} {
			if strings.Contains(string(shared[sec]), field) {
				t.Errorf("shared %s = %s, want no %s", sec, shared[sec], field)
			}
		}
	}
	items, err := s.Doc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	for key, item := range items {
		for _, field := range []string{"/peer/dir", "/peer/home", `"serve"`} {
			if strings.Contains(string(item), field) {
				t.Errorf("Items()[%s] = %s, want no %s", key, item, field)
			}
		}
	}
	mustLoadAndBody(t, s)
}

// TestStaleLocalRowNeverResurrectsADeletedItem pins that a config_local row for
// an agent, actor or server the shared body no longer holds is ignored.
func TestStaleLocalRowNeverResurrectsADeletedItem(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)
	err := s.db.Tx(func(tx *db.Tx) error {
		now := s.now().UTC()
		for key, patch := range map[string]string{
			"agents/ghost":   `{"shape":"reader"}`,
			"actors/ghost":   `{"shape":"reader"}`,
			"servers/ghost":  `{"url":"https://ghost"}`,
			"agents/my-exec": `{}`,
		} {
			if err := tx.ConfigLocalPut(key, []byte(patch), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed stale rows: %v", err)
	}
	for _, sec := range []Section{Agents, Actors, Servers} {
		body, _, err := s.Body(sec)
		if err != nil {
			t.Fatalf("Body(%s): %v", sec, err)
		}
		if strings.Contains(string(body), "ghost") {
			t.Errorf("%s = %s, want the stale row not to resurrect ghost", sec, body)
		}
	}
	mustLoadAndBody(t, s)
}

// TestLocalWriteNeverAddsOrRemovesShared pins that --local writes which would
// add or remove a shared item or section are refused, write no row, and leave
// the merged config readable.
func TestLocalWriteNeverAddsOrRemovesShared(t *testing.T) {
	t.Parallel()

	cases := map[string]Doc{
		"remove an agent":         {Agents: json.RawMessage(`{}`)},
		"remove an account":       {Accounts: json.RawMessage(`[]`)},
		"remove a whole section":  {Agents: json.RawMessage(`null`)},
		"remove a whole policy":   {Policy: json.RawMessage(`null`)},
		"remove accounts section": {Accounts: json.RawMessage(`null`)},
		"add an agent": {Agents: json.RawMessage(
			`{"my-exec":{"shape":"writer","native":{"claude":{"agent":"my-exec"}}},"fresh":{"shape":"reader"}}`)},
		"write an absent section": {Hooks: json.RawMessage(`{"state_changed":[["/x"]]}`)},
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			s := openStore(t)
			seedSections(t, s, map[Section]string{
				Candidates: docCandidates, Agents: docAgents, Actors: docActors,
				Accounts: docAccounts, Policy: docPolicy,
			})
			before := localKeyCount(t, s)
			err := s.PutLocal(doc)
			if err == nil || !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("PutLocal = %v, want an invalid-value refusal", err)
			}
			if n := localKeyCount(t, s); n != before {
				t.Errorf("config_local holds %d rows after a refused write, want the %d it held", n, before)
			}
			mustLoadAndBody(t, s)
		})
	}
}
