package config

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// The shared document as it syncs: which key each section contributes, and
// that nothing this machine alone knows is among them.

const (
	docCandidates = `[{"name":"m","harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`
	docAgents     = `{"my-exec":{"shape":"writer","native":{"claude":{"agent":"my-exec"}}}}`
	docActors     = `{"builder-1":{"shape":"writer","agent":"my-exec"}}`
	docAccounts   = `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/tmp/claude-work"}]`
	docPolicy     = `{"order":{"builder":["claude/anthropic/sonnet"]}}`
	docServers    = `{"zen":{"url":"https://zen:7777","fingerprint":"sha256:abcd"}}`
	docPrices     = `{"as_of":"2026-01-01","source":"file","models":{}}`
	docHooks      = `{"state_changed":[["/tmp/hook"]]}`
)

// seedDocConfig stores one valid body for every section, so a doc that is
// missing a key can only be missing it because the split left it out.
func seedDocConfig(t *testing.T, s *Store) {
	t.Helper()

	const wfSource = "name: ship\nstart: build\nsteps:\n  build: { run: builder }\n"
	seedSections(t, s, map[Section]string{
		Candidates: docCandidates,
		Agents:     docAgents,
		Actors:     docActors,
		Accounts:   docAccounts,
		Policy:     docPolicy,
		Roles:      `{}`,
		Prices:     docPrices,
		Servers:    docServers,
		Hooks:      docHooks,
		Workflows:  string(wfBody(t, "ship", wfSource, wfRaw(t, wfParse(t, wfSource)))),
		Sync:       syncStored,
	})
}

func sortedItemKeys(t *testing.T, doc *SharedDoc) []string {
	t.Helper()
	items, err := doc.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestConfigItemsSplitPerItemAndPerSection(t *testing.T) {
	t.Parallel()

	s := openRootedStore(t)
	seedDocConfig(t, s)

	want := []string{
		"accounts/work",
		"actors/builder-1",
		"agents/my-exec",
		"candidates/m",
		"hooks",
		"policy",
		"prices",
		"roles",
		"servers/zen",
		"workflows",
	}
	got := sortedItemKeys(t, s.Doc())
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Items keys =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The per-item sections are split, so one account is one entry and the
	// entry is that account rather than the array around it.
	items, err := s.Doc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	var account map[string]json.RawMessage
	if err := json.Unmarshal(items["accounts/work"], &account); err != nil {
		t.Fatalf("accounts/work = %s, want one account object", items["accounts/work"])
	}
	if _, ok := account["name"]; !ok {
		t.Errorf("accounts/work = %s, want the account's own fields", items["accounts/work"])
	}

	// The whole-section sections keep their body whole, because there is no
	// item in them to split on.
	if got := string(items["policy"]); got != docPolicy {
		t.Errorf("policy = %s, want the stored body", got)
	}
}

func TestConfigItemsCarryNoMachineField(t *testing.T) {
	t.Parallel()

	s := openRootedStore(t)
	seedDocConfig(t, s)

	items, err := s.Doc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if strings.Contains(string(items["accounts/work"]), "config_dir") {
		t.Errorf("accounts/work = %s, want no directory: a machine field must not be exported",
			items["accounts/work"])
	}
}

func TestSyncSectionNeverSyncs(t *testing.T) {
	t.Parallel()

	s := openRootedStore(t)
	seedDocConfig(t, s)
	doc := s.Doc()

	for _, key := range sortedItemKeys(t, doc) {
		if key == string(Sync) || strings.HasPrefix(key, string(Sync)+"/") {
			t.Errorf("Items holds the sync key %q: a machine's peer settings are not shared config", key)
		}
	}

	// Applying into it is refused for the same reason it is never exported: the
	// section a peer would have to win to take effect is this machine's own.
	for _, key := range []string{"sync", "sync/enabled"} {
		if err := doc.Apply(key, json.RawMessage(`{"enabled":true}`)); err == nil {
			t.Errorf("Apply(%q) = nil, want a refusal", key)
		}
	}
}
