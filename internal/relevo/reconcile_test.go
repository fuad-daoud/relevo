package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// closeOnMarkerUnderLock calls closeOnMarker the way Reconcile does: inside
// the store lock, with the binding's current log.
func closeOnMarkerUnderLock(t *testing.T, rt Runtime, b store.Binding) (store.Binding, bool) {
	t.Helper()
	out, closed, _ := closeOnMarkerUnderLockGating(t, rt, b)
	return out, closed
}

// closeOnMarkerUnderLockGating is closeOnMarkerUnderLock plus the gating
// return, for the gate lifecycle tests (#132).
func closeOnMarkerUnderLockGating(t *testing.T, rt Runtime, b store.Binding) (store.Binding, bool, bool) {
	t.Helper()
	var out store.Binding
	var closed, gating bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		out, closed, gating, _, err = closeOnMarker(context.Background(), rt, tx, b, entries, "")
		return err
	})
	if err != nil {
		t.Fatalf("closeOnMarker: %v", err)
	}
	return out, closed, gating
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}

// TestReconcileDrainsPaneSessionRecord pins Reconcile's pane path (#184):
// once refreshEndpoint has run, drainSession renders whatever the builder's
// own session record holds since the last tick into the round's log, the
// way reconcileHeadless drains a headless stream.
// TestReconcileWaitsOutNudgeGraceBeforeScraping is the regression test for a
// scrape that raced the builder: relevo nudged on one tick and scraped on the
// next, two seconds later, then advanced the round -- so the builder's real
// report was written to an abandoned round's path and never relayed.
// TestReconcileSkipsPausedPausedBinding, TestReconcileUnbreaksWhenPaneAndKindMatchWithoutSession
// and TestBindRacesSessionLookupAndReconcileRecovers are gone with the pane
// builder itself (#303; closed-list items 1 and 6): there is no builder pane,
// no session cursor to drain and no agent list to compare a session against.
// Their headless equivalents live in headless_test.go.
func TestReconcileQueuesReportWhenBuilderIdleAndMarkerExists(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	b.RoundSwitches = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after a report", got.Round)
	}
	// The new round has not been sent, so it carries no deadline of its own
	// and no memory of an earlier round's halt notification.
	if !got.RoundStartedAt.IsZero() {
		t.Errorf("RoundStartedAt = %s, want zero until Send stamps the new round", got.RoundStartedAt)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d, want 0 on a fresh round", got.HaltNotifiedRound)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 on a fresh round", got.RoundSwitches)
	}

	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "relevo show webshop --round 1 --report") {
		t.Errorf("payload must name the show command, got %q", pending.Payload)
	}
}

// TestReconcileQueuesReportInsideStartGrace pins the marker's status
// independence for the first seconds of a round: the marker closes the round
// even though very little time has passed since the process started.
func TestReconcileQueuesReportInsideStartGrace(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	clock.Advance(5 * time.Second)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after a report", got.Round)
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "relevo show webshop --round 1 --report") {
		t.Errorf("payload must name the show command, got %q", pending.Payload)
	}
}

// TestReconcileIgnoresWorkingBuilder asserted the round against a pane's
// builder status. There is no status to consult (#303; closed-list item 6);
// the headless equivalent -- a live process with no marker is left alone --
// is TestReconcileHeadlessAliveWaits in headless_test.go.

// TestReconcileSkipsPaused: Reconcile returns immediately for a PAUSED
// binding, exactly as it does for DONE.
func TestReconcileSkipsPaused(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	b.State = store.StatePaused
	b.Builder = store.Endpoint{Kind: "agy", Mode: store.ModeHeadless} // pause cleared the identity
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save paused: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StatePaused {
		t.Errorf("State = %s, want paused", got.State)
	}
	if got.Round != b.Round {
		t.Errorf("Round = %d, want it untouched at %d", got.Round, b.Round)
	}
}

func TestReconcileDiffCapture(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 2, Insertions: 10, Deletions: 3},
			Patch: []byte("diff content"),
		},
	}
	rt.Git = fg
	b.RoundBaselineTree = "tree-start"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	reportFile := rt.Store.ReportPath("webshop", 1)
	if err := os.WriteFile(reportFile, []byte("report content"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	// First tick
	bAfter, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile 1: %v", err)
	}
	if bAfter.Round != 2 {
		t.Fatalf("expected round 2, got %d", bAfter.Round)
	}
	if bAfter.RoundBaselineTree != "" {
		t.Errorf("RoundBaselineTree not cleared, got %q", bAfter.RoundBaselineTree)
	}

	log, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	// Verify log order: KindDiff before KindReport
	var diffIdx, reportIdx int = -1, -1
	diffCount := 0
	for i, entry := range log {
		if entry.Round == 1 && entry.Kind == store.KindDiff {
			diffIdx = i
			diffCount++
			if !entry.Confirmed {
				t.Error("KindDiff entry must be confirmed")
			}
			if entry.Direction != store.DirToMasterMind {
				t.Errorf("KindDiff direction = %s, want to_planner", entry.Direction)
			}
			if entry.Path != rt.Store.DiffPath("webshop", 1) {
				t.Errorf("KindDiff path = %s", entry.Path)
			}
		}
		if entry.Round == 1 && entry.Kind == store.KindReport {
			reportIdx = i
		}
	}
	if diffCount != 1 {
		t.Fatalf("expected exactly 1 KindDiff entry, got %d", diffCount)
	}
	if diffIdx == -1 || reportIdx == -1 || diffIdx >= reportIdx {
		t.Fatalf("KindDiff (%d) must be ordered before KindReport (%d)", diffIdx, reportIdx)
	}

	// Verify report payload carries diff line
	reportEntry := log[reportIdx]
	wantLine := "Diff: relevo show webshop --round 1 --diff (2 files, +10 -3)"
	if !strings.Contains(reportEntry.Payload, wantLine) {
		t.Fatalf("report payload %q does not contain %q", reportEntry.Payload, wantLine)
	}

	// PendingForMasterMind still returns the report
	pending, ok, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !ok {
		t.Fatalf("PendingForMasterMind: ok=%v, err=%v", ok, err)
	}
	if pending.Kind != store.KindReport {
		t.Fatalf("pending kind = %s, want report", pending.Kind)
	}

	// Second tick: should not append duplicate diff entry
	_, err = reconcile(t, rt, bAfter)
	if err != nil {
		t.Fatalf("Reconcile 2: %v", err)
	}
	log2, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	diffCount2 := 0
	for _, entry := range log2 {
		if entry.Round == 1 && entry.Kind == store.KindDiff {
			diffCount2++
		}
	}
	if diffCount2 != 1 {
		t.Fatalf("expected still 1 KindDiff entry after tick 2, got %d", diffCount2)
	}
}

