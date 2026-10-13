package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// Removing a machine field: the merged view has to lose it, and the row that
// supplied it has to record the removal rather than keep handing the value back.

// TestAFieldRemovedFromASharedWriteIsClearedLocally pins that a shared write
// which drops an optional machine field clears the override that supplied it.
// Without the clear the value would come straight back out of the local row the
// previous write left, and the write would report success while changing
// nothing.
func TestAFieldRemovedFromASharedWriteIsClearedLocally(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: docCandidates,
		Policy:     `{"order":{"builder":["claude/anthropic/sonnet"]},"serve":{"max_builders":3}}`,
	})
	if !strings.Contains(localPatchFor(t, s, "policy"), "serve") {
		t.Fatal("the first write left no override to clear")
	}

	// The policy's serve block is optional, so dropping it is a valid config
	// and has to take the override with it.
	if _, err := s.As("test", "drop the serve block").Put(Policy,
		json.RawMessage(`{"order":{"builder":["claude/anthropic/sonnet"]}}`)); err != nil {
		t.Fatalf("Put(policy without serve): %v", err)
	}

	if patch := localPatchFor(t, s, "policy"); !strings.Contains(patch, "null") {
		t.Errorf("the override for policy = %s, want the serve block cleared", patch)
	}
	body, _, err := s.Body(Policy)
	if err != nil {
		t.Fatalf("Body(policy): %v", err)
	}
	if strings.Contains(string(body), "serve") {
		t.Errorf("policy = %s, want the removed serve block gone", body)
	}
	if _, err := s.Load(); err != nil {
		t.Errorf("Load after the serve block was cleared: %v", err)
	}
}

// TestARemovedRequiredMachineFieldIsRefused pins the other half: an account's
// config dir and home are what their harness kind requires, so clearing one
// would leave an account that cannot be honoured. The write is refused and the
// override is left as it was, rather than clearing the row and leaving a broken
// config behind it.
func TestARemovedRequiredMachineFieldIsRefused(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: docCandidates,
		Accounts:   `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/tmp/first"}]`,
	})

	if _, err := s.As("test", "drop the directory").Put(Accounts,
		json.RawMessage(`[{"name":"work","harness":"claude","groups":["anthropic"]}]`)); err == nil {
		t.Fatal("Put of an account with no config_dir = nil, want a refusal")
	}

	if patch := localPatchFor(t, s, "accounts/work"); !strings.Contains(patch, "/tmp/first") {
		t.Errorf("the override for accounts/work = %s, want the directory left alone after a refusal", patch)
	}
	if _, err := s.Load(); err != nil {
		t.Errorf("Load after the refused write: %v", err)
	}
}

// TestTheSecondFieldOfAnItemIsKeptWhenTheFirstIsSet pins that a write carrying
// one machine field sets that field's row entry and leaves the row writable
// afterwards, which is what an unset of the other field then reads.
func TestTheSecondFieldOfAnItemIsKeptWhenTheFirstIsSet(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: docCandidates,
		Accounts:   `[{"name":"work","harness":"codex","groups":["openai"],"home":"/home/mine"}]`,
	})

	const again = "/home/again"
	if _, err := s.As("test", "move the home").Put(Accounts,
		json.RawMessage(`[{"name":"work","harness":"codex","groups":["openai"],"home":"`+again+`"}]`)); err != nil {
		t.Fatalf("Put(accounts with a new home): %v", err)
	}

	patch := localPatchFor(t, s, "accounts/work")
	if !strings.Contains(patch, again) {
		t.Errorf("the override for accounts/work = %s, want the new home", patch)
	}
	if strings.Contains(patch, "config_dir") {
		t.Errorf("the override for accounts/work = %s, want no field this account never carried", patch)
	}
	body, _, err := s.Body(Accounts)
	if err != nil {
		t.Fatalf("Body(accounts): %v", err)
	}
	if !strings.Contains(string(body), again) {
		t.Errorf("accounts = %s, want the new home", body)
	}
}

// TestAnOverrideOfOneItemLeavesTheOtherAlone pins that a local write is per
// item: an account this machine did not override is read from the shared layer
// unchanged.
func TestAnOverrideOfOneItemLeavesTheOtherAlone(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: docCandidates,
		Accounts: `[{"name":"work","harness":"codex","groups":["openai"],"home":"/home/work"},` +
			`{"name":"side","harness":"codex","groups":["openai"],"home":"/home/side"}]`,
	})

	const mine = "/home/mine"
	if err := s.PutLocal(Doc{Accounts: json.RawMessage(
		`[{"name":"work","harness":"codex","groups":["openai"],"home":"` + mine + `"},` +
			`{"name":"side","harness":"codex","groups":["openai"],"home":"/home/side"}]`)}); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}

	body, _, err := s.Body(Accounts)
	if err != nil {
		t.Fatalf("Body(accounts): %v", err)
	}
	if !strings.Contains(string(body), mine) {
		t.Errorf("accounts = %s, want the overridden home", body)
	}
	if !strings.Contains(string(body), "/home/side") {
		t.Errorf("accounts = %s, want the other account's home from the shared layer", body)
	}
}

// TestTheDocumentIsNamedConfig pins the word an entry's table carries after the
// `doc:` prefix, which is what every machine has to agree on for the entries to
// route at all.
func TestTheDocumentIsNamedConfig(t *testing.T) {
	t.Parallel()

	if got := openStore(t).Doc().Name(); got != Config {
		t.Errorf("the config document is named %q, want %q", got, Config)
	}
	if Config != "config" {
		t.Errorf("Config = %q, want the word the table name carries", Config)
	}
}

// TestAMalformedSectionBodyIsRefusedOnBothSides pins that a body the shared
// layer already holds in a shape the key split cannot take apart is a refusal
// rather than a silent empty item set: an item that went missing from the
// export would look to a peer like a deletion.
func TestAMalformedSectionBodyIsRefusedOnBothSides(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	// The agents section is an object keyed by name; a body that is not an
	// object is written under the store to stand for one that arrived that way.
	seedSharedDoc(t, s, map[Section]string{
		Candidates: docCandidates,
		Agents:     `[]`,
	})

	if _, err := s.Doc().Items(); err == nil {
		t.Error("Items over an agents body that is not an object = nil, want a refusal")
	}
	if err := s.Doc().Apply("agents/x", json.RawMessage(`{}`)); err == nil {
		t.Error("Apply into an agents body that is not an object = nil, want a refusal")
	}
}
