package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Kinds a sync_binding_left row carries: the binding left this machine by the
// owner's hand-over, or was taken by another machine's takeover.
const (
	LeftHandover = "handover"
	LeftTakeover = "takeover"
)

// BindingLeft is one root this installation no longer writes, and where it went.
type BindingLeft struct {
	RootID   string
	Owner    string
	Name     string
	ToWriter string
	Kind     string
	At       time.Time
}

// Roots are the two root ids of one binding: the record and its history mirror.
// BindingID is empty when no mirror row exists.
type Roots struct {
	RecordID  string
	BindingID string
}

// RowRef names one shared row.
type RowRef struct {
	Table string
	PK    string
}

// LiveRoots finds the record and mirror ids of owner's live binding name that
// this installation writes. found is false when this installation does not
// write one.
func (t *Tx) LiveRoots(owner, name string) (Roots, bool, error) {
	var r Roots
	err := t.queryRow(`SELECT id FROM binding_record WHERE `+writerScope+` AND owner = ? AND name = ? AND archived_at IS NULL`,
		t.origin, owner, name).Scan(&r.RecordID)
	if errors.Is(err, sql.ErrNoRows) {
		return Roots{}, false, nil
	}
	if err != nil {
		return Roots{}, false, fmt.Errorf("db: live roots %s/%q: %w", owner, name, mapBusy(err))
	}
	err = t.queryRow(`SELECT id FROM binding WHERE `+writerScope+` AND name = ? AND archived_at IS NULL ORDER BY created_at DESC LIMIT 1`,
		t.origin, name).Scan(&r.BindingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Roots{}, false, fmt.Errorf("db: live roots %s/%q: mirror: %w", owner, name, mapBusy(err))
	}
	return r, true, nil
}

// HandOver makes target the writer of owner's live binding name: both roots'
// writer column flips in this transaction, so the outbox attributes the flip to
// this installation (the update triggers read OLD.writer) and the exporter
// carries it. Each root is recorded in sync_binding_left, and the keys of the
// binding's children are kept so this machine's reconcile never emits a delete
// for rows it no longer writes.
func (t *Tx) HandOver(owner, name, target string, at time.Time) (Roots, error) {
	roots, found, err := t.LiveRoots(owner, name)
	if err != nil {
		return Roots{}, err
	}
	if !found {
		return Roots{}, fmt.Errorf("db: hand over %s/%q: this installation writes no such binding: %w", owner, name, ErrInvalid)
	}
	if err := t.leaveRoots(roots, owner, name, target, LeftHandover, at); err != nil {
		return Roots{}, err
	}
	return roots, nil
}

// leaveRoots flips both roots to writer, records them as left and keeps the
// children's keys. The manifest is read before the flip because a later read of
// the children resolves through a root this installation no longer writes.
func (t *Tx) leaveRoots(roots Roots, owner, name, writer, kind string, at time.Time) error {
	if err := t.recordLeftRows(roots); err != nil {
		return err
	}
	if err := t.SetRootWriter("binding_record", roots.RecordID, writer); err != nil {
		return err
	}
	if err := t.RecordBindingLeft(BindingLeft{RootID: roots.RecordID, Owner: owner, Name: name, ToWriter: writer, Kind: kind, At: at}); err != nil {
		return err
	}
	if roots.BindingID == "" {
		return nil
	}
	if err := t.SetRootWriter("binding", roots.BindingID, writer); err != nil {
		return err
	}
	return t.RecordBindingLeft(BindingLeft{RootID: roots.BindingID, Owner: owner, Name: name, ToWriter: writer, Kind: kind, At: at})
}

// SetRootWriter writes the writer column of one root row. A missing row is not
// an error: a takeover can be applied before the mirror reached this machine.
func (t *Tx) SetRootWriter(table, id, writer string) error {
	if table != "binding_record" && table != "binding" {
		return fmt.Errorf("db: set root writer: %q is not a root: %w", table, ErrInvalid)
	}
	if _, err := t.exec(`UPDATE `+table+` SET writer = ? WHERE id = ?`, writer, id); err != nil {
		return fmt.Errorf("db: set %s writer %s: %w", table, id, mapBusy(err))
	}
	return nil
}

