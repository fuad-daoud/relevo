package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The write half of the exchange seam: how a row another installation sent
// lands in this machine's file. Both writes name their columns from the schema
// and their values from bound parameters, so nothing a body carries can become
// SQL, and both run on a *Tx so a batch's rows and the mark that covers them
// commit together.

// ExchangeUpsert writes one row's values onto its primary key, inserting the row
// when it is absent and updating it in place when it is present.
//
// The conflict clause is the whole point of the method. INSERT OR REPLACE would
// delete the row it collides with before writing the new one, and binding_event,
// round_file and chain_* hang off their parent with ON DELETE CASCADE, so
// replacing a parent would take its children with it and lose history this file
// already holds. DO UPDATE writes the columns in place and leaves every other
// row alone.
//
// Columns come from the table's own declaration, so a key in the body this
// build's schema does not carry is dropped rather than named. The primary key
// must be among them: a row whose key the body does not name cannot be placed,
// and guessing a key would write the values onto whatever row held it.
//
// A column the body omits is left out of the statement, so on insert it takes
// the column's declared default and on update it keeps what the file already
// holds. Naming it as NULL instead would erase a value this machine has and the
// other machine merely did not mention.
//
// An append-only table is the one exception, and it is the exception that makes
// the method's own contract true. A row of one of those tables is written once
// and never rewritten -- by this machine or by any other -- so an upsert of an
// id the file already holds is not an update at all: an identical body is a
// replay the mark's own recovery makes possible (a batch re-offered after a mark
// that did not survive a restore) and is a no-op, and a body differing in any
// column is a peer rewriting history, which is refused. Both are decided before
// the statement runs, so the replay records no outbox row: nothing was written.
// The live writers and the migration's OR IGNORE path are untouched; they never
// come through here.
//
// owner is the installation the row belongs to, and it is a parameter rather
// than something read out of values: a body may name the owner column, but what
// it says there is a claim from a machine this one does not control, and a claim
// is not a fact about this file. The caller resolves the owner from this
// machine's own rows and passes it here, so a forged body cannot move a row
// between owners.
func (t *Tx) ExchangeUpsert(tbl, owner string, values map[string]any) error {
	shared := sharedTable(tbl)
	if shared == nil {
		return fmt.Errorf("db: exchange upsert: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	columns, err := exchangeColumns(t.ctx, t.conn, tbl)
	if err != nil {
		return err
	}
	if creator := creatorColumn(tbl); creator != "" {
		if err := t.checkCreatorImmutability(shared, creator, owner, values); err != nil {
			return err
		}
	}
	owned := ownedValues(tbl, owner, values)
	named, args, err := upsertColumns(shared.PrimaryKey, columns, owned)
	if err != nil {
		return fmt.Errorf("db: exchange upsert %s: %w", tbl, err)
	}
	if AppendOnly(shared.Name) {
		held, identical, err := t.appendOnlyHeld(shared, named, owned)
		if err != nil {
			return fmt.Errorf("db: exchange upsert %s: %w", tbl, err)
		}
		if held && !identical {
			return fmt.Errorf("db: exchange upsert %s: a row of %s is never rewritten: %w",
				tbl, shared.Name, ErrInvalid)
		}
		if held {
			return nil
		}
	}
	if _, err := t.exec(upsertStatement(shared.Name, named, shared.PrimaryKey), args...); err != nil {
		return fmt.Errorf("db: exchange upsert %s: %w", tbl, mapRefused(mapBusy(err)))
	}
	return nil
}

// checkCreatorImmutability validates that the creator column (origin) is immutable
// on update, and ensures it is populated on insert (from body or owner).
func (t *Tx) checkCreatorImmutability(shared *SharedTable, creator, owner string, values map[string]any) error {
	keyArgs := make([]any, len(shared.PrimaryKey))
	for i, col := range shared.PrimaryKey {
		v, vok := values[col]
		if !vok {
			return fmt.Errorf("db: exchange upsert %s: body names no %s: %w", shared.Name, col, ErrInvalid)
		}
		keyArgs[i] = v
	}
	where, _ := keyWhere(shared.PrimaryKey, keyArgs)
	var storedOrigin string
	err := t.conn.QueryRowContext(t.ctx, "SELECT "+quoteIdent(creator)+" FROM "+quoteIdent(shared.Name)+" WHERE "+where, keyArgs...).Scan(&storedOrigin)
	if err == nil {
		if bodyOrigin, _ := values[creator].(string); bodyOrigin != "" && storedOrigin != bodyOrigin {
			return fmt.Errorf("db: exchange upsert %s: %s is immutable (stored %q, body %q): %w",
				shared.Name, creator, storedOrigin, bodyOrigin, ErrInvalid)
		}
		values[creator] = storedOrigin
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		if bodyOrigin, _ := values[creator].(string); bodyOrigin != "" {
			values[creator] = bodyOrigin
		} else if owner != "" {
			values[creator] = owner
		} else {
			return fmt.Errorf("db: exchange upsert %s: %s is missing or empty: %w", shared.Name, creator, ErrInvalid)
		}
		return nil
	}
	return fmt.Errorf("db: exchange upsert %s: check stored %s: %w", shared.Name, creator, mapRefused(mapBusy(err)))
}

// MaxOutboxSeq returns the highest seq in sync_outbox, or 0 if empty.
func (t *Tx) MaxOutboxSeq() (int, error) {
	var seq sql.Null[int]
	if err := t.queryRow(`SELECT MAX(seq) FROM sync_outbox`).Scan(&seq); err != nil {
		if isMissingTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("db: max outbox seq: %w", mapBusy(err))
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.V, nil
}

// DiscardOutboxSince removes any outbox entries for (tbl, pk) with seq > sinceSeq
// that are attributed to this installation (origin IN (?, ”) or NULL).
// This prevents import statements from queueing echoes of local writes,
// whatever the op (insert, update, delete).
func (t *Tx) DiscardOutboxSince(tbl, pk string, sinceSeq int) error {
	query := `DELETE FROM sync_outbox WHERE seq > ? AND tbl = ? AND pk = ? AND (origin = ? OR origin = '' OR origin IS NULL)`
	if _, err := t.exec(query, sinceSeq, tbl, pk, t.origin); err != nil {
		if isMissingTable(err) {
			return nil
		}
		return fmt.Errorf("db: discard outbox %s %s since %d: %w", tbl, pk, sinceSeq, mapBusy(err))
	}
	return nil
}

// appendOnlyHeld reads the columns a body names out of the row the file already
// holds under the body's key, and reports whether it holds one at all and
// whether it is the row the body carries.
//
// The read is inside the transaction and the row is placed by the same one, so
// a row cannot appear between the question and the write. It names only the
// columns the body names, so a body that leaves a column out is compared on the
// columns it did name -- which is the same set the upsert would have written.
func (t *Tx) appendOnlyHeld(shared *SharedTable, named []string, values map[string]any) (held, identical bool, err error) {
	keyArgs := make([]any, len(shared.PrimaryKey))
	for i, col := range shared.PrimaryKey {
		value, ok := values[col]
		if !ok {
			return false, false, fmt.Errorf("body names no %s: %w", col, ErrInvalid)
		}
		keyArgs[i] = value
	}
	where, _ := keyWhere(shared.PrimaryKey, keyArgs)
	query := "SELECT " + joinQuoted(named) + " FROM " + quoteIdent(shared.Name) + " WHERE " + where
	rows, err := t.conn.QueryContext(t.ctx, query, keyArgs...)
	if err != nil {
		return false, false, mapRefused(mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, false, mapRefused(mapBusy(err))
		}
		return false, false, nil
	}
	have := make([]any, len(named))
	dest := make([]any, len(named))
	for i := range have {
		dest[i] = &have[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return false, false, fmt.Errorf("db: exchange upsert %s: read the held row: %w", shared.Name, err)
	}
	for i, col := range named {
		if !sameStoredValue(have[i], values[col]) {
			return true, false, nil
		}
	}
	return true, true, nil
}

// sameStoredValue reports whether the value a column holds is the value a body
// carries. A body is decoded from JSON, so a number arrives as an integer or a
// real while the column it lands in reads back as whichever of the two the
// storage class chose; the two are compared as numbers, not as types. Anything
// else -- text against a real, bytes against text, a null against a value --
// differs, which for an append-only row is the refusal the caller wants.
func sameStoredValue(have, incoming any) bool {
	if have == nil || incoming == nil {
		return have == nil && incoming == nil
	}
	if held, ok := have.([]byte); ok {
		incoming, ok := incoming.([]byte)
		return ok && bytes.Equal(held, incoming)
	}
	if held, ok := numericValue(have); ok {
		incoming, ok := numericValue(incoming)
		return ok && held == incoming
	}
	held, ok := have.(string)
	incoming, isText := incoming.(string)
	return ok && isText && held == incoming
}

// numericValue reads a value as the exact integer it is, when it is one. A real
// with a fraction, one beyond int64, and one that is not a number at all are
// none of them a column value this file could have stored, so they answer
// false rather than a number that would compare equal to something else.
func numericValue(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		if !wholeNumber(n) {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

// ownedValues is the values an upsert writes with the owner column set from the
// owner the caller resolved, whatever the body carried there. A table whose rows
// name their owner in a column of their own takes the caller's owner; a child
// resolves through its parent and has no owner column of its own, so its values
// pass through as they are. The map is copied rather than edited so the caller's
// body is not rewritten behind its back.
func ownedValues(tbl, owner string, values map[string]any) map[string]any {
	column := ownerColumn(tbl)
	if column == "" {
		return values
	}
	out := make(map[string]any, len(values)+1)
	for name, value := range values {
		out[name] = value
	}
	out[column] = owner
	return out
}

// ownerColumn is the column one shared table's rows carry their owner in, and
// empty for a table whose rows resolve through a parent and therefore have none
// of their own. Only the owner rules can answer it, which is why a writer asks
// this package for the column rather than naming it.
func ownerColumn(tbl string) string {
	rule, ok := OwnerRules[tbl]
	if !ok || len(rule.hops) != 0 || len(rule.kinds) != 0 {
		return ""
	}
	return rule.owner
}

// mapRefused turns a constraint the engine refused into ErrInvalid: a foreign
// key whose parent is not here, a unique index the row collides with. The values
// broke it from a machine this one does not control, so the constraint is the
// log's answer rather than this file's failure, and a caller can treat it as it
// treats any other body it cannot act on rather than as an outage of its own.
func mapRefused(err error) error {
	if err == nil {
		return nil
	}
	if code, _, ok := errCode(err); ok && code&0xff == sqliteConstraint {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	// A value rebuilt on the client from the wire can arrive without its code,
	// which is the same reason mapBusy also matches on the text.
	if strings.Contains(strings.ToLower(err.Error()), "constraint") {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return err
}

// ExchangeDelete removes one row by its primary key. A row that is already gone
// is not a failure: the delete is idempotent, which is what lets a batch be
// re-applied after a mark that did not survive a restore.
func (t *Tx) ExchangeDelete(tbl, pk string) error {
	shared := sharedTable(tbl)
	if shared == nil {
		return fmt.Errorf("db: exchange delete: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	keys, err := exchangeKeyValues(pk, len(shared.PrimaryKey))
	if err != nil {
		return err
	}
	where, args := keyWhere(shared.PrimaryKey, keys)
	query := "DELETE FROM " + quoteIdent(shared.Name) + " WHERE " + where
	if _, err := t.exec(query, args...); err != nil {
		return fmt.Errorf("db: exchange delete %s %s: %w", tbl, pk, mapRefused(mapBusy(err)))
	}
	return nil
}

// upsertColumns is the columns an upsert names and the values it binds for them,
// in the order the table declares them. The order is the schema's so two bodies
// naming the same values produce the same statement whatever order the map
// hands them over in.
func upsertColumns(keys, columns []string, values map[string]any) ([]string, []any, error) {
	named := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	present := make(map[string]bool, len(columns))
	for _, col := range columns {
		value, ok := values[col]
		if !ok {
			continue
		}
		named = append(named, col)
		args = append(args, value)
		present[col] = true
	}
	if len(named) == 0 {
		return nil, nil, fmt.Errorf("body names no column of this table: %w", ErrInvalid)
	}
	for _, key := range keys {
		if !present[key] {
			return nil, nil, fmt.Errorf("body names no %s: %w", key, ErrInvalid)
		}
	}
	return named, args, nil
}

// upsertStatement builds the one INSERT that can write a row this file may
// already hold. The key columns are the conflict target rather than part of the
// update, since a row that collided already holds exactly those values.
func upsertStatement(tbl string, named, keys []string) string {
	target := make([]string, len(keys))
	for i, col := range keys {
		target[i] = quoteIdent(col)
	}
	return "INSERT INTO " + quoteIdent(tbl) +
		" (" + joinQuoted(named) + ")" +
		" VALUES (" + placeholders(len(named)) + ")" +
		" ON CONFLICT(" + strings.Join(target, ", ") + ") DO " + updateClause(named, keys)
}

// updateClause is what the conflict does to a row that is already there: every
// named column the body carries takes the incoming value, and the key columns
// are left alone. A body carrying nothing but the key updates nothing rather
// than deleting the row, which is the failure mode the conflict clause exists
// to avoid.
func updateClause(named, keys []string) string {
	isKey := make(map[string]bool, len(keys))
	for _, col := range keys {
		isKey[col] = true
	}
	var sets []string
	for _, col := range named {
		if isKey[col] {
			continue
		}
		sets = append(sets, quoteIdent(col)+" = excluded."+quoteIdent(col))
	}
	if len(sets) == 0 {
		return "NOTHING"
	}
	return "UPDATE SET " + strings.Join(sets, ", ")
}

// keyWhere is the equality chain a key selects a row by, with every value bound
// rather than spliced. The key text is JSON from another machine, so nothing in
// it may reach the statement.
func keyWhere(keys []string, values []any) (string, []any) {
	where := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, col := range keys {
		where[i] = quoteIdent(col) + " = ?"
		args[i] = values[i]
	}
	return strings.Join(where, " AND "), args
}

// ImportMarks returns every origin this file has applied entries from and how
// far it has applied them, as the mark map a transport's pull is asked with.
// An origin absent from the map is one read from the start of its log, which is
// the same thing as a mark of zero.
func (d *DB) ImportMarks() (map[string]int, error) {
	rows, err := d.sqlDB.Query(`SELECT origin, seq FROM sync_import_mark`)
	if err != nil {
		if isMissingTable(err) {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("db: import marks: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	marks := map[string]int{}
	for rows.Next() {
		var origin string
		var seq int
		if err := rows.Scan(&origin, &seq); err != nil {
			return nil, fmt.Errorf("db: import marks: %w", mapBusy(err))
		}
		marks[origin] = seq
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: import marks: %w", mapBusy(err))
	}
	return marks, nil
}

// placeholders is the "?, ?, ?" a VALUES clause of n bound values needs. The
// values themselves are always arguments, so this is where a statement's shape
// ends.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func joinQuoted(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quoteIdent(name)
	}
	return strings.Join(quoted, ", ")
}
