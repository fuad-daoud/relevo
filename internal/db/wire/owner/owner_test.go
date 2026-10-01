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
	srv := New(sqlDB, 3, 9, "01ORIGIN", nil)
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

// execRaw writes one exec frame without waiting for its answer.
func execRaw(t *testing.T, w *wire.Conn, id int, query string) {
	t.Helper()
	payload, err := wire.Encode(wire.KindExec, &wire.Exec{
		Header: wire.Header{Type: wire.TypeExec, ID: id},
		Query:  query,
	}, nil)
	if err != nil {
		t.Fatalf("Encode exec: %v", err)
	}
	if err := w.Write(payload); err != nil {
		t.Fatalf("Write exec: %v", err)
	}
}

// readDone reads one frame and requires it to be a done answer.
func readDone(t *testing.T, w *wire.Conn, nc net.Conn) {
	t.Helper()
	if err := nc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	frame, err := w.Read()
	if err != nil {
		t.Fatalf("Read done: %v", err)
	}
	if kind, err := wire.Kind(frame); err != nil || kind != wire.KindDone {
		t.Fatalf("frame kind = %d (%v), want done", kind, err)
	}
}

// TestConnCountCountsLiveConnections pins the count the welcome and the
// daemon's idle watcher read: 0 with no client, 1 after a handshake, and back
// to 0 once the client closes.
func TestConnCountCountsLiveConnections(t *testing.T) {
	srv, sock := startServer(t)
	if got := srv.ConnCount(); got != 0 {
		t.Fatalf("ConnCount with no client = %d, want 0", got)
	}

	w, nc := dialRaw(t, sock)
	sendHello(t, w, wire.Version)
	_ = welcome(t, w)
	if got := srv.ConnCount(); got != 1 {
		t.Fatalf("ConnCount after the handshake = %d, want 1", got)
	}

	_ = nc.Close()
	deadline := time.Now().Add(2 * time.Second)
	for srv.ConnCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("ConnCount = %d after the client closed, want 0", srv.ConnCount())
		}
		time.Sleep(time.Millisecond)
	}
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
	maxConns = 1
	defer func() { maxConns = old }()

	_, sock := startServer(t)

	// One client pins the single slot for the life of its connection.
	a, na := dialRaw(t, sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)
	execRaw(t, a, 1, `CREATE TABLE t (n INTEGER)`)
	readDone(t, a, na)

	// A second client's handshake is served while the cap is full: the cap
	// never bounds the handshake, so a short deadline is enough to prove it.
	b, nb := dialRaw(t, sock)
	if err := nb.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	sendHello(t, b, wire.Version)
	welcome(t, b)
	if err := nb.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	// Its request waits for the slot, and keeps waiting longer than the
	// client's two-second handshake timeout: a cap that refused, or that
	// bounded the handshake, would have failed this client already.
	execRaw(t, b, 1, `INSERT INTO t (n) VALUES (1)`)
	if err := nb.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := b.Read(); err == nil {
		t.Fatal("a request was served over the cap")
	}
	if err := nb.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	// Free the slot; the waiting request now succeeds.
	_ = na.Close()
	readDone(t, b, nb)
}

func TestLiveConnectionCapDropsTheExtra(t *testing.T) {
	old := maxLiveConns
	maxLiveConns = 1
	defer func() { maxLiveConns = old }()

	_, sock := startServer(t)

	// The first connection handshakes and then sits idle, holding the one live
	// slot for as long as it stays open.
	a, na := dialRaw(t, sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)

	// The next dial is dropped without a frame, so its hello is either never
	// written or never answered: one of the two fails within the deadline.
	b, nb := dialRaw(t, sock)
	if err := nb.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetWriteDeadline: %v", err)
	}
	if err := tryHello(b, wire.Version); err != nil {
		// The owner closed the connection before the hello could be written.
	} else {
		if err := nb.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		if _, err := b.Read(); err == nil {
			t.Fatal("owner served a connection over the live bound")
		}
	}
	_ = nb.SetWriteDeadline(time.Time{})

	// The holder is still served: its request pins a database connection and
	// returns a done, so dropping the extra connection leaves existing work
	// and the pinned path untouched.
	execRaw(t, a, 1, `CREATE TABLE t (n INTEGER)`)
	readDone(t, a, na)
}

// liveConn returns the server's one live connection.
func liveConn(t *testing.T, s *Server) *conn {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		return c
	}
	t.Fatal("server has no live connection")
	return nil
}

// TestAFinishedRequestDoesNotDropTheNextOne pins the slot hand-off: a finished
// request clears its slot from its own goroutine, so a request that arrives in
// that window waits for the clear rather than having the whole connection
// dropped as if the client had pipelined two requests.
func TestAFinishedRequestDoesNotDropTheNextOne(t *testing.T) {
	srv, sock := startServer(t)
	w, nc := dialRaw(t, sock)
	sendHello(t, w, wire.Version)
	welcome(t, w)

	// A request whose goroutine has answered but not yet released its slot is
	// the window this test widens by hand.
	c := liveConn(t, srv)
	held, _ := newRequest(99)
	c.mu.Lock()
	c.cur = held
	c.mu.Unlock()
	go func() {
		time.Sleep(100 * time.Millisecond)
		c.mu.Lock()
		if c.cur == held {
			c.cur = nil
		}
		c.mu.Unlock()
		close(held.done)
	}()

	// The next request must be served, not dropped with the connection.
	execRaw(t, w, 1, `CREATE TABLE t (n INTEGER)`)
	readDone(t, w, nc)
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

// TestPeerAllowedAcceptsRoot pins the socket peer rule: root and the owner's
// own uid are allowed, any other uid is refused.
func TestPeerAllowedAcceptsRoot(t *testing.T) {
	cases := []struct {
		peer, own int
		want      bool
	}{
		{0, 1000, true},    // root reaches every owner's socket
		{0, 0, true},       // root owns a root-owned socket
		{1000, 1000, true}, // the owner itself
		{1001, 1000, false},
		{1000, 0, false},
	}
	for _, tc := range cases {
		if got := peerAllowed(tc.peer, tc.own); got != tc.want {
			t.Errorf("peerAllowed(%d, %d) = %v, want %v", tc.peer, tc.own, got, tc.want)
		}
	}
}
