//go:build unix

package owner

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// helloAdHoc sends a hello with the ad-hoc marker set or clear, which the
// shared sendHello helper cannot express.
func helloAdHoc(t *testing.T, w *wire.Conn, adHoc bool) {
	t.Helper()
	payload, err := wire.Encode(wire.KindHello, &wire.Hello{
		Header:  wire.Header{Type: wire.TypeHello},
		Proto:   wire.Proto,
		Version: wire.Version,
		AdHoc:   adHoc,
	}, nil)
	if err != nil {
		t.Fatalf("Encode hello: %v", err)
	}
	if err := w.Write(payload); err != nil {
		t.Fatalf("Write hello: %v", err)
	}
}

// readRefusal reads one refusal with a fresh deadline, so a deadline a previous
// read set cannot fail this read.
func readRefusal(t *testing.T, w *wire.Conn, nc net.Conn) *wire.Refusal {
	t.Helper()
	if err := nc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	return refusal(t, w)
}

// waitNotReaping blocks until the server has no abandoned statement registered.
func waitNotReaping(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !srv.reaping() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the abandoned registration never cleared")
}

// TestAbandonedStatementFiresTheReapHookOnce pins the once-only guarantee: one
// registration whose statement never finishes calls the hook exactly once, no
// matter how many graces pass.
func TestAbandonedStatementFiresTheReapHookOnce(t *testing.T) {
	SetReapGrace(30 * time.Millisecond)
	t.Cleanup(func() { SetReapGrace(0) })

	srv := New(nil, 3, 9, "01ORIGIN", nil)
	var calls atomic.Int32
	srv.OnAbandoned = func() { calls.Add(1) }

	srv.noteAbandoned(make(chan struct{}))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("the hook ran %d times after the grace, want 1", got)
	}

	// Several more graces must not fire it again.
	time.Sleep(150 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Errorf("the hook ran %d times, want exactly 1", got)
	}
}

// TestAStatementThatEndsBeforeTheGraceIsNotReaped pins the grace's other side:
// a statement whose rollback and discard finish first clears the registration,
// so no hook fires and ad-hoc reads are never refused for it.
func TestAStatementThatEndsBeforeTheGraceIsNotReaped(t *testing.T) {
	SetReapGrace(200 * time.Millisecond)
	t.Cleanup(func() { SetReapGrace(0) })

	srv := New(nil, 3, 9, "01ORIGIN", nil)
	fired := make(chan struct{})
	srv.OnAbandoned = func() { close(fired) }

	finish := make(chan struct{})
	srv.noteAbandoned(finish)
	close(finish)

	select {
	case <-fired:
		t.Fatal("a statement that ended before the grace was reaped")
	case <-time.After(400 * time.Millisecond):
	}
	if srv.reaping() {
		t.Error("the registration was not cleared when the statement ended")
	}
}

// TestOwnerRefusesAdHocRequestsWhileAStatementIsAbandoned pins the refusal: an
// ad-hoc connection is refused with wire.RefuseReaping while an abandoned
// statement is registered, a connection without the marker is served, and both
// are served again once the statement ends.
func TestOwnerRefusesAdHocRequestsWhileAStatementIsAbandoned(t *testing.T) {
	SetReapGrace(time.Hour)
	t.Cleanup(func() { SetReapGrace(0) })

	srv, sock := startServer(t)

	plainW, plainNC := dialRaw(t, sock)
	helloAdHoc(t, plainW, false)
	welcome(t, plainW)

	adhocW, adhocNC := dialRaw(t, sock)
	helloAdHoc(t, adhocW, true)
	welcome(t, adhocW)

	execRaw(t, plainW, 1, `SELECT 1`)
	readDone(t, plainW, plainNC)
	execRaw(t, adhocW, 1, `SELECT 1`)
	readDone(t, adhocW, adhocNC)

	released := make(chan struct{})
	srv.noteAbandoned(released)

	execRaw(t, adhocW, 2, `SELECT 1`)
	if got := readRefusal(t, adhocW, adhocNC); got.Code != wire.RefuseReaping {
		t.Errorf("ad-hoc refusal code = %q, want %q", got.Code, wire.RefuseReaping)
	}

	execRaw(t, plainW, 2, `SELECT 1`)
	readDone(t, plainW, plainNC)

	close(released)
	waitNotReaping(t, srv)

	execRaw(t, adhocW, 3, `SELECT 1`)
	readDone(t, adhocW, adhocNC)
}
