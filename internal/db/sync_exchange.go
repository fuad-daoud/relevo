package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The exchange seam the sync log is built on: the local-only import marks, the
// outbox drain and the reads that turn a drained entry into the row's current
// state. Every read and write here is scoped to one *Tx so a caller can hold the
// outbox contents and the rows they name in a single consistent read, and every
// *DB form opens that transaction itself.

// ImportMark returns how far origin's entries have been applied: the sequence
// number of the last entry applied from that origin's log. found is false when
// origin has no mark, which is what a machine that has never imported from it
// sees and is different from a mark of zero.
//
// A mark is absent rather than zero on a file whose schema predates the table:
// there is nothing applied either way, and the next write creates the row.
func (d *DB) ImportMark(origin string) (int, bool, error) {
	return importMark(context.Background(), d.sqlDB, origin)
}

func (t *Tx) ImportMark(origin string) (int, bool, error) { return importMark(t.ctx, t.conn, origin) }

func importMark(ctx context.Context, q queryer, origin string) (int, bool, error) {
	var seq int
	err := q.QueryRowContext(ctx, `SELECT seq FROM sync_import_mark WHERE origin = ?`, origin).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("db: import mark %s: %w", origin, mapBusy(err))
	}
	return seq, true, nil
}

// SetImportMark records that every entry up to and including seq has been
// applied from origin's log. It replaces any earlier mark for the origin in
// place, so one origin keeps one mark however many rounds have written it.
//
// It is written in the same transaction as the batch that moved the mark, which
// is the property that makes a restore from a backup consistent: the file comes
// back with the rows and the marks in the same state, so the entries that
// arrived after the backup are re-applied rather than skipped.
func (d *DB) SetImportMark(origin string, seq int) error {
	return d.Tx(func(t *Tx) error { return t.SetImportMark(origin, seq) })
}

func (t *Tx) SetImportMark(origin string, seq int) error {
	// OR REPLACE is right here and wrong for a shared row. The mark table has
	// no trigger, no foreign key and nothing that references it, so replacing
	// the row deletes one mark row and writes another; replacing a shared row
	// instead deletes that row first and cascades to its children, which is why
	// the importer writes those with a conflict clause instead.
	if _, err := t.exec(`INSERT OR REPLACE INTO sync_import_mark (origin, seq) VALUES (?, ?)`, origin, seq); err != nil {
		return fmt.Errorf("db: set import mark %s: %w", origin, mapBusy(err))
	}
	return nil
}

// ExchangeRow is one shared row read for export: the table it is in, the
// primary key it was found by, and every column the file holds, in schema
// order, each value in the type the file stored it as.
//
// Values come back typed rather than as text. A BLOB column returns its bytes
// and the compressed columns of migration 013 return the bytes on disk, so a
// caller sees exactly what is stored and never a decompressed rendering that a
// recompression might not reproduce.
type ExchangeRow struct {
	// Table is the shared table the row is in.
	Table string
	// PK is the primary key the row was read by, as the outbox records it.
	PK string
	// Columns are the row's columns in schema order, keyed by name.
	Columns []ExchangeColumn
}

// ExchangeColumn is one column of an exported row: its name and the value the
// file holds for it, with NULL kept as a nil value rather than dropped.
type ExchangeColumn struct {
	// Name is the column name as this machine's schema spells it.
	Name string
	// Value is the stored value: nil, int64, float64, string or []byte.
	Value any
}

// ReadExchangeRow returns one row of a shared table by its primary key, with
// every column the table declares. ok is false when the table has no such row,
// which for a drained outbox entry is the row's absence rather than a failure.
//
// tbl must be a name SharedTables carries: the table name reaches SQL from a
// body a caller may not control, and the only names that may reach SQL are the
// ones this package already classified as shared.
func (d *DB) ReadExchangeRow(tbl, pk string) (ExchangeRow, bool, error) {
	return readExchangeRow(context.Background(), d.sqlDB, tbl, pk)
}

func (t *Tx) ReadExchangeRow(tbl, pk string) (ExchangeRow, bool, error) {
	return readExchangeRow(t.ctx, t.conn, tbl, pk)
}

