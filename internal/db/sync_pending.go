package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// fkParent is one reference a shared row carries into another shared table: the
// child column holding the key and the parent table the value names.
type fkParent struct {
	column string
	table  string
}

// pendingParents is the drain's closure over the parents its window names: every
// row a drained row references whose own outbox entries all lie past the window,
// followed recursively, each returned with its snapshot state.
//
// Only a shared parent is followed, and only one whose key is a single column
// this machine can name: the reference gives one parent key value, and a parent
// keyed by several columns cannot be placed from it.
func (t *Tx) pendingParents(window []DrainedEntry, tail int) ([]DrainedEntry, error) {
	seen := make(map[string]bool, len(window))
	queue := make([]DrainedEntry, 0, len(window))
	for _, d := range window {
		if d.Row == nil {
			continue
		}
		seen[d.Table+"\x00"+d.PK] = true
		queue = append(queue, d)
	}
	fks := make(map[string][]fkParent)
	var pulled []DrainedEntry
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		refs, err := t.parentRefs(fks, d.Table)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			value, ok := exchangeValue(*d.Row, ref.column)
			if !ok || value == nil {
				continue
			}
			pk, err := keyText(value)
			if err != nil {
				return nil, err
			}
			key := ref.table + "\x00" + pk
			if seen[key] {
				continue
			}
			entry, err := t.pullParent(ref.table, pk, tail)
			if err != nil {
				return nil, err
			}
			if entry == nil {
				continue
			}
			seen[key] = true
			pulled = append(pulled, *entry)
			queue = append(queue, *entry)
		}
	}
	return pulled, nil
}

// parentRefs returns tbl's references into shared tables, reading and caching
// them per table so one drain reads each table's schema once.
func (t *Tx) parentRefs(cache map[string][]fkParent, tbl string) ([]fkParent, error) {
	refs, ok := cache[tbl]
	if ok {
		return refs, nil
	}
	refs, err := t.foreignParents(tbl)
	if err != nil {
		return nil, err
	}
	cache[tbl] = refs
	return refs, nil
}

// pullParent returns the parent a reference names, with its snapshot state, when
// that parent has outbox entries past the window; it returns nil when the parent
// is not pending, is not here, or the row that referenced it carries no such
// key. The parent's owner is resolved so the exporter can still tell whose row
// it is.
func (t *Tx) pullParent(parent, pk string, tail int) (*DrainedEntry, error) {
	pending, err := t.outboxPast(parent, pk, tail)
	if err != nil {
		return nil, err
	}
	if !pending {
		return nil, nil
	}
	row, found, err := t.ReadExchangeRow(parent, pk)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	owner, resolved, err := t.ResolveOwner(parent, pk)
	if err != nil {
		return nil, err
	}
	entry := DrainedEntry{Table: parent, PK: pk, Row: &row}
	if resolved {
		entry.Origin = sql.NullString{String: owner, Valid: true}
	}
	return &entry, nil
}

// foreignParents is the references tbl carries into shared tables, as the schema
// declares them. A parent keyed by more than one column is left out: the
// reference names one value, which cannot place such a row.
func (t *Tx) foreignParents(tbl string) ([]fkParent, error) {
	rows, err := t.conn.QueryContext(t.ctx,
		`SELECT "table", "from", "to" FROM pragma_foreign_key_list(?) ORDER BY id, seq`, tbl)
	if err != nil {
		return nil, fmt.Errorf("db: foreign keys of %s: %w", tbl, mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	var out []fkParent
	for rows.Next() {
		var parent, column, key string
		if err := rows.Scan(&parent, &column, &key); err != nil {
			return nil, fmt.Errorf("db: foreign keys of %s: %w", tbl, mapBusy(err))
		}
		shared := sharedTable(parent)
		if shared == nil || len(shared.PrimaryKey) != 1 || shared.PrimaryKey[0] != key {
			continue
		}
		out = append(out, fkParent{column: column, table: parent})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: foreign keys of %s: %w", tbl, mapBusy(err))
	}
	return out, nil
}

// outboxPast reports whether one row has outbox entries past seq, which is what
// makes its state something a later batch has still to carry.
func (t *Tx) outboxPast(tbl, pk string, seq int) (bool, error) {
	var one int
	err := t.conn.QueryRowContext(t.ctx,
		`SELECT 1 FROM sync_outbox WHERE tbl = ? AND pk = ? AND seq > ? LIMIT 1`, tbl, pk, seq).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: pending parent %s %s: %w", tbl, pk, mapBusy(err))
	}
	return true, nil
}

// exchangeValue returns one column's value out of a row read, and whether the
// row carries that column.
func exchangeValue(row ExchangeRow, name string) (any, bool) {
	for _, col := range row.Columns {
		if col.Name == name {
			return col.Value, true
		}
	}
	return nil, false
}

// keyText renders one key value as the json_array text the outbox and the log
// use to name a row, so a parent pulled by a reference carries the same key
// spelling as every other entry.
func keyText(value any) (string, error) {
	raw, err := json.Marshal([]any{value})
	if err != nil {
		return "", fmt.Errorf("db: key value %v: %w", value, ErrInvalid)
	}
	return string(raw), nil
}
