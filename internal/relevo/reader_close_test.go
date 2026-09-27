package relevo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

// readerCloseFinal is the final message a fake reader stream carries: a
// summary ending in a relevo block, so a close can tail-parse it.
const readerCloseFinal = "The review is done.\n\n```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: [index.html]\ncommands_run: []\nnot_done: []\n```\n"

// readerCloseSummary is readerCloseFinal with the relevo block stripped: what
// summary.md must hold after the close, so the planner receives no stray block.
const readerCloseSummary = "The review is done.\n"

// bindReader binds a reviewer on repo and sends it a plan, so round 1 is open
// in a scratch worktree. It returns the runtime and the stored binding.
func bindReader(t *testing.T, repo string) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Git = git.NewClient("git", 0, 0)
	rt.Runner = newFakeRunner()
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "reader-bind", Role: "reviewer", Candidate: testClaudeRef,
		PlannerID: testPlannerName, CWD: repo,
	}); err != nil {
		t.Fatalf("Bind(reader): %v", err)
	}
	if _, err := Send(context.Background(), rt, "reader-bind", writePlan(t, "review it"), SendOptions{}); err != nil {
		t.Fatalf("Send(reader): %v", err)
	}
	b, err := rt.Store.Load("reader-bind")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// writeReaderStream writes a claude stream whose last assistant text is text.
func writeReaderStream(t *testing.T, rt Runtime, name string, round int, text string) {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	if err != nil {
		t.Fatalf("encode stream: %v", err)
	}
	path := rt.Store.BuilderStreamPath(name, round)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
}

// reportEntryFor returns the newest to_planner report entry of one round.
func reportEntryFor(t *testing.T, rt Runtime, name string, round int) store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var found store.LogEntry
	ok := false
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport {
			found, ok = e, true
		}
	}
	if !ok {
		t.Fatalf("no report entry for %s round %d: %+v", name, round, entries)
	}
	return found
}

// seedLog appends entries to a binding's log.
func seedLog(t *testing.T, rt Runtime, name string, entries ...store.LogEntry) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, e := range entries {
			if err := tx.AppendLog(name, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append log for %s: %v", name, err)
	}
}

// exitReaderRunner makes the fake runner report the reader's process as
// exited: a reader round closes on its runner's exit, not on its marker, so a
// test that wants a close has to let the process go first.
func exitReaderRunner(t *testing.T, rt Runtime, b store.Binding) {
	t.Helper()
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	fr.alive[b.Builder.PID] = []bool{false}
	fr.exit(b.Builder.PID, 0)
}

// TestReaderCloseWritesSummaryFromTheFinalMessage closes a reader round whose
// runner has exited and checks that summary.md holds the runner's final
// message, that the report entry points at it, that its tail parses, and that
// no diff was taken.
func TestReaderCloseWritesSummaryFromTheFinalMessage(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	summary := rt.Store.SummaryPath("reader-bind", 1, "reviewer")
	got, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("summary.md was not written: %v", err)
	}
	if string(got) != readerCloseSummary {
		t.Errorf("summary.md = %q, want the stripped summary %q", got, readerCloseSummary)
	}

	e := reportEntryFor(t, rt, "reader-bind", 1)
	if e.Path != summary {
		t.Errorf("report entry Path = %q, want the summary path %q", e.Path, summary)
	}
	if _, ok, _ := reporttail.ParseWithReason([]byte(got)); ok {
		t.Errorf("the saved summary still carries a parseable relevo block:\n%s", got)
	}
	if e.Outcome != reporttail.OutcomeDone {
		t.Errorf("entry.Outcome = %q, want %q", e.Outcome, reporttail.OutcomeDone)
	}
	if !strings.Contains(e.Payload, "The runner finished round 1. Findings: relevo show reader-bind --round 1 --summary") {
		t.Errorf("payload does not contain the expected reader close line:\n%s", e.Payload)
	}
	if strings.Contains(e.Payload, "--report") {
		t.Errorf("reader payload contains --report:\n%s", e.Payload)
	}

	entries, _ := rt.Store.ReadLog("reader-bind")
	if HasEntry(entries, 1, store.DirToPlanner, store.KindDiff) {
		t.Error("a reader round captured a diff")
	}
}

