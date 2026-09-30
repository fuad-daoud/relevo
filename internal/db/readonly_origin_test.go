package db

import (
	"path/filepath"
	"testing"
)

// TestOpenReadOnlyWithHonoursOrigin pins the read-only opener's origin: rows
// relevo writes are scoped to the installation's id (originScope is
// `origin IN (?, ”)`), so a read-only handle that serves them must carry that
// id. A handle opened with the writer's origin sees the row; one opened with
// any other origin sees nothing.
func TestOpenReadOnlyWithHonoursOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	const origin = "01AAAAAAAAAAAAAAAAAAAAAAAA"

	w, err := OpenWith(path, Options{Origin: origin})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	if _, err := w.RecordPut(testRecord("api")); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	own, err := OpenReadOnlyWith(path, Options{Origin: origin})
	if err != nil {
		t.Fatalf("OpenReadOnlyWith own origin: %v", err)
	}
	t.Cleanup(func() { _ = own.Close() })
	if _, ok, err := own.RecordGet("", "api"); err != nil || !ok {
		t.Errorf("own-origin RecordGet = (_, %v, %v), want the saved row", ok, err)
	}

	other, err := OpenReadOnlyWith(path, Options{Origin: "01BBBBBBBBBBBBBBBBBBBBBBBB"})
	if err != nil {
		t.Fatalf("OpenReadOnlyWith other origin: %v", err)
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