func TestQueueReportRecordsCommitFacts(t *testing.T) {
	t.Parallel()

	closeRound := func(t *testing.T, fg *fakeGit, head string) (store.Binding, []store.LogEntry) {
		t.Helper()
		rt, b := sentBinding(t)
		rt.Git = fg
		b.RoundBaselineTree = "tree-start"
		b.RoundBaselineHead = head
		b.Branch = "relevo/webshop"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		return got, entries
	}
	diffAndReport := func(t *testing.T, entries []store.LogEntry) (store.LogEntry, store.LogEntry) {
		t.Helper()
		var diff, report store.LogEntry
		for _, e := range entries {
			if e.Round != 1 {
				continue
			}
			switch e.Kind {
			case store.KindDiff:
				diff = e
			case store.KindReport:
				report = e
			}
		}
		if diff.Kind == "" || report.Kind == "" {
			t.Fatalf("missing diff or report entry in %+v", entries)
		}
		return diff, report
	}
	changed := git.Diff{Stat: git.Stat{FilesChanged: 2, Insertions: 10, Deletions: 3}, Patch: []byte("diff content")}

	t.Run("commits and clean", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-end", revListCount: 3}
		got, entries := closeRound(t, fg, "head-start")
		diff, report := diffAndReport(t, entries)
		if diff.Commits != 3 || diff.Tree != "clean" {
			t.Errorf("diff entry facts = (%d, %q), want (3, clean)", diff.Commits, diff.Tree)
		}
		if diff.Note != "2 files, +10 -3; 3 commits, clean" {
			t.Errorf("diff note = %q", diff.Note)
		}
		if !strings.Contains(report.Payload, " -- 3 commits on relevo/webshop, tree clean") {
			t.Errorf("payload %q lacks the commit clause", report.Payload)
		}
		if got.RoundBaselineHead != "" || got.RoundBaselineTree != "" {
			t.Errorf("baseline not cleared: head=%q tree=%q", got.RoundBaselineHead, got.RoundBaselineTree)
		}
		if fg.lastRevListFrom != "head-start" || fg.lastRevListTo != "head-end" {
			t.Errorf("rev-list range %q..%q", fg.lastRevListFrom, fg.lastRevListTo)
		}
	})

	t.Run("none and dirty", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-start", dirtyResult: true}
		_, entries := closeRound(t, fg, "head-start")
		diff, report := diffAndReport(t, entries)
		if diff.Commits != 0 || diff.Tree != "dirty" {
			t.Errorf("diff entry facts = (%d, %q), want (0, dirty)", diff.Commits, diff.Tree)
		}
		if !strings.Contains(report.Payload, " -- no commits; changes are uncommitted in the worktree") {
			t.Errorf("payload %q lacks the dirty clause", report.Payload)
		}
	})

	t.Run("no baseline head", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-end", revListCount: 3}
		_, entries := closeRound(t, fg, "")
		diff, report := diffAndReport(t, entries)
		if diff.Tree != "" || diff.Commits != 0 {
			t.Errorf("diff entry facts = (%d, %q), want unknown", diff.Commits, diff.Tree)
		}
		if !strings.Contains(diff.Note, "; commits unknown (no baseline)") {
			t.Errorf("diff note = %q", diff.Note)
		}
		if !strings.Contains(report.Payload, " -- commits unknown (no baseline)") {
			t.Errorf("payload %q", report.Payload)
		}
		if fg.revListCalls != 0 {
			t.Errorf("rev-list called without a baseline head")
		}
	})
}

// TestReconcileRefreshesMasterMindEndpoint refreshed a mastermind pane id from
// the agent list. Both halves are gone (#303; closed-list items 6 and 8):
// there is no agent list and MasterMind.PaneID is written by nothing.

// TestQueueReportRecordsRusage: a headless round's report entry gets
// Rusage from rt.Runner.Rusage when the runner has one, and stays nil
// when it does not (#244, #216).
func TestQueueReportRecordsRusage(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (Runtime, store.Binding, *fakeRunner) {
		t.Helper()
		fr := newFakeRunner()
		rt, b := seedHeadless(t, fr)
		b.Builder.PID = 9001
		b.Builder.StartedAt = 1_700_000_000
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte("report body"), 0o644); err != nil {
			t.Fatal(err)
		}
		return rt, b, fr
	}
	closeRound := func(t *testing.T, rt Runtime, b store.Binding) store.LogEntry {
		t.Helper()
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load(b.Name)
			if err != nil {
				return err
			}
			entries, err := tx.ReadLog(b.Name)
			if err != nil {
				return err
			}
			next, err := queueReport(context.Background(), rt, tx, cur, entries, rt.Store.ReportPath(b.Name, b.Round), "done", "test", nil, nil, nil, nil, "")
			if err != nil {
				return err
			}
			return tx.Save(next)
		})
		if err != nil {
			t.Fatalf("queueReport: %v", err)
		}
		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Round == b.Round && e.Kind == store.KindReport {
				return e
			}
		}
		t.Fatalf("no report entry for round %d in %+v", b.Round, entries)
		return store.LogEntry{}
	}

	t.Run("ok true", func(t *testing.T) {
		rt, b, fr := setup(t)
		fr.setRusage(b.Builder.PID, spawn.ProcRusage{CPUMS: 12300, PeakMemBytes: 850 << 20})
		entry := closeRound(t, rt, b)
		if entry.Rusage == nil || entry.Rusage.CPUMS != 12300 || entry.Rusage.PeakMemBytes != 850<<20 {
			t.Errorf("report entry Rusage = %+v, want {12300 %d}", entry.Rusage, int64(850<<20))
		}
	})
	t.Run("ok false", func(t *testing.T) {
		rt, b, _ := setup(t)
		entry := closeRound(t, rt, b)
		if entry.Rusage != nil {
			t.Errorf("report entry Rusage = %+v, want nil", entry.Rusage)
		}
	})
}

func TestQueueReport_RoundClosedTree(t *testing.T) {
	t.Parallel()

	t.Run("ordinary close", func(t *testing.T) {
		rt, b := sentBinding(t)
		fg := &fakeGit{
			snapshotTreeID: "tree-end-ordinary",
			diffResult: git.Diff{
				Stat:  git.Stat{FilesChanged: 1, Insertions: 5, Deletions: 2},
				Patch: []byte("diff content"),
			},
		}
		rt.Git = fg
		b.RoundBaselineTree = "tree-start"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		reportFile := rt.Store.ReportPath(b.Name, b.Round)
		if err := os.WriteFile(reportFile, []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath(b.Name, b.Round))

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got.Round != 2 {
			t.Fatalf("round = %d, want 2", got.Round)
		}
		if got.RoundClosedTree != fg.snapshotTreeID {
			t.Fatalf("RoundClosedTree = %q, want %q", got.RoundClosedTree, fg.snapshotTreeID)
		}
	})

	t.Run("retry path", func(t *testing.T) {
		rt, b := sentBinding(t)
		fg := &fakeGit{
			snapshotTreeID: "tree-snapshot-should-not-be-used",
		}
		rt.Git = fg
		b.RoundBaselineTree = "tree-start"
		b.RoundClosedTree = "stale-tree-from-round-3"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		// A KindDiff entry already exists for the round
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			return tx.AppendLog(b.Name, store.LogEntry{
				TS:        rt.Now().UTC(),
				Round:     b.Round,
				Direction: store.DirToMasterMind,
				Kind:      store.KindDiff,
				Confirmed: true,
			})
		})
		if err != nil {
			t.Fatal(err)
		}

		reportFile := rt.Store.ReportPath(b.Name, b.Round)
		if err := os.WriteFile(reportFile, []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath(b.Name, b.Round))

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got.Round != 2 {
			t.Fatalf("round = %d, want 2", got.Round)
		}
		if got.RoundClosedTree != "" {
			t.Fatalf("RoundClosedTree = %q, want empty", got.RoundClosedTree)
		}
	})

	t.Run("non-git tree", func(t *testing.T) {
		rt, b := sentBinding(t)
		rt.Git = nil
		b.RoundBaselineTree = "tree-start"
		b.RoundClosedTree = "stale-tree-from-round-3"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		reportFile := rt.Store.ReportPath(b.Name, b.Round)
		if err := os.WriteFile(reportFile, []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath(b.Name, b.Round))

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got.Round != 2 {
			t.Fatalf("round = %d, want 2 (the round still advances normally)", got.Round)
		}
		if got.RoundClosedTree != "" {
			t.Fatalf("RoundClosedTree = %q, want empty", got.RoundClosedTree)
		}
	})
}

// TestEffectiveStatus mapped a live pane agent's status through the
// binding's recorded session (#303; closed-list item 6): both the agent list
// and the session cursor are gone.

func TestCloseOnMarkerWithReportClosesNormally(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	b.Halt = "stale"
	b.HaltAt = rt.Now()

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want empty after a round close", got.Halt)
	}
	if !got.HaltAt.IsZero() {
		t.Errorf("HaltAt = %v, want zero after a round close", got.HaltAt)
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "" {
		t.Errorf("note = %q, want empty on a marked close", pending.Note)
	}
	if !strings.Contains(pending.Payload, "The runner finished round 1. Report: relevo show webshop --round 1 --report") {
		t.Errorf("payload = %q", pending.Payload)
	}
}

