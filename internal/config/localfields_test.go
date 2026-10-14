package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The machine-local layer: which fields land in it by default, that it wins on
// read, and that the fields are moved out of the shared bodies before anything
// could export them.

func TestMachineFieldsLandLocalByDefault(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)

	// A bare write of a machine field is what `relevo config set
	// accounts.work.config_dir /elsewhere` amounts to, and it has to land here
	// rather than in the shared body.
	const dir = "/elsewhere/claude-work"
	seedSections(t, s, map[Section]string{
		Accounts: `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"` + dir + `"}]`,
		Policy:   `{"order":{"builder":["claude/anthropic/sonnet"]},"serve":{"max_builders":3}}`,
	})

	shared, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if strings.Contains(string(shared[Accounts]), "config_dir") {
		t.Errorf("the shared accounts body = %s, want no directory", shared[Accounts])
	}
	if strings.Contains(string(shared[Policy]), "serve") {
		t.Errorf("the shared policy body = %s, want no serve block", shared[Policy])
	}

	accountPatch := localPatchFor(t, s, "accounts/work")
	if !strings.Contains(accountPatch, dir) {
		t.Errorf("the local override for accounts/work = %s, want the directory", accountPatch)
	}
	if policyPatch := localPatchFor(t, s, "policy"); !strings.Contains(policyPatch, "serve") {
		t.Errorf("the local override for policy = %s, want the serve block", policyPatch)
	}

	// The merged view is what every reader gets, so the fields are still there.
	body, _, err := s.Body(Accounts)
	if err != nil {
		t.Fatalf("Body(accounts): %v", err)
	}
	if !strings.Contains(string(body), dir) {
		t.Errorf("accounts = %s, want the directory back in the merged view", body)
	}
}

// TestLocalLayerWinsOverShared pins the read order: the shared layer is the
// whole truth for a key and the local layer overrides it, so a local value is
// what a reader sees and what a shared write of the same key does not displace.
func TestLocalLayerWinsOverShared(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: docCandidates,
		Policy:     docPolicy,
	})
	// The shared half is written straight into the store rows, because a
	// machine field put through the store is routed local by definition and
	// this test needs a shared value for the local one to override.
	seedSharedDoc(t, s, map[Section]string{
		Accounts: `[{"name":"work","harness":"codex","groups":["openai"],"home":"/home/shared"}]`,
	})

	const home = "/home/mine"
	if err := s.PutLocal(Doc{Accounts: json.RawMessage(
		`[{"name":"work","harness":"codex","groups":["openai"],"home":"` + home + `"}]`)}); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}

	body, _, err := s.Body(Accounts)
	if err != nil {
		t.Fatalf("Body(accounts): %v", err)
	}
	if !strings.Contains(string(body), home) {
		t.Errorf("accounts = %s, want the local home %q to win", body, home)
	}
	if strings.Contains(string(body), "/home/shared") {
		t.Errorf("accounts = %s, want the shared home to be overridden", body)
	}

	// The shared body is untouched by an override, which is what lets a peer's
	// edit of the same account still reach this machine.
	shared, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if !strings.Contains(string(shared[Accounts]), "/home/shared") {
		t.Errorf("the shared accounts body = %s, want the shared home left alone", shared[Accounts])
	}

	// An override of one field leaves its neighbours to the shared layer.
	const groups = `["anthropic"]`
	if err := s.PutLocal(Doc{Accounts: json.RawMessage(
		`[{"name":"work","harness":"codex","groups":` + groups + `,"home":"` + home + `"}]`)}); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}
	body, _, err = s.Body(Accounts)
	if err != nil {
		t.Fatalf("Body(accounts): %v", err)
	}
	if !strings.Contains(string(body), home) || !strings.Contains(string(body), "anthropic") {
		t.Errorf("accounts = %s, want the local home and the edited groups together", body)
	}
}

