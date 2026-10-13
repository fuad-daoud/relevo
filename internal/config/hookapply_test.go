package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// A memory applier standing in for the disk one: it records what it was asked
// to write so a test can see the hash-guarded rule without touching a
// filesystem, and it can be handed a hand-edited file to report as kept.
type memApplier struct {
	root   string
	files  map[string][]byte
	execs  map[string]bool
	reads  int
	writes int
}

func newMemApplier(root string) *memApplier {
	return &memApplier{root: root, files: map[string][]byte{}, execs: map[string]bool{}}
}

func (m *memApplier) HookFilePath(event, file string) (string, error) {
	if err := safeComponent(event); err != nil {
		return "", err
	}
	if err := safeComponent(file); err != nil {
		return "", err
	}
	return filepath.Join(m.root, hooksDirName, event+hooksSuffix, file), nil
}

func (m *memApplier) ReadHookFile(path string) ([]byte, bool, error) {
	m.reads++
	data, ok := m.files[path]
	return data, ok, nil
}

func (m *memApplier) WriteHookFile(path string, data []byte, exec bool) (bool, error) {
	m.writes++
	m.files[path] = append([]byte(nil), data...)
	m.execs[path] = exec
	return true, nil
}

// The agent trio is the hooks' trio under another directory rule: a harness
// kind's own home, computed here out of the kind and the agent name.
func (m *memApplier) AgentFilePath(kind, agent string) (string, error) {
	if _, ok := harness.Lookup(kind); !ok {
		return "", fmt.Errorf("agents: %q is not a harness kind: %w", kind, ErrAgentPath)
	}
	if err := safeAgentComponent(agent); err != nil {
		return "", err
	}
	return filepath.Join(m.root, kind, agent+".md"), nil
}

func (m *memApplier) ReadAgentFile(path string) ([]byte, bool, error) {
	m.reads++
	data, ok := m.files[path]
	return data, ok, nil
}

func (m *memApplier) WriteAgentFile(path string, data []byte) (bool, error) {
	m.writes++
	m.files[path] = append([]byte(nil), data...)
	m.execs[path] = false
	return true, nil
}

// diskApplier is a FileApplier over a real temporary root, for a test whose
// assertions read the disk rather than the applier's own record: the drift
// report is one of those, so a memApplier would let it pass without the write
// having landed anywhere.
type diskApplier struct{ root string }

func (a diskApplier) HookFilePath(event, file string) (string, error) {
	if err := safeComponent(event); err != nil {
		return "", err
	}
	if err := safeComponent(file); err != nil {
		return "", err
	}
	return filepath.Join(a.root, hooksDirName, event+hooksSuffix, file), nil
}

func (a diskApplier) ReadHookFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (a diskApplier) WriteHookFile(path string, data []byte, exec bool) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return false, err
	}
	return true, nil
}

func (a diskApplier) AgentFilePath(kind, agent string) (string, error) {
	if _, ok := harness.Lookup(kind); !ok {
		return "", fmt.Errorf("agents: %q is not a harness kind: %w", kind, ErrAgentPath)
	}
	if err := safeAgentComponent(agent); err != nil {
		return "", err
	}
	return filepath.Join(a.root, kind, agent+".md"), nil
}

func (a diskApplier) ReadAgentFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (a diskApplier) WriteAgentFile(path string, data []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// hooksRootStore is one store over a temporary config root, with the applier
// bound the way sharedDocs binds the real one.
func hooksRootStore(t *testing.T) (*Store, *memApplier, string) {
	t.Helper()
	root := t.TempDir()
	store := openStore(t).WithFiles(newMemApplier(root), root)
	return store, store.files.(*memApplier), root
}

// writeScript puts an executable script at <root>/hooks/<event>.d/<file> and
// returns its root-relative name.
func writeScript(t *testing.T, root, event, file, body string, exec bool) string {
	t.Helper()
	dir := filepath.Join(root, hooksDirName, event+hooksSuffix)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), mode); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return HookPathOf(event, file)
}