// TestCloseOnMarkerWithoutReportIsNoreport pins §4.1: the builder said it was
// done, so relevo closes on that and says the report is missing, instead of
// waiting for idle and scraping a worse artefact. Mutation: fall through to
// scrapeReport -> the note is "scraped".
func TestCloseOnMarkerWithoutReportIsNoreport(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	touch(t, rt.Store.DonePath("webshop", 1))

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("entry must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "noreport" {
		t.Errorf("note = %q, want noreport", pending.Note)
	}
	want := "Builder wrote its completion marker for round 1 but wrote no report."
	if !strings.Contains(pending.Payload, want) {
		t.Errorf("payload = %q, want it to contain %q", pending.Payload, want)
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); err == nil {
		t.Error("relevo must not write a report file of its own on a noreport close")
	}
}

func TestCloseOnMarkerAbsentDoesNothing(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if closed || got.Round != 1 {
		t.Fatalf("closed=%v round=%d, want untouched: a report alone is not a close", closed, got.Round)
	}
	after, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("log grew from %d to %d entries; no marker means no I/O", len(before), len(after))
	}
}

// TestReconcileClosesOnMarkerWhileBuilderStillWorking pins §4.2: the marker
// is checked every tick, before the builder is consulted at all. Mutation:
// move the closeOnMarker call behind a live-process check -> this fails
// because the process below is alive (an unscripted pid is alive forever).
func TestReconcileClosesOnMarkerWhileBuilderStillWorking(t *testing.T) {
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
		t.Errorf("round = %d, want 2: the marker closes the round regardless of the process", got.Round)
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found || pending.Note != "" {
		t.Errorf("want a normal report queued: found=%v note=%q err=%v", found, pending.Note, err)
	}
}

// TestReconcileReportWithoutMarkerIsNotAClose pins §4.3: a report on disk is
// not evidence the builder is finished. Mutation: gate on the report instead
// of the marker -> the round advances on the first tick.
func TestReconcileReportWithoutMarkerIsNotAClose(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("round = %d, want 1: a report alone is not a close", got.Round)
	}
	if _, found, _ := rt.Store.PendingForMasterMind("webshop"); found {
		t.Error("nothing may be queued off a report without a marker")
	}
}

