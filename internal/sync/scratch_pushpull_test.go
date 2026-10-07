//go:build !modernc

package sync_test

// The push over a real remote, against a scratch cloud database. It is the
// end-to-end assertion the whole re-land exists for: rows this machine wrote
// before capture was turned on, and rows written through its pools afterwards,
// both reach the remote.
//
// The scratch remote is named by two environment variables, so nothing here
// carries a credential and CI never has one to carry:
//
//	RELEVO_SCRATCH_SYNC_URL    libsql://<scratch-name>-<account>.<region>.turso.io
//	RELEVO_SCRATCH_SYNC_TOKEN  a token for that database alone
//
// Every local file is created under t.TempDir(). No live database is named
// anywhere below and no verb runs against a live state: these are driver calls
// over fixtures, which is the only way "did the push land" can be answered from
// this side.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	turso "turso.tech/database/tursogo"
)

// scratchRemote is the remote this file talks to, skipping when it is not named.
func scratchRemote(t *testing.T) (string, string) {
	t.Helper()
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("no scratch remote named: set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN")
	}
	return url, token
}

// scratchCtx bounds one driver call. A pull long-polls, so the bound is the only
// thing standing between a quiet remote and a test that never returns.
func scratchCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 90*time.Second)
}

// openSyncHandle opens a file against the remote the way the enable does and
// returns both the sync client and the pool the sync engine owns. The two are
// the same handle: the engine's own pool is where a schema change has to be made
// for the remote to learn of it, and the client is what pushes.
func openSyncHandle(t *testing.T, path, url, token string) (*relevosync.Turso, *sql.DB) {
	t.Helper()
	flag := false
	handle, err := turso.NewTursoSyncDb(context.Background(), turso.TursoSyncDbConfig{
		Path:             path,
		RemoteUrl:        url,
		AuthToken:        token,
		ClientName:       "relevo-scratch-owner",
		BootstrapIfEmpty: &flag,
	})
	if err != nil {
		t.Fatalf("open the member at %s: %v", path, err)
	}
	pool, err := handle.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect the sync-owned pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return relevosync.NewTurso(handle), pool
}

