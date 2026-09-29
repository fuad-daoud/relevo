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