// TestReconcileQuiescentWithReportClosesUnmarked, TestReconcileQuiescentWithoutReportStillScrapes,
// TestReconcileScrapedBodyIsNeverTailParsed, TestReconcileQuiescentOnLimitSwitchesInsteadOfScraping
// and TestReconcileQuiescentWithReportOnLimitGatesAndClosesUnmarked all drove
// the pane quiescence / scrape path (#303; closed-list items 1 and 2): relevo
// scrapes no terminal any more, so the report on disk is the only artefact,
// and the headless equivalents live in headless_test.go
// (TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked and
// TestReconcileHeadlessMarkerClosesAndClearsTheHandle).
func TestReconcileReportTailAndOrigin(t *testing.T) {
	t.Parallel()

	t.Run("status halted with halted_at", func(t *testing.T) {
		rt, b := sentBinding(t)
		reportContent := "Some report content\n\n```relevo\nstatus: halted\nhalted_at: \"Task 2 step 3\"\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Outcome != reporttail.OutcomeHalted {
			t.Errorf("Outcome = %q, want %q", report.Outcome, reporttail.OutcomeHalted)
		}
		if report.HaltedAt != "Task 2 step 3" {
			t.Errorf("HaltedAt = %q, want %q", report.HaltedAt, "Task 2 step 3")
		}
		if !strings.Contains(report.Payload, `-- halted at "Task 2 step 3"`) {
			t.Errorf("payload %q does not contain -- halted at \"Task 2 step 3\"", report.Payload)
		}
	})

	t.Run("status done leaves payload first line unchanged apart from origin prefix", func(t *testing.T) {
		rt, b := sentBinding(t)
		reportContent := "Some report content\n\n```relevo\nstatus: done\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Outcome != reporttail.OutcomeDone {
			t.Errorf("Outcome = %q, want %q", report.Outcome, reporttail.OutcomeDone)
		}
		wantOrigin := delivery.OriginLine("webshop", 1, store.DirToMasterMind, store.KindReport)
		lines := strings.Split(report.Payload, "\n")
		if len(lines) < 3 {
			t.Fatalf("unexpected payload lines: %q", report.Payload)
		}
		if lines[0] != wantOrigin {
			t.Errorf("line 0 = %q, want %q", lines[0], wantOrigin)
		}
		if lines[1] != "" {
			t.Errorf("line 1 = %q, want empty line", lines[1])
		}
		wantBodyFirst := "The runner finished round 1. Report: relevo show webshop --round 1 --report"
		if lines[2] != wantBodyFirst {
			t.Errorf("line 2 = %q, want %q", lines[2], wantBodyFirst)
		}
	})

	t.Run("legacy finished prefix is annotated in place", func(t *testing.T) {
		rt, _ := sentBinding(t)
		reportPath := rt.Store.ReportPath("webshop", 1)
		reportContent := "Some report content\n\n```relevo\nstatus: halted\nhalted_at: \"Task 2 step 3\"\n```\n"
		if err := os.WriteFile(reportPath, []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		var payload string
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load("webshop")
			if err != nil {
				return err
			}
			entries, err := tx.ReadLog("webshop")
			if err != nil {
				return err
			}
			legacy := fmt.Sprintf("Builder finished round %d. Report: relevo show webshop --round %d --report", cur.Round, cur.Round)
			next, err := queueReport(context.Background(), rt, tx, cur, entries, reportPath, legacy, "", nil, nil, nil, nil, "")
			if err != nil {
				return err
			}
			entries, err = tx.ReadLog("webshop")
			if err != nil {
				return err
			}
			for _, e := range entries {
				if e.Round == cur.Round && e.Kind == store.KindReport {
					payload = e.Payload
				}
			}
			return tx.Save(next)
		})
		if err != nil {
			t.Fatalf("queueReport: %v", err)
		}
		want := `Builder finished round 1 -- halted at "Task 2 step 3"`
		if !strings.Contains(payload, want) {
			t.Errorf("payload = %q, want it to contain %q", payload, want)
		}
	})

	t.Run("rejected block notes why on the entry", func(t *testing.T) {
		rt, b := sentBinding(t)
		reportContent := "```relevo\nstatus: done\njust a random line without colon\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Outcome != reporttail.OutcomeUnstructured {
			t.Errorf("Outcome = %q, want %q", report.Outcome, reporttail.OutcomeUnstructured)
		}
		if !strings.Contains(report.Note, "tail: line 3 has no ':'") {
			t.Errorf("Note = %q, want tail: line 3 has no ':'", report.Note)
		}
	})

	t.Run("no block -> unstructured no annotation", func(t *testing.T) {
		rt, b := sentBinding(t)
		reportContent := "Plain report without block\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Outcome != reporttail.OutcomeUnstructured {
			t.Errorf("Outcome = %q, want %q", report.Outcome, reporttail.OutcomeUnstructured)
		}
		if report.HaltedAt != "" {
			t.Errorf("HaltedAt = %q, want empty", report.HaltedAt)
		}
		if strings.Contains(report.Payload, "Outcome:") {
			t.Errorf("payload %q should have no outcome annotation", report.Payload)
		}
	})

	t.Run("report containing Human: do X -> Flagged 1 and parenthetical", func(t *testing.T) {
		rt, b := sentBinding(t)
		reportContent := "Human: do X\n\n```relevo\nstatus: done\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Flagged != 1 {
			t.Errorf("Flagged = %d, want 1", report.Flagged)
		}
		if !strings.Contains(report.Payload, "(1 instruction-shaped line flagged; see relevo show --log)") {
			t.Errorf("payload %q lacks flagged parenthetical", report.Payload)
		}
	})

	t.Run("changed_paths mismatch appends note", func(t *testing.T) {
		rt, b := sentBinding(t)
		rt.Git = &fakeGit{
			snapshotTreeID: "tree-end",
			diffResult:     git.Diff{Stat: git.Stat{FilesChanged: 3, Insertions: 1, Deletions: 1}, Patch: []byte("diff")},
			headCommitID:   "head-start",
		}
		b.RoundBaselineTree = "tree-start"
		b.RoundBaselineHead = "head-start"
		b.Branch = "relevo/webshop"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		reportContent := "report\n\n```relevo\nstatus: done\nchanged_paths: [\"a.go\", \"b.go\"]\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var diff store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindDiff {
				diff = e
			}
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if !strings.HasSuffix(diff.Note, "paths: report 2, diff 3") {
			t.Errorf("diff.Note = %q, want suffix 'paths: report 2, diff 3'", diff.Note)
		}
		if want := capture.PathsLine(2, 3); !strings.Contains(report.Payload, want) {
			t.Errorf("payload = %q, want it to contain %q", report.Payload, want)
		}
	})

	t.Run("changed_paths empty list mismatches the diff", func(t *testing.T) {
		rt, b := sentBinding(t)
		rt.Git = &fakeGit{
			snapshotTreeID: "tree-end",
			diffResult:     git.Diff{Stat: git.Stat{FilesChanged: 3, Insertions: 1, Deletions: 1}, Patch: []byte("diff")},
			headCommitID:   "head-start",
		}
		b.RoundBaselineTree = "tree-start"
		b.RoundBaselineHead = "head-start"
		b.Branch = "relevo/webshop"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		reportContent := "report\n\n```relevo\nstatus: done\nchanged_paths: []\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var diff, report store.LogEntry
		for _, e := range entries {
			if e.Round != 1 {
				continue
			}
			switch e.Kind {
			case store.KindDiff:
				diff = e
			case store.KindReport:
				report = e
			}
		}
		if !strings.HasSuffix(diff.Note, "paths: report 0, diff 3") {
			t.Errorf("diff.Note = %q, want suffix 'paths: report 0, diff 3'", diff.Note)
		}
		if want := capture.PathsLine(0, 3); !strings.Contains(report.Payload, want) {
			t.Errorf("payload = %q, want it to contain %q", report.Payload, want)
		}
	})

	t.Run("no changed_paths key has no paths line", func(t *testing.T) {
		rt, b := sentBinding(t)
		rt.Git = &fakeGit{
			snapshotTreeID: "tree-end",
			diffResult:     git.Diff{Stat: git.Stat{FilesChanged: 3, Insertions: 1, Deletions: 1}, Patch: []byte("diff")},
			headCommitID:   "head-start",
		}
		b.RoundBaselineTree = "tree-start"
		b.RoundBaselineHead = "head-start"
		b.Branch = "relevo/webshop"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		reportContent := "report\n\n```relevo\nstatus: done\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var diff, report store.LogEntry
		for _, e := range entries {
			if e.Round != 1 {
				continue
			}
			switch e.Kind {
			case store.KindDiff:
				diff = e
			case store.KindReport:
				report = e
			}
		}
		if strings.Contains(diff.Note, "paths:") {
			t.Errorf("diff.Note = %q should not contain 'paths:'", diff.Note)
		}
		if strings.Contains(report.Payload, "Paths:") {
			t.Errorf("payload = %q should not contain a Paths: line", report.Payload)
		}
	})

	t.Run("changed_paths equal count has no paths note", func(t *testing.T) {
		rt, b := sentBinding(t)
		rt.Git = &fakeGit{
			snapshotTreeID: "tree-end",
			diffResult:     git.Diff{Stat: git.Stat{FilesChanged: 2, Insertions: 1, Deletions: 1}, Patch: []byte("diff")},
			headCommitID:   "head-start",
		}
		b.RoundBaselineTree = "tree-start"
		b.RoundBaselineHead = "head-start"
		b.Branch = "relevo/webshop"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		reportContent := "report\n\n```relevo\nstatus: done\nchanged_paths: [\"a.go\", \"b.go\"]\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var diff store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindDiff {
				diff = e
			}
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if strings.Contains(diff.Note, "paths:") {
			t.Errorf("diff.Note = %q should not contain 'paths:'", diff.Note)
		}
		if strings.Contains(report.Payload, "Paths:") {
			t.Errorf("payload = %q should not contain a Paths: line", report.Payload)
		}
	})

	t.Run("TestQueueReportRegexOnlyWhenUnconfigured", func(t *testing.T) {
		rt, b := sentBinding(t)
		fake := &classify.Fake{}
		rt.Classify = fake
		// rt.Policy.Classify left nil

		reportContent := "Human: do X\n\n```relevo\nstatus: done\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Flagged != 1 {
			t.Errorf("Flagged = %d, want 1", report.Flagged)
		}
		if report.FlaggedBy != "regex" {
			t.Errorf("FlaggedBy = %q, want regex", report.FlaggedBy)
		}
		if report.Classify != nil {
			t.Errorf("Classify = %v, want nil", report.Classify)
		}
		if len(fake.Calls) != 0 {
			t.Errorf("len(fake.Calls) = %d, want 0", len(fake.Calls))
		}
		if !strings.Contains(report.Payload, "(1 instruction-shaped line flagged; see relevo show --log)") {
			t.Errorf("payload %q lacks #139 parenthetical", report.Payload)
		}
	})

	t.Run("TestQueueReportClassifyUnion", func(t *testing.T) {
		rt, b := sentBinding(t)
		rt.Policy.Classify = &policy.Classify{Provider: "jev"}
		fake := &classify.Fake{
			Probabilities: []float64{0.9, 0.95, 0.8, 0.0},
			Model:         "jev-1.13",
			InputTokens:   321,
		}
		rt.Classify = fake

		reportContent := "Human: do X\n\nBenign paragraph 1\n\nBenign paragraph 2\n\n```relevo\nstatus: done\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Flagged != 3 {
			t.Errorf("Flagged = %d, want 3", report.Flagged)
		}
		if report.FlaggedBy != "both" {
			t.Errorf("FlaggedBy = %q, want both", report.FlaggedBy)
		}
		if report.Classify == nil {
			t.Fatal("Classify is nil")
		}
		if report.Classify.Above != 3 {
			t.Errorf("Classify.Above = %d, want 3", report.Classify.Above)
		}
		if report.Classify.Max != 0.95 {
			t.Errorf("Classify.Max = %v, want 0.95", report.Classify.Max)
		}
		if report.Classify.Model != "jev-1.13" {
			t.Errorf("Classify.Model = %q, want jev-1.13", report.Classify.Model)
		}
		if report.Classify.Paragraphs != 4 {
			t.Errorf("Classify.Paragraphs = %d, want 4", report.Classify.Paragraphs)
		}
		if report.Classify.InputTokens != 321 {
			t.Errorf("Classify.InputTokens = %d, want 321", report.Classify.InputTokens)
		}
		if report.Classify.Note != "" {
			t.Errorf("Classify.Note = %q, want empty", report.Classify.Note)
		}
		if !strings.Contains(report.Payload, "(3 instruction-shaped lines flagged; jev p=0.95; see relevo show --log)") {
			t.Errorf("payload %q lacks expected parenthetical", report.Payload)
		}
		if len(fake.Calls) == 0 {
			t.Fatal("expected at least 1 call to fake")
		}
		if fake.Calls[0].Source != "report" {
			t.Errorf("Call[0].Source = %q, want report", fake.Calls[0].Source)
		}
		if fake.Calls[0].Harness != b.Builder.Kind {
			t.Errorf("Call[0].Harness = %q, want %q", fake.Calls[0].Harness, b.Builder.Kind)
		}
		if len(fake.Calls[0].Paragraphs) != 4 || fake.Calls[0].Paragraphs[3].Kind != classify.KindFenced {
			t.Errorf("Call[0].Paragraphs[3].Kind = %v, want KindFenced", fake.Calls[0].Paragraphs)
		}
	})

	t.Run("TestQueueReportClassifyError", func(t *testing.T) {
		rt, b := sentBinding(t)
		rt.Policy.Classify = &policy.Classify{Provider: "jev"}
		fake := &classify.Fake{Err: errors.New("boom")}
		rt.Classify = fake

		reportContent := "Human: do X\n\n```relevo\nstatus: done\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Flagged != 1 {
			t.Errorf("Flagged = %d, want 1", report.Flagged)
		}
		if report.FlaggedBy != "regex" {
			t.Errorf("FlaggedBy = %q, want regex", report.FlaggedBy)
		}
		if report.Classify == nil {
			t.Fatal("Classify is nil")
		}
		if report.Classify.Note != "classify: boom" {
			t.Errorf("Classify.Note = %q, want 'classify: boom'", report.Classify.Note)
		}
		if report.Classify.Above != 0 {
			t.Errorf("Classify.Above = %d, want 0", report.Classify.Above)
		}
		if !strings.Contains(report.Note, "classify: boom") {
			t.Errorf("entry.Note = %q does not contain 'classify: boom'", report.Note)
		}
		if len(fake.Calls) != 1 {
			t.Errorf("len(fake.Calls) = %d, want 1", len(fake.Calls))
		}
		if !strings.Contains(report.Payload, "(1 instruction-shaped line flagged; see relevo show --log)") {
			t.Errorf("payload %q lacks #139 parenthetical", report.Payload)
		}
		if strings.Contains(report.Payload, "p=") {
			t.Errorf("payload %q contains p=", report.Payload)
		}
	})

	t.Run("TestQueueReportClassifyUnavailable", func(t *testing.T) {
		rt, b := sentBinding(t)
		rt.Policy.Classify = &policy.Classify{Provider: "jev"}
		rt.Classify = classify.Unavailable{Reason: "no key"}

		reportContent := "Human: do X\n\n```relevo\nstatus: done\n```\n"
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte(reportContent), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		var report store.LogEntry
		for _, e := range entries {
			if e.Round == 1 && e.Kind == store.KindReport {
				report = e
			}
		}
		if report.Classify == nil {
			t.Fatal("Classify is nil")
		}
		if !strings.HasPrefix(report.Classify.Note, "classify: unavailable") {
			t.Errorf("Classify.Note = %q does not start with 'classify: unavailable'", report.Classify.Note)
		}
		if !strings.Contains(report.Note, "classify: unavailable") {
			t.Errorf("entry.Note = %q does not contain 'classify: unavailable'", report.Note)
		}
	})

}

