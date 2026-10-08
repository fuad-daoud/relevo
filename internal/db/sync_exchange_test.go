package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// competingRepoWrite is the write that contends with the drain: it moves the
// repo row the fixture seeded, so whichever state the drain read is visible.
const competingRepoWrite = `UPDATE repo SET origin_url = 'after' WHERE id = 'r1'`

// TestOutboxDrainIsOneSnapshot pins that a drain holds the write lock from its
// first outbox read through its last row read, so every entry in the batch and
// the row state it names were the state of one moment. A drain that read the
// entries in one transaction and their rows in another would report each entry
// against a row a write committed between the two reads had already moved, so
// the batch would carry a state no moment held.
//
// The proof is the lock, not two drains with a write between them: no
// arrangement of complete sequential drains can observe a write landing inside
// one drain. The first half holds a transaction that runs the drain open and
// points a competing write's BEGIN IMMEDIATE at that lock -- the write has to
// fail busy with its body never run. The second half puts a competing write in
// flight for the whole of the drain instead, because that is the half a split
// drain can slip past: its second transaction takes the write lock again
// before the drain returns, so a writer held off until the drain is over never
// learns the lock was let go in the middle.
func TestOutboxDrainIsOneSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := openExchangeDB(t, path)
	sharedRowFixture(t, d)
	if err := exchangeWrite(d, `UPDATE repo SET origin_url = 'before' WHERE id = 'r1'`); err != nil {
		t.Fatalf("write before the drain: %v", err)
	}

	drained, err := drainUnderTwoRivals(t, d, path)
	if err != nil {
		t.Fatalf("the drain: %v", err)
	}
	if len(drained) == 0 {
		t.Fatal("the drain returned no entries")
	}
	repo := findDrained(t, drained, "repo", `["r1"]`)
	if repo.Row == nil {
		t.Fatal("the drained repo entry carries no row state")
	}
	if got := exchangeColumn(t, *repo.Row, "origin_url"); got != "before" {
		t.Errorf("the drained repo row reads origin_url %v, want before: the row state is the one the drain's own transaction saw", got)
	}
}

// drainUnderTwoRivals runs one drain on d inside a transaction it opens itself,
// and pushes two classes of competing write at the write lock that transaction
// holds. A fail-fast rival must be turned away at once; a rival that samples the
// lock as fast as the file will answer keeps asking for the whole of the drain,
// which is the half a split drain cannot survive, because its second transaction
// takes the lock back before the drain returns.
func drainUnderTwoRivals(t *testing.T, d *DB, path string) ([]DrainedEntry, error) {
	t.Helper()
	failFast := openExchangeRival(t, path, time.Millisecond)
	// A one-nanosecond retry window gives up on the first busy BEGIN, so a rival
	// built on it samples the lock every time the file answers rather than once
	// per backoff -- which is the difference between missing the window a split
	// drain leaves open and landing in it. Three of them narrow the remaining
	// race further.
	var samplers []*DB
	for i := 0; i < 3; i++ {
		samplers = append(samplers, openExchangeRival(t, path, time.Nanosecond))
	}

	// competingRan is read from inside the drain's own transaction, before its
	// COMMIT, so the question it answers -- did a write get in while the drain
	// was running -- is asked of a moment the drain can still speak for.
	var competingRan int32
	holding := make(chan struct{})
	release := make(chan struct{})
	stop := make(chan struct{})
	drainDone := make(chan error, 1)
	var drained []DrainedEntry
	go func() {
		drainDone <- d.Tx(func(tx *Tx) error {
			// BEGIN IMMEDIATE has the write lock from here on, and the drain
			// below runs inside it.
			close(holding)
			<-release
			var derr error
			drained, derr = tx.DrainOutbox(1000)
			if atomic.LoadInt32(&competingRan) != 0 {
				t.Errorf("a competing write ran while the drain held the write lock: the drain let the lock go between its entry read and its row reads")
			}
			return derr
		})
	}()
	<-holding

	ran := false
	cerr := failFast.Tx(func(tx *Tx) error {
		ran = true
		_, err := tx.exec(competingRepoWrite)
		return err
	})
	if !errors.Is(cerr, ErrBusy) {
		t.Errorf("the competing write = %v, want errors.Is(..., ErrBusy): the drain holds the write lock across both its reads", cerr)
	}
	if ran {
		t.Error("the competing write's body ran while the drain held the write lock")
	}

	// Each rival hammers BEGIN IMMEDIATE for as long as the drain runs.
	spun := make(chan struct{}, len(samplers))
	spinRivals(samplers, &competingRan, stop, spun)
	// Let the sampler reach the lock before the drain is let run, so it is
	// contending rather than starting afterwards.
	time.Sleep(20 * time.Millisecond)
	close(release)
	derr := <-drainDone
	close(stop)
	for range samplers {
		<-spun
	}
	if derr != nil {
		return nil, derr
	}

	// Busy, then success: the same write lands once the drain has let the lock
	// go, which is what makes the failures above the drain's lock rather than a
	// handle that cannot write at all.
	if err := exchangeWrite(failFast, competingRepoWrite); err != nil {
		t.Fatalf("the competing write after the drain: %v", err)
	}
	return drained, nil
}

