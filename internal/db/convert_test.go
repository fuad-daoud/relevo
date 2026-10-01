//go:build !modernc

package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// openSQLiteTestDB opens path with modernc, the driver a pre-Turso relevo wrote
// the file with.
func openSQLiteTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path}).String() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("modernc open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// sqliteWALSnapshot writes a fresh database through modernc and, while the pool
// is still open, snapshots its main file, -wal and -shm into dst. dst is then
// the crash image of a modernc database whose committed rows live only in the
// -wal, with a -shm beside them: the state convertLegacy exists to handle.
func sqliteWALSnapshot(t *testing.T, dst string, seed func(pool *sql.DB)) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src.db")
	pool := openSQLiteTestDB(t, src)
	seed(pool)

	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(src + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s%s: %v", src, suffix, err)
		}
		if err := os.WriteFile(dst+suffix, data, 0o600); err != nil {
			t.Fatalf("write %s%s: %v", dst, suffix, err)
		}
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("snapshot main file: %v", err)
	}
	if _, err := os.Stat(dst + "-shm"); err != nil {
		t.Fatalf("the fixture has no -shm, so it never triggers conversion: %v", err)
	}
}

// snapshotFacts reads every file in path's directory, keyed by name, with its
// size and modification time, so a no-op conversion is provably no-op.
func snapshotFacts(t *testing.T, path string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			t.Fatalf("info %s: %v", e.Name(), ierr)
		}
		out[e.Name()] = fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
	}
	return out
}

