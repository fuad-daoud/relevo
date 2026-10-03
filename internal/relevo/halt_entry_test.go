package relevo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// These tests pin the halt entry: a halt queues one to_planner entry per round,
// so a MasterMind with no push route hears about it at all.

// haltRuntime is routeRuntime's bare runtime with a binding already one round
// in: round 1 has a prompt and no report, so the round reads as open and a halt
// on it is a halt a human must resolve rather than a round that closed.
//
// Deliberately not seedPending: that helper leaves a REPORT entry pending for
// round 1, which closes the round and is a different fixture (case (e) builds
// that one on purpose).
func haltRuntime(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt := routeRuntime(t)
	b := store.Binding{
		Name:         "webshop",
		CWD:          "/repo/webshop",
		Round:        1,
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: "claude", SessionID: "sess"},
		MasterMindID: testClaimMasterMind,
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return tx.AppendLog(b.Name, store.LogEntry{
			Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
		})
	}); err != nil {
		t.Fatalf("seed the open round: %v", err)
	}
	return rt, b
}

// haltIn halts b and saves it, through the same haltAndSettle the reconcile
// paths call. rt's Deliverers decide whether the entry can leave by push; a
// bare runtime has none, so the entry stays pending for wait (case (a)).
func haltIn(t *testing.T, rt Runtime, b store.Binding, text string) store.Binding {
	t.Helper()
	var next store.Binding
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, err = haltAndSettle(context.Background(), rt, tx, b, text)
		if err != nil {
			return err
		}
		return tx.Save(next)
	}); err != nil {
		t.Fatalf("haltAndSettle(%q): %v", text, err)
	}
	return next
}

// haltEntries returns the log's KindHalt entries for name, in log order.
func haltEntries(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindHalt {
			out = append(out, e)
		}
	}
	return out
}

// TestHaltQueuesOneEntryPerRound is step 1's known-good case and the plan's
// (c) and (d): a halt leaves exactly one queued unconfirmed entry, a second
// halt of the same round adds none, and the next round that halts gets its own.
//
// The dedup is haltBinding's existing HaltNotifiedRound key, which is the same
// key the log line uses; that is the whole of the dedup, so these assertions
// are what make the two one fact rather than two that happen to agree.
func TestHaltQueuesOneEntryPerRound(t *testing.T) {
	t.Parallel()

	rt, b := haltRuntime(t)

	halted := haltIn(t, rt, b, "webshop: builder exited (code 1) without a report")
	got := haltEntries(t, rt, b.Name)
	if len(got) != 1 {
		t.Fatalf("halt entries = %d, want 1: %+v", len(got), got)
	}

	e := got[0]
	if e.Round != 1 {
		t.Errorf("entry.Round = %d, want the halted round 1", e.Round)
	}
	if e.Direction != store.DirToMasterMind {
		t.Errorf("entry.Direction = %q, want %q", e.Direction, store.DirToMasterMind)
	}
	if e.Confirmed {
		t.Error("entry.Confirmed is true, want false: an unqueued-halt round owes the MasterMind an undelivered entry")
	}
	if e.Path != "" {
		t.Errorf("entry.Path = %q, want empty: a halt wrote no artifact", e.Path)
	}
	// The payload is the halt reason plus the verb that resolves it, and the
	// origin line delivery.Queue prepends is the first line of what wait prints.
	if !strings.Contains(e.Payload, "builder exited (code 1) without a report") {
		t.Errorf("entry.Payload = %q, want it to carry the halt reason", e.Payload)
	}
	if !strings.Contains(e.Payload, "relevo status --name webshop") {
		t.Errorf("entry.Payload = %q, want it to carry the status pointer", e.Payload)
	}
	if e.Note != "builder exited (code 1) without a report" {
		t.Errorf("entry.Note = %q, want the name-stripped halt text", e.Note)
	}
	if !strings.HasPrefix(e.Payload, delivery.OriginLine("webshop", 1, store.DirToMasterMind, store.KindHalt)) {
		t.Errorf("entry.Payload = %q, want it to start with the origin line", e.Payload)
	}

	// (c) A second halt of the same round: the reason changes, the entry does
	// not follow it.
	halted = haltIn(t, rt, halted, "webshop: a different reason for the same round")
	if got := haltEntries(t, rt, b.Name); len(got) != 1 {
		t.Errorf("halt entries after a second halt of round 1 = %d, want 1", len(got))
	}

	// (d) The next round gets its own. Advancing is what a close does: it
	// clears HaltNotifiedRound along with Halt, and both must clear for the
	// next failure to be reported at all.
	halted.Round = 2
	halted.HaltNotifiedRound = 0
	halted.Halt = ""
	halted.HaltAt = time.Time{}
	haltIn(t, rt, halted, "webshop: round 2 builder exited")

	got = haltEntries(t, rt, b.Name)
	if len(got) != 2 {
		t.Fatalf("halt entries after round 2 halted = %d, want 2: %+v", len(got), got)
	}
	if got[1].Round != 2 {
		t.Errorf("second entry.Round = %d, want 2", got[1].Round)
	}
}

