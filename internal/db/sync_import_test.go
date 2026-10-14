package db

import (
	"errors"
	"testing"
)

// The write half of the exchange seam, pinned on its own rather than only
// through the synclog tests that call it: these are the statements a remote
// body's values reach, and what they must not be able to do is decide the
// statement's shape.

// exchangeRow is one row of a shared table read back by key, so a case can say
// what the write left rather than only that the write did not fail.
func exchangeRow(t *testing.T, d *DB, tbl, pk string) ExchangeRow {
	t.Helper()
	row, found, err := d.ReadExchangeRow(tbl, pk)
	if err != nil {
		t.Fatalf("read %s %s: %v", tbl, pk, err)
	}
	if !found {
		t.Fatalf("%s %s is not in the file", tbl, pk)
	}
	return row
}

// value returns one column of an exchanged row, so a case asserts the value that
// was written rather than the presence of a row.
func value(t *testing.T, row ExchangeRow, column string) any {
	t.Helper()
	for _, col := range row.Columns {
		if col.Name == column {
			return col.Value
		}
	}
	t.Fatalf("row %s %s carries no %s column", row.Table, row.PK, column)
	return nil
}

// An absent row is inserted and a row already there is updated in place. The
// second write is the one that matters: it must not replace the row, because
// replacing it deletes it first and every child of it cascades away with it.
func TestExchangeUpsertInsertsThenUpdatesInPlace(t *testing.T) {
	d := openTestDB(t)
	row := map[string]any{
		"id": "b1", "name": "b1", "cwd": "/first", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "instX",
	}
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert("binding", "instX", row) }); err != nil {
		t.Fatalf("upsert an absent row: %v", err)
	}
	if got := value(t, exchangeRow(t, d, "binding", `["b1"]`), "cwd"); got != "/first" {
		t.Fatalf("cwd = %v, want the inserted value", got)
	}

	row["cwd"] = "/second"
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert("binding", "instX", row) }); err != nil {
		t.Fatalf("upsert a row already there: %v", err)
	}
	if got := value(t, exchangeRow(t, d, "binding", `["b1"]`), "cwd"); got != "/second" {
		t.Fatalf("cwd = %v, want the updated value", got)
	}

	// The outbox recorded both writes and no replacement: a REPLACE would have
	// left a delete behind, which is what tells the two apart in the history.
	var ops int
	if err := d.Tx(func(tx *Tx) error {
		return tx.queryRow(`SELECT COUNT(*) FROM sync_outbox WHERE tbl = 'binding'`).Scan(&ops)
	}); err != nil {
		t.Fatalf("count the outbox entries: %v", err)
	}
	if ops != 2 {
		t.Fatalf("the outbox holds %d entries for the binding, want the insert and the update", ops)
	}
}

// A row the peer holds with children is updated without them being touched. The
// children here carry ON DELETE CASCADE, so a REPLACE would take them with the
// row it deleted first and this would come back empty.
func TestExchangeUpsertKeepsCascadedChildren(t *testing.T) {
	d := openTestDB(t)
	if err := d.Tx(func(tx *Tx) error {
		return tx.ExchangeUpsert("binding_record", "instX", map[string]any{
			"id": "r1", "owner": "o", "name": "n", "state": "open", "round": 1,
			"cwd": "/z", "record_json": "{}", "created_at": "t", "updated_at": "t", "origin": "instX",
		})
	}); err != nil {
		t.Fatalf("upsert the record: %v", err)
	}
	if err := d.Tx(func(tx *Tx) error {
		return tx.ExchangeUpsert("round_file", "instX", map[string]any{
			"record_id": "r1", "name": "f", "round": 1, "body": []byte{0, 1},
			"bytes": 2, "sha256": "s", "mtime": "t", "sealed_at": "t",
		})
	}); err != nil {
		t.Fatalf("upsert the round file: %v", err)
	}

	record := map[string]any{
		"id": "r1", "owner": "o", "name": "n", "state": "closed", "round": 1,
		"cwd": "/moved", "record_json": "{}", "created_at": "t", "updated_at": "t", "origin": "instX",
	}
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert("binding_record", "instX", record) }); err != nil {
		t.Fatalf("upsert the record again: %v", err)
	}

	if got := value(t, exchangeRow(t, d, "binding_record", `["r1"]`), "state"); got != "closed" {
		t.Fatalf("state = %v, want the updated value", got)
	}
	file := exchangeRow(t, d, "round_file", `["r1","f"]`)
	if body, ok := value(t, file, "body").([]byte); !ok || len(body) != 2 {
		t.Fatalf("the child's body = %v, want it kept as the stored bytes", value(t, file, "body"))
	}
}

