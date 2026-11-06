package sync

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// A locked join owes this machine's own history. The steady attempts send it once
// the machine holds a key, so an unlock never has to wait for a send of any length.

// owedMachine is a locked join finished and then unlocked, holding count bindings
// of its own beyond the one lockedJoin seeds.
func owedMachine(t *testing.T, count int) (*db.DB, Local, *synclog.MemTransport) {
	t.Helper()
	shared, local, log := lockedJoin(t)
	statements := make([]string, count)
	for i := range statements {
		statements[i] = joinBinding(fmt.Sprintf("extra-%04d", i), "m1")
	}
	seedJoin(t, shared, statements...)
	// History older than the last time sync was on is in no outbox, so only the
	// walk can send it.
	if err := shared.TruncateOutbox(); err != nil {
		t.Fatal(err)
	}
	if _, err := runEnable(t, shared, local, []byte(joinToken), log); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := SetKey(local, syncTestKey, time.Unix(0, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	return shared, local, log
}

// drainOwed runs attempts until the marker clears, failing if the send never ends.
func drainOwed(t *testing.T, runner *Runner, shared *db.DB, local Local) {
	t.Helper()
	for attempts := 1; attempts < 100; attempts++ {
		if out := runner.SyncOnce(context.Background(), shared); out.Err != nil {
			t.Fatalf("attempt %d: %v", attempts, out.Err)
		}
		if _, joining, _ := ReadJoin(local); !joining {
			return
		}
	}
	t.Fatal("the owed history never finished")
}

// sentBindings counts how many times each binding of m1's reached the log, by
// primary key, leaving out the signed entry the log started with and its removal.
func sentBindings(t *testing.T, log *synclog.MemTransport) map[string]int {
	t.Helper()
	pulled, err := log.OnLog("m2").Pull(nil)
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string]int{}
	for _, e := range pulled {
		if e.Origin != "m1" || e.Seq == 1 || e.Table != "binding" {
			continue
		}
		if !synclog.VerifyMAC(syncTestKey, e) {
			t.Errorf("entry %d of the owed history is not signed with the key", e.Seq)
		}
		if e.Op != synclog.OpDelete {
			sent[e.PK]++
		}
	}
	return sent
}

func TestAnAttemptSendsTheOwedHistorySignedAndClearsTheMarker(t *testing.T) {
	t.Parallel()
	shared, local, log := owedMachine(t, 0)
	runner := &Runner{Client: log, Local: local}

	drainOwed(t, runner, shared, local)

	if sent := sentBindings(t, log); sent[`["b1"]`] != 1 {
		t.Errorf("binding b1 reached the log %d times, want once: %v", sent[`["b1"]`], sent)
	}
}

// markCountingLog counts mark publishes and can refuse them.
type markCountingLog struct {
	*synclog.MemTransport
	published int
	refuse    bool
}

func (l *markCountingLog) PublishMarks(marks map[string]int, at time.Time) error {
	if l.refuse {
		return errLogRefused
	}
	l.published++
	return l.MemTransport.PublishMarks(marks, at)
}

// The end of a join says where the machine has read to, whatever the steady
// throttle would have done; the debt is only forgiven once that went out.
func TestFinishingTheOwedHistoryPublishesTheMarksBeforeClearingTheDebt(t *testing.T) {
	t.Parallel()
	shared, local, log := owedMachine(t, 0)
	counting := &markCountingLog{MemTransport: log, refuse: true}
	runner := &Runner{Client: counting, Local: local}

	if out := runner.SyncOnce(context.Background(), shared); !errors.Is(out.Err, errLogRefused) {
		t.Fatalf("attempt with marks refused = %v, want the refusal", out.Err)
	}
	if _, joining, _ := ReadJoin(local); !joining {
		t.Error("the debt was cleared though the marks never went out")
	}

	counting.refuse = false
	if out := runner.SyncOnce(context.Background(), shared); out.Err != nil {
		t.Fatalf("attempt: %v", out.Err)
	}
	if counting.published == 0 {
		t.Error("finishing the owed history published no marks")
	}
	if _, joining, _ := ReadJoin(local); joining {
		t.Error("the marker is still set")
	}
}

func TestALockedMachineSendsNothingAndStaysOwing(t *testing.T) {
	t.Parallel()
	shared, local, log := lockedJoin(t)
	if _, err := runEnable(t, shared, local, []byte(joinToken), log); err != nil {
		t.Fatalf("enable: %v", err)
	}
	runner := &Runner{Client: log, Local: local}

	out := runner.SyncOnce(context.Background(), shared)

	if out.Err != nil {
		t.Fatalf("attempt: %v", out.Err)
	}
	if len(out.Trouble.Held) == 0 {
		t.Error("the attempt does not say the export is held for the lock")
	}
	if _, joining, _ := ReadJoin(local); !joining {
		t.Error("the marker was cleared with the history still owed")
	}
	if sent := sentBindings(t, log); len(sent) != 0 {
		t.Errorf("a locked machine sent %v", sent)
	}
}

// One slice is one batch here, so the send has to span attempts. The outbox waits
// for all of it, and every row still arrives exactly once, because the walk keeps
// its place between attempts.
func TestTheOwedHistorySpansAttemptsAndHoldsTheOutboxBack(t *testing.T) {
	t.Parallel()
	const extra = 300
	shared, local, log := owedMachine(t, extra)
	runner := &Runner{Client: log, Local: local, OwedSlice: time.Nanosecond}
	seedJoin(t, shared, joinBinding("written-meanwhile", "m1"))

	first := runner.SyncOnce(context.Background(), shared)
	if first.Err != nil {
		t.Fatalf("first attempt: %v", first.Err)
	}
	progress, joining, err := ReadJoin(local)
	if err != nil || !joining {
		t.Fatalf("after one slice the marker reads (%v, %v), want it still set", joining, err)
	}
	if progress.Sent == 0 {
		t.Error("the marker counts nothing sent after one slice")
	}
	if progress.At.IsZero() {
		t.Error("recording progress lost when the join began")
	}
	if first.Exported != 0 {
		t.Errorf("the attempt exported %d outbox entries while history was owed", first.Exported)
	}
	if has, _ := shared.HasOutboxEntries(); !has {
		t.Error("the outbox was emptied while history was owed")
	}
	if len(first.Trouble.Held) != 0 {
		t.Errorf("an unlocked machine still reports held: %v", first.Trouble.Held)
	}

	drainOwed(t, runner, shared, local)
	sent := sentBindings(t, log)
	if len(sent) != extra+2 {
		t.Errorf("%d bindings reached the log, want %d", len(sent), extra+2)
	}
	// A row written during the send is in the walk and the outbox both, and applying
	// it twice is harmless; everything else has to arrive once.
	for pk, times := range sent {
		if times != 1 && pk != `["written-meanwhile"]` {
			t.Errorf("binding %s reached the log %d times, want once", pk, times)
		}
	}
}

// Nothing but the marker survives a daemon restart, so a new runner has to pick the
// send up from it, and what already went out must not go again.
func TestARestartedDaemonFinishesTheOwedHistoryWithoutResending(t *testing.T) {
	t.Parallel()
	shared, local, log := owedMachine(t, 300)
	before := &Runner{Client: log, Local: local, OwedSlice: time.Nanosecond}
	if out := before.SyncOnce(context.Background(), shared); out.Err != nil {
		t.Fatalf("attempt before the restart: %v", out.Err)
	}
	if _, joining, _ := ReadJoin(local); !joining {
		t.Fatal("the send finished in one slice, so nothing is left to restart")
	}

	drainOwed(t, &Runner{Client: log, Local: local}, shared, local)

	sent := sentBindings(t, log)
	if len(sent) != 301 {
		t.Errorf("%d bindings reached the log, want 301", len(sent))
	}
	for pk, times := range sent {
		if times != 1 {
			t.Errorf("binding %s reached the log %d times, want once", pk, times)
		}
	}
}

// refusingLog refuses appends while refuse is set.
type refusingLog struct {
	*synclog.MemTransport
	refuse bool
}

var errLogRefused = errors.New("the log refused the append")

func (l *refusingLog) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	if l.refuse {
		return nil, errLogRefused
	}
	return l.MemTransport.Append(entries)
}

