//go:build unix

package owner

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
	turso "turso.tech/database/tursogo"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// openPoisonTurso opens path with the engine the owner serves, without the
// per-connection pragmas the database package adds: the poison lives in the
// files the pool reads, not in how a connection is set up.
func openPoisonTurso(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := turso.NewConnector(path + "?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("turso connector: %v", err)
	}
	return sql.OpenDB(conn)
}

// openPoisonLegacy opens path with the pre-Turso driver, which writes rows
// into the -wal and holds the -shm the truncation below leaves stale.
func openPoisonLegacy(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path}).String() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("modernc open: %v", err)
	}
	return pool
}

// TestShortReadWALFiresTheHealHookOnce pins the owner's half of the WAL heal:
// a pool poisoned by a WAL reset under it fails every statement with a short
// read, the client still gets that error, and the hook fires exactly once no
// matter how many statements fail. A plain failure never fires it: only the
// poison asks for a re-exec.
func TestShortReadWALFiresTheHealHookOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.db")

	seeder := openPoisonLegacy(t, path)
	defer func() { _ = seeder.Close() }()
	if _, err := seeder.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := seeder.Exec(`INSERT INTO t (n) VALUES (?)`, i); err != nil {
			t.Fatalf("seed insert %d: %v", i, err)
		}
	}

	// The live reader adopts the WAL index the reset leaves stale; it stays
	// open the way the daemon's pool does.
	reader := openPoisonTurso(t, path)
	defer func() { _ = reader.Close() }()
	var seeded int
	if err := reader.QueryRow(`SELECT count(*) FROM t`).Scan(&seeded); err != nil {
		t.Fatalf("pre-poison read: %v", err)
	}
	if seeded != 50 {
		t.Fatalf("pre-poison count = %d, want 50", seeded)
	}

	// Another handle resets the WAL under the live pool: every frame moves
	// into the main file and the WAL is truncated away.
	poisoner := openPoisonLegacy(t, path)
	if _, err := poisoner.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("poison checkpoint: %v", err)
	}
	if err := poisoner.Close(); err != nil {
		t.Fatalf("poisoner close: %v", err)
	}

	victim := openPoisonTurso(t, path)
	defer func() { _ = victim.Close() }()

	var fired atomic.Int64
	l, sock := shortListener(t)
	srv := New(victim, 3, 9, "01ORIGIN", nil)
	srv.OnShortReadWAL = func() { fired.Add(1) }
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	cli, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("client open: %v", err)
	}
	defer func() { _ = cli.Close() }()
	cli.SetMaxOpenConns(1)

	query := func() error {
		var n int
		return cli.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
	}
	if err := query(); !wire.IsShortReadWAL(err) {
		t.Fatalf("first query err = %v, want the short WAL read", err)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("hook fired %d times after one failing statement, want 1", got)
	}
	if err := query(); !wire.IsShortReadWAL(err) {
		t.Fatalf("second query err = %v, want the short WAL read", err)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("hook fired %d times after two failing statements, want 1", got)
	}
}

// TestPlainFailureNeverFiresTheHealHook pins the classifier: a statement that
// fails for any other reason leaves the hook alone, so only a poisoned pool
// asks for a re-exec. It fails if sendError fires the hook without checking
// the error.
func TestPlainFailureNeverFiresTheHealHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.db")
	healthy := openPoisonTurso(t, path)
	defer func() { _ = healthy.Close() }()
	if _, err := healthy.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	var fired atomic.Int64
	l, sock := shortListener(t)
	srv := New(healthy, 3, 9, "01ORIGIN", nil)
	srv.OnShortReadWAL = func() { fired.Add(1) }
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	cli, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("client open: %v", err)
	}
	defer func() { _ = cli.Close() }()

	var n int
	plainErr := cli.QueryRow(`SELECT n FROM missing`).Scan(&n)
	if plainErr == nil {
		t.Fatalf("plain failing query err = nil, want the missing-table error")
	}
	if wire.IsShortReadWAL(plainErr) {
		t.Fatalf("plain failing query err = %v, want an error that is not a short read", plainErr)
	}
	if got := fired.Load(); got != 0 {
		t.Fatalf("hook fired %d times after a plain failure, want 0", got)
	}
}