// spinRivals hammers BEGIN IMMEDIATE at the write lock from each of rivals until
// stop closes, so a drain that lets the lock go between its two reads is caught
// at the moment it does. A rival whose body runs marks ran, which the drain
// reads from inside its own transaction before it commits. Each rival sends to
// spun as it stops.
func spinRivals(rivals []*DB, ran *int32, stop <-chan struct{}, spun chan<- struct{}) {
	for _, rival := range rivals {
		go func() {
			for {
				select {
				case <-stop:
					spun <- struct{}{}
					return
				default:
				}
				_ = rival.Tx(func(tx *Tx) error {
					atomic.StoreInt32(ran, 1)
					_, err := tx.exec(competingRepoWrite)
					return err
				})
			}
		}()
	}
}

// TestDrainOutboxIsOneTransaction pins the wrapper's whole guarantee rather than
// only its transaction body: the entries and the rows they name are read under
// one write lock, so no write commits between the two reads. A wrapper that read
// the entries in one transaction and the rows in another would let a competing
// writer in between, and the batch would report each entry against a row state
// from a later moment than the entry list.
//
// The proof is the pool, not a race: the drain's handle allows one connection at
// a time, and a competing transaction waits on that pool. A one-transaction
// wrapper holds the connection from its entry read through its row reads and
// never gives the waiter a turn, so it returns at once. A wrapper split in two
// returns the connection between them, the waiter takes it and holds it, and the
// second transaction waits out the holder -- which is the delay measured here.
func TestDrainOutboxIsOneTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := openTestDBAt(t, path)
	seedDrainBacklog(t, d, 1000)
	d.sqlDB.SetMaxOpenConns(1)

	for attempt := 0; attempt < 3; attempt++ {
		held, err := drainWhilePoolHeld(d, 1000)
		if err != nil {
			t.Fatalf("DrainOutbox under a pool waiter = %v, want one transaction across both its reads", err)
		}
		if held > 2*time.Second {
			t.Fatalf("DrainOutbox took %s while a waiter held the pool, want one transaction: a split drain waits out the holder", held)
		}
	}
}

// drainWhilePoolHeld runs one drain while a competing transaction waits on the
// same one-connection pool. It returns how long the drain took. The waiter is
// started just after the drain, so it queues behind the drain's first
// transaction; a drain that is one transaction keeps the connection and never
// lets the waiter run, and a drain that is two lets the waiter take the
// connection in between and waits out its hold.
func drainWhilePoolHeld(d *DB, limit int) (time.Duration, error) {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := d.DrainOutbox(limit)
		done <- err
	}()
	// Let the drain take the pool's one connection before the waiter asks: the
	// waiter must queue behind it, not race it for the connection.
	time.Sleep(time.Millisecond)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = d.Tx(func(tx *Tx) error {
			select {
			case <-stop:
			case <-time.After(5 * time.Second):
			}
			return nil
		})
	}()
	err := <-done
	held := time.Since(start)
	close(stop)
	wg.Wait()
	return held, err
}