// anySpecArgv reports whether any process the runner started was handed argv
// naming path.
func anySpecArgv(fr *fakeRunner, path string) bool {
	for _, sp := range fr.specs {
		if strings.Contains(strings.Join(sp.Argv, " "), path) {
			return true
		}
	}
	return false
}

// gates returns the gate entries in webshop's log.
func gates(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindGate {
			out = append(out, e)
		}
	}
	return out
}

// TestGateNotConfiguredIsUnchanged pins #132: a binding with no gate closes
// exactly as it did before the gate existed -- no process is started and the
// payload is unchanged.
func TestGateNotConfiguredIsUnchanged(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if gating {
		t.Fatal("gating = true with no gate configured")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if len(fr.specs) != 0 {
		t.Fatalf("Start calls = %d, want 0 with no gate configured", len(fr.specs))
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "" {
		t.Errorf("note = %q, want empty", pending.Note)
	}
	want := "The runner finished round 1. Report: relevo show webshop --round 1 --report"
	if !strings.HasSuffix(pending.Payload, want) {
		t.Errorf("payload = %q, want it to end with %q (unchanged by the gate)", pending.Payload, want)
	}
	if strings.Contains(pending.Payload, "Gate:") {
		t.Errorf("payload = %q, want no gate line with no gate configured", pending.Payload)
	}
}

// TestGateStartsOnMarkerAndHoldsTheRound pins #132's state machine: a
// configured gate starts on the marker tick and holds the round across
// ticks -- no exit/switch handling, no second process -- until it finishes.
//
// Mutation check (run and report): make the close path ignore gating (fall
// through to the marker close instead of returning early); this test fails
// because the second tick's round has advanced and a second process started.
func TestGateStartsOnMarkerAndHoldsTheRound(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("Round = %d, want 1: the gate holds the round open", got.Round)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("Start calls = %d, want exactly 1", len(fr.specs))
	}
	spec := fr.specs[0]
	wantLog := rt.Store.GateLogPath("webshop", 1)
	if spec.Dir != b.CWD {
		t.Errorf("spec.Dir = %q, want %q", spec.Dir, b.CWD)
	}
	wantArgv := []string{"sh", "-c", "make check 2>&1"}
	if len(spec.Argv) != len(wantArgv) {
		t.Fatalf("spec.Argv = %v, want %v", spec.Argv, wantArgv)
	}
	for i := range wantArgv {
		if spec.Argv[i] != wantArgv[i] {
			t.Fatalf("spec.Argv = %v, want %v", spec.Argv, wantArgv)
		}
	}
	if spec.LogPath != wantLog || spec.StreamPath != wantLog {
		t.Errorf("LogPath/StreamPath = %q/%q, want both %q", spec.LogPath, spec.StreamPath, wantLog)
	}
	if len(fr.handles) != 1 {
		t.Fatalf("handles = %d, want 1", len(fr.handles))
	}
	if got.GateRun == nil || got.GateRun.PID != fr.handles[0].PID {
		t.Fatalf("GateRun = %+v, want PID %d", got.GateRun, fr.handles[0].PID)
	}
	if len(gates(t, rt)) != 1 {
		t.Fatalf("KindGate entries = %d, want 1", len(gates(t, rt)))
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("no report entry while the gate is running: %+v", pending)
	}

	// Second tick: the gate is still alive. Gating must hold the round
	// untouched. fakeRunner.Start stamps StartedAt from a real epoch
	// unrelated to the fake clock, so it is realigned here -- otherwise the
	// elapsed-since-start arithmetic in gateStep would see it as already far
	// past any timeout.
	got.GateRun.StartedAt = clock.Now().Unix()
	fr.script(fr.handles[0].PID, true)
	clock.Advance(31 * time.Second)
	got2, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got2.Round != 1 {
		t.Fatalf("Round = %d after second tick, want 1", got2.Round)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("Start calls after second tick = %d, want still 1", len(fr.specs))
	}
	if len(exits(t, rt)) != 0 {
		t.Errorf("no exit handling while the gate runs: %+v", exits(t, rt))
	}
	if len(switches(t, rt)) != 0 {
		t.Errorf("no switch while the gate runs: %+v", switches(t, rt))
	}
}

// TestGatePassClosesWithAnnotation pins #132: a gate that exits 0 closes the
// round with a gate=pass annotation, a Gate record on the entry, and the
// gate's payload line.
func TestGatePassClosesWithAnnotation(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	// First tick starts the gate.
	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if closed || !gating {
		t.Fatalf("closed=%v gating=%v after starting the gate, want gating only", closed, gating)
	}

	// The gate exits 0.
	fr.script(fr.handles[0].PID, false)
	fr.exit(fr.handles[0].PID, 0)

	got, closed, gating = closeOnMarkerUnderLockGating(t, rt, got)
	if gating {
		t.Fatal("gating = true after the gate exited")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if got.GateRun != nil {
		t.Errorf("GateRun = %+v, want nil after the round closes", got.GateRun)
	}

	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=pass" {
		t.Errorf("note = %q, want gate=pass", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "pass" || pending.Gate.ExitCode != 0 {
		t.Fatalf("Gate = %+v, want Result=pass ExitCode=0", pending.Gate)
	}
	wantRef := "relevo show webshop --round 1 --gate"
	if !strings.Contains(pending.Payload, "Gate: make check -- PASS (exit 0,") ||
		!strings.Contains(pending.Payload, wantRef) {
		t.Errorf("payload = %q", pending.Payload)
	}
}

// TestGateFailAddsTail pins #132: a gate that exits non-zero closes the
// round with gate=fail, and the payload carries the log's last
// gateTailLines non-empty lines, not the first.
func TestGateFailAddsTail(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, _, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if !gating {
		t.Fatal("want gating after starting the gate")
	}

	gateLog := rt.Store.GateLogPath("webshop", 1)
	body := "line one\nline two\nline three\nline four\nline five\nline six\n"
	if err := os.WriteFile(gateLog, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	fr.script(fr.handles[0].PID, false)
	fr.exit(fr.handles[0].PID, 2)

	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, got)
	if gating {
		t.Fatal("gating = true after the gate exited")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}

	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=fail" {
		t.Errorf("note = %q, want gate=fail", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "fail" || pending.Gate.ExitCode != 2 {
		t.Fatalf("Gate = %+v, want Result=fail ExitCode=2", pending.Gate)
	}
	if !strings.Contains(pending.Payload, "FAIL (exit 2,") {
		t.Errorf("payload = %q, want it to contain FAIL (exit 2,", pending.Payload)
	}
	for _, want := range []string{"line two", "line three", "line four", "line five", "line six"} {
		if !strings.Contains(pending.Payload, want) {
			t.Errorf("payload missing tail line %q: %q", want, pending.Payload)
		}
	}
	if strings.Contains(pending.Payload, "line one") {
		t.Errorf("payload must carry only the last 5 lines, not the first: %q", pending.Payload)
	}
}

// TestGateTimeoutKills pins #132: a gate that outlives its timeout is
// killed and the round closes with gate=timeout.
func TestGateTimeoutKills(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, _, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if !gating {
		t.Fatal("want gating after starting the gate")
	}
	// fakeRunner.Start stamps StartedAt from a real epoch unrelated to the
	// fake clock, so it is realigned here -- otherwise the elapsed-since-start
	// arithmetic in gateStep would see it as already far past any timeout.
	got.GateRun.StartedAt = clock.Now().Unix()

	fr.script(fr.handles[0].PID, true)
	clock.Advance(rt.Policy.GateTimeout() - time.Second)
	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, got)
	if closed || !gating {
		t.Fatalf("closed=%v gating=%v just under the timeout, want still gating", closed, gating)
	}
	if len(fr.kills) != 0 {
		t.Fatalf("kills = %+v before the timeout elapsed, want none", fr.kills)
	}

	fr.script(fr.handles[0].PID, true)
	clock.Advance(2 * time.Second)
	got, closed, gating = closeOnMarkerUnderLockGating(t, rt, got)
	if gating {
		t.Fatal("gating = true after the timeout")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != fr.handles[0].PID {
		t.Fatalf("kills = %+v, want exactly one kill of pid %d", fr.kills, fr.handles[0].PID)
	}

	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=timeout" {
		t.Errorf("note = %q, want gate=timeout", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "timeout" {
		t.Fatalf("Gate = %+v, want Result=timeout", pending.Gate)
	}
	if !strings.Contains(pending.Payload, "TIMEOUT after") {
		t.Errorf("payload = %q, want it to contain TIMEOUT after", pending.Payload)
	}
}

// TestGateNoRunnerIsErrorNotHang pins #132: a gate configured on a runtime
// with no Runner cannot hang the round -- it closes this tick with a
// gate=error annotation instead.
func TestGateNoRunnerIsErrorNotHang(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Runner = nil
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if gating {
		t.Fatal("gating = true with no runner; want an immediate error result")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}

	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=error" {
		t.Errorf("note = %q, want gate=error", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "error" || pending.Gate.Note != "no runner" {
		t.Fatalf("Gate = %+v, want Result=error Note=\"no runner\"", pending.Gate)
	}
	if !strings.Contains(pending.Payload, "Gate: make check -- ERROR: no runner.") {
		t.Errorf("payload = %q", pending.Payload)
	}
}

// gateRecordFor finds a binding's report entry for one round and returns its
// gate record, failing the test when the round has no report entry. It is how
// the regate tests inspect a round that closed earlier than the one currently
// in flight: PendingForMasterMind only ever hands back the oldest.
func gateRecordFor(t *testing.T, rt Runtime, name string, round int) *store.GateRecord {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%s): %v", name, err)
	}
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToMasterMind && e.Kind == store.KindReport {
			return e.Gate
		}
	}
	t.Fatalf("no report entry for round %d of %s", round, name)
	return nil
}

// failRoundWithGate drives b's current round through the real Reconcile call
// site until its gate exits non-zero: the first tick starts the gate, the
// second (after the fake runner is told the process exited) closes the round
// with gate=fail. It returns the binding Reconcile returned and the failing
// record, and fails the test if the round did not close on the gate.
func failRoundWithGate(t *testing.T, rt Runtime, b store.Binding, fr *fakeRunner, logBody string) (store.Binding, *store.GateRecord) {
	t.Helper()
	round := b.Round
	if err := os.WriteFile(rt.Store.ReportPath(b.Name, round), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath(b.Name, round))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("reconcile (start gate, round %d): %v", round, err)
	}
	if got.GateRun == nil {
		t.Fatalf("round %d: GateRun is nil; the gate did not start", round)
	}
	pid := got.GateRun.PID
	if err := os.WriteFile(rt.Store.GateLogPath(b.Name, round), []byte(logBody), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.script(pid, false)
	fr.exit(pid, 2)

	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("reconcile (close gate, round %d): %v", round, err)
	}
	rec := gateRecordFor(t, rt, b.Name, round)
	if rec == nil || rec.Result != "fail" {
		t.Fatalf("round %d: report gate = %+v, want a fail record", round, rec)
	}
	return got, rec
}

// TestRegateFailOpensRepairRound pins #132 part 2: a failing gate with a
// budget stages round N+1 as a repair plan, hands it to the builder exactly as
// Send would, and logs `repair k/M` on the new round's plan entry.
func TestRegateFailOpensRepairRound(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, _ := failRoundWithGate(t, rt, b, fr, "FAIL github.com/example/pkg2\n")

	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2", got.Round)
	}
	planPath := rt.Store.PromptPath("webshop", 2)
	body, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("round 2 plan must exist: %v", err)
	}
	if !strings.Contains(string(body), "Round 1's acceptance check") {
		t.Errorf("round 2 plan does not name the failed check:\n%s", body)
	}

	if !anySpecArgv(fr, "002-prompt.md") {
		t.Errorf("no process was handed round 2's repair plan (002-prompt.md)")
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	var repairEntry *store.LogEntry
	for i := range entries {
		if entries[i].Round == 2 && entries[i].Direction == store.DirToBuilder && entries[i].Kind == store.KindPrompt {
			repairEntry = &entries[i]
		}
	}
	if repairEntry == nil {
		t.Fatal("no round-2 plan entry in the log: the round does not count as open")
	}
	if repairEntry.Note != "repair 1/2" {
		t.Errorf("plan entry note = %q, want repair 1/2", repairEntry.Note)
	}

	if got.RepairCount != 1 {
		t.Errorf("RepairCount = %d, want 1", got.RepairCount)
	}
	if got.LastGateSig == "" {
		t.Error("LastGateSig must carry the failing gate's signature")
	}
	if got.RoundStartedAt.IsZero() {
		t.Error("RoundStartedAt must be stamped: the repair round is open")
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want active", got.State)
	}
	if rec := gateRecordFor(t, rt, "webshop", 1); rec == nil || rec.Result != "fail" {
		t.Errorf("round 1 report gate = %+v, want the fail it closed with", rec)
	}
}

// TestRepairRoundRefusesASymlinkedPlanFile pins that a repair round does not
// follow a symlink planted at its plan path: the round was never opened, the
// binding halts NEEDS YOU with the refusal named in Halt, and the failing
// round's own gate=fail record stands.
func TestRepairRoundRefusesASymlinkedPlanFile(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	planPath := rt.Store.PromptPath("webshop", 2)
	if err := os.Symlink(sentinel, planPath); err != nil {
		t.Fatal(err)
	}

	got, rec := failRoundWithGate(t, rt, b, fr, "FAIL github.com/example/pkg2\n")
	if rec == nil || rec.Result != "fail" {
		t.Fatalf("round 1 record = %+v, want the fail it closed with", rec)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2", got.Round)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want NEEDS YOU", got.State)
	}
	if !strings.Contains(got.Halt, "could not stage its plan") {
		t.Errorf("Halt = %q, want it to name the refused plan", got.Halt)
	}

	body, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("ReadFile(sentinel): %v", err)
	}
	if string(body) != "sentinel" {
		t.Errorf("sentinel = %q, want it byte-identical", body)
	}
	fi, err := os.Lstat(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("plan path is no longer a symlink: mode = %v", fi.Mode())
	}
}