func readExchangeRow(ctx context.Context, q queryer, tbl, pk string) (ExchangeRow, bool, error) {
	shared := sharedTable(tbl)
	if shared == nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	keys, err := exchangeKeyValues(pk, len(shared.PrimaryKey))
	if err != nil {
		return ExchangeRow{}, false, err
	}
	columns, err := exchangeColumns(ctx, q, tbl)
	if err != nil {
		return ExchangeRow{}, false, err
	}
	query, args := exchangeRowQuery(*shared, columns, keys)
	return scanExchangeRow(ctx, q, tbl, pk, query, args)
}

// exchangeRowQuery builds the one SELECT a row read runs: every column by name,
// and the primary key as an equality chain with the key values bound rather
// than spliced, so a pk that is not this machine's shape cannot become SQL.
func exchangeRowQuery(shared SharedTable, columns []string, keys []any) (string, []any) {
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = quoteIdent(col)
	}
	where := ""
	args := make([]any, 0, len(shared.PrimaryKey))
	for i, col := range shared.PrimaryKey {
		if i > 0 {
			where += " AND "
		}
		where += quoteIdent(col) + " = ?"
		args = append(args, keys[i])
	}
	return "SELECT " + strings.Join(quoted, ", ") + " FROM " + shared.Name + " WHERE " + where, args
}

// scanExchangeRow runs the row read and turns the result set into one row. A
// NULL value is kept as a nil so an absent column is distinguishable from a
// column holding an empty string, which is the difference between a column the
// writer set to nothing and one that was never set.
func scanExchangeRow(ctx context.Context, q queryer, tbl, pk, query string, args []any) (ExchangeRow, bool, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
		}
		return ExchangeRow{}, false, nil
	}
	names, err := rows.Columns()
	if err != nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
	}
	values := make([]any, len(names))
	dest := make([]any, len(names))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
	}
	if err := rows.Close(); err != nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
	}

	row := ExchangeRow{Table: tbl, PK: pk, Columns: make([]ExchangeColumn, len(names))}
	for i, name := range names {
		row.Columns[i] = ExchangeColumn{Name: name, Value: values[i]}
	}
	return row, true, nil
}

// exchangeColumns returns the table's column names in schema order, so an
// export carries the columns the file declares rather than the columns a query
// happened to name. The names come from this machine's own schema, which is the
// only source a column name may come from.
func exchangeColumns(ctx context.Context, q queryer, tbl string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, tbl)
	if err != nil {
		return nil, fmt.Errorf("db: exchange columns %s: %w", tbl, mapBusy(err))
	}
	return collectRows(rows, func(s rowScanner) (string, error) { return scanString(s) })
}

// exchangeKeyValues parses the pk text an outbox entry carries into the key
// values it names. The text is JSON, so it is parsed rather than read, and each
// value is returned as a parameter; nothing from the text is spliced into a
// statement. A pk that is not the JSON array of exactly the table's key columns
// is refused rather than guessed at, because a key of the wrong shape would
// either match nothing or match a row the entry never named.
func exchangeKeyValues(pk string, want int) ([]any, error) {
	var raw []any
	if err := json.Unmarshal([]byte(pk), &raw); err != nil {
		return nil, fmt.Errorf("db: exchange key %s: not a JSON array: %w", pk, ErrInvalid)
	}
	if len(raw) != want {
		return nil, fmt.Errorf("db: exchange key %s: has %d values, want %d: %w", pk, len(raw), want, ErrInvalid)
	}
	out := make([]any, len(raw))
	for i, v := range raw {
		out[i] = exchangeKeyValue(v)
	}
	return out, nil
}

// exchangeKeyValue maps a decoded JSON key value onto the type the driver binds.
// A JSON number arrives as a float64, and binding that for an INTEGER column
// would compare as a real, so an integral number is bound as the integer it is.
func exchangeKeyValue(v any) any {
	f, ok := v.(float64)
	if !ok {
		return v
	}
	if f == float64(int64(f)) {
		return int64(f)
	}
	return f
}