// openTestDBAt opens path as the test's own file.
func openTestDBAt(t *testing.T, path string) *DB {
	t.Helper()
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedDrainBacklog writes one repo row behind a backlog of outbox entries, so a
// drain reads a window long enough to hold its transaction across more than the
// entry read.
func seedDrainBacklog(t *testing.T, d *DB, writes int) {
	t.Helper()
	err := d.Tx(func(tx *Tx) error {
		if _, err := tx.exec(`INSERT INTO repo (id, origin_url, common_dir, first_seen, origin)
			VALUES ('r0', 'u', '/c', 't', 'instP')`); err != nil {
			return err
		}
		for i := 1; i < writes; i++ {
			if _, err := tx.exec(`UPDATE repo SET origin_url = ? WHERE id = 'r0'`, fmt.Sprintf("u%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed the drain backlog: %v", err)
	}
}

// openExchangeDB opens path for the drain that will run on it.
func openExchangeDB(t *testing.T, path string) *DB {
	t.Helper()
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// openExchangeRival opens a second handle on path whose busy waits are short: a
// small retry gives up almost at once, so a busy answer is the drain's lock
// rather than a long wait, and a large one is still contending afterwards.
func openExchangeRival(t *testing.T, path string, retry time.Duration) *DB {
	t.Helper()
	d, err := OpenWith(path, Options{BusyTimeout: 20 * time.Millisecond, BeginRetry: retry})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// exchangeWrite runs one statement in a transaction of its own.
func exchangeWrite(d *DB, query string) error {
	return d.Tx(func(tx *Tx) error {
		_, err := tx.exec(query)
		return err
	})
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

// TestImportMarkMigrationAppliesFreshAndUpgraded pins that the mark table reaches
// both kinds of file: one created by this build, and one an earlier build left
// behind. The upgraded file must not have the table before it is opened, or the
// migration would be applying to a file that never needed it.
func TestImportMarkMigrationAppliesFreshAndUpgraded(t *testing.T) {
	t.Run("a file this build creates", func(t *testing.T) {
		d := openTestDB(t)
		if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 4) }); err != nil {
			t.Fatalf("write a mark on a fresh file: %v", err)
		}
	})

	t.Run("a file migrated before the table existed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "relevo.db")
		old := migrateThrough(t, path, importMarkMigration-1)
		if _, err := old.Exec(`SELECT 1 FROM sync_import_mark`); err == nil {
			t.Error("the mark table exists on a file that predates its migration")
		}
		if err := old.Close(); err != nil {
			t.Fatalf("close the pre-migration file: %v", err)
		}

		d, err := Open(path)
		if err != nil {
			t.Fatalf("Open the upgraded file: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })
		if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 4) }); err != nil {
			t.Fatalf("write a mark on the upgraded file: %v", err)
		}
	})
}

// TestImportMarkSurvivesBackupRestore pins the property the mark's home is chosen
// for: a mark lives in the same file as the rows it describes, so a copy of that
// file comes back with a mark that matches the rows it holds. A mark kept
// anywhere else could come back ahead of the rows and skip them for good.
func TestImportMarkSurvivesBackupRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 11) }); err != nil {
		t.Fatalf("write the mark: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restored := filepath.Join(t.TempDir(), "restored.db")
	copyFile(t, path, restored)
	r, err := Open(restored)
	if err != nil {
		t.Fatalf("Open the copy: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if seq, found, err := r.ImportMark("instX"); err != nil || !found || seq != 11 {
		t.Errorf("the restored file reads (%d, %t, %v), want (11, true, nil)", seq, found, err)
	}
	// The restored file's mark and rows are the ones the backup held, so the
	// entries that came after the backup are the ones it must re-apply rather
	// than skip.
	if _, found, err := r.ImportMark("instY"); err != nil || found {
		t.Errorf("an origin the backup never marked reads (found=%t, %v), want (false, nil)", found, err)
	}
}

// migrateThrough applies only the migrations up to version last to path, so a
// test can stand up a file as an earlier build left it and then let the current
// one open it.
func migrateThrough(t *testing.T, path string, last int) *sql.DB {
	t.Helper()
	sqlDB := rawSQLDB(t, path)
	names, err := migrationNames(migrationFiles)
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}
	for _, name := range names {
		n, err := migrationNumber(name)
		if err != nil {
			t.Fatalf("migrationNumber %s: %v", name, err)
		}
		if n > last {
			break
		}
		if err := applyOneMigration(sqlDB, migrationFiles, name, n); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return sqlDB
}

// copyFile copies src to dst, so a test can stand up a backup of a file the
// handle has already closed.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}