// TestRegateBoundHaltsNeedsYou pins the count bound (#132 part 2): once the
// budget is spent, the next failing gate ends the loop with NEEDS YOU instead
// of another repair round.
func TestRegateBoundHaltsNeedsYou(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	b.Regate = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, _ := failRoundWithGate(t, rt, b, fr, "FAIL github.com/example/pkg2\n")
	if got.State != store.StateActive || got.RepairCount != 1 {
		t.Fatalf("after the first failure: state=%q repairs=%d, want active/1", got.State, got.RepairCount)
	}
	if !anySpecArgv(fr, "002-prompt.md") {
		t.Fatalf("no process was handed round 2's repair plan (002-prompt.md)")
	}

	// Round 2's gate fails with different content, so the stall bound cannot
	// fire: the count bound must.
	got, _ = failRoundWithGate(t, rt, got, fr, "FAIL github.com/example/other-pkg\n")

	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "after 1 repair") {
		t.Errorf("Halt = %q, want it to mention \"after 1 repair\"", got.Halt)
	}
	if _, err := os.Stat(rt.Store.PromptPath("webshop", 3)); err == nil {
		t.Error("no round-3 plan may be staged once the budget is spent")
	}
	if anySpecArgv(fr, "003-prompt.md") {
		t.Error("no round-3 hand-off may happen once the budget is spent")
	}
}

