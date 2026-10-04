package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
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

// freeLogRoom takes the filler back out, so the next append fits under the cap
// again.
//
// The cap is a permanent refusal, not a transient one, and the halt entry a
// close owes has to be written by some later tick: a test that needs the fault
// to clear between ticks has to remove the filler rather than wait for it.
func freeLogRoom(t *testing.T, rt Runtime, name string) {
	t.Helper()

	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("Store.DB: %v", err)
	}
	rec, ok, err := d.RecordGet(rt.Store.Owner(), name)
	if err != nil || !ok {
		t.Fatalf("RecordGet(%q) = (ok %v, %v)", name, ok, err)
	}
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var evs []db.RecordEvent
	for _, e := range entries {
		if e.Note == logFillerNote {
			continue
		}
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal entry %d: %v", e.Seq, err)
		}
		// Renumbered from 1, and the renumbering is the point of the helper:
		// appendLog refuses on the log's highest seq, so an entry that kept the
		// position the filler gave it would leave the log at the cap.
		evs = append(evs, db.RecordEvent{
			Seq: len(evs) + 1, TS: e.TS, Round: e.Round,
			Direction: string(e.Direction), Kind: string(e.Kind),
			Confirmed: e.Confirmed, DeliveredAt: e.DeliveredAt, Route: e.Route,
			JSON: string(line),
		})
	}
	if err := d.EventReplaceAll(rec.ID, evs); err != nil {
		t.Fatalf("EventReplaceAll: %v", err)
	}

	got, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog after freeing the room: %v", err)
	}
	if len(got) != len(evs) {
		t.Fatalf("log holds %d entries after freeing the room, want %d", len(got), len(evs))
	}
}

// refusedScope is the verdict both tests below close a writer round with. The
// judging is not what either of them pins.
func refusedScope() scopeVerdict {
	return scopeVerdict{Scoped: true, Refused: true, Path: "internal/other.go", Reason: "outside scope"}
}

// closeWithUnwritableHaltEntry closes b's open round with a refused scope
// verdict against a log that has room for everything the close appends except
// the halt's own entry, and saves what the close returned.
//
// It fails the test if the close reports an error: the whole point of the state
// it leaves behind is that a close whose halt entry cannot be written still
// commits.
func closeWithUnwritableHaltEntry(t *testing.T, rt Runtime, b store.Binding) store.Binding {
	t.Helper()

	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if err := os.WriteFile(reportPath, []byte("all done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath(b.Name, b.Round))

	var next store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		next, err = queueReport(context.Background(), rt, tx, cur, entries,
			reportPath, "done", "test", nil, nil, nil, nil, "", false, refusedScope())
		if err != nil {
			return err
		}
		return tx.Save(next)
	})
	if err != nil {
		t.Fatalf("close with an unwritable halt entry: %v", err)
	}
	return next
}

// closeCounts reports how many of the log's diff and report entries the close
// wrote for round, ignoring the filler.
func closeCounts(t *testing.T, rt Runtime, name string, round int) (reports, diffs int) {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Note == logFillerNote || e.Round != round {
			continue
		}
		switch e.Kind {
		case store.KindReport:
			if e.Direction == store.DirToMasterMind {
				reports++
			}
		case store.KindDiff:
			diffs++
		}
	}
	return reports, diffs
}