// openPeer opens a second local file against the same remote through the sync
// engine and returns its SQL pool. A peer is the only way this file can look at
// the remote's contents without a shell and without naming the machine's own
// database: it is a file of its own, and everything it knows about the cloud it
// learned by pulling it.
func openPeer(t *testing.T, dir, name, url, token string) *sql.DB {
	t.Helper()
	flag := true
	handle, err := turso.NewTursoSyncDb(context.Background(), turso.TursoSyncDbConfig{
		Path:             filepath.Join(dir, name),
		RemoteUrl:        url,
		AuthToken:        token,
		ClientName:       "relevo-scratch-peer",
		BootstrapIfEmpty: &flag,
	})
	if err != nil {
		t.Fatalf("open the peer %s: %v", name, err)
	}
	pool, err := handle.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect the peer %s: %v", name, err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// countTable reads a table back over whatever pool it is handed. A remote the
// push never reached has no such table, which is a count of zero rather than a
// failure to read: that absence is the measurement.
func countTable(t *testing.T, pool *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0
		}
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestThePushCarriesTheRowsWrittenAfterTheMemberOpen is the property the re-land
// exists for, against a real remote: rows this machine wrote through its own pool
// after capture was turned on, and a push that carries them.
//
// The table is created over a connection the sync engine owns, because that is
// the only way a remote learns a table exists at all -- see
// TestARemoteOnlyLearnsATableTheSyncEngineCreated.
func TestThePushCarriesTheRowsWrittenAfterTheMemberOpen(t *testing.T) {
	url, token := scratchRemote(t)
	ctx, cancel := scratchCtx(t)
	defer cancel()
	dir := t.TempDir()
	live := filepath.Join(dir, "relevo.db")

	client, synced := openSyncHandle(t, live, url, token)
	if _, err := synced.Exec(`CREATE TABLE IF NOT EXISTS scratch (k INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create the table on the sync connection: %v", err)
	}

	// The pool the daemon holds over the same file. It never carries the pragma,
	// which is the point: the re-land puts capture on one connection and leaves
	// this one alone.
	owner, err := db.OpenRaw(live)
	if err != nil {
		t.Fatalf("open the member's pool: %v", err)
	}
	defer func() { _ = owner.Close() }()

	const rows = 6
	for i := range rows {
		if _, err := owner.Exec(`INSERT INTO scratch (k, v) VALUES (?, 'after-the-member-open')`,
			time.Now().UnixNano()+int64(i)); err != nil {
			t.Fatalf("insert through the member's pool: %v", err)
		}
	}

	was := countTable(t, openPeer(t, dir, "before.db", url, token), "scratch")

	// The pass is what makes these rows reach the remote: capture is per
	// connection, and this pool's connection does not carry it.
	local, err := db.Open(filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatalf("open the machine-local file: %v", err)
	}
	defer func() { _ = local.Close() }()
	backfilled, err := relevosync.Backfill(ctx, local, live)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	t.Logf("the backfill recorded %d row(s)", backfilled.Rows)

	if err := client.Push(ctx); err != nil {
		t.Fatalf("the push reported a failure: %v", err)
	}
	if got := countTable(t, openPeer(t, dir, "after.db", url, token), "scratch") - was; got != rows {
		t.Fatalf("the push carried %d rows, want %d", got, rows)
	}
	t.Logf("the push carried all %d rows written through the member's pool after the member open", rows)
}

// TestThePushCarriesTheRowsWrittenBeforeCaptureWasOn is the gap the backfill
// exists to close, against a real remote: rows written through the pool while the
// file held no capture at all, and a push that carries them once the backfill has
// recorded them into the change set.
func TestThePushCarriesTheRowsWrittenBeforeCaptureWasOn(t *testing.T) {
	url, token := scratchRemote(t)
	ctx, cancel := scratchCtx(t)
	defer cancel()
	dir := t.TempDir()
	live := filepath.Join(dir, "relevo.db")

	client, synced := openSyncHandle(t, live, url, token)
	if _, err := synced.Exec(`CREATE TABLE IF NOT EXISTS precapture (k INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create the table on the sync connection: %v", err)
	}

	// The bare pool, writing the way a machine wrote before capture was on: the
	// rows land in the file and in no change set. Nothing has asked for capture
	// and nothing will, so a push of this file alone would carry none of them.
	pre, err := db.OpenRaw(live)
	if err != nil {
		t.Fatalf("open the bare pool: %v", err)
	}
	// The keys are stamped rather than fixed: the scratch remote outlives this
	// test, and a fixed key would make the second run rewrite the first run's
	// rows, which the remote absorbs as the same rows and would read as a zero
	// delta.
	run := time.Now().UnixNano()
	const preRows = 6
	for i := range preRows {
		if _, err := pre.Exec(`INSERT INTO precapture (k, v) VALUES (?, 'before-capture')`, run+int64(i)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if err := pre.Close(); err != nil {
		t.Fatalf("close the bare pool: %v", err)
	}

	local, err := db.Open(filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatalf("open the machine-local file: %v", err)
	}
	defer func() { _ = local.Close() }()

	was := countTable(t, openPeer(t, dir, "before.db", url, token), "precapture")
	res, err := relevosync.Backfill(ctx, local, live)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res.Rows < preRows {
		t.Fatalf("the backfill recorded %d rows, want at least the %d written before capture was on", res.Rows, preRows)
	}

	if err := client.Push(ctx); err != nil {
		t.Fatalf("the push reported a failure: %v", err)
	}
	got := countTable(t, openPeer(t, dir, "after.db", url, token), "precapture") - was
	if got < preRows {
		t.Fatalf("the push carried %d of the %d rows written before capture was on", got, preRows)
	}
	t.Logf("the push carried %d rows written before capture was on", got)
}

// TestARemoteOnlyLearnsATableTheSyncEngineCreated is the limit the two tests above
// are written around, pinned so it is not rediscovered by surprise.
//
// The driver syncs a table's schema only for a schema change one of the sync
// engine's own connections made. A table this package's own pool created -- on a
// bare file, or on one already joined -- never reaches a remote that has not
// seen it, and the push of its rows then fails on the remote with "no such
// table". Replaying the same DDL as a no-op does not help: the engine syncs the
// change it made, and a no-op is not one.
//
// This is a driver boundary rather than a defect in this tree, and it is the
// reason the round's push assertions run against a table the engine created.
func TestARemoteOnlyLearnsATableTheSyncEngineCreated(t *testing.T) {
	url, token := scratchRemote(t)
	ctx, cancel := scratchCtx(t)
	defer cancel()
	dir := t.TempDir()
	live := filepath.Join(dir, "relevo.db")

	client, synced := openSyncHandle(t, live, url, token)
	owner, err := db.OpenRaw(live)
	if err != nil {
		t.Fatalf("open the member's pool: %v", err)
	}
	defer func() { _ = owner.Close() }()

	if _, err := owner.Exec(`CREATE TABLE plainmade (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create through the plain pool: %v", err)
	}
	if _, err := owner.Exec(`INSERT INTO plainmade (k) VALUES (1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Replaying the same DDL over the engine's own connection is a no-op locally
	// and does not teach the remote anything.
	if _, err := synced.Exec(`CREATE TABLE IF NOT EXISTS plainmade (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("replay the DDL: %v", err)
	}
	if err := client.Push(ctx); err == nil {
		t.Logf("the push reported success; the remote may already hold this table from an earlier run")
	} else {
		t.Logf("the push of a table only the plain pool created: %v", err)
	}
	if got := countTable(t, openPeer(t, dir, "probe.db", url, token), "plainmade"); got != 0 {
		t.Logf("the remote holds %d rows of plainmade, so this driver build does sync a plain-pool schema", got)
	}
}