// TestHaltEntryIsNotAReport pins (f) from both sides: the entry must not close
// the round, and view.WaitingOn must still classify the binding the way it did
// before any of this existed.
//
// The kind is the lever for both. KindReport would make store.RoundOpen and
// WaitOutcome call the round closed; KindQuestion would make WaitingOn report
// cause "blocked" and replace the halt line with the question's own text.
func TestHaltEntryIsNotAReport(t *testing.T) {
	t.Parallel()

	rt, b := haltRuntime(t)
	halted := haltIn(t, rt, b, "webshop: builder exited (code 1) without a report")

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}

	if !store.RoundOpen(entries, 1) {
		t.Error("RoundOpen(round 1) = false, want true: a halt entry is not a report entry")
	}
	if HasEntry(entries, 1, store.DirToMasterMind, store.KindReport) {
		t.Error("the log holds a report entry for round 1, want none: a halt closes nothing")
	}

	w, ok := view.WaitingOn(halted, entries, questionFirstLine(rt))
	if !ok {
		t.Fatal("WaitingOn ok = false, want true for a halted binding")
	}
	if w.Cause != "halted" {
		t.Errorf("WaitingOn.Cause = %q, want %q", w.Cause, "halted")
	}
	if !strings.Contains(w.Line, "builder exited (code 1)") {
		t.Errorf("WaitingOn.Line = %q, want the halt text", w.Line)
	}

	res := WaitOutcome(halted, entries, 1, questionFirstLine(rt))
	if res.Code != WaitNeedsYou {
		t.Errorf("WaitOutcome.Code = %d, want WaitNeedsYou (%d)", res.Code, WaitNeedsYou)
	}
	if res.Done != true {
		t.Error("WaitOutcome.Done = false, want true: needs-you is a finished wait")
	}
}

// TestBrokenHaltsQueueOnlyWhenNotSwitchable pins the broken half of the rule:
// queueBrokenHalt owes the same entry a halt does, but NOT for a switchable
// broken binding, because view.WaitingOn answers ok=false for those -- the
// daemon is about to fix them itself, so notifying would report a fault that
// resolves itself. The two must use the same test or the two halves disagree.
func TestBrokenHaltsQueueOnlyWhenNotSwitchable(t *testing.T) {
	t.Parallel()

	t.Run("non-switchable broken queues", func(t *testing.T) {
		rt, b := haltRuntime(t)
		// No candidate and no started round: nothing about this binding can
		// bring a builder back on its own.
		b.BuilderCandidate = ""
		b.RoundStartedAt = time.Time{}

		var next store.Binding
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			var err error
			next, err = queueBrokenHalt(context.Background(), rt, tx, b, "builder claude; no candidate could be resolved")
			if err != nil {
				return err
			}
			return tx.Save(next)
		}); err != nil {
			t.Fatalf("queueBrokenHalt: %v", err)
		}

		got := haltEntries(t, rt, b.Name)
		if len(got) != 1 {
			t.Fatalf("halt entries = %d, want 1: %+v", len(got), got)
		}
		if !strings.Contains(got[0].Payload, "no candidate could be resolved") {
			t.Errorf("entry.Payload = %q, want it to carry the broken reason", got[0].Payload)
		}
		if next.HaltNotifiedRound != next.Round {
			t.Errorf("HaltNotifiedRound = %d, want %d: the dedup key must be stamped", next.HaltNotifiedRound, next.Round)
		}

		// The same key gates a repeat, exactly as haltBinding's does.
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			_, err := queueBrokenHalt(context.Background(), rt, tx, next, "the same broken round again")
			return err
		}); err != nil {
			t.Fatalf("second queueBrokenHalt: %v", err)
		}
		if got := haltEntries(t, rt, b.Name); len(got) != 1 {
			t.Errorf("halt entries after a repeat = %d, want 1", len(got))
		}
	})

	t.Run("switchable broken does not queue", func(t *testing.T) {
		rt, b := haltRuntime(t)
		b.BuilderCandidate = "claude"
		b.RoundStartedAt = baseTime

		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			_, err := queueBrokenHalt(context.Background(), rt, tx, b, "builder claude; switching to codex failed")
			return err
		}); err != nil {
			t.Fatalf("queueBrokenHalt: %v", err)
		}
		if got := haltEntries(t, rt, b.Name); len(got) != 0 {
			t.Errorf("halt entries = %d, want 0 for a switchable broken binding: %+v", len(got), got)
		}
	})
}

