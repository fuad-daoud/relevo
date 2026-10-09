package delivery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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
// and the ack verb is what confirms it with route=push.
func TestRunPushAdmitsWritesConfirmsOnAck(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{}
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, lineWriter) }()

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

	// The entry is not confirmed before the ack: only the ack verb confirms it.
	// The pause lets a confirm-on-write mutation show itself.
	time.Sleep(250 * time.Millisecond)
	if entries, err := rt.Store.ReadLog("webshop"); err != nil || len(entries) != 1 || entries[0].Confirmed {
		t.Fatalf("the entry must not be confirmed before its ack: %v (entries=%d)", err, len(entries))
	}

	if _, err := AckPush(rt, testClaimMasterMind, "webshop", ev.Seq); err != nil {
		t.Fatalf("AckPush: %v", err)
	}
	pushWaitFor(t, 2*time.Second, func() bool {
		entries, err := rt.Store.ReadLog("webshop")
		return err == nil && len(entries) == 1 && entries[0].Confirmed && entries[0].Route == "push"
	}, "the entry confirmed with route=push")

	// Cancelling ctx ends the holder cleanly and releases the claim.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPush: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return after its context was cancelled")
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

// TestConfirmAdmittedClearsOrphanedAdmit is the orphan case on the done/paused
// path: a mastermind with no deliverer (a Claude kind) has an admitted,
// unconfirmed entry and NO live push claim, so the admit is orphaned.
// ConfirmAdmitted must clear it -- which is what makes the entry claimable
// again -- and must not confirm it. Removing the clearOrphanAdmit call from
// ConfirmAdmitted makes this fail.
func TestConfirmAdmittedClearsOrphanedAdmit(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{} // no live claim: the holder is gone
	b := seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	b.State = store.StateDone
	admitEntryAt(t, rt, "webshop")

	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Fatalf("claimable while admitted = %d, want 0", n)
	}

	_, got := confirmAdmittedOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want nothing delivered for an orphaned admit", got)
	}
	if admitOf(t, rt, b.Name) != nil {
		t.Error("ConfirmAdmitted must clear the orphaned admit")
	}
	if n := claimableCount(t, rt, "webshop"); n != 1 {
		t.Errorf("claimable after ConfirmAdmitted = %d, want the entry back", n)
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Confirmed {
			t.Error("an orphaned admit must not be confirmed by ConfirmAdmitted")
		}
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("the orphaned entry must stay unconfirmed: pending=%v err=%v", pending, err)
	}
}

// TestConfirmAdmittedKeepsAdmitWithALiveClaim is the mirror of the orphan
// case: while the push claim IS live the holder owns the entry, so the same
// admit is left in place and the entry is not confirmed either. Flipping the
// live-claim condition in clearOrphanAdmit makes this fail.
func TestConfirmAdmittedKeepsAdmitWithALiveClaim(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{testClaimMasterMind: &Claim{MasterMind: testClaimMasterMind, PID: 1, SeenAt: rt.Now()}}
	b := seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	b.State = store.StateDone
	admitEntryAt(t, rt, "webshop")

	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Fatalf("claimable while admitted = %d, want 0", n)
	}

	_, got := confirmAdmittedOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want nothing delivered while the claim is live", got)
	}
	if admitOf(t, rt, b.Name) == nil {
		t.Error("a live claim owns the entry: ConfirmAdmitted must leave the admit in place")
	}
	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Errorf("claimable with a live claim = %d, want the entry still admitted", n)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("the entry must stay pending for the live holder: pending=%v err=%v", pending, err)
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

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, lineWriter) }()

	ev := readPushLine(t, bufio.NewReader(lineOut))
	if ev.Binding != "webshop" || ev.Round != 1 {
		t.Fatalf("restarted holder wrote %+v, want the webshop round 1 line", ev)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return after its context was cancelled")
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

	err := RunPush(context.Background(), rt, testClaimMasterMind, io.Discard)
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

// seedBinding saves an own binding with no log entry, so a step sees only its
// state.
func seedBinding(t *testing.T, rt Deps, name, mastermindID string, state store.State) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:         name,
		CWD:          "/repo/" + name,
		Round:        1,
		State:        state,
		MasterMind:   store.Endpoint{Kind: "claude", SessionID: "sess"},
		MasterMindID: mastermindID,
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.Save(b) }); err != nil {
		t.Fatalf("seed binding %s: %v", name, err)
	}
	return b
}

