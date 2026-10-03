package relevo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
)

// haltEntriesFor returns every to_planner halt entry a binding's log holds.
func haltEntriesFor(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindHalt && e.Direction == store.DirToMasterMind {
			out = append(out, e)
		}
	}
	return out
}

// waitOnClosedRoundHaltText is what the default wait path reads back for the
// round it waits on: DefaultWaitRound picks N (the prompt round), and the
// through-pull PullPendingThroughEntries(N) then claims every unconfirmed
// entry of round <= N.
//
// This is the whole of the defect-1 claim in one helper: before the fix the
// halt was filed under N+1 and the through-pull never saw it, so the text a
// human is waiting on was invisible. Asserting on the through-pull rather than
// on a raw log scan is deliberate -- the log entry existing was never the
// question; whether the wait could reach it was.
func waitOnClosedRoundHaltText(t *testing.T, rt Runtime, name string, b store.Binding) string {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	round := DefaultWaitRound(b, entries)
	if round != 1 {
		t.Fatalf("DefaultWaitRound = %d, want 1 (the round the builder was sent)", round)
	}
	delivered, err := delivery.PullPendingThroughEntries(context.Background(), rt.Store, name, "wait", round)
	if err != nil {
		t.Fatalf("PullPendingThroughEntries: %v", err)
	}
	var text string
	for _, d := range delivered {
		if d.Entry.Kind == store.KindHalt {
			text += d.Text
		}
	}
	return text
}

// TestQueueReportArtifactCapHaltWaitOnClosedRound pins the stranded
// post-advance halt: a
// reader round that closes over the artifact cap halts AFTER the round has
// advanced, and the halt entry must be filed under the round that closed.
//
// Filing under N+1 stranded it: PullPendingThroughEntries answers `<= round`
// and the default wait is still sitting on N, while WaitOutcome has already
// returned Done on N's report. The halt text was written and never seen.
func TestQueueReportArtifactCapHaltWaitOnClosedRound(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)

	dir := rt.Store.ArtifactDir("reader-bind", 1, "reviewer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := bytes.Repeat([]byte("x"), 2<<20)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	oneMB := 1
	rt.Policy.ArtifactMaxMB = &oneMB

	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The round did advance: the cap halt is a post-advance halt, which is the
	// whole reason the entry's round is a question.
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2: the close advances before the cap halt runs", got.Round)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want %q", got.State, store.StateNeedsYou)
	}
	wantHalt := "artifacts over the cap: 2 > 1 MB; raise policy.artifact_max_mb to seal them"
	if got.Halt != wantHalt {
		t.Fatalf("Halt = %q, want %q", got.Halt, wantHalt)
	}

	halts := haltEntriesFor(t, rt, "reader-bind")
	if len(halts) != 1 {
		t.Fatalf("halt entries = %d, want exactly 1: %+v", len(halts), halts)
	}
	if halts[0].Round != 1 {
		t.Errorf("halt entry Round = %d, want 1 (the round whose artifacts were over the cap)", halts[0].Round)
	}

	// The dedup key stays the new round: a halt of the round just opened is a
	// new notification, and the stamp has to say so.
	if got.HaltNotifiedRound != 2 {
		t.Errorf("HaltNotifiedRound = %d, want 2 (keyed to the new round)", got.HaltNotifiedRound)
	}

	if text := waitOnClosedRoundHaltText(t, rt, "reader-bind", got); !strings.Contains(text, wantHalt) {
		t.Errorf("wait on round 1 returned %q, want it to carry the halt text", text)
	}
}

// TestQueueReportScopeRefusalHaltWaitOnClosedRound is TestQueueReportArtifactCapHaltWaitOnClosedRound
// for the second post-advance halt: a scope refusal. It reaches queueReport
// with a refused verdict already judged, because the judging itself is not what
// this pins.
func TestQueueReportScopeRefusalHaltWaitOnClosedRound(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	reportPath := rt.Store.ReportPath("webshop", 1)
	if err := os.WriteFile(reportPath, []byte("all done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	refused := scopeVerdict{Scoped: true, Refused: true, Path: "internal/other.go", Reason: "outside scope"}

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog("webshop")
		if err != nil {
			return err
		}
		next, err := queueReport(context.Background(), rt, tx, cur, entries,
			reportPath, "done", "test", nil, nil, nil, nil, "", false, refused)
		if err != nil {
			return err
		}
		got = next
		return tx.Save(next)
	})
	if err != nil {
		t.Fatalf("queueReport: %v", err)
	}

	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2: the close advances before the refusal halt runs", got.Round)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want %q", got.State, store.StateNeedsYou)
	}
	wantHalt := "round 1 changed internal/other.go outside actor"
	if !strings.Contains(got.Halt, wantHalt) {
		t.Fatalf("Halt = %q, want it to contain %q", got.Halt, wantHalt)
	}

	halts := haltEntriesFor(t, rt, "webshop")
	if len(halts) != 1 {
		t.Fatalf("halt entries = %d, want exactly 1: %+v", len(halts), halts)
	}
	if halts[0].Round != 1 {
		t.Errorf("halt entry Round = %d, want 1 (the round the refusal is about)", halts[0].Round)
	}
	if got.HaltNotifiedRound != 2 {
		t.Errorf("HaltNotifiedRound = %d, want 2 (keyed to the new round)", got.HaltNotifiedRound)
	}

	if text := waitOnClosedRoundHaltText(t, rt, "webshop", got); !strings.Contains(text, "internal/other.go") {
		t.Errorf("wait on round 1 returned %q, want it to carry the scope refusal", text)
	}
}

