package db

import (
	"context"
	"fmt"
	"time"
)

// RecordList returns one row per live record of owner's, name-ordered.
func (d *DB) RecordList(owner string) ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE `+writerScope+` AND owner = ? AND archived_at IS NULL ORDER BY name ASC`,
		d.origin, owner)
	if err != nil {
		return nil, fmt.Errorf("db: record list: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanRecord)
	if err != nil {
		return nil, fmt.Errorf("db: record list: %w", mapBusy(err))
	}
	return out, nil
}

// RecordListForMasterMind is RecordList narrowed, in the query, to the records
// whose binding names mastermindID, so a caller polling for its own bindings
// does not read every record. '$.planner_id' is the JSON key store.Binding
// writes MasterMindID under.
func (d *DB) RecordListForMasterMind(owner, mastermindID string) ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE `+writerScope+` AND owner = ? AND archived_at IS NULL
			AND json_extract(record_json, '$.planner_id') = ? ORDER BY name ASC`,
		d.origin, owner, mastermindID)
	if err != nil {
		return nil, fmt.Errorf("db: record list for mastermind: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanRecord)
	if err != nil {
		return nil, fmt.Errorf("db: record list for mastermind: %w", mapBusy(err))
	}
	return out, nil
}

// RecordListWithCheckRun is RecordList narrowed, in the query, to the records
// whose binding carries a check run, so a sweep that advances checks reads no
// other binding. '$.check_run' is the JSON key store.Binding writes CheckRun
// under.
func (d *DB) RecordListWithCheckRun(owner string) ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE `+writerScope+` AND owner = ? AND archived_at IS NULL
			AND json_extract(record_json, '$.check_run') IS NOT NULL ORDER BY name ASC`,
		d.origin, owner)
	if err != nil {
		return nil, fmt.Errorf("db: record list with check run: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanRecord)
	if err != nil {
		return nil, fmt.Errorf("db: record list with check run: %w", mapBusy(err))
	}
	return out, nil
}

// RecordListArchived returns every archived row, oldest archived_at first and by
// name within one stamp, so archive readers see what RecordGet and RecordList
// hide.
func (d *DB) RecordListArchived(owner string) ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE `+writerScope+` AND owner = ? AND archived_at IS NOT NULL
			ORDER BY archived_at ASC, name ASC`, d.origin, owner)
	if err != nil {
		return nil, fmt.Errorf("db: record list archived: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanRecord)
	if err != nil {
		return nil, fmt.Errorf("db: record list archived: %w", mapBusy(err))
	}
	return out, nil
}

// ArchivedRef is one archived row's identity for the mirror's cursor: the row
// without its JSON, so a tick that only looks for new archives reads no
// binding documents.
type ArchivedRef struct {
	ID         string
	Name       string
	ArchivedAt time.Time
}

// RecordListArchivedSince returns the identity of every archived row stamped
// at or after since, oldest first and by name within one stamp. The stamp
// comparison is on the stored text, which orders chronologically. A since
// equal to a row's stamp still lists the row: rows sharing a stamp are told
// apart by the caller's seen set, not by the query.
func (d *DB) RecordListArchivedSince(owner string, since time.Time) ([]ArchivedRef, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT id, name, archived_at FROM binding_record WHERE `+writerScope+` AND owner = ? AND archived_at IS NOT NULL AND archived_at >= ?
			ORDER BY archived_at ASC, name ASC`, d.origin, owner, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("db: record list archived since: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanArchivedRef)
	if err != nil {
		return nil, fmt.Errorf("db: record list archived since: %w", mapBusy(err))
	}
	return out, nil
}

func scanArchivedRef(s rowScanner) (ArchivedRef, error) {
	var r ArchivedRef
	var at string
	if err := s.Scan(&r.ID, &r.Name, &at); err != nil {
		return ArchivedRef{}, err
	}
	t, err := parseTime(at)
	if err != nil {
		return ArchivedRef{}, fmt.Errorf("parse archived_at: %w", err)
	}
	r.ArchivedAt = t
	return r, nil
}

// RecordListUnlinked returns every remote binding row that carries no link at
// all -- both link columns NULL. Name-ordered.
//
// It is a read and never a repair: a row stays unlinked until it is bound again
// on purpose. The link columns are read as NULL rather than compared to a
// sentinel, so a half-written link is still listed rather than treated as bound.
func (d *DB) RecordListUnlinked() ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE link_origin IS NULL AND link_id IS NULL
			ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("db: record list unlinked: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanRecord)
	if err != nil {
		return nil, fmt.Errorf("db: record list unlinked: %w", err)
	}
	return out, nil
}
