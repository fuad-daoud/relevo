package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Where the agents section's embedded harness files come from and go to. The
// rules mirror the hooks' and differ only where the files differ: a harness
// file lives in the kind's own home rather than under the config root, it is
// never executable, and its name is the kind and the agent name rather than an
// event and a file name. Everything else is the same decision for the same
// reason: a missing file is written, a file whose bytes still match is
// refreshed, and a file a person has edited is kept.

// agentHashMarker is the local kv row of the capture's hash cache for files
// outside the config root: one absolute harness-file path to the digest, size
// and modification time last seen on disk. It is a separate row from the
// hooks' cache because the two describe different trees, and one row for both
// would make a section's capture rewrite the other's entries.
//
// It is a cache and only ever a shortcut: an entry that disagrees with the disk
// costs one read and is replaced.
const agentHashMarker = "sync.agenthash.v1"

// agentsSource labels the write a capture makes, so a revision reads as what
// happened rather than as a config set nobody ran.
const agentsSource = "agent capture"

// capturedAgents is what one capture read off the disk: the agents items the
// disk describes, the cache that describes how they were read, and whether
// either of them moved.
type capturedAgents struct {
	items   map[string]json.RawMessage
	hashes  map[string]agentFileValue
	changed bool
}

// CaptureAgentFiles reads the harness files of every native agent in the config
// and stores the ones whose contents differ from what the document carries.
// changed reports whether anything moved, which is false for the ordinary case
// of nothing having been edited: an unchanged file costs a stat and a cache
// hit, no stamp, no signal and no revision.
//
// It is a person's edit when it fires, exactly as CaptureHookFiles is: a
// definition a person edited in their editor has to reach the peers, so the
// write stamps the agents items it changed and the config_doc trigger signals
// the document.
func (s *Store) CaptureAgentFiles() (changed bool, err error) {
	if s.files == nil {
		return false, nil
	}
	captured, err := s.captureAgents()
	if err != nil || captured == nil {
		return false, err
	}
	writer := s.As(agentsSource, "native agent files edited on disk")
	err = writer.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(captured.hashes)
		if err != nil {
			return err
		}
		if err := t.KVPut(agentHashMarker, encoded); err != nil {
			return err
		}
		if len(captured.items) == 0 {
			return nil
		}
		body, err := writer.replaceAgentItems(t, captured.items)
		if err != nil {
			return err
		}
		if body == nil {
			return nil
		}
		// The write goes through record, which is what stamps the items and
		// leaves the revision. Without it a captured edit would be stored and
		// never exported, because the exporter only reads a key this machine
		// has stamped.
		return writer.writeRecord(t, before, Agents, body)
	})
	return captured.changed, err
}

// captureAgents is the read half of CaptureAgentFiles: the items the disk
// describes, or nil when the disk describes the document already.
func (s *Store) captureAgents() (*capturedAgents, error) {
	stored, err := s.storedAgentItems()
	if err != nil {
		return nil, err
	}
	cache := s.readAgentHashes()
	captured := &capturedAgents{
		items:  map[string]json.RawMessage{},
		hashes: map[string]agentFileValue{},
	}
	for _, name := range sortedItemNames(stored) {
		doc, err := ParseAgentEntryDoc(name, stored[name])
		if err != nil {
			return nil, err
		}
		if !doc.Native() {
			continue
		}
		changed, err := s.readAgentFiles(doc, cache, captured.hashes)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		encoded, err := doc.Encode()
		if err != nil {
			return nil, invalidValueOf(err)
		}
		captured.items[name] = encoded
	}
	captured.changed = len(captured.items) > 0 || !sameAgentHashes(cache, captured.hashes)
	if !captured.changed {
		return nil, nil
	}
	return captured, nil
}

