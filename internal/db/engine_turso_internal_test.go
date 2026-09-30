//go:build !modernc

package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEngineCodeMapsARealBusyAndConstraint pins the Turso error mapping against
// real driver errors: a constraint violation becomes 19 and a busy 5, the codes
// modernc's error type carries and mapBusy and mapMasterMindKey mask.
func TestEngineCodeMapsARealBusyAndConstraint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.db")
	d, err := OpenWith(path, Options{BusyTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

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
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create the file: %v", err)
	}
	if _, err := OpenReadOnly(path); !errors.Is(err, ErrOpen) {
		t.Fatalf("OpenReadOnly(a path with '?') = %v, want ErrOpen", err)
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
