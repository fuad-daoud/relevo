package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
)

// routedStore roots a store at a fresh directory and installs open as the
// machine route for the test, restoring the direct open when it ends.
func routedStore(t *testing.T, open MachineOpener) *Store {
	t.Helper()
	t.Cleanup(func() { SetMachineOpener(nil) })
	SetMachineOpener(open)
	return New(t.TempDir())
}

// TestMachineOpenerRoutsTheMachineDatabase pins that an installed opener's
// handle is the store's handle: pointer identity, no second open of
// <root>/relevo.db.
func TestMachineOpenerRoutsTheMachineDatabase(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	st := routedStore(t, func(string) (*db.DB, bool, error) { return d, true, nil })

	got, err := st.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if got != d {
		t.Errorf("store handle = %p, want the opener's %p", got, d)
	}
	if _, err := os.Stat(st.DBPath()); !os.IsNotExist(err) {
		t.Errorf("the routed open created %s: stat error = %v, want not-exist", st.DBPath(), err)
	}
}

// TestRoutedOpenDoesNotMintTheInstallationFile pins the client's side of the
// origin rule: the handle comes from the opener, so the store never writes an
// installation file beside the database.
func TestRoutedOpenDoesNotMintTheInstallationFile(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	st := routedStore(t, func(string) (*db.DB, bool, error) { return d, true, nil })

	if _, err := st.DB(); err != nil {
		t.Fatalf("DB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.root, "installation.json")); !os.IsNotExist(err) {
		t.Errorf("a routed open minted installation.json: stat error = %v, want not-exist", err)
	}
}

// TestRoutedOpenStillRefusesANewerSchema pins that the hook branch keeps the
// refusal a direct open applies: a newer schema is closed and reported as
// ErrNewerSchema, never handed to the caller.
func TestRoutedOpenStillRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ahead.db")
	raw := dbtest.RawOpen(t, path)
	if _, err := raw.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (99, '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	_ = raw.Close()

	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if !d.Newer() {
		t.Fatal("a database at schema 99 is not reported newer")
	}

	st := routedStore(t, func(string) (*db.DB, bool, error) { return d, true, nil })

	if _, err := st.DB(); !errors.Is(err, db.ErrNewerSchema) {
		t.Errorf("DB = %v, want ErrNewerSchema", err)
	}
}
