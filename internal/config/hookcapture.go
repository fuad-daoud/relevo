package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Where the hooks section's embedded scripts land on this machine's disk. The
// interface is declared here because the document that carries the scripts is
// the one that has to decide when to write them; the implementation is built
// from this machine's config root and bound at sharedDocs.
//
// A store with no applier still carries the scripts: the config lands, the
// bytes travel, and only the write to disk is skipped. That is what lets the
// pure tests of the document run without a filesystem.
type FileApplier interface {
	// HookFilePath returns the absolute path one script of one event belongs
	// at. The directory is computed from the event name and the config root,
	// never read out of the entry, so an entry cannot steer the write.
	HookFilePath(event, file string) (string, error)
	// ReadHookFile returns what is on disk at path; ok is false when the file
	// is absent or is not a regular file.
	ReadHookFile(path string) (data []byte, ok bool, err error)
	// WriteHookFile writes data at path through a temp file and a rename in
	// path's own directory, setting the executable bit only for a path under
	// the config root's hooks directory. It reports whether it wrote.
	WriteHookFile(path string, data []byte, exec bool) (wrote bool, err error)
	// AgentFilePath returns the absolute path one native agent's harness file
	// belongs at for one kind. The directory is the kind's own harness home,
	// computed from the kind and the agent name, so an item cannot steer the
	// write and cannot name a directory outside it.
	AgentFilePath(kind, agent string) (path string, err error)
	// ReadAgentFile returns what is on disk at path; ok is false when the file
	// is absent or is not a regular file.
	ReadAgentFile(path string) (data []byte, ok bool, err error)
	// WriteAgentFile writes data at path through a temp file and a rename in
	// path's own directory. An agent definition is never executable, whatever
	// mode the item asked for. It reports whether it wrote.
	WriteAgentFile(path string, data []byte) (wrote bool, err error)
}

// hooksLocalMarker is the local kv row the outside-root argv move is recorded
// by. It is a second marker rather than the machine-layer one because that one
// is already set on every machine that has run the 3b split: a move guarded by
// it would never run there, and the shared bodies would keep this machine's
// directories.
const hooksLocalMarker = "sync.config_hooks_local.v1"

// hooksMovedMarker is the marker value.
var hooksMovedMarker = []byte(`"moved"`)

// hooksSource labels the write a capture makes, so a revision reads as what
// happened rather than as a config set nobody ran.
const hooksSource = "hook capture"

// fileHashMarker is the local kv row of the capture's hash cache: one root-
// relative script path to the digest, size, modification time and executable
// bit last seen on disk. It is a cache and only ever a shortcut: an entry that
// disagrees with the disk costs one read and is replaced.
const fileHashMarker = "sync.filehash.v1"

// WithFiles binds the applier the embedded scripts are written through and the
// config root every root-relative name is measured against. The receiver is
// unchanged, so a runtime's base store stays unbound.
//
// The outside-root argv move runs here rather than at Open, because this is the
// first binding that knows which root this machine has. A store with no root
// cannot tell an argv row naming something outside it from one naming something
// inside it, so it moves nothing and records nothing, and the move is still
// waiting for the binding that can do it.
func (s *Store) WithFiles(files FileApplier, root string) *Store {
	c := *s
	c.files = files
	c.root = root
	// A failure is left to the binding's own write rather than reported here:
	// the marker is what the exporter checks, and a store that could not write
	// has not moved anything.
	_, _ = c.EnsureHooksLocal()
	return &c
}

// AgentRerenderer re-renders one agent's files on every harness kind on PATH.
// It is called once the transaction that applied the agent has committed,
// because the render reads the config the apply wrote and a render that read
// the state before it would render the agent as it used to be.
//
// It reports nothing: a render that cannot run is a warning, exactly as
// refreshRoles treats a definition it could not write.
type AgentRerenderer func(agent string)

// WithAgentRenders binds the re-render an arriving agents item queues. The
// receiver is unchanged, so a store with no renderer still lands the item and
// its files; only the render is skipped.
func (s *Store) WithAgentRenders(rerender AgentRerenderer) *Store {
	c := *s
	c.rerender = rerender
	return &c
}

