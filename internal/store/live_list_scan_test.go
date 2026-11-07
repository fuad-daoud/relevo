package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestListMergesBatchedOverlaysByRecordID pins what one batched listing
// preserves: overlays land on the bindings whose record ids minted them, rows
// without overlays read from their synced values (a legacy row included), a
// malformed blob ruins only its own row, and a valid overlay-shaped document
// past the namespace bound reaches no row.
func TestListMergesBatchedOverlaysByRecordID(t *testing.T) {
	s := New(t.TempDir())
	save := func(b Binding) {
		t.Helper()
		if err := s.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
	}

	save(newBinding("plain", "/repo/plain"))

	first := newBinding("first", "/repo/first")
	first.Builder.StreamOffset = 111
	first.Builder.RemoteLive = &LiveFacts{PID: 4711}
	save(first)

	second := newBinding("second", "/repo/second")
	second.Builder.StreamOffset = 333
	save(second)

	legacy := liveBinding()
	save(legacy)
	seedPreOverlayRow(t, s, legacy)

	save(newBinding("broken", "/repo/broken"))

	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, "broken")
	if err != nil || !ok {
		t.Fatalf("RecordGet(broken): ok=%v err=%v", ok, err)
	}
	// Valid JSON but no overlay shape, so the read warns and treats the row
	// as overlay-less without touching the others.
	if err := d.LocalOrSelf().KVPut(liveKey(rec.ID), []byte(`[1,2]`)); err != nil {
		t.Fatalf("seed malformed overlay: %v", err)
	}
	if err := d.LocalOrSelf().KVPut("live0probe", []byte(`{"stream_offset":4242}`)); err != nil {
		t.Fatalf("seed out-of-range key: %v", err)
	}

	listed, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 5 {
		t.Fatalf("List returned %d bindings, want 5", len(listed))
	}
	byName := make(map[string]Binding, len(listed))
	for _, b := range listed {
		byName[b.Name] = b
	}

	if got := byName["plain"]; got.Builder.StreamOffset != 0 || got.Builder.RemoteLive != nil {
		t.Errorf("plain reads %+v, want the synced zeros", got.Builder)
	}
	if got := byName["first"]; got.Builder.StreamOffset != 111 ||
		got.Builder.RemoteLive == nil || got.Builder.RemoteLive.PID != 4711 {
		t.Errorf("first reads offset %d live %+v, want 111 and PID 4711", got.Builder.StreamOffset, got.Builder.RemoteLive)
	}
	if got := byName["second"]; got.Builder.StreamOffset != 333 || got.Builder.RemoteLive != nil {
		t.Errorf("second reads offset %d live %+v, want 333 and no live view", got.Builder.StreamOffset, got.Builder.RemoteLive)
	}
	if got := byName["api"]; got.Builder.StreamOffset != 9000 || got.Builder.RemoteLive == nil {
		t.Errorf("legacy row reads offset %d live %+v, want its synced 9000 and live view", got.Builder.StreamOffset, got.Builder.RemoteLive)
	}
	if got := byName["broken"]; got.Builder.StreamOffset != 0 || got.Builder.RemoteLive != nil {
		t.Errorf("malformed row reads offset %d live %+v, want the synced zeros", got.Builder.StreamOffset, got.Builder.RemoteLive)
	}
	for _, b := range listed {
		if b.Builder.StreamOffset == 4242 {
			t.Errorf("out-of-range key reached row %q", b.Name)
		}
	}
}

// TestMergeLiveListWithoutDatabaseLeavesBindingsAlone pins the no-database
// read: with no database file there are no overlays, so the rows pass through.
func TestMergeLiveListWithoutDatabaseLeavesBindingsAlone(t *testing.T) {
	s := New(t.TempDir())
	bindings := []Binding{newBinding("ghost", "/repo/ghost")}
	recs := []db.Record{{ID: "01GHOST", Name: "ghost"}}
	got, err := s.mergeLiveList(bindings, recs)
	if err != nil {
		t.Fatalf("mergeLiveList: %v", err)
	}
	if len(got) != 1 || got[0].Name != "ghost" || got[0].Builder.StreamOffset != 0 {
		t.Errorf("bindings without a database read as %+v, want them unchanged", got)
	}
}

// TestMergeLiveListSkipsEmptyRecordIDs pins the empty-id guard: no record id
// means no overlay key, so even a stored `live/` key itself merges nowhere.
func TestMergeLiveListSkipsEmptyRecordIDs(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("seed", "/repo/seed")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := d.LocalOrSelf().KVPut("live/", []byte(`{"stream_offset":777}`)); err != nil {
		t.Fatalf("seed bare live key: %v", err)
	}
	got, err := s.mergeLiveList(
		[]Binding{newBinding("ghost", "/repo/ghost")},
		[]db.Record{{Name: "ghost"}},
	)
	if err != nil {
		t.Fatalf("mergeLiveList: %v", err)
	}
	if len(got) != 1 || got[0].Builder.StreamOffset != 0 {
		t.Errorf("empty-id row reads offset %d, want 0", got[0].Builder.StreamOffset)
	}
}

// TestListFailsWhenTheOverlayScanFails pins that a dead overlay file fails the
// whole listing rather than returning half-merged rows.
func TestListFailsWhenTheOverlayScanFails(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("api", "/repo/a")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	// Closing only the machine-local file leaves the shared rows readable but
	// the overlay scan dead, which is exactly the fault the batch read must
	// report instead of hiding.
	if err := d.Local().Close(); err != nil {
		t.Fatalf("close the local file: %v", err)
	}
	_, err = s.List()
	if err == nil || !strings.Contains(err.Error(), "read live overlays") {
		t.Errorf("List over a dead overlay file = %v, want an error naming the overlay read", err)
	}
}

// TestMergeLiveListSurfacesADatabaseError pins that a database failure reaches
// the caller instead of reading as no overlays.
func TestMergeLiveListSurfacesADatabaseError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	s := New(blocker)
	_, err := s.mergeLiveList(
		[]Binding{newBinding("ghost", "/repo/ghost")},
		[]db.Record{{ID: "01GHOST", Name: "ghost"}},
	)
	if err == nil {
		t.Error("mergeLiveList over an unopenable database = nil, want an error")
	}
}