// TestHooksItemsEmbedContentsAndExecBit pins the export half: the hooks item
// carries the script itself and whether it is executable, not only the argv
// row naming it, so a peer receives the hook rather than the name of one.
func TestHooksItemsEmbedContentsAndExecBit(t *testing.T) {
	t.Parallel()
	store, _, root := hooksRootStore(t)
	rel := writeScript(t, root, "PreToolUse", "check.sh", "#!/bin/sh\nexit 0\n", true)
	if _, err := store.As("config set", "").Put(Hooks,
		[]byte(`{"events":{"PreToolUse":[["`+rel+`"]]}}`)); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	// The write that named the script also captured it, so the document
	// already carries the script rather than only the name of one.
	items, err := store.Doc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	doc, err := ParseHookDoc(items[string(Hooks)])
	if err != nil {
		t.Fatalf("ParseHookDoc: %v", err)
	}
	file, ok := doc.Files[rel]
	if !ok {
		t.Fatalf("hooks item carries no script for %s: %v", rel, items[string(Hooks)])
	}
	if string(file.Contents) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("script contents = %q, want the file on disk", file.Contents)
	}
	if !file.Exec {
		t.Error("script exec bit = false, want true for an executable file")
	}
}

// TestHooksArgvRelativizesInsideRoot pins the argv half of the read: a script
// under this machine's config root travels as the root-relative name it is
// stored with, so a peer with a different root applies it under its own.
func TestHooksArgvRelativizesInsideRoot(t *testing.T) {
	t.Parallel()
	store, _, root := hooksRootStore(t)
	writeScript(t, root, "PreToolUse", "check.sh", "x", true)
	abs := filepath.Join(root, hooksDirName, "PreToolUse.d", "check.sh")
	if _, err := store.As("config set", "").Put(Hooks,
		[]byte(`{"events":{"PreToolUse":[[`+jsonStr(abs)+`]]}}`)); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	doc, err := store.HookDoc()
	if err != nil {
		t.Fatalf("HookDoc: %v", err)
	}
	row := doc.Events["PreToolUse"][0]
	if row[0] != "hooks/PreToolUse.d/check.sh" {
		t.Fatalf("shared argv[0] = %q, want the root-relative name", row[0])
	}
	if len(doc.Local) != 0 {
		t.Errorf("a script inside the root landed in the machine half: %v", doc.Local)
	}
}

// TestHooksArgvOutsideRootStaysLocal pins the path rule: an argv row naming
// anything outside the config root is this machine's, so it moves to the local
// layer and never appears in the shared body an exporter sends.
func TestHooksArgvOutsideRootStaysLocal(t *testing.T) {
	t.Parallel()
	store, _, _ := hooksRootStore(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.sh")
	body := `{"events":{"PreToolUse":[[` + jsonStr(outside) + `]]}}`
	if _, err := store.As("config set", "").Put(Hooks, []byte(body)); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	doc, err := store.HookDoc()
	if err != nil {
		t.Fatalf("HookDoc: %v", err)
	}
	if got := doc.Local["PreToolUse"]; len(got) != 1 || got[0][0] != outside {
		t.Fatalf("machine half = %v, want the outside-root row", doc.Local)
	}
	if len(doc.Events["PreToolUse"]) != 0 {
		t.Errorf("the outside-root row reached the shared half: %v", doc.Events)
	}
	items, err := store.Doc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if body := string(items[string(Hooks)]); containsArg(body, outside) {
		t.Errorf("the exported hooks body carries %s:\n%s", outside, body)
	}
}

// TestHooksApplyRefusesEscapeAndSymlink pins the path rules one at a time: an
// entry cannot name a directory of its own choosing, and a script is only ever
// written to the path its own event name computes.
func TestHooksApplyRefusesEscapeAndSymlink(t *testing.T) {
	t.Parallel()
	t.Run("argv in another event's directory", func(t *testing.T) {
		t.Parallel()
		store, _, _ := hooksRootStore(t)
		body := `{"events":{"PreToolUse":[["hooks/PostToolUse.d/check.sh"]]}}`
		if err := store.validateHooksBody(json.RawMessage(body)); err == nil {
			t.Fatal("validateHooksBody accepted an argv row under another event's directory")
		}
	})
	for _, tc := range []struct {
		name string
		rel  string
	}{
		{"escape", "hooks/PreToolUse.d/../../../etc/passwd"},
		{"absolute", "hooks/PreToolUse.d//etc/shadow"},
		{"backslash", `hooks\PreToolUse.d\check.sh`},
		{"outside the hooks dir", "scripts/check.sh"},
		{"dot name", "hooks/PreToolUse.d/."},
		{"dotdot name", "hooks/PreToolUse.d/.."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, _, _ := hooksRootStore(t)
			body := `{"files":{` + jsonStr(tc.rel) + `:{"contents":"eA==","exec":true}}}`
			err := store.validateHooksBody(json.RawMessage(body))
			if err == nil {
				t.Fatalf("validateHooksBody(%q) accepted the path; want a refusal", tc.rel)
			}
		})
	}
}

