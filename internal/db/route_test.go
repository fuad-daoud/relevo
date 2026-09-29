//go:build unix

package db_test

import (
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestRouteNamesTheOpen pins the route a handle reports: a direct open reaches
// the file, a dial reaches the owner's socket by name.
func TestRouteNamesTheOpen(t *testing.T) {
	direct, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	if got := direct.Route(); got != "file" {
		t.Errorf("Open route = %q, want %q", got, "file")
	}

	served, err := db.Open(filepath.Join(t.TempDir(), "served.db"))
	if err != nil {
		t.Fatalf("Open served: %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })

	l, sock := shortSock(t)
	srv := db.NewOwner(served)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})

	dialled, err := db.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })
	if got, want := dialled.Route(), "owner "+sock; got != want {
		t.Errorf("Dial route = %q, want %q", got, want)
	}
}