// RecordBindingLeft upserts one sync_binding_left row.
func (t *Tx) RecordBindingLeft(l BindingLeft) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO sync_binding_left (root_id, owner, name, to_writer, kind, at) VALUES (?,?,?,?,?,?)`,
		l.RootID, l.Owner, l.Name, l.ToWriter, l.Kind, formatTime(l.At)); err != nil {
		return fmt.Errorf("db: record binding left %s: %w", l.RootID, mapBusy(err))
	}
	return nil
}

// ClearBindingLeft forgets that this installation left a root: the binding came
// back to it by a hand-over or a takeover.
func (t *Tx) ClearBindingLeft(rootID string) error {
	if _, err := t.exec(`DELETE FROM sync_binding_left WHERE root_id = ?`, rootID); err != nil {
		if isMissingTable(err) {
			return nil
		}
		return fmt.Errorf("db: clear binding left %s: %w", rootID, mapBusy(err))
	}
	return nil
}

// BindingLeftByName returns the newest left row of owner's binding name.
func (d *DB) BindingLeftByName(owner, name string) (BindingLeft, bool, error) {
	return bindingLeft(context.Background(), d.sqlDB, `WHERE owner = ? AND name = ?`, owner, name)
}

// BindingLeftByRoot returns the left row of one root.
func (d *DB) BindingLeftByRoot(rootID string) (BindingLeft, bool, error) {
	return bindingLeft(context.Background(), d.sqlDB, `WHERE root_id = ?`, rootID)
}

func bindingLeft(ctx context.Context, q queryer, where string, args ...any) (BindingLeft, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT root_id, owner, name, to_writer, kind, at FROM sync_binding_left `+where+` ORDER BY at DESC LIMIT 1`, args...)
	if err != nil {
		if isMissingTable(err) {
			return BindingLeft{}, false, nil
		}
		return BindingLeft{}, false, fmt.Errorf("db: binding left: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanBindingLeft)
	if err != nil || len(out) == 0 {
		return BindingLeft{}, false, err
	}
	return out[0], true, nil
}

// BindingsLeft lists every root this installation left, newest first.
func (d *DB) BindingsLeft() ([]BindingLeft, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT root_id, owner, name, to_writer, kind, at FROM sync_binding_left WHERE root_id IN (SELECT id FROM binding_record) ORDER BY at DESC, root_id`)
	if err != nil {
		if isMissingTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("db: bindings left: %w", mapBusy(err))
	}
	return collectRows(rows, scanBindingLeft)
}

func scanBindingLeft(s rowScanner) (BindingLeft, error) {
	var l BindingLeft
	var at string
	if err := s.Scan(&l.RootID, &l.Owner, &l.Name, &l.ToWriter, &l.Kind, &at); err != nil {
		return BindingLeft{}, err
	}
	var err error
	if l.At, err = parseTime(at); err != nil {
		return BindingLeft{}, fmt.Errorf("parse at: %w", err)
	}
	return l, nil
}

// OutboxPendingFor counts the outbox rows still queued for one root's own rows,
// which is how status tells a hand-over that was exported from one that was not.
func (d *DB) OutboxPendingFor(rootID string) (int, error) {
	var n int
	err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE tbl IN ('binding_record','binding') AND pk = ?`,
		`["`+rootID+`"]`).Scan(&n)
	if err != nil {
		if isMissingTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("db: outbox pending %s: %w", rootID, mapBusy(err))
	}
	return n, nil
}

// RaiseTerm records that writer's entries on a root are valid only through upto.
// It never lowers an existing fence: a later takeover that read a smaller mark
// would otherwise reopen entries an earlier one refused.
func (t *Tx) RaiseTerm(rootID, writer string, upto int) error {
	if _, err := t.exec(`INSERT INTO sync_binding_term (root_id, writer, upto) VALUES (?, ?, ?)
		ON CONFLICT(root_id, writer) DO UPDATE SET upto = MAX(upto, excluded.upto)`, rootID, writer, upto); err != nil {
		return fmt.Errorf("db: raise term %s %s: %w", rootID, writer, mapBusy(err))
	}
	return nil
}