// TestHooksApplyRefusesOversize pins both caps: one file over 256 KiB, and a
// hooks item over 2 MiB in total.
func TestHooksApplyRefusesOversize(t *testing.T) {
	t.Parallel()
	t.Run("one file", func(t *testing.T) {
		t.Parallel()
		store, _, _ := hooksRootStore(t)
		big := make([]byte, MaxHookFileBytes+1)
		for i := range big {
			big[i] = 'x'
		}
		doc := HookDoc{
			Events: map[string][][]string{},
			Files:  map[string]HookFile{"hooks/PreToolUse.d/a.sh": {Contents: big}},
			Root:   store.ConfigRoot(),
		}
		if err := ValidateHookDoc(doc); err == nil {
			t.Fatal("ValidateHookDoc accepted a file over the per-file cap")
		}
	})

	t.Run("the whole item", func(t *testing.T) {
		t.Parallel()
		store, _, _ := hooksRootStore(t)
		files := map[string]HookFile{}
		chunk := make([]byte, MaxHookFileBytes)
		for i := range chunk {
			chunk[i] = 'x'
		}
		for i := 0; i < 9; i++ {
			files[HookPathOf("PreToolUse", string(rune('a'+i))+".sh")] = HookFile{Contents: chunk}
		}
		doc := HookDoc{Events: map[string][][]string{}, Files: files, Root: store.ConfigRoot()}
		if err := ValidateHookDoc(doc); err == nil {
			t.Fatal("ValidateHookDoc accepted a hooks item over the 2 MiB cap")
		}
	})
}

