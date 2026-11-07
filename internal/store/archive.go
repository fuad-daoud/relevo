package store

import (
	"fmt"
	"time"
)

// ArchivedBinding is one archived record: the binding it was, the record id
// its events and sealed round files hang off, and when it was archived.
type ArchivedBinding struct {
	Binding    Binding
	RecordID   string
	ArchivedAt time.Time
}

// SealAll seals every round present among name's NNN-* files, ignoring
// Sealable: the caller has already stopped the builder.
func (t *Tx) SealAll(name string) (int, error) {
	rounds, err := t.s.RoundsOnDisk(name)
	if err != nil {
		return 0, err
	}

	total := 0
	for _, r := range rounds {
		n, err := t.SealRound(name, r)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// ArchivedRef is one archived record's identity for the mirror's cursor: the
// record without its binding, so a tick that only looks for new archives
// decodes nothing.
type ArchivedRef struct {
	RecordID   string
	Name       string
	ArchivedAt time.Time
}

// ListArchivedSince returns the identity of every record archived at or after
// since, oldest archived_at first. Rows sharing a stamp are all listed: the
// caller tells a new row from an already mirrored one.
func (s *Store) ListArchivedSince(since time.Time) ([]ArchivedRef, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	recs, err := d.RecordListArchivedSince(s.owner, since)
	if err != nil {
		return nil, fmt.Errorf("read archived records since: %w", err)
	}

	out := make([]ArchivedRef, 0, len(recs))
	for _, rec := range recs {
		out = append(out, ArchivedRef{RecordID: rec.ID, Name: rec.Name, ArchivedAt: rec.ArchivedAt})
	}
	return out, nil
}

// ListArchived returns every archived record, oldest archived_at first.
func (s *Store) ListArchived() ([]ArchivedBinding, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	recs, err := d.RecordListArchived(s.owner)
	if err != nil {
		return nil, fmt.Errorf("read archived records: %w", err)
	}

	out := make([]ArchivedBinding, 0, len(recs))
	for _, rec := range recs {
		b, err := decodeBinding([]byte(rec.JSON), rec.Name)
		if err != nil {
			return nil, err
		}
		var at time.Time
		if rec.ArchivedAt != nil {
			at = *rec.ArchivedAt
		}
		out = append(out, ArchivedBinding{Binding: b, RecordID: rec.ID, ArchivedAt: at})
	}
	return out, nil
}

// HasArchived reports whether name was used by an archived chain or binding:
// a live name is free, so a create must ask the record layer rather than
// reaching into the database around the store.
func (s *Store) HasArchived(name string) (bool, error) {
	d, err := s.dbForRead()
	if err != nil {
		return false, err
	}
	if d == nil {
		return false, nil
	}
	_, ok, err := d.RecordGetArchivedByName(s.owner, name)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// RecordID returns name's live record id, the id a caller names a binding by
// when it holds only the binding's name -- a served binding's view reports it,
// and the link columns point at it. A binding that does not exist is
// ErrNotFound.
func (s *Store) RecordID(name string) (string, error) {
	d, err := s.dbForRead()
	if err != nil {
		return "", err
	}
	if d == nil {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return "", fmt.Errorf("read binding %q: %w", name, err)
	}
	if !ok {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return rec.ID, nil
}

// ArchivedLog returns the events of the archived record recordID.
func (s *Store) ArchivedLog(recordID string) ([]LogEntry, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	events, err := d.EventsOf(recordID, 0)
	if err != nil {
		return nil, fmt.Errorf("read archived log %s: %w", recordID, err)
	}
	return logEntriesOf(events)
}

func (s *Store) ArchivedAtOf(recordID string) (time.Time, bool) {
	d, err := s.dbForRead()
	if err != nil || d == nil {
		return time.Time{}, false
	}
	rec, ok, err := d.RecordGetByID(recordID)
	if err != nil || !ok || rec.ArchivedAt == nil {
		return time.Time{}, false
	}
	return *rec.ArchivedAt, true
}

// ArchivedRecord returns recordID decoded into a Binding, and whether it was found.
func (s *Store) ArchivedRecord(recordID string) (Binding, bool, error) {
	d, err := s.dbForRead()
	if err != nil {
		return Binding{}, false, err
	}
	if d == nil {
		return Binding{}, false, nil
	}
	rec, ok, err := d.RecordGetByID(recordID)
	if err != nil || !ok {
		return Binding{}, false, err
	}
	b, err := decodeBinding([]byte(rec.JSON), rec.Name)
	if err != nil {
		return Binding{}, false, err
	}
	return b, true, nil
}

// ArchivedFile returns the sealed bytes of one of recordID's round files.
func (s *Store) ArchivedFile(recordID, name string) ([]byte, bool, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, false, err
	}
	if d == nil {
		return nil, false, nil
	}
	body, _, ok, err := d.RoundFileGet(recordID, name)
	if err != nil {
		return nil, false, err
	}
	return body, ok, nil
}

// ArchivedFiles returns the basenames of recordID's sealed round files.
func (s *Store) ArchivedFiles(recordID string) ([]string, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	return d.RoundFileList(recordID)
}