// TestRoundCapHaltWaitOnCurrentRound pins the round-cap halt as the case that
// needed no change: it halts before the round advances, so it already files
// under the current round and a default wait on that round reaches it.
//
// This is the contrast that makes the closedRound filing a defect fix rather
// than a preference: the round cap was already right, and the two post-advance
// halts were the ones filing into a round nobody waits on.
func TestRoundCapHaltWaitOnCurrentRound(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	b.Round = 3
	b.RoundCap = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want %q", got.State, store.StateNeedsYou)
	}
	if !strings.Contains(got.Halt, ErrRoundCap.Error()) {
		t.Fatalf("Halt = %q, want it to name the round cap", got.Halt)
	}
	// The cap halt must not advance the round: it is the current round's own
	// budget, and advancing would let a fresh round past the same cap.
	if got.Round != 3 {
		t.Errorf("Round = %d, want 3: a round-cap halt does not advance the round", got.Round)
	}

	halts := haltEntriesFor(t, rt, "webshop")
	if len(halts) != 1 {
		t.Fatalf("halt entries = %d, want exactly 1: %+v", len(halts), halts)
	}
	if halts[0].Round != 3 {
		t.Errorf("halt entry Round = %d, want 3 (the current round)", halts[0].Round)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	// The binding waits on round 1 (the only prompt sent), and the halt is
	// filed under 3. So a wait on the default round does NOT reach it -- and
	// that is correct here: the round-cap halt belongs to round 3, which has
	// no prompt and no wait. What the test pins is the round, not reachability.
	if round := DefaultWaitRound(got, entries); round != 1 {
		t.Fatalf("DefaultWaitRound = %d, want 1", round)
	}
	for _, h := range halts {
		if h.Round != got.Round {
			t.Errorf("halt entry Round = %d, want the binding's own round %d", h.Round, got.Round)
		}
	}

	// A wait aimed at the halted round does reach it, which is the reachability
	// this case actually has to keep.
	delivered, err := delivery.PullPendingThroughEntries(context.Background(), rt.Store, "webshop", "wait", got.Round)
	if err != nil {
		t.Fatalf("PullPendingThroughEntries: %v", err)
	}
	if len(delivered) == 0 {
		t.Fatal("a wait on the halted round delivered nothing")
	}
	var text string
	for _, d := range delivered {
		if d.Entry.Kind == store.KindHalt {
			text += d.Text
		}
	}
	if !strings.Contains(text, ErrRoundCap.Error()) {
		t.Errorf("wait on round 3 returned %q, want the round-cap halt text", text)
	}
}

// TestRoundCapHaltDoesNotAdvanceRoundAtAll is the boundary the closedRound
// filing must not cross: a halt before the advance keeps the round, so the
// binding stays put rather than opening a fresh round past its own cap.
func TestRoundCapHaltDoesNotAdvanceRoundAtAll(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	b.Round = 2
	b.RoundCap = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	before := rt.Now().UTC()

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2", got.Round)
	}
	if got.HaltAt.IsZero() || got.HaltAt.Before(before) {
		t.Errorf("HaltAt = %v, want it stamped at the halt", got.HaltAt)
	}
	if got.HaltAt.After(rt.Now().UTC().Add(time.Second)) {
		t.Errorf("HaltAt = %v, want it no later than the halt tick", got.HaltAt)
	}
}

// storeLogCap mirrors the store's binding-log entry cap (maxLogEntries). Only
// the store holds that number and this package cannot ask it, so it is repeated
// here; fillLogToCapLessOne fails loudly rather than silently seeding the wrong
// count if the two ever disagree.
const storeLogCap = 10000

