package dbtest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestInstallSetsAndClears pins the contract a TestMain depends on: a fresh
// Open is a copy of the template, and cleanup removes the template directory.
func TestInstallSetsAndClears(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	cleanup, err := Install()
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Cleanup(cleanup)

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Install left %d entries in the temp dir, want 1", len(entries))
	}
	dir := filepath.Join(tmp, entries[0].Name())
	tplBytes, err := os.ReadFile(filepath.Join(dir, "template.db"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	fresh := filepath.Join(tmp, "fresh.db")
	d, err := db.Open(fresh)
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close fresh: %v", err)
	}
	freshBytes, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatalf("read fresh: %v", err)
	}
	if !bytes.Equal(freshBytes, tplBytes) {
		t.Errorf("a fresh Open is not a byte copy of the template")
	}

	cleanup()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("template directory still present after cleanup: %v", err)
	}
}
