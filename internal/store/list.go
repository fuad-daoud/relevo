package store

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

func (s *Store) list() ([]Binding, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	recs, err := d.RecordList(s.owner)
	if err != nil {
		return nil, fmt.Errorf("read state root: %w", err)
	}
	bindings, err := decodeRecords(recs)
	if err != nil {
		return nil, err
	}
	return s.mergeLiveList(bindings, recs)
}

// ListForMasterMind is List narrowed to the bindings of one MasterMind, read
// with the filter in the query rather than by listing everything.
func (s *Store) ListForMasterMind(mastermindID string) ([]Binding, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	recs, err := d.RecordListForMasterMind(s.owner, mastermindID)
	if err != nil {
		return nil, fmt.Errorf("read state root: %w", err)
	}
	bindings, err := decodeRecords(recs)
	if err != nil {
		return nil, err
	}
	return s.mergeLiveList(bindings, recs)
}

// ListWithCheckRun is List narrowed to the bindings carrying a check run,
// read with the filter in the query rather than by listing everything.
func (s *Store) ListWithCheckRun() ([]Binding, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	recs, err := d.RecordListWithCheckRun(s.owner)
	if err != nil {
		return nil, fmt.Errorf("read state root: %w", err)
	}
	bindings, err := decodeRecords(recs)
	if err != nil {
		return nil, err
	}
	return s.mergeLiveList(bindings, recs)
}

func decodeRecords(recs []db.Record) ([]Binding, error) {
	bindings := make([]Binding, 0, len(recs))
	for _, rec := range recs {
		b, err := decodeBinding([]byte(rec.JSON), rec.Name)
		if err != nil {
			return nil, err
		}
		// The stamp rides the row already read, so no reader pays a second
		// RecordGet for a fact List already holds.
		b.ViewedAt = rec.ViewedAt
		bindings = append(bindings, b)
	}

	return bindings, nil
}