// equalFacts reports whether two snapshotFacts maps are identical.
func equalFacts(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// seedRows creates t and inserts n rows through pool.
func seedRows(t *testing.T, pool *sql.DB, n int) {
	t.Helper()
	if _, err := pool.Exec(`CREATE TABLE t (n INTEGER, s TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := pool.Exec(`INSERT INTO t (n, s) VALUES (?, ?)`, i, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
}

// tursoRowCount opens path with Turso's raw driver and counts t's rows, which
// fails if Turso cannot read the file at all.
func tursoRowCount(t *testing.T, path string) int {
	t.Helper()
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("Turso OpenRaw: %v", err)
	}
	defer func() { _ = pool.Close() }()
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("Turso count: %v", err)
	}
	return n
}

// TestTursoReadsRowsLeftInASQLiteWAL records the fact the conversion is built
// on: whether Turso, without conversion, can read a crash-snapshot copy of a
// modernc database whose committed rows are still only in its -wal. It fails
// neither way: the point is the recorded answer.
func TestTursoReadsRowsLeftInASQLiteWAL(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "relevo.db")
	sqliteWALSnapshot(t, dst, func(pool *sql.DB) { seedRows(t, pool, 5) })

	pool, err := OpenRaw(dst)
	if err != nil {
		t.Logf("Turso refused the SQLite WAL image at open: %v", err)
		return
	}
	defer func() { _ = pool.Close() }()
	var n int
	if qerr := pool.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr != nil {
		t.Logf("Turso opened the SQLite WAL image but could not read t: %v", qerr)
		return
	}
	t.Logf("Turso read %d rows from the SQLite WAL image without conversion", n)
}

// TestConvertFindsEveryWALOnlyRowUnderTurso pins the conversion's purpose: rows
// that live only in SQLite's -wal are present when Turso reads the converted
// file.
func TestConvertFindsEveryWALOnlyRowUnderTurso(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "relevo.db")
	sqliteWALSnapshot(t, dst, func(pool *sql.DB) { seedRows(t, pool, 7) })

	if err := convertLegacy(dst); err != nil {
		t.Fatalf("convertLegacy: %v", err)
	}
	if got := tursoRowCount(t, dst); got != 7 {
		t.Errorf("Turso read %d rows, want all 7 the -wal held", got)
	}
}

// TestConvertIsIdempotent pins that the conversion runs once: a second call
// finds no -shm and changes nothing.
func TestConvertIsIdempotent(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "relevo.db")
	sqliteWALSnapshot(t, dst, func(pool *sql.DB) { seedRows(t, pool, 3) })

	if err := convertLegacy(dst); err != nil {
		t.Fatalf("first convertLegacy: %v", err)
	}
	before := snapshotFacts(t, dst)
	if err := convertLegacy(dst); err != nil {
		t.Fatalf("second convertLegacy: %v", err)
	}
	after := snapshotFacts(t, dst)
	if !equalFacts(before, after) {
		t.Errorf("the second conversion changed the directory: %v -> %v", before, after)
	}
}

// TestConvertSurvivesAKillAfterEveryStep pins the kill safety: stopping the
// conversion right after any step leaves a file the next conversion finishes,
// with every row readable by Turso.
func TestConvertSurvivesAKillAfterEveryStep(t *testing.T) {
	killed := errors.New("killed")
	for _, step := range []string{convertBackup, convertCheckpoint, convertRepair, convertClosed, convertSidecars} {
		t.Run(step, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "relevo.db")
			sqliteWALSnapshot(t, dst, func(pool *sql.DB) { seedRows(t, pool, 4) })

			convertStepHook = func(s string) error {
				if s == step {
					return killed
				}
				return nil
			}
			t.Cleanup(func() { convertStepHook = nil })

			if err := convertLegacy(dst); !errors.Is(err, killed) {
				t.Fatalf("convertLegacy stopping after %s = %v, want the kill sentinel", step, err)
			}
			convertStepHook = nil

			if err := convertLegacy(dst); err != nil {
				t.Fatalf("the conversion after a kill at %s: %v", step, err)
			}
			if got := tursoRowCount(t, dst); got != 4 {
				t.Errorf("after a kill at %s Turso read %d rows, want 4", step, got)
			}
		})
	}
}

// TestConvertLeavesNoSQLiteSidecars pins the end state: no -wal and no -shm
// survive the conversion, so the next open does not convert again. It also
// checks the checkpoint drained the -wal at its own step, where a skipped
// checkpoint is the only place the frames could still be seen.
func TestConvertLeavesNoSQLiteSidecars(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "relevo.db")
	sqliteWALSnapshot(t, dst, func(pool *sql.DB) { seedRows(t, pool, 3) })

	convertStepHook = func(step string) error {
		if step == convertCheckpoint {
			if size, err := walSize(dst); err != nil || size != 0 {
				t.Errorf("the -wal holds %d bytes at the checkpoint step (err=%v), want it drained", size, err)
			}
		}
		return nil
	}
	t.Cleanup(func() { convertStepHook = nil })

	if err := convertLegacy(dst); err != nil {
		t.Fatalf("convertLegacy: %v", err)
	}
	convertStepHook = nil
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dst + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s%s still exists after conversion: stat error = %v", dst, suffix, err)
		}
	}
}

// TestConvertSkipsAFileSQLiteClosedCleanly pins the trigger: a file with no
// -shm is not converted and gets no backup.
func TestConvertSkipsAFileSQLiteClosedCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	pool := openSQLiteTestDB(t, path)
	seedRows(t, pool, 2)
	if err := pool.Close(); err != nil {
		t.Fatalf("clean close: %v", err)
	}
	if _, err := os.Stat(path + "-shm"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a clean close left a -shm: %v", err)
	}

	if err := convertLegacy(path); err != nil {
		t.Fatalf("convertLegacy on a clean file: %v", err)
	}
	if _, err := os.Stat(path + ".pre-turso"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a clean-close file was backed up: stat error = %v, want not-exist", err)
	}
}

// TestConvertRepairsInvalidTextInAWALOnlyRow pins the amendment: a text value
// that is not valid UTF-8, living only in the -wal, is repaired by the
// conversion, so Turso reads every row.
func TestConvertRepairsInvalidTextInAWALOnlyRow(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "relevo.db")
	sqliteWALSnapshot(t, dst, func(pool *sql.DB) {
		if _, err := pool.Exec(`CREATE TABLE t (n INTEGER, s TEXT)`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		bad := string([]byte{0xff, 0xfe, 'x'})
		if _, err := pool.Exec(`INSERT INTO t (n, s) VALUES (?, ?)`, 0, bad); err != nil {
			t.Fatalf("insert invalid text: %v", err)
		}
		if _, err := pool.Exec(`INSERT INTO t (n, s) VALUES (?, ?)`, 1, "ok"); err != nil {
			t.Fatalf("insert valid text: %v", err)
		}
	})

	if err := convertLegacy(dst); err != nil {
		t.Fatalf("convertLegacy: %v", err)
	}
	pool, err := OpenRaw(dst)
	if err != nil {
		t.Fatalf("Turso OpenRaw after conversion: %v", err)
	}
	defer func() { _ = pool.Close() }()

	var (
		count int
		value string
	)
	if err := pool.QueryRow(`SELECT count(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("Turso count: %v", err)
	}
	if count != 2 {
		t.Errorf("Turso read %d rows, want 2", count)
	}
	if err := pool.QueryRow(`SELECT s FROM t WHERE n = 0`).Scan(&value); err != nil {
		t.Fatalf("Turso read the repaired row: %v", err)
	}
	if !utf8.ValidString(value) || !strings.Contains(value, "x") {
		t.Errorf("the repaired value = %q, want valid UTF-8 ending in x", value)
	}
}

// TestTursoWrittenFileOpensUnderSQLite is the way back: a file Turso wrote
// opens under modernc with its rows, its WAL mode and a clean integrity check.
func TestTursoWrittenFileOpensUnderSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Turso Open: %v", err)
	}
	if _, err := d.sqlDB.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := d.sqlDB.Exec(`INSERT INTO t (n) VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	pool := openSQLiteTestDB(t, path)
	var mode string
	if err := pool.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var check string
	if err := pool.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if check != "ok" {
		t.Errorf("integrity_check = %q, want ok", check)
	}
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("rows = %d, want the 3 Turso wrote", n)
	}
}

// TestSQLiteReadsATursoWAL is the crash path back: a copy taken while a Turso
// handle holds un-checkpointed frames opens under modernc with every committed
// row. A failure here means the way back after a crash is broken.
func TestSQLiteReadsATursoWAL(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.db")
	d, err := Open(src)
	if err != nil {
		t.Fatalf("Turso Open: %v", err)
	}
	if _, err := d.sqlDB.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := d.sqlDB.Exec(`INSERT INTO t (n) VALUES (1), (2), (3), (4)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, rerr := os.ReadFile(src + suffix)
		if errors.Is(rerr, os.ErrNotExist) {
			continue
		}
		if rerr != nil {
			t.Fatalf("read %s%s: %v", src, suffix, rerr)
		}
		if werr := os.WriteFile(dst+suffix, data, 0o600); werr != nil {
			t.Fatalf("write %s%s: %v", dst, suffix, werr)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close Turso handle: %v", err)
	}

	pool := openSQLiteTestDB(t, dst)
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("modernc count from the Turso WAL copy: %v", err)
	}
	if n != 4 {
		t.Errorf("modernc read %d rows from Turso's WAL, want all 4", n)
	}
}
