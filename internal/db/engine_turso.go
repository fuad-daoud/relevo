//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	turso_libs "github.com/tursodatabase/turso-go-platform-libs"
	turso "turso.tech/database/tursogo"

	// The default build keeps the old driver linked: round 2's one-time
	// conversion opens it to read a pre-swap database.
	_ "modernc.org/sqlite"
)

// engineName is the driver this build opens databases with.
const engineName = "turso"

// The SQLite primary result codes Turso's sentinels map onto, so a caller
// masks the same codes a modernc error carries.
const (
	tursoBusy       = 5
	tursoConstraint = 19
	tursoReadOnly   = 8
	sqliteIOErr     = 10
)

// cacheEnv is the directory the loader extracts the embedded library into.
const cacheEnv = "TURSO_GO_CACHE_DIR"

// maxOpenConns bounds how many connections one pool may have open at once.
//
// Database/sql opens a connection per concurrent caller until this bound, so
// without it a burst of requests is a burst of connects -- and every connect
// pays the file's own open cost against a database that has a single write
// slot behind it, so the unbounded form is what turns a busy write slot into
// thousands of blocked connects rather than one that surfaces as an error.
//
// The number is the daemon's own measured fan-out, not a guess. Sampling
// `Stats().OpenConnections` on the shared pool while the served request paths
// ran concurrently against it gave a peak of 9 open connections at 128
// concurrent clients; the store's own concurrent readers and writers, which is
// what an idle tick plus a served request amount to, peaked at 15. Eight times
// the measured ceiling leaves room for every caller the daemon admits at once
// while still refusing the pile-up, and the bound is a pool ceiling rather than
// a queue: past it a caller waits on database/sql's own semaphore instead of
// opening a connection, so the failure is a bounded wait and then an error.
const maxOpenConns = 128

// openPool opens a pool on path with Turso. Turso parses no `file:` URI and
// ends the path at the first `?`, so a path carrying one would silently open
// another file: that is refused. Settings that modernc takes in the DSN are
// applied per connection instead.
func openPool(path string, busy time.Duration, readOnly bool) (*sql.DB, error) {
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("db: open %s: the path contains '?': %w", path, ErrOpen)
	}
	if err := prepareEngine(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("db: open %s: engine: %w: %w", path, ErrOpen, err)
	}
	if err := prepareTempDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("db: open %s: temp dir: %w: %w", path, ErrOpen, err)
	}
	// Pre-create the -wal owner-only before the engine derives anything from
	// it; an existing file is fine.
	if err := createFile(path + "-wal"); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("db: open %s: create -wal: %w: %w", path, ErrOpen, err)
	}
	// A database with no SQLite header is one this build is creating, not a
	// legacy file: it is marked at creation, so the one-time conversion never
	// touches a file Turso wrote. The read-only open never marks, because the
	// marker is a write.
	fresh, err := freshDatabase(path)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: read the header: %w: %w", path, ErrOpen, err)
	}
	conn, err := newTursoConnector(path, busy.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("db: open %s: connector: %w: %w", path, ErrOpen, err)
	}
	pool := sql.OpenDB(repairConnector{Connector: &pragmaConnector{base: conn, pragmas: openPragmas(readOnly)}})
	pool.SetMaxOpenConns(maxOpenConns)
	pool.SetMaxIdleConns(maxOpenConns)
	if fresh && !readOnly {
		if err := markFreshDatabase(pool); err != nil {
			_ = pool.Close()
			return nil, fmt.Errorf("db: open %s: mark: %w", path, err)
		}
	}
	return pool, nil
}

// newTursoConnector is the engine's connector for a path: the DSN the pool
// opens with, busy timeout included.
func newTursoConnector(path string, busyMS int64) (driver.Connector, error) {
	conn, err := turso.NewConnector(fmt.Sprintf("%s?_busy_timeout=%d", path, busyMS))
	if err != nil {
		return nil, fmt.Errorf("db: open %s: connector: %w: %w", path, ErrOpen, err)
	}
	return conn, nil
}

// freshDatabase reports whether path has no SQLite header yet: absent, empty or
// too short to hold one. A file SQLite wrote always carries the header, so only
// a database this build creates is fresh.
func freshDatabase(path string) (bool, error) {
	_, fresh, err := fileMarker(path)
	return fresh, err
}