// TestHooksApplyKeepsHandEdits pins the hash guard: an arriving script is
// written when the file is missing, is left alone when the bytes still match,
// and is left alone when this machine has edited it. The document lands in
// every case, so the peer's other scripts travel either way.
func TestHooksApplyKeepsHandEdits(t *testing.T) {
	t.Parallel()
	t.Run("missing is written", func(t *testing.T) {
		t.Parallel()
		store, applier, _ := hooksRootStore(t)
		arriving := `{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"bmV3","exec":true}}}`
		if err := store.Doc().Apply(string(Hooks), json.RawMessage(arriving)); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		path := filepath.Join(applier.root, hooksDirName, "PreToolUse.d", "a.sh")
		if got, ok := applier.files[path]; !ok || string(got) != "new" {
			t.Fatalf("file on disk = %q (present %v), want the arriving script", got, ok)
		}
	})

	t.Run("matching is not rewritten", func(t *testing.T) {
		t.Parallel()
		store, applier, _ := hooksRootStore(t)
		arriving := `{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"bmV3","exec":true}}}`
		if err := store.Doc().Apply(string(Hooks), json.RawMessage(arriving)); err != nil {
			t.Fatalf("first Apply: %v", err)
		}
		writes := applier.writes
		if err := store.Doc().Apply(string(Hooks), json.RawMessage(arriving)); err != nil {
			t.Fatalf("second Apply: %v", err)
		}
		if applier.writes != writes {
			t.Errorf("a matching file was rewritten: %d writes, want %d", applier.writes, writes)
		}
	})

	t.Run("a hand edit is kept", func(t *testing.T) {
		t.Parallel()
		store, applier, _ := hooksRootStore(t)
		if err := store.Doc().Apply(string(Hooks),
			json.RawMessage(`{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"bmV3","exec":true}}}`)); err != nil {
			t.Fatalf("first Apply: %v", err)
		}
		path := filepath.Join(applier.root, hooksDirName, "PreToolUse.d", "a.sh")
		applier.files[path] = []byte("edited by a person")
		if err := store.Doc().Apply(string(Hooks),
			json.RawMessage(`{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"bmV3","exec":true}}}`)); err != nil {
			t.Fatalf("second Apply: %v", err)
		}
		if got := string(applier.files[path]); got != "edited by a person" {
			t.Errorf("file on disk = %q, want the hand edit kept", got)
		}
	})
}

// TestHooksApplyPeerUpdatesLand pins the other side of the guard from the
// previous test: a script this machine has not edited is the peer's to
// update, so an arriving copy of it lands rather than being kept as a hand
// edit nobody made.
func TestHooksApplyPeerUpdatesLand(t *testing.T) {
	t.Parallel()
	t.Run("an update to an unedited script lands", func(t *testing.T) {
		t.Parallel()
		store, applier, _ := hooksRootStore(t)
		if err := store.Doc().Apply(string(Hooks),
			json.RawMessage(`{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"Zmlyc3Q=","exec":true}}}`)); err != nil {
			t.Fatalf("first Apply: %v", err)
		}
		path := filepath.Join(applier.root, hooksDirName, "PreToolUse.d", "a.sh")
		if err := store.Doc().Apply(string(Hooks),
			json.RawMessage(`{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"c2Vjb25k","exec":true}}}`)); err != nil {
			t.Fatalf("second Apply: %v", err)
		}
		if got := string(applier.files[path]); got != "second" {
			t.Errorf("file on disk = %q, want the peer's update", got)
		}
	})

	// The executable bit is part of what the document carries, so a peer that
	// flips it lands even though the contents are untouched.
	t.Run("an exec flip lands", func(t *testing.T) {
		t.Parallel()
		store, applier, _ := hooksRootStore(t)
		if err := store.Doc().Apply(string(Hooks),
			json.RawMessage(`{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"c2hlbGw=","exec":true}}}`)); err != nil {
			t.Fatalf("first Apply: %v", err)
		}
		path := filepath.Join(applier.root, hooksDirName, "PreToolUse.d", "a.sh")
		if !applier.execs[path] {
			t.Fatal("the first write left the script unexecutable, want it executable")
		}
		if err := store.Doc().Apply(string(Hooks),
			json.RawMessage(`{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"c2hlbGw=","exec":false}}}`)); err != nil {
			t.Fatalf("second Apply: %v", err)
		}
		if applier.execs[path] {
			t.Error("the script is still executable, want the peer's flip to land")
		}
		if got := string(applier.files[path]); got != "shell" {
			t.Errorf("file on disk = %q, want the contents untouched", got)
		}
	})

	// An arriving item that already holds what is on disk writes nothing and
	// records nothing: a re-apply that rewrote every file would stamp and
	// re-export the document on every import.
	t.Run("an identical re-apply is unstamped", func(t *testing.T) {
		t.Parallel()
		store, applier, _ := hooksRootStore(t)
		arriving := `{"files":{"hooks/PreToolUse.d/a.sh":{"contents":"c2hlbGw=","exec":true}}}`
		if err := store.Doc().Apply(string(Hooks), json.RawMessage(arriving)); err != nil {
			t.Fatalf("first Apply: %v", err)
		}
		writes := applier.writes
		cache := store.readFileHashes()
		if err := store.Doc().Apply(string(Hooks), json.RawMessage(arriving)); err != nil {
			t.Fatalf("second Apply: %v", err)
		}
		if applier.writes != writes {
			t.Errorf("an identical item rewrote the file: %d writes, want %d", applier.writes, writes)
		}
		if got := store.readFileHashes(); !reflect.DeepEqual(got, cache) {
			t.Errorf("an identical item moved the hash cache:\n%+v\nwant\n%+v", got, cache)
		}
	})
}

