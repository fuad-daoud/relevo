package relevo

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// markedWriterRound seeds round 1 with a report and a marker stamped at
// mtime, so the grace comparison is the one under test and not a clock
// accident between the wall clock and the runtime's fixed clock.
func markedWriterRound(t *testing.T, mtime time.Time) (Runtime, store.Binding, *fakeRunner) {
	t.Helper()
	rt, b := sentBinding(t)
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runner = %T, want *fakeRunner", rt.Runner)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	marker := rt.Store.DonePath("webshop", 1)
	if err := os.Chtimes(marker, mtime, mtime); err != nil {
		t.Fatalf("chtimes marker: %v", err)
	}
	return rt, b, fr
}

// roundReport returns the round's KindReport entry, or fails the test when
// the close queued none.
func roundReport(t *testing.T, rt Runtime, round int) store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Round == round && e.Kind == store.KindReport {
			return e
		}
	}
	t.Fatalf("no report entry for round %d", round)
	return store.LogEntry{}
}

// reportCount counts the round's KindReport entries.
func reportCount(t *testing.T, rt Runtime, round int) int {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, e := range entries {
		if e.Round == round && e.Kind == store.KindReport {
			n++
		}
	}
	return n
}

// TestReconcileHoldsMarkerCloseForLiveRunnerThenRecordsRusage pins the grace
// close: a marker with a live runner holds the round open with no report,
// and the close after the exit files the trailer's measurement on the
// report entry and records the served close once. Mutation: never hold (the
// helper returns false) -> tick 1 closes with no rusage.
func TestReconcileHoldsMarkerCloseForLiveRunnerThenRecordsRusage(t *testing.T) {
	t.Parallel()

	rt, b, fr := markedWriterRound(t, baseTime)
	fr.script(b.Builder.PID, true)
	called := false
	rt.CloseServedRound = recordCloseServedRound(&called, "")
	b.Owner = "client1"

	tick1, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile tick 1: %v", err)
	}
	if tick1.Round != 1 {
		t.Errorf("tick 1 round = %d, want 1: a live runner holds the marked close", tick1.Round)
	}
	if n := reportCount(t, rt, 1); n != 0 {
		t.Errorf("tick 1 queued %d report entries, want none while held", n)
	}
	if called {
		t.Error("served hook called before the close")
	}

	fr.script(b.Builder.PID, false)
	fr.setRusage(b.Builder.PID, spawn.ProcRusage{CPUMS: 12300, PeakMemBytes: 850 << 20,
		BaseBytes: ptr(int64(100)), BurstBytes: ptr(int64(200)), BurstSecs: ptr(int64(15))})
	tick2, err := reconcile(t, rt, tick1)
	if err != nil {
		t.Fatalf("Reconcile tick 2: %v", err)
	}
	if tick2.Round != 2 {
		t.Fatalf("tick 2 round = %d, want 2: the exit closes the round", tick2.Round)
	}
	entry := roundReport(t, rt, 1)
	if entry.Rusage == nil {
		t.Fatal("report entry Rusage is nil, want the trailer's measurement")
	}
	if entry.Rusage.CPUMS != 12300 || entry.Rusage.PeakMemBytes != 850<<20 {
		t.Errorf("report entry Rusage = %+v, want {12300 %d}", entry.Rusage, int64(850<<20))
	}
	if entry.Rusage.BaseBytes == nil || *entry.Rusage.BaseBytes != 100 {
		t.Errorf("report entry BaseBytes = %+v, want 100", entry.Rusage.BaseBytes)
	}
	if entry.Rusage.BurstBytes == nil || *entry.Rusage.BurstBytes != 200 {
		t.Errorf("report entry BurstBytes = %+v, want 200", entry.Rusage.BurstBytes)
	}
	if entry.Rusage.BurstSecs == nil || *entry.Rusage.BurstSecs != 15 {
		t.Errorf("report entry BurstSecs = %+v, want 15", entry.Rusage.BurstSecs)
	}
	if !called {
		t.Error("served hook not called on the close")
	}
}

