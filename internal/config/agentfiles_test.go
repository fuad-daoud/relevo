package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// The native agents entry carries each kind's harness file beside the entry
// that names it, so a peer receives the agent rather than a pointer to a
// definition it may never have had. These tests pin the three rules that shape
// that member: the file is the one the kind's own home holds, a kind the entry
// does not name is refused, and the caps hold.

// nativeItem is one agents item body: the entry plus, when files is non-nil,
// the member that carries them. The files map is kind to the definition's own
// contents -- this helper does the base64 the wire form needs, so a test states
// what the file holds rather than how it is spelled.
func nativeItem(name, shape string, native map[string]roles.DefRow, files map[string]string) string {
	entry := map[string]any{"shape": shape}
	if native != nil {
		entry["native"] = native
	}
	if files != nil {
		entry[agentsFilesMember] = agentFilesValue(files)
	}
	body, err := json.Marshal(map[string]any{name: entry})
	if err != nil {
		panic(err)
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		panic(err)
	}
	return string(items[name])
}

// agentFilesValue is the files member as the wire form spells it.
func agentFilesValue(files map[string]string) map[string]string {
	out := make(map[string]string, len(files))
	for kind, body := range files {
		out[kind] = base64.StdEncoding.EncodeToString([]byte(body))
	}
	return out
}

// agentItemBody is nativeItem as a whole section body, which is what a store
// holds.
func agentItemBody(items map[string]string) []byte {
	out := map[string]json.RawMessage{}
	for name, item := range items {
		out[name] = json.RawMessage(item)
	}
	body, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	return body
}

// agentsStore is one store with the applier bound the way sharedDocs binds the
// real one, over a temporary root. The applier is the disk one rather than the
// memory one because the capture reads the disk directly -- that is how it
// costs one stat per unchanged file -- so a memory applier would let a capture
// test pass without a file ever having existed.
func agentsStore(t *testing.T) (*Store, diskApplier) {
	t.Helper()
	root := t.TempDir()
	return openStore(t).WithFiles(diskApplier{root: root}, root), diskApplier{root: root}
}

// writeAgentFile puts a definition on disk at where the applier says it goes.
func writeAgentFile(t *testing.T, a diskApplier, kind, agent, body string) string {
	t.Helper()
	path, err := a.AgentFilePath(kind, agent)
	if err != nil {
		t.Fatalf("AgentFilePath(%s, %s): %v", kind, agent, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func readAgentFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestNativeAgentsItemsEmbedPerKindContents pins that the capture reads each
// kind's own harness file and stores it in the item, one entry per kind.
func TestNativeAgentsItemsEmbedPerKindContents(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	writeAgentFile(t, applier, "claude", "my-native", "# the claude definition\n")
	writeAgentFile(t, applier, "codex", "my-native", "name = \"the codex definition\"\n")

	store.setAgent(t, nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}, "codex": {Agent: "my-native"}}, nil))

	// A config write is one of the paths the capture runs on, so the write that
	// created the entry has already brought the definitions with it.
	doc, err := store.agentDoc(t, "my-agent")
	if err != nil {
		t.Fatalf("read item: %v", err)
	}
	for kind, want := range map[string]string{
		"claude": "# the claude definition\n",
		"codex":  "name = \"the codex definition\"\n",
	} {
		got, ok := doc.Files[kind]
		if !ok {
			t.Errorf("item carries no %s file; it carries %v", kind, doc.Kinds())
			continue
		}
		if string(got.Contents) != string(want) {
			t.Errorf("the %s file = %q, want %q", kind, got.Contents, want)
		}
	}
}

