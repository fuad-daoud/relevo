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