// TestReaderCloseKeepsARunnerWrittenSummary checks that a summary the runner
// wrote itself is the report, and is never overwritten by the final message.
func TestReaderCloseKeepsARunnerWrittenSummary(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	summary := rt.Store.SummaryPath("reader-bind", 1, "reviewer")
	if err := os.MkdirAll(filepath.Dir(summary), 0o755); err != nil {
		t.Fatal(err)
	}
	const own = "# The runner's own summary\n"
	if err := os.WriteFile(summary, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	if string(got) != own {
		t.Errorf("summary.md = %q, want the runner's own %q", got, own)
	}
	if e := reportEntryFor(t, rt, "reader-bind", 1); e.Path != summary {
		t.Errorf("report entry Path = %q, want the summary path %q", e.Path, summary)
	}
}

// TestReaderCloseStripsARunnerWrittenSummaryWithABlock checks that when the
// runner writes its own summary and that summary ends in a relevo block, the
// block is stripped just like the final-message case.
func TestReaderCloseStripsARunnerWrittenSummaryWithABlock(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	summary := rt.Store.SummaryPath("reader-bind", 1, "reviewer")
	if err := os.MkdirAll(filepath.Dir(summary), 0o755); err != nil {
		t.Fatal(err)
	}
	const ownWithBlock = "# Runner summary.\n\n```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"
	if err := os.WriteFile(summary, []byte(ownWithBlock), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	const wantStripped = "# Runner summary.\n"
	if string(got) != wantStripped {
		t.Errorf("summary.md = %q, want the stripped bytes %q", got, wantStripped)
	}
	e := reportEntryFor(t, rt, "reader-bind", 1)
	if e.Outcome != reporttail.OutcomeDone {
		t.Errorf("entry.Outcome = %q, want %q", e.Outcome, reporttail.OutcomeDone)
	}
}

// TestReaderCloseKeepsTheHaltedStatusOutOfTheSummary checks that a halted
// reader round's status is preserved in the entry's Outcome/HaltedAt and the
// payload annotation, while the summary.md file has no relevo block.
func TestReaderCloseKeepsTheHaltedStatusOutOfTheSummary(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	const haltedFinal = "Partial review.\n\n```relevo\nstatus: halted\nhalted_at: \"step 2\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"
	writeReaderStream(t, rt, "reader-bind", 1, haltedFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	summary := rt.Store.SummaryPath("reader-bind", 1, "reviewer")
	got, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	if _, ok, _ := reporttail.ParseWithReason(got); ok {
		t.Errorf("the saved summary still carries a parseable relevo block:\n%s", got)
	}

	e := reportEntryFor(t, rt, "reader-bind", 1)
	if e.Outcome != reporttail.OutcomeHalted {
		t.Errorf("entry.Outcome = %q, want %q", e.Outcome, reporttail.OutcomeHalted)
	}
	if e.HaltedAt != "step 2" {
		t.Errorf("entry.HaltedAt = %q, want \"step 2\"", e.HaltedAt)
	}
	if !strings.Contains(e.Payload, "The runner finished round 1 -- halted at \"step 2\".") {
		t.Errorf("payload does not contain the halted annotation:\n%s", e.Payload)
	}

	entries, _ := rt.Store.ReadLog("reader-bind")
	if got := WaitOutcome(b, entries, 1, func(string, int) string { return "" }); got.Code != WaitHalted {
		t.Errorf("WaitOutcome code = %d, want WaitHalted (%d)", got.Code, WaitHalted)
	}
}

// TestReaderCloseRemovesTheScratch checks that a reader round's throwaway
// worktree is gone once the round has closed on its marker.
func TestReaderCloseRemovesTheScratch(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	scratch := rt.Store.ScratchWorktreePath("reader-bind", 1)
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("the scratch was not created: %v", err)
	}
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("the scratch is still present after close: %v", err)
	}
}

// TestReaderStopRemovesTheScratch checks that stopping a reader round takes
// its throwaway worktree with it.
func TestReaderStopRemovesTheScratch(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	scratch := rt.Store.ScratchWorktreePath("reader-bind", 1)
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("the scratch was not created: %v", err)
	}

	if _, err := Stop(context.Background(), rt, "reader-bind", StopOptions{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("the scratch is still present after stop: %v", err)
	}
}

// TestReaderDoneRemovesTheScratch checks that done takes a reader binding's
// throwaway worktree with it.
func TestReaderDoneRemovesTheScratch(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	scratch := rt.Store.ScratchWorktreePath("reader-bind", 1)
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("the scratch was not created: %v", err)
	}

	if _, err := Done(context.Background(), rt, "reader-bind"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("the scratch is still present after done: %v", err)
	}
}