// TestMigrationMovesMachineFieldsBeforeExport pins the one-time move: while the
// marker is unset the doc refuses to be exported at all, because the shared
// bodies still carry this machine's directories, and once it has run the
// fields are in the local layer with the shared bodies clean. Running it again
// moves nothing.
func TestMigrationMovesMachineFieldsBeforeExport(t *testing.T) {
	t.Parallel()

	s := openRootedStore(t)
	// A config written before the layer existed: the fields are still in the
	// shared bodies, and the marker is unset, which is exactly the state the
	// move exists to end.
	seedSharedDoc(t, s, map[Section]string{
		Candidates: docCandidates,
		Policy:     `{"order":{"builder":["claude/anthropic/sonnet"]},"serve":{"max_builders":3}}`,
		Accounts:   `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/legacy/dir"}]`,
	})
	clearLocalLayerMarker(t, s)

	doc := s.Doc()
	if _, err := doc.Items(); err == nil {
		t.Fatal("Items before the move = nil error, want a refusal: the shared bodies still carry this machine's fields")
	}

	moved, err := s.EnsureLocalLayer()
	if err != nil {
		t.Fatalf("EnsureLocalLayer: %v", err)
	}
	if !moved {
		t.Error("EnsureLocalLayer moved nothing, want the machine fields moved")
	}

	shared, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if strings.Contains(string(shared[Accounts]), "config_dir") {
		t.Errorf("the shared accounts body = %s, want the directory moved out", shared[Accounts])
	}
	if strings.Contains(string(shared[Policy]), "serve") {
		t.Errorf("the shared policy body = %s, want the serve block moved out", shared[Policy])
	}
	if !strings.Contains(localPatchFor(t, s, "accounts/work"), "/legacy/dir") {
		t.Error("the moved directory is not in the local layer")
	}
	if !strings.Contains(localPatchFor(t, s, "policy"), "serve") {
		t.Error("the moved serve block is not in the local layer")
	}

	if _, err := doc.Items(); err != nil {
		t.Fatalf("Items after the move: %v", err)
	}

	// A second call is the idempotence: the marker is what makes it a no-op,
	// not the state of the bodies.
	moved, err = s.EnsureLocalLayer()
	if err != nil {
		t.Fatalf("EnsureLocalLayer again: %v", err)
	}
	if moved {
		t.Error("EnsureLocalLayer moved again, want the marker to make it a no-op")
	}
	if !strings.Contains(localPatchFor(t, s, "accounts/work"), "/legacy/dir") {
		t.Error("the second run dropped the moved directory")
	}
}

// TestAnOverrideNeverInventsAnAccount pins that the local layer overrides what
// the shared layer holds and never adds an item of its own. A stale override
// naming an account the shared layer no longer has would otherwise hand the
// merged view an account made of nothing but its own machine field, which is a
// document Load refuses.
func TestAnOverrideNeverInventsAnAccount(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)
	const dir = "/tmp/claude-work"
	if err := s.PutLocal(Doc{Accounts: json.RawMessage(
		`[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"` + dir + `"}]`)}); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}

	// The account goes away on the shared side, as a peer's delete would: the
	// shared body is rewritten under the store, which is what a merged write
	// from another machine looks like from here.
	seedSharedDoc(t, s, map[Section]string{
		Accounts: `[{"name":"other","harness":"codex","groups":["openai"],"home":"/home/other"}]`,
	})

	body, _, err := s.Body(Accounts)
	if err != nil {
		t.Fatalf("Body(accounts): %v", err)
	}
	if strings.Contains(string(body), `"work"`) {
		t.Errorf("accounts = %s, want the stale override not to invent the removed account", body)
	}
	if !strings.Contains(string(body), "other") {
		t.Errorf("accounts = %s, want the account the shared layer still holds", body)
	}
	if _, err := s.Load(); err != nil {
		t.Errorf("Load with a stale override: %v", err)
	}
}

func localPatchFor(t *testing.T, s *Store, key string) string {
	t.Helper()
	patch, ok, err := s.db.LocalOrSelf().ConfigLocalGet(key)
	if err != nil {
		t.Fatalf("the local override for %s: %v", key, err)
	}
	if !ok {
		t.Fatalf("the local layer holds no override for %s", key)
	}
	return string(patch)
}

// seedSharedDoc writes section bodies straight into the store rows, the way a
// config written before the local layer existed is on disk.
func seedSharedDoc(t *testing.T, s *Store, bodies map[Section]string) {
	t.Helper()
	now := s.now().UTC()
	err := s.db.Tx(func(t *db.Tx) error {
		for _, sec := range Sections {
			body, ok := bodies[sec]
			if !ok {
				continue
			}
			if err := t.ConfigPut(string(sec), []byte(body), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed the shared config: %v", err)
	}
}

func clearLocalLayerMarker(t *testing.T, s *Store) {
	t.Helper()
	if err := s.db.KVDelete(localLayerMarker); err != nil {
		t.Fatalf("clear the local layer marker: %v", err)
	}
}