// TestCleanPeerUpdateIsNotDrift pins that the hand edit is the one file doctor
// names. A peer update to an untouched script lands, so it is not drift, and
// reporting it would tell a person their script had drifted every time a peer
// touched it.
func TestCleanPeerUpdateIsNotDrift(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := openStore(t).WithFiles(diskApplier{root: root}, root)
	relA := writeScript(t, root, "PreToolUse", "a.sh", "first\n", true)
	relB := writeScript(t, root, "PreToolUse", "b.sh", "other\n", true)
	if _, err := store.As("config set", "").Put(Hooks, []byte(`{"events":{"PreToolUse":[[`+
		jsonStr(relA)+"],["+jsonStr(relB)+"]]}}")); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	if _, err := store.CaptureHookFiles(); err != nil {
		t.Fatalf("first capture: %v", err)
	}
	// A person edits one script in their editor, on this machine alone.
	pathA := filepath.Join(root, filepath.FromSlash(relA))
	writeFile(t, pathA, "edited by a person\n", 0o755)
	// The peer sends an update to the untouched script and restates the edited
	// one, which is what a peer that has not seen the edit sends.
	if err := store.Doc().Apply(string(Hooks), json.RawMessage(
		`{"files":{"`+relA+`":{"contents":"Zmlyc3QK","exec":true},`+
			`"`+relB+`":{"contents":"`+base64.StdEncoding.EncodeToString([]byte("updated\n"))+`","exec":true}}}`)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFileString(t, filepath.Join(root, filepath.FromSlash(relB))); got != "updated\n" {
		t.Errorf("the untouched script = %q, want the peer's update", got)
	}
	if got := readFileString(t, pathA); got != "edited by a person\n" {
		t.Errorf("the edited script = %q, want the hand edit kept", got)
	}
	edited, err := store.DriftedHookFiles()
	if err != nil {
		t.Fatalf("DriftedHookFiles: %v", err)
	}
	if len(edited) != 1 {
		t.Fatalf("drifted = %v, want only the hand-edited script", edited)
	}
	if edited[0].Path != pathA {
		t.Errorf("drifted path = %q, want %q", edited[0].Path, pathA)
	}
}

// TestHookFileChangeStampsAndSignals pins the capture as a person's edit: a
// hand-edited script on disk is a config write, so the hooks item is stamped
// and the document is signalled for export.
func TestHookFileChangeStampsAndSignals(t *testing.T) {
	t.Parallel()
	store, _, root := hooksRootStore(t)
	rel := writeScript(t, root, "PreToolUse", "check.sh", "first\n", true)
	if _, err := store.As("config set", "").Put(Hooks,
		[]byte(`{"events":{"PreToolUse":[[`+jsonStr(rel)+`]]}}`)); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	if _, err := store.CaptureHookFiles(); err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if err := clearDocSignals(store); err != nil {
		t.Fatalf("clear signals: %v", err)
	}
	// A person edits the script in their editor.
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("second\n"), 0o755); err != nil {
		t.Fatalf("edit script: %v", err)
	}
	changed, err := store.CaptureHookFiles()
	if err != nil || !changed {
		t.Fatalf("CaptureHookFiles = %v, %v; want the edit to be captured", changed, err)
	}
	if _, ok, err := store.Doc().ItemWriteStamp(string(Hooks)); err != nil || !ok {
		t.Fatalf("hooks write stamp = %v (present %v); want the capture to stamp it", ok, err)
	}
	signals, err := store.db.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if !hasConfigSignal(signals) {
		t.Fatalf("signals = %v, want the config document signalled", signals)
	}
}