// TestSweepScratchKeepsOpenReaderRounds checks that the daemon's sweep removes
// every scratch except the one the binding's current round still needs.
func TestSweepScratchKeepsOpenReaderRounds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := readerRepo(t)
	rt := newRuntime(t)
	rt.Git = git.NewClient("git", 0, 0)

	// open-reader is still on round 1, whose scratch it needs. closed-reader
	// has closed round 1 and advanced to round 2, so round 1's scratch is a
	// leftover the sweep must take. ghost has no binding at all.
	for _, b := range []struct {
		name  string
		round int
	}{{"open-reader", 1}, {"closed-reader", 2}} {
		if err := rt.Store.Save(store.Binding{
			Name: b.name, CWD: repo, Shape: store.ShapeReader,
			Round: b.round, State: store.StateActive,
		}); err != nil {
			t.Fatalf("Save(%s): %v", b.name, err)
		}
	}
	seedLog(t, rt, "open-reader",
		store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan})
	seedLog(t, rt, "closed-reader",
		store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan},
		store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport})

	for _, name := range []string{"open-reader", "closed-reader", "ghost"} {
		if _, err := CreateScratch(ctx, rt, store.Binding{Name: name, CWD: repo}, 1); err != nil {
			t.Fatalf("CreateScratch(%s): %v", name, err)
		}
	}

	sweepReaderScratch(ctx, rt)

	if _, err := os.Stat(rt.Store.ScratchWorktreePath("open-reader", 1)); err != nil {
		t.Errorf("the open reader's scratch was removed: %v", err)
	}
	for _, name := range []string{"closed-reader", "ghost"} {
		if _, err := os.Stat(rt.Store.ScratchWorktreePath(name, 1)); !os.IsNotExist(err) {
			t.Errorf("%s's scratch is still present after the sweep: %v", name, err)
		}
	}
}

// TestSweepKeepsTheCurrentRoundsScratchBeforeItOpens checks the send race: a
// reader binding is on round 2 with a closed round 1 in its log and no round-2
// plan entry yet, because send creates round 2's scratch before it logs the
// plan. The sweep must keep round 2's scratch and take round 1's.
func TestSweepKeepsTheCurrentRoundsScratchBeforeItOpens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := readerRepo(t)
	rt := newRuntime(t)
	rt.Git = git.NewClient("git", 0, 0)

	if err := rt.Store.Save(store.Binding{
		Name: "reader-bind", CWD: repo, Shape: store.ShapeReader,
		Round: 2, State: store.StateActive,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	seedLog(t, rt, "reader-bind",
		store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan},
		store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport})

	for _, round := range []int{1, 2} {
		if _, err := CreateScratch(ctx, rt, store.Binding{Name: "reader-bind", CWD: repo}, round); err != nil {
			t.Fatalf("CreateScratch(%d): %v", round, err)
		}
	}

	sweepReaderScratch(ctx, rt)

	if _, err := os.Stat(rt.Store.ScratchWorktreePath("reader-bind", 2)); err != nil {
		t.Errorf("the current round's scratch was removed before its plan entry: %v", err)
	}
	if _, err := os.Stat(rt.Store.ScratchWorktreePath("reader-bind", 1)); !os.IsNotExist(err) {
		t.Errorf("round 1's scratch is still present after the sweep: %v", err)
	}
}

