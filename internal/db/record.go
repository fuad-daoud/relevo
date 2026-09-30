package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Record is one binding's authoritative row in binding_record: the whole
// Binding as JSON, plus the columns internal/store reads and sorts on.
type Record struct {
	ID        string
	Owner     string
	Name      string
	State     string
	Round     int
	CWD       string
	JSON      string
	CreatedAt time.Time
	UpdatedAt time.Time
	// ViewedAt replaces the old <binding dir>/.viewed file.
	ViewedAt *time.Time
	// ArchivedAt is set by RecordArchive, which hides the row from reads.
	ArchivedAt *time.Time

	// LinkOrigin and LinkID are the other copy of a remote binding: the
	// installation that holds it and that installation's record id. Empty
	// means the row is not linked -- every row written before migration 015,
	// and every binding created by or against an older relevo.
	LinkOrigin string
	LinkID     string
}

// RecordEvent is one entry of a binding_record's log. JSON is authoritative,
// so a key a newer relevo wrote survives a read and a confirm.
type RecordEvent struct {
	Seq         int
	TS          time.Time
	Round       int
	Direction   string
	Kind        string
	Confirmed   bool
	DeliveredAt *time.Time
	Route       string
	JSON        string
}

const recordCols = `id, owner, name, state, round, cwd, record_json, created_at, updated_at, viewed_at, archived_at, link_origin, link_id`

// originScope is the WHERE fragment every scoped record query carries, with
// the handle's own origin as its one placeholder: a row is in scope when this
// installation wrote it, or when its origin is empty -- a row that existed
// before the origin column did. Only one installation opens a machine database
// today, so an empty-origin row is this installation's own. A sync must refuse
// to run while any empty-origin row remains.
const originScope = `origin IN (?, '')`

// nullIfEmpty stores an optional TEXT column: an empty string becomes NULL.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scanRecord(s rowScanner) (Record, error) {
	var r Record
	var createdAt, updatedAt string
	var viewedAt, archivedAt sql.Null[string]
	var linkOrigin, linkID sql.Null[string]

	if err := s.Scan(&r.ID, &r.Owner, &r.Name, &r.State, &r.Round, &r.CWD, &r.JSON,
		&createdAt, &updatedAt, &viewedAt, &archivedAt, &linkOrigin, &linkID); err != nil {
		return Record{}, err
	}

	var err error
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return Record{}, fmt.Errorf("parse created_at: %w", err)
	}
	if r.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Record{}, fmt.Errorf("parse updated_at: %w", err)
	}
	if r.ViewedAt, err = nullTimeFrom(viewedAt); err != nil {
		return Record{}, fmt.Errorf("parse viewed_at: %w", err)
	}
	if r.ArchivedAt, err = nullTimeFrom(archivedAt); err != nil {
		return Record{}, fmt.Errorf("parse archived_at: %w", err)
	}
	r.LinkOrigin = linkOrigin.V
	r.LinkID = linkID.V

	return r, nil
}

// RecordGet returns owner's live row for name within this handle's origin, and
// whether it was found. The live row is keyed by (origin, owner, name): one
// machine database holds every owner's bindings, and a shared one would hold
// every installation's.
func (d *DB) RecordGet(owner, name string) (Record, bool, error) {
	return getRecord(context.Background(), d.sqlDB, d.origin, owner, name)
}