// readAgentFiles fills doc's files and hashes with what the disk holds for one
// agent, and reports whether the item's own files moved. It stats first, so an
// unchanged definition costs one stat and no read: a cached entry the file
// still matches is taken from the document rather than read again.
//
// A path that is gone records nothing rather than failing the capture. A person
// who deletes a definition has made an edit, and the capture's job is to carry
// it; a definition that is not a regular file is left out rather than
// followed, for the same reason a hook script is.
func (s *Store) readAgentFiles(doc AgentEntryDoc, cache, hashes map[string]agentFileValue) (bool, error) {
	changed := false
	for _, kind := range doc.Kinds() {
		path, err := s.files.AgentFilePath(kind, doc.Entry.Native[kind].Agent)
		if err != nil {
			return false, invalidValueOf(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return false, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if seen, ok := cache[path]; ok && seen.matches(info) {
			if _, held := doc.Files[kind]; held {
				hashes[path] = seen
				continue
			}
		}
		contents, err := readLimited(path, MaxAgentFileBytes)
		if err != nil {
			return false, err
		}
		file := AgentFile{Kind: kind, Contents: contents}
		hashes[path] = agentFileValue{
			Hash: file.Hash(), Size: info.Size(), Mod: info.ModTime().UnixNano(),
		}
		if previous, had := doc.Files[kind]; had && bytes.Equal(previous.Contents, contents) {
			continue
		}
		doc.Files[kind] = file
		changed = true
	}
	return changed, nil
}

// storedAgentItems reads the agents section as the items the doc keys name, one
// transaction so the split and the read are the same snapshot.
func (s *Store) storedAgentItems() (map[string]json.RawMessage, error) {
	var items map[string]json.RawMessage
	err := s.db.Tx(func(t *db.Tx) error {
		body, ok, err := t.ConfigGet(string(Agents))
		if err != nil || !ok {
			return err
		}
		items, err = splits(Agents, body)
		return err
	})
	if err != nil {
		return nil, invalidValueOf(err)
	}
	return items, nil
}

// replaceAgentItems rebuilds the agents body with the captured items laid over
// the stored one, or nil when the section is not there to rebuild.
func (s *Store) replaceAgentItems(t *db.Tx, items map[string]json.RawMessage) (json.RawMessage, error) {
	body, ok, err := t.ConfigGet(string(Agents))
	if err != nil || !ok {
		return nil, err
	}
	stored, err := splits(Agents, body)
	if err != nil {
		return nil, invalidValueOf(err)
	}
	for name, item := range items {
		stored[name] = item
	}
	return joins(Agents, stored)
}

// applyAgentFiles writes the harness files an arriving agents item carries,
// keeping a file a person has edited: only a missing file, or one whose bytes
// still hold what the capture last recorded, is written. A kind the item's own
// native map names and its files member does not is skipped rather than
// refused, so a peer that captured fewer kinds still lands the agent.
//
// It runs inside the importer's transaction, so the item and the files it
// carries land as one decision. The item lands in every case, so a peer's other
// agents travel even when this machine keeps one of its own.
func (s *Store) applyAgentFiles(t *db.Tx, name string, body json.RawMessage) error {
	if s.files == nil {
		return nil
	}
	doc, err := ParseAgentEntryDoc(name, body)
	if err != nil {
		return invalidValueOf(err)
	}
	if err := ValidateAgentEntryDoc(doc); err != nil {
		return invalidValueOf(err)
	}
	if !doc.Native() {
		return nil
	}
	cache := s.readAgentHashesIn(t)
	for _, kind := range sortedAgentKinds(doc.Files) {
		file := doc.Files[kind]
		path, err := s.files.AgentFilePath(kind, doc.Entry.Native[kind].Agent)
		if err != nil {
			return invalidValueOf(err)
		}
		current, ok, err := s.files.ReadAgentFile(path)
		if err != nil {
			return err
		}
		if ok {
			if bytes.Equal(current, file.Contents) {
				// Bytes that already equal the arriving ones are left alone:
				// rewriting a file with itself is the churn a re-apply must
				// not cause.
				continue
			}
			// Differing bytes are a peer's update only while the cache still
			// vouches for what is on disk: that is a file this machine has not
			// changed since the capture read it or a write put it there. A
			// cache that disagrees is a hand edit, which is kept -- the item
			// still lands, and `relevo doctor` names this one.
			if !agentFileUnchanged(path, current, cache) {
				continue
			}
		}
		if _, err := s.files.WriteAgentFile(path, file.Contents); err != nil {
			return err
		}
		// What is on disk is now what the item carried, so the cache says so: a
		// file this machine wrote joins the capture that will read it back, and
		// the next arriving copy of it is an update rather than a hand edit.
		cache[path] = agentFileValue{Hash: file.Hash(), Size: int64(len(file.Contents))}
	}
	return s.recordAgentHashes(t, cache)
}

// agentFileUnchanged reports whether the bytes on disk are still the ones the
// cache last recorded for path, which is what makes an arriving copy safe to
// write over them. An entry the cache cannot vouch for is a hand edit.
func agentFileUnchanged(path string, current []byte, cache map[string]agentFileValue) bool {
	seen, held := cache[path]
	return held && AgentFile{Contents: current}.Hash() == seen.Hash
}

// readAgentHashes reads the capture's hash cache. A row that does not parse is
// an empty cache rather than an error: it is only ever a shortcut, and a
// missing one costs a read per file rather than a refusal.
func (s *Store) readAgentHashes() map[string]agentFileValue {
	raw, ok, err := s.db.KVGet(agentHashMarker)
	if err != nil || !ok {
		return map[string]agentFileValue{}
	}
	return decodeAgentHashes(raw)
}

// readAgentHashesIn is readAgentHashes inside a caller's transaction, so an
// apply reads the cache the capture last wrote rather than a second
// connection's view of it.
func (s *Store) readAgentHashesIn(t *db.Tx) map[string]agentFileValue {
	raw, ok, err := t.KVGet(agentHashMarker)
	if err != nil || !ok {
		return map[string]agentFileValue{}
	}
	return decodeAgentHashes(raw)
}

func decodeAgentHashes(raw []byte) map[string]agentFileValue {
	out := map[string]agentFileValue{}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]agentFileValue{}
	}
	return out
}