// openPragmas is the per-connection pragma sequence: WAL always, and then the
// guard that matches the handle's mode.
func openPragmas(readOnly bool) []string {
	if readOnly {
		return []string{"PRAGMA journal_mode = wal", "PRAGMA query_only = 1", "PRAGMA temp_store = MEMORY"}
	}
	return []string{"PRAGMA journal_mode = wal", "PRAGMA foreign_keys = ON", "PRAGMA temp_store = MEMORY"}
}

// pragmaConnector runs pragmas on every new driver connection before
// database/sql can use it. Turso's DSN has no _pragma option, and the mode word
// and per-handle guards must hold on each pooled connection, not just the first.
type pragmaConnector struct {
	base    driver.Connector
	pragmas []string
}

func (c *pragmaConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	ex, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("db: %s connection cannot run pragmas: %w", engineName, ErrOpen)
	}
	for _, p := range c.pragmas {
		if _, err := ex.ExecContext(ctx, p, nil); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("db: %s: %w", p, err)
		}
	}
	return conn, nil
}

func (c *pragmaConnector) Driver() driver.Driver { return c.base.Driver() }

// engineCode maps Turso's sentinel errors onto the SQLite codes modernc's error
// type carries, so mapBusy and mapMasterMindKey mask the same values either
// way. Turso's errors wrap a sentinel with fmt.Errorf, so errors.Is is the test.
func engineCode(err error) (int, int, bool) {
	switch {
	case errors.Is(err, turso.ErrTursoBusy):
		return tursoBusy, tursoBusy, true
	case errors.Is(err, turso.ErrTursoConstraint):
		return tursoConstraint, tursoConstraint, true
	case errors.Is(err, turso.ErrTursoReadOnly):
		return tursoReadOnly, tursoReadOnly, true
	case errors.Is(err, turso.ErrTursoGeneric) && isTursoIOErr(err):
		return sqliteIOErr, sqliteIOErr, true
	}
	return 0, 0, false
}

// isTursoIOErr reports whether err wraps an ErrTursoGeneric whose error text
// indicates an I/O failure (starts with "I/O error").
func isTursoIOErr(err error) bool {
	for e := err; e != nil; {
		msg := e.Error()
		if strings.HasPrefix(msg, "I/O error") || strings.HasPrefix(strings.TrimPrefix(msg, "turso: error: "), "I/O error") {
			return true
		}
		e = errors.Unwrap(e)
	}
	return false
}

// engineStatus reports the Turso library's state under root. No library under
// root/turso-go means the daemon has not opened a database there yet: that is
// Missing, not an error. Otherwise the extracted copy is verified through the
// same loader an open uses, with the cache directory pointed at root, and the
// load is never retried: a mismatch is reported, not healed.
func engineStatus(root string) EngineState {
	cacheDir := filepath.Join(root, "turso-go")
	status := EngineState{Name: engineName, CacheDir: cacheDir}
	matches, err := filepath.Glob(filepath.Join(cacheDir, "*", libraryFileName()))
	if err != nil {
		status.Err = fmt.Errorf("find the extracted turso library: %w", err)
		return status
	}
	if len(matches) == 0 {
		status.Missing = true
		return status
	}
	status.Library = matches[0]

	prev, had := os.LookupEnv(cacheEnv)
	if err := os.Setenv(cacheEnv, root); err != nil {
		status.Err = fmt.Errorf("set %s: %w", cacheEnv, err)
		return status
	}
	defer restoreEnv(cacheEnv, prev, had)

	if _, err := turso_libs.LoadTursoLibrary(turso_libs.LoadTursoLibraryConfig{}); err != nil {
		status.Err = err
	}
	return status
}

// engineLocked reports whether err is the engine's own refusal to open a file
// another process holds. Turso reports it as ErrTursoGeneric carrying "locked
// by another process" (core/io/unix.rs); modernc's own lock is a busy error,
// which mapBusy already handles, so it never reaches here.
func engineLocked(err error) bool {
	return errors.Is(err, turso.ErrTursoGeneric) && strings.Contains(err.Error(), "locked by another process")
}