// setBindingState rewrites one saved binding's state.
func setBindingState(t *testing.T, rt Deps, name string, state store.State) {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	b.State = state
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.Save(b) }); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// stepRun is a pushRun a test can drive one step at a time: an in-memory out
// and empty per-step state.
func stepRun(t *testing.T, rt Deps) (*pushRun, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	p := &pushRun{
		d:            rt,
		mastermindID: testClaimMasterMind,
		out:          out,
		unacked:      map[pushAdmit]struct{}{},
		last:         map[string]store.State{},
	}
	return p, out
}

// stepOnce runs one step and returns the lines it wrote.
func stepOnce(t *testing.T, p *pushRun, out *bytes.Buffer) []PushEvent {
	t.Helper()
	out.Reset()
	if _, err := p.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	return decodePushLines(t, out.Bytes())
}

// decodePushLines decodes every NDJSON line in raw.
func decodePushLines(t *testing.T, raw []byte) []PushEvent {
	t.Helper()
	var evs []PushEvent
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var ev PushEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("decode push line %q: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan push lines: %v", err)
	}
	return evs
}

// pushEventChan decodes NDJSON push lines into a channel until the reader
// errors or EOF.
func pushEventChan(r *bufio.Reader) <-chan PushEvent {
	out := make(chan PushEvent)
	go func() {
		defer close(out)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var ev PushEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				return
			}
			out <- ev
		}
	}()
	return out
}

// waitPushEvent returns the next push event or fails the test on timeout.
func waitPushEvent(t *testing.T, events <-chan PushEvent, timeout time.Duration) PushEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("push event stream closed")
		}
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a push event")
		return PushEvent{}
	}
}

// TestNextAdmittedReturnsAdmittedEntry: the find-and-admit helper hands back an
// entry it has already admitted, so no reader may claim it.
func TestNextAdmittedReturnsAdmittedEntry(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	b, entry, idx, found, err := nextAdmitted(rt, testClaimMasterMind)
	if err != nil {
		t.Fatalf("nextAdmitted: %v", err)
	}
	if !found || b.Name != "webshop" || idx != 0 || entry.Kind != store.KindReport {
		t.Fatalf("nextAdmitted = %+v entry=%+v idx=%d found=%v", b, entry, idx, found)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 1 || entries[0].AdmittedAt == nil {
		t.Fatalf("the returned entry is not admitted: %+v", entries)
	}
	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Errorf("claimable after nextAdmitted = %d, want 0", n)
	}
}

// TestNextAdmittedSkipsConfirmedEntry: an entry a reader confirmed before the
// helper runs is never returned.
func TestNextAdmittedSkipsConfirmedEntry(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	if err := rt.Store.ConfirmIndex("webshop", 0, "wait"); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	_, _, _, found, err := nextAdmitted(rt, testClaimMasterMind)
	if err != nil {
		t.Fatalf("nextAdmitted: %v", err)
	}
	if found {
		t.Error("nextAdmitted returned an entry a reader already confirmed")
	}
}