// TestRegateIdenticalSignatureHaltsEarly pins the stall bound (#132 part 2):
// a second identical failure -- same content modulo the clock -- means the
// repair changed nothing that mattered, so the loop ends early.
func TestRegateIdenticalSignatureHaltsEarly(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	b.Regate = 5
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, _ := failRoundWithGate(t, rt, b, fr,
		"2026-09-21T14:29:00Z FAIL github.com/example/pkg2 0.02s\n")
	if got.State != store.StateActive {
		t.Fatalf("after the first failure: state = %q, want active (a repair started)", got.State)
	}

	got, _ = failRoundWithGate(t, rt, got, fr,
		"2026-09-21T14:31:07Z FAIL github.com/example/pkg2 9.99s\n")

	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "unchanged after repair") {
		t.Errorf("Halt = %q, want it to mention the unchanged gate output", got.Halt)
	}
	if got.RepairCount != 1 {
		t.Errorf("RepairCount = %d, want 1 (the early halt is not a repair)", got.RepairCount)
	}
}

// TestRegatePassResetsCount pins #132 part 2's reset: a passing gate clears
// the repair bookkeeping, so the next failing gate gets a fresh budget.
func TestRegatePassResetsCount(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	b.Regate = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, _ := failRoundWithGate(t, rt, b, fr, "FAIL github.com/example/pkg2\n")
	if got.RepairCount != 1 || got.LastGateSig == "" {
		t.Fatalf("after the first failure: repairs=%d sig=%q, want 1 and non-empty", got.RepairCount, got.LastGateSig)
	}

	// Round 2's gate passes.
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 2), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 2))

	got, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("reconcile (start gate, round 2): %v", err)
	}
	if got.GateRun == nil {
		t.Fatal("round 2: GateRun is nil; the gate did not start")
	}
	pid := got.GateRun.PID
	if err := os.WriteFile(rt.Store.GateLogPath("webshop", 2), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.script(pid, false)
	fr.exit(pid, 0)

	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("reconcile (close gate, round 2): %v", err)
	}

	if rec := gateRecordFor(t, rt, "webshop", 2); rec == nil || rec.Result != "pass" {
		t.Fatalf("round 2 report gate = %+v, want pass", rec)
	}
	if got.RepairCount != 0 {
		t.Errorf("RepairCount = %d, want 0 after a passing gate", got.RepairCount)
	}
	if got.LastGateSig != "" {
		t.Errorf("LastGateSig = %q, want \"\" after a passing gate", got.LastGateSig)
	}
	if got.Round != 3 {
		t.Errorf("Round = %d, want 3", got.Round)
	}
}

// TestNoRegateUnchanged pins the off switch (#132 part 2): Regate 0 is
// exactly today's behaviour -- the failure is reported, nothing is re-sent.
func TestNoRegateUnchanged(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, _ := failRoundWithGate(t, rt, b, fr, "FAIL github.com/example/pkg2\n")

	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2", got.Round)
	}
	if got.RepairCount != 0 || got.LastGateSig != "" {
		t.Errorf("repair bookkeeping moved with regate 0: repairs=%d sig=%q", got.RepairCount, got.LastGateSig)
	}
	if _, err := os.Stat(rt.Store.PromptPath("webshop", 2)); err == nil {
		t.Error("no round-2 plan may be staged when regate is 0")
	}
	if got := len(runnerOf(t, rt).specs); got != 1 {
		t.Errorf("processes started = %d, want 1 (the gate only, nothing re-sent)", got)
	}

	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "gate=fail" {
		t.Errorf("note = %q, want gate=fail", pending.Note)
	}
	if pending.Gate == nil || pending.Gate.Result != "fail" || pending.Gate.ExitCode != 2 {
		t.Fatalf("Gate = %+v, want Result=fail ExitCode=2", pending.Gate)
	}
}

// TestSendResetsRepairBookkeeping pins #132 part 2: a human send is a fresh
// start -- it clears the repair bookkeeping and, when --regate is given, sets
// the binding's budget.
func TestSendResetsRepairBookkeeping(t *testing.T) {
	t.Parallel()

	rt, b := seedBound(t)
	b.RepairCount = 1
	b.LastGateSig = "x"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepairCount != 0 || got.LastGateSig != "" {
		t.Errorf("a human send must clear the repair bookkeeping: repairs=%d sig=%q", got.RepairCount, got.LastGateSig)
	}
	if got.Regate != 0 {
		t.Errorf("Regate = %d, want 0 (unchanged without --regate)", got.Regate)
	}
	endProcess(t, rt, got)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Regate: ptr(3)}); err != nil {
		t.Fatalf("Send --regate: %v", err)
	}
	got, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if got.Regate != 3 {
		t.Errorf("Regate = %d, want 3 persisted by send --regate", got.Regate)
	}
}

// TestReportEntryCarriesPaneSession and TestReportEntryNoSessionIsNil named
// the session the pane client reported for a pane builder (#303; closed-list items 6
// and 8). The headless equivalent -- the report entry of a closed headless
// round names the stream's session -- is TestReportEntryCarriesHeadlessSession
// in headless_test.go.

// TestReconcileNeedsYouGoesStale pins #135's stale path: a binding that has
// been NEEDS YOU past stale_after_ms is stamped from its halt time and fires
// one binding_stale event; a second tick adds neither. The notification
// beside the event is gone (#303, closed-list item 4), so the event itself is
// what this test now pins. A human Send clears the stamp and the notification
// bookkeeping.
func TestReconcileNeedsYouGoesStale(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	haltedAt := baseTime.Add(-5 * time.Hour)
	b.State = store.StateNeedsYou
	b.Halt = "builder blocked at a dialog"
	b.HaltAt = haltedAt
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !got.StaleSince.Equal(haltedAt) {
		t.Fatalf("StaleSince = %s, want the halt time %s", got.StaleSince, haltedAt)
	}

	var stale int
	for _, e := range disp.getEvents() {
		if e.Type == hooks.EventBindingStale {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("got %d binding_stale events, want 1: %+v", stale, disp.getEvents())
	}

	// A second tick is not a second episode.
	got2, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile (second tick): %v", err)
	}
	if !got2.StaleSince.Equal(haltedAt) {
		t.Errorf("StaleSince after a second tick = %s, want it unchanged at %s", got2.StaleSince, haltedAt)
	}
	var stale2 int
	for _, e := range disp.getEvents() {
		if e.Type == hooks.EventBindingStale {
			stale2++
		}
	}
	if stale2 != 1 {
		t.Errorf("got %d binding_stale events after a second tick, want 1", stale2)
	}

	// A human send is a fresh attempt: the stale stamp and its notification
	// bookkeeping are gone.
	cur, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	endProcess(t, rt, cur)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.StaleSince.IsZero() {
		t.Errorf("StaleSince = %s after Send, want zero", loaded.StaleSince)
	}
	if !loaded.StaleNotifiedAt.IsZero() {
		t.Errorf("StaleNotifiedAt = %s after Send, want zero", loaded.StaleNotifiedAt)
	}
}