// vacuumIntoStmt builds Turso's literal VACUUM INTO form: it accepts a string
// literal only, not a placeholder. A path holding a quote cannot be embedded in
// a literal, so it is refused.
func vacuumIntoStmt(path string) (string, []any, error) {
	if strings.Contains(path, "'") {
		return "", nil, fmt.Errorf("db: vacuum into %s: the path contains a quote: %w", path, ErrInvalid)
	}
	return `VACUUM INTO '` + path + `'`, nil, nil
}

// enginePrepareOnce keeps the library load to one per process: the first
// database the process opens directly fixes the directory it is extracted into.
var (
	enginePrepareOnce sync.Once
	enginePrepareErr  error
)

// prepareEngine extracts and loads the embedded Turso library once per process
// beneath dir, so the extracted library sits beside the database rather than in
// a shared user cache. The error of the first call is kept and returned by every
// later one.
func prepareEngine(dir string) error {
	enginePrepareOnce.Do(func() { enginePrepareErr = extractAndLoad(dir) })
	return enginePrepareErr
}

// prepareTempDir creates dir/tmp owner-only and sets TURSO_TMPDIR and
// SQLITE_TMPDIR to it when not already set in the environment, so the library's
// temp files land beside the database instead of in /tmp.
func prepareTempDir(dir string) error {
	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", tmpDir, err)
	}
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpDir, err)
	}
	for _, env := range []string{"TURSO_TMPDIR", "SQLITE_TMPDIR"} {
		if val, ok := os.LookupEnv(env); !ok || val == "" {
			if err := os.Setenv(env, tmpDir); err != nil {
				return fmt.Errorf("set %s: %w", env, err)
			}
		} else {
			_ = os.MkdirAll(val, 0o700)
		}
	}
	return nil
}

// extractAndLoad creates dir/turso-go owner-only, points the loader's cache at
// dir for the duration, and loads the library. A cached copy whose hash does not
// match is removed and the load retried once, so a truncated or corrupted cache
// heals instead of bricking every later open.
func extractAndLoad(dir string) error {
	if err := prepareTempDir(dir); err != nil {
		return err
	}
	cacheDir := filepath.Join(dir, "turso-go")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", cacheDir, err)
	}
	if err := os.Chmod(cacheDir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", cacheDir, err)
	}
	prev, had := os.LookupEnv(cacheEnv)
	if err := os.Setenv(cacheEnv, dir); err != nil {
		return fmt.Errorf("set %s: %w", cacheEnv, err)
	}
	defer restoreEnv(cacheEnv, prev, had)

	cfg := turso_libs.LoadTursoLibraryConfig{}
	if _, err := turso_libs.LoadTursoLibrary(cfg); err != nil {
		if !strings.Contains(err.Error(), "hash sum mismatch") {
			return fmt.Errorf("load turso library: %w", err)
		}
		if rerr := removeCachedLibrary(dir); rerr != nil {
			return rerr
		}
		if _, err := turso_libs.LoadTursoLibrary(cfg); err != nil {
			return fmt.Errorf("load turso library after clearing the corrupt cache: %w", err)
		}
	}
	return initLibrary()
}

// initLibrary calls turso.InitLibrary, turning the panic it raises when the
// library cannot load into an error, so the failure stays inside the seam
// instead of escaping a driver call.
func initLibrary() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("init turso library: %v", r)
		}
	}()
	turso.InitLibrary(turso_libs.LoadTursoLibraryConfig{})
	return nil
}

// restoreEnv puts the cache variable back the way it was, so the seam does not
// leak a setting into the rest of the process.
func restoreEnv(key, prev string, had bool) {
	if had {
		_ = os.Setenv(key, prev)
		return
	}
	_ = os.Unsetenv(key)
}

// removeCachedLibrary deletes an extracted library under dir's cache, so the
// next load re-extracts it from the embedded copy.
func removeCachedLibrary(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "turso-go", "*", libraryFileName()))
	if err != nil {
		return fmt.Errorf("find the cached turso library: %w", err)
	}
	for _, match := range matches {
		if err := os.Remove(match); err != nil {
			return fmt.Errorf("remove the cached turso library %s: %w", match, err)
		}
	}
	return nil
}

// libraryFileName is the embedded library's name on this platform, matching
// what the loader extracts.
func libraryFileName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libturso_sync_sdk_kit.dylib"
	case "windows":
		return "turso_sync_sdk_kit.dll"
	default:
		return "libturso_sync_sdk_kit.so"
	}
}