// TestRunPushNeverPullsAndWritesOneEntry pins the one-lock find-and-admit: a
// reader that runs in the seam between a split scan and admit must find
// nothing, because the entry is already admitted. Moving the admit out to a
// second lock lets the reader claim the entry the holder also writes, and this
// test fails.
func TestRunPushNeverPullsAndWritesOneEntry(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{}
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type pullResult struct {
		n   int
		err error
	}
	pulled := make(chan pullResult, 1)
	seam := func() {
		delivered, err := PullPendingThroughEntries(ctx, rt.Store, "webshop", "wait", 0)
		pulled <- pullResult{n: len(delivered), err: err}
	}

	done := make(chan error, 1)
	go func() { done <- runPush(ctx, rt, testClaimMasterMind, lineWriter, seam) }()

	ev := readPushLine(t, bufio.NewReader(lineOut))
	if ev.Binding != "webshop" || ev.Kind != string(store.KindReport) {
		t.Fatalf("push line = %+v, want the webshop report", ev)
	}

	got := <-pulled
	if got.err != nil {
		t.Fatalf("reader pull: %v", got.err)
	}
	if got.n != 0 {
		t.Fatalf("a reader pulled %d entries the holder also wrote", got.n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return after its context was cancelled")
	}
}

// TestPushStateLineOnEnteringNeedsYou pins the transition rule: running ->
// needs_you writes exactly one state line, and staying in needs_you writes no
// second one. A transition check that ignores old_state writes on every poll
// and fails this test.
func TestPushStateLineOnEnteringNeedsYou(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedBinding(t, rt, "webshop", testClaimMasterMind, store.StateActive)
	p, out := stepRun(t, rt)

	if evs := stepOnce(t, p, out); len(evs) != 0 {
		t.Fatalf("active wrote %+v, want no line", evs)
	}

	setBindingState(t, rt, "webshop", store.StateNeedsYou)
	evs := stepOnce(t, p, out)
	if len(evs) != 1 {
		t.Fatalf("running -> needs_you wrote %d lines, want 1: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.Kind != "state" || ev.State != "needs_you" || ev.Seq != 0 {
		t.Errorf("state event = %+v, want kind state seq 0 state needs_you", ev)
	}
	if ev.Binding != "webshop" || ev.Round != 1 || ev.OldState != "active" {
		t.Errorf("state event = %+v, want webshop round 1 old_state active", ev)
	}
	if !strings.Contains(ev.Text, "NEEDS YOU") {
		t.Errorf("state text = %q, want the NEEDS YOU label", ev.Text)
	}

	if evs := stepOnce(t, p, out); len(evs) != 0 {
		t.Fatalf("staying in needs_you wrote a second line: %+v", evs)
	}
}

// TestPushStateLineOnLeavingAndReturningUnhealthy: needs_you -> running ->
// broken announces the second unhealthy state in its own line, after the
// healthy one reset the transition.
func TestPushStateLineOnLeavingAndReturningUnhealthy(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedBinding(t, rt, "webshop", testClaimMasterMind, store.StateNeedsYou)
	p, out := stepRun(t, rt)

	if evs := stepOnce(t, p, out); len(evs) != 1 || evs[0].State != "needs_you" {
		t.Fatalf("first sight needs_you wrote %+v, want one needs_you line", evs)
	}
	setBindingState(t, rt, "webshop", store.StateActive)
	if evs := stepOnce(t, p, out); len(evs) != 0 {
		t.Fatalf("needs_you -> active wrote %+v, want no line", evs)
	}
	setBindingState(t, rt, "webshop", store.StateBroken)
	evs := stepOnce(t, p, out)
	if len(evs) != 1 || evs[0].State != "broken" || evs[0].OldState != "active" {
		t.Fatalf("active -> broken wrote %+v, want one broken line from active", evs)
	}
	if !strings.Contains(evs[0].Text, "BROKEN") {
		t.Errorf("state text = %q, want the BROKEN label", evs[0].Text)
	}
}

// TestPushStateLineConfirmsNothing: a state line carries seq 0 and leaves the
// log untouched -- it is not an entry, so it is never confirmed or admitted.
func TestPushStateLineConfirmsNothing(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedBinding(t, rt, "webshop", testClaimMasterMind, store.StateActive)
	p, out := stepRun(t, rt)

	setBindingState(t, rt, "webshop", store.StateNeedsYou)
	evs := stepOnce(t, p, out)
	if len(evs) != 1 || evs[0].Seq != 0 || evs[0].Kind != "state" {
		t.Fatalf("state line = %+v, want one seq 0 state line", evs)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a state line wrote a log entry: %+v", entries)
	}
	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Errorf("claimable after a state line = %d, want 0", n)
	}
}

// TestRunPushDeliversTwoEntriesInOrderAsAcked pins the ordering and the
// one-entry-at-a-time rule with nothing to drive from stdin: two bindings'
// entries arrive in binding-name order, no second line is written before the
// first ack, and the holder is still running after the last one.
func TestRunPushDeliversTwoEntriesInOrderAsAcked(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{}
	seedPending(t, rt, "alpha", testClaimMasterMind, "claude")
	seedPending(t, rt, "beta", testClaimMasterMind, "claude")

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, lineWriter) }()

	events := pushEventChan(bufio.NewReader(lineOut))
	first := waitPushEvent(t, events, 2*time.Second)
	if first.Binding != "alpha" {
		t.Fatalf("first line = %+v, want the alpha binding first", first)
	}

	// Unacked, the holder waits: the claim is still live and no beta line
	// arrives, so the drain is one entry deep.
	select {
	case got := <-events:
		t.Fatalf("a second line arrived before the first ack: %+v", got)
	case <-time.After(pushPollEvery + 200*time.Millisecond):
	}
	if c, err := rt.Channels.Live(testClaimMasterMind, rt.Now()); err != nil || c == nil {
		t.Errorf("the holder must still hold its claim between entries: %+v (err %v)", c, err)
	}

	if _, err := AckPush(rt, testClaimMasterMind, "alpha", first.Seq); err != nil {
		t.Fatalf("AckPush alpha: %v", err)
	}
	second := waitPushEvent(t, events, 2*time.Second)
	if second.Binding != "beta" {
		t.Fatalf("second line = %+v, want the beta binding next", second)
	}
	if _, err := AckPush(rt, testClaimMasterMind, "beta", second.Seq); err != nil {
		t.Fatalf("AckPush beta: %v", err)
	}

	for _, name := range []string{"alpha", "beta"} {
		pushWaitFor(t, 2*time.Second, func() bool {
			entries, err := rt.Store.ReadLog(name)
			return err == nil && len(entries) == 1 && entries[0].Confirmed && entries[0].Route == "push"
		}, name+" confirmed with route push")
	}

	// Every entry is delivered, so the only thing left that can end the holder
	// is its context or its claim. A stdin EOF would end it here instead, and
	// the holder would have no reason left to be alive.
	select {
	case err := <-done:
		t.Fatalf("RunPush returned with no reason to stop: %v", err)
	case <-time.After(pushPollEvery + 200*time.Millisecond):
	}
	if c, err := rt.Channels.Live(testClaimMasterMind, rt.Now()); err != nil || c == nil {
		t.Errorf("the holder must still hold its claim after its last entry: %+v (err %v)", c, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPush: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return after its context was cancelled")
	}
}

