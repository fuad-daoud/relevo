package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
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

// sharedRowFixture writes one row into every shared table, with a BLOB whose
// bytes are not text, so a row read has to return the stored bytes rather than a
// rendering of them. It returns each row's primary key in the outbox's spelling.
func sharedRowFixture(t *testing.T, d *DB) map[string]string {
	t.Helper()
	keys := map[string]string{
		"repo":           `["r1"]`,
		"mastermind":     `["m1"]`,
		"binding_record": `["rec1"]`,
		"installation":   `["instI"]`,
		"binding":        `["b1"]`,
		"chains":         `["c1"]`,
		"binding_event":  `["rec1",1]`,
		"round_file":     `["rec1","report.md"]`,
		"chain_event":    `["c1",1]`,
		"chain_member":   `["c1","b1"]`,
		"chain_check":    `["c1",1]`,
		"round":          `["rd1"]`,
		"event":          `["e1"]`,
		"artifact":       `["a1"]`,
		"transcript":     `["t1"]`,
	}
	if err := d.Tx(func(t *Tx) error {
		return execTxAll(t, seedSharedRows()...)
	}); err != nil {
		t.Fatalf("seed one row per shared table: %v", err)
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
// schema order, and a BLOB column comes back as the bytes on disk rather than as
// text. A compressed body read as its decompressed rendering would let a
// re-export write bytes the other machine never held.
func TestExchangeRowReadCoversSharedTables(t *testing.T) {
	d := openTestDB(t)
	keys := sharedRowFixture(t, d)

	for _, table := range SharedTables {
		row, found, err := d.ReadExchangeRow(table.Name, keys[table.Name])
		if err != nil {
			t.Errorf("read %s %s: %v", table.Name, keys[table.Name], err)
			continue
		}
		if !found {
			t.Errorf("%s %s reads as absent, want the seeded row", table.Name, keys[table.Name])
			continue
		}
		want := tableColumnNames(t, d, table.Name)
		got := make([]string, len(row.Columns))
		for i, c := range row.Columns {
			got[i] = c.Name
		}
		if len(got) != len(want) {
			t.Errorf("%s returns %d columns, want the schema's %d", table.Name, len(got), len(want))
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s column %d is %s, want %s: the read must follow schema order", table.Name, i, got[i], want[i])
			}
		}
	}

	// The three codec-tagged columns and their bodies are stored bytes, and the
	// read must hand back those bytes rather than a text rendering.
	rowFile, _, err := d.ReadExchangeRow("round_file", `["rec1","report.md"]`)
	if err != nil {
		t.Fatalf("read round_file: %v", err)
	}
	if got := exchangeColumn(t, rowFile, "body"); !bytes.Equal(got.([]byte), []byte{0x00, 0x28, 0xB8, 0x0B, 0xFD, 0xFF, 0xFF}) {
		t.Errorf("round_file body reads %#v, want the stored bytes", got)
	}
	if got := exchangeColumn(t, rowFile, "body_codec"); got != int64(1) {
		t.Errorf("round_file body_codec reads %#v, want the integer 1", got)
	}

	rowTranscript, _, err := d.ReadExchangeRow("transcript", `["t1"]`)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if got := exchangeColumn(t, rowTranscript, "record_json"); !bytes.Equal(got.([]byte), []byte{0x7B, 0x7D, 0xFF}) {
		t.Errorf("transcript record_json reads %#v, want the stored bytes", got)
	}
	if got := exchangeColumn(t, rowTranscript, "rendered"); !bytes.Equal(got.([]byte), []byte{0x0A, 0x1B}) {
		t.Errorf("transcript rendered reads %#v, want the stored bytes", got)
	}
	if got := exchangeColumn(t, rowTranscript, "record_json_codec"); got != int64(1) {
		t.Errorf("transcript record_json_codec reads %#v, want the integer 1", got)
	}

	// A NULL column stays a nil rather than becoming an empty string, because
	// the two mean different things to a writer replaying the row.
	rowChain, _, err := d.ReadExchangeRow("chains", `["c1"]`)
	if err != nil {
		t.Fatalf("read chains: %v", err)
	}
	if got := exchangeColumn(t, rowChain, "plan"); got != int64(1) {
		t.Errorf("an INTEGER column reads %#v, want the integer 1", got)
	}
	rowBinding, _, err := d.ReadExchangeRow("binding", `["b1"]`)
	if err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if got := exchangeColumn(t, rowBinding, "archived_at"); got != nil {
		t.Errorf("a NULL column reads %#v, want nil", got)
	}

	// A key the table does not have reads as absent rather than as a row.
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