// TestQueueReportHaltAppendFailureAdvancesAndOwesTheEntry pins the close's
// half of the fix: a post-advance halt whose entry cannot be written is owed,
// not returned.
//
// The store's lock is a file lock rather than a transaction, so the round's
// diff and report entries are on disk before the halt runs. Returning the halt's
// failure ended the tick unsaved with the binding still on a round that had
// already been reported, and from there nothing re-closed it, nothing halted it
// again, and every send was refused with ErrReportPending: a binding stuck on a
// round it had finished. The close therefore owes the notification and commits
// the advance, the halt text and the state word.
func TestQueueReportHaltAppendFailureAdvancesAndOwesTheEntry(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)

	// Room for the two entries a writer's close queues before the halt -- the
	// round diff and the report -- so the halt's own append is the one the cap
	// refuses.
	fillLogLeavingRoom(t, rt, b.Name, 2)

	got := closeWithUnwritableHaltEntry(t, rt, b)
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2: the close advances, and the advance is what was being lost", got.Round)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want %q", got.State, store.StateNeedsYou)
	}
	wantHalt := "round 1 changed internal/other.go outside actor"
	if !strings.Contains(got.Halt, wantHalt) {
		t.Fatalf("Halt = %q, want it to contain %q", got.Halt, wantHalt)
	}
	if got.OwedHalt == nil {
		t.Fatal("OwedHalt is nil, want the entry the close could not write recorded on the binding")
	}
	if got.OwedHalt.Round != 1 {
		t.Errorf("OwedHalt.Round = %d, want 1 (the round that closed)", got.OwedHalt.Round)
	}
	if got.OwedHalt.Text != got.Halt {
		t.Errorf("OwedHalt.Text = %q, want the halt text %q the entry would carry", got.OwedHalt.Text, got.Halt)
	}
	// The dedup stamp stays where the halt put it, which is why the retry is
	// keyed on the marker and not on this.
	if got.HaltNotifiedRound != 2 {
		t.Errorf("HaltNotifiedRound = %d, want 2 (keyed to the new round)", got.HaltNotifiedRound)
	}

	// The log holds what the close got through and nothing more. The report
	// being there is what makes this the halt's append that failed rather than
	// an earlier one -- and what makes the round closed whatever the binding
	// went on to save.
	reports, diffs := closeCounts(t, rt, b.Name, 1)
	if reports != 1 || diffs != 1 {
		t.Errorf("close queued %d reports and %d diffs for round 1, want one of each: the fault hit an earlier append, not the halt's", reports, diffs)
	}
	if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 0 {
		t.Errorf("halt entries = %d, want 0: the log was already full", len(halts))
	}

	// Saved, not merely returned: the wedge was a binding left on a reported
	// round, so what is on disk is the question.
	stored, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Round != 2 {
		t.Errorf("stored Round = %d, want 2: a reported round must not be the binding's round", stored.Round)
	}
	if stored.State != store.StateNeedsYou || stored.Halt != got.Halt {
		t.Errorf("stored binding = %q/%q, want %q/%q", stored.State, stored.Halt, got.State, got.Halt)
	}
	if stored.OwedHalt == nil {
		t.Error("stored OwedHalt is nil, want the owed entry to survive the tick that could not write it")
	}
}

// TestOwedHaltEntryIsQueuedOnALaterTick is the retry half: the owed entry is not
// a promise, it is written. By the next tick the log has room again, and that
// tick queues the halt under the round that closed without closing anything a
// second time.
func TestOwedHaltEntryIsQueuedOnALaterTick(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	fillLogLeavingRoom(t, rt, b.Name, 2)
	owed := closeWithUnwritableHaltEntry(t, rt, b)
	freeLogRoom(t, rt, b.Name)

	got, err := reconcileAndSave(t, rt, owed)
	if err != nil {
		t.Fatalf("the tick after the owed entry: %v", err)
	}
	if got.OwedHalt != nil {
		t.Errorf("OwedHalt = %+v, want nil: the entry is written, so nothing is owed", got.OwedHalt)
	}
	// The retry is a notification, not a round: it neither advances nor halts.
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2: the owed entry is not a second close", got.Round)
	}
	if got.State != store.StateNeedsYou || !strings.Contains(got.Halt, "internal/other.go") {
		t.Errorf("binding = %q/%q, want the halt to stand until a human resolves it", got.State, got.Halt)
	}

	halts := haltEntriesFor(t, rt, b.Name)
	if len(halts) != 1 {
		t.Fatalf("halt entries = %d, want exactly 1: %+v", len(halts), halts)
	}
	if halts[0].Round != 1 {
		t.Errorf("halt entry Round = %d, want 1 (the round whose refusal it is about)", halts[0].Round)
	}
	if !strings.Contains(halts[0].Payload, "internal/other.go") {
		t.Errorf("halt entry Payload = %q, want it to carry the scope refusal", halts[0].Payload)
	}

	// One report entry for the round, still: the round was closed once.
	if reports, _ := closeCounts(t, rt, b.Name, 1); reports != 1 {
		t.Errorf("report entries for round 1 = %d, want 1: the owed entry must not re-close the round", reports)
	}

	// The marker is cleared, so however many ticks run the halt is notified
	// once. The dedup stamp cannot do this job on its own: the failed tick left
	// it equal to the binding's round.
	if _, err := reconcileAndSave(t, rt, got); err != nil {
		t.Fatalf("the tick after the owed entry was queued: %v", err)
	}
	if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 1 {
		t.Errorf("halt entries after another tick = %d, want 1: %+v", len(halts), halts)
	}
}

