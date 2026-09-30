//go:build modernc

package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenSetsJournalSizeLimit pins that sqlite truncates the -wal file after a
// checkpoint instead of leaving it at a write burst's high-water size. Only the
// modernc build sets the pragma; Turso does not support it.
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

// TestOpenPathWithQuestionUsesThatFile pins the modernc half of the escaped-DSN
// contract: a `?` in the path stays part of the filename instead of truncating
// the DSN to the prefix before it.
func TestOpenPathWithQuestionUsesThatFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.FromSlash("a?b/relevo.db"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	truncated := filepath.Join(dir, "a")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := os.Stat(truncated); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("truncated prefix %s exists after Open, want absent (stat err = %v)", truncated, err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	have, know := ro.SchemaVersions()
	if have != know || have <= 0 {
		t.Errorf("OpenReadOnly SchemaVersions() = (%d, %d), want have == know > 0", have, know)
	}
}
