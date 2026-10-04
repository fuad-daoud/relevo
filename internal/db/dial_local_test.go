//go:build unix

package db_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// The owner route has to carry the machine-local file, or every reader that
// reaches the database through the owner -- the cockpit included -- cannot see
// the rows the split keeps on this machine. These tests pin that on the real
// protocol, with a real split pair behind a real socket: the dial path's own
// handle and the socket it dialled are the only things under test, so a green
// run here is the same shape as the daemon's.

// servedPair is a split pair with an owner serving it: the direct handle, the
// socket, and the server, all cleaned up in the right order.
type servedPair struct {
	shared *db.DB
	sock   string
	srv    *owner.Server
	ln     net.Listener
}

// serveSplit opens a split pair in a fresh directory, serves the shared file on
// a socket short enough for sun_path, and returns everything a test needs. The
// direct handle stays open because it is what the owner serves.
func serveSplit(t *testing.T) servedPair {
	t.Helper()
	dir := t.TempDir()
	shared, err := db.OpenSplit(filepath.Join(dir, "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	l, sock := shortSock(t)
	srv := db.NewOwner(shared)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
		_ = shared.Close()
	})
	return servedPair{shared: shared, sock: sock, srv: srv, ln: l}
}

// TestDialedHandleCarriesTheMachineLocalFile pins the fix the cockpit needed: a
// handle that reached the database through the owner names the machine-local
// file too, and the rows in it are readable and writable through that handle.
//
// Both halves are load-bearing. A handle with no local file reports the sync
// rows as absent on a machine that has them, and a local handle that quietly
// answered from the shared file would report settings no enable ever wrote.
func TestDialedHandleCarriesTheMachineLocalFile(t *testing.T) {
	p := serveSplit(t)

	const localKey, localValue = "sync.remote_url", `"libsql://dialled.turso.io"`
	if err := p.shared.Local().KVPut(localKey, []byte(localValue)); err != nil {
		t.Fatalf("seed the local file: %v", err)
	}
	const sharedKey, sharedValue = "record.of.the.shared.file", `"history"`
	if err := p.shared.KVPut(sharedKey, []byte(sharedValue)); err != nil {
		t.Fatalf("seed the shared file: %v", err)
	}

	dialled, err := db.Dial(p.sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })

	local := dialled.Local()
	if local == nil {
		t.Fatal("a dialled handle has no machine-local file; every owner-routed reader of a sync row would miss it")
	}
	if !isOwnerRoute(local.Route()) {
		t.Errorf("the local handle's route is %q, want the owner route", local.Route())
	}

	got, ok, err := local.KVGet(localKey)
	if err != nil {
		t.Fatalf("read the local row through the dialled handle: %v", err)
	}
	if !ok || string(got) != localValue {
		t.Errorf("local row through the dial = %q (present %t), want %q", got, ok, localValue)
	}

	// The split is still the split over the wire: a row that lives in the shared
	// file is not answerable from the local handle, or the routing the owner
	// applies would be doing nothing at all.
	if _, ok, err := local.KVGet(sharedKey); err != nil {
		t.Fatalf("ask the local handle for a shared row: %v", err)
	} else if ok {
		t.Errorf("the local handle answered with the shared file's %s", sharedKey)
	}

	// And a write through the dialled local handle lands in this machine's local
	// file, which is what a marker a tick writes has to do.
	const written = `{"enabled":true}`
	if err := local.KVPut("sync.enabled", []byte(written)); err != nil {
		t.Fatalf("write through the dialled local handle: %v", err)
	}
	value, ok, err := p.shared.Local().KVGet("sync.enabled")
	if err != nil || !ok || string(value) != written {
		t.Errorf("the write through the dial = %q (present %t, err %v), want it in the local file", value, ok, err)
	}
}

// TestDialedHandleWithoutALocalFileReportsNone pins the other side of the same
// contract. An owner opened without a machine-local file has none to serve, so
// the dial must report that rather than attach something: a caller that asked
// for a local file and got the shared one would read the rows the split exists
// to keep apart.
func TestDialedHandleWithoutALocalFileReportsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	l, sock := shortSock(t)
	srv := db.NewOwner(shared)
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
	if dialled.Local() != nil {
		t.Error("a handle dialled from an owner with no local file reports one")
	}
}