// sharedTable returns the shared table entry named tbl, or nil when no shared
// table carries that name.
func sharedTable(tbl string) *SharedTable {
	for i := range SharedTables {
		if SharedTables[i].Name == tbl {
			return &SharedTables[i]
		}
	}
	return nil
}

// DrainedEntry is one outbox entry with the state of the row it names. The entry
// says which row was written and how; Row is what that row holds at the moment
// the drain read it, and is absent when the row is gone.
type DrainedEntry struct {
	// Seq is the entry's position in the outbox, and the order entries come
	// back in.
	Seq int
	// Table and PK are the row the entry names, in the outbox's own spelling:
	// the table name and the json_array of its primary-key columns.
	Table string
	PK    string
	// Op is the write the trigger recorded: insert, update or delete.
	Op string
	// Origin is the installation the trigger attributed the row to. It is
	// absent for a child whose parent was already gone, which the exporter
	// skips rather than treating as a failure.
	Origin sql.NullString
	// Row is the row's current state, nil when the row is not in the file.
	Row *ExchangeRow
}

// DrainOutbox reads up to limit outbox entries in seq order, and with each entry
// the current state of the row it names, from this one transaction.
//
// Both reads are here together on purpose. The exporter needs the entry and the
// row to agree: an entry whose state is read after the drain, in a separate
// transaction, would report a row as present or absent according to a write that
// arrived between the two reads, and the batch it builds would carry a state no
// point in time held. Holding the write lock for the whole read closes that
// window -- another writer cannot commit while the drain runs, so every entry in
// one drain is the state at one moment.
func (d *DB) DrainOutbox(limit int) ([]DrainedEntry, error) {
	var out []DrainedEntry
	err := d.Tx(func(t *Tx) error {
		var err error
		out, err = t.DrainOutbox(limit)
		return err
	})
	return out, err
}

func (t *Tx) DrainOutbox(limit int) ([]DrainedEntry, error) {
	entries, err := t.readOutbox(limit)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		row, found, rerr := t.ReadExchangeRow(entries[i].Table, entries[i].PK)
		if rerr != nil {
			// An entry naming a table this file does not share is a defect in
			// the schema rather than a row to skip: reading it is what found
			// it, so the drain reports it instead of dropping a change.
			return nil, fmt.Errorf("db: drain outbox entry %d: %w", entries[i].Seq, rerr)
		}
		if found {
			entries[i].Row = &row
		}
	}
	return entries, nil
}

