package delivery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// readPushLine reads one NDJSON push event.
func readPushLine(t *testing.T, r *bufio.Reader) PushEvent {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read push line: %v", err)
	}
	var ev PushEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		t.Fatalf("decode push line %q: %v", line, err)
	}
	return ev
}

// claimableCount reports how many entries a reader could claim for name.
func claimableCount(t *testing.T, rt Deps, name string) int {
	t.Helper()
	var n int
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		entries, err := tx.ClaimableForMasterMindThrough(name, 0)
		n = len(entries)
		return err
	}); err != nil {
		t.Fatalf("claimable scan: %v", err)
	}
	return n
}

// pushWaitFor polls cond until it holds or the timeout elapses.
func pushWaitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// admitEntryAt marks name's oldest pending entry admitted, standing in for a
// holder that crashed between the admit and its ack.
func admitEntryAt(t *testing.T, rt Deps, name string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, idx, found, err := tx.PendingForMasterMind(name)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("no pending entry for %s", name)
		}
		return tx.AdmitIndex(name, idx)
	}); err != nil {
		t.Fatalf("admit %s: %v", name, err)
	}
}

// TestRunPushAdmitsWritesConfirmsOnAck is the loop's required case: the entry is
// admitted before its line is written, no reader may claim it while admitted,
// and the matching ack is what confirms it with route=push.
func TestRunPushAdmitsWritesConfirmsOnAck(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{}
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	ackIn, ackWriter := io.Pipe()
	lineOut, lineWriter := io.Pipe()
	defer func() { _ = ackIn.Close() }()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, ackIn, lineWriter) }()

	ev := readPushLine(t, bufio.NewReader(lineOut))
	if ev.Binding != "webshop" || ev.Kind != string(store.KindReport) || ev.Round != 1 {
		t.Fatalf("push event = %+v, want webshop round 1 report", ev)
	}
	if !strings.Contains(ev.Text, "round 1 report") {
		t.Errorf("push text = %q, want the report payload", ev.Text)
	}

	// The admit landed first: no reader may claim or print the entry while the
	// holder is about to hand it to the mod.
	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Errorf("claimable entries while admitted = %d, want 0", n)
	}
	if delivered, err := PullPendingThroughEntries(ctx, rt.Store, "webshop", "wait", 0); err != nil || len(delivered) != 0 {
		t.Errorf("a wait must not pull an admitted entry: delivered=%d err=%v", len(delivered), err)
	}

	// The entry is not confirmed before its ack: only the ack line confirms it.
	// The pause lets a confirm-on-write mutation show itself.
	time.Sleep(250 * time.Millisecond)
	if entries, err := rt.Store.ReadLog("webshop"); err != nil || len(entries) != 1 || entries[0].Confirmed {
		t.Fatalf("the entry must not be confirmed before its ack: %v (entries=%d)", err, len(entries))
	}

	if _, err := fmt.Fprintf(ackWriter, "ack %d\n", ev.Seq); err != nil {
		t.Fatalf("write ack: %v", err)
	}
	pushWaitFor(t, 2*time.Second, func() bool {
		entries, err := rt.Store.ReadLog("webshop")
		return err == nil && len(entries) == 1 && entries[0].Confirmed && entries[0].Route == "push"
	}, "the entry confirmed with route=push")

	// stdin EOF ends the holder cleanly and releases the claim.
	if err := ackWriter.Close(); err != nil {
		t.Fatalf("close ack side: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPush: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return on stdin EOF")
	}
	if c, err := rt.Channels.Live(testClaimMasterMind, rt.Now()); err != nil || c != nil {
		t.Errorf("the claim must be released on exit, got %+v (err %v)", c, err)
	}
}

// TestDeliverPendingClearsOrphanedAdmit is the crash case: an entry a dead
// holder admitted, with no live claim, is cleared by DeliverPending so the
// background wait can claim it again. Removing the clearOrphanAdmit call from
// DeliverPending makes this fail.
func TestDeliverPendingClearsOrphanedAdmit(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{} // no live claim: the holder is gone
	b := seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")

	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Fatalf("claimable while admitted = %d, want 0", n)
	}

	_, got := deliverOnce(t, rt, b)
	if got.Route != "pull" {
		t.Fatalf("Route = %q, want pull once the orphan admit is cleared", got.Route)
	}
	if n := claimableCount(t, rt, "webshop"); n != 1 {
		t.Errorf("claimable after DeliverPending = %d, want the entry back", n)
	}
}

// TestRunPushRestartResendsUnackedEntry: a holder that starts after a crash
// clears the dead holder's admit and re-sends the entry with a fresh line.
func TestRunPushRestartResendsUnackedEntry(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{}
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")

	ackIn, ackWriter := io.Pipe()
	lineOut, lineWriter := io.Pipe()
	defer func() { _ = ackIn.Close() }()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, ackIn, lineWriter) }()

	ev := readPushLine(t, bufio.NewReader(lineOut))
	if ev.Binding != "webshop" || ev.Round != 1 {
		t.Fatalf("restarted holder wrote %+v, want the webshop round 1 line", ev)
	}

	_ = ackWriter.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return on stdin EOF")
	}
}

// TestRunPushSecondHolderRefused: a live claim with a different pid refuses the
// new holder with ErrClaimHeld, so only one process drains a mailbox.
func TestRunPushSecondHolderRefused(t *testing.T) {
	t.Parallel()

	claims, _ := testClaims(t)
	if err := claims.Write(Claim{MasterMind: testClaimMasterMind, PID: 4242, StartedAt: baseTime, SeenAt: baseTime}, baseTime); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	rt := Deps{Store: store.New(t.TempDir()), Now: func() time.Time { return baseTime }, Channels: claims}

	err := RunPush(context.Background(), rt, testClaimMasterMind, strings.NewReader(""), io.Discard)
	if !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("RunPush with a held claim = %v, want ErrClaimHeld", err)
	}
}

// TestConfirmedChannelRouteRowRenders keeps the rename's history honest: a row
// confirmed under the old route "channel" stays readable and never returns to
// the claimable scans.
func TestConfirmedChannelRouteRowRenders(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	if err := rt.Store.ConfirmIndex("webshop", 0, "channel"); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadLog: %v (entries=%d)", err, len(entries))
	}
	if !entries[0].Confirmed || entries[0].Route != "channel" {
		t.Errorf("entry = confirmed:%v route:%q, want a readable channel-route row", entries[0].Confirmed, entries[0].Route)
	}
	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Errorf("a confirmed channel row must not be claimable, got %d", n)
	}
}
