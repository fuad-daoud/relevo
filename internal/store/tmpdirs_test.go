package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEnsureRemoveTmpEntryRoundTrip pins that Ensure makes <tmpRoot>/<rel>,
// reports its absolute path, and that Remove deletes the entry alone -- the
// parent it sits in survives for the next entry.
func TestEnsureRemoveTmpEntryRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	tmpRoot := t.TempDir()

	dir, err := s.EnsureTmpEntry(tmpRoot, "rounds/x-001")
	if err != nil {
		t.Fatalf("EnsureTmpEntry: %v", err)
	}
	if want := filepath.Join(tmpRoot, "rounds", "x-001"); dir != want {
		t.Errorf("EnsureTmpEntry = %q, want %q", dir, want)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("entry not created: %v", err)
	}

	if err := s.RemoveTmpEntry(tmpRoot, "rounds/x-001"); err != nil {
		t.Fatalf("RemoveTmpEntry: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("entry survived removal: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(tmpRoot, "rounds")); err != nil || !fi.IsDir() {
		t.Errorf("rounds/ went with the entry: err %v", err)
	}
}

// TestRemoveMissingTmpEntryIsSuccess pins the sweep's idempotence rule: an
// entry that is not there is not an error.
func TestRemoveMissingTmpEntryIsSuccess(t *testing.T) {
	s := New(t.TempDir())
	if err := s.RemoveTmpEntry(t.TempDir(), "rounds/gone-001"); err != nil {
		t.Errorf("RemoveTmpEntry on a missing entry = %v, want nil", err)
	}
}

// TestEnsureTmpEntryRefusesSymlinkedDir pins the plant defence: rounds/
// swapped for a symlink out of the tmp root is refused, the target gains
// nothing, and the link itself stays exactly where it was planted.
func TestEnsureTmpEntryRefusesSymlinkedDir(t *testing.T) {
	s := New(t.TempDir())
	tmpRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(tmpRoot, "rounds")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if _, err := s.EnsureTmpEntry(tmpRoot, "rounds/x-001"); err == nil {
		t.Fatal("EnsureTmpEntry through a symlinked rounds/ succeeded, want refusal")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Errorf("symlink target holds %v entries, want untouched (err %v)", len(entries), err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("rounds/ is no longer the planted symlink (fi %v, err %v)", fi, err)
	}
}

// TestEnsureTmpEntryChownsEachCreatedDir pins that the tenant chown callback
// sees every directory Ensure creates -- the intermediate rounds/ and the
// leaf -- and nothing else: never the pre-existing parent on a second entry,
// and never the tmp root itself.
func TestEnsureTmpEntryChownsEachCreatedDir(t *testing.T) {
	s := New(t.TempDir())
	tmpRoot := t.TempDir()
	var got []string
	s.SetTenantChown(func(path string) error {
		got = append(got, path)
		return nil
	})

	if _, err := s.EnsureTmpEntry(tmpRoot, "rounds/x-001"); err != nil {
		t.Fatalf("EnsureTmpEntry: %v", err)
	}
	want := []string{filepath.Join(tmpRoot, "rounds"), filepath.Join(tmpRoot, "rounds", "x-001")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("chown callback got %v, want %v", got, want)
	}

	if _, err := s.EnsureTmpEntry(tmpRoot, "rounds/x-002"); err != nil {
		t.Fatalf("second EnsureTmpEntry: %v", err)
	}
	if len(got) != 3 || got[2] != filepath.Join(tmpRoot, "rounds", "x-002") {
		t.Errorf("after the second entry the callback got %v, want only the new leaf", got)
	}
}

// TestTmpEntryHelpersRefuseEscapingRels pins that a rel containing .. is
// refused by all three helpers, before anything is created.
func TestTmpEntryHelpersRefuseEscapingRels(t *testing.T) {
	s := New(t.TempDir())
	tmpRoot := t.TempDir()

	for _, rel := range []string{"../escape", "rounds/../../escape", ".."} {
		if _, err := s.EnsureTmpEntry(tmpRoot, rel); err == nil {
			t.Errorf("EnsureTmpEntry(%q) = nil error, want refusal", rel)
		}
		if err := s.RemoveTmpEntry(tmpRoot, rel); err == nil {
			t.Errorf("RemoveTmpEntry(%q) = nil error, want refusal", rel)
		}
		if _, err := s.ListTmpEntries(tmpRoot, rel); err == nil {
			t.Errorf("ListTmpEntries(%q) = nil error, want refusal", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(tmpRoot), "escape")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("escape survived the refusal: err %v", err)
	}
}

// TestListTmpEntriesReturnsChildrenWithMtimes pins the sweep's worklist: the
// direct children of the named sub, each with a fresh mtime, siblings under
// other subs absent, and a missing sub reading empty.
func TestListTmpEntriesReturnsChildrenWithMtimes(t *testing.T) {
	s := New(t.TempDir())
	tmpRoot := t.TempDir()
	before := time.Now().Add(-time.Second)
	for _, rel := range []string{"rounds/x-001", "rounds/x-002", "checks/y-001"} {
		if _, err := s.EnsureTmpEntry(tmpRoot, rel); err != nil {
			t.Fatalf("EnsureTmpEntry(%q): %v", rel, err)
		}
	}

	entries, err := s.ListTmpEntries(tmpRoot, "rounds")
	if err != nil {
		t.Fatalf("ListTmpEntries: %v", err)
	}
	mtimes := map[string]time.Time{}
	for _, e := range entries {
		mtimes[e.Name] = e.ModTime
	}
	if len(mtimes) != 2 {
		t.Fatalf("direct children = %+v, want only x-001 and x-002", entries)
	}
	for _, name := range []string{"x-001", "x-002"} {
		mt, ok := mtimes[name]
		if !ok {
			t.Errorf("%s missing from the listing", name)
			continue
		}
		if mt.Before(before) {
			t.Errorf("%s mtime %v predates creation", name, mt)
		}
	}

	if got, err := s.ListTmpEntries(tmpRoot, "missing"); err != nil || len(got) != 0 {
		t.Errorf("ListTmpEntries on a missing sub = (%v, %v), want empty", got, err)
	}
}