// ConfigRoot is the config root this store was bound to, or the empty string.
func (s *Store) ConfigRoot() string { return s.root }

// sharedHooksDoc reads the hooks section's shared body with this machine's
// config root attached, so every relative name can be checked against it.
func (s *Store) sharedHooksDoc(t *db.Tx) (HookDoc, json.RawMessage, error) {
	empty := json.RawMessage(`{}`)
	raw, ok, err := t.ConfigGet(string(Hooks))
	if err != nil {
		return HookDoc{}, nil, err
	}
	if !ok {
		raw = empty
	}
	doc, err := ParseHookDoc(raw)
	if err != nil {
		return HookDoc{}, nil, err
	}
	doc.Root = s.root
	return doc, raw, nil
}

// HookDoc reads the hooks section as a reader sees it: the shared half with
// this machine's overrides over it.
func (s *Store) HookDoc() (HookDoc, error) {
	var doc HookDoc
	err := s.db.Tx(func(t *db.Tx) error {
		_, raw, err := s.sharedHooksDoc(t)
		if err != nil {
			return err
		}
		merged, err := mergedBody(t, Hooks, raw)
		if err != nil {
			return err
		}
		doc, err = ParseHookDoc(merged)
		if err != nil {
			return err
		}
		doc.Root = s.root
		return nil
	})
	return doc, err
}

// sharedHooksDocInTx reads the shared half outside a caller's transaction, for
// a caller that has none.
func (s *Store) sharedHooksDocInTx() (HookDoc, json.RawMessage, error) {
	var doc HookDoc
	var raw json.RawMessage
	err := s.db.Tx(func(t *db.Tx) error {
		var err error
		doc, raw, err = s.sharedHooksDoc(t)
		return err
	})
	return doc, raw, err
}

// EnsureHooksLocal runs the outside-root argv move once and records the
// marker. moved is false once the marker is set, which is what makes a second
// call a no-op rather than a second move.
//
// A store with no config root moves nothing and records nothing: with nothing
// to measure the rows against, the move would leave every row where it is and
// still mark the work done, which is the one outcome the marker must never
// report. The move runs when a root is bound instead.
func (s *Store) EnsureHooksLocal() (moved bool, err error) {
	if s.root == "" {
		return false, nil
	}
	if _, done, err := s.db.KVGet(hooksLocalMarker); err != nil || done {
		return false, err
	}
	err = s.db.Tx(func(t *db.Tx) error {
		stored, _, err := s.sharedHooksDoc(t)
		if err != nil {
			return err
		}
		normalized, changed, err := s.NormalizeHookDoc(stored)
		if err != nil {
			return err
		}
		if !changed {
			return t.KVPut(hooksLocalMarker, hooksMovedMarker)
		}
		moved = true
		if err := s.writeHooks(t, normalized); err != nil {
			return err
		}
		return t.KVPut(hooksLocalMarker, hooksMovedMarker)
	})
	if err != nil {
		return false, err
	}
	return moved, nil
}

// hooksLocalReady refuses while an argv row naming something outside the
// config root may still sit in the shared body, which is the state an exporter
// must not send a peer.
func (s *Store) hooksLocalReady() error {
	_, done, err := s.db.KVGet(hooksLocalMarker)
	if err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("config: hook argv is still in the shared hooks body: %w", ErrInvalidValue)
	}
	return nil
}

