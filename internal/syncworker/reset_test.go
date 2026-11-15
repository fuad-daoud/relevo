package syncworker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	turso "turso.tech/database/tursogo"
)

// reseedReplica writes statements to a replica path from a driver hook, which
// runs on the worker's goroutine: a failure is an error on the test, not a
// fatal that would end the wrong goroutine.
func reseedReplica(t *testing.T, path string, stmts []string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Errorf("open %s to reseed it: %v", path, err)
		return
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Errorf("reseed %q: %v", stmt, err)
		}
	}
}

// TestUnknownReplicaIsDiscardedAndBootstrappedAgain pins the reset: a
// pre-existing replica whose tables match no shape this build knows is
// discarded once and bootstrapped again, and the fresh replica is created at
// the current format. The re-bootstrap's download reaches the byte totals.
func TestUnknownReplicaIsDiscardedAndBootstrappedAgain(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path, createStatements[0], "CREATE TABLE secrets (s TEXT NOT NULL)")
	driver := &localDriver{path: path}
	driver.afterDiscard = func(string) {
		// The fresh file stands for an empty remote; the download the fresh
		// bootstrap made is what the engine's counter reports.
		driver.bootstrapped = 500
	}
	b := NewTursoBackend(driver)
	w := startWorker(t, b)
	resp := w.ask(helloRequest("h"))
	if !resp.OK {
		t.Fatalf("hello = %+v, want the reset replica to open", resp)
	}
	if driver.discards != 1 {
		t.Errorf("discards = %d, want exactly one", driver.discards)
	}
	if driver.opens != 2 {
		t.Errorf("opens = %d, want the first open and the re-bootstrap", driver.opens)
	}
	tables := replicaTables(t, path)
	for _, want := range []string{"heads", "meta", "shared"} {
		if !tables[want] {
			t.Errorf("the fresh replica does not hold %q; it holds %v", want, tables)
		}
	}
	if len(tables) != 3 {
		t.Errorf("the fresh replica holds %v, want exactly the log's three tables", tables)
	}
	if got := metaFormat(t, path); got != "6" {
		t.Errorf("meta format = %q, want 6", got)
	}
	statsResp := w.ask(Request{ID: "s", Verb: VerbStats})
	if !statsResp.OK {
		t.Fatalf("stats = %+v, want the byte totals", statsResp)
	}
	if statsResp.Stats.TursoReceived != 500 {
		t.Errorf("TursoReceived = %d, want the re-bootstrap's 500 bytes", statsResp.Stats.TursoReceived)
	}
}

// TestUnknownReplicaThatComesBackMismatchedIsRefused pins the once-only reset:
// a re-bootstrap that lands on the same unknown shape is refused as not a log
// rather than discarded again.
func TestUnknownReplicaThatComesBackMismatchedIsRefused(t *testing.T) {
	path := tempReplica(t)
	stmts := []string{createStatements[0], "CREATE TABLE secrets (s TEXT NOT NULL)"}
	execOn(t, path, stmts...)
	driver := &localDriver{path: path}
	driver.afterDiscard = func(p string) { reseedReplica(t, p, stmts) }
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want a refusal", resp)
	}
	if resp.Code != CodeRemote {
		t.Errorf("code = %q, want %q: the refusal repeats on every attempt", resp.Code, CodeRemote)
	}
	if !strings.Contains(resp.Error, "not a relevo sync log") {
		t.Errorf("refusal %q does not say the remote is not a log", resp.Error)
	}
	if driver.discards != 1 {
		t.Errorf("discards = %d, want exactly one: the second mismatch is refused, not reset again", driver.discards)
	}
	if driver.opens != 2 {
		t.Errorf("opens = %d, want the first open and one re-bootstrap", driver.opens)
	}
}

// TestOlderFormatPullsOnceBeforeTheMigrateRefusal pins that a format-5
// replica whose pull brings nothing is refused with the migrate message
// after exactly one pull, and is never discarded: an older format classifies.
func TestOlderFormatPullsOnceBeforeTheMigrateRefusal(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	driver := &localDriver{path: path}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want the migrate refusal", resp)
	}
	if resp.Code != CodeRemote {
		t.Errorf("code = %q, want %q", resp.Code, CodeRemote)
	}
	if !strings.Contains(resp.Error, "run relevo db sync migrate once every machine runs this release") {
		t.Errorf("refusal %q does not carry the migrate message", resp.Error)
	}
	if driver.pulls != 1 {
		t.Errorf("pulls = %d, want exactly one before the refusal", driver.pulls)
	}
	if driver.discards != 0 {
		t.Errorf("discards = %d, want none: an older format is not thrown away", driver.discards)
	}
	if driver.pushes != 0 {
		t.Errorf("pushes = %d, want none: a refused remote is never written into", driver.pushes)
	}
}