// TestUnchangedHookFileIsUnstamped pins the other half of the capture: with
// nothing edited there is no write, so no stamp and no signal. A capture on
// every steady attempt that stamped every time would make the config re-export
// itself forever.
func TestUnchangedHookFileIsUnstamped(t *testing.T) {
	t.Parallel()
	store, _, root := hooksRootStore(t)
	rel := writeScript(t, root, "PreToolUse", "check.sh", "first\n", true)
	if _, err := store.As("config set", "").Put(Hooks,
		[]byte(`{"events":{"PreToolUse":[[`+jsonStr(rel)+`]]}}`)); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	if _, err := store.CaptureHookFiles(); err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if err := clearDocSignals(store); err != nil {
		t.Fatalf("clear signals: %v", err)
	}
	before, _, err := store.Doc().ItemWriteStamp(string(Hooks))
	if err != nil {
		t.Fatalf("read the stamp before: %v", err)
	}
	changed, err := store.CaptureHookFiles()
	if err != nil || changed {
		t.Fatalf("CaptureHookFiles = %v, %v; want no change on an unedited file", changed, err)
	}
	after, ok, err := store.Doc().ItemWriteStamp(string(Hooks))
	if err != nil {
		t.Fatalf("read the stamp after: %v", err)
	}
	if !ok || after != before {
		t.Fatalf("hooks write stamp = %q (present %v), want the unchanged %q", after, ok, before)
	}
	signals, err := store.db.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if hasConfigSignal(signals) {
		t.Fatalf("signals = %v, want the config document left alone", signals)
	}
}

// TestScriptAddedRemovedAndExecFlipped pins the three capture cases the plan
// names: a script appears, one goes away, and one loses its executable bit.
func TestScriptAddedRemovedAndExecFlipped(t *testing.T) {
	t.Parallel()
	store, _, root := hooksRootStore(t)
	first := writeScript(t, root, "PreToolUse", "a.sh", "a\n", true)
	second := writeScript(t, root, "PreToolUse", "b.sh", "b\n", true)
	if _, err := store.As("config set", "").Put(Hooks, []byte(`{"events":{"PreToolUse":[[`+
		jsonStr(first)+`],["`+first+`"]]}}`)); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	if _, err := store.CaptureHookFiles(); err != nil {
		t.Fatalf("first capture: %v", err)
	}
	doc, err := store.HookDoc()
	if err != nil {
		t.Fatalf("HookDoc: %v", err)
	}
	if len(doc.Files) != 2 {
		t.Fatalf("captured %d scripts, want both on disk", len(doc.Files))
	}

	// A script is removed from the disk and one loses its executable bit.
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(second))); err != nil {
		t.Fatalf("remove script: %v", err)
	}
	if err := os.Chmod(filepath.Join(root, filepath.FromSlash(first)), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if changed, err := store.CaptureHookFiles(); err != nil || !changed {
		t.Fatalf("CaptureHookFiles = %v, %v; want the removal and the flipped bit", changed, err)
	}
	doc, err = store.HookDoc()
	if err != nil {
		t.Fatalf("HookDoc: %v", err)
	}
	if _, still := doc.Files[second]; still {
		t.Errorf("a removed script is still carried: %v", sortedFiles(doc.Files))
	}
	if doc.Files[first].Exec {
		t.Error("exec bit = true after the file lost it")
	}
}