// writeHooks stores one normalized hooks document: the shared half as a config
// section and the machine half as a local-layer patch, in the caller's
// transaction so neither half is right without the other.
func (s *Store) writeHooks(t *db.Tx, doc HookDoc) error {
	if err := ValidateHookDoc(doc); err != nil {
		return invalidValueOf(err)
	}
	// The value handed to the machine-field split carries the machine half as
	// its `local` member, so the split is what takes it out of the shared body
	// and into the local layer. Encoding the shared half alone and splitting
	// that would divide nothing.
	body, err := EncodeHookDoc(doc)
	if err != nil {
		return invalidValueOf(err)
	}
	if len(doc.Local) > 0 {
		local, err := EncodeHookLocal(doc)
		if err != nil {
			return err
		}
		if body, err = mergePatch(body, local); err != nil {
			return err
		}
	}
	sharedBody, patches, err := splitMachineFields(Hooks, body)
	if err != nil {
		return invalidValueOf(err)
	}
	if err := writeLocalPatches(t, patches, s.now().UTC()); err != nil {
		return err
	}
	return t.ConfigPut(string(Hooks), sharedBody, s.now().UTC())
}

// fileHash is one cache entry: what the capture last saw of a script on disk.
type fileHash struct {
	Hash string `json:"h"`
	Size int64  `json:"s"`
	Exec bool   `json:"x"`
	Mod  int64  `json:"m"`
}

func (f fileHash) matches(info os.FileInfo) bool {
	return f.Size == info.Size() && f.Mod == info.ModTime().UnixNano() &&
		f.Exec == (info.Mode()&0o111 != 0)
}

// capturedHooks is what one capture read off the disk: the document the disk
// describes and the cache that describes how it was read.
type capturedHooks struct {
	doc    HookDoc
	hashes map[string]fileHash
	raw    json.RawMessage
}

// CaptureHookFiles reads the hook scripts on disk and stores the ones whose
// contents differ from what the document carries. changed reports whether the
// document moved, which is false for the ordinary case of nothing having been
// edited: an unchanged file costs a stat and a cache hit, no stamp, no signal
// and no revision.
//
// It is a person's edit when it fires. A hook script a person edited in their
// editor has to reach the peers, so the write stamps the hooks item and the
// config_doc trigger signals the document, exactly as `relevo config set`
// would.
func (s *Store) CaptureHookFiles() (changed bool, err error) {
	if s.root == "" || s.files == nil {
		return false, nil
	}
	if err := s.hooksLocalReady(); err != nil {
		return false, err
	}
	captured, err := s.captureHooks()
	if err != nil || captured == nil {
		return false, err
	}
	writer := s.As(hooksSource, "hook scripts edited on disk")
	err = writer.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if err := writer.writeHooks(t, captured.doc); err != nil {
			return err
		}
		encoded, err := json.Marshal(captured.hashes)
		if err != nil {
			return err
		}
		if err := t.KVPut(fileHashMarker, encoded); err != nil {
			return err
		}
		// The write goes through record, which is what stamps the item and
		// leaves the revision. Without it a captured edit would be stored and
		// never exported, because the exporter only reads a key this machine
		// has stamped.
		return writer.record(t, before, nil)
	})
	return true, err
}

// captureHooks is the read half of CaptureHookFiles: the document the disk
// describes, or nil when the disk describes the document already.
func (s *Store) captureHooks() (*capturedHooks, error) {
	var stored HookDoc
	var raw json.RawMessage
	if err := s.db.Tx(func(t *db.Tx) error {
		var err error
		stored, raw, err = s.sharedHooksDoc(t)
		return err
	}); err != nil {
		return nil, err
	}

	cache := s.readFileHashes()
	disk, hashes, err := DiscoverHookFiles(s.root, cache, stored.Files, stored.Events)
	if err != nil {
		return nil, err
	}
	normalized, _, err := s.NormalizeHookDoc(HookDoc{
		Events: stored.Events,
		Files:  disk,
		Local:  stored.Local,
		Root:   s.root,
	})
	if err != nil {
		return nil, err
	}
	encoded, err := EncodeHookDoc(normalized)
	if err != nil {
		return nil, invalidValueOf(err)
	}
	if sameJSON(encoded, raw) && sameFileHashes(cache, hashes) {
		return nil, nil
	}
	return &capturedHooks{doc: normalized, hashes: hashes, raw: raw}, nil
}