// readOutbox returns up to limit entries from the head of the change log, in
// seq order. Reading in seq order and not by primary key is what makes the drain
// a history: the order is the order the writes happened in, and a parent written
// before its child comes out before it.
func (t *Tx) readOutbox(limit int) ([]DrainedEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := t.conn.QueryContext(t.ctx,
		`SELECT seq, tbl, pk, op, origin FROM sync_outbox ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		if isMissingTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("db: drain outbox: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	var entries []DrainedEntry
	for rows.Next() {
		var e DrainedEntry
		if err := rows.Scan(&e.Seq, &e.Table, &e.PK, &e.Op, &e.Origin); err != nil {
			return nil, fmt.Errorf("db: drain outbox: %w", mapBusy(err))
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: drain outbox: %w", mapBusy(err))
	}
	return entries, nil
}

// DeleteDrainedOutbox removes every outbox entry up to and including seq, and
// leaves the rest. It runs after the transport has accepted the batch those
// entries produced, so a crash between the append and this delete re-exports
// them rather than losing them: re-export is safe because import is an
// idempotent upsert.
//
// The cut is by seq and not by count, so a drain that returned fewer entries
// than the caller asked for -- because the log held fewer -- deletes exactly what
// it drained and nothing that arrived after it.
func (d *DB) DeleteDrainedOutbox(seq int) error {
	return d.Tx(func(t *Tx) error { return t.DeleteDrainedOutbox(seq) })
}

func (t *Tx) DeleteDrainedOutbox(seq int) error {
	if _, err := t.exec(`DELETE FROM sync_outbox WHERE seq <= ?`, seq); err != nil {
		if isMissingTable(err) {
			return nil
		}
		return fmt.Errorf("db: delete drained outbox through %d: %w", seq, mapBusy(err))
	}
	return nil
}

// ownerStep is one step from a shared row toward the installation that owns it:
// read column from parent, where parentKey holds the value fed in.
type ownerStep struct {
	// column is the column read from the parent table.
	column string
	// parent is the table to read it from.
	parent string
	// key is the parent column the incoming value is compared against.
	key string
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
	// installation table ends at its own id, every other root at origin.
	owner string
	// hops are the steps outward, innermost first.
	hops []ownerStep
	// kinds resolves the row per owner_kind for the one table that carries a
	// discriminator, and is empty for every other table.
	kinds []ownerKind
}

// ownerKind is one discriminator value's route to an owner.
type ownerKind struct {
	// kind is the owner_kind value this rule applies to.
	kind string
	// rule is the route a row of that kind takes.
	rule ownerRule
}

// OwnerRules is how each shared table's rows resolve to the installation that
// owns them, in SharedTables order.
//
// This is the Go half of the resolution the outbox triggers already perform in
// SQL. The triggers are immutable once applied, so a second hand-written copy of
// their logic is where the two would drift; TestOwnerResolutionMatchesTriggers
// compares each rule against the live trigger body, which is the only copy that
// cannot go stale.
var OwnerRules = map[string]ownerRule{
	"repo":           {owner: "origin"},
	"mastermind":     {owner: "origin"},
	"binding_record": {owner: "origin"},
	// The installation's id is its own installation id, so its row names
	// itself rather than reading an origin column.
	"installation":  {owner: "id"},
	"binding":       {owner: "origin"},
	"chains":        {owner: "origin"},
	"binding_event": {child: "record_id", hops: []ownerStep{{column: "origin", parent: "binding_record", key: "id"}}},
	"round_file":    {child: "record_id", hops: []ownerStep{{column: "origin", parent: "binding_record", key: "id"}}},
	"chain_event":   {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"chain_member":  {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"chain_check":   {child: "chain_id", hops: []ownerStep{{column: "origin", parent: "chains", key: "id"}}},
	"round":         {child: "binding_id", hops: []ownerStep{{column: "origin", parent: "binding", key: "id"}}},
	"event":         {child: "binding_id", hops: []ownerStep{{column: "origin", parent: "binding", key: "id"}}},
	// Two hops to an owner: an artifact belongs to a round, and the round
	// belongs to a binding.
	"artifact": {child: "round_id", hops: []ownerStep{
		{column: "binding_id", parent: "round", key: "id"},
		{column: "origin", parent: "binding", key: "id"},
	}},
	// A transcript has no parent column: owner_kind names the table owner_id is
	// an id in. A kind this build does not know resolves to no owner rather than
	// to a guess, which is what the trigger's ELSE does.
	"transcript": {kinds: []ownerKind{
		{kind: "round", rule: ownerRule{child: "owner_id", hops: []ownerStep{
			{column: "binding_id", parent: "round", key: "id"},
			{column: "origin", parent: "binding", key: "id"},
		}}},
		{kind: "mastermind", rule: ownerRule{child: "owner_id", hops: []ownerStep{
			{column: "origin", parent: "mastermind", key: "id"},
		}}},
	}},
}

// OwnerExpression renders the rule's SQL for one row alias, in the shape the
// outbox triggers write: a root's owner column, a child's nested parent
// subqueries, or the discriminator's CASE for a table that has one.
//
// The same rendering serves the trigger comparison and the row lookup, so the
// text a test pins against a trigger body is the text the query runs.
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
// The kinds are rendered in the rule's order, so the text a trigger is compared
// against is ordered the way the trigger orders its WHEN arms.
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
// resolve to: the row is absent, or its parent is already gone, which is the
// case a child removed by a cascade lands in. A caller that skips such an entry
// is right to, because the parent's own delete entry carries the removal to every
// importer.
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
// the row's own table, restricted by the primary key with every key value bound.
// The table and column names come from SharedTables and OwnerRules, never from
// the pk text, which is parsed into parameters.
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