// TestNativeAgentsApplyWritesMissingOrMatching pins the hash guard: a missing
// file is written, a file this machine wrote is refreshed by a peer's newer
// copy, and a file that already holds the arriving bytes is left alone.
func TestNativeAgentsApplyWritesMissingOrMatching(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	path, err := applier.AgentFilePath("claude", "my-native")
	if err != nil {
		t.Fatalf("AgentFilePath: %v", err)
	}

	// A missing file is written.
	applied, err := store.applyAgent(t, "my-agent", nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}},
		map[string]string{"claude": "first"}))
	if err != nil || !applied {
		t.Fatalf("apply of a missing file = %v, %v; want it written", applied, err)
	}
	if got := readAgentFile(t, path); got != "first" {
		t.Fatalf("the file = %q, want the arriving bytes", got)
	}

	// A peer that sends different bytes refreshes it: this machine wrote what
	// is there, so the cache vouches for it.
	if _, err := store.applyAgent(t, "my-agent", nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}},
		map[string]string{"claude": "second"})); err != nil {
		t.Fatalf("apply of a newer copy: %v", err)
	}
	if got := readAgentFile(t, path); got != "second" {
		t.Fatalf("the file = %q, want the peer's newer bytes", got)
	}

	// The same bytes again write nothing: re-applying an entry must not churn
	// the disk.
	before := fileModTime(t, path)
	if _, err := store.applyAgent(t, "my-agent", nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}},
		map[string]string{"claude": "second"})); err != nil {
		t.Fatalf("apply of identical bytes: %v", err)
	}
	if after := fileModTime(t, path); !after.Equal(before) {
		t.Errorf("the file was rewritten for identical bytes: %v then %v", before, after)
	}
}

// TestNativeAgentsApplyKeepsHandEdits pins that a definition a person edited
// survives a peer's copy: the item still lands, so the peer's other agents
// travel, and only this file is kept.
func TestNativeAgentsApplyKeepsHandEdits(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	path := writeAgentFile(t, applier, "claude", "my-native", "what a person wrote\n")
	// The cache knows nothing about this file, so the bytes on disk are a hand
	// edit by the rule's own terms.
	store.putAgentHash(t, map[string]agentFileValue{})

	item := nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}, "codex": {Agent: "my-native"}},
		map[string]string{"claude": "peer"})
	applied, err := store.applyAgent(t, "my-agent", item)
	if err != nil || !applied {
		t.Fatalf("apply over a hand edit = %v, %v; want the item to land", applied, err)
	}
	if got := readAgentFile(t, path); got != "what a person wrote\n" {
		t.Errorf("the file = %q, want the person's edit kept", got)
	}
	if _, err := os.Stat(filepath.Join(applier.root, "codex", "my-native.md")); err == nil {
		t.Error("the item named no codex file, so none was written")
	}
	if !strings.Contains(store.agentSection(t), "my-agent") {
		t.Error("the item did not land, so a peer's other agents could not travel")
	}
}

// TestAgentsApplyRefusesUnknownKind pins that a file whose kind is not a
// harness kind is refused rather than written somewhere guessed at. The
// document's own rule is asserted as well as the apply: the section's validator
// refuses the same body first, so an apply alone would pass even with the
// document's guard gone.
func TestAgentsApplyRefusesUnknownKind(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	item := `{"shape":"writer","native":{"nonesuch":{"agent":"my-native"}},` +
		`"files":{"nonesuch":"Zmlyc3Q="}}`
	doc, err := ParseAgentEntryDoc("my-agent", json.RawMessage(item))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := ValidateAgentEntryDoc(doc); !errors.Is(err, ErrAgentPath) {
		t.Errorf("ValidateAgentEntryDoc = %v, want the path refusal", err)
	}
	applied, err := store.applyAgent(t, "my-agent", item)
	if applied || err != nil {
		t.Fatalf("apply of an unknown kind = %v, %v; want a refusal and no error", applied, err)
	}
	if _, serr := os.Stat(filepath.Join(applier.root, "nonesuch", "my-native.md")); serr == nil {
		t.Error("the refused item wrote a file under a kind that is not a harness")
	}
}