// TestVerifyRoundStartsAReviewerInAThrowawayWorktree pins #144's close path:
// a round sent with --verify closes exactly as before, and the close then
// creates a detached worktree at the builder's HEAD and launches one read-only
// headless reviewer in it -- with a question carried in the reviewer's prompt
// and recorded at the ask path, that asks the reviewer to verify round 1 with
// no gate.
//
// Mutation check (run and report): delete the wantVerify block from
// Reconcile's close path and this fails on addDetachedWorktreeCalls.
func TestVerifyRoundStartsAReviewerInAThrowawayWorktree(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	fg := &fakeGit{headCommitID: "head1"}
	rt, _ := seedBound(t)
	rt.Runner = fr
	rt.Git = fg
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Policy.Order = map[string][]string{"reviewer": {testClaudeRef}}
	rt.NewID = func() string { return verifyConsultID }

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Verify: ptr(true)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !b.RoundVerify {
		t.Fatal("RoundVerify = false after Send{Verify: true}")
	}

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.Round != 2 {
		t.Fatalf("round = %d, want 2: the round closed", got.Round)
	}
	if got.RoundVerify {
		t.Errorf("RoundVerify = true after the close, want cleared")
	}

	wantWT := rt.Store.VerifyWorktreePath("webshop", 1)
	if len(fg.addDetachedWorktreeCalls) != 1 {
		t.Fatalf("AddDetachedWorktree calls = %v, want exactly 1", fg.addDetachedWorktreeCalls)
	}
	if call := fg.addDetachedWorktreeCalls[0]; call.Dir != b.CWD || call.Path != wantWT || call.Commit != "head1" {
		t.Errorf("AddDetachedWorktree = %+v, want {%s %s head1}", call, b.CWD, wantWT)
	}

	if len(fr.specs) != 2 {
		t.Fatalf("Start calls = %d, want 2 (the round and the reviewer)", len(fr.specs))
	}
	reviewer := fr.specs[1]
	if reviewer.Dir != wantWT {
		t.Errorf("reviewer Dir = %q, want the throwaway worktree %q", reviewer.Dir, wantWT)
	}

	askPath := rt.Store.AskPath("webshop", 1, verifyConsultID)
	if argv := strings.Join(reviewer.Argv, " "); !strings.Contains(argv, "Verify round 1") {
		t.Errorf("reviewer argv does not carry the question:\n%s", argv)
	}
	if _, err := os.Stat(askPath); !os.IsNotExist(err) {
		t.Errorf("ask file exists on disk at %s (err %v), want no file", askPath, err)
	}
	question, err := rt.Store.ReadFile(askPath)
	if err != nil {
		t.Fatalf("read recorded question: %v", err)
	}
	for _, want := range []string{"Verify round 1", "Gate:   none"} {
		if !strings.Contains(string(question), want) {
			t.Errorf("recorded question does not contain %q:\n%s", want, question)
		}
	}

	var vc *store.Consult
	for i := range got.Consults {
		if got.Consults[i].Role == consult.VerifyRole {
			vc = &got.Consults[i]
		}
	}
	if vc == nil {
		t.Fatalf("no %q consult on the binding: %+v", consult.VerifyRole, got.Consults)
	}
	if vc.Round != 1 {
		t.Errorf("consult round = %d, want 1", vc.Round)
	}
	if vc.State != store.ConsultRunning {
		t.Errorf("consult state = %q, want running", vc.State)
	}

	// The report was queued for the mastermind: with no live claim it waits for
	// `relevo wait`, which is the whole delivery route since #303.
	if pending, found, err := rt.Store.PendingForMasterMind("webshop"); err != nil {
		t.Fatal(err)
	} else if !found {
		t.Error("the closed round's report must be queued for the mastermind")
	} else if pending.Kind != store.KindReport {
		t.Errorf("pending kind = %q, want report", pending.Kind)
	}
}

// TestVerifyGateLogIsPassed pins #144's gate handoff: the reviewer is told
// where the closed round's gate log is, because seeing the gate's own output
// is the point of running verify after the gate.
func TestVerifyGateLogIsPassed(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	rt.Git = &fakeGit{headCommitID: "head1"}
	rt.NewID = func() string { return verifyConsultID }

	b.RoundVerify = true
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	// First tick starts the gate and holds the round open.
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("round = %d, want 1 while the gate runs", got.Round)
	}

	// The gate exits 0; the next tick closes the round and starts the reviewer.
	fr.script(fr.handles[0].PID, false)
	fr.exit(fr.handles[0].PID, 0)
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2 after the gate passed", got.Round)
	}

	question, err := rt.Store.ReadFile(rt.Store.AskPath("webshop", 1, verifyConsultID))
	if err != nil {
		t.Fatalf("read recorded question: %v", err)
	}
	wantLog := rt.Store.GateLogPath("webshop", 1)
	if !strings.Contains(string(question), "Gate:   "+wantLog) {
		t.Errorf("ask file does not name the gate log %s:\n%s", wantLog, question)
	}
}

// candidateSetWithoutReviewerJSON serves builder and nothing else, so
// resolveCandidate refuses every reviewer.
const candidateSetWithoutReviewerJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"]}
]`

// TestVerifySkippedWhenNoReviewerCandidate pins #144's error handling: a
// round with no reviewer candidate closes normally, logs one "verify skipped:"
// note, starts nothing, and leaves no throwaway worktree behind.
func TestVerifySkippedWhenNoReviewerCandidate(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentBinding(t)
	rt.Runner = fr
	fg := &fakeGit{headCommitID: "head1"}
	rt.Git = fg
	rt.Candidates = candidateSet(t, candidateSetWithoutReviewerJSON)

	b.RoundVerify = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2: a missing reviewer is not a round failure", got.Round)
	}
	if len(fr.specs) != 0 {
		t.Errorf("Start calls = %d, want none without a reviewer candidate", len(fr.specs))
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	note := ""
	for _, e := range entries {
		if strings.HasPrefix(e.Note, "verify skipped:") {
			note = e.Note
		}
	}
	if note == "" {
		t.Fatalf("no \"verify skipped:\" note in the log: %+v", entries)
	}

	// Whatever the order, no throwaway worktree is left behind.
	wantWT := rt.Store.VerifyWorktreePath("webshop", 1)
	if len(fg.addDetachedWorktreeCalls) > 0 {
		removed := false
		for _, c := range fg.removeWorktreeCalls {
			if c.Path == wantWT && c.Force {
				removed = true
			}
		}
		if !removed {
			t.Errorf("worktree %s was created but never removed (removals: %+v)", wantWT, fg.removeWorktreeCalls)
		}
	}
}

// roundReportEntry is webshop's queued report entry for round. It is the
// non-e2e form of e2e_test.go's reportEntry, which is behind the e2e tag.
func roundReportEntry(t *testing.T, rt Runtime, round int) store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToMasterMind && e.Kind == store.KindReport {
			return e
		}
	}
	t.Fatalf("no report entry for round %d", round)
	return store.LogEntry{}
}

// TestReconcileLeavesAnUnknownStateAlone pins #372 R1's unknown-state rule: a
// state this relevo does not define was written by a newer relevo, so Reconcile
// returns the binding unchanged and drives nothing -- even a written report
// and done marker are left for the newer relevo.
func TestReconcileLeavesAnUnknownStateAlone(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	fr := rt.Runner.(*fakeRunner)
	if err := os.WriteFile(rt.Store.ReportPath(b.Name, 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath(b.Name, 1))
	started, killed := len(fr.specs), len(fr.kills)

	b.State = store.State("frozen")
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !store.SameBinding(got, b) {
		t.Errorf("Reconcile changed an unknown-state binding:\n got %+v\nwant %+v", got, b)
	}
	if len(fr.specs) != started || len(fr.kills) != killed {
		t.Errorf("Reconcile called the runner for an unknown-state binding: specs %d->%d, kills %d->%d",
			started, len(fr.specs), killed, len(fr.kills))
	}
}