// TestWaitPullsTheHaltTextOnce is case (a), the point of the whole change: a
// halt with no push route used to be silent, and now the first wait carries
// the text. The second wait must NOT, or a mastermind polling in a loop would
// re-read the same halt forever -- and the binding still reads as needs-you
// either way, because the halt is not cleared by being delivered.
func TestWaitPullsTheHaltTextOnce(t *testing.T) {
	t.Parallel()

	rt, b := haltRuntime(t)
	haltIn(t, rt, b, "webshop: builder exited (code 1) without a report")

	first, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{b.Name}, Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait #1: %v", err)
	}
	if first != b.Name || res.Code != WaitNeedsYou || !res.Done {
		t.Fatalf("Wait #1 = (%q, %+v), want needs-you for %s", first, res, b.Name)
	}
	if !strings.Contains(res.Payload, "builder exited (code 1) without a report") {
		t.Errorf("Wait #1 Payload = %q, want the queued halt text", res.Payload)
	}
	if !strings.Contains(res.Payload, "relevo status --name webshop") {
		t.Errorf("Wait #1 Payload = %q, want the halt's pointer too", res.Payload)
	}
	if res.DeliverErr != nil {
		t.Errorf("Wait #1 DeliverErr = %v, want nil", res.DeliverErr)
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed || last.Route != "wait" {
		t.Errorf("halt entry = confirmed:%v route:%q, want confirmed with route=wait", last.Confirmed, last.Route)
	}

	// The second wait has nothing left to pull, so it falls back to the
	// needs-you outcome line alone.
	_, res, err = Wait(context.Background(), rt, WaitOptions{
		Names: []string{b.Name}, Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait #2: %v", err)
	}
	if res.Code != WaitNeedsYou {
		t.Errorf("Wait #2 Code = %d, want WaitNeedsYou: delivering the halt does not resolve it", res.Code)
	}
	if res.Payload != "" {
		t.Errorf("Wait #2 Payload = %q, want empty: the halt text is pulled once", res.Payload)
	}
	if !strings.Contains(res.Line, "builder exited (code 1)") {
		t.Errorf("Wait #2 Line = %q, want the needs-you outcome line", res.Line)
	}
}

// TestHaltEntryReachesADelivererNotWait is case (b): a MasterMind with a push
// route is told through that route, exactly once. The entry is confirmed with
// the deliverer route, and because it is confirmed the later wait has nothing
// to pull -- a halt must not arrive twice just because two routes exist.
func TestHaltEntryReachesADelivererNotWait(t *testing.T) {
	t.Parallel()

	rt, b := haltRuntime(t)
	stub := &haltDeliverer{}
	rt.Deliverers = map[string]delivery.MasterMindDeliverer{b.MasterMind.Kind: stub}

	haltIn(t, rt, b, "webshop: builder exited (code 1) without a report")

	if stub.deliverCalls != 1 {
		t.Errorf("deliverer.Deliver calls = %d, want 1", stub.deliverCalls)
	}
	if stub.text == "" || !strings.Contains(stub.text, "builder exited (code 1) without a report") {
		t.Errorf("deliverer text = %q, want the halt reason", stub.text)
	}
	if stub.confirmCalls != 1 {
		t.Errorf("deliverer.Confirm calls = %d, want 1: the push that took the payload confirms it", stub.confirmCalls)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed {
		t.Fatal("halt entry is unconfirmed, want confirmed by the deliverer route")
	}
	if want := "deliverer:" + b.MasterMind.Kind; last.Route != want {
		t.Errorf("halt entry Route = %q, want %q", last.Route, want)
	}

	// The wait must not re-deliver what the push route already took.
	_, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{b.Name}, Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Payload != "" {
		t.Errorf("Wait Payload = %q, want empty: the deliverer already took it", res.Payload)
	}
	if res.Code != WaitNeedsYou {
		t.Errorf("Wait Code = %d, want WaitNeedsYou", res.Code)
	}
}

// TestHaltTextArrivesAfterAPendingReport is case (e): the halt entry is
// appended, so it is the newer of the two, and the report must not be jumped.
// PullPendingThrough reads oldest first and Pull picks the oldest claimable
// entry, so this is a property of append order -- but it is the property that
// would break first if the halt were written anywhere other than at the halt.
func TestHaltTextArrivesAfterAPendingReport(t *testing.T) {
	t.Parallel()

	rt, b := haltRuntime(t)
	// A report for round 1 is pending ahead of the halt, which is the shape a
	// close that then trips the artifact cap leaves behind.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return delivery.Queue(context.Background(), deliveryDeps(rt), tx, b.Name, store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Payload: "round 1 report body",
		})
	}); err != nil {
		t.Fatalf("queue report: %v", err)
	}

	haltIn(t, rt, b, "webshop: artifact directory over the cap")

	_, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{b.Name}, Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	reportAt := strings.Index(res.Payload, "round 1 report body")
	haltAt := strings.Index(res.Payload, "artifact directory over the cap")
	if reportAt < 0 || haltAt < 0 {
		t.Fatalf("Wait Payload = %q, want both the report and the halt text", res.Payload)
	}
	if reportAt > haltAt {
		t.Errorf("Wait Payload puts the halt at %d and the report at %d, want the report first", haltAt, reportAt)
	}
}

