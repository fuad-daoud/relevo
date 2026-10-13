package relevo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests drive the hooks files through the real sync pipeline on the
// in-memory log: nothing here binds the config document by hand, so a
// construction site that forgot to bind it fails.
//
// The applier resolves its root through XDG_CONFIG_HOME, so each machine's
// attempt runs with that machine's root in the environment. That is the only
// lever a test has, and it is the same one a person has: which config root
// this installation reads.

// writeHookScript puts an executable hook script under root and returns its
// root-relative name, the name the config document carries it under.
func writeHookScript(t *testing.T, root, event, file, body string) string {
	t.Helper()
	dir := filepath.Join(root, "hooks", event+".d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}
	return config.HookPathOf(event, file)
}

// attemptIn runs one machine's steady attempt with that machine's config root
// in the environment.
func (m *cfgMachine) attemptIn(t *testing.T, root string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", root)
	m.attempt(t)
}

// hooksOf reads the hook scripts this machine's config document carries.
func hooksOf(t *testing.T, m *cfgMachine) config.HookDoc {
	t.Helper()
	doc, err := m.cfg.HookDoc()
	if err != nil {
		t.Fatalf("HookDoc: %v", err)
	}
	return doc
}

// TestHandEditedHookExportsOnTheNextAttempt pins MasterMind correction C4: a
// person editing a hook script on disk is a person's edit, and it exports on
// the next steady attempt without waiting for the tick, a config write, an
// import, a join or an unlock.
func TestHandEditedHookExportsOnTheNextAttempt(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	aRoot, bRoot := t.TempDir(), t.TempDir()

	rel := writeHookScript(t, aRoot, "PreToolUse", "check.sh", "first\n")
	a.set(t, config.Hooks, `{"events":{"PreToolUse":[["`+rel+`"]]}}`)
	a.attemptIn(t, aRoot)
	b.attemptIn(t, bRoot)

	first, ok := hooksOf(t, b).Files[rel]
	if !ok || string(first.Contents) != "first\n" {
		t.Fatalf("B's copy of %s = %q (present %v); want the script A shipped", rel, first.Contents, ok)
	}

	// A person edits the script in their editor: no config write, no import,
	// no daemon restart.
	if err := os.WriteFile(filepath.Join(aRoot, filepath.FromSlash(rel)), []byte("edited\n"), 0o755); err != nil {
		t.Fatalf("hand edit: %v", err)
	}
	a.attemptIn(t, aRoot)
	b.attemptIn(t, bRoot)

	second, ok := hooksOf(t, b).Files[rel]
	if !ok || string(second.Contents) != "edited\n" {
		t.Fatalf("B's copy of %s = %q (present %v); want the hand edit to have exported", rel, second.Contents, ok)
	}
}

// TestHookFilesApplyLandedOnThePeer drives the same exchange the other way: a
// hook script one machine writes is written to disk on the peer, under the
// peer's own config root, at the path the event name computes.
func TestHookFilesApplyLandedOnThePeer(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	aRoot, bRoot := t.TempDir(), t.TempDir()

	rel := writeHookScript(t, aRoot, "PreToolUse", "check.sh", "#!/bin/sh\nexit 0\n")
	a.set(t, config.Hooks, `{"events":{"PreToolUse":[["`+rel+`"]]}}`)
	a.attemptIn(t, aRoot)
	b.attemptIn(t, bRoot)

	landed := filepath.Join(bRoot, "hooks", "PreToolUse.d", "check.sh")
	data, err := os.ReadFile(landed)
	if err != nil {
		t.Fatalf("read the landed script at %s: %v", landed, err)
	}
	if string(data) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("landed script = %q, want the one A shipped", data)
	}
	info, err := os.Stat(landed)
	if err != nil {
		t.Fatalf("stat the landed script: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("landed script mode = %v, want it executable under the hooks directory", info.Mode())
	}
}

// TestPeerHookScriptKeepsThisMachinesEdit pins the hash guard across the wire:
// a file this machine has edited is kept when a peer's copy of it arrives, and
// the peer's other scripts still land.
func TestPeerHookScriptKeepsThisMachinesEdit(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	aRoot, bRoot := t.TempDir(), t.TempDir()

	kept := writeHookScript(t, bRoot, "PreToolUse", "kept.sh", "mine\n")
	other := writeHookScript(t, bRoot, "PreToolUse", "other.sh", "theirs\n")
	b.set(t, config.Hooks, `{"events":{"PreToolUse":[["`+kept+`"],["`+other+`"]]}}`)

	// B captures both, then a person edits the first on B alone.
	b.attemptIn(t, bRoot)
	if err := os.WriteFile(filepath.Join(bRoot, filepath.FromSlash(kept)), []byte("edited here\n"), 0o755); err != nil {
		t.Fatalf("edit on B: %v", err)
	}
	// A has its own copies, and loses the one B edited.
	writeHookScript(t, aRoot, "PreToolUse", "kept.sh", "from a\n")
	writeHookScript(t, aRoot, "PreToolUse", "other.sh", "from a\n")
	a.set(t, config.Hooks, `{"events":{"PreToolUse":[["`+kept+`"],["`+other+`"]]}}`)
	a.attemptIn(t, aRoot)
	b.attemptIn(t, bRoot)

	keptPath := filepath.Join(bRoot, filepath.FromSlash(kept))
	data, err := os.ReadFile(keptPath)
	if err != nil {
		t.Fatalf("read the kept script: %v", err)
	}
	if string(data) != "edited here\n" {
		t.Errorf("script on disk = %q, want the hand edit kept", data)
	}
	otherPath := filepath.Join(bRoot, filepath.FromSlash(other))
	if _, err := os.ReadFile(otherPath); err != nil {
		t.Errorf("the arriving script did not land at %s: %v", otherPath, err)
	}
}

// TestOutsideRootHookArgvNeverLeavesTheMachine pins the path rule end to end:
// an argv row naming a file outside the config root is divided out before the
// export, so the peer never receives it while the shared script beside it does.
func TestOutsideRootHookArgvNeverLeavesTheMachine(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	aRoot, bRoot := t.TempDir(), t.TempDir()

	outside := filepath.Join(t.TempDir(), "elsewhere.sh")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write the outside script: %v", err)
	}
	encoded, err := json.Marshal(outside)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	rel := writeHookScript(t, aRoot, "PreToolUse", "check.sh", "shared\n")
	a.set(t, config.Hooks, `{"events":{"PreToolUse":[[`+string(encoded)+`],["`+rel+`"]]}}`)
	a.attemptIn(t, aRoot)
	b.attemptIn(t, bRoot)

	// The machine-local row still runs here.
	doc, err := a.cfg.HookDoc()
	if err != nil {
		t.Fatalf("HookDoc: %v", err)
	}
	found := false
	for _, row := range doc.Local["PreToolUse"] {
		if len(row) > 0 && row[0] == outside {
			found = true
		}
	}
	if !found {
		t.Errorf("machine half = %v, want the outside-root row to stay local", doc.Local)
	}

	// And it never reached the peer: the exported body carries no such path.
	t.Setenv("XDG_CONFIG_HOME", aRoot)
	items, err := a.cfg.Doc().Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if body := string(items[string(config.Hooks)]); indexOfSubstring(body, outside) >= 0 {
		t.Errorf("the exported hooks body carries the outside path %s:\n%s", outside, body)
	}

	// The shared script did travel, which is what the division is for.
	if file, ok := hooksOf(t, b).Files[rel]; !ok || string(file.Contents) != "shared\n" {
		t.Errorf("B's copy of %s = %q (present %v); want the shared script", rel, file.Contents, ok)
	}
}

func indexOfSubstring(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