// TestDialingTheLocalFileOfAnOwnerWithoutOneIsRefused pins the refusal rather
// than the absence: a caller that asks the local scope of an owner serving only
// the shared file is told no, and is never handed the shared file under the name
// it asked for. A quiet downgrade here would return the rows the split keeps
// out of the shared file -- a token, a sync marker -- to a caller that believes
// it is reading the local one.
func TestDialingTheLocalFileOfAnOwnerWithoutOneIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	l, sock := shortSock(t)
	srv := db.NewOwner(shared)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})

	local := sql.OpenDB(client.ScopedConnector(sock, false, wire.ScopeLocal))
	t.Cleanup(func() { _ = local.Close() })
	_, err = local.ExecContext(context.Background(), "SELECT count(*) FROM kv")
	var refusal *wire.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("asking for the local file = %v, want a refusal from the owner", err)
	}
	if refusal.Code != wire.RefuseNoLocal {
		t.Errorf("refusal code = %q, want %q", refusal.Code, wire.RefuseNoLocal)
	}
}

// TestDialedLocalFileIsScopedToItsOwnStateRoot pins the isolation the split
// depends on across roots. One owner socket serves one state root, so the local
// file a dialled handle names is that root's companion and no other: a handle
// for root A must never read or write root B's local file, whatever the two
// roots' files are called or where they live.
//
// The two roots are shaped identically -- the same file names, the same keys,
// one row each -- because a cross-root read would be invisible if only the names
// differed.
func TestDialedLocalFileIsScopedToItsOwnStateRoot(t *testing.T) {
	a, b := newRootPair(t, "a"), newRootPair(t, "b")
	if a.sock == b.sock {
		t.Fatal("the two roots share a socket, so the fixture cannot tell them apart")
	}
	assertScopedToItsOwnRoot(t, a, b)
	assertScopedToItsOwnRoot(t, b, a)
}

// rootPair is one state root's split pair served on its own socket, dialled, and
// seeded with the rows that name it. Everything the cross-root assertion needs
// is here, so a second root is a second one of these.
type rootPair struct {
	dialled *db.DB
	sock    string
	letter  string
}

// newRootPair serves one root, seeds the local marker and the shared row that
// name it, and dials the socket: the same three things the daemon and the
// cockpit do, in that order.
func newRootPair(t *testing.T, letter string) rootPair {
	t.Helper()
	served := serveSplit(t)
	marker := `{"root":"` + letter + `"}`
	if err := served.shared.Local().KVPut("sync.enabled", []byte(marker)); err != nil {
		t.Fatalf("seed root %s's local file: %v", letter, err)
	}
	if err := served.shared.KVPut("record."+letter, []byte(`"only root `+letter+`"`)); err != nil {
		t.Fatalf("seed root %s's shared file: %v", letter, err)
	}
	dialled, err := db.Dial(served.sock)
	if err != nil {
		t.Fatalf("dial root %s: %v", letter, err)
	}
	t.Cleanup(func() { _ = dialled.Close() })
	return rootPair{dialled: dialled, sock: served.sock, letter: letter}
}

// assertScopedToItsOwnRoot is one root's half of the comparison against other:
// its local file answers with its own rows, a write through it lands in its own
// file and not the other's, and its shared file carries its own history and none
// of the other root's.
func assertScopedToItsOwnRoot(t *testing.T, self, other rootPair) {
	t.Helper()
	local := self.dialled.Local()
	if local == nil {
		t.Fatalf("root %s: a dialled handle has no machine-local file", self.letter)
	}
	want := `{"root":"` + self.letter + `"}`
	got, ok, err := local.KVGet("sync.enabled")
	if err != nil || !ok {
		t.Fatalf("root %s: read the local marker: %v (present %t)", self.letter, err, ok)
	}
	if string(got) != want {
		t.Errorf("root %s: local marker = %s, want %s", self.letter, got, want)
	}
	// Each root writes its own key, so a leak is a row that appeared in the
	// other file rather than one this root's own earlier write put there.
	scoped := "sync.scoped." + self.letter
	if err := local.KVPut(scoped, []byte(want)); err != nil {
		t.Fatalf("root %s: write the scoped marker: %v", self.letter, err)
	}
	if _, ok, err := other.dialled.Local().KVGet(scoped); err != nil {
		t.Errorf("root %s: ask the other root: %v", self.letter, err)
	} else if ok {
		t.Errorf("root %s: a write through its handle reached the other root's local file", self.letter)
	}
	if _, ok, err := self.dialled.KVGet("record." + other.letter); err != nil {
		t.Errorf("root %s: ask the other root's shared file: %v", self.letter, err)
	} else if ok {
		t.Errorf("root %s: the shared handle answered with root %s's row", self.letter, other.letter)
	}
	if _, ok, err := self.dialled.KVGet("record." + self.letter); err != nil || !ok {
		t.Errorf("root %s: the shared handle does not carry its own record: %v (present %t)", self.letter, err, ok)
	}
}

// isOwnerRoute reports whether a route names the owner socket rather than a
// direct file.
func isOwnerRoute(route string) bool {
	return strings.HasPrefix(route, "owner ")
}
