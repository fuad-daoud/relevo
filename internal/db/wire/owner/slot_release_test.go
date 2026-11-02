//go:build unix

package owner

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// TestIdleConnectionHoldsNoSlot pins the per-statement slot: a client that ran
// one statement to completion outside a transaction and sits connected and
// idle holds no slot, so a second client's request is served rather than
// refused.
func TestIdleConnectionHoldsNoSlot(t *testing.T) {
	oldMax, oldWait := maxConns, slotWait
	maxConns = 1
	SetSlotWait(100 * time.Millisecond)
	defer func() {
		maxConns = oldMax
		SetSlotWait(oldWait)
	}()

	srv, sock := startServer(t)

	a, na := dialRaw(t, sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)
	execRaw(t, a, 1, `CREATE TABLE t (n INTEGER)`)
	readDone(t, a, na)

	// The finished statement returned its slot while A stays connected.
	waitForSlot(t, srv, 0)

	b, nb := dialRaw(t, sock)
	sendHello(t, b, wire.Version)
	welcome(t, b)
	execRaw(t, b, 1, `INSERT INTO t (n) VALUES (1)`)
	readDoneWithin(t, b, nb, readAllowance)
}

// TestIdleQueryHoldsNoSlot pins the same release on the query path: a query
// read to its done, and one that fails, both free the slot while the client
// stays connected.
func TestIdleQueryHoldsNoSlot(t *testing.T) {
	oldMax, oldWait := maxConns, slotWait
	maxConns = 1
	SetSlotWait(100 * time.Millisecond)
	defer func() {
		maxConns = oldMax
		SetSlotWait(oldWait)
	}()

	srv, sock := startServer(t)

	a, na := dialRaw(t, sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)
	execRaw(t, a, 1, `CREATE TABLE t (n INTEGER)`)
	readDone(t, a, na)
	execRaw(t, a, 2, `INSERT INTO t (n) VALUES (1)`)
	readDone(t, a, na)

	if err := na.SetReadDeadline(time.Now().Add(readAllowance)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if err := a.Write(queryFrame(`SELECT n FROM t`)); err != nil {
		t.Fatalf("Write query: %v", err)
	}
	// One row fits the first batch, so a single rows frame and the done follow.
	readKind(t, a, na, wire.KindRows)
	readKind(t, a, na, wire.KindDone)
	if err := na.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	waitForSlot(t, srv, 0)

	// A query that fails frees the slot too: nothing ran, so nothing is kept.
	if err := na.SetReadDeadline(time.Now().Add(readAllowance)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if err := a.Write(queryFrame(`SELECT n FROM missing`)); err != nil {
		t.Fatalf("Write query: %v", err)
	}
	readKind(t, a, na, wire.KindError)
	if err := na.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	waitForSlot(t, srv, 0)

	b, nb := dialRaw(t, sock)
	sendHello(t, b, wire.Version)
	welcome(t, b)
	execRaw(t, b, 1, `INSERT INTO t (n) VALUES (2)`)
	readDoneWithin(t, b, nb, readAllowance)
}

// TestTransactionHoldsSlotAcrossStatements pins that the slot is per
// transaction, not per statement, while one is open: a client inside BEGIN
// keeps its slot, so a second client is refused busy; after the COMMIT the
// slot is free and a further request is served.
func TestTransactionHoldsSlotAcrossStatements(t *testing.T) {
	oldMax, oldWait := maxConns, slotWait
	maxConns = 1
	SetSlotWait(100 * time.Millisecond)
	defer func() {
		maxConns = oldMax
		SetSlotWait(oldWait)
	}()

	srv, sock := startServer(t)

	a, na := dialRaw(t, sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)
	execRaw(t, a, 1, `CREATE TABLE t (n INTEGER)`)
	readDone(t, a, na)
	execRaw(t, a, 2, "BEGIN")
	readDone(t, a, na)

	// Inside the transaction A keeps its slot: B is refused busy inside the
	// shrunk bound, naming the held and maximum slot counts.
	b, nb := dialRaw(t, sock)
	sendHello(t, b, wire.Version)
	welcome(t, b)
	execRaw(t, b, 1, `INSERT INTO t (n) VALUES (1)`)
	if err := nb.SetReadDeadline(time.Now().Add(readAllowance)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	got := refusal(t, b)
	if got.Code != wire.RefuseBusy {
		t.Errorf("refusal code = %q, want %q", got.Code, wire.RefuseBusy)
	}
	if !strings.Contains(got.Message, "1/1") {
		t.Errorf("refusal message = %q, want it to name the held and maximum slots (1/1)", got.Message)
	}
	if err := nb.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	// After the COMMIT the slot is free: a further request is served.
	execRaw(t, a, 3, "COMMIT")
	readDone(t, a, na)
	waitForSlot(t, srv, 0)
	execRaw(t, b, 2, `INSERT INTO t (n) VALUES (2)`)
	readDoneWithin(t, b, nb, readAllowance)
}

// TestReleaseAfterTakePinnedFreesNoSlot pins the single-free rule: when cleanup
// already detached the connection, release frees nothing, so the slot is not
// returned twice.
func TestReleaseAfterTakePinnedFreesNoSlot(t *testing.T) {
	c, pinned, _ := newAckConn(t)
	if got := len(c.s.sem); got != 1 {
		t.Fatalf("len(sem) after pin = %d, want 1", got)
	}
	// Cleanup's half first: detach the connection as a disconnect would.
	if detached := c.takePinned(); detached != pinned {
		t.Fatal("takePinned did not detach the pinned connection")
	}
	c.release()
	if got := len(c.s.sem); got != 1 {
		t.Errorf("len(sem) after release with nothing pinned = %d, want 1", got)
	}
}
