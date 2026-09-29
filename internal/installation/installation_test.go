package installation

import (
	"os"
	"path/filepath"
	"sync"
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

// TestConcurrentLoadsNeverSeeAPartialFile pins the mint's whole-file
// visibility: many Loads raced on a fresh root see either no file or the whole
// file, never the zero-length or partial one a create-then-write leaves
// readable, and they all agree on one id. Without the atomic mint a loader
// that creates the final path before writing lets a peer decode a partial file
// and fail.
func TestConcurrentLoadsNeverSeeAPartialFile(t *testing.T) {
	const (
		loaders = 32
		rounds  = 50
	)

	for round := 0; round < rounds; round++ {
		root := t.TempDir()

		start := make(chan struct{})
		ids := make([]string, loaders)
		errs := make([]error, loaders)
		var wg sync.WaitGroup
		for i := 0; i < loaders; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				inst, err := Load(root)
				ids[i] = inst.ID
				errs[i] = err
			}(i)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: loader %d: Load: %v", round, i, err)
			}
		}
		for i, id := range ids {
			if id != ids[0] {
				t.Fatalf("round %d: loader %d id = %q, want %q (one id per root)", round, i, id, ids[0])
			}
		}

		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("round %d: read root: %v", round, err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if len(names) != 1 || names[0] != FileName {
			t.Fatalf("round %d: root holds %v, want exactly [%s] (one file, no .tmp-* residue)", round, names, FileName)
		}
	}
}
