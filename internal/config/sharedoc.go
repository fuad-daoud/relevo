package config

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// Config is the name the config document travels under, as `doc:config` on the
// log.
const Config = "config"

// supersededSourcePrefix labels the revision an arriving item lands when it
// would leave this machine's merged config invalid. It reads as what happened
// rather than as a transport detail: the entry was newer than what this machine
// holds and still lost, because taking it would have broken the config.
const supersededSourcePrefix = "sync-superseded:"

// SharedDoc is the config document as a synclog SharedDoc. One key is one
// candidate, agent, actor, account or server, or one whole-section body for the
// sections that have no items to speak of; the sync section is not among them,
// because a sync setting is this machine's answer to its own peer and travels
// with the connection rather than in the config.
type SharedDoc struct {
	store *Store
}

// NewSharedDoc binds the config document of one store.
func NewSharedDoc(s *Store) *SharedDoc { return &SharedDoc{store: s} }

// Name is the document's name, the word an entry's table carries after `doc:`.
func (d *SharedDoc) Name() string { return Config }

// Doc returns the config document of this store, for the sync runtime to
// register once it has one.
func (s *Store) Doc() *SharedDoc { return NewSharedDoc(s) }

// Items returns the shared document as one value per syncable key. It reads
// the shared layer and never the local one, because the local layer holds the
// machine fields the exporter must not send and has stripped out of the shared
// bodies already; merging it here would put this machine's directories back
// into every entry.
func (d *SharedDoc) Items() (map[string]json.RawMessage, error) {
	if err := d.store.localLayerReady(); err != nil {
		return nil, err
	}
	shared, err := d.store.Current()
	if err != nil {
		return nil, err
	}

	return itemsOf(shared, false)
}

// itemsOf is one value per syncable key of a shared document. A section that
// does not split into named items is an error, or with lenient is left out: a
// body written before its items had names has no keys to stamp.
func itemsOf(shared Doc, lenient bool) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for _, sec := range Sections {
		if !syncs(sec) {
			continue
		}
		body, ok := shared[sec]
		if !ok {
			continue
		}
		items, err := splits(sec, body)
		if err != nil {
			if lenient {
				continue
			}
			return nil, err
		}
		for name, item := range items {
			out[docKeyOf(sec, name)] = item
		}
	}
	return out, nil
}

// ItemUpdatedAt is when the section a key lives in was last written, which is
// as fine as the document records: an item of a per-item section carries its
// section's time.
func (d *SharedDoc) ItemUpdatedAt(key string) (time.Time, bool, error) {
	sec, _, err := splitKey(key)
	if err != nil {
		return time.Time{}, false, err
	}
	return d.store.db.ConfigUpdatedAt(string(sec))
}

// ItemWriteStamp is the stamp recorded when key was last written on this
// machine. An item that arrived from a peer records none.
func (d *SharedDoc) ItemWriteStamp(key string) (synclog.Stamp, bool, error) {
	text, ok, err := d.store.db.ConfigWriteStamp(key)
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}

// ItemWriteStampIn is ItemWriteStamp read inside the importer's transaction.
func (d *SharedDoc) ItemWriteStampIn(t *db.Tx, key string) (synclog.Stamp, bool, error) {
	text, ok, err := t.ConfigWriteStamp(key)
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}

// syncs reports whether a section is part of the shared document at all. The
// sync section is not: its settings say how this machine talks to its peer,
// which is this machine's business and would be overwritten by whichever peer
// answered last.
func syncs(sec Section) bool { return sec != Sync }

// Apply writes one key's value, or removes the key when value is nil. It only
// ever writes the shared layer, and drops the fields naming things on this
// machine from the arriving value, so an arriving item can neither redirect an
// account's directory or this host's serve policy nor carry a peer's into the
// shared body this machine re-exports.
//
// A value that would leave the merged config invalid is refused and recorded as
// superseded: the entry is newer than what this machine holds, so the choice is
// between taking a config that does not resolve and keeping one that does.
func (d *SharedDoc) Apply(key string, value json.RawMessage) error {
	sec, name, err := splitKey(key)
	if err != nil {
		return err
	}
	if !syncs(sec) {
		return fmt.Errorf("config: the %s section never syncs: %w", sec, ErrInvalidValue)
	}
	return d.store.applyKey(sec, name, value)
}

// applyKey writes one key's value in one transaction: the prospective shared
// document is merged with the local layer and validated as the reader would
// see it, so a write the reader would refuse is refused here rather than
// leaving Load broken behind it.
//
// A refusal is still a transaction that commits, because the record of why the
// entry lost has to outlive the call that reported it. The refusal is therefore
// carried out of the transaction and returned once it has committed, rather
// than returned from inside it where it would take the record with it.
func (s *Store) applyKey(sec Section, name string, value json.RawMessage) error {
	var refusal error
	err := s.db.Tx(func(t *db.Tx) error {
		var err error
		if refusal, err = s.applyKeyIn(t, sec, name, value); refusal == nil || err != nil {
			return err
		}
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		key := docKeyOf(sec, name)
		return s.recordSuperseded(t, before, key, value, key, refusal)
	})
	if err != nil {
		return err
	}
	return refusal
}