// SetRegrant records on an existing term the entry that gave writer the root
// back. Entries of writer above the term's upto stay fenced unless they follow
// the regrant.
func (t *Tx) SetRegrant(rootID, writer, origin string, seq int) error {
	if _, err := t.exec(`UPDATE sync_binding_term SET regrant_origin = ?, regrant_seq = ? WHERE root_id = ? AND writer = ?`,
		origin, seq, rootID, writer); err != nil {
		return fmt.Errorf("db: set regrant %s %s: %w", rootID, writer, mapBusy(err))
	}
	return nil
}

// TakeOver makes this installation the writer of both roots, which the update
// triggers attribute to the old writer, so the flip itself is not exported.
// Each root's old-writer term is raised to upto and this installation's own
// term is dropped.
func (t *Tx) TakeOver(roots Roots, oldWriter string, upto int) error {
	for _, r := range []struct{ table, id string }{{"binding_record", roots.RecordID}, {"binding", roots.BindingID}} {
		if r.id == "" {
			continue
		}
		if err := t.SetRootWriter(r.table, r.id, t.origin); err != nil {
			return err
		}
		if err := t.RaiseTerm(r.id, oldWriter, upto); err != nil {
			return err
		}
		if err := t.DeleteTerm(r.id, t.origin); err != nil {
			return err
		}
		if err := t.ClearBindingLeft(r.id); err != nil {
			return err
		}
	}
	return nil
}

// RecordLeaveTakeover applies a takeover's effect on a root row this machine
// holds: the writer flips to taker, the old writer's term is raised, and when
// this installation was the old writer the root is recorded as left. It returns
// whether the root row exists here.
func (t *Tx) RecordLeaveTakeover(table, id, taker, oldWriter string, upto int, at time.Time) (bool, error) {
	var owner, name, writer string
	var err error
	switch table {
	case "binding_record":
		err = t.queryRow(`SELECT owner, name, writer FROM binding_record WHERE id = ?`, id).Scan(&owner, &name, &writer)
	default:
		err = t.queryRow(`SELECT '', name, writer FROM binding WHERE id = ?`, id).Scan(&owner, &name, &writer)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, t.RaiseTerm(id, oldWriter, upto)
	}
	if err != nil {
		return false, fmt.Errorf("db: takeover root %s: %w", id, mapBusy(err))
	}
	if err := t.SetRootWriter(table, id, taker); err != nil {
		return false, err
	}
	if err := t.RaiseTerm(id, oldWriter, upto); err != nil {
		return false, err
	}
	if oldWriter == t.origin {
		return true, t.RecordBindingLeft(BindingLeft{RootID: id, Owner: owner, Name: name, ToWriter: taker, Kind: LeftTakeover, At: at})
	}
	return true, nil
}

const leftRowsKey = "sync.left_rows.v1"

// recordLeftRows keeps the keys of every child of a root this installation is
// about to stop writing.
func (t *Tx) recordLeftRows(r Roots) error {
	refs, err := t.childRefs(r)
	if err != nil {
		return err
	}
	have, err := loadLeftRows(t.ctx, t.conn)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		have[ref] = true
	}
	list := make([]RowRef, 0, len(have))
	for ref := range have {
		list = append(list, ref)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return t.KVPut(leftRowsKey, raw)
}