func getRecord(ctx context.Context, q queryer, origin, owner, name string) (Record, bool, error) {
	r, err := scanRecord(q.QueryRowContext(ctx,
		`SELECT `+recordCols+` FROM binding_record WHERE `+originScope+` AND owner = ? AND name = ? AND archived_at IS NULL`,
		origin, owner, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("db: record get %s/%q: %w", owner, name, mapBusy(err))
	}
	return r, true, nil
}

func (d *DB) RecordList(owner string) ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE `+originScope+` AND owner = ? AND archived_at IS NULL ORDER BY name ASC`,
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

// RecordListArchived returns every archived row, oldest archived_at first and
// by name within one stamp, so the archive readers can see a row RecordGet and
// RecordList hide.
func (d *DB) RecordListArchived(owner string) ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE `+originScope+` AND owner = ? AND archived_at IS NOT NULL
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

// RecordGetByID returns the row with this id, live or archived, so an archive
// source can read a record when it holds the id rather than the name.
func (d *DB) RecordGetByID(id string) (Record, bool, error) {
	r, err := scanRecord(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("db: record get by id %q: %w", id, mapBusy(err))
	}
	return r, true, nil
}

func (d *DB) RecordCounts() (live, archived int, err error) {
	var total, liveCount int
	if err := d.sqlDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN archived_at IS NULL THEN 1 ELSE 0 END), 0)
			FROM binding_record`).Scan(&total, &liveCount); err != nil {
		return 0, 0, fmt.Errorf("db: record counts: %w", mapBusy(err))
	}
	return liveCount, total - liveCount, nil
}

// RecordGetArchivedByName returns the most recently archived row for name. Two
// archives of one name can share a millisecond archived_at and a ULID is
// random within a millisecond, so the tie-break is rowid: insertion order.
func (d *DB) RecordGetArchivedByName(owner, name string) (Record, bool, error) {
	r, err := scanRecord(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record
			WHERE `+originScope+` AND owner = ? AND name = ? AND archived_at IS NOT NULL
			ORDER BY archived_at DESC, rowid DESC LIMIT 1`, d.origin, owner, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("db: record get archived %s/%q: %w", owner, name, mapBusy(err))
	}
	return r, true, nil
}

// RecordPut inserts or updates the live row for r.Owner and r.Name. A hit
// keeps the row's id, viewed_at and created_at -- the binding's own creation
// stamp, not this write's -- and takes r's other columns.
func (t *Tx) RecordPut(r Record) (string, error) {
	updatedAt := r.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	var id string
	err := t.queryRow(`SELECT id FROM binding_record WHERE `+originScope+` AND owner = ? AND name = ? AND archived_at IS NULL`,
		t.origin, r.Owner, r.Name).Scan(&id)
	switch {
	case err == nil:
		// The update stamps origin too, so a row this handle touches is scoped
		// to this installation from then on.
		if _, uerr := t.exec(`UPDATE binding_record SET origin = ?, owner = ?, state = ?, round = ?, cwd = ?, record_json = ?, updated_at = ?, link_origin = ?, link_id = ? WHERE id = ?`,
			t.origin, r.Owner, r.State, r.Round, r.CWD, r.JSON, formatTime(updatedAt),
			nullIfEmpty(r.LinkOrigin), nullIfEmpty(r.LinkID), id); uerr != nil {
			return "", fmt.Errorf("db: record put %q: update: %w", r.Name, mapBusy(uerr))
		}
		return id, nil
	case errors.Is(err, sql.ErrNoRows):
		return t.insertRecord(r, updatedAt)
	default:
		return "", fmt.Errorf("db: record put %q: select: %w", r.Name, mapBusy(err))
	}
}

func (t *Tx) insertRecord(r Record, updatedAt time.Time) (string, error) {
	id := r.ID
	if id == "" {
		id = NewID()
	}
	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = updatedAt
	}
	if _, err := t.exec(`INSERT INTO binding_record
			(id, owner, name, state, round, cwd, record_json, created_at, updated_at, viewed_at, archived_at, origin, link_origin, link_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,NULL,?,?,?)`,
		id, r.Owner, r.Name, r.State, r.Round, r.CWD, r.JSON,
		formatTime(createdAt), formatTime(updatedAt), nullableTime(r.ViewedAt), t.origin,
		nullIfEmpty(r.LinkOrigin), nullIfEmpty(r.LinkID)); err != nil {
		return "", fmt.Errorf("db: record put %q: insert: %w", r.Name, mapBusy(err))
	}
	return id, nil
}

// RecordArchive takes owner's live row for name out of RecordGet and
// RecordList. No live row is a no-op.
func (t *Tx) RecordArchive(owner, name string, at time.Time) error {
	if _, err := t.exec(`UPDATE binding_record SET archived_at = ? WHERE `+originScope+` AND owner = ? AND name = ? AND archived_at IS NULL`,
		formatTime(at), t.origin, owner, name); err != nil {
		return fmt.Errorf("db: record archive %s/%q: %w", owner, name, mapBusy(err))
	}
	return nil
}

// RecordDelete removes owner's live row for name; its events go with it
// through the foreign key's ON DELETE CASCADE. Archived rows of the same name
// are kept: they are history a later binding must not destroy.
func (t *Tx) RecordDelete(owner, name string) error {
	if _, err := t.exec(`DELETE FROM binding_record WHERE `+originScope+` AND owner = ? AND name = ? AND archived_at IS NULL`,
		t.origin, owner, name); err != nil {
		return fmt.Errorf("db: record delete %s/%q: %w", owner, name, mapBusy(err))
	}
	return nil
}

// RecordSetViewed stamps viewed_at on owner's live row for name. No row is a
// no-op: a read verb must not fail because a stamp could not be written.
func (t *Tx) RecordSetViewed(owner, name string, at time.Time) error {
	if _, err := t.exec(`UPDATE binding_record SET viewed_at = ? WHERE `+originScope+` AND owner = ? AND name = ? AND archived_at IS NULL`,
		formatTime(at), t.origin, owner, name); err != nil {
		return fmt.Errorf("db: record set viewed %s/%q: %w", owner, name, mapBusy(err))
	}
	return nil
}

// EventsOf returns recordID's events whose Seq is greater than afterSeq,
// ascending; afterSeq 0 reads the whole log.
func (d *DB) EventsOf(recordID string, afterSeq int) ([]RecordEvent, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT seq, ts, round, direction, kind, confirmed, delivered_at, route, entry_json
			FROM binding_event WHERE record_id = ? AND seq > ? ORDER BY seq ASC`,
		recordID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("db: events of %s: %w", recordID, mapBusy(err))
	}
	out, err := collectRows(rows, scanRecordEvent)
	if err != nil {
		return nil, fmt.Errorf("db: events of %s: %w", recordID, mapBusy(err))
	}
	return out, nil
}