// A body key this build's schema does not declare is dropped rather than named
// in SQL, and a column the body omits keeps what the file already holds. A key
// a remote machine invented must not be able to add a column to a local table,
// and a body that is silent about a column must not erase it.
func TestExchangeUpsertIgnoresUnknownAndKeepsOmittedColumns(t *testing.T) {
	d := openTestDB(t)
	if err := d.Tx(func(tx *Tx) error {
		return tx.ExchangeUpsert("binding", "instX", map[string]any{
			"id": "b1", "name": "b1", "cwd": "/first", "branch": "main",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "instX",
		})
	}); err != nil {
		t.Fatalf("upsert the binding: %v", err)
	}
	if err := d.Tx(func(tx *Tx) error {
		return tx.ExchangeUpsert("binding", "instX", map[string]any{
			"id": "b1", "name": "b1", "cwd": "/second",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "instX",
		})
	}); err != nil {
		t.Fatalf("upsert the binding again: %v", err)
	}

	row := exchangeRow(t, d, "binding", `["b1"]`)
	if got := value(t, row, "cwd"); got != "/second" {
		t.Fatalf("cwd = %v, want the updated value", got)
	}
	if got := value(t, row, "branch"); got != "main" {
		t.Fatalf("branch = %v, want the column the second body omitted to be kept", got)
	}
	for _, col := range row.Columns {
		if col.Name == `x"; DROP TABLE binding; --` {
			t.Fatal("the upsert named a column the body invented")
		}
	}
}

// The statement a body reaches is fixed by the schema: its column names come
// from the table's own declaration and its values are bound, so nothing in a
// body can become SQL.
func TestExchangeUpsertBindsEveryValue(t *testing.T) {
	d := openTestDB(t)
	err := d.Tx(func(tx *Tx) error {
		return tx.ExchangeUpsert("binding", "instX", map[string]any{
			"id": "b1", "name": "b1", "cwd": "/x'); DROP TABLE binding; --",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "instX",
		})
	})
	if err != nil {
		t.Fatalf("upsert a body carrying a statement: %v", err)
	}
	if got := value(t, exchangeRow(t, d, "binding", `["b1"]`), "cwd"); got != `/x'); DROP TABLE binding; --` {
		t.Fatalf("cwd = %v, want the value stored rather than executed", got)
	}
}

// An upsert that cannot name the row it writes is refused rather than guessed
// at: a key the body omits would leave the write to land on whatever row held
// the values, and a body naming no column of this table names nothing writable.
func TestExchangeUpsertRefusesAnUnplaceableBody(t *testing.T) {
	d := openTestDB(t)
	cases := []struct {
		name   string
		tbl    string
		values map[string]any
	}{
		{"no key column", "binding", map[string]any{"name": "b1", "cwd": "/x"}},
		{"no column of the table", "binding", map[string]any{"nope": 1}},
		{"not a shared table", "schema_version", map[string]any{"version": 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert(c.tbl, "instX", c.values) })
			if err == nil {
				t.Fatalf("upsert %s with %v succeeded, want the body refused", c.tbl, c.values)
			}
		})
	}
}

