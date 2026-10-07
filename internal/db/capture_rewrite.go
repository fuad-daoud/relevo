//go:build !modernc

package db

// The rewrite: the one shape of change set the remote accepts, for a whole
// foreign-key group in one transaction.
//
// A rewrite is the engine's own delete-then-insert pair, so recording it is
// writing the row twice and the change set carries both halves. The remote
// applies them in the change set's order with its foreign keys enforced, so the
// halves cannot be interleaved table by table:
//
//   - deleting a row the remote still has children for is refused, so the
//     children of a table go before the table itself;
//   - inserting a row whose parent the remote does not hold is refused, so the
//     parent of a table goes before the table itself.
//
// One order cannot serve both, so this records the group's deletes children
// first and its inserts parents first, holding each table's values between the
// two halves. It is one transaction: a rewrite that committed its deletes and
// not its inserts would leave the file missing rows, which is the one thing a
// backfill must never do.
//
// The values are held in this process rather than staged in the file, because
// every write this connection could make is captured -- a staging table in the
// file is itself a change set, and the engine captures it (see the driver's
// capture surface, which offers no way to exclude a table) -- and ATTACH, which
// would put the staging outside the file, is an experimental feature this
// driver refuses without a flag no caller here can set.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// insertVars is how many host parameters one INSERT statement binds. It is well
// under the 999 a stock SQLite allows, so the statement is one every build
// accepts however wide the table is.
const insertVars = 500

// TablePass is one table's outcome in a group rewrite.
type TablePass struct {
	// Table is the table this is about.
	Table string
	// Rows is how many rows the rewrite recorded for it.
	Rows int64
	// NextRowID is the rowid after the last row the rewrite covered, which is
	// where a later pass resumes.
	NextRowID int64
	// Done is whether the rewrite reached the end of the table.
	Done bool
}

// GroupPass is what one group rewrite recorded, per table.
type GroupPass struct {
	// Tables is each table of the group in the order the rewrite took them,
	// which is the group's own order for the inserts.
	Tables []TablePass
}

// recordedRow is one row held between the delete half and the insert half.
type recordedRow struct {
	// rowid is the row's identity, kept so the rewrite puts it back where it was
	// and a later pass resumes from the right rowid.
	rowid int64
	// values are the row's column values, in the column order the rewrite read.
	values []any
}

// Groups is the file's application tables as foreign-key-closed groups, each
// ordered parents first. It is the walk's own table set: the same rule about the
// driver's own tables applies, so a table this tree has not heard of is in a
// group too.
func (c *CaptureConn) Groups(ctx context.Context) ([]CaptureGroup, error) {
	conn, release, err := c.held.acquire(ctx)
	if err != nil {
		return nil, stepError(c.path, "the backfill's groups", err)
	}
	defer release()
	return CaptureGroups(ctx, conn)
}

// Rerecord records the group's rows into the change set as one transaction: the
// deletes children first, the inserts parents first, so the remote can apply
// every half of the rewrite in the order the change set carries them.
//
// from is the rowid each table resumes at, which is what makes the walk
// resumable, and limit is how many rows of each table this call takes, so one
// call is bounded whatever the database holds. A group whose rows do not fit one
// call is rewritten by several, and each of those is a whole group again: the
// deletes and the inserts of the rows one call covers, never half of either.
func (c *CaptureConn) Rerecord(ctx context.Context, group CaptureGroup, from map[string]int64, limit int) (GroupPass, error) {
	var out GroupPass
	if limit <= 0 {
		return out, nil
	}
	conn, release, err := c.held.acquire(ctx)
	if err != nil {
		return out, stepError(group.Key, "the backfill's group rewrite", err)
	}
	defer release()
	return rerecordOver(ctx, conn, group, from, limit)
}