// TestOlderFormatOpensWhenThePullBringsTheCurrentLog pins the machine that was
// offline while another migrated: its format-5 replica pulls once, the pull
// brings the current tables and a row, and hello opens without a discard. The
// pull reads the migrated row from the bucket's segment, so the segment is in
// the store the backend opens and the heads row arrives with the pull.
func TestOlderFormatOpensWhenThePullBringsTheCurrentLog(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	migrated := []Entry{{
		Origin: "origin-b", Seq: 1, Batch: 1, Tbl: "board", PK: `["b"]`, Op: opUpsert,
		SchemaVersion: 3, Body: json.RawMessage(`{"title":"migrated"}`),
		At: time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC),
	}}
	data, _, err := EncodeSegment("origin-b", migrated)
	if err != nil {
		t.Fatalf("encode the migrated segment: %v", err)
	}
	key := blobstore.SegmentKey("origin-b", 1)
	store := blobstore.NewMemBlobStore(nil)
	if err := store.Put(context.Background(), key, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("seed the migrated segment: %v", err)
	}
	driver := &localDriver{path: path}
	driver.afterPull = func(db *sql.DB) {
		// The migration appears once; a later pull brings nothing new.
		driver.afterPull = nil
		for _, stmt := range []string{
			createStatements[0],
			createStatements[2],
			"UPDATE meta SET value = '6' WHERE key = '" + metaFormatKey + "'",
			// The migration drops the tables the current format does not
			// hold, so the replica answers with the current set alone.
			"DROP TABLE log",
			"DROP TABLE marks",
			"INSERT INTO heads (origin, seq, key, entries, bytes, at)" +
				" VALUES ('origin-b', 1, '" + key + "', 1, " + strconv.Itoa(len(data)) + ", '2026-10-08T09:30:00Z')",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Errorf("the pull brings %q: %v", stmt, err)
			}
		}
	}
	b := NewTursoBackend(driver)
	b.openStore = func(R2Config) blobstore.BlobStore { return store }
	w := startWorker(t, b)
	resp := w.ask(helloRequest("h"))
	if !resp.OK {
		t.Fatalf("hello = %+v, want the migrated replica to open", resp)
	}
	if driver.pulls != 1 {
		t.Errorf("pulls = %d, want exactly one", driver.pulls)
	}
	if driver.discards != 0 {
		t.Errorf("discards = %d, want none: the pull brought the log", driver.discards)
	}
	if driver.opens != 1 {
		t.Errorf("opens = %d, want one: the replica was never re-bootstrapped", driver.opens)
	}
	pullResp := w.ask(Request{ID: "p", Verb: VerbPull})
	if !pullResp.OK {
		t.Fatalf("pull after the open = %+v, want the migrated row", pullResp)
	}
	assertSeqs(t, pullResp.Entries, 1)
	if len(pullResp.Entries) != 1 || pullResp.Entries[0].Origin != "origin-b" {
		t.Errorf("pull = %+v, want the row the migration brought", pullResp.Entries)
	}
}

// TestOlderFormatRefusesWithThePullError pins that a pull which fails on the
// older-format branch answers with the pull's own error, not the migrate
// sentence.
func TestOlderFormatRefusesWithThePullError(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	driver := &localDriver{path: path, pullErr: errors.New("remote unreachable")}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want the pull's refusal", resp)
	}
	if !strings.Contains(resp.Error, "remote unreachable") {
		t.Errorf("refusal %q does not carry the pull's error", resp.Error)
	}
	if strings.Contains(resp.Error, "run relevo db sync migrate") {
		t.Errorf("refusal %q carries the migrate sentence over a failed pull", resp.Error)
	}
	if driver.pulls != 1 {
		t.Errorf("pulls = %d, want exactly one", driver.pulls)
	}
	if driver.discards != 0 {
		t.Errorf("discards = %d, want none", driver.discards)
	}
}

// TestCurrentLogDoesNotPullDuringOpen pins that the pull belongs to the
// older-format branch alone: a replica already at the current format opens
// without one.
func TestCurrentLogDoesNotPullDuringOpen(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path, append(append([]string(nil), createStatements...),
		"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '6')")...)
	driver := &localDriver{path: path}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if !resp.OK {
		t.Fatalf("hello = %+v, want the current log to open", resp)
	}
	if driver.pulls != 0 {
		t.Errorf("pulls = %d, want none: the current set never pulls on open", driver.pulls)
	}
	if driver.discards != 0 {
		t.Errorf("discards = %d, want none", driver.discards)
	}
	if driver.pushes != 0 {
		t.Errorf("pushes = %d, want none: the log was already there", driver.pushes)
	}
}