func (l *refusingLog) AppendSigned(entries []synclog.Entry, b synclog.Boundary) ([]synclog.Entry, error) {
	if l.refuse {
		return nil, errLogRefused
	}
	return l.MemTransport.AppendSigned(entries, b)
}

// A failure nobody is waiting on has to surface in the attempt, keep the debt, and
// not skip the rows the failed chunk had read.
func TestAFailedSendReportsItKeepsTheMarkerAndLosesNoRow(t *testing.T) {
	t.Parallel()
	shared, local, log := owedMachine(t, 300)
	broken := &refusingLog{MemTransport: log}
	runner := &Runner{Client: broken, Local: local, OwedSlice: time.Nanosecond}
	if out := runner.SyncOnce(context.Background(), shared); out.Err != nil {
		t.Fatalf("first attempt: %v", out.Err)
	}

	broken.refuse = true
	out := runner.SyncOnce(context.Background(), shared)
	if !errors.Is(out.Err, errLogRefused) {
		t.Fatalf("attempt against a refusing log = %v, want the refusal", out.Err)
	}
	if _, joining, _ := ReadJoin(local); !joining {
		t.Error("the marker was cleared by a failed send")
	}

	broken.refuse = false
	drainOwed(t, runner, shared, local)
	if sent := sentBindings(t, log); len(sent) != 301 {
		t.Errorf("%d bindings reached the log, want 301", len(sent))
	}
}

