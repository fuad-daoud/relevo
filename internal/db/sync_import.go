package db

import (
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
	named, args, err := upsertColumns(shared.PrimaryKey, columns, ownedValues(tbl, owner, values))
	if err != nil {
		return fmt.Errorf("db: exchange upsert %s: %w", tbl, err)
	}
	if _, err := t.exec(upsertStatement(shared.Name, named, shared.PrimaryKey), args...); err != nil {
		return fmt.Errorf("db: exchange upsert %s: %w", tbl, mapRefused(mapBusy(err)))
	}
	return nil
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