// The owner a row ends up carrying is the one the caller resolved, and never the
// one the body claimed. A body may name the owner column, but it comes from a
// machine this one does not control: taking its word for who owns a row would let
// one installation take another's rows out of its hands by writing them again.
func TestExchangeUpsertTakesTheOwnerNotTheBody(t *testing.T) {
	d := openTestDB(t)
	body := map[string]any{
		"id": "b1", "name": "b1", "cwd": "/x", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "thief",
	}
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert("binding", "instX", body) }); err != nil {
		t.Fatalf("upsert a body naming an owner of its own: %v", err)
	}
	if got := value(t, exchangeRow(t, d, "binding", `["b1"]`), "origin"); got != "instX" {
		t.Fatalf("origin = %v, want the owner the caller resolved rather than the body's", got)
	}

	// Writing it again under the same owner changes nothing, and the caller's
	// map is not rewritten behind its back either.
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert("binding", "instX", body) }); err != nil {
		t.Fatalf("upsert the binding again: %v", err)
	}
	if got := value(t, exchangeRow(t, d, "binding", `["b1"]`), "origin"); got != "instX" {
		t.Fatalf("origin = %v, want the owner's own value", got)
	}
	if body["origin"] != "thief" {
		t.Fatalf("the caller's body now holds origin %v, want it left as it was", body["origin"])
	}

	// A child names no owner of its own: it resolves through its parent, so the
	// owner rule for it has no column and the caller's owner reaches no column
	// of the row at all.
	if err := d.Tx(func(tx *Tx) error {
		if err := tx.ExchangeUpsert("binding_record", "instX", map[string]any{
			"id": "r1", "owner": "o", "name": "n", "state": "open", "round": 1,
			"cwd": "/z", "record_json": "{}", "created_at": "t", "updated_at": "t",
		}); err != nil {
			return err
		}
		return tx.ExchangeUpsert("round_file", "instX", map[string]any{
			"record_id": "r1", "name": "f", "round": 1, "body": []byte{0, 1},
			"bytes": 2, "sha256": "s", "mtime": "t", "sealed_at": "t",
		})
	}); err != nil {
		t.Fatalf("upsert a child row: %v", err)
	}
	if got := value(t, exchangeRow(t, d, "binding_record", `["r1"]`), "origin"); got != "instX" {
		t.Fatalf("the record's origin = %v, want the owner the caller resolved", got)
	}
	for _, col := range exchangeRow(t, d, "round_file", `["r1","f"]`).Columns {
		if col.Name == "origin" {
			t.Fatal("the child row grew an origin column, want its owner to resolve through the parent")
		}
	}
	if ownerColumn("round_file") != "" || ownerColumn("binding") != "origin" {
		t.Fatalf("the owner columns are %q and %q, want none for the child and origin for the root",
			ownerColumn("round_file"), ownerColumn("binding"))
	}
}

// A mark only ever moves forward. A sequence at or below the one already stored
// is not written at all, so a replayed batch, an out-of-order pull, or a
// transport answering with a number behind this machine's own mark cannot rewind
// what has been applied.
func TestSetImportMarkNeverMovesBackward(t *testing.T) {
	d := openTestDB(t)
	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 9) }); err != nil {
		t.Fatalf("set the mark: %v", err)
	}
	for _, seq := range []int{4, 9} {
		if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", seq) }); err != nil {
			t.Fatalf("set the mark to %d: %v", seq, err)
		}
		if have, found, err := d.ImportMark("instX"); err != nil || !found || have != 9 {
			t.Fatalf("ImportMark after setting %d = (%d, %t, %v), want it resting at 9", seq, have, found, err)
		}
	}
	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 11) }); err != nil {
		t.Fatalf("move the mark forward: %v", err)
	}
	if seq, _, err := d.ImportMark("instX"); err != nil || seq != 11 {
		t.Fatalf("ImportMark = %d (%v), want it moved forward", seq, err)
	}
}

// A delete names one row by its key and leaves everything else, and a row that
// is already gone is not a failure: the delete has to be safe to re-apply after
// a mark that did not survive a restore.
func TestExchangeDeleteRemovesOneRowAndIsIdempotent(t *testing.T) {
	d := openTestDB(t)
	if err := d.Tx(func(tx *Tx) error {
		return tx.ExchangeUpsert("binding", "instX", map[string]any{
			"id": "b1", "name": "b1", "cwd": "/x",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "instX",
		})
	}); err != nil {
		t.Fatalf("upsert the binding: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := d.Tx(func(tx *Tx) error { return tx.ExchangeDelete("binding", `["b1"]`) }); err != nil {
			t.Fatalf("delete, pass %d: %v", i+1, err)
		}
	}
	if _, found, err := d.ReadExchangeRow("binding", `["b1"]`); err != nil || found {
		t.Fatalf("the binding is still there (found=%t, %v), want it removed", found, err)
	}

	// A key that is not this table's shape names no row to remove, so it is
	// refused rather than read as a prefix of a longer key.
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeDelete("binding", `["b1","extra"]`) }); err == nil {
		t.Fatal("delete with a key of the wrong shape succeeded, want it refused")
	}
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeDelete("schema_version", `["1"]`) }); err == nil {
		t.Fatal("delete from a table that is not shared succeeded, want it refused")
	}
}