// A re-enable can point the machine at another log, whose head the walk in progress
// never read.
func TestAWalkDoesNotCarryOverToAnotherClient(t *testing.T) {
	t.Parallel()
	shared, local, log := owedMachine(t, 300)
	runner := &Runner{Client: log, Local: local, OwedSlice: time.Nanosecond}
	if out := runner.SyncOnce(context.Background(), shared); out.Err != nil {
		t.Fatalf("attempt: %v", out.Err)
	}

	other := synclog.NewMemTransport("m1")
	runner.Client = other
	drainOwed(t, runner, shared, local)

	pulled, err := other.OnLog("m2").Pull(nil)
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]bool{}
	for _, e := range pulled {
		if e.Table == "binding" {
			bindings[e.PK] = true
		}
	}
	if len(bindings) != 301 {
		t.Errorf("the new log holds %d bindings, want all 301", len(bindings))
	}
}

func TestAMachineOwingNothingExportsAsUsual(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))
	log := synclog.NewMemTransport("m1")
	if err := SetKey(local, syncTestKey, time.Unix(0, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := MarkEnabled(local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Client: log, Local: local}

	if out := runner.SyncOnce(context.Background(), shared); out.Err != nil {
		t.Fatalf("attempt: %v", out.Err)
	} else if out.Exported == 0 {
		t.Error("the attempt exported nothing from a machine owing no history")
	}
}

// stoppingLog stops the runner on the append it is told to, the way a disable
// arriving mid-send does.
type stoppingLog struct {
	*synclog.MemTransport
	runner  *Runner
	armed   bool
	stopped int
}

func (l *stoppingLog) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	got, err := l.MemTransport.Append(entries)
	if l.armed {
		l.stopped++
		l.runner.Stop()
	}
	return got, err
}

// A walk spans attempts, but a stop reaches only the attempt in flight, so each
// attempt has to carry the walk on its own transport or a disable could not end it.
func TestStoppingAnAttemptEndsAWalkItInherited(t *testing.T) {
	t.Parallel()
	shared, local, log := owedMachine(t, 800)
	stopping := &stoppingLog{MemTransport: log}
	runner := &Runner{Client: stopping, Local: local, OwedSlice: time.Nanosecond}
	stopping.runner = runner
	if out := runner.SyncOnce(context.Background(), shared); out.Err != nil {
		t.Fatalf("first attempt: %v", out.Err)
	}

	stopping.armed = true
	runner.OwedSlice = time.Hour
	if out := runner.SyncOnce(context.Background(), shared); out.Err == nil {
		t.Error("the stopped attempt reported no failure")
	}
	if stopping.stopped != 1 {
		t.Errorf("%d appends reached the log after the stop began, want the one in flight", stopping.stopped)
	}
	if _, joining, _ := ReadJoin(local); !joining {
		t.Error("the stopped attempt cleared the debt")
	}
}