// TestReaderRoundIsNotAnEscape checks that a reader round never runs the
// worktree-escape check: a dirty binding tree beside a reader's scratch is not
// an escape, and no git read is even made.
func TestReaderRoundIsNotAnEscape(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{snapshotTreeID: "tree1", dirtyResult: true}
	rt := Runtime{Git: fg}
	reader := store.Binding{
		Name: "reader-bind", CWD: "/tree", Repo: "/repo",
		Shape: store.ShapeReader, RoundBaselineTree: "tree1",
		Builder: store.Endpoint{Mode: store.ModeHeadless},
	}
	if got := escapeCheck(context.Background(), rt, reader, true); got != EscapeNone {
		t.Errorf("escapeCheck(reader) = %v, want EscapeNone", got)
	}
	if fg.snapshotCalls != 0 {
		t.Errorf("the reader escape check made %d git reads, want 0", fg.snapshotCalls)
	}

	// The same inputs on a writer are exactly the escape the check catches, so
	// the reader arm is what makes the difference.
	writer := reader
	writer.Shape = store.ShapeWriter
	if got := escapeCheck(context.Background(), rt, writer, true); got != EscapeNote {
		t.Errorf("escapeCheck(writer) = %v, want EscapeNote", got)
	}
}

// TestReaderRoundWaitsForExitAfterMarker: a reader round's marker is not the
// end of its stream. While its runner is alive the round stays open even though
// the marker is present, and the next tick -- after the runner has exited and
// the stream carries the final message -- closes it with that message as
// summary.md.
func TestReaderRoundWaitsForExitAfterMarker(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))

	open, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile with a live runner: %v", err)
	}
	if open.Round != 1 {
		t.Fatalf("round = %d with a live runner after its marker, want the round still open on round 1", open.Round)
	}
	if _, err := os.Stat(rt.Store.SummaryPath("reader-bind", 1, "reviewer")); !os.IsNotExist(err) {
		t.Errorf("summary.md was written while the runner was still alive: %v", err)
	}

	exitReaderRunner(t, rt, open)
	closed, err := reconcile(t, rt, open)
	if err != nil {
		t.Fatalf("Reconcile after the runner exited: %v", err)
	}
	if closed.Round != 2 {
		t.Fatalf("round = %d after the runner exited, want the round closed", closed.Round)
	}
	got, err := os.ReadFile(rt.Store.SummaryPath("reader-bind", 1, "reviewer"))
	if err != nil {
		t.Fatalf("summary.md was not written: %v", err)
	}
	if string(got) != readerCloseSummary {
		t.Errorf("summary.md = %q, want the stripped summary %q", got, readerCloseSummary)
	}
}

// TestReaderRoundGraceClosesALingeringRunner: a runner still alive more than
// readerFinalMessageGrace after its marker must not hold the round forever. The
// round closes with the summary taken from the stream as it is, the note says
// so, and the lingering process is stopped the way `relevo stop` stops one.
func TestReaderRoundGraceClosesALingeringRunner(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	marker := rt.Store.DonePath("reader-bind", 1)
	touch(t, marker)
	aged := baseTime.Add(-3 * time.Minute)
	if err := os.Chtimes(marker, aged, aged); err != nil {
		t.Fatalf("age the marker: %v", err)
	}

	closed, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if closed.Round != 2 {
		t.Fatalf("round = %d, want the lingering runner's round closed", closed.Round)
	}
	e := reportEntryFor(t, rt, "reader-bind", 1)
	if !strings.Contains(e.Note, readerSummaryEarlyNote) {
		t.Errorf("report note = %q, want it to contain %q", e.Note, readerSummaryEarlyNote)
	}
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	if len(fr.kills) != 1 {
		t.Fatalf("kills = %d, want the lingering runner stopped once", len(fr.kills))
	}
	if fr.kills[0].PID != b.Builder.PID {
		t.Errorf("stopped pid = %d, want the runner's pid %d", fr.kills[0].PID, b.Builder.PID)
	}
}

// TestWriterRoundStillClosesOnMarker: a writer is unchanged. Its report is a
// file written before the marker, so a live process does not delay the close.
func TestWriterRoundStillClosesOnMarker(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d with a live runner and a marker, want the writer closed on the marker", got.Round)
	}
}
