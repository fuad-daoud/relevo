package installation

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadMintsAndReusesOneID pins the file's contract: the first Load mints
// it with the hostname label and owner-only mode, and a second Load returns
// the same id rather than minting a second identity.
func TestLoadMintsAndReusesOneID(t *testing.T) {
	root := t.TempDir()

	first, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if first.ID == "" {
		t.Fatal("minted id is empty")
	}
	if first.CreatedAt.IsZero() {
		t.Error("minted created_at is zero")
	}
	if host, herr := os.Hostname(); herr == nil && first.Label != host {
		t.Errorf("label = %q, want the hostname %q", first.Label, host)
	}

	second, err := Load(root)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second Load id = %q, want the first Load's %q", second.ID, first.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("second Load created_at = %s, want %s", second.CreatedAt, first.CreatedAt)
	}

	fi, err := os.Stat(filepath.Join(root, FileName))
	if err != nil {
		t.Fatalf("stat %s: %v", FileName, err)
	}
	if got := fi.Mode().Perm(); got != fileMode {
		t.Errorf("%s mode = %v, want %v", FileName, got, fileMode)
	}
}

// TestLoadTwoRootsGetDifferentIDs pins what makes the id usable as an origin:
// each state root mints its own, so two installations never share one.
func TestLoadTwoRootsGetDifferentIDs(t *testing.T) {
	a, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load a: %v", err)
	}
	b, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load b: %v", err)
	}
	if a.ID == b.ID {
		t.Errorf("two roots share the id %q", a.ID)
	}
}