// TestAgentsApplyRefusesOversize pins the two caps: one file over the
// per-file cap, and one item over the whole-item cap. The document's own
// refusals are asserted alongside the apply, so the pin is on the rule rather
// than on whichever check happens to run first.
func TestAgentsApplyRefusesOversize(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)

	oversizeFile := `{"shape":"writer","native":{"claude":{"agent":"my-native"}},` +
		`"files":{"claude":` + quoteBase64(strings.Repeat("a", MaxAgentFileBytes+1)) + `}}`
	doc, err := ParseAgentEntryDoc("my-agent", json.RawMessage(oversizeFile))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Files["claude"].Contents) != MaxAgentFileBytes+1 {
		t.Fatalf("the parsed file is %d bytes, want %d", len(doc.Files["claude"].Contents), MaxAgentFileBytes+1)
	}
	if err := ValidateAgentEntryDoc(doc); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("ValidateAgentEntryDoc = %v, want the cap refusal", err)
	}
	applied, err := store.applyAgent(t, "my-agent", oversizeFile)
	if applied || err != nil {
		t.Fatalf("apply of a %d-byte file = %v, %v; want a refusal and no error",
			MaxAgentFileBytes+1, applied, err)
	}

	// The whole-item cap is its own rule, checked on the encode rather than on
	// the apply: with one file per harness kind there are only four kinds to
	// carry, so a valid item cannot reach a 2 MiB total on the per-file cap
	// alone. The rule is still what bounds the bytes one key stores, so it is
	// pinned here where it can be reached.
	full := AgentEntryDoc{Name: "my-agent", Entry: roles.AgentEntry{Shape: "writer"},
		Files: map[string]AgentFile{}}
	for i := 0; len(full.Files) < MaxAgentsValueBytes/MaxAgentFileBytes+1; i++ {
		full.Files[fmt.Sprintf("kind%d", i)] = AgentFile{
			Kind:     fmt.Sprintf("kind%d", i),
			Contents: bytes.Repeat([]byte("a"), MaxAgentFileBytes-1),
		}
	}
	if _, err := full.Encode(); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("an item over the whole-item cap encoded (%v), want the refusal", err)
	}

	path, perr := applier.AgentFilePath("claude", "my-native")
	if perr != nil {
		t.Fatalf("AgentFilePath: %v", perr)
	}
	if _, serr := os.Stat(path); serr == nil {
		t.Error("the oversize item wrote a file")
	}
}

// quoteBase64 renders one string the way the files member stores it: base64,
// because an agent definition is a byte string and JSON text cannot carry one
// that is not valid UTF-8 without silently changing it.
func quoteBase64(s string) string {
	return `"` + base64.StdEncoding.EncodeToString([]byte(s)) + `"`
}

// TestNativeFileChangeStampsAndSignals pins that a definition a person edited
// on disk is a config edit: the capture stamps the agents item and signals the
// document, exactly as `relevo config set` would, and a file nobody edited
// costs neither.
func TestNativeFileChangeStampsAndSignals(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	item := nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}}, map[string]string{"claude": "original"})
	store.setAgent(t, item)
	if err := clearDocSignals(store); err != nil {
		t.Fatalf("clear signals: %v", err)
	}

	// Nothing on disk: the capture reads nothing and signals nothing.
	if changed, err := store.CaptureAgentFiles(); err != nil || changed {
		t.Fatalf("CaptureAgentFiles with no file = %v, %v; want nothing", changed, err)
	}
	signals, err := store.db.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if hasConfigSignal(signals) {
		t.Errorf("signals = %v, want the config document unsignalled", signals)
	}

	// A person writes the file: the capture stores it, stamps the item and
	// signals the document, exactly as `relevo config set` would.
	writeAgentFile(t, applier, "claude", "my-native", "a person's definition\n")
	if changed, err := store.CaptureAgentFiles(); err != nil || !changed {
		t.Fatalf("CaptureAgentFiles after an edit = %v, %v; want the edit stored", changed, err)
	}
	stamp, ok, err := store.Doc().ItemWriteStamp(itemKey(Agents, "my-agent"))
	if err != nil || !ok || stamp == 0 {
		t.Errorf("the agents item stamp = %v, %v, %v; want one", stamp, ok, err)
	}
	signals, err = store.db.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if !hasConfigSignal(signals) {
		t.Errorf("signals = %v, want the config document signalled", signals)
	}

	// A second capture of an unchanged file signals nothing new: a capture on
	// every steady attempt that stamped every time would make the config
	// re-export itself forever.
	if err := clearDocSignals(store); err != nil {
		t.Fatalf("clear signals: %v", err)
	}
	if changed, err := store.CaptureAgentFiles(); err != nil || changed {
		t.Fatalf("CaptureAgentFiles with nothing edited = %v, %v; want nothing", changed, err)
	}
	if signals, err = store.db.DocSignals(); err != nil {
		t.Fatalf("DocSignals: %v", err)
	} else if hasConfigSignal(signals) {
		t.Errorf("an unchanged capture signalled the document: %v", signals)
	}
}