// The marks map is what a pull is asked with: every origin this file has applied
// from, each at how far. An origin absent from the map is one read from the
// start of its log, which is the same as a mark of zero.
func TestImportMarksReportsEveryOrigin(t *testing.T) {
	d := openTestDB(t)
	marks, err := d.ImportMarks()
	if err != nil {
		t.Fatalf("ImportMarks on a fresh file: %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("a fresh file reports %v, want no marks", marks)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 5) }); err != nil {
		t.Fatalf("set the first mark: %v", err)
	}
	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instY", 12) }); err != nil {
		t.Fatalf("set the second mark: %v", err)
	}
	marks, err = d.ImportMarks()
	if err != nil {
		t.Fatalf("ImportMarks: %v", err)
	}
	if marks["instX"] != 5 || marks["instY"] != 12 {
		t.Fatalf("marks = %v, want instX at 5 and instY at 12", marks)
	}

	// Moving one origin's mark leaves the other's where it was.
	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 9) }); err != nil {
		t.Fatalf("move the first mark: %v", err)
	}
	marks, err = d.ImportMarks()
	if err != nil {
		t.Fatalf("ImportMarks after the move: %v", err)
	}
	if marks["instX"] != 9 || marks["instY"] != 12 {
		t.Fatalf("marks = %v, want instX at 9 and instY untouched at 12", marks)
	}
}

// The statement builders are pure text, and what they produce is the claim this
// package makes about what a body can reach: fixed column names from the
// schema, a conflict target on the key, and no replacement.
func TestUpsertStatementNeverReplaces(t *testing.T) {
	got := upsertStatement("binding", []string{"id", "cwd", "name"}, []string{"id"})
	want := `INSERT INTO "binding" ("id", "cwd", "name") VALUES (?, ?, ?)` +
		` ON CONFLICT("id") DO UPDATE SET "cwd" = excluded."cwd", "name" = excluded."name"`
	if got != want {
		t.Fatalf("upsertStatement =\n%s\nwant\n%s", got, want)
	}

	// A body carrying nothing but the key updates nothing rather than replacing
	// the row, which is the failure mode the conflict clause exists to avoid.
	if got := upsertStatement("binding", []string{"id"}, []string{"id"}); got != want2() {
		t.Fatalf("upsertStatement for a key-only body = %s, want the row left alone", got)
	}
	if where, args := keyWhere([]string{"id"}, []any{"b1"}); where != `"id" = ?` || len(args) != 1 {
		t.Fatalf("keyWhere = (%q, %v), want the key bound as a parameter", where, args)
	}
	if got := placeholders(0); got != "" {
		t.Fatalf("placeholders(0) = %q, want none", got)
	}
	if got := placeholders(2); got != "?, ?" {
		t.Fatalf("placeholders(2) = %q, want two bound slots", got)
	}
	if got := joinQuoted([]string{"a", `b"c`}); got != `"a", "b""c"` {
		t.Fatalf("joinQuoted = %q, want every name quoted", got)
	}
}

func want2() string {
	return `INSERT INTO "binding" ("id") VALUES (?) ON CONFLICT("id") DO NOTHING`
}