// TestRunPushCancelClearsUnackedAdmit: a holder cancelled with an entry written
// but unacked clears that entry's admit, so it is claimable again.
func TestRunPushCancelClearsUnackedAdmit(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{}
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, lineWriter) }()

	readPushLine(t, bufio.NewReader(lineOut))
	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Fatalf("claimable while the holder waits for its ack = %d, want 0", n)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPush: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return after its context was cancelled")
	}
	if e := ackEntry(t, rt, "webshop", 0); e.Confirmed {
		t.Error("a cancelled holder must not confirm the entry it wrote")
	}
	if n := claimableCount(t, rt, "webshop"); n != 1 {
		t.Errorf("claimable after the holder exited = %d, want the entry back", n)
	}
}

// guardedClaimStore is a ClaimStore a test may write to while a holder runs:
// fakeClaimStore's map is unsynchronized, and taking a claim over from under a
// live holder is exactly a cross-goroutine map write.
type guardedClaimStore struct {
	mu sync.Mutex
	m  map[string]*Claim
}

func (g *guardedClaimStore) Live(mastermind string, now time.Time) (*Claim, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.m[mastermind], nil
}

func (g *guardedClaimStore) Write(c Claim, now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]*Claim{}
	}
	g.m[c.MasterMind] = &c
	return nil
}

func (g *guardedClaimStore) Remove(mastermind string, pid int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.m, mastermind)
	return nil
}

// takeOver replaces the stored claim with one owned by another pid, which is
// how a successor holder takes the claim over.
func (g *guardedClaimStore) takeOver(c Claim) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]*Claim{}
	}
	g.m[c.MasterMind] = &c
}

