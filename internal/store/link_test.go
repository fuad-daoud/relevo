package store

import (
	"errors"
	"testing"
)

// TestSaveKeepsRemoteLink pins the link a remote binding carries: the save
// promotes it into binding_record's link columns and keeps it in the binding
// JSON, so a load returns it, and BindingFormat stays the one an older relevo
// still reads -- the link is a field recordFormat does not stamp.
func TestSaveKeepsRemoteLink(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("api", "/repo")
	b.Builder = Endpoint{Mode: ModeRemote, Server: "zen"}
	b.Link = &RemoteLink{Installation: "01SERVER", ID: "01SRVRECORD"}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Link == nil || got.Link.Installation != "01SERVER" || got.Link.ID != "01SRVRECORD" {
		t.Fatalf("reloaded Link = %+v, want the saved link", got.Link)
	}

	d, err := s.dbForWrite()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: ok=%v err=%v", ok, err)
	}
	if rec.LinkOrigin != "01SERVER" || rec.LinkID != "01SRVRECORD" {
		t.Fatalf("row link = %q/%q, want the promoted columns", rec.LinkOrigin, rec.LinkID)
	}

	if BindingFormat != 10 {
		t.Fatalf("BindingFormat = %d, want 10: the link is a field recordFormat does not stamp", BindingFormat)
	}
}

// TestSaveWithoutLinkStoresNone pins the row shape for every other binding: a
// binding with no link stores no link columns.
func TestSaveWithoutLinkStoresNone(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("api", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.dbForWrite()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: ok=%v err=%v", ok, err)
	}
	if rec.LinkOrigin != "" || rec.LinkID != "" {
		t.Fatalf("row link = %q/%q, want none", rec.LinkOrigin, rec.LinkID)
	}
	if got, err := s.Load("api"); err != nil || got.Link != nil {
		t.Fatalf("reloaded binding = %+v (err %v), want no link", got.Link, err)
	}
}

// TestSaveUsesPreMintedRecordID pins the seam addRemote uses: a caller that
// must know a binding's record id before the save sets Binding.RecordID, the
// row takes that id, and Store.RecordID reads it back. RecordID is not part of
// the binding JSON: a load leaves it empty.
func TestSaveUsesPreMintedRecordID(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("api", "/repo")
	b.RecordID = "01MINTEDRECORD"
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.RecordID("api")
	if err != nil {
		t.Fatalf("RecordID: %v", err)
	}
	if got != "01MINTEDRECORD" {
		t.Fatalf("RecordID = %q, want the pre-minted id", got)
	}
	loaded, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.RecordID != "" {
		t.Errorf("loaded RecordID = %q, want empty: it is not part of bind.json", loaded.RecordID)
	}
}

// TestRecordIDMissingIsNotFound pins the error a caller gets for a binding that
// does not exist.
func TestRecordIDMissingIsNotFound(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.RecordID("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RecordID on a missing binding = %v, want ErrNotFound", err)
	}
}
