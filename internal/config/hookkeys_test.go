package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestConfigSectionNamesDoNotCollideWithDocPrefixes is the write-stamp key
// rule (MasterMind correction C6). A stamp key is `<doc>/<key>` for every doc
// but the config, which keeps its bare keys -- so a config key is a bare
// section or item name. If a section were named `gates`, `consent` or
// `session_consent`, that config key and the other document's key would be the
// same string in one table, and one document's stamp would answer for the
// other's write.
func TestConfigSectionNamesDoNotCollideWithDocPrefixes(t *testing.T) {
	t.Parallel()
	// The documents that live beside the config in one stamp table. Kept as a
	// literal so a document added later has to be added here, where its
	// collision is refused rather than discovered in a write stamp table.
	documents := []string{"gates", "consent", "session_consent"}
	for _, sec := range Sections {
		for _, doc := range documents {
			if string(sec) == doc {
				t.Errorf("config section %q is also a shared document name; its write stamp would collide", sec)
			}
		}
	}
}

// TestHooksLocalMoveIsIdempotent pins the migration step: it runs once, records
// its marker, and a second call is a no-op rather than a second move. The
// marker is its own row because the machine-layer marker is already set on
// every machine that ran the 3b split, and a move guarded by that one would
// never run there.
func TestHooksLocalMoveIsIdempotent(t *testing.T) {
	t.Parallel()
	store, _, root := hooksRootStore(t)
	outside := root + "-elsewhere/check.sh"
	if _, err := store.As("config set", "").Put(Hooks,
		[]byte(`{"events":{"PreToolUse":[[`+jsonStr(outside)+`]]}}`)); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	doc, err := store.HookDoc()
	if err != nil {
		t.Fatalf("HookDoc: %v", err)
	}
	if len(doc.Local["PreToolUse"]) != 1 {
		t.Fatalf("machine half = %v, want the outside-root row", doc.Local)
	}

	// Open runs the move, so the marker is what says a second call is a no-op.
	if _, done, err := store.db.KVGet(hooksLocalMarker); err != nil || !done {
		t.Fatalf("marker present = %v (err %v); want the move recorded", done, err)
	}
	_, before, err := store.sharedHooksDocInTx()
	if err != nil {
		t.Fatalf("read the shared body: %v", err)
	}
	if moved, err := store.EnsureHooksLocal(); err != nil || moved {
		t.Fatalf("EnsureHooksLocal = %v, %v; want a no-op on a machine that already moved", moved, err)
	}
	_, after, err := store.sharedHooksDocInTx()
	if err != nil {
		t.Fatalf("read the shared body again: %v", err)
	}
	if !sameJSON(before, after) {
		t.Errorf("a second move changed the shared body:\n%s\n%s", before, after)
	}
}

// TestHooksExportRefusesBeforeTheMove pins the other half of the marker: an
// exporter must not send a shared body that may still carry an argv row naming
// something outside the config root.
func TestHooksExportRefusesBeforeTheMove(t *testing.T) {
	t.Parallel()
	store, _, _ := hooksRootStore(t)
	if err := store.db.KVDelete(hooksLocalMarker); err != nil {
		t.Fatalf("clear the marker: %v", err)
	}
	if _, err := store.Doc().Items(); err == nil {
		t.Fatal("Items exported before the outside-root argv move ran; want a refusal")
	}
	if _, err := store.CaptureHookFiles(); err == nil {
		t.Fatal("CaptureHookFiles ran before the outside-root argv move; want a refusal")
	}
}

// TestHandEditedHookIsReportedByDoctor pins the other half of the hash guard:
// the file the applier kept is exactly the one the drift report names.
func TestHandEditedHookIsReportedByDoctor(t *testing.T) {
	t.Parallel()
	store, applier, root := hooksRootStore(t)
	rel := writeScript(t, root, "PreToolUse", "check.sh", "shipped\n", true)
	if _, err := store.As("config set", "").Put(Hooks,
		[]byte(`{"events":{"PreToolUse":[[`+jsonStr(rel)+`]]}}`)); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), "mine\n", 0o755)
	edited, err := store.DriftedHookFiles()
	if err != nil {
		t.Fatalf("DriftedHookFiles: %v", err)
	}
	if len(edited) != 1 {
		t.Fatalf("drifted = %v, want the one edited script", edited)
	}
	if want := filepath.Join(root, filepath.FromSlash(rel)); edited[0].Path != want {
		t.Errorf("drifted path = %q, want %q", edited[0].Path, want)
	}

	// An arriving script must not overwrite the hand edit.
	_ = applier
	if err := store.Doc().Apply(string(Hooks), json.RawMessage(
		`{"files":{"`+rel+`":{"contents":"c2hpcHBlZA==","exec":true}}}`)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read the script: %v", err)
	}
	if string(data) != "mine\n" {
		t.Errorf("script on disk = %q, want the hand edit kept", data)
	}
}