// applyHookFiles writes the scripts an arriving hooks item carries, keeping a
// file a person has edited: only a missing file, or one whose bytes still hold
// what the capture last recorded, is written.
//
// It runs inside the importer's transaction, so the document and the files it
// carries land as one decision. The document lands in every case, so a peer's
// other scripts travel even when this machine keeps one of its own.
func (s *Store) applyHookFiles(t *db.Tx, doc HookDoc) error {
	if s.files == nil {
		return nil
	}
	cache := s.readFileHashesIn(t)
	for _, rel := range sortedFiles(doc.Files) {
		if err := validateHookFilePath(rel); err != nil {
			return invalidValueOf(err)
		}
		file := doc.Files[rel]
		if len(file.Contents) > MaxHookFileBytes {
			return invalidValueOf(fmt.Errorf("hooks: %s is %d bytes, over the %d byte cap: %w",
				rel, len(file.Contents), MaxHookFileBytes, errOversize))
		}
		event, base, err := HookParts(rel)
		if err != nil {
			return invalidValueOf(err)
		}
		full, err := s.files.HookFilePath(event, base)
		if err != nil {
			return err
		}
		current, ok, err := s.files.ReadHookFile(full)
		if err != nil {
			return err
		}
		if ok {
			// Bytes that already equal the arriving ones are left alone: the
			// only thing left to reconcile is the executable bit, which is part
			// of what the document carries, so a peer that flips it lands. A
			// bit that already agrees writes nothing at all, because rewriting
			// a file with itself is the churn a re-apply must not cause.
			if bytes.Equal(current, file.Contents) && hookFileExecSettled(rel, file.Exec, cache) {
				continue
			}
			// Differing bytes are a peer's update only while the cache still
			// vouches for what is on disk: that is a file this machine has not
			// changed since the capture read it or a write put it there. A
			// cache that disagrees is a hand edit, which is kept -- the
			// document still lands, so the peer's other scripts travel, and
			// `relevo doctor` names this one.
			if !bytes.Equal(current, file.Contents) && !hookFileUnchanged(rel, current, cache) {
				continue
			}
		}
		if _, err := s.files.WriteHookFile(full, file.Contents, file.Exec); err != nil {
			return err
		}
		// What is on disk is now what the item carried, so the cache says so:
		// a file this machine wrote joins the capture that will read it back,
		// and the next arriving copy of it is an update rather than a hand
		// edit. Without this a script written from a peer could never be
		// updated by a peer again.
		cache[rel] = fileHash{
			Hash: file.Hash(),
			Size: int64(len(file.Contents)),
			Exec: file.Exec,
		}
	}
	return s.recordFileHashes(t, cache)
}

// hookFileUnchanged reports whether the bytes on disk are still the ones the
// cache last recorded for rel, which is what makes an arriving copy safe to
// write over them. An entry the cache cannot vouch for is a hand edit.
func hookFileUnchanged(rel string, current []byte, cache map[string]fileHash) bool {
	seen, held := cache[rel]
	if !held {
		return false
	}
	return HookFile{Contents: current}.Hash() == seen.Hash
}

// hookFileExecSettled reports whether the executable bit already on disk is
// the one the arriving file carries, so an identical file needs no write. An
// entry the cache does not hold is not settled: nothing says what mode the
// file has, so the write that settles it is the safe answer.
func hookFileExecSettled(rel string, exec bool, cache map[string]fileHash) bool {
	seen, held := cache[rel]
	return held && seen.Exec == exec
}

// readFileHashes reads the capture's hash cache. A row that does not parse is
// an empty cache rather than an error: it is only ever a shortcut, and a missing
// one costs a read per file rather than a refusal.
func (s *Store) readFileHashes() map[string]fileHash {
	raw, ok, err := s.db.KVGet(fileHashMarker)
	if err != nil || !ok {
		return map[string]fileHash{}
	}
	return decodeFileHashes(raw)
}

// readFileHashesIn is readFileHashes inside a caller's transaction, so an apply
// reads the cache the capture last wrote rather than a second connection's view
// of it.
func (s *Store) readFileHashesIn(t *db.Tx) map[string]fileHash {
	raw, ok, err := t.KVGet(fileHashMarker)
	if err != nil || !ok {
		return map[string]fileHash{}
	}
	return decodeFileHashes(raw)
}

