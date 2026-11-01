package synclog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// DocStamper is the optional SharedDoc half knowing when a key was last written.
// A first export stamps from it, not the clock: a joiner with an old config must
// not beat a newer peer edit.
type DocStamper interface {
	// ItemUpdatedAt is when key was last written; ok is false when the doc
	// cannot say.
	ItemUpdatedAt(key string) (at time.Time, ok bool, err error)
}

// DocWriteStamper is the optional SharedDoc half stamping an item when written,
// so the export carries the write's time, not the export's.
type DocWriteStamper interface {
	// ItemWriteStamp is the stamp recorded when key was last written here; ok is
	// false when no write on this machine stamped it.
	ItemWriteStamp(key string) (stamp Stamp, ok bool, err error)
}

// DocReSealStamper is the optional SharedDoc half for sealed values. A key
// rotation re-seals every value without an edit; a fresh stamp would outrank a
// peer's concurrent edit, so re-emissions carry the stamp the key already had.
type DocReSealStamper interface {
	// ResealKeepsStamp says a digest that changed with no newer write stamp is
	// a re-seal of what the key already carried.
	ResealKeepsStamp() bool
}

// ItemDigest is the hash an item is compared by, compacted first so spacing
// never marks an unchanged item changed.
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

// exportDocs turns every signalled doc into log entries. Locked emits nothing
// and keeps every signal, since unsigned entries are refused everywhere;
// unregistered keeps its signal until something can read it.
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

// exportDoc diffs one doc and appends one entry per changed key, before the state
// write, so a crash between them re-exports values a reader applies as the same
// state.
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
		written, err := e.appendTo(e.transport, e.origin, entries)
		if err != nil {
			return 0, fmt.Errorf("append: %w", err)
		}
		noteOwnHead(e.db.LocalOrSelf(), written)
	}
	if err := e.db.RecordDocExport(sig, writes); err != nil {
		return len(entries), err
	}
	return len(entries), nil
}

func (e *Exporter) docEntries(doc SharedDoc, items map[string]json.RawMessage, state map[string]db.DocExportRow) ([]Entry, []db.DocExportWrite, error) {
	clock := NewHybridClock(e.db.LocalOrSelf())
	var entries []Entry
	var writes []db.DocExportWrite
	for _, key := range slices.Sorted(maps.Keys(items)) {
		digest, err := ItemDigest(items[key])
		if err != nil {
			return nil, nil, err
		}
		prev, had := state[key]
		if had && prev.Digest == digest {
			continue
		}
		stamp, err := e.docStamp(clock, doc, key, prev, had, false)
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
	for _, key := range slices.Sorted(maps.Keys(state)) {
		if _, held := items[key]; held {
			continue
		}
		stamp, err := e.docStamp(clock, doc, key, state[key], true, true)
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

// docStamp is the stamp for a key about to be exported. The write stamp wins
// when newer than the last carried; a re-sealing doc otherwise keeps the stamp
// it had, since a re-seal is the same value under new bytes. A first export
// stamps from the doc, every later one from the clock past the last carried.
func (e *Exporter) docStamp(clock *HybridClock, doc SharedDoc, key string, prev db.DocExportRow, had, tombstone bool) (Stamp, error) {
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
	if !tombstone {
		if keeper, ok := doc.(DocReSealStamper); ok && keeper.ResealKeepsStamp() && had {
			return last, nil
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