// The append-only exception to the upsert, on both tables. An id the file does
// not hold is inserted; an id it holds with the same body is a replay and
// changes nothing; an id it holds with one column changed is refused rather
// than rewritten. The refusal is ErrInvalid, which is what puts the batch on the
// importer's drop path with the mark moving past it.
func TestExchangeUpsertNeverRewritesAnAppendOnlyRow(t *testing.T) {
	for _, tbl := range AppendOnlyTables {
		t.Run(tbl, func(t *testing.T) {
			d := openTestDB(t)
			body := appendOnlyBody(tbl, "01A", "instX")

			if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert(tbl, "instX", body) }); err != nil {
				t.Fatalf("upsert a new %s row: %v", tbl, err)
			}
			if queued := queryStrings(t, d, `SELECT tbl FROM sync_outbox WHERE tbl = ?`, tbl); len(queued) != 1 {
				t.Fatalf("outbox holds %d entries after the insert, want the one the insert recorded", len(queued))
			}

			// The same body again is the replay a lost mark makes: handled, and
			// writing nothing, so it records no second outbox row.
			replay := appendOnlyBody(tbl, "01A", "instX")
			if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert(tbl, "instX", replay) }); err != nil {
				t.Fatalf("upsert an identical %s body: %v", tbl, err)
			}
			if queued := queryStrings(t, d, `SELECT tbl FROM sync_outbox WHERE tbl = ?`, tbl); len(queued) != 1 {
				t.Fatalf("outbox holds %d entries after an identical replay, want still one", len(queued))
			}
			if got := value(t, exchangeRow(t, d, tbl, `["01A"]`), appendOnlyNoteColumn(tbl)); got != appendOnlyNote(tbl) {
				t.Fatalf("the replayed row's note = %v, want the row untouched", got)
			}

			// One column different is not the same row: it is refused, and the
			// row keeps what the file already held.
			changed := appendOnlyBody(tbl, "01A", "instX")
			changed[appendOnlyNoteColumn(tbl)] = "rewritten"
			err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert(tbl, "instX", changed) })
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("upsert of a differing %s body = %v, want an ErrInvalid refusal", tbl, err)
			}
			if got := value(t, exchangeRow(t, d, tbl, `["01A"]`), appendOnlyNoteColumn(tbl)); got != appendOnlyNote(tbl) {
				t.Fatalf("the row's note = %v after a refused upsert, want it unwritten", got)
			}
			if queued := queryStrings(t, d, `SELECT tbl FROM sync_outbox WHERE tbl = ?`, tbl); len(queued) != 1 {
				t.Fatalf("outbox holds %d entries after a refused upsert, want still one", len(queued))
			}
		})
	}
}

// A table that is not append-only is untouched by the rule: the same three
// bodies update in place exactly as they did before it, because a row that is
// not immutable is the whole reason the conflict clause updates.
func TestExchangeUpsertStillUpdatesAMutableRow(t *testing.T) {
	d := openTestDB(t)
	first := map[string]any{
		"id": "b1", "name": "b1", "cwd": "/first", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "instX",
	}
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert("binding", "instX", first) }); err != nil {
		t.Fatalf("insert the binding: %v", err)
	}
	second := map[string]any{
		"id": "b1", "name": "b1", "cwd": "/second", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "instX",
	}
	if err := d.Tx(func(tx *Tx) error { return tx.ExchangeUpsert("binding", "instX", second) }); err != nil {
		t.Fatalf("update the binding: %v", err)
	}
	if got := value(t, exchangeRow(t, d, "binding", `["b1"]`), "cwd"); got != "/second" {
		t.Fatalf("binding cwd = %v, want the update applied", got)
	}
}

// appendOnlyBody is one row of one of the two append-only tables as a body
// carries it: every column, so a replay is compared in full rather than on the
// key alone.
func appendOnlyBody(tbl, id, owner string) map[string]any {
	if tbl == AvailabilityEventTable {
		return map[string]any{
			"id": id, "at_ms": int64(1757000000000), "kind": "rate_limited",
			"provider": "test", "account": "", "token": "", "source": "relevo",
			"binding": "", "note": appendOnlyNote(tbl), "origin": owner,
		}
	}
	return map[string]any{
		"id": id, "at_ms": int64(1757000000000), "token": "opencode/test/m",
		"host": "laptop", "ttft_ms": int64(120), "total_ms": int64(900),
		"err": appendOnlyNote(tbl), "origin": owner,
	}
}

// appendOnlyNoteColumn is the free-text column of one of the two tables, which
// is the one a rewriting peer would most plausibly change and the one a case
// changes to prove the row was compared rather than only its key.
func appendOnlyNoteColumn(tbl string) string {
	if tbl == AvailabilityEventTable {
		return "note"
	}
	return "err"
}

func appendOnlyNote(tbl string) string {
	if tbl == AvailabilityEventTable {
		return "the original note"
	}
	return "the original error"
}