// TestHaltEntryDoesNotDisturbTheAdmittedReadBack is (g): the done/paused
// ConfirmAdmitted path is untouched by any of this. A halt entry on a done
// binding is settled by exactly the read-back that settles any admitted entry
// on a done binding, and is never pushed.
func TestHaltEntryDoesNotDisturbTheAdmittedReadBack(t *testing.T) {
	t.Parallel()

	rt, b := haltRuntime(t)
	stub := &doneReadBackDeliverer{delivered: true}
	rt.Deliverers = map[string]delivery.MasterMindDeliverer{b.MasterMind.Kind: stub}

	// The binding goes done, and the halt entry is admitted by a route before
	// the state changed: the one state ConfirmAdmitted exists for.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		halted, err := haltBinding(context.Background(), rt, tx, b, "webshop: builder exited (code 1)")
		if err != nil {
			return err
		}
		_, idx, found, err := tx.PendingForMasterMind(b.Name)
		if err != nil {
			return err
		}
		if !found {
			t.Fatal("the halt queued no entry to admit")
		}
		if err := tx.AdmitIndex(b.Name, idx); err != nil {
			return err
		}
		halted.State = store.StateDone
		return tx.Save(halted)
	}); err != nil {
		t.Fatalf("seed the admitted halt: %v", err)
	}

	done, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got store.Binding
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, _, err = delivery.ConfirmAdmitted(context.Background(), deliveryDeps(rt), tx, done)
		return err
	}); err != nil {
		t.Fatalf("ConfirmAdmitted: %v", err)
	}

	if stub.deliverCalls != 0 {
		t.Errorf("deliverer.Deliver calls = %d, want 0: a done binding is only read back", stub.deliverCalls)
	}
	if stub.onceCalls != 1 {
		t.Errorf("deliverer.ConfirmOnce calls = %d, want 1", stub.onceCalls)
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed || last.Route != "deliverer:"+b.MasterMind.Kind {
		t.Errorf("halt entry = confirmed:%v route:%q, want confirmed with the deliverer route", last.Confirmed, last.Route)
	}
	if got.Name != b.Name {
		t.Errorf("ConfirmAdmitted returned %q, want the binding back", got.Name)
	}
}

// haltDeliverer is the push-route double for case (b): it admits and confirms
// in one call, which is the shape a deliverer that both posts and reads the
// session back has, and it records the text it was handed so the test can
// prove the halt reason is what was pushed.
type haltDeliverer struct {
	deliverCalls int
	confirmCalls int
	text         string
}

func (d *haltDeliverer) Deliver(_ context.Context, _ store.Endpoint, text, _ string, _ time.Time) (delivery.Outcome, string, error) {
	d.deliverCalls++
	d.text = text
	return delivery.OutcomeAdmitted, "posted; awaiting the session", nil
}

func (d *haltDeliverer) Confirm(_ context.Context, _ store.Endpoint, _ string, _ time.Time) (delivery.Outcome, string, error) {
	d.confirmCalls++
	return delivery.OutcomeDelivered, "", nil
}

func (d *haltDeliverer) ConfirmOnce(_ context.Context, _ store.Endpoint, _ string, _ time.Time) (delivery.Outcome, string, error) {
	return delivery.OutcomeDelivered, "", nil
}
