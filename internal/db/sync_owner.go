package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ownerStep is one step from a shared row toward the installation that owns it:
// read column from parent, where key holds the value fed in.
type ownerStep struct {
	column string
	parent string
	key    string
}

// ownerRule is how one shared table's rows resolve to the installation that owns
// them. A table with no hops carries the owner itself and owner names that
// column; a child walks hops outward, and the first hop is fed by the child
// column the table holds. A table whose parent depends on a discriminator
// carries one rule per kind, in the order its trigger lists them.
type ownerRule struct {
	// child is the column on the row that feeds the first hop. It is empty for
	// a table that resolves without a hop.
	child string
	// owner is the column the resolution ends at when there are no hops: the
	// installation table ends at its own id, every other root at origin or writer.
	owner string
	// hops are the steps outward, innermost first.
	hops []ownerStep
	// kinds resolves the row per owner_kind for the one table that carries a
	// discriminator, and is empty for every other table.
	kinds []ownerKind
	// oldOnUpdate indicates that the update trigger attributes to OLD rather than NEW.
	oldOnUpdate bool
}

// ownerKind is one discriminator value's route to an owner.
type ownerKind struct {
	// kind is the owner_kind value this rule applies to.
	kind string
	// rule is the route a row of that kind takes.
	rule ownerRule
}