// TestReconcileClosesMarkedRoundAfterRusageGraceWithoutRusage pins the
// bound: a runner still alive past the grace closes without a measurement,
// and a later tick records no second close. Mutation: hold past the grace
// -> the round never advances.
func TestReconcileClosesMarkedRoundAfterRusageGraceWithoutRusage(t *testing.T) {
	t.Parallel()

	rt, b, fr := markedWriterRound(t, baseTime.Add(-31*time.Second))
	fr.script(b.Builder.PID, true)
	calls := 0
	called := false
	inner := recordCloseServedRound(&called, "")
	rt.CloseServedRound = func(ctx context.Context, rt Runtime, b store.Binding) store.Binding {
		calls++
		return inner(ctx, rt, b)
	}
	b.Owner = "client1"

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2: the grace expired", got.Round)
	}
	if entry := roundReport(t, rt, 1); entry.Rusage != nil {
		t.Errorf("report entry Rusage = %+v, want nil with no trailer", entry.Rusage)
	}
	if calls != 1 {
		t.Errorf("served hook calls = %d, want 1", calls)
	}

	rest, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile second tick: %v", err)
	}
	if rest.Round != 2 {
		t.Errorf("second tick round = %d, want 2", rest.Round)
	}
	if calls != 1 {
		t.Errorf("served hook calls = %d after a second tick, want 1: no second record", calls)
	}
	if n := reportCount(t, rt, 1); n != 1 {
		t.Errorf("round 1 report entries = %d, want 1: no second report", n)
	}
}

// TestReconcileClosesMarkedRoundAtOnceWhenRunnerExited pins the fast path:
// a marker with an already-exited runner closes on the first tick with the
// trailer's measurement, exactly as an ungated close does.
func TestReconcileClosesMarkedRoundAtOnceWhenRunnerExited(t *testing.T) {
	t.Parallel()

	rt, b, fr := markedWriterRound(t, baseTime)
	fr.script(b.Builder.PID, false)
	fr.setRusage(b.Builder.PID, spawn.ProcRusage{CPUMS: 4200, BaseBytes: ptr(int64(7))})

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2: an exited runner closes at once", got.Round)
	}
	entry := roundReport(t, rt, 1)
	if entry.Rusage == nil || entry.Rusage.CPUMS != 4200 {
		t.Errorf("report entry Rusage = %+v, want CPUMS 4200", entry.Rusage)
	}
	if entry.Rusage != nil && (entry.Rusage.BaseBytes == nil || *entry.Rusage.BaseBytes != 7) {
		t.Errorf("report entry BaseBytes = %+v, want 7", entry.Rusage.BaseBytes)
	}
}

// TestReconcileHoldsMarkerCloseOnLivenessError pins that an unreadable
// liveness check holds the round: the error is not evidence the builder
// stopped.
func TestReconcileHoldsMarkerCloseOnLivenessError(t *testing.T) {
	t.Parallel()

	rt, b, fr := markedWriterRound(t, baseTime)
	fr.script(b.Builder.PID, true)
	fr.aliveErr = errors.New("ps refused")

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("round = %d, want 1: a failed liveness check holds the close", got.Round)
	}
	if n := reportCount(t, rt, 1); n != 0 {
		t.Errorf("queued %d report entries, want none while held", n)
	}
}

// TestReconcileClosesMarkedRoundWhenGateFinishesDespiteLiveRunner pins the
// running-gate bypass: a gate already running owns the close, so the rusage
// wait must not sit in front of it even with the builder still alive.
func TestReconcileClosesMarkedRoundWhenGateFinishesDespiteLiveRunner(t *testing.T) {
	t.Parallel()

	rt, b, fr := markedWriterRound(t, baseTime)
	fr.script(b.Builder.PID, true)
	b.Gate = "make check"
	b.GateRun = &store.GateRun{PID: 9001, StartedAt: baseTime.Unix(), Round: b.Round, Command: "make check"}
	fr.script(9001, false)
	fr.exit(9001, 0)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2: the finished gate closes the round", got.Round)
	}
	if got.GateRun != nil {
		t.Errorf("GateRun = %+v, want nil after the round closes", got.GateRun)
	}
	if n := reportCount(t, rt, 1); n != 1 {
		t.Fatalf("round 1 report entries = %d, want 1", n)
	}
	if entry := roundReport(t, rt, 1); entry.Gate == nil || entry.Gate.Result != "pass" {
		t.Errorf("report entry Gate = %+v, want Result pass", entry.Gate)
	}
}