// TestAgentFileIsNeverExecutable pins the rule the plan states for these files
// and no other: a definition lands mode 0644 whatever the item asked for.
func TestAgentFileIsNeverExecutable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	applier := diskApplier{root: root}
	path, err := applier.AgentFilePath("claude", "my-native")
	if err != nil {
		t.Fatalf("AgentFilePath: %v", err)
	}
	if _, err := applier.WriteAgentFile(path, []byte("x")); err != nil {
		t.Fatalf("WriteAgentFile: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&0o111 != 0 {
		t.Errorf("the definition landed mode %v, want no executable bit", info.Mode())
	}
}

// TestAgentFilePathRefusesBadNames pins the path rules one refusal each: an
// unknown kind, and an agent name that is not one plain component.
func TestAgentFilePathRefusesBadNames(t *testing.T) {
	t.Parallel()
	applier := memApplier{root: t.TempDir()}
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", strings.Repeat("a", maxHookComponent+1), "a\x00b"} {
		if _, err := applier.AgentFilePath("claude", name); err == nil {
			t.Errorf("AgentFilePath(claude, %q) = no error, want a refusal", name)
		}
	}
	if _, err := applier.AgentFilePath("nonesuch", "fine"); !errors.Is(err, ErrAgentPath) {
		t.Errorf("AgentFilePath(nonesuch, fine) = %v, want the path refusal", err)
	}
}

// TestAgentFilesMemberSurvivesASectionRoundTrip pins that the member is
// carried through the split and join a per-item section goes through, so an
// entry exported is the entry that was stored.
func TestAgentFilesMemberSurvivesASectionRoundTrip(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	writeAgentFile(t, applier, "claude", "my-native", "the definition\n")
	store.setAgent(t, nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}}, nil))
	if _, err := store.CaptureAgentFiles(); err != nil {
		t.Fatalf("CaptureAgentFiles: %v", err)
	}

	body, ok, err := store.Body(Agents)
	if err != nil || !ok {
		t.Fatalf("the agents body: %v, %v", ok, err)
	}
	items, err := splits(Agents, body)
	if err != nil {
		t.Fatalf("splits: %v", err)
	}
	if !strings.Contains(string(items["my-agent"]), agentsFilesMember) {
		t.Errorf("the stored item = %s, want the files member", items["my-agent"])
	}
	if _, err := joins(Agents, items); err != nil {
		t.Fatalf("joins: %v", err)
	}
}

// --- test helpers over the store -------------------------------------------

// setAgent stores one agents item the way `relevo config set agents` would.
func (s *Store) setAgent(t *testing.T, item string) {
	t.Helper()
	names := map[string]string{}
	if current, ok, err := s.Body(Agents); err == nil && ok {
		items, err := splits(Agents, current)
		if err != nil {
			t.Fatalf("splits: %v", err)
		}
		for name, raw := range items {
			names[name] = string(raw)
		}
	}
	names["my-agent"] = item
	if _, err := s.As("config set", "").Put(Agents, agentItemBody(names)); err != nil {
		t.Fatalf("config set agents: %v", err)
	}
}

// applyAgent applies one arriving agents item through the importer's path and
// reports whether it landed.
func (s *Store) applyAgent(t *testing.T, name, item string) (bool, error) {
	t.Helper()
	var landed bool
	err := s.db.Tx(func(tx *db.Tx) error {
		refusal, err := s.applyKeyIn(tx, Agents, name, json.RawMessage(item))
		landed = refusal == nil && err == nil
		if refusal != nil {
			return nil
		}
		return err
	})
	if err != nil {
		return landed, err
	}
	return landed, nil
}

// agentDoc reads one stored agents item as the document carries it.
func (s *Store) agentDoc(t *testing.T, name string) (AgentEntryDoc, error) {
	t.Helper()
	var body []byte
	if err := s.db.Tx(func(tx *db.Tx) error {
		raw, _, err := tx.ConfigGet(string(Agents))
		body = raw
		return err
	}); err != nil {
		return AgentEntryDoc{}, err
	}
	items, err := splits(Agents, body)
	if err != nil {
		return AgentEntryDoc{}, err
	}
	return ParseAgentEntryDoc(name, items[name])
}