// TestRunPushEndsWhenItLosesTheClaim pins the guarded exit clear: another holder
// takes the claim, so this one ends cleanly -- but it must NOT clear the admit
// it wrote, because the successor may already be delivering that same entry.
func TestRunPushEndsWhenItLosesTheClaim(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	claims := &guardedClaimStore{}
	rt.Channels = claims
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, lineWriter) }()

	readPushLine(t, bufio.NewReader(lineOut))
	pushWaitFor(t, 2*time.Second, func() bool {
		c, err := claims.Live(testClaimMasterMind, rt.Now())
		return err == nil && c != nil && c.PID == os.Getpid()
	}, "the holder to take the claim")

	// A successor takes the claim over; this holder is no longer the owner.
	claims.takeOver(Claim{MasterMind: testClaimMasterMind, PID: os.Getpid() + 1, SeenAt: rt.Now()})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPush on claim loss = %v, want a clean nil return", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPush did not return after losing the claim")
	}

	if e := ackEntry(t, rt, "webshop", 0); e.AdmittedAt == nil {
		t.Error("a holder that lost its claim must leave the entry admitted for the successor's clear")
	}
	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Errorf("claimable after claim loss = %d, want 0: the successor owns the entry", n)
	}
}

// TestRunPushReSendsEntryWhoseAdmitWasCleared pins the second exit awaitConfirm
// has: an admit cleared by someone else makes the entry claimable again, so the
// holder re-sends it instead of waiting on an ack that would now be refused.
func TestRunPushReSendsEntryWhoseAdmitWasCleared(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{}
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, lineWriter) }()

	events := pushEventChan(bufio.NewReader(lineOut))
	first := waitPushEvent(t, events, 2*time.Second)

	// Someone clears the admit under the waiting holder: the entry is claimable
	// again, and an ack for it would be refused as not admitted.
	if err := rt.Store.ClearAdmitIndex("webshop", 0); err != nil {
		t.Fatalf("ClearAdmitIndex: %v", err)
	}

	second := waitPushEvent(t, events, 3*time.Second)
	if second.Seq != first.Seq || second.Binding != first.Binding {
		t.Fatalf("re-sent line = %+v, want the same entry as %+v", second, first)
	}
	if _, err := AckPush(rt, testClaimMasterMind, "webshop", second.Seq); err != nil {
		t.Fatalf("AckPush of the re-sent entry: %v", err)
	}
	pushWaitFor(t, 2*time.Second, func() bool {
		entries, err := rt.Store.ReadLog("webshop")
		return err == nil && len(entries) == 1 && entries[0].Confirmed && entries[0].Route == "push"
	}, "the re-sent entry confirmed with route push")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPush: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return after its context was cancelled")
	}
}

// TestRunPushStateLineWaitsForEntryAck keeps the state lines in the single
// writer's between-entry slot: while an entry waits for its ack no state line
// is written, and the transition is announced only once the entry is
// confirmed.
func TestRunPushStateLineWaitsForEntryAck(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{}
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	setBindingState(t, rt, "webshop", store.StateNeedsYou)

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPush(ctx, rt, testClaimMasterMind, lineWriter) }()

	events := pushEventChan(bufio.NewReader(lineOut))
	ev := waitPushEvent(t, events, 2*time.Second)
	if ev.Kind != string(store.KindReport) {
		t.Fatalf("first line = %+v, want the report entry", ev)
	}

	// The entry is unacked: no state line may slip in while awaitConfirm waits.
	select {
	case got := <-events:
		t.Fatalf("a state line arrived while an entry awaited its ack: %+v", got)
	case <-time.After(pushPollEvery + 200*time.Millisecond):
	}

	if _, err := AckPush(rt, testClaimMasterMind, "webshop", ev.Seq); err != nil {
		t.Fatalf("AckPush: %v", err)
	}
	state := waitPushEvent(t, events, 2*time.Second)
	if state.Kind != "state" || state.State != "needs_you" {
		t.Fatalf("after the ack got %+v, want the needs_you state line", state)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return after its context was cancelled")
	}
}
