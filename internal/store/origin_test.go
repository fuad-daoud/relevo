package store

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/installation"
)

// TestSavedRecordCarriesTheInstallationOrigin pins step 4's seam: the store
// opens the database with the origin from the installation file beside it, so
// a saved record is scoped to this installation and to nothing else.
func TestSavedRecordCarriesTheInstallationOrigin(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	inst, err := installation.Load(root)
	if err != nil {
		t.Fatalf("installation.Load: %v", err)
	}
	if err := s.Save(newBinding("api", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The store's own handle reads the record back.
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if _, ok, err := d.RecordGet("", "api"); err != nil || !ok {
		t.Fatalf("store RecordGet = (_, %v, %v), want the saved row", ok, err)
	}

	// A handle opened with this installation's id sees it too, and a handle
	// opened with any other id sees nothing: the stored origin is the id the
	// installation file holds, not an empty default.
	own, err := db.OpenWith(s.DBPath(), db.Options{Origin: inst.ID})
	if err != nil {
		t.Fatalf("OpenWith own origin: %v", err)
	}
	t.Cleanup(func() { _ = own.Close() })
	if _, ok, err := own.RecordGet("", "api"); err != nil || !ok {
		t.Errorf("own-origin RecordGet = (_, %v, %v), want the saved row", ok, err)
	}

	other, err := db.OpenWith(s.DBPath(), db.Options{Origin: "01OTHERINSTALLATION0000000"})
	if err != nil {
		t.Fatalf("OpenWith other origin: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, ok, err := other.RecordGet("", "api"); err != nil || ok {
		t.Errorf("other-origin RecordGet = (_, %v, %v), want no row", ok, err)
	}
	if list, err := other.RecordList(""); err != nil {
		t.Fatalf("other-origin RecordList: %v", err)
	} else if len(list) != 0 {
		t.Errorf("other-origin RecordList = %+v, want no rows", list)
	}
}
