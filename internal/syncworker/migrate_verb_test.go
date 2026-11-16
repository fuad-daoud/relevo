package syncworker

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// The migrate verb's pipe tests: a migrate hello serves the verb through the
// pipe the daemon holds, a normal hello refuses it, and a backend refusal
// crosses the pipe with its class. Every test serves a real TursoBackend over
// the local driver and an in-memory bucket, because the pipe is the path the
// daemon migrates through.

// serveMigrateBackend serves an unopened backend over a pipe, handing it the
// store hello's R2 names. The backend is unopened so hello is the open, the
// way a daemon's handshake reaches it.
func serveMigrateBackend(t *testing.T, path string, store *blobstore.MemBlobStore) (*TursoBackend, *session) {
	t.Helper()
	driver := &localDriver{path: path}
	b := NewTursoBackend(driver)
	b.openStore = func(R2Config) blobstore.BlobStore { return store }
	return b, startWorker(t, b)
}

// migratePipeHello is the handshake that opens a pipe for the migrate verb
// alone, with the mirror list the verb needs to fill shared.
func migratePipeHello(id string) Request {
	req := migrateHello(id)
	req.MirrorTables = migrateMirror
	return req
}

// TestMigrateVerbServesAMigrationPipe pins the verb through the pipe: a
// migrate hello then migrate answers ok with the run's counts, a second
// migrate answers already-migrated, and stats stays refused on the pipe.
func TestMigrateVerbServesAMigrationPipe(t *testing.T) {
	path := tempReplica(t)
	seeded := seedMigrateRemote(t, path)
	store := blobstore.NewMemBlobStore(nil)
	_, w := serveMigrateBackend(t, path, store)

	if resp := w.ask(migratePipeHello("h")); !resp.OK {
		t.Fatalf("migrate hello = %+v, want ok", resp)
	}
	resp := w.ask(Request{ID: "1", Verb: VerbMigrate})
	if !resp.OK {
		t.Fatalf("migrate = %+v, want ok", resp)
	}
	if resp.Migrate == nil {
		t.Fatal("migrate answered ok with no result")
	}
	if resp.Migrate.AlreadyMigrated {
		t.Error("a fresh migration reported already-migrated")
	}
	if resp.Migrate.Rows != len(seeded) {
		t.Errorf("migrate rows = %d, want the %d seeded rows", resp.Migrate.Rows, len(seeded))
	}
	byOrigin := make(map[string]int)
	for _, e := range seeded {
		byOrigin[e.Origin]++
	}
	for origin, rows := range byOrigin {
		got, ok := resp.Migrate.Origins[origin]
		if !ok {
			t.Errorf("migrate result has no origin %s", origin)
			continue
		}
		if got.Rows != rows {
			t.Errorf("migrate %s rows = %d, want %d", origin, got.Rows, rows)
		}
	}
	if resp.Migrate.ObjectsCreated+resp.Migrate.ObjectsVerified != totalObjects(t, store) {
		t.Errorf("created %d + verified %d != the store's objects",
			resp.Migrate.ObjectsCreated, resp.Migrate.ObjectsVerified)
	}
	if resp.Migrate.RowsSwept != 0 {
		t.Errorf("rows swept = %d, want 0 on a clean run", resp.Migrate.RowsSwept)
	}

	again := w.ask(Request{ID: "2", Verb: VerbMigrate})
	if !again.OK || again.Migrate == nil || !again.Migrate.AlreadyMigrated {
		t.Errorf("second migrate = %+v, want ok with already-migrated", again)
	}

	stats := w.ask(Request{ID: "3", Verb: VerbStats})
	if stats.OK {
		t.Errorf("stats on a migration pipe = %+v, want a refusal", stats)
	}
	if !strings.Contains(stats.Error, "migration pipe") {
		t.Errorf("stats refusal %q does not say the pipe is a migration pipe", stats.Error)
	}
}

// TestMigrateVerbRefusedOnANormalPipe pins that a pipe a normal hello opened
// answers the verb with the handshake's refusal rather than running anything:
// the backend is never reached, so no pull runs.
func TestMigrateVerbRefusedOnANormalPipe(t *testing.T) {
	path := tempReplica(t)
	store := blobstore.NewMemBlobStore(nil)
	driver := &localDriver{path: path}
	b := NewTursoBackend(driver)
	b.openStore = func(R2Config) blobstore.BlobStore { return store }
	w := startWorker(t, b)

	hello := helloRequest("h")
	hello.R2 = &R2Config{Bucket: "bucket"}
	if resp := w.ask(hello); !resp.OK {
		t.Fatalf("hello = %+v, want ok", resp)
	}
	resp := w.ask(Request{ID: "1", Verb: VerbMigrate})
	if resp.OK {
		t.Fatalf("migrate on a normal pipe = %+v, want a refusal", resp)
	}
	if !strings.Contains(resp.Error, "migrate on a pipe not opened for it") {
		t.Errorf("migrate refusal %q does not say the pipe was not opened for it", resp.Error)
	}
	if !strings.Contains(resp.Error, ErrProtocol.Error()) {
		t.Errorf("migrate refusal %q does not carry the protocol refusal", resp.Error)
	}
	if resp.Code != "" {
		t.Errorf("migrate refusal carries code %q, want none: the pipe refusal is not a remote's", resp.Code)
	}
	if resp.Migrate != nil {
		t.Errorf("migrate refusal carries a result %+v, want none", resp.Migrate)
	}
	if driver.pulls != 0 {
		t.Errorf("pulls = %d, want none: the refusal runs before any transfer", driver.pulls)
	}
}

// TestMigrateVerbCarriesTheLateRowsRefusal pins that a run an old binary keeps
// writing past answers the late-rows refusal through the pipe with its class,
// so the daemon retries the verb rather than recording a failure.
func TestMigrateVerbCarriesTheLateRowsRefusal(t *testing.T) {
	path := tempReplica(t)
	seedFormatFiveRemote(t, path)
	seedFormatFiveLog(t, path, legacyLogEntries("origin-b", 1, 10))
	store := blobstore.NewMemBlobStore(nil)
	driver := &localDriver{path: path}
	b := NewTursoBackend(driver)
	b.openStore = func(R2Config) blobstore.BlobStore { return store }
	w := startWorker(t, b)

	if resp := w.ask(migratePipeHello("h")); !resp.OK {
		t.Fatalf("migrate hello = %+v, want ok", resp)
	}
	next := 11
	driver.afterPull = func(db *sql.DB) {
		if _, err := db.Exec(
			"INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			"origin-b", next, next, "board", `["late"]`, opUpsert, 3, `{"title":"late"}`,
			time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC).Format(time.RFC3339Nano),
			nil, nil, nil); err != nil {
			t.Errorf("insert the late row: %v", err)
		}
		next++
	}
	resp := w.ask(Request{ID: "1", Verb: VerbMigrate})
	if resp.OK {
		t.Fatalf("migrate past rows on every pull = %+v, want a refusal", resp)
	}
	if resp.Code != CodeMigrateIncomplete {
		t.Errorf("migrate refusal code = %q, want %q", resp.Code, CodeMigrateIncomplete)
	}
	if !strings.Contains(resp.Error, "origin-b") {
		t.Errorf("migrate refusal %q does not name the blocking origin", resp.Error)
	}
}