func scanRecordEvent(s rowScanner) (RecordEvent, error) {
	var e RecordEvent
	var ts string
	var confirmed int64
	var deliveredAt, route sql.Null[string]

	if err := s.Scan(&e.Seq, &ts, &e.Round, &e.Direction, &e.Kind, &confirmed,
		&deliveredAt, &route, &e.JSON); err != nil {
		return RecordEvent{}, err
	}

	var err error
	if e.TS, err = parseTime(ts); err != nil {
		return RecordEvent{}, fmt.Errorf("parse ts: %w", err)
	}
	e.Confirmed = confirmed != 0
	if e.DeliveredAt, err = nullTimeFrom(deliveredAt); err != nil {
		return RecordEvent{}, fmt.Errorf("parse delivered_at: %w", err)
	}
	e.Route = route.V

	return e, nil
}

// EventAppend inserts one event; (record_id, seq) is the primary key, so an
// existing seq fails rather than duplicating a line.
func (t *Tx) EventAppend(recordID string, e RecordEvent) error {
	if _, err := t.exec(`INSERT INTO binding_event
			(record_id, seq, ts, round, direction, kind, confirmed, delivered_at, route, entry_json)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		recordID, e.Seq, formatTime(e.TS), e.Round, e.Direction, e.Kind,
		boolToInt(e.Confirmed), nullableTime(e.DeliveredAt), nullIfEmpty(e.Route), e.JSON); err != nil {
		return fmt.Errorf("db: event append %s#%d: %w", recordID, e.Seq, mapBusy(err))
	}
	return nil
}

// EventReplaceAll replaces recordID's entire log with evs in one transaction,
// so a failed import leaves no half of a previous log behind.
func (t *Tx) EventReplaceAll(recordID string, evs []RecordEvent) error {
	if _, err := t.exec(`DELETE FROM binding_event WHERE record_id = ?`, recordID); err != nil {
		return fmt.Errorf("db: event replace %s: delete: %w", recordID, mapBusy(err))
	}
	for _, e := range evs {
		if err := t.EventAppend(recordID, e); err != nil {
			return err
		}
	}
	return nil
}

// EventConfirm marks recordID's seq'th event delivered now, by route when that
// is non-empty, and replaces the entry's JSON with the caller's patched map, so
// unknown keys survive.
func (t *Tx) EventConfirm(recordID string, seq int, at time.Time, route, newJSON string) error {
	if _, err := t.exec(`UPDATE binding_event SET confirmed = 1, delivered_at = ?, route = ?, entry_json = ? WHERE record_id = ? AND seq = ?`,
		formatTime(at), nullIfEmpty(route), newJSON, recordID, seq); err != nil {
		return fmt.Errorf("db: event confirm %s#%d: %w", recordID, seq, mapBusy(err))
	}
	return nil
}

// EventAdmit replaces recordID's seq'th event's JSON with the caller's patched
// map. The admit lives only in the entry's JSON, which is authoritative, so the
// confirmed, delivered_at and route columns are untouched: a later EventConfirm
// still writes them.
func (t *Tx) EventAdmit(recordID string, seq int, newJSON string) error {
	if _, err := t.exec(`UPDATE binding_event SET entry_json = ? WHERE record_id = ? AND seq = ?`,
		newJSON, recordID, seq); err != nil {
		return fmt.Errorf("db: event admit %s#%d: %w", recordID, seq, mapBusy(err))
	}
	return nil
}

func (t *Tx) EventMaxSeq(recordID string) (int, error) {
	n, err := eventMaxSeq(t.queryRow(`SELECT MAX(seq) FROM binding_event WHERE record_id = ?`, recordID))
	if err != nil {
		return 0, fmt.Errorf("db: event max seq %s: %w", recordID, mapBusy(err))
	}
	return n, nil
}

func (d *DB) EventMaxSeq(recordID string) (int, error) {
	n, err := eventMaxSeq(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT MAX(seq) FROM binding_event WHERE record_id = ?`, recordID))
	if err != nil {
		return 0, fmt.Errorf("db: event max seq %s: %w", recordID, mapBusy(err))
	}
	return n, nil
}

