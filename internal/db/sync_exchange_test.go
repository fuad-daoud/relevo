package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportMarkTableIsLocalOnly pins that the import mark is about this
// machine's progress rather than about shared history: no trigger names it, and
// a write to it records nothing in the outbox. A mark that travelled would let
// another machine believe the entries between the two marks were applied.
func TestImportMarkTableIsLocalOnly(t *testing.T) {
	sqlDB := migratedSQLDB(t)
	mark := seedRoots(t, sqlDB)

	bodies := triggerBodies(t, sqlDB)
	for _, op := range outboxOps {
		if name := "sync_outbox_sync_import_mark" + op.Suffix; bodies[name] != "" {
			t.Errorf("%s exists; a mark must never travel", name)
		}
	}

	if _, err := sqlDB.Exec(`INSERT INTO sync_import_mark (origin, seq) VALUES ('instX', 7)`); err != nil {
		t.Fatalf("write sync_import_mark: %v", err)
	}
	wantEntries(t, sqlDB, mark)

	var recorded int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&recorded); err != nil {
		t.Fatalf("count sync_outbox: %v", err)
	}
	if recorded != mark {
		t.Errorf("sync_outbox holds %d rows, want the %d the fixture wrote", recorded, mark)
	}
}

// TestImportMarkRoundTrip pins the mark's whole life: an origin with no mark
// reads as absent rather than as zero, a mark written reads back, and a second
// write for the same origin replaces the first in place rather than adding a
// row, because one origin has one mark.
func TestImportMarkRoundTrip(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	if seq, found, err := sqlImportMark(t, sqlDB, "instX"); err != nil {
		t.Fatalf("read a mark that was never written: %v", err)
	} else if found {
		t.Errorf("an origin with no mark reads seq %d, want absent", seq)
	}

	for _, seq := range []int{3, 11} {
		if _, err := sqlDB.Exec(`INSERT OR REPLACE INTO sync_import_mark (origin, seq) VALUES (?, ?)`,
			"instX", seq); err != nil {
			t.Fatalf("write mark %d: %v", seq, err)
		}
		got, found, err := sqlImportMark(t, sqlDB, "instX")
		if err != nil {
			t.Fatalf("read mark %d: %v", seq, err)
		}
		if !found || got != seq {
			t.Errorf("mark reads (%d, %t), want (%d, true)", got, found, seq)
		}
	}

	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_import_mark`).Scan(&rows); err != nil {
		t.Fatalf("count sync_import_mark: %v", err)
	}
	if rows != 1 {
		t.Errorf("sync_import_mark holds %d rows for one origin, want 1", rows)
	}

	if _, err := sqlDB.Exec(`INSERT INTO sync_import_mark (origin, seq) VALUES (?, ?)`, "instY", 4); err != nil {
		t.Fatalf("write a second origin's mark: %v", err)
	}
	if got, found, err := sqlImportMark(t, sqlDB, "instX"); err != nil || !found || got != 11 {
		t.Errorf("the first origin's mark reads (%d, %t, %v) after another origin wrote, want (11, true, nil)", got, found, err)
	}
}

// sqlImportMark reads one origin's mark straight from the file, so a test can
// check a mark against the schema rather than against the seam that writes it.
func sqlImportMark(t *testing.T, sqlDB *sql.DB, origin string) (int, bool, error) {
	t.Helper()
	var seq int
	err := sqlDB.QueryRow(`SELECT seq FROM sync_import_mark WHERE origin = ?`, origin).Scan(&seq)
	switch {
	case err == sql.ErrNoRows:
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	return seq, true, nil
}

// TestImportMarkThroughTheSeam pins that the mark methods the importer calls
// read and write the table the migration created: a mark set through the Tx is
// visible to the *DB read, and a mark set on one origin leaves another's alone.
func TestImportMarkThroughTheSeam(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, found, err := d.ImportMark("instX"); err != nil {
		t.Fatalf("ImportMark on a fresh file: %v", err)
	} else if found {
		t.Error("a fresh file reports a mark for an origin that never wrote one")
	}

	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 5) }); err != nil {
		t.Fatalf("set mark inside a Tx: %v", err)
	}
	seq, found, err := d.ImportMark("instX")
	if err != nil {
		t.Fatalf("ImportMark: %v", err)
	}
	if !found || seq != 5 {
		t.Errorf("ImportMark reads (%d, %t), want (5, true)", seq, found)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 9) }); err != nil {
		t.Fatalf("move the mark inside a Tx: %v", err)
	}
	var rows int
	if err := d.Tx(func(tx *Tx) error {
		seq, found, err := tx.ImportMark("instX")
		if err != nil {
			return err
		}
		if !found || seq != 9 {
			t.Errorf("the Tx reads (%d, %t), want (9, true)", seq, found)
		}
		return tx.queryRow(`SELECT COUNT(*) FROM sync_import_mark`).Scan(&rows)
	}); err != nil {
		t.Fatalf("read the mark back inside the Tx that wrote it: %v", err)
	}
	if rows != 1 {
		t.Errorf("sync_import_mark holds %d rows after two writes for one origin, want 1", rows)
	}
}

// seededSharedKeys is one seeded row per shared table, each with its primary key
// in the outbox's json_array spelling. It covers every entry of SharedTables, so
// a table added to that list without a row here is a compile-time mismatch rather
// than a silent gap in what the exchange tests read.
var seededSharedKeys = []struct {
	tbl string
	pk  string
}{
	{"repo", `["r1"]`},
	{"mastermind", `["m1"]`},
	{"binding_record", `["rec1"]`},
	{"installation", `["instI"]`},
	{"binding", `["b1"]`},
	{"chains", `["c1"]`},
	{"binding_event", `["rec1",1]`},
	{"round_file", `["rec1","report.md"]`},
	{"chain_event", `["c1",1]`},
	{"chain_member", `["c1","b1"]`},
	{"chain_check", `["c1",1]`},
	{"round", `["rd1"]`},
	{"event", `["e1"]`},
	{"artifact", `["a1"]`},
	{"transcript", `["t1"]`},
}

// sharedRowFixture writes the row each entry of seededSharedKeys names, with BLOB
// columns whose bytes are not text, so a row read has to return the stored bytes
// rather than a rendering of them.
func sharedRowFixture(t *testing.T, d *DB) map[string]string {
	t.Helper()
	if len(seededSharedKeys) != len(SharedTables) {
		t.Fatalf("the fixture seeds %d rows for %d shared tables", len(seededSharedKeys), len(SharedTables))
	}
	if err := d.Tx(func(t *Tx) error {
		return execTxAll(t, seedSharedRows()...)
	}); err != nil {
		t.Fatalf("seed one row per shared table: %v", err)
	}
	keys := make(map[string]string, len(seededSharedKeys))
	for _, k := range seededSharedKeys {
		keys[k.tbl] = k.pk
	}
	return keys
}

// execTxAll runs each statement inside the transaction, so a failing one names
// itself and the whole fixture leaves nothing behind.
func execTxAll(t *Tx, stmts ...string) error {
	for _, stmt := range stmts {
		if _, err := t.exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// seedSharedRows writes the rows the fixture needs, parents before children so
// the foreign keys accept them. The three BLOB columns hold bytes that are not
// valid UTF-8, which is what a compressed body actually holds.
func seedSharedRows() []string {
	return []string{
		`INSERT INTO repo (id, origin_url, common_dir, first_seen, origin) VALUES ('r1', 'u', '/c', 't', 'instP')`,
		`INSERT INTO "mastermind" (id, harness_kind, session_id, first_seen, last_seen, origin) VALUES ('m1', 'agy', 's', 't', 't', 'instM')`,
		`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin) VALUES ('b1', 'n', '/x', 'headless', 't', '', 'instB')`,
		`INSERT INTO installation (id, label, first_seen, last_seen) VALUES ('instI', 'self', 't', 't')`,
		`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin)
		 VALUES ('rec1', 'o', 'n', 'live', 1, '/x', '{}', 't', 't', 'instR')`,
		`INSERT INTO chains (id, name, status, phase, step, plan, plans, plan_paths, builder, created_at, updated_at, origin)
		 VALUES ('c1', 'n', 'open', 'p', 's', 1, 1, '[]', 'b', 't', 't', 'instC')`,
		`INSERT INTO binding_event (record_id, seq, ts, round, direction, kind, confirmed, entry_json)
		 VALUES ('rec1', 1, 't', 1, 'in', 'k', 1, '{}')`,
		`INSERT INTO round_file (record_id, name, round, body, body_codec, bytes, sha256, mtime, sealed_at)
		 VALUES ('rec1', 'report.md', 1, X'0028B80BFDFFFF', 1, 5, 'sha', 't', 't')`,
		`INSERT INTO chain_event (chain_id, seq, ts, phase, step, member, round, event, action)
		 VALUES ('c1', 1, 't', 'p', 's', 'm', 1, '{}', '{}')`,
		`INSERT INTO chain_member (chain_id, binding, actor, seq) VALUES ('c1', 'b1', 'a', 1)`,
		`INSERT INTO chain_check (chain_id, run, step, visit, command, created_at) VALUES ('c1', 1, 's', 1, 'cmd', 't')`,
		`INSERT INTO round (id, binding_id, number, started_at, outcome, switches) VALUES ('rd1', 'b1', 1, 't', 'green', 0)`,
		`INSERT INTO event (id, binding_id, round_id, seq, ts, kind, direction, confirmed, late, entry_json)
		 VALUES ('e1', 'b1', 'rd1', 1, 't', 'k', 'in', 1, 0, '{}')`,
		`INSERT INTO artifact (id, round_id, kind, consult_id, text, bytes, sha256, captured_at)
		 VALUES ('a1', 'rd1', 'report', '', 'x', 1, 'sha', 't')`,
		`INSERT INTO transcript (id, owner_kind, owner_id, seq, record_json, record_json_codec, rendered, rendered_codec)
		 VALUES ('t1', 'round', 'rd1', 1, X'7B7DFF', 1, X'0A1B', 1)`,
	}
}

// TestOutboxDrainIsOrderedAndDeletable pins the drain's two halves: the entries
// come back in the order the writes happened in rather than in the order the
// tables are walked, and the delete removes exactly the entries a drain covered
// and nothing that arrived after it.
func TestOutboxDrainIsOrderedAndDeletable(t *testing.T) {
	d := openTestDB(t)
	sharedRowFixture(t, d)

	drained, err := d.DrainOutbox(3)
	if err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	if len(drained) != 3 {
		t.Fatalf("drained %d entries, want 3", len(drained))
	}
	for i := 1; i < len(drained); i++ {
		if drained[i-1].Seq >= drained[i].Seq {
			t.Errorf("entries %d and %d have seq %d then %d, want increasing",
				i-1, i, drained[i-1].Seq, drained[i].Seq)
		}
	}
	if drained[0].Table != "repo" {
		t.Errorf("the first entry names %s, want repo: the seed wrote it first", drained[0].Table)
	}

	// A write after the drain must not be swept up by the delete that follows
	// it: the delete cuts by the highest seq the drain returned.
	if err := d.Tx(func(tx *Tx) error {
		_, err := tx.exec(`UPDATE repo SET origin_url = 'u2' WHERE id = 'r1'`)
		return err
	}); err != nil {
		t.Fatalf("write between drains: %v", err)
	}
	if err := d.DeleteDrainedOutbox(drained[len(drained)-1].Seq); err != nil {
		t.Fatalf("DeleteDrainedOutbox: %v", err)
	}

	rest, err := d.DrainOutbox(1000)
	if err != nil {
		t.Fatalf("DrainOutbox after the delete: %v", err)
	}
	if len(rest) == 0 {
		t.Fatal("the delete removed the write that arrived after the drain")
	}
	if rest[0].Seq <= drained[len(drained)-1].Seq {
		t.Errorf("the next drain starts at seq %d, want past %d", rest[0].Seq, drained[len(drained)-1].Seq)
	}
}

// TestOutboxDrainIsOneSnapshot pins that a drain reads the outbox and the rows
// it names from one transaction. A write that lands between two drains must show
// up whole in the second and not at all in the first: a drain that read the
// entries in one transaction and the rows in another would report the row's
// state after a write the entries it drained predates.
func TestOutboxDrainIsOneSnapshot(t *testing.T) {
	d := openTestDB(t)
	sharedRowFixture(t, d)

	if err := d.Tx(func(tx *Tx) error {
		_, err := tx.exec(`UPDATE repo SET origin_url = 'before' WHERE id = 'r1'`)
		return err
	}); err != nil {
		t.Fatalf("write before the first drain: %v", err)
	}

	first, err := d.DrainOutbox(1000)
	if err != nil {
		t.Fatalf("first DrainOutbox: %v", err)
	}
	firstRepo := findDrained(t, first, "repo", `["r1"]`)
	if got := exchangeColumn(t, *firstRepo.Row, "origin_url"); got != "before" {
		t.Errorf("the first drain reads origin_url %v, want before", got)
	}

	// The write lands between the drains. It both changes the row the first
	// drain read and appends an entry the first drain did not return.
	if err := d.Tx(func(tx *Tx) error {
		_, err := tx.exec(`UPDATE repo SET origin_url = 'after' WHERE id = 'r1'`)
		return err
	}); err != nil {
		t.Fatalf("write between drains: %v", err)
	}

	if err := d.DeleteDrainedOutbox(first[len(first)-1].Seq); err != nil {
		t.Fatalf("delete what the first drain drained: %v", err)
	}
	second, err := d.DrainOutbox(1000)
	if err != nil {
		t.Fatalf("second DrainOutbox: %v", err)
	}

	secondRepo := findDrained(t, second, "repo", `["r1"]`)
	if got := exchangeColumn(t, *secondRepo.Row, "origin_url"); got != "after" {
		t.Errorf("the second drain reads origin_url %v, want after: the row's state moved with the write", got)
	}
	if len(second) != 1 {
		t.Errorf("the second drain returned %d entries, want 1: the write between drains is the only new entry", len(second))
	}
	for _, e := range first {
		if e.Seq >= second[0].Seq {
			t.Errorf("the first drain returned seq %d, which the second also returned", e.Seq)
		}
	}
}

// TestExchangeRowReadCoversSharedTables pins that every shared table can be read
// by primary key: the row comes back with every column the schema declares, in
// schema order, and each value in the type the file holds it as. A compressed
// body read as its decompressed rendering would let a re-export write bytes the
// other machine never held.
func TestExchangeRowReadCoversSharedTables(t *testing.T) {
	d := openTestDB(t)
	keys := sharedRowFixture(t, d)

	t.Run("every shared table reads its seeded row in schema order", func(t *testing.T) {
		for _, table := range SharedTables {
			wantSchemaColumns(t, d, table, keys[table.Name])
		}
	})

	t.Run("a compressed body reads as its stored bytes", func(t *testing.T) {
		wantBytes(t, d, "round_file", `["rec1","report.md"]`, "body", []byte{0x00, 0x28, 0xB8, 0x0B, 0xFD, 0xFF, 0xFF})
		wantInteger(t, d, "round_file", `["rec1","report.md"]`, "body_codec", 1)

		wantBytes(t, d, "transcript", `["t1"]`, "record_json", []byte{0x7B, 0x7D, 0xFF})
		wantBytes(t, d, "transcript", `["t1"]`, "rendered", []byte{0x0A, 0x1B})
		wantInteger(t, d, "transcript", `["t1"]`, "record_json_codec", 1)
		wantInteger(t, d, "transcript", `["t1"]`, "rendered_codec", 1)
	})

	t.Run("an integer stays an integer and a NULL stays a NULL", func(t *testing.T) {
		// A NULL has to stay distinguishable from an empty string: the two mean
		// different things to a writer replaying the row.
		wantInteger(t, d, "chains", `["c1"]`, "plan", 1)
		wantValue(t, d, "binding", `["b1"]`, "archived_at", nil)
	})

	t.Run("a key that names no row or is not a shared key is refused", func(t *testing.T) {
		if _, found, err := d.ReadExchangeRow("repo", `["nope"]`); err != nil {
			t.Errorf("read a key with no row: %v", err)
		} else if found {
			t.Error("a key with no row reads as present")
		}
		if _, _, err := d.ReadExchangeRow("kv", `["probe"]`); !errors.Is(err, ErrInvalid) {
			t.Errorf("reading a table that does not share gives %v, want ErrInvalid", err)
		}
		if _, _, err := d.ReadExchangeRow("repo", `not json`); !errors.Is(err, ErrInvalid) {
			t.Errorf("reading a key that is not JSON gives %v, want ErrInvalid", err)
		}
		if _, _, err := d.ReadExchangeRow("chain_event", `["c1"]`); !errors.Is(err, ErrInvalid) {
			t.Errorf("a key with too few values gives %v, want ErrInvalid", err)
		}
	})
}

// wantSchemaColumns asserts that reading a shared table's seeded row returns
// exactly the columns the schema declares, in the order it declares them. A read
// that named its own columns instead would drop a column a migration had added
// and would export the row without it.
func wantSchemaColumns(t *testing.T, d *DB, table SharedTable, pk string) {
	t.Helper()
	row, found, err := d.ReadExchangeRow(table.Name, pk)
	if err != nil {
		t.Errorf("read %s %s: %v", table.Name, pk, err)
		return
	}
	if !found {
		t.Errorf("%s %s reads as absent, want the seeded row", table.Name, pk)
		return
	}
	want := tableColumnNames(t, d, table.Name)
	if len(row.Columns) != len(want) {
		t.Errorf("%s returns %d columns, want the schema's %d", table.Name, len(row.Columns), len(want))
		return
	}
	for i, col := range row.Columns {
		if col.Name != want[i] {
			t.Errorf("%s column %d is %s, want %s: the read must follow schema order", table.Name, i, col.Name, want[i])
		}
	}
}

// wantBytes asserts that one column reads back as exactly these bytes, which is
// what a BLOB column holds on disk.
func wantBytes(t *testing.T, d *DB, tbl, pk, column string, want []byte) {
	t.Helper()
	got := readExchangeColumn(t, d, tbl, pk, column)
	blob, isBlob := got.([]byte)
	if !isBlob {
		t.Errorf("%s.%s reads %#v of type %T, want the stored bytes %v", tbl, column, got, got, want)
		return
	}
	if !bytes.Equal(blob, want) {
		t.Errorf("%s.%s reads %#v, want the stored bytes %#v", tbl, column, blob, want)
	}
}

// wantInteger asserts that one column reads back as an integer rather than as
// text or a float, so a consumer can tell a count from a label.
func wantInteger(t *testing.T, d *DB, tbl, pk, column string, want int64) {
	t.Helper()
	got := readExchangeColumn(t, d, tbl, pk, column)
	if got != want {
		t.Errorf("%s.%s reads %#v of type %T, want the integer %d", tbl, column, got, got, want)
	}
}

// wantValue asserts that one column reads back as exactly this value, which is
// how a NULL is pinned: nil rather than an empty string.
func wantValue(t *testing.T, d *DB, tbl, pk, column string, want any) {
	t.Helper()
	if got := readExchangeColumn(t, d, tbl, pk, column); got != want {
		t.Errorf("%s.%s reads %#v, want %#v", tbl, column, got, want)
	}
}

// readExchangeColumn reads one row and returns one of its columns' values.
func readExchangeColumn(t *testing.T, d *DB, tbl, pk, column string) any {
	t.Helper()
	row, found, err := d.ReadExchangeRow(tbl, pk)
	if err != nil {
		t.Fatalf("read %s %s: %v", tbl, pk, err)
	}
	if !found {
		t.Fatalf("%s %s reads as absent, want the seeded row", tbl, pk)
	}
	return exchangeColumn(t, row, column)
}

// findDrained returns the entry naming tbl and pk, and fails the test when no
// entry does.
func findDrained(t *testing.T, entries []DrainedEntry, tbl, pk string) *DrainedEntry {
	t.Helper()
	for i := range entries {
		if entries[i].Table == tbl && entries[i].PK == pk {
			return &entries[i]
		}
	}
	t.Fatalf("no drained entry names %s %s", tbl, pk)
	return nil
}

// exchangeColumn returns one column's value, failing the test when the column is
// not there, so a test states which value it means.
func exchangeColumn(t *testing.T, row ExchangeRow, name string) any {
	t.Helper()
	for _, c := range row.Columns {
		if c.Name == name {
			return c.Value
		}
	}
	t.Fatalf("%s has no column %s", row.Table, name)
	return nil
}

// tableColumnNames returns the column names pragma_table_info reports for tbl,
// which is the schema the row read has to follow.
func tableColumnNames(t *testing.T, d *DB, tbl string) []string {
	t.Helper()
	var names []string
	err := d.Tx(func(tx *Tx) error {
		rows, err := tx.conn.QueryContext(tx.ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, tbl)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			name, err := scanString(rows)
			if err != nil {
				return err
			}
			names = append(names, name)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read the columns of %s: %v", tbl, err)
	}
	return names
}

// TestOwnerResolutionMatchesTriggers pins the Go owner rules against the live
// trigger bodies. The triggers are the resolution SQL itself and are immutable
// once applied, so a rule that names a different parent, a different key or a
// different column than its trigger would attribute a child's rows to the wrong
// installation. Comparing against the applied body rather than against another
// hand-written copy is what keeps the two from drifting apart.
func TestOwnerResolutionMatchesTriggers(t *testing.T) {
	sqlDB := migratedSQLDB(t)
	bodies := triggerBodies(t, sqlDB)

	for _, table := range SharedTables {
		rule, ok := OwnerRules[table.Name]
		if !ok {
			t.Errorf("OwnerRules has no rule for the shared table %s", table.Name)
			continue
		}
		for _, op := range outboxOps {
			alias := "NEW"
			if op.Name == "delete" {
				alias = "OLD"
			}
			name := "sync_outbox_" + table.Name + op.Suffix
			body, ok := bodies[name]
			if !ok {
				t.Errorf("%s is missing", name)
				continue
			}
			got, ok := triggerOwnerExpr(body)
			if !ok {
				t.Errorf("%s writes no origin expression", name)
				continue
			}
			want := tightSQL(ownerExpression(rule, alias))
			if got != want {
				t.Errorf("%s resolves the owner as\n  %s\nwant\n  %s", name, got, want)
			}
		}
	}

	if len(OwnerRules) != len(SharedTables) {
		t.Errorf("OwnerRules holds %d rules for %d shared tables", len(OwnerRules), len(SharedTables))
	}
}

// TestResolveOwnerAttributesEachRowToItsRoot pins that the rules resolve against
// the rows themselves, not just that they read like the triggers: every child
// resolves to the origin of the root that owns it. The origins are distinct
// precisely so that reading an owner from the wrong table is visible here.
func TestResolveOwnerAttributesEachRowToItsRoot(t *testing.T) {
	d := openTestDB(t)
	sharedRowFixture(t, d)

	for _, key := range seededSharedKeys {
		owner, found, err := d.ResolveOwner(key.tbl, key.pk)
		if err != nil {
			t.Errorf("resolve the owner of %s %s: %v", key.tbl, key.pk, err)
			continue
		}
		if !found {
			t.Errorf("%s %s resolves to no owner, want the origin its parent carries", key.tbl, key.pk)
			continue
		}
		if !strings.HasPrefix(owner, "inst") {
			t.Errorf("%s %s resolves to %q, which is not an installation id", key.tbl, key.pk, owner)
		}
	}

	for _, c := range []struct{ tbl, pk, want string }{
		{"binding_event", `["rec1",1]`, bindingRecordOrigin},
		{"round_file", `["rec1","report.md"]`, bindingRecordOrigin},
		{"chain_event", `["c1",1]`, chainsOrigin},
		{"chain_member", `["c1","b1"]`, chainsOrigin},
		{"chain_check", `["c1",1]`, chainsOrigin},
		{"round", `["rd1"]`, bindingOrigin},
		{"event", `["e1"]`, bindingOrigin},
		{"artifact", `["a1"]`, bindingOrigin},
		{"transcript", `["t1"]`, bindingOrigin},
		{"repo", `["r1"]`, repoOrigin},
		{"mastermind", `["m1"]`, mastermindOrigin},
		{"binding_record", `["rec1"]`, bindingRecordOrigin},
		{"binding", `["b1"]`, bindingOrigin},
		{"chains", `["c1"]`, chainsOrigin},
		// An installation's id is its own installation id, so its row names
		// itself rather than reading an origin column.
		{"installation", `["instI"]`, installationID},
	} {
		got, found, err := d.ResolveOwner(c.tbl, c.pk)
		if err != nil || !found || got != c.want {
			t.Errorf("%s %s resolves to (%q, %t, %v), want (%s, true, nil)", c.tbl, c.pk, got, found, err, c.want)
		}
	}
}

// TestResolveOwnerRefusesToGuess pins the two cases where resolution has no
// answer and must say so rather than pick the nearest plausible root: an
// owner_kind this build does not write, and a row whose parent is already gone.
// A transcript has no foreign key on its owner_id, so it is the one shared table
// a row can outlive its parent in, and that row must read as unowned rather than
// as owned by whichever root happens to share the id.
func TestResolveOwnerRefusesToGuess(t *testing.T) {
	d := openTestDB(t)
	sharedRowFixture(t, d)

	writeTranscript(t, d, "t2", "mastermind", "m1")
	if got, found, err := d.ResolveOwner("transcript", `["t2"]`); err != nil || !found || got != mastermindOrigin {
		t.Errorf("a mastermind transcript resolves to (%q, %t, %v), want (%s, true, nil)", got, found, err, mastermindOrigin)
	}

	writeTranscript(t, d, "t3", "unknown_kind", "m1")
	if _, found, err := d.ResolveOwner("transcript", `["t3"]`); err != nil || found {
		t.Errorf("a transcript of an unknown kind resolves to (found=%t, %v), want (false, nil)", found, err)
	}

	writeTranscript(t, d, "t4", "round", "no_such_round")
	if _, found, err := d.ResolveOwner("transcript", `["t4"]`); err != nil || found {
		t.Errorf("a row whose parent is gone resolves to (found=%t, %v), want (false, nil)", found, err)
	}

	if _, found, err := d.ResolveOwner("repo", `["nope"]`); err != nil || found {
		t.Errorf("an absent row resolves to (found=%t, %v), want (false, nil)", found, err)
	}
	if _, _, err := d.ResolveOwner("kv", `["probe"]`); !errors.Is(err, ErrInvalid) {
		t.Errorf("resolving a table that does not share gives %v, want ErrInvalid", err)
	}
}

// writeTranscript adds one transcript row of the given owner kind.
func writeTranscript(t *testing.T, d *DB, id, kind, ownerID string) {
	t.Helper()
	err := d.Tx(func(tx *Tx) error {
		_, err := tx.exec(`INSERT INTO transcript (id, owner_kind, owner_id, seq, record_json, rendered)
			VALUES (?, ?, ?, 1, '{}', '')`, id, kind, ownerID)
		return err
	})
	if err != nil {
		t.Fatalf("write transcript %s of kind %s: %v", id, kind, err)
	}
}

// triggerOwnerExpr returns the origin expression a trigger body writes: the
// fourth value of its INSERT, which is where the resolved installation goes.
// Spaces are dropped first because the engine rewrites a call's open paren with
// one before it, so the text a trigger holds is not the text migration 022 wrote.
func triggerOwnerExpr(body string) (string, bool) {
	tight := strings.ReplaceAll(body, " ", "")
	open := strings.Index(tight, "VALUES(")
	if open < 0 {
		return "", false
	}
	rest := tight[open+len("VALUES("):]

	var fields []string
	depth, start := 0, 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				fields = append(fields, rest[start:i])
				return exprField(fields, len(fields))
			}
			depth--
		case ',':
			if depth == 0 {
				fields = append(fields, rest[start:i])
				start = i + 1
			}
		}
	}
	return "", false
}

// exprField returns the origin expression among an INSERT's four fields: the
// table, the key, the operation and then the owner the trigger resolved.
func exprField(fields []string, _ int) (string, bool) {
	if len(fields) < 4 {
		return "", false
	}
	return fields[3], true
}

// tightSQL drops every space from a rendered expression, so a comparison against
// a trigger body is about the names and the structure rather than the spacing.
func tightSQL(expr string) string {
	return strings.ReplaceAll(expr, " ", "")
}