// TestFreshlyBootstrappedReplicaIsNotDiscarded pins that a replica this open
// bootstrapped already is the remote: even when its tables match no known
// shape it is refused, not thrown away and downloaded again.
func TestFreshlyBootstrappedReplicaIsNotDiscarded(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path, createStatements[0], "CREATE TABLE secrets (s TEXT NOT NULL)")
	driver := &localDriver{path: path, bootstrapped: 4096}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want the not-a-log refusal", resp)
	}
	if !strings.Contains(resp.Error, "not a relevo sync log") {
		t.Errorf("refusal %q does not say the remote is not a log", resp.Error)
	}
	if driver.discards != 0 {
		t.Errorf("discards = %d, want none: the open just bootstrapped this replica", driver.discards)
	}
	if driver.opens != 1 {
		t.Errorf("opens = %d, want one", driver.opens)
	}
}

// TestTursoDriverDiscardRemovesOnlyTheReplicasFiles pins the file rule: the
// replica and the driver files named after it go, an unrelated file and a
// directory stay, and the next open through the sync constructor bootstraps
// again. A missing directory is not an error; an unreadable one is.
func TestTursoDriverDiscardRemovesOnlyTheReplicasFiles(t *testing.T) {
	restore := newSyncDb
	t.Cleanup(func() { newSyncDb = restore })

	dir := t.TempDir()
	replica := filepath.Join(dir, "relevo-sync.db")
	removed := []string{replica, replica + "-wal", replica + ".info"}
	kept := filepath.Join(dir, "other.db")
	for _, name := range append(append([]string(nil), removed...), kept) {
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// A directory under the replica's prefix is not a driver file.
	keptDir := replica + "-dir"
	if err := os.Mkdir(keptDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", keptDir, err)
	}
	newSyncDb = func(context.Context, turso.TursoSyncDbConfig) (syncDb, error) {
		return &fakeSyncDb{db: openAt(t, replica)}, nil
	}
	d := newTursoDriver(replica)
	if _, err := d.Open(context.Background(), Spec{URL: "u", Token: "t"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Discard(context.Background()); err != nil {
		t.Fatalf("discard: %v", err)
	}
	for _, name := range removed {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after the discard (stat err = %v)", filepath.Base(name), err)
		}
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the unrelated file went with the replica: %v", err)
	}
	if _, err := os.Stat(keptDir); err != nil {
		t.Errorf("the directory went with the replica: %v", err)
	}
	if _, err := d.Open(context.Background(), Spec{URL: "u", Token: "t"}); err != nil {
		t.Errorf("the open after the discard: %v", err)
	}

	missing := newTursoDriver(filepath.Join(t.TempDir(), "absent", "relevo-sync.db"))
	if err := missing.Discard(context.Background()); err != nil {
		t.Errorf("discard of a missing directory = %v, want nil", err)
	}

	// A path whose parent is a file cannot be read: that is an error, not a
	// missing directory.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write the blocker: %v", err)
	}
	blocked := newTursoDriver(filepath.Join(blocker, "relevo-sync.db"))
	if err := blocked.Discard(context.Background()); err == nil {
		t.Error("discard over an unreadable directory = nil, want the read's error")
	}

	// A file that cannot be removed refuses the discard rather than passing
	// as missing.
	lockedDir := t.TempDir()
	lockedReplica := filepath.Join(lockedDir, "relevo-sync.db")
	if err := os.WriteFile(lockedReplica, []byte("x"), 0o600); err != nil {
		t.Fatalf("write the locked replica: %v", err)
	}
	if err := os.Chmod(lockedDir, 0o500); err != nil {
		t.Fatalf("lock the directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o700) })
	locked := newTursoDriver(lockedReplica)
	if err := locked.Discard(context.Background()); err == nil {
		t.Error("discard of an unremovable file = nil, want the removal's error")
	}
}

// TestDiscardErrorRefusesTheHello pins that a discard which fails refuses the
// handshake, marked as the other open failures are: the error carries a
// transport needle, so the class is the one markOpenRefusal gives it.
func TestDiscardErrorRefusesTheHello(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path, createStatements[0], "CREATE TABLE secrets (s TEXT NOT NULL)")
	driver := &localDriver{path: path, discardErr: errors.New("remove the replica: connection reset by peer")}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want the discard's refusal", resp)
	}
	if resp.Code != CodeTransport {
		t.Errorf("code = %q, want %q: the discard's error is marked as an open failure", resp.Code, CodeTransport)
	}
	if !strings.Contains(resp.Error, "connection reset by peer") {
		t.Errorf("refusal %q does not carry the discard's error", resp.Error)
	}
	if driver.discards != 1 {
		t.Errorf("discards = %d, want exactly one", driver.discards)
	}
	if driver.opens != 1 {
		t.Errorf("opens = %d, want one: a failed discard is not re-bootstrapped", driver.opens)
	}
}

// TestUnknownReplicaReopenErrorRefusesTheHello pins that a re-bootstrap open
// which fails refuses the handshake, marked as the other open failures are:
// the error carries a transport needle, so the class is the one markOpenRefusal
// gives it.
func TestUnknownReplicaReopenErrorRefusesTheHello(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path, createStatements[0], "CREATE TABLE secrets (s TEXT NOT NULL)")
	openErr := errors.New("open replica: connection reset by peer")
	driver := &localDriver{path: path}
	driver.afterDiscard = func(string) {
		// The error is set after the discard so the first open succeeds and
		// only the re-bootstrap open fails.
		driver.openErr = openErr
	}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want the re-bootstrap open's refusal", resp)
	}
	if resp.Code != CodeTransport {
		t.Errorf("code = %q, want %q: the open's error is marked as an open failure", resp.Code, CodeTransport)
	}
	if !strings.Contains(resp.Error, openErr.Error()) {
		t.Errorf("refusal %q does not carry the open's error", resp.Error)
	}
	if driver.discards != 1 {
		t.Errorf("discards = %d, want exactly one", driver.discards)
	}
	if driver.opens != 2 {
		t.Errorf("opens = %d, want the first open and the failed re-bootstrap", driver.opens)
	}
}

