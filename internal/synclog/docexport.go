package synclog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// DocStamper is the optional half of a SharedDoc that knows when a key was last
// written. A first export stamps from it rather than from the clock: a machine
// that joins with an old config must not beat an edit another machine made
// since, which a stamp of "now" would do.
type DocStamper interface {
	// ItemUpdatedAt is when key was last written; ok is false when the doc
	// cannot say.
	ItemUpdatedAt(key string) (at time.Time, ok bool, err error)
}

// DocWriteStamper is the optional half of a SharedDoc that stamps an item when
// it is written, so the export carries the time of the write and not the time
// of the export.
type DocWriteStamper interface {
	// ItemWriteStamp is the stamp recorded when key was last written here; ok is
	// false when no write on this machine stamped it.
	ItemWriteStamp(key string) (stamp Stamp, ok bool, err error)
}

// ItemDigest is the hash an item is compared by. The value is compacted first
// so spacing never makes an unchanged item look changed.
func ItemDigest(value json.RawMessage) (string, error) {
	var buf []byte
	if value != nil {
		compact, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("synclog: item digest: %w: %w", err, ErrInvalid)
		}
		buf = compact
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// exportDocs turns every signalled doc into entries on the log.
//
// A locked machine emits nothing and keeps every signal: a doc entry must be
// signed, and an unsigned one would be refused by every unlocked reader. A doc
// nobody registered keeps its signal too, so it is exported once something can
// read it.
func (e *Exporter) exportDocs() (int, error) {
	if len(e.key) == 0 {
		return 0, nil
	}
	signals, err := e.db.DocSignals()
	if err != nil {
		return 0, fmt.Errorf("synclog: export docs: %w", err)
	}
	var appended int
	for _, sig := range signals {
		doc, known := e.docNamed(sig.Doc)
		if !known {
			continue
		}
		n, err := e.exportDoc(doc, sig)
		appended += n
		if err != nil {
			return appended, fmt.Errorf("synclog: export doc %s: %w", sig.Doc, err)
		}
	}
	return appended, nil
}

// exportDoc diffs one doc against what was last exported and appends one entry
// per changed key. The append comes before the state write, so a crash between
// them re-exports the same values under fresh stamps, which a reader applies
// as the same state.
func (e *Exporter) exportDoc(doc SharedDoc, sig db.DocSignal) (int, error) {
	items, err := doc.Items()
	if err != nil {
		return 0, err
	}
	state, err := e.db.DocExportState(sig.Doc)
	if err != nil {
		return 0, err
	}
	entries, writes, err := e.docEntries(doc, items, state)
	if err != nil {
		return 0, err
	}
	if len(entries) > 0 {
		if err := e.guard(e.transport, e.origin); err != nil {
			return 0, err
		}
		e.sign(entries)
		if _, err := e.appendTo(e.transport, e.origin, entries); err != nil {
			return 0, fmt.Errorf("append: %w", err)
		}
	}
	if err := e.db.RecordDocExport(sig, writes); err != nil {
		return len(entries), err
	}
	return len(entries), nil
}

// docEntries builds the entries for every key whose digest differs from the
// export state, and for every exported key the doc no longer holds.
func (e *Exporter) docEntries(doc SharedDoc, items map[string]json.RawMessage, state map[string]db.DocExportRow) ([]Entry, []db.DocExportWrite, error) {
	clock := NewHybridClock(e.db.LocalOrSelf())
	var entries []Entry
	var writes []db.DocExportWrite
	for _, key := range sortedKeys(items) {
		digest, err := ItemDigest(items[key])
		if err != nil {
			return nil, nil, err
		}
		prev, had := state[key]
		if had && prev.Digest == digest {
			continue
		}
		stamp, err := e.docStamp(clock, doc, key, prev, had)
		if err != nil {
			return nil, nil, err
		}
		entry, err := NewDocUpsert(e.origin, doc, key, e.schema, stamp, items[key], e.now())
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, entry)
		writes = append(writes, db.DocExportWrite{Key: key, Origin: e.origin, Stamp: stamp.String(), Digest: digest})
	}
	for _, key := range sortedKeys(state) {
		if _, held := items[key]; held {
			continue
		}
		stamp, err := e.docStamp(clock, doc, key, state[key], true)
		if err != nil {
			return nil, nil, err
		}
		entry, err := NewDocDelete(e.origin, doc, key, e.schema, stamp, e.now())
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, entry)
		writes = append(writes, db.DocExportWrite{Key: key, Origin: e.origin, Stamp: stamp.String(), Tombstone: true})
	}
	return entries, writes, nil
}

// docStamp is the stamp for a key about to be exported. The stamp recorded when
// the key was written wins when it is newer than the one the key last carried.
// Otherwise a key never exported takes its stamp from when the doc says it was
// written, and every later one comes from the clock, moved past the stamp the
// key last carried so a change always outranks the state it replaces.
func (e *Exporter) docStamp(clock *HybridClock, doc SharedDoc, key string, prev db.DocExportRow, had bool) (Stamp, error) {
	var last Stamp
	if had {
		var err error
		if last, err = ParseStamp(prev.Stamp); err != nil {
			return 0, err
		}
	}
	if w, ok := doc.(DocWriteStamper); ok {
		wrote, found, err := w.ItemWriteStamp(key)
		if err != nil {
			return 0, err
		}
		if found && (!had || wrote > last) {
			return wrote, nil
		}
	}
	if !had {
		return e.firstStamp(clock, doc, key)
	}
	if err := clock.Observe(last); err != nil {
		return 0, err
	}
	return clock.Now()
}

func (e *Exporter) firstStamp(clock *HybridClock, doc SharedDoc, key string) (Stamp, error) {
	if stamper, ok := doc.(DocStamper); ok {
		at, known, err := stamper.ItemUpdatedAt(key)
		if err != nil {
			return 0, err
		}
		if known {
			return stampAt(at, 0), nil
		}
	}
	return clock.Now()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
