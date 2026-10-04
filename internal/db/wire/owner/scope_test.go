//go:build unix

package owner

import (
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// The owner's half of the split: two files served over one socket, and which of
// them a connection reaches. The db-level tests pin what a dialled handle then
// sees; these pin the routing itself, so a failure names the owner rather than
// the dial.

// splitServer is two raw pools and a server over both, each pool holding a row
// that says which file it is.
type splitServer struct {
	srv    *Server
	sock   string
	shared *sql.DB
	local  *sql.DB
}

// startSplitServer serves a shared file and a machine-local file beside it, each
// seeded with a row naming itself, so a statement's answer says which pool ran
// it.
func startSplitServer(t *testing.T, withLocal bool) splitServer {
	t.Helper()
	dir := t.TempDir()
	open := func(name, mark string) *sql.DB {
		sqlDB, err := sql.Open("sqlite", "file:"+filepath.Join(dir, name)+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatalf("sql.Open %s: %v", name, err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		if _, err := sqlDB.Exec(`CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT)`); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := sqlDB.Exec(`INSERT INTO kv (k, v) VALUES ('file', ?)`, mark); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return sqlDB
	}
	s := splitServer{shared: open("shared.db", "shared")}
	srv := New(s.shared, 3, 9, "01ORIGIN", nil)
	if withLocal {
		s.local = open("local.db", "local")
		srv.ServeLocal(s.local)
	}
	l, sock := shortListener(t)
	s.srv, s.sock = srv, sock
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})
	return s
}

// readFileOverTheWire runs a statement on a connection scoped to scope and
// reports which file answered it.
func readFileOverTheWire(t *testing.T, sock, scope string) (string, error) {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = nc.Close() }()
	w := wire.NewConn(nc)
	if err := w.Write(handshakeFrame(scope)); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	frame, err := w.Read()
	if err != nil {
		return "", err
	}
	kind, err := wire.Kind(frame)
	if err != nil {
		return "", err
	}
	if kind == wire.KindRefuse {
		var refusal wire.Refusal
		if _, err := wire.Decode(frame, &refusal); err != nil {
			return "", err
		}
		return "", &refusal
	}
	if err := w.Write(queryFrame(`SELECT v FROM kv WHERE k = 'file'`)); err != nil {
		return "", err
	}
	rows, err := w.Read()
	if err != nil {
		return "", err
	}
	raw, err := wire.Decode(rows, &wire.Rows{})
	if err != nil {
		return "", err
	}
	value, _, err := wire.NewCursor(raw).Next()
	if err != nil {
		return "", err
	}
	text, _ := value.(string)
	return text, nil
}

// handshakeFrame is a hello naming scope, and queryFrame a statement over it.
func handshakeFrame(scope string) []byte {
	payload, err := wire.Encode(wire.KindHello, &wire.Hello{
		Header: wire.Header{Type: wire.TypeHello},
		Proto:  wire.Proto, Version: wire.Version, Scope: scope,
	}, nil)
	if err != nil {
		panic(err)
	}
	return payload
}

func queryFrame(query string) []byte {
	payload, err := wire.Encode(wire.KindQuery, &wire.Query{
		Header: wire.Header{Type: wire.TypeQuery, ID: 1}, Query: query,
	}, nil)
	if err != nil {
		panic(err)
	}
	return payload
}

// TestTheLocalScopeIsServedFromTheLocalFile pins the routing: a connection that
// asks for the local file is answered from the local pool, and one that does
// not is answered from the shared pool. A server that answered both from the
// shared file would make the split decorative.
func TestTheLocalScopeIsServedFromTheLocalFile(t *testing.T) {
	for _, tc := range []struct{ scope, want string }{
		{wire.ScopeLocal, "local"},
		{wire.ScopeShared, "shared"},
		{"", "shared"},
	} {
		t.Run("scope "+tc.scope, func(t *testing.T) {
			s := startSplitServer(t, true)
			got, err := readFileOverTheWire(t, s.sock, tc.scope)
			if err != nil {
				t.Fatalf("read over scope %q: %v", tc.scope, err)
			}
			if got != tc.want {
				t.Errorf("scope %q was answered from the %s file, want %s", tc.scope, got, tc.want)
			}
		})
	}
}

// TestTheLocalScopeIsRefusedWhenThereIsNoLocalFile pins the refusal rather than
// a downgrade. An owner serving only the shared file says no to the local scope,
// because answering from the shared file would hand the caller the rows the
// split keeps out of it under the name it asked for.
func TestTheLocalScopeIsRefusedWhenThereIsNoLocalFile(t *testing.T) {
	s := startSplitServer(t, false)
	_, err := readFileOverTheWire(t, s.sock, wire.ScopeLocal)
	var refusal *wire.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the local scope against an owner with no local file = %v, want a refusal", err)
	}
	if refusal.Code != wire.RefuseNoLocal {
		t.Errorf("refusal code = %q, want %q", refusal.Code, wire.RefuseNoLocal)
	}
}

// TestTheWelcomeSaysWhetherALocalFileIsServed pins the bit a dial reads to
// decide whether it may attach a local handle. It has to be true exactly when
// the local scope would be answered, or a dial either skips a file it could
// reach or attaches one the owner would refuse.
func TestTheWelcomeSaysWhetherALocalFileIsServed(t *testing.T) {
	for _, withLocal := range []bool{true, false} {
		s := startSplitServer(t, withLocal)
		got := welcomeHasLocal(t, s.sock)
		if got != withLocal {
			t.Errorf("with a local file %t, the welcome said %t", withLocal, got)
		}
	}
}

// welcomeHasLocal reads one handshake's welcome and reports its has-local bit.
func welcomeHasLocal(t *testing.T, sock string) bool {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = nc.Close() }()
	w := wire.NewConn(nc)
	if err := w.Write(handshakeFrame(wire.ScopeShared)); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	frame, err := w.Read()
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	var welcome wire.Welcome
	if _, err := wire.Decode(frame, &welcome); err != nil {
		t.Fatalf("decode welcome: %v", err)
	}
	return welcome.HasLocal
}