// TestFreshAttemptDropsTheOwedHaltMarker pins that a fresh attempt carries no
// notification from the halt it replaces. A re-send and a rebind each clear the
// halt's text, its stamp and its state word; the owed marker is the one field
// that says "this binding still has a halt to tell its MasterMind about", and
// leaving it behind makes the next tick queue that old halt -- filed under the
// round that raised it, which is a round the fresh attempt has already left.
func TestFreshAttemptDropsTheOwedHaltMarker(t *testing.T) {
	t.Parallel()

	t.Run("re-send", func(t *testing.T) {
		t.Parallel()

		rt, b := sentBinding(t)
		fillLogLeavingRoom(t, rt, b.Name, 2)
		owed := closeWithUnwritableHaltEntry(t, rt, b)
		if owed.OwedHalt == nil {
			t.Fatal("OwedHalt is nil, want the owed entry as the fixture")
		}
		freeLogRoom(t, rt, b.Name)
		endProcess(t, rt, owed)

		if _, err := Send(context.Background(), rt, b.Name, writePlan(t, "again"), SendOptions{}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		stored, err := rt.Store.Load(b.Name)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if stored.Halt != "" || stored.HaltNotifiedRound != 0 {
			t.Fatalf("halt = %q notified %d, want the re-send's own reset as the fixture", stored.Halt, stored.HaltNotifiedRound)
		}
		if stored.OwedHalt != nil {
			t.Errorf("OwedHalt = %+v, want nil: the re-send answers the halt the marker is about", stored.OwedHalt)
		}

		got, err := reconcileAndSave(t, rt, stored)
		if err != nil {
			t.Fatalf("the tick after the re-send: %v", err)
		}
		if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 0 {
			t.Errorf("halt entries = %d, want 0: %+v", len(halts), halts)
		}
		if got.State != store.StateActive {
			t.Errorf("state = %q, want active: nothing has halted the new round", got.State)
		}
	})

	t.Run("rebind", func(t *testing.T) {
		t.Parallel()

		rt := newRuntime(t)
		existing := store.Binding{
			Name:             "webshop",
			CWD:              "/repo",
			Round:            3,
			State:            store.StateNeedsYou,
			MasterMind:       store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
			Builder:          store.Endpoint{Mode: store.ModeHeadless, Kind: "opencode"},
			BuilderCandidate: testOpencodeRef,
			Halt:             "round 3 needs a hand",
			HaltAt:           baseTime,
			OwedHalt:         &store.OwedHalt{Round: 3, Text: "round 3 needs a hand"},
		}
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		if _, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Rebind: true, Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
		}); err != nil {
			t.Fatalf("Bind --resume --rebind: %v", err)
		}
		saved, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if saved.OwedHalt != nil {
			t.Errorf("OwedHalt = %+v, want nil: the replacement builder answers the halt", saved.OwedHalt)
		}
	})
}

// reconcileAndSave runs one tick the way the daemon does -- the stored binding
// under the lock, then saved -- which is the only sequence in which the marker
// a tick clears can be seen to be cleared.
func reconcileAndSave(t *testing.T, rt Runtime, _ store.Binding) (store.Binding, error) {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		next, err := Reconcile(context.Background(), rt, tx, cur)
		out = next
		if err != nil {
			return err
		}
		return tx.Save(next)
	})
	return out, err
}
