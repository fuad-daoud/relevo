package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrNoRevision reports a lookup naming a revision that is not stored.
var ErrNoRevision = errors.New("no such revision")

// ErrNoChange reports a rollback whose target equals the stored config.
var ErrNoChange = errors.New("config already equals that revision")

// As returns a copy of s labelled with source and message; the receiver is
// unchanged, so a runtime's base store stays unlabelled.
func (s *Store) As(source, message string) *Store {
	c := *s
	c.source = source
	c.message = message
	return &c
}

// WithClock returns a copy of s whose writes and revisions read the time from
// now.
func (s *Store) WithClock(now func() time.Time) *Store {
	c := *s
	c.now = now
	return &c
}

// readDoc reads every stored section in Sections order; secrets are never part
// of it.
func readDoc(t *db.Tx) (Doc, error) {
	doc := Doc{}
	for _, sec := range Sections {
		body, ok, err := t.ConfigGet(string(sec))
		if err != nil {
			return nil, err
		}
		if ok {
			doc[sec] = body
		}
	}
	return doc, nil
}

// snapshot is a document with the config version it was read at.
type snapshot struct {
	doc     Doc
	version int64
}

func readSnapshot(t *db.Tx) (snapshot, error) {
	doc, err := readDoc(t)
	if err != nil {
		return snapshot{}, err
	}
	version, err := t.ConfigVersion()
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{doc: doc, version: version}, nil
}

// record appends one revision for a write that changed something; as the last
// statement of every write transaction, it commits with the change or not.
func (s *Store) record(t *db.Tx, before snapshot, extra []Change) error {
	after, err := readDoc(t)
	if err != nil {
		return err
	}

	changes := append(DiffDocs(before.doc, after), extra...)
	if len(changes) == 0 {
		return nil
	}

	version, err := t.ConfigVersion()
	if err != nil {
		return err
	}

	// The first revision on a machine that already has config is preceded by a
	// baseline carrying the version before the write, so it can be rolled back.
	if len(before.doc) > 0 {
		exists, err := t.RevisionsExist()
		if err != nil {
			return err
		}
		if !exists {
			snapshot, err := EncodeDoc(before.doc)
			if err != nil {
				return err
			}
			if _, err := t.RevisionInsert(db.RevisionRow{
				At:       s.now(),
				Source:   "baseline",
				Message:  "config before revisions",
				Version:  before.version,
				Changes:  []byte("[]"),
				Snapshot: snapshot,
			}); err != nil {
				return err
			}
		}
	}

	source := s.source
	if source == "" {
		source = "unknown"
	}
	changesJSON, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	snapshot, err := EncodeDoc(after)
	if err != nil {
		return err
	}
	if _, err := t.RevisionInsert(db.RevisionRow{
		At:       s.now(),
		Source:   source,
		Message:  s.message,
		Version:  version,
		Changes:  changesJSON,
		Snapshot: snapshot,
	}); err != nil {
		return err
	}
	return nil
}

func (s *Store) Log(limit int) ([]db.RevisionRow, error) { return s.db.Revisions(limit) }

// Revision returns one revision with its snapshot, or ErrNoRevision.
func (s *Store) Revision(rev int64) (db.RevisionRow, error) {
	r, ok, err := s.db.Revision(rev)
	if err != nil {
		return db.RevisionRow{}, err
	}
	if !ok {
		return db.RevisionRow{}, fmt.Errorf("%w: #%d", ErrNoRevision, rev)
	}
	return r, nil
}

// RevisionDoc returns the document revision rev wrote, or ErrNoRevision.
func (s *Store) RevisionDoc(rev int64) (Doc, error) {
	row, err := s.Revision(rev)
	if err != nil {
		return nil, err
	}
	return decodeSnapshot(row.Snapshot)
}

// RollbackPlan returns the changes rolling back to rev would make, or
// ErrNoChange when there are none, so the CLI can answer without a transaction.
func (s *Store) RollbackPlan(rev int64) ([]Change, error) {
	target, err := s.Revision(rev)
	if err != nil {
		return nil, err
	}
	snapshot, err := decodeSnapshot(target.Snapshot)
	if err != nil {
		return nil, err
	}
	current, err := s.currentDoc()
	if err != nil {
		return nil, err
	}
	changes := DiffDocs(current, snapshot)
	if len(changes) == 0 {
		return nil, ErrNoChange
	}
	return changes, nil
}

// Rollback makes the stored config equal to rev's snapshot as one new revision
// with source "rollback" and message "rollback to #<rev>", plus s.message when
// set. A snapshot that no longer validates writes nothing, and an
// already-matching config returns ErrNoChange.
func (s *Store) Rollback(rev int64) (db.RevisionRow, error) {
	target, err := s.Revision(rev)
	if err != nil {
		return db.RevisionRow{}, err
	}
	snapshot, err := decodeSnapshot(target.Snapshot)
	if err != nil {
		return db.RevisionRow{}, err
	}
	if err := validateSnapshot(rev, snapshot); err != nil {
		return db.RevisionRow{}, err
	}

	message := fmt.Sprintf("rollback to #%d", rev)
	if s.message != "" {
		message += "; " + s.message
	}
	s = s.As("rollback", message)

	noChange := false
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if len(DiffDocs(before.doc, snapshot)) == 0 {
			noChange = true
			return nil
		}
		return s.applySnapshot(t, before, snapshot)
	}); err != nil {
		return db.RevisionRow{}, err
	}
	if noChange {
		return db.RevisionRow{}, ErrNoChange
	}

	rows, err := s.db.Revisions(1)
	if err != nil {
		return db.RevisionRow{}, err
	}
	if len(rows) == 0 {
		return db.RevisionRow{}, errors.New("rollback recorded no revision")
	}
	return rows[0], nil
}

func validateSnapshot(rev int64, target Doc) error {
	for _, sec := range Sections {
		body, ok := target[sec]
		if !ok {
			continue
		}
		if _, err := Validate(sec, body); err != nil {
			return fmt.Errorf("rollback to #%d: %w", rev, err)
		}
	}
	return nil
}

func (s *Store) applySnapshot(t *db.Tx, before snapshot, target Doc) error {
	now := s.now().UTC()
	for _, sec := range Sections {
		body, ok := target[sec]
		if !ok {
			continue
		}
		if current, present := before.doc[sec]; present && len(diffRaw(string(sec), current, body)) == 0 {
			continue
		}
		if err := t.ConfigPut(string(sec), body, now); err != nil {
			return err
		}
	}
	for _, sec := range Sections {
		if _, ok := target[sec]; ok {
			continue
		}
		if _, present := before.doc[sec]; !present {
			continue
		}
		if err := t.ConfigDelete(string(sec)); err != nil {
			return err
		}
	}
	return s.record(t, before, nil)
}

func (s *Store) Current() (Doc, error) { return s.currentDoc() }

func (s *Store) currentDoc() (Doc, error) {
	doc := Doc{}
	for _, sec := range Sections {
		body, ok, err := s.db.ConfigGet(string(sec))
		if err != nil {
			return nil, err
		}
		if ok {
			doc[sec] = body
		}
	}
	return doc, nil
}

func decodeSnapshot(raw []byte) (Doc, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("revision snapshot: %w", err)
	}
	doc := Doc{}
	for name, body := range m {
		doc[Section(name)] = body
	}
	return doc, nil
}