// OwnerRules is how each shared table's rows resolve to the installation that
// owns them, in SharedTables order. This is the Go half of the resolution the
// outbox triggers perform in SQL. The triggers are immutable once applied, so
// this second copy is where the two can drift;
// TestOwnerResolutionMatchesTriggers compares each rule against the live
// trigger body, which cannot go stale.
var OwnerRules = map[string]ownerRule{
	"repo":               {owner: "origin"},
	"mastermind":         {owner: "origin"},
	"binding_record":     {owner: "writer", oldOnUpdate: true},
	"installation":       {owner: "id"},
	"binding":            {owner: "writer", oldOnUpdate: true},
	"chains":             {owner: "origin"},
	"availability_event": {owner: "origin"},
	"latency_sample":     {owner: "origin"},
	"binding_event":      {child: "record_id", hops: []ownerStep{{column: "writer", parent: "binding_record", key: "id"}}},
	"round_file":         {child: "record_id", hops: []ownerStep{{column: "writer", parent: "binding_record", key: "id"}}},
	"chain_event":        {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"chain_member":       {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"chain_check":        {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"round":              {child: "binding_id", hops: []ownerStep{{column: "writer", parent: "binding", key: "id"}}},
	"event":              {child: "binding_id", hops: []ownerStep{{column: "writer", parent: "binding", key: "id"}}},
	"artifact": {child: "round_id", hops: []ownerStep{
		{column: "binding_id", parent: "round", key: "id"},
		{column: "writer", parent: "binding", key: "id"},
	}},
	"transcript": {kinds: []ownerKind{
		{kind: "round", rule: ownerRule{child: "owner_id", hops: []ownerStep{
			{column: "binding_id", parent: "round", key: "id"},
			{column: "writer", parent: "binding", key: "id"},
		}}},
		{kind: "mastermind", rule: ownerRule{child: "owner_id", hops: []ownerStep{
			{column: "origin", parent: "mastermind", key: "id"},
		}}},
	}},
}

func creatorColumn(tbl string) string {
	if tbl == "binding_record" || tbl == "binding" {
		return "origin"
	}
	return ""
}

// CreatorColumn returns "origin" for binding_record and binding, and empty string
// for every other table.
func CreatorColumn(tbl string) string {
	return creatorColumn(tbl)
}

// ownerExpression renders the rule's SQL for one row alias, in the shape the
// outbox triggers write. The same rendering serves the trigger comparison and
// the row lookup, so the text the test pins is the text the query runs.
func ownerExpression(rule ownerRule, alias string) string {
	if len(rule.kinds) > 0 {
		return ownerKindExpression(rule, alias)
	}
	if len(rule.hops) == 0 {
		return alias + "." + rule.owner
	}
	value := alias + "." + rule.child
	for i, hop := range rule.hops {
		inner := "SELECT " + hop.column + " FROM " + hop.parent + " WHERE " + hop.key + " = " + value
		// Every step but the outermost is a subquery in a value position and
		// needs its own parentheses; the outermost one is wrapped by the return.
		if i < len(rule.hops)-1 {
			inner = "(" + inner + ")"
		}
		value = inner
	}
	return "(" + value + ")"
}

// ownerKindExpression renders the CASE a discriminator table's trigger writes.
// The kinds render in the rule's order, matching the order of the trigger's
// WHEN arms.
func ownerKindExpression(rule ownerRule, alias string) string {
	var b strings.Builder
	b.WriteString("CASE " + alias + ".owner_kind")
	for _, k := range rule.kinds {
		b.WriteString(" WHEN '" + k.kind + "' THEN " + ownerExpression(k.rule, alias))
	}
	b.WriteString(" ELSE NULL END")
	return b.String()
}

// ResolveOwner returns the installation that owns one shared row, resolving it
// the way the outbox triggers do. found is false when the row has no owner to
// resolve to: the row is absent, or its parent is already gone after a cascade.
// Skipping such an entry is right: the parent's own delete entry carries the
// removal to every importer.
func (d *DB) ResolveOwner(tbl, pk string) (string, bool, error) {
	return resolveOwner(context.Background(), d.sqlDB, tbl, pk)
}

func (t *Tx) ResolveOwner(tbl, pk string) (string, bool, error) {
	return resolveOwner(t.ctx, t.conn, tbl, pk)
}

func resolveOwner(ctx context.Context, q queryer, tbl, pk string) (string, bool, error) {
	shared := sharedTable(tbl)
	if shared == nil {
		return "", false, fmt.Errorf("db: resolve owner: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	rule, ok := OwnerRules[tbl]
	if !ok {
		return "", false, fmt.Errorf("db: resolve owner: %s has no owner rule: %w", tbl, ErrInvalid)
	}
	keys, err := exchangeKeyValues(pk, len(shared.PrimaryKey))
	if err != nil {
		return "", false, err
	}
	query, args := ownerQuery(*shared, rule, keys)
	var owner sql.NullString
	err = q.QueryRowContext(ctx, query, args...).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: resolve owner %s %s: %w", tbl, pk, mapBusy(err))
	}
	if !owner.Valid {
		return "", false, nil
	}
	return owner.String, true, nil
}

// ownerQuery builds the lookup for one row's owner: the rule's expression over
// the row's own table, restricted by the primary key. The table and column
// names come from SharedTables and OwnerRules, never from the pk text, which
// is parsed into parameters.
func ownerQuery(shared SharedTable, rule ownerRule, keys []any) (string, []any) {
	where := make([]string, len(shared.PrimaryKey))
	args := make([]any, 0, len(keys))
	for i, col := range shared.PrimaryKey {
		where[i] = quoteIdent(col) + " = ?"
		args = append(args, keys[i])
	}
	query := "SELECT " + ownerExpression(rule, quoteIdent(shared.Name)) +
		" FROM " + quoteIdent(shared.Name) + " WHERE " + strings.Join(where, " AND ")
	return query, args
}

// ResolveRoot returns the root ID (the binding_record or binding primary key id,
// or chains/mastermind/repo id) for one shared row, resolving it through its parent chain.
// found is false when the row has no root: the row is absent, or its parent is already gone.
func (d *DB) ResolveRoot(tbl, pk string) (string, bool, error) {
	return resolveRoot(context.Background(), d.sqlDB, tbl, pk)
}

func (t *Tx) ResolveRoot(tbl, pk string) (string, bool, error) {
	return resolveRoot(t.ctx, t.conn, tbl, pk)
}

func resolveRoot(ctx context.Context, q queryer, tbl, pk string) (string, bool, error) {
	shared := sharedTable(tbl)
	if shared == nil {
		return "", false, fmt.Errorf("db: resolve root: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	rule, ok := OwnerRules[tbl]
	if !ok {
		return "", false, fmt.Errorf("db: resolve root: %s has no owner rule: %w", tbl, ErrInvalid)
	}
	keys, err := exchangeKeyValues(pk, len(shared.PrimaryKey))
	if err != nil {
		return "", false, err
	}
	query, args := rootQuery(*shared, rule, keys)
	var rootID sql.NullString
	err = q.QueryRowContext(ctx, query, args...).Scan(&rootID)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: resolve root %s %s: %w", tbl, pk, mapBusy(err))
	}
	if !rootID.Valid {
		return "", false, nil
	}
	return rootID.String, true, nil
}

func rootQuery(shared SharedTable, rule ownerRule, keys []any) (string, []any) {
	where := make([]string, len(shared.PrimaryKey))
	args := make([]any, 0, len(keys))
	for i, col := range shared.PrimaryKey {
		where[i] = quoteIdent(col) + " = ?"
		args = append(args, keys[i])
	}
	query := "SELECT " + rootExpression(rule, quoteIdent(shared.Name)) +
		" FROM " + quoteIdent(shared.Name) + " WHERE " + strings.Join(where, " AND ")
	return query, args
}

func rootExpression(rule ownerRule, alias string) string {
	if len(rule.kinds) > 0 {
		var b strings.Builder
		b.WriteString("CASE " + alias + ".owner_kind")
		for _, k := range rule.kinds {
			b.WriteString(" WHEN '" + k.kind + "' THEN " + rootExpression(k.rule, alias))
		}
		b.WriteString(" ELSE NULL END")
		return b.String()
	}
	if len(rule.hops) == 0 {
		return alias + ".id"
	}
	value := alias + "." + rule.child
	for i, hop := range rule.hops {
		targetCol := hop.column
		if i == len(rule.hops)-1 {
			targetCol = hop.key
		}
		inner := "SELECT " + targetCol + " FROM " + hop.parent + " WHERE " + hop.key + " = " + value
		if i < len(rule.hops)-1 {
			inner = "(" + inner + ")"
		}
		value = inner
	}
	return "(" + value + ")"
}

// Origin returns the installation id this Tx scopes to.
func (t *Tx) Origin() string {
	return t.origin
}

// HasLiveBindingRecord reports whether a live unarchived binding_record exists with the given (writer, owner, name) excluding excludeID.
func (t *Tx) HasLiveBindingRecord(writer, owner, name, excludeID string) (bool, error) {
	var count int
	query := `SELECT COUNT(*) FROM binding_record WHERE writer = ? AND owner = ? AND name = ? AND archived_at IS NULL AND id <> ?`
	err := t.conn.QueryRowContext(t.ctx, query, writer, owner, name, excludeID).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: check live binding record: %w", mapBusy(err))
	}
	return count > 0, nil
}

// Term returns the term for (rootID, writer) from sync_binding_term.
func (d *DB) Term(rootID, writer string) (upto int, regrantOrigin string, regrantSeq int, found bool, err error) {
	return termQuery(context.Background(), d.sqlDB, rootID, writer)
}

func (t *Tx) Term(rootID, writer string) (upto int, regrantOrigin string, regrantSeq int, found bool, err error) {
	return termQuery(t.ctx, t.conn, rootID, writer)
}

func termQuery(ctx context.Context, q queryer, rootID, writer string) (int, string, int, bool, error) {
	var (
		upto          int
		regrantOrigin sql.NullString
		regrantSeq    sql.NullInt64
	)
	query := `SELECT upto, regrant_origin, regrant_seq FROM sync_binding_term WHERE root_id = ? AND writer = ?`
	err := q.QueryRowContext(ctx, query, rootID, writer).Scan(&upto, &regrantOrigin, &regrantSeq)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return 0, "", 0, false, nil
	}
	if err != nil {
		return 0, "", 0, false, fmt.Errorf("db: term %s %s: %w", rootID, writer, mapBusy(err))
	}
	return upto, regrantOrigin.String, int(regrantSeq.Int64), true, nil
}

// SetTerm upserts the term for (rootID, writer).
func (t *Tx) SetTerm(rootID, writer string, upto int) error {
	query := `INSERT INTO sync_binding_term (root_id, writer, upto) VALUES (?, ?, ?)
		ON CONFLICT(root_id, writer) DO UPDATE SET upto = excluded.upto`
	if _, err := t.exec(query, rootID, writer, upto); err != nil {
		return fmt.Errorf("db: set term %s %s: %w", rootID, writer, mapBusy(err))
	}
	return nil
}

// DeleteTerm removes the term for (rootID, writer).
func (t *Tx) DeleteTerm(rootID, writer string) error {
	query := `DELETE FROM sync_binding_term WHERE root_id = ? AND writer = ?`
	if _, err := t.exec(query, rootID, writer); err != nil {
		if isMissingTable(err) {
			return nil
		}
		return fmt.Errorf("db: delete term %s %s: %w", rootID, writer, mapBusy(err))
	}
	return nil
}

// SetWriterAfter writes the after dependency for a handed-over root.
func (t *Tx) SetWriterAfter(rootID, afterOrigin string, afterSeq int) error {
	query := `INSERT INTO sync_writer_after (root_id, after_origin, after_seq) VALUES (?, ?, ?)
		ON CONFLICT(root_id) DO UPDATE SET after_origin = excluded.after_origin, after_seq = excluded.after_seq`
	if _, err := t.exec(query, rootID, afterOrigin, afterSeq); err != nil {
		return fmt.Errorf("db: set writer after %s: %w", rootID, mapBusy(err))
	}
	return nil
}

// WriterAfter returns the after dependency from sync_writer_after for the root of (tbl, pk).
func (d *DB) WriterAfter(tbl, pk string) (string, int, bool, error) {
	return writerAfter(context.Background(), d.sqlDB, tbl, pk)
}

func (t *Tx) WriterAfter(tbl, pk string) (string, int, bool, error) {
	return writerAfter(t.ctx, t.conn, tbl, pk)
}

func writerAfter(ctx context.Context, q queryer, tbl, pk string) (string, int, bool, error) {
	rootID, found, err := resolveRoot(ctx, q, tbl, pk)
	if err != nil || !found {
		return "", 0, false, err
	}
	var origin string
	var seq int
	err = q.QueryRowContext(ctx, `SELECT after_origin, after_seq FROM sync_writer_after WHERE root_id = ?`, rootID).Scan(&origin, &seq)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("db: writer after %s %s: %w", tbl, pk, mapBusy(err))
	}
	return origin, seq, true, nil
}

// RecordFencedChange writes a row into sync_fenced_change.
func (d *DB) RecordFencedChange(rootID, tbl, pk string, at time.Time) error {
	query := `INSERT OR REPLACE INTO sync_fenced_change (root_id, tbl, pk, at) VALUES (?, ?, ?, ?)`
	if _, err := d.sqlDB.ExecContext(context.Background(), query, rootID, tbl, pk, at.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("db: record fenced change: %w", mapBusy(err))
	}
	return nil
}

func (t *Tx) RecordFencedChange(rootID, tbl, pk string, at time.Time) error {
	query := `INSERT OR REPLACE INTO sync_fenced_change (root_id, tbl, pk, at) VALUES (?, ?, ?, ?)`
	if _, err := t.exec(query, rootID, tbl, pk, at.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("db: record fenced change: %w", mapBusy(err))
	}
	return nil
}

// IsTakeoverFenced reports whether rootID carries a takeover fence against this installation.
func (d *DB) IsTakeoverFenced(rootID string) (bool, error) {
	return isTakeoverFenced(context.Background(), d.sqlDB, rootID, d.origin)
}

func (t *Tx) IsTakeoverFenced(rootID string) (bool, error) {
	return isTakeoverFenced(t.ctx, t.conn, rootID, t.origin)
}

func isTakeoverFenced(ctx context.Context, q queryer, rootID, origin string) (bool, error) {
	var leftKind string
	err := q.QueryRowContext(ctx, `SELECT kind FROM sync_binding_left WHERE root_id = ?`, rootID).Scan(&leftKind)
	if err == nil && leftKind == "takeover" {
		return true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !isMissingTable(err) {
		return false, fmt.Errorf("db: check binding left %s: %w", rootID, mapBusy(err))
	}
	var count int
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_binding_term WHERE root_id = ? AND writer = ?`, rootID, origin).Scan(&count)
	if err == nil && count > 0 {
		return true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !isMissingTable(err) {
		return false, fmt.Errorf("db: check binding term %s: %w", rootID, mapBusy(err))
	}
	return false, nil
}
