//go:build unix

package owner

import (
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

func shortListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "o.sock")
	if len(sock) >= 104 {
		t.Fatalf("socket path %q is %d bytes, over the 104-byte sun_path", sock, len(sock))
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, sock
}

func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "o.db")
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	l, sock := shortListener(t)
	srv := New(sqlDB, 3, 9, "01ORIGIN")
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})
	return srv, sock
}

func dialRaw(t *testing.T, sock string) (*wire.Conn, net.Conn) {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	return wire.NewConn(nc), nc
}

func sendHello(t *testing.T, w *wire.Conn, version int) {
	t.Helper()
	if err := tryHello(w, version); err != nil {
		t.Fatalf("Write hello: %v", err)
	}
}

func tryHello(w *wire.Conn, version int) error {
	payload, err := wire.Encode(wire.KindHello, &wire.Hello{
		Header:  wire.Header{Type: wire.TypeHello},
		Proto:   wire.Proto,
		Version: version,
	}, nil)
	if err != nil {
		return err
	}
	return w.Write(payload)
}

func welcome(t *testing.T, w *wire.Conn) *wire.Welcome {
	t.Helper()
	frame, err := w.Read()
	if err != nil {
		t.Fatalf("Read welcome: %v", err)
	}
	kind, err := wire.Kind(frame)
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	if kind != wire.KindWelcome {
		t.Fatalf("frame kind = %d, want welcome", kind)
	}
	var m wire.Welcome
	if _, err := wire.Decode(frame, &m); err != nil {
		t.Fatalf("Decode welcome: %v", err)
	}
	return &m
}

func refusal(t *testing.T, w *wire.Conn) *wire.Refusal {
	t.Helper()
	frame, err := w.Read()
	if err != nil {
		t.Fatalf("Read refusal: %v", err)
	}
	kind, err := wire.Kind(frame)
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	if kind != wire.KindRefuse {
		t.Fatalf("frame kind = %d, want refuse", kind)
	}
	var m wire.Refusal
	if _, err := wire.Decode(frame, &m); err != nil {
		t.Fatalf("Decode refusal: %v", err)
	}
	return &m
}

func TestOwnerGreetsWithItsSchemaAndOrigin(t *testing.T) {
	_, sock := startServer(t)
	w, _ := dialRaw(t, sock)
	sendHello(t, w, wire.Version)

	got := welcome(t, w)
	if got.SchemaHave != 3 || got.SchemaKnow != 9 || got.Origin != "01ORIGIN" {
		t.Errorf("welcome = have %d know %d origin %q", got.SchemaHave, got.SchemaKnow, got.Origin)
	}
}

func TestOwnerRefusesWrongProto(t *testing.T) {
	_, sock := startServer(t)
	w, _ := dialRaw(t, sock)
	sendHello(t, w, wire.Version+1)

	got := refusal(t, w)
	if got.Code != wire.RefuseWrongProto {
		t.Errorf("refusal code = %q, want %q", got.Code, wire.RefuseWrongProto)
	}
}

func TestOwnerRefusesWhenShuttingDown(t *testing.T) {
	srv, sock := startServer(t)

	// A live connection is served as usual before the shutdown.
	w, _ := dialRaw(t, sock)
	sendHello(t, w, wire.Version)
	welcome(t, w)

	// Mark the server as shutting down without closing the listener, which is
	// the window in which a client must be told rather than dropped.
	srv.mu.Lock()
	srv.closed = true
	srv.mu.Unlock()

	w2, _ := dialRaw(t, sock)
	// The owner refuses before it reads hello, so its close can arrive first;
	// the refusal frame it wrote is still readable.
	_ = tryHello(w2, wire.Version)
	got := refusal(t, w2)
	if got.Code != wire.RefuseShuttingDown {
		t.Errorf("refusal code = %q, want %q", got.Code, wire.RefuseShuttingDown)
	}
}

func TestConnectionCapWaitsInsteadOfRefusing(t *testing.T) {
	old := maxConns
	maxConns = 2
	defer func() { maxConns = old }()

	_, sock := startServer(t)

	// Fill the cap with two live connections.
	c1, n1 := dialRaw(t, sock)
	sendHello(t, c1, wire.Version)
	welcome(t, c1)
	c2, _ := dialRaw(t, sock)
	sendHello(t, c2, wire.Version)
	welcome(t, c2)

	// A third client is not refused; it waits. A short read deadline shows no
	// welcome arrived while the cap is full.
	c3, n3 := dialRaw(t, sock)
	sendHello(t, c3, wire.Version)
	if err := n3.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := c3.Read(); err == nil {
		t.Fatal("a third connection was served over the cap")
	}
	_ = n3.Close()

	// Free a slot; a fresh connection is served again.
	_ = n1.Close()
	c4, n4 := dialRaw(t, sock)
	if err := n4.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	sendHello(t, c4, wire.Version)
	welcome(t, c4)
}

func TestOwnerDropsADifferentUid(t *testing.T) {
	old := ownerUID
	ownerUID = os.Getuid() + 1
	defer func() { ownerUID = old }()

	_, sock := startServer(t)
	w, nc := dialRaw(t, sock)
	if err := nc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	// The owner may have dropped the connection before hello was written, so a
	// write error is the drop the test is looking for.
	if err := tryHello(w, wire.Version); err != nil {
		return
	}
	if _, err := w.Read(); err == nil {
		t.Fatal("owner served a peer with a different uid")
	}
}