// applyKeyIn is applyKey inside a caller's transaction, and records nothing for
// a refusal: the caller decides whether the item is lost for good or may be
// retried once the keys it depends on have landed. The second result is a
// failure of the store itself.
func (s *Store) applyKeyIn(t *db.Tx, sec Section, name string, value json.RawMessage) (refusal error, hard error) {
	unstamped := *s
	unstamped.arrived = true
	s = &unstamped
	before, err := readSnapshot(t)
	if err != nil {
		return nil, err
	}
	body, err := setDocKey(before.doc, sec, name, dropMachineFields(sec, value))
	if err != nil {
		return invalidValueOf(err), nil
	}

	prospective := copyDoc(before.doc)
	if body == nil {
		delete(prospective, sec)
	} else {
		prospective[sec] = body
	}
	merged, err := mergedDoc(t, prospective)
	if err != nil {
		return invalidValueOf(err), nil
	}
	if err := validateProspective(merged); err != nil {
		return err, nil
	}
	if body == nil {
		return nil, s.deleteRecord(t, before, sec)
	}
	return nil, s.writeRecord(t, before, sec, body)
}

// docKeyOf is the doc key of one item, or of a whole section.
func docKeyOf(sec Section, name string) string {
	if perItemSections[sec] {
		return itemKey(sec, name)
	}
	return string(sec)
}

// setDocKey returns the shared body of sec with one key set or removed, or nil
// when the whole section is gone.
func setDocKey(doc Doc, sec Section, name string, value json.RawMessage) (json.RawMessage, error) {
	if !perItemSections[sec] {
		return value, nil
	}
	items := map[string]json.RawMessage{}
	if len(doc[sec]) > 0 {
		var err error
		if items, err = splits(sec, doc[sec]); err != nil {
			return nil, err
		}
	}
	if value == nil {
		delete(items, name)
	} else {
		items[name] = value
	}
	return joins(sec, items)
}

// writeSection stores one shared section body and bumps the config version a
// reader's reload watches. It is the shared write path: Put, PutDoc and an
// applied item all store through it and then record through record, so a
// change that arrived from a peer takes effect exactly as one made here by
// `relevo config set` does, with no restart.
func (s *Store) writeSection(t *db.Tx, sec Section, body json.RawMessage) error {
	return t.ConfigPut(string(sec), body, s.now().UTC())
}

// deleteSection removes one shared section, bumping the version with it.
func (s *Store) deleteSection(t *db.Tx, sec Section) error {
	return t.ConfigDelete(string(sec))
}

// writeRecord stores a section and records the change in one transaction, which
// is what the callers that change exactly one section use.
func (s *Store) writeRecord(t *db.Tx, before snapshot, sec Section, body json.RawMessage) error {
	if err := s.writeSection(t, sec, body); err != nil {
		return err
	}
	return s.record(t, before, nil)
}

// deleteRecord removes a section and records the removal.
func (s *Store) deleteRecord(t *db.Tx, before snapshot, sec Section) error {
	if err := s.deleteSection(t, sec); err != nil {
		return err
	}
	return s.record(t, before, nil)
}

// mergedDoc is the shared document with every section's local overrides laid
// over it, which is the document every reader decodes and every writer
// validates. It is the only view of the config a caller ever sees.
//
// The read runs in one transaction so the shared rows and the overrides read
// back together are the same snapshot: a write landing between them would
// otherwise show a merged body no single moment ever held.
func (s *Store) mergedDoc() (Doc, error) {
	var out Doc
	err := s.db.Tx(func(t *db.Tx) error {
		shared, err := readDoc(t)
		if err != nil {
			return err
		}
		out, err = mergedDoc(t, shared)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// recordSuperseded leaves the config exactly as it is and records why an
// arriving item did not land. The revision points at the unchanged document, so
// rolling back to it is a no-op rather than a second change.
func (s *Store) recordSuperseded(t *db.Tx, before snapshot, key string, value json.RawMessage, label string, cause error) error {
	changes, err := json.Marshal([]Change{{Path: key, Op: "add", After: compactCopy(value)}})
	if err != nil {
		return err
	}
	snapshot, err := EncodeDoc(before.doc)
	if err != nil {
		return err
	}
	_, err = t.RevisionInsert(db.RevisionRow{
		At:       s.now(),
		Source:   supersededSourcePrefix + label,
		Message:  cause.Error(),
		Version:  before.version,
		Changes:  changes,
		Snapshot: snapshot,
	})
	return err
}