func decodeFileHashes(raw []byte) map[string]fileHash {
	out := map[string]fileHash{}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]fileHash{}
	}
	return out
}

// recordFileHashes stores the cache back, and only when it changed: a hook
// apply that wrote nothing must leave no trace for the next capture to read as
// a change.
func (s *Store) recordFileHashes(t *db.Tx, cache map[string]fileHash) error {
	encoded, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	existing, ok, err := t.KVGet(fileHashMarker)
	if err != nil {
		return err
	}
	if ok && string(existing) == string(encoded) {
		return nil
	}
	return t.KVPut(fileHashMarker, encoded)
}

// EditedHookFile is one script on disk that no longer holds what the document
// carries, with the path a person would look at.
type EditedHookFile struct {
	Path string
	Hash string
}

// DriftedHookFiles reports the scripts on disk that no longer hold what the
// document carries. A file the document does not carry is not drift: it is a
// script nobody has synced.
func (s *Store) DriftedHookFiles() ([]EditedHookFile, error) {
	doc, err := s.HookDoc()
	if err != nil {
		return nil, err
	}
	var out []EditedHookFile
	for _, rel := range sortedFiles(doc.Files) {
		file, err := readHookFileAt(s.root, rel)
		if err != nil || file == nil {
			continue
		}
		if file.Hash() != doc.Files[rel].Hash() {
			out = append(out, EditedHookFile{Path: filepath.Join(s.root, filepath.FromSlash(rel)), Hash: file.Hash()})
		}
	}
	return out, nil
}

func sameFileHashes(a, b map[string]fileHash) bool {
	if len(a) != len(b) {
		return false
	}
	for key, want := range b {
		got, ok := a[key]
		if !ok || got != want {
			return false
		}
	}
	return true
}

// captureAfterFilesWrite runs the captures after a write of the sections whose
// items carry files has committed, so a write that adds a hook row or a native
// agent also brings the file the row names. A capture that cannot read the
// disk leaves the document as it is, which is the state the write made.
func (s *Store) captureAfterHooksWrite(sec Section) {
	if s.files == nil {
		return
	}
	switch sec {
	case Hooks:
		if s.root == "" {
			return
		}
		if _, err := s.CaptureHookFiles(); err != nil && !errors.Is(err, ErrInvalidValue) {
			return
		}
	case Agents:
		_, _ = s.CaptureAgentFiles()
	}
}

// normalizeSectionBody is the path-aware half of the machine-field split for
// the hooks section: a write of argv rows has to be divided by path before
// splitMachineFields runs, because what leaves the shared body is decided by
// whether argv[0] names a file under this machine's config root.
//
// Every other section returns its body unchanged, so the call sites do not have
// to know which sections have a path-aware rule.
func (s *Store) normalizeSectionBody(sec Section, body json.RawMessage) (json.RawMessage, error) {
	if sec != Hooks || s.root == "" {
		return body, nil
	}
	doc, err := ParseHookDoc(body)
	if err != nil {
		return nil, err
	}
	doc.Root = s.root
	normalized, _, err := s.NormalizeHookDoc(doc)
	if err != nil {
		return nil, err
	}
	shared, err := EncodeHookDoc(normalized)
	if err != nil {
		return nil, err
	}
	if len(normalized.Local) == 0 {
		return shared, nil
	}
	local, err := EncodeHookLocal(normalized)
	if err != nil {
		return nil, err
	}
	return mergePatch(shared, local)
}

// validateHooksBody refuses a hooks body against this store's config root:
// the caps, and every name that is not one plain component under the
// directory its own event name computes.
func (s *Store) validateHooksBody(body json.RawMessage) error {
	hooks, err := ParseHookDoc(body)
	if err != nil {
		return invalidValueOf(err)
	}
	hooks.Root = s.root
	return ValidateHookDoc(hooks)
}