// agentSection is the stored agents body as text.
func (s *Store) agentSection(t *testing.T) string {
	t.Helper()
	body, _, err := s.Body(Agents)
	if err != nil {
		t.Fatalf("Body(agents): %v", err)
	}
	return string(body)
}

// putAgentHash stores the capture's cache directly, so a test can hand a file
// the cache knows nothing about -- which is what a hand edit is.
func (s *Store) putAgentHash(t *testing.T, cache map[string]agentFileValue) {
	t.Helper()
	encoded, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	if err := s.db.KVPut(agentHashMarker, encoded); err != nil {
		t.Fatalf("KVPut %s: %v", agentHashMarker, err)
	}
}

// fileModTime is a file's modification time, which is what tells a rewritten
// file from one left alone.
func fileModTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return info.ModTime()
}

// TestDoctorSeesAnEditedAgentFile pins the report the doctor row is built
// from: a definition a person edited on disk, against the copy the item
// carries, with the path a person would look at.
func TestDoctorSeesAnEditedAgentFile(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	store.setAgent(t, nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}},
		map[string]string{"claude": "what the document carries\n"}))
	path := writeAgentFile(t, applier, "claude", "my-native", "what a person wrote\n")

	carried, edited, err := store.CarriedAgentFiles()
	if err != nil {
		t.Fatalf("CarriedAgentFiles: %v", err)
	}
	if carried != 1 {
		t.Errorf("carried = %d, want the one file the item holds", carried)
	}
	if len(edited) != 1 || edited[0].Path != path {
		t.Fatalf("edited = %+v, want the one path %s", edited, path)
	}
	if edited[0].Hash != (AgentFile{Kind: "claude", Contents: []byte("what a person wrote\n")}).Hash() {
		t.Errorf("the reported hash is not the file's: %q", edited[0].Hash)
	}

	// A definition nobody has edited is not drift.
	writeAgentFile(t, applier, "claude", "my-native", "what the document carries\n")
	if _, edited, err = store.CarriedAgentFiles(); err != nil || len(edited) != 0 {
		t.Errorf("edited = %+v, %v, want none for an unedited definition", edited, err)
	}
}

// TestAgentCaptureSkipsWhatItCannotRead pins that a definition which is gone
// or is not a regular file is left out of the capture rather than failing it:
// a person who deleted or replaced a definition has made an edit, and the
// capture's job is to carry what the disk says, not to refuse.
func TestAgentCaptureSkipsWhatItCannotRead(t *testing.T) {
	t.Parallel()
	store, applier := agentsStore(t)
	store.setAgent(t, nativeItem("my-agent", "writer",
		map[string]roles.DefRow{"claude": {Agent: "my-native"}}, nil))

	// Nothing on disk at all: no capture, no error.
	if changed, err := store.CaptureAgentFiles(); err != nil || changed {
		t.Fatalf("CaptureAgentFiles with no file = %v, %v; want nothing", changed, err)
	}

	// A directory where the definition belongs is not a file to read, and not
	// one to follow either.
	path, err := applier.AgentFilePath("claude", "my-native")
	if err != nil {
		t.Fatalf("AgentFilePath: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if changed, err := store.CaptureAgentFiles(); err != nil || changed {
		t.Errorf("CaptureAgentFiles over a directory = %v, %v; want nothing", changed, err)
	}
}

// TestSafeAgentComponentAndRenderBinding pins the two seams the document
// exposes to the applier: the component rule, and the binding that lets an
// arriving agents item queue its re-render.
func TestSafeAgentComponentAndRenderBinding(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"fine", "my-native", "a.b_c"} {
		if err := SafeAgentComponent(name); err != nil {
			t.Errorf("SafeAgentComponent(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "a\x00b", strings.Repeat("a", 129)} {
		if err := SafeAgentComponent(name); err == nil {
			t.Errorf("SafeAgentComponent(%q) = nil, want a refusal", name)
		}
	}

	base := openStore(t)
	var queued []string
	bound := base.WithAgentRenders(func(agent string) { queued = append(queued, agent) })
	bound.queueAgentRender("my-agent")
	if len(queued) != 1 || queued[0] != "my-agent" {
		t.Errorf("the renderer saw %v, want [my-agent]", queued)
	}
	// A store with no renderer still lands its items; only the render is
	// skipped, so this must not panic.
	base.queueAgentRender("my-agent")
}