// rerecordOver is the rewrite on a connection the caller already holds.
func rerecordOver(ctx context.Context, conn *sql.Conn, group CaptureGroup,
	from map[string]int64, limit int) (GroupPass, error) {
	var out GroupPass
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return out, fmt.Errorf("db: %s: backfill begin: %w", group.Key, mapBusy(err))
	}
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") }()

	// The delete half: the group's own order reversed, so a table's rows go
	// before the rows of the tables that reference them.
	held := make(map[string][]recordedRow, len(group.Tables))
	for i := len(group.Tables) - 1; i >= 0; i-- {
		table := group.Tables[i]
		columns, err := backfillColumns(ctx, conn, table)
		if err != nil {
			return out, err
		}
		if len(columns) == 0 {
			// No table the walk can name has no columns, so this is a table that
			// went away between the walk listing it and this rewrite reading it.
			// It is reported rather than walked as empty: a group that silently
			// dropped one of its tables would be a group whose recorded order no
			// longer says what the remote has to be able to apply.
			return out, fmt.Errorf("db: %s: backfill: the table is not there to rewrite: %w",
				table, ErrInvalid)
		}
		rows, pass, err := takeRows(ctx, conn, table, columns, from[table], limit)
		if err != nil {
			return out, err
		}
		held[table] = rows
		out.Tables = append(out.Tables, pass)
	}

	// The insert half: the group's own order, so a table's rows go back after
	// the rows of the tables they reference.
	for _, table := range group.Tables {
		rows, ok := held[table]
		if !ok || len(rows) == 0 {
			continue
		}
		columns, err := backfillColumns(ctx, conn, table)
		if err != nil {
			return out, err
		}
		ordered, err := parentsFirstRows(ctx, conn, table, columns, rows)
		if err != nil {
			return out, err
		}
		if err := insertRows(ctx, conn, table, columns, ordered); err != nil {
			return out, err
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return out, fmt.Errorf("db: %s: backfill commit: %w", group.Key, mapBusy(err))
	}
	return out, nil
}

// takeRows reads up to limit rows of one table from the rowid the walk is at and
// deletes exactly those rows, so the delete is recorded and the values are still
// in this process to put back.
func takeRows(ctx context.Context, conn *sql.Conn, table string, columns []string, from int64, limit int) ([]recordedRow, TablePass, error) {
	pass := TablePass{Table: table}
	list := quotedColumns(columns)
	rows, err := conn.QueryContext(ctx,
		`SELECT rowid, `+list+` FROM "`+table+`" WHERE rowid >= ? ORDER BY rowid LIMIT ?`, from, limit)
	if err != nil {
		return nil, pass, fmt.Errorf("db: %s: backfill read: %w", table, mapBusy(err))
	}
	var held []recordedRow
	for rows.Next() {
		cells := make([]any, len(columns)+1)
		for i := range cells {
			cells[i] = new(any)
		}
		if err := rows.Scan(cells...); err != nil {
			_ = rows.Close()
			return nil, pass, fmt.Errorf("db: %s: backfill read: %w", table, err)
		}
		row := recordedRow{values: make([]any, len(columns))}
		for i := range cells {
			value := *(cells[i].(*any))
			if blob, ok := value.([]byte); ok {
				// The driver's own buffer does not outlive the read, and the
				// insert half runs after this loop has moved on.
				value = append([]byte(nil), blob...)
			}
			if i == 0 {
				row.rowid, _ = value.(int64)
				continue
			}
			row.values[i-1] = value
		}
		held = append(held, row)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, pass, fmt.Errorf("db: %s: backfill read: %w", table, mapBusy(err))
	}

	if len(held) == 0 {
		pass.Done = true
		return nil, pass, nil
	}
	last := held[len(held)-1].rowid
	where := make([]string, 0, len(held))
	args := make([]any, 0, len(held))
	for _, row := range held {
		where = append(where, "?")
		args = append(args, row.rowid)
	}
	del := `DELETE FROM "` + table + `" WHERE rowid IN (` + strings.Join(where, ", ") + `)`
	if _, err := conn.ExecContext(ctx, del, args...); err != nil {
		return nil, pass, fmt.Errorf("db: %s: backfill delete: %w", table, mapBusy(err))
	}

	pass.Rows = int64(len(held))
	pass.NextRowID = last + 1
	pass.Done = int64(len(held)) < int64(limit)
	return held, pass, nil
}

// parentsFirstRows orders one table's held rows so that a row whose own table
// references it comes after the row it references. The cross-table order is the
// group's; this is the last case inside one table, and it is the case a chunked
// insert would otherwise get wrong: the remote applies the rows of one statement
// together, so a row referencing a row in a later statement is refused.
//
// A row whose reference is not among the held rows is already on the remote, or
// was never there; either way it is not this pass's business, so it is left
// where the read put it.
func parentsFirstRows(ctx context.Context, conn *sql.Conn, table string, columns []string, rows []recordedRow) ([]recordedRow, error) {
	refs, err := selfReferences(ctx, conn, table)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return rows, nil
	}
	// A reference naming a column the table does not have is one the engine
	// enforces by other means; there is nothing to order from it.
	from, to, ok := selfRefIndexes(columns, refs)
	if !ok {
		return rows, nil
	}
	position := make(map[any]int, len(rows))
	for i, row := range rows {
		position[row.values[to]] = i
	}
	waiting := make([][]int, len(rows)) // row -> the held rows it waits for
	for i, row := range rows {
		parent, found := position[row.values[from]]
		if !found || parent == i {
			continue
		}
		waiting[i] = append(waiting[i], parent)
	}
	ordered := make([]recordedRow, 0, len(rows))
	placed := make([]bool, len(rows))
	for len(ordered) < len(rows) {
		next := -1
		for i := range rows {
			if placed[i] || !allPlacedIndexes(waiting[i], placed) {
				continue
			}
			next = i
			break
		}
		if next < 0 {
			// A cycle between rows of the table, which a self-reference with
			// immediate constraints cannot hold; the read's own order is taken
			// so the rewrite still runs.
			for i := range rows {
				if !placed[i] {
					next = i
					break
				}
			}
		}
		ordered = append(ordered, rows[next])
		placed[next] = true
	}
	return ordered, nil
}