// recordAgentHashes stores the cache back, and only when it changed: an apply
// that wrote nothing must leave no trace for the next capture to read as a
// change.
func (s *Store) recordAgentHashes(t *db.Tx, cache map[string]agentFileValue) error {
	encoded, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	existing, ok, err := t.KVGet(agentHashMarker)
	if err != nil {
		return err
	}
	if ok && sameJSON(existing, encoded) {
		return nil
	}
	return t.KVPut(agentHashMarker, encoded)
}

// EditedAgentFile is one harness file on disk that no longer holds what the
// document carries, with the path a person would look at.
type EditedAgentFile struct {
	Path string
	Hash string
}

// CarriedAgentFiles is how many harness files this machine's agents carry and
// which of them a person has edited since, as one call so a caller cannot
// count one and read the other.
func (s *Store) CarriedAgentFiles() (int, []EditedAgentFile, error) {
	if s.files == nil {
		return 0, nil, nil
	}
	edited, err := s.DriftedAgentFiles()
	if err != nil {
		return 0, nil, err
	}
	return s.carriedAgentCount(), edited, nil
}

// carriedAgentCount is the number of kind-to-file pairs the stored agents
// items carry, which is what the drift report is a fraction of.
func (s *Store) carriedAgentCount() int {
	items, err := s.storedAgentItems()
	if err != nil {
		return 0
	}
	count := 0
	for _, raw := range items {
		doc, err := ParseAgentEntryDoc("", raw)
		if err != nil {
			continue
		}
		count += len(doc.Files)
	}
	return count
}

// DriftedAgentFiles reports the harness files on disk that no longer hold what
// the document carries, for every native agent. A file the document does not
// carry is not drift: it is a definition nobody has synced.
func (s *Store) DriftedAgentFiles() ([]EditedAgentFile, error) {
	if s.files == nil {
		return nil, nil
	}
	items, err := s.storedAgentItems()
	if err != nil {
		return nil, err
	}
	var out []EditedAgentFile
	for _, name := range sortedItemNames(items) {
		doc, err := ParseAgentEntryDoc(name, items[name])
		if err != nil || !doc.Native() {
			continue
		}
		for _, kind := range sortedAgentKinds(doc.Files) {
			path, err := s.files.AgentFilePath(kind, doc.Entry.Native[kind].Agent)
			if err != nil {
				continue
			}
			contents, ok, err := readAgentFileAt(path)
			if err != nil || !ok {
				continue
			}
			held := AgentFile{Kind: kind, Contents: contents}
			if held.Hash() != doc.Files[kind].Hash() {
				out = append(out, EditedAgentFile{Path: path, Hash: held.Hash()})
			}
		}
	}
	return out, nil
}

// readAgentFileAt reads one harness file, reporting ok false when it is absent
// or is not a regular file. A definition this machine cannot read is not one
// to report as edited.
func readAgentFileAt(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	data, err := readLimited(path, MaxAgentFileBytes)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func sameAgentHashes(a, b map[string]agentFileValue) bool {
	if len(a) != len(b) {
		return false
	}
	for key, want := range b {
		if got, ok := a[key]; !ok || got != want {
			return false
		}
	}
	return true
}

func sortedItemNames(items map[string]json.RawMessage) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
