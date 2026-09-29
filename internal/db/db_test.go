package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenCreatesAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	v, err := d.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if want := embeddedVersion(t); v != want {
		t.Errorf("Version() = %d, want %d", v, want)
	}

	var mode string
	if err := d.sqlDB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	d1, err := Open(path)
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	_ = d1.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("Open (2nd): %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })

	v, err := d2.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if want := embeddedVersion(t); v != want {
		t.Errorf("Version() = %d, want %d", v, want)
	}

	var count int
	if err := d2.sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&count); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if want := embeddedVersion(t); count != want {
		t.Errorf("schema_version has %d rows, want %d", count, want)
	}
}

// TestOpenCurrentSchemaWhileAnotherProcessWrites pins that Open on a database
// whose schema is already current takes no write lock: it must return
// quickly even while another connection holds BEGIN IMMEDIATE.
func TestOpenCurrentSchemaWhileAnotherProcessWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	_ = d.Close()

	oldTimeout := busyTimeoutMS
	busyTimeoutMS = 200
	t.Cleanup(func() { busyTimeoutMS = oldTimeout })

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path, busyTimeoutMS)
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open writer: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	ctx := context.Background()
	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatalf("writer.Conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") })

	start := time.Now()
	d2, err := Open(path)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Open (2nd) while another connection holds the write lock: %v", err)
	}
	defer func() { _ = d2.Close() }()
	if elapsed >= 2*time.Second {
		t.Errorf("Open took %s, want < 2s: a current schema must not wait on the write lock", elapsed)
	}
	if d2.Newer() {
		t.Error("Newer() = true, want false")
	}
}

// TestOpenLeavesANewerSchemaAlone pins that a newer schema is not migrated.
func TestOpenLeavesANewerSchemaAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	before := seedNewerSchema(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if !d.Newer() {
		t.Fatal("Newer() = false, want true for a schema above this relevo's migrations")
	}
	have, know := d.SchemaVersions()
	if want := embeddedVersion(t); have != 99 || know != want {
		t.Errorf("SchemaVersions() = (%d, %d), want (99, %d)", have, know, want)
	}

	var after int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&after); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if after != before {
		t.Errorf("table count = %d, want %d: a newer schema must not be migrated", after, before)
	}
}

func TestCheckMigrateRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedNewerSchema(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := d.CheckMigrate(); err == nil {
		t.Fatal("CheckMigrate() = nil, want an error on a newer schema")
	} else if !errors.Is(err, ErrNewerSchema) {
		t.Errorf("CheckMigrate() error = %v, want errors.Is(..., ErrNewerSchema)", err)
	}

	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if err := fresh.CheckMigrate(); err != nil {
		t.Errorf("CheckMigrate() on a fresh schema = %v, want nil", err)
	}
}

// TestConcurrentOpenAppliesEachMigrationOnce pins the migration guard: the
// in-transaction re-check is what makes one row per migration hold.
func TestConcurrentOpenAppliesEachMigrationOnce(t *testing.T) {
	for i := 0; i < 20; i++ {
		path := filepath.Join(t.TempDir(), "relevo.db")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j := range errs {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				d, err := Open(path)
				if err != nil {
					errs[j] = err
					return
				}
				_ = d.Close()
			}(j)
		}
		wg.Wait()

		for j, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: Open %d: %v", i, j, err)
			}
		}

		sqlDB, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatalf("iteration %d: sql.Open: %v", i, err)
		}
		rows, err := sqlDB.Query(`SELECT version FROM schema_version`)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatalf("iteration %d: query schema_version: %v", i, err)
		}
		versions := map[int]int{}
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				_ = rows.Close()
				_ = sqlDB.Close()
				t.Fatalf("iteration %d: scan version: %v", i, err)
			}
			versions[v]++
		}
		_ = rows.Close()
		_ = sqlDB.Close()

		if want := embeddedVersion(t); len(versions) != want {
			t.Fatalf("iteration %d: schema_version rows = %v, want one row per migration (%d)", i, versions, want)
		}
		for v := 1; v <= embeddedVersion(t); v++ {
			if versions[v] != 1 {
				t.Fatalf("iteration %d: version %d appears %d times, want exactly one", i, v, versions[v])
			}
		}
	}
}

// TestOpenSetsJournalSizeLimit pins that sqlite truncates the -wal file after a
// checkpoint instead of leaving it at a write burst's high-water size.
func TestOpenSetsJournalSizeLimit(t *testing.T) {
	d := openTestDB(t)

	var limit int
	if err := d.sqlDB.QueryRow(`PRAGMA journal_size_limit`).Scan(&limit); err != nil {
		t.Fatalf("PRAGMA journal_size_limit: %v", err)
	}
	if limit != journalSizeLimit {
		t.Errorf("journal_size_limit = %d, want %d", limit, journalSizeLimit)
	}
}

func TestTxRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedNewerSchema(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	err = d.Tx(func(tx *Tx) error { return nil })
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("Tx on a newer schema = %v, want errors.Is(..., ErrNewerSchema)", err)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	origin := "https://example.test/repo.git"
	wantErr := errFake

	err = d.Tx(func(tx *Tx) error {
		if _, err := tx.UpsertRepo(Repo{OriginURL: &origin}); err != nil {
			t.Fatalf("UpsertRepo inside Tx: %v", err)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Tx returned %v, want %v", err, wantErr)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM repo`).Scan(&count); err != nil {
		t.Fatalf("count repo: %v", err)
	}
	if count != 0 {
		t.Errorf("repo has %d rows after rollback, want 0", count)
	}
}

// TestTxRetriesABusyBegin pins that a busy BEGIN IMMEDIATE is retried, and fn
// still runs exactly once.
func TestTxRetriesABusyBegin(t *testing.T) {
	oldTimeout, oldRetry := busyTimeoutMS, beginRetryFor
	busyTimeoutMS, beginRetryFor = 20, 3*time.Second
	t.Cleanup(func() { busyTimeoutMS, beginRetryFor = oldTimeout, oldRetry })

	path := filepath.Join(t.TempDir(), "relevo.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("Open d1: %v", err)
	}
	t.Cleanup(func() { _ = d1.Close() })
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("Open d2: %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })

	holding := make(chan struct{})
	var once sync.Once
	d1Done := make(chan error, 1)
	go func() {
		d1Done <- d1.Tx(func(tx *Tx) error {
			once.Do(func() { close(holding) })
			time.Sleep(300 * time.Millisecond)
			return nil
		})
	}()
	<-holding

	var mu sync.Mutex
	runs := 0
	err = d2.Tx(func(tx *Tx) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil
	})
	if cerr := <-d1Done; cerr != nil {
		t.Fatalf("d1.Tx: %v", cerr)
	}
	if err != nil {
		t.Fatalf("d2.Tx = %v, want nil (it must retry past d1's lock)", err)
	}
	if runs != 1 {
		t.Errorf("d2's fn ran %d times, want exactly 1", runs)
	}
}

// TestTxGivesUpOnABusyBeginAfterTheDeadline pins the beginRetryFor bound.
func TestTxGivesUpOnABusyBeginAfterTheDeadline(t *testing.T) {
	oldTimeout, oldRetry := busyTimeoutMS, beginRetryFor
	busyTimeoutMS, beginRetryFor = 20, 200*time.Millisecond
	t.Cleanup(func() { busyTimeoutMS, beginRetryFor = oldTimeout, oldRetry })

	path := filepath.Join(t.TempDir(), "relevo.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("Open d1: %v", err)
	}
	t.Cleanup(func() { _ = d1.Close() })
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("Open d2: %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })

	holding := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d1Done := make(chan error, 1)
	go func() {
		d1Done <- d1.Tx(func(tx *Tx) error {
			once.Do(func() { close(holding) })
			<-release
			return nil
		})
	}()
	<-holding

	ran := false
	err = d2.Tx(func(tx *Tx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("d2.Tx = %v, want errors.Is(..., ErrBusy)", err)
	}
	if !strings.Contains(err.Error(), "db: tx begin") {
		t.Errorf("d2.Tx error = %q, want it to contain %q", err, "db: tx begin")
	}
	if ran {
		t.Error("d2's fn ran, but its BEGIN never succeeded")
	}

	close(release)
	if cerr := <-d1Done; cerr != nil {
		t.Fatalf("d1.Tx: %v", cerr)
	}
}

// TestOpenWithShortBusyFailsFast pins that OpenWith's BeginRetry bounds a busy
// Tx, so a caller that must fail fast does not wait out the default window.
func TestOpenWithShortBusyFailsFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := OpenWith(path, Options{BusyTimeout: 20 * time.Millisecond, BeginRetry: time.Millisecond})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	holding := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- d.Tx(func(tx *Tx) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	start := time.Now()
	err = d.Tx(func(tx *Tx) error { return nil })
	elapsed := time.Since(start)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second Tx = %v, want errors.Is(..., ErrBusy)", err)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("second Tx took %s, want under 2s: BeginRetry must bound the wait", elapsed)
	}

	close(release)
	if cerr := <-holderDone; cerr != nil {
		t.Fatalf("first Tx: %v", cerr)
	}
}

// TestBackupToCopiesEveryRowAndRefusesAnExistingPath pins the copy and the guard.
func TestBackupToCopiesEveryRowAndRefusesAnExistingPath(t *testing.T) {
	d := openTestDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	bindingID, err := d.UpsertBinding(newTestBinding("webshop", now))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	if _, err := d.UpsertRound(Round{BindingID: bindingID, Number: 1, StartedAt: now, Outcome: OutcomeOpen}); err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}
	if err := d.KVPut("probe", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	want, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	path := filepath.Join(t.TempDir(), "copy.db")
	if err := d.BackupTo(path); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(backup): %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %o, want 600", perm)
	}

	copyDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open(backup): %v", err)
	}
	t.Cleanup(func() { _ = copyDB.Close() })

	got, err := copyDB.Stats()
	if err != nil {
		t.Fatalf("Stats(backup): %v", err)
	}
	for tbl, n := range want.Rows {
		if got.Rows[tbl] != n {
			t.Errorf("backup Rows[%s] = %d, want %d", tbl, got.Rows[tbl], n)
		}
	}

	err = d.BackupTo(path)
	if err == nil {
		t.Fatal("BackupTo(a path that already exists) = nil, want an error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("BackupTo(existing) err = %v, want it to name %s", err, path)
	}
}

// TestVacuumKeepsRows pins that Vacuum leaves the rows readable.
func TestVacuumKeepsRows(t *testing.T) {
	d := openTestDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	bindingID, err := d.UpsertBinding(newTestBinding("webshop", now))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	if _, err := d.UpsertRound(Round{BindingID: bindingID, Number: 1, StartedAt: now, Outcome: OutcomeOpen}); err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}

	if err := d.Vacuum(); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}

	rounds, err := d.Rounds(bindingID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if len(rounds) != 1 {
		t.Errorf("rounds after Vacuum = %d, want 1", len(rounds))
	}
}