// selfReferences is the table's own foreign keys: the pairs of a child column
// and the parent column it names, for the references that name this table.
func selfReferences(ctx context.Context, conn *sql.Conn, table string) ([]columnRef, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT "from", "to" FROM pragma_foreign_key_list(?) WHERE "table" = ?`, table, table)
	if err != nil {
		return nil, fmt.Errorf("db: %s: read the self references: %w", table, mapBusy(err))
	}
	var refs []columnRef
	for rows.Next() {
		var from, to sql.NullString
		if err := rows.Scan(&from, &to); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("db: %s: read the self references: %w", table, err)
		}
		refs = append(refs, columnRef{from: from.String, to: to.String})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, fmt.Errorf("db: %s: read the self references: %w", table, mapBusy(err))
	}
	return refs, nil
}

// columnRef is one foreign key as a pair of column names: the child column and
// the parent column it names.
type columnRef struct {
	from string
	to   string
}

// selfRefIndexes is where the child column and the parent column of the first
// usable self-reference sit in the row's value list, and whether such a reference
// exists at all.
func selfRefIndexes(columns []string, refs []columnRef) (int, int, bool) {
	for _, ref := range refs {
		from, to := -1, -1
		for i, column := range columns {
			switch column {
			case ref.from:
				from = i
			case ref.to:
				to = i
			}
		}
		if from >= 0 && to >= 0 {
			return from, to, true
		}
	}
	return 0, 0, false
}

// allPlacedIndexes reports whether every index in wait has been placed already.
func allPlacedIndexes(wait []int, placed []bool) bool {
	for _, i := range wait {
		if !placed[i] {
			return false
		}
	}
	return true
}

// insertRows puts the held rows back, with their rowids, in the order given. The
// statement is one per chunk of insertVars parameters, which is why the caller
// orders the rows: a row referencing a row in a later chunk is a refusal.
func insertRows(ctx context.Context, conn *sql.Conn, table string, columns []string, rows []recordedRow) error {
	if len(rows) == 0 {
		return nil
	}
	names := make([]string, 0, len(columns)+1)
	names = append(names, `"rowid"`)
	names = append(names, quotedColumnNames(columns)...)
	head := `INSERT INTO "` + table + `" (` + strings.Join(names, ", ") + `) VALUES `
	perRow := len(columns) + 1
	chunk := insertVars / perRow
	if chunk < 1 {
		chunk = 1
	}
	for start := 0; start < len(rows); start += chunk {
		end := min(start+chunk, len(rows))
		tuples := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*perRow)
		for _, row := range rows[start:end] {
			marks := make([]string, perRow)
			for i := range marks {
				marks[i] = "?"
			}
			tuples = append(tuples, "("+strings.Join(marks, ", ")+")")
			args = append(args, row.rowid)
			args = append(args, row.values...)
		}
		stmt := head + strings.Join(tuples, ", ")
		if _, err := conn.ExecContext(ctx, stmt, args...); err != nil {
			return fmt.Errorf("db: %s: backfill insert: %w", table, mapBusy(err))
		}
	}
	return nil
}

// quotedColumnNames is the column list as the quoted names a statement names
// them with, which is what keeps a column called `from` or a column with a space
// in it from reading as syntax.
func quotedColumnNames(columns []string) []string {
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = `"` + column + `"`
	}
	return quoted
}

// quotedColumns is the same list as one statement fragment.
func quotedColumns(columns []string) string {
	return strings.Join(quotedColumnNames(columns), ", ")
}

// CaptureFKParents is a group's tables in the order their deletes must be
// recorded in, which is the group's own order reversed. It is exported for the
// walk's own bookkeeping and for a caller that wants to say which order a
// rewrite will take without running one.
func CaptureFKParents(group CaptureGroup) []string {
	out := make([]string, 0, len(group.Tables))
	for i := len(group.Tables) - 1; i >= 0; i-- {
		out = append(out, group.Tables[i])
	}
	return out
}