// TestOlderFormatRefusesWhenThePulledReplicaCannotBeRead pins that a pull
// which leaves the replica unreadable answers with the read's error rather
// than the migrate sentence.
func TestOlderFormatRefusesWhenThePulledReplicaCannotBeRead(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	driver := &localDriver{path: path}
	driver.afterPull = func(db *sql.DB) {
		if err := db.Close(); err != nil {
			t.Errorf("close the replica after the pull: %v", err)
		}
	}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want the read's refusal", resp)
	}
	if !strings.Contains(resp.Error, "read the remote's tables") {
		t.Errorf("refusal %q does not carry the read's error", resp.Error)
	}
	if driver.pulls != 1 {
		t.Errorf("pulls = %d, want exactly one", driver.pulls)
	}
}

// TestOlderFormatRefusesWhenThePulledFormatCannotBeRead pins that a pull which
// brings the current tables but leaves the format row unreadable answers with
// the read's error.
func TestOlderFormatRefusesWhenThePulledFormatCannotBeRead(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	driver := &localDriver{path: path}
	driver.afterPull = func(db *sql.DB) {
		for _, stmt := range []string{
			createStatements[0],
			createStatements[2],
			"DROP TABLE log",
			"DROP TABLE marks",
			"DROP TABLE meta",
			"CREATE TABLE meta (name TEXT)",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Errorf("the pull brings %q: %v", stmt, err)
			}
		}
	}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want the read's refusal", resp)
	}
	if !strings.Contains(resp.Error, "read the log version") {
		t.Errorf("refusal %q does not carry the format read's error", resp.Error)
	}
	if driver.pulls != 1 {
		t.Errorf("pulls = %d, want exactly one", driver.pulls)
	}
}

// TestOlderFormatStillRefusesWhenThePullBringsCurrentTablesAtAnOlderFormat
// pins that the current tables alone do not open the replica: the format row
// must name this build's format too.
func TestOlderFormatStillRefusesWhenThePullBringsCurrentTablesAtAnOlderFormat(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	driver := &localDriver{path: path}
	driver.afterPull = func(db *sql.DB) {
		// The current tables arrive, but the format row stays at the older
		// number: the refusal must come from the row, not the table set.
		for _, stmt := range []string{
			createStatements[0],
			createStatements[2],
			"DROP TABLE log",
			"DROP TABLE marks",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Errorf("the pull brings %q: %v", stmt, err)
			}
		}
	}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want the migrate refusal", resp)
	}
	if !strings.Contains(resp.Error, "run relevo db sync migrate once every machine runs this release") {
		t.Errorf("refusal %q does not carry the migrate message", resp.Error)
	}
	if driver.pulls != 1 {
		t.Errorf("pulls = %d, want exactly one", driver.pulls)
	}
	if driver.discards != 0 {
		t.Errorf("discards = %d, want none", driver.discards)
	}
}
