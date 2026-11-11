package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// holdsHandle reports whether this process still holds a direct handle on
// path, by the canonical spelling db.OpenHandles lists.
func holdsHandle(t *testing.T, path string) bool {
	t.Helper()
	canon, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return slices.Contains(db.OpenHandles(), canon)
}

// TestCloseReleasesTheDatabaseAWriteOpened pins that Close gives back the
// handle the store's first write opened, and that a second Close is a no-op.
func TestCloseReleasesTheDatabaseAWriteOpened(t *testing.T) {
	st := New(t.TempDir())
	if err := st.Save(Binding{Name: "webshop", CWD: "/repo"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !holdsHandle(t, st.DBPath()) {
		t.Fatalf("no handle on %s after a write; the test cannot see a close", st.DBPath())
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if holdsHandle(t, st.DBPath()) {
		t.Errorf("a handle on %s is still open after Close", st.DBPath())
	}
	if err := st.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

// TestCloseBeforeAnyWriteKeepsTheStoreFromOpening pins that a store closed
// before it opened its database refuses a later write rather than opening a
// handle nobody would close.
func TestCloseBeforeAnyWriteKeepsTheStoreFromOpening(t *testing.T) {
	st := New(t.TempDir())
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st.Save(Binding{Name: "webshop", CWD: "/repo"}); !errors.Is(err, ErrClosed) {
		t.Errorf("Save after Close = %v, want ErrClosed", err)
	}
	if _, err := os.Stat(st.DBPath()); !os.IsNotExist(err) {
		t.Errorf("the refused write created %s: stat error = %v, want not-exist", st.DBPath(), err)
	}
}

// TestCloseLeavesASharedHandleOpen pins that a shared store's handle stays
// the caller's: Close on the store does not close it.
func TestCloseLeavesASharedHandleOpen(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "machine.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	st := NewShared(t.TempDir(), "client", d)
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := d.Version(); err != nil {
		t.Errorf("the caller's handle after the store's Close: Version = %v, want it still open", err)
	}
}

// newStore is a store over a fresh temp directory, closed when t ends.
func newStore(t testing.TB) *Store {
	t.Helper()
	return storeAt(t, t.TempDir())
}

// storeAt is a store over root, closed when t ends: before t.TempDir removes
// root, since the close is registered after it.
func storeAt(t testing.TB, root string) *Store {
	t.Helper()
	st := New(root)
	t.Cleanup(func() { _ = st.Close() })
	return st
}