func eventMaxSeq(row *sql.Row) (int, error) {
	var n sql.Null[int64]
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	if !n.Valid {
		return 0, nil
	}
	return int(n.V), nil
}

// The *DB forms below wrap one Tx each.

func (d *DB) RecordPut(r Record) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.RecordPut(r)
		return err
	})
	return id, err
}

func (d *DB) RecordArchive(owner, name string, at time.Time) error {
	return d.Tx(func(t *Tx) error { return t.RecordArchive(owner, name, at) })
}

func (d *DB) RecordDelete(owner, name string) error {
	return d.Tx(func(t *Tx) error { return t.RecordDelete(owner, name) })
}

func (d *DB) RecordSetViewed(owner, name string, at time.Time) error {
	return d.Tx(func(t *Tx) error { return t.RecordSetViewed(owner, name, at) })
}

func (d *DB) EventAppend(recordID string, e RecordEvent) error {
	return d.Tx(func(t *Tx) error { return t.EventAppend(recordID, e) })
}

func (d *DB) EventReplaceAll(recordID string, evs []RecordEvent) error {
	return d.Tx(func(t *Tx) error { return t.EventReplaceAll(recordID, evs) })
}

func (d *DB) EventConfirm(recordID string, seq int, at time.Time, route, newJSON string) error {
	return d.Tx(func(t *Tx) error { return t.EventConfirm(recordID, seq, at, route, newJSON) })
}

func (d *DB) EventAdmit(recordID string, seq int, newJSON string) error {
	return d.Tx(func(t *Tx) error { return t.EventAdmit(recordID, seq, newJSON) })
}
