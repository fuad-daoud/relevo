//go:build !modernc

package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	turso "turso.tech/database/tursogo"
)

// TestEngineCodeMapsAReadOnlyError pins the read-only mapping: a write against a
// read-only connection reports code 8, the primary code modernc's error carries.
func TestEngineCodeMapsAReadOnlyError(t *testing.T) {
	err := fmt.Errorf("%w: attempt to write a readonly database", turso.ErrTursoReadOnly)
	if code, _, ok := engineCode(err); !ok || code != tursoReadOnly {
		t.Errorf("engineCode(%v) = (%d, ok %v), want the read-only code", err, code, ok)
	}
}

// TestEngineCodeMapsARealBusyAndConstraint pins the Turso error mapping against
// real driver errors: a constraint violation becomes 19 and a busy 5, the codes
// modernc's error type carries and mapBusy and mapMasterMindKey mask.
func TestEngineCodeMapsARealBusyAndConstraint(t *testing.T) {
	// engineCode maps the engine's own sentinels, which a dialled handle never
	// sees: its errors are rebuilt from the wire, so the test needs a direct
	// handle to reach a real Turso busy and constraint error.
	path := filepath.Join(t.TempDir(), "engine.db")
	d := directOpen(t, path, Options{BusyTimeout: 30 * time.Millisecond})

	ctx := context.Background()

	// Version 1 is already present, so this violates the primary key.
	_, cerr := d.sqlDB.ExecContext(ctx, `INSERT INTO schema_version (version, applied_at) VALUES (1, '2026-01-01T00:00:00.000Z')`)
	if code, _, ok := engineCode(cerr); !ok || code&0xff != sqliteConstraint {
		t.Errorf("engineCode(%v) = (%d, ok %v), want a constraint code", cerr, code, ok)
	}

	holder, err := d.sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn holder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = holder.ExecContext(ctx, "ROLLBACK")
		_ = holder.Close()
	})
	if _, err := holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE on the holder: %v", err)
	}

	loser, err := d.sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn loser: %v", err)
	}
	t.Cleanup(func() { _ = loser.Close() })
	_, berr := loser.ExecContext(ctx, "BEGIN IMMEDIATE")
	if code, _, ok := engineCode(berr); !ok || code != tursoBusy {
		t.Fatalf("engineCode(%v) = (%d, ok %v), want a busy code", berr, code, ok)
	}
}

// TestOpenRefusesAQuestionMarkInThePath pins the Turso refusal: the DSN path
// ends at the first `?`, so a path carrying one would silently open another
// file.
func TestOpenRefusesAQuestionMarkInThePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.FromSlash("a?b/relevo.db"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	if _, err := Open(path); !errors.Is(err, ErrOpen) {
		t.Fatalf("Open(a path with '?') = %v, want ErrOpen", err)
	}
	// The read-only half needs a converted copy: the marker check runs first,
	// and an unmarked file would stop there instead of reaching the path check
	// this half pins.
	seed := directOpen(t, filepath.Join(dir, "seed.db"), Options{})
	if err := seed.Close(); err != nil {
		t.Fatalf("close the seed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "seed.db"))
	if err != nil {
		t.Fatalf("read the converted seed: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write the converted copy: %v", err)
	}
	if _, err := OpenReadOnly(path); !errors.Is(err, ErrOpen) {
		t.Fatalf("OpenReadOnly(a path with '?') = %v, want ErrOpen", err)
	}
}

// TestOpenReadOnlyRefusesAnUnconvertedFile pins the read-only marker check: a
// file whose header lacks the conversion marker is refused rather than opened
// under Turso without conversion, and the refusal names the fix.
func TestOpenReadOnlyRefusesAnUnconvertedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := directOpen(t, path, Options{})
	if err := d.Close(); err != nil {
		t.Fatalf("close the writable handle: %v", err)
	}

	// Clear the conversion marker -- application_id -- in the header, the state
	// a file an earlier build wrote is in.
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open the file: %v", err)
	}
	if _, err := f.WriteAt(make([]byte, 4), applicationIDOffset); err != nil {
		_ = f.Close()
		t.Fatalf("clear the marker: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close the file: %v", err)
	}

	// The writable handle's pool leaves an empty -wal beside the file; remove it
	// so the refusal can be pinned to creating none of its own.
	if err := os.Remove(path + "-wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove the stale -wal: %v", err)
	}

	_, oerr := OpenReadOnly(path)
	if !errors.Is(oerr, ErrNotConverted) {
		t.Fatalf("OpenReadOnly on an unconverted file = %v, want ErrNotConverted", oerr)
	}
	if !strings.Contains(oerr.Error(), "not converted yet; start the daemon once") {
		t.Errorf("message = %q, want it to name the fix", oerr)
	}
	if _, serr := os.Stat(path + "-wal"); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("a refused read-only open left a -wal: stat error = %v, want not-exist", serr)
	}
}

// TestReadOnlyCloseDoesNotCheckpoint pins the read-only close: a read-only
// handle must not write, so the -wal of the file it read is left exactly as it
// was, rather than truncated by a checkpoint.
func TestReadOnlyCloseDoesNotCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := directOpen(t, path, Options{})
	if _, err := d.sqlDB.Exec(`CREATE TABLE probe (n INTEGER)`); err != nil {
		t.Fatalf("write to grow the -wal: %v", err)
	}
	before, err := walSize(path)
	if err != nil {
		t.Fatalf("walSize: %v", err)
	}
	if before == 0 {
		t.Fatalf("-wal is empty after a write; the test cannot pin the checkpoint")
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if !ro.readOnly {
		t.Error("the read-only handle is not marked read-only")
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("read-only Close: %v", err)
	}

	after, err := walSize(path)
	if err != nil {
		t.Fatalf("walSize: %v", err)
	}
	if after != before {
		t.Errorf("-wal is %d bytes after a read-only Close, want %d", after, before)
	}
}

// TestFreshTursoFileIsInWALMode pins that the per-connection pragma leaves a
// fresh database in WAL, so the file stays SQLite-readable.
func TestFreshTursoFileIsInWALMode(t *testing.T) {
	d := openTestDB(t)

	var mode string
	if err := d.sqlDB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

// TestBackupToRefusesAQuoteInThePath pins that Turso's literal-only VACUUM INTO
// cannot be built for a path holding a quote, so the backup refuses rather than
// building a broken statement.
func TestBackupToRefusesAQuoteInThePath(t *testing.T) {
	d := openTestDB(t)
	path := filepath.Join(t.TempDir(), "it's a backup.db")

	if err := d.BackupTo(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("BackupTo(a path with a quote) = %v, want ErrInvalid", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused backup left %s behind", path)
	}
}