// childRefs lists the keys of the shared rows that hang off the two roots.
func (t *Tx) childRefs(r Roots) ([]RowRef, error) {
	queries := []struct{ table, query, id string }{
		{"binding_event", `SELECT json_array(record_id, seq) FROM binding_event WHERE record_id = ?`, r.RecordID},
		{"round_file", `SELECT json_array(record_id, name) FROM round_file WHERE record_id = ?`, r.RecordID},
	}
	if r.BindingID != "" {
		queries = append(queries,
			struct{ table, query, id string }{"round", `SELECT json_array(id) FROM round WHERE binding_id = ?`, r.BindingID},
			struct{ table, query, id string }{"event", `SELECT json_array(id) FROM event WHERE binding_id = ?`, r.BindingID},
			struct{ table, query, id string }{"artifact", `SELECT json_array(id) FROM artifact WHERE round_id IN (SELECT id FROM round WHERE binding_id = ?)`, r.BindingID},
			struct{ table, query, id string }{"transcript", `SELECT json_array(id) FROM transcript WHERE owner_kind = 'round' AND owner_id IN (SELECT id FROM round WHERE binding_id = ?)`, r.BindingID},
		)
	}
	out := []RowRef{{Table: "binding_record", PK: `["` + r.RecordID + `"]`}}
	if r.BindingID != "" {
		out = append(out, RowRef{Table: "binding", PK: `["` + r.BindingID + `"]`})
	}
	for _, q := range queries {
		rows, err := t.conn.QueryContext(t.ctx, q.query, q.id)
		if err != nil {
			return nil, fmt.Errorf("db: child keys %s: %w", q.table, mapBusy(err))
		}
		for rows.Next() {
			var pk string
			if err := rows.Scan(&pk); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, RowRef{Table: q.table, PK: pk})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// LeftRows is the set of rows this installation stopped writing, for the
// reconcile walk to skip.
func (d *DB) LeftRows() (map[RowRef]bool, error) {
	return loadLeftRows(context.Background(), d.sqlDB)
}

func loadLeftRows(ctx context.Context, q queryer) (map[RowRef]bool, error) {
	raw, ok, err := kvGet(ctx, q, leftRowsKey)
	if err != nil {
		return nil, err
	}
	out := map[RowRef]bool{}
	if !ok {
		return out, nil
	}
	var list []RowRef
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("db: left rows: %w", err)
	}
	for _, ref := range list {
		out[ref] = true
	}
	return out, nil
}

// ForeignRoots finds the record and mirror ids of owner's live binding name
// whoever writes them, with the record's creator and writer. found is false when
// no such record exists in this file.
func (t *Tx) ForeignRoots(owner, name string) (r Roots, creator, writer string, found bool, err error) {
	return foreignRoots(t.ctx, t.conn, owner, name)
}

// ForeignRoots is the read-only form of Tx.ForeignRoots.
func (d *DB) ForeignRoots(owner, name string) (Roots, string, string, bool, error) {
	return foreignRoots(context.Background(), d.sqlDB, owner, name)
}

func foreignRoots(ctx context.Context, q queryer, owner, name string) (r Roots, creator, writer string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT id, origin, writer FROM binding_record WHERE owner = ? AND name = ? AND archived_at IS NULL ORDER BY created_at DESC LIMIT 1`,
		owner, name).Scan(&r.RecordID, &creator, &writer)
	if errors.Is(err, sql.ErrNoRows) {
		return Roots{}, "", "", false, nil
	}
	if err != nil {
		return Roots{}, "", "", false, fmt.Errorf("db: foreign roots %s/%q: %w", owner, name, mapBusy(err))
	}
	err = q.QueryRowContext(ctx, `SELECT id FROM binding WHERE name = ? AND writer = ? AND archived_at IS NULL ORDER BY created_at DESC LIMIT 1`,
		name, writer).Scan(&r.BindingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Roots{}, "", "", false, fmt.Errorf("db: foreign roots %s/%q: mirror: %w", owner, name, mapBusy(err))
	}
	return r, creator, writer, true, nil
}

// RepointMirror points this installation's live mirror of name at repoID.
func (t *Tx) RepointMirror(name, repoID string) error {
	if _, err := t.exec(`UPDATE binding SET repo_id = ? WHERE `+writerScope+` AND name = ? AND archived_at IS NULL`,
		repoID, t.origin, name); err != nil {
		return fmt.Errorf("db: repoint mirror %q: %w", name, mapBusy(err))
	}
	return nil
}

// Fenced is one binding whose local changes a takeover fenced.
type Fenced struct {
	Name  string
	Taker string
	Count int
}

// FencedSummary counts, per binding, the local changes the exporter moved to
// sync_fenced_change because another machine took the binding over.
func (d *DB) FencedSummary() ([]Fenced, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT l.name, l.to_writer, COUNT(*) FROM sync_fenced_change f
		 JOIN sync_binding_left l ON l.root_id = f.root_id
		 GROUP BY l.name, l.to_writer ORDER BY l.name`)
	if err != nil {
		if isMissingTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("db: fenced summary: %w", mapBusy(err))
	}
	return collectRows(rows, func(s rowScanner) (Fenced, error) {
		var f Fenced
		return f, s.Scan(&f.Name, &f.Taker, &f.Count)
	})
}