// TestDeletedArgvScriptStillExports pins that a deletion is a person's edit
// like any other. An argv row keeps naming a script this machine no longer
// has, and the capture has to carry that away rather than fail the export and
// the capture both over a file that is simply gone.
func TestDeletedArgvScriptStillExports(t *testing.T) {
	t.Parallel()
	store, _, root := hooksRootStore(t)
	kept := writeScript(t, root, "PreToolUse", "kept.sh", "kept\n", true)
	gone := writeScript(t, root, "PreToolUse", "gone.sh", "gone\n", true)
	if _, err := store.As("config set", "").Put(Hooks, []byte(`{"events":{"PreToolUse":[[`+
		jsonStr(kept)+"],["+jsonStr(gone)+"]]}}")); err != nil {
		t.Fatalf("put hooks: %v", err)
	}
	if _, err := store.CaptureHookFiles(); err != nil {
		t.Fatalf("first capture: %v", err)
	}

	// A person deletes the script one argv row still names.
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(gone))); err != nil {
		t.Fatalf("remove script: %v", err)
	}
	items, err := store.Doc().Items()
	if err != nil {
		t.Fatalf("Items after the deletion: %v", err)
	}
	doc, err := ParseHookDoc(items[string(Hooks)])
	if err != nil {
		t.Fatalf("ParseHookDoc: %v", err)
	}
	if _, still := doc.Files[gone]; still {
		t.Errorf("the deleted script is still carried: %v", sortedFiles(doc.Files))
	}
	if _, keptHere := doc.Files[kept]; !keptHere {
		t.Errorf("the surviving script was dropped too: %v", sortedFiles(doc.Files))
	}

	// The capture records the deletion as the edit it is.
	changed, err := store.CaptureHookFiles()
	if err != nil {
		t.Fatalf("CaptureHookFiles after the deletion: %v", err)
	}
	if !changed {
		t.Error("CaptureHookFiles = false, want the deletion captured")
	}
}

// TestHookScriptSymlinkIsNotCarried pins the discovery rule: the walk skips
// anything that is not a regular file, so a symlink under a hook event is
// neither followed nor exported.
func TestHookScriptSymlinkIsNotCarried(t *testing.T) {
	t.Parallel()
	store, _, root := hooksRootStore(t)
	dir := filepath.Join(root, hooksDirName, "PreToolUse.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "outside.sh")
	if err := os.WriteFile(target, []byte("secret\n"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.sh")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.CaptureHookFiles(); err != nil {
		t.Fatalf("CaptureHookFiles: %v", err)
	}
	doc, err := store.HookDoc()
	if err != nil {
		t.Fatalf("HookDoc: %v", err)
	}
	if rel := HookPathOf("PreToolUse", "link.sh"); len(doc.Files[rel].Contents) != 0 {
		t.Errorf("a symlinked script was carried: %q", doc.Files[rel].Contents)
	}
}

// clearDocSignals forgets every pending export signal, so a test can tell the
// signal the capture made from the one the write that seeded it made.
func clearDocSignals(s *Store) error {
	signals, err := s.db.DocSignals()
	if err != nil {
		return err
	}
	for _, sig := range signals {
		if err := s.db.RecordDocExport(sig, nil); err != nil {
			return err
		}
	}
	return nil
}

// hasConfigSignal reports whether the config document is signalled for export.
func hasConfigSignal(signals []db.DocSignal) bool {
	for _, sig := range signals {
		if sig.Doc == Config {
			return true
		}
	}
	return false
}

// jsonStr encodes one string as a JSON string, for a body built by
// concatenation in a test.
func jsonStr(s string) string { return string(mustJSON(s)) }

// readFileString reads a file whole, so an assertion about what a write landed
// on the disk does not have to repeat the read error handling.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func containsArg(body, script string) bool {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return false
	}
	for _, member := range []string{hooksEventsMember, hooksLocalMember, hooksFilesMember} {
		raw, ok := parsed[member]
		if !ok {
			continue
		}
		if string(raw) != "" && indexOfScript(string(raw), script) >= 0 {
			return true
		}
	}
	return false
}

func indexOfScript(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