// fillLogLeavingRoom tops a binding's log up so that exactly room further
// appends still fit under the cap. The filler is marked as filler so a test can
// tell the entries a close wrote from the ones this seeded.
//
// The log is the only fault this package can inject that a close cannot write
// past: a shared store's closed database fails every append, including the
// report one, which would prove nothing about the halt's own queue.
func fillLogLeavingRoom(t *testing.T, rt Runtime, name string, room int) {
	t.Helper()

	target := storeLogCap - room
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(name)
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog(name)
		if err != nil {
			return err
		}
		need := target - len(entries)
		if need < 0 {
			t.Fatalf("log already holds %d entries, past the %d this test needs", len(entries), target)
		}
		if need == 0 {
			return nil
		}
		filler := make([]store.LogEntry, need)
		for i := range filler {
			filler[i] = store.LogEntry{
				TS: rt.Now().UTC(), Round: 1, Direction: store.DirToMasterMind,
				Kind: store.KindReport, Payload: "filler", Note: logFillerNote, Confirmed: true,
			}
		}
		return tx.SaveWithLog(cur, filler...)
	})
	if err != nil {
		t.Fatalf("fill log to %d entries: %v", target, err)
	}

	got, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog after the fill: %v", err)
	}
	if len(got) != target {
		t.Fatalf("log holds %d entries after the fill, want %d: the store's cap is not the one this test mirrors", len(got), target)
	}
}

// logFillerNote marks the entries fillLogLeavingRoom seeded, so an assertion
// about what a close queued cannot be satisfied by the seeding itself.
const logFillerNote = "log-cap filler"

// TestQueueReportScopeRefusalHaltAppendFailureSurfaces pins that a post-advance
// halt whose entry cannot be written is reported as a failure instead of being
// dropped.
//
// The dedup key is stamped before the entry is queued, so swallowing the error
// saved the stamp with nothing behind it. A binding whose HaltNotifiedRound
// already equals its Round queues nothing ever again, so the notification was
// not merely delayed -- it was gone, and the binding was left running rather
// than asking for a human. Returning the error ends the tick unsaved, so the
// round is still closed on disk and the next tick halts and queues again.
func TestQueueReportScopeRefusalHaltAppendFailureSurfaces(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	reportPath := rt.Store.ReportPath("webshop", 1)
	if err := os.WriteFile(reportPath, []byte("all done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	refused := scopeVerdict{Scoped: true, Refused: true, Path: "internal/other.go", Reason: "outside scope"}

	// Room for the two entries a writer's close queues before the halt -- the
	// round diff and the report -- so the halt's own append is the one the cap
	// refuses.
	fillLogLeavingRoom(t, rt, "webshop", 2)

	var saved store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog("webshop")
		if err != nil {
			return err
		}
		next, err := queueReport(context.Background(), rt, tx, cur, entries,
			reportPath, "done", "test", nil, nil, nil, nil, "", false, refused)
		saved = next
		if err != nil {
			return err
		}
		return tx.Save(next)
	})
	if err == nil {
		t.Fatal("queueReport returned no error, want the halt's failed queue surfaced")
	}
	if !strings.Contains(err.Error(), "log exceeds") {
		t.Fatalf("queueReport error = %v, want the failed append on the log cap", err)
	}

	// The entries the close queued before the halt did land, which is what
	// makes this the halt's append that failed rather than an earlier one.
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	queuedReport, queuedDiff := false, false
	for _, e := range entries {
		if e.Note == logFillerNote {
			continue
		}
		switch {
		case e.Kind == store.KindReport && e.Round == 1 && e.Direction == store.DirToMasterMind:
			queuedReport = true
		case e.Kind == store.KindDiff && e.Round == 1:
			queuedDiff = true
		}
	}
	if !queuedDiff || !queuedReport {
		t.Errorf("close queued diff=%v report=%v, want both: the fault hit an earlier append, not the halt's", queuedDiff, queuedReport)
	}
	if halts := haltEntriesFor(t, rt, "webshop"); len(halts) != 0 {
		t.Errorf("halt entries = %d, want 0: the log was already full", len(halts))
	}

	// Nothing was saved, so the close did not advance and the dedup stamp did
	// not persist: the next tick re-closes this round and queues the entry.
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("stored Round = %d, want 1: a failed close is retried, not committed", got.Round)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("stored HaltNotifiedRound = %d, want 0: the stamp must not outlive the entry it deduplicates", got.HaltNotifiedRound)
	}
	if got.State != store.StateActive {
		t.Errorf("stored State = %q, want %q", got.State, store.StateActive)
	}
	if saved.Round != 2 {
		t.Errorf("returned Round = %d, want 2 (the close advanced before the halt ran)", saved.Round)
	}
}
