package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// closeFactsEnv places one round on a stub row so the helper has a binding,
// its owner store and a log to write into. Nothing dials: the helper works
// on a fabricated view.
func closeFactsEnv(t *testing.T, clock time.Time) (*testEnv, store.Forward) {
	t.Helper()
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	row := stubRow(t, "b1", closedPortURL(t), burstTestFingerprint)
	return env, queueForward(t, env, "api", row, clock)
}

// fullFactsFetch is the node's shipped view with every field the wire
// carries set, plus the report and diff bodies the close fetched.
func fullFactsFetch() *observeFetch {
	return &observeFetch{
		view: remote.BindingView{
			Name: "nb-api", Round: 2, RoundState: remote.RoundClosed, ClosedRound: 1,
			ReportOutcome: "done",
			ReportNote:    "the runner finished round 1",
			GateResult:    "pass",
			DiffNote:      "2 commits, 3 files",
			DiffCommits:   2,
			DiffTree:      "clean",
			Switches: []string{
				"switched to agy/test/m: rate-limited",
				// A nudge prompt's own entry note, and an empty one: the
				// writer and the reader both exclude them from a round's
				// switch history.
				store.NudgeNote,
				"",
				"switched to claude/anthropic/sonnet: gated",
			},
			Usage:       &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.5, Basis: usage.Measured}},
			Rusage:      &store.Rusage{CPUMS: 42000, PeakMemBytes: 1 << 20},
			PriorTokens: &usage.Tokens{In: 7, Out: 3},
		},
		report: []byte("# Report\nDone.\n"),
		diff:   []byte("diff body"),
	}
}

func countKind(entries []store.LogEntry, round int, kind store.Kind) int {
	var n int
	for _, e := range entries {
		if e.Round == round && e.Kind == kind {
			n++
		}
	}
	return n
}

// TestForwardedCloseEntriesCarryTheNodeViewFacts pins the report, diff and
// switch entries the head close writes: every field lands verbatim from the
// view, the nudged and empty switch notes are skipped, and the report entry
// is left unconfirmed so the owner daemon delivers it on the standard path.
func TestForwardedCloseEntriesCarryTheNodeViewFacts(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, fwd := closeFactsEnv(t, clock)
	rt := env.runtime(t)
	fetch := fullFactsFetch()

	if !appendForwardedCloseFacts(rt, "api", fwd, fetch, clock) {
		t.Fatal("appendForwardedCloseFacts = false, want the facts written")
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}

	if got := countKind(entries, 1, store.KindReport); got != 1 {
		t.Fatalf("report entries = %d, want one", got)
	}
	var report store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindReport {
			report = e
		}
	}
	view := fetch.view
	if report.Round != 1 || report.Direction != store.DirToMasterMind || !report.TS.Equal(clock) {
		t.Errorf("report entry = round %d dir %q ts %v, want round 1 %q at %v",
			report.Round, report.Direction, report.TS, store.DirToMasterMind, clock)
	}
	if report.Confirmed {
		t.Errorf("report Confirmed = true, want false: the owner daemon delivers it")
	}
	if report.Outcome != view.ReportOutcome || report.Note != view.ReportNote {
		t.Errorf("report outcome %q note %q, want %q %q", report.Outcome, report.Note, view.ReportOutcome, view.ReportNote)
	}
	if report.Path != rt.Store.ReportPath("api", 1) || report.Payload != string(fetch.report) {
		t.Errorf("report path %q payload %q, want the head report path and body", report.Path, report.Payload)
	}
	if report.Usage == nil || *report.Usage != *view.Usage {
		t.Errorf("report usage = %v, want the view's %v verbatim", report.Usage, view.Usage)
	}
	if report.Rusage == nil || *report.Rusage != *view.Rusage {
		t.Errorf("report rusage = %v, want the view's %v verbatim", report.Rusage, view.Rusage)
	}
	if report.PriorTokens == nil || *report.PriorTokens != *view.PriorTokens {
		t.Errorf("report prior tokens = %v, want the view's %v verbatim", report.PriorTokens, view.PriorTokens)
	}
	if report.Gate == nil || report.Gate.Result != view.GateResult {
		t.Errorf("report gate = %v, want the view's gate result %q", report.Gate, view.GateResult)
	}

	if got := countKind(entries, 1, store.KindDiff); got != 1 {
		t.Fatalf("diff entries = %d, want one", got)
	}
	for _, e := range entries {
		if e.Kind != store.KindDiff {
			continue
		}
		if e.Note != view.DiffNote || e.Commits != view.DiffCommits || e.Tree != view.DiffTree {
			t.Errorf("diff entry note %q commits %d tree %q, want %q %d %q",
				e.Note, e.Commits, e.Tree, view.DiffNote, view.DiffCommits, view.DiffTree)
		}
		if !e.Confirmed || e.Path != rt.Store.DiffPath("api", 1) {
			t.Errorf("diff entry confirmed %v path %q, want a confirmed entry at the head diff path", e.Confirmed, e.Path)
		}
	}

	// The nudge-shaped and empty notes are skipped; the two real switches
	// each land as their own confirmed entry, in view order.
	if got := countKind(entries, 1, store.KindSwitch); got != 2 {
		t.Fatalf("switch entries = %d, want two (nudge and empty skipped)", got)
	}
	wantSwitches := []string{"switched to agy/test/m: rate-limited", "switched to claude/anthropic/sonnet: gated"}
	var gotSwitches []string
	for _, e := range entries {
		if e.Kind == store.KindSwitch {
			gotSwitches = append(gotSwitches, e.Note)
			if !e.Confirmed {
				t.Errorf("switch entry %q unconfirmed, want confirmed: a switch is status, not a delivery", e.Note)
			}
		}
	}
	if strings.Join(gotSwitches, "|") != strings.Join(wantSwitches, "|") {
		t.Errorf("switch notes = %q, want %q", strings.Join(gotSwitches, "|"), strings.Join(wantSwitches, "|"))
	}
	if got := countKind(entries, 1, store.KindStop); got != 0 {
		t.Errorf("stop entries = %d, want none for a round that was not stopped", got)
	}

	// A retried close appends nothing a second time: the guard covers the
	// whole helper.
	if !appendForwardedCloseFacts(rt, "api", fwd, fetch, clock.Add(time.Minute)) {
		t.Fatal("retried appendForwardedCloseFacts = false, want the skip to succeed")
	}
	again, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []store.Kind{store.KindReport, store.KindDiff, store.KindSwitch} {
		if got, want := countKind(again, 1, kind), countKind(entries, 1, kind); got != want {
			t.Errorf("%s entries after retry = %d, want %d: nothing may append twice", kind, got, want)
		}
	}
}

// TestForwardedCloseStoppedRoundWritesStopWithoutReport pins the stopped
// shape: a round the node stopped may carry no report, and the head still
// files the stop entry exactly as relevo.Stop writes it, so the laptop and
// the chain member read the round as stopped.
func TestForwardedCloseStoppedRoundWritesStopWithoutReport(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, fwd := closeFactsEnv(t, clock)
	rt := env.runtime(t)
	fetch := fullFactsFetch()
	fetch.report = nil
	fetch.view.Stopped = "killed"

	if !appendForwardedCloseFacts(rt, "api", fwd, fetch, clock) {
		t.Fatal("appendForwardedCloseFacts = false")
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if got := countKind(entries, 1, store.KindReport); got != 0 {
		t.Errorf("report entries = %d, want none without a report", got)
	}
	var stop store.LogEntry
	stops := 0
	for _, e := range entries {
		if e.Kind == store.KindStop {
			stop, stops = e, stops+1
		}
	}
	if stops != 1 {
		t.Fatalf("stop entries = %d, want exactly one", stops)
	}
	if stop.Note != "stopped/killed" || !stop.Confirmed || stop.Round != 1 {
		t.Errorf("stop entry = %+v, want a confirmed stopped/killed on round 1", stop)
	}

	// The stop entry is the guard for this shape: without a report entry it
	// is what marks a completed run, and a retry writes nothing again.
	if !appendForwardedCloseFacts(rt, "api", fwd, fetch, clock.Add(time.Minute)) {
		t.Fatal("retried helper = false")
	}
	again, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if got := countKind(again, 1, store.KindStop); got != 1 {
		t.Errorf("stop entries after retry = %d, want one", got)
	}
	if got := countKind(again, 1, store.KindDiff); got != 1 {
		t.Errorf("diff entries after retry = %d, want one", got)
	}
}

// TestForwardedCloseNilFactsLandsNilColumns pins the absent-facts shape: a
// view carrying no usage, rusage, prior tokens or gate lands those fields
// nil -- the head measures nothing itself -- without the helper failing.
func TestForwardedCloseNilFactsLandsNilColumns(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, fwd := closeFactsEnv(t, clock)
	rt := env.runtime(t)
	fetch := &observeFetch{view: remote.BindingView{
		Name: "nb-api", RoundState: remote.RoundClosed, ClosedRound: 1,
		ReportOutcome: "done",
	}, report: []byte("# Report\nDone.\n")}

	if !appendForwardedCloseFacts(rt, "api", fwd, fetch, clock) {
		t.Fatal("appendForwardedCloseFacts = false")
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if got := countKind(entries, 1, store.KindReport); got != 1 {
		t.Fatalf("report entries = %d, want one", got)
	}
	for _, e := range entries {
		if e.Kind != store.KindReport {
			continue
		}
		if e.Usage != nil || e.Rusage != nil || e.PriorTokens != nil || e.Gate != nil {
			t.Errorf("entry usage %v rusage %v prior %v gate %v, want all nil", e.Usage, e.Rusage, e.PriorTokens, e.Gate)
		}
		if e.Outcome != "done" {
			t.Errorf("outcome = %q, want the view's verbatim", e.Outcome)
		}
	}
	if got := countKind(entries, 1, store.KindDiff); got != 0 {
		t.Errorf("diff entries = %d, want none for a view with no diff facts", got)
	}
}

// TestForwardedCloseChainMemberMarksConsumed pins the chain arm: a member of
// a running chain records its report consumed, the way queueReportEntry does,
// because the chain's own end delivery is the MasterMind's copy.
func TestForwardedCloseChainMemberMarksConsumed(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, fwd := closeFactsEnv(t, clock)
	rt := env.runtime(t)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainPut(db.ChainRow{
			ID: "ch1", Name: "ch1", Owner: rt.Store.Owner(), Status: string(chain.StatusRunning),
			Phase: "build", Step: "build", Plan: 1, Plans: 1,
			Builder: "api", CreatedAt: clock, UpdatedAt: clock,
		})
	}); err != nil {
		t.Fatalf("chain put: %v", err)
	}

	fetch := fullFactsFetch()
	if !appendForwardedCloseFacts(rt, "api", fwd, fetch, clock) {
		t.Fatal("appendForwardedCloseFacts = false")
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind != store.KindReport {
			continue
		}
		if !e.Confirmed {
			t.Errorf("report Confirmed = false, want consumed: the running chain owes the delivery")
		}
		if !strings.Contains(e.Note, "consumed by chain ch1") {
			t.Errorf("report note = %q, want it to name the consuming chain", e.Note)
		}
		return
	}
	t.Fatal("no report entry written")
}

// TestForwardedCloseAppendFailureDefersClose pins the retry contract: with
// the binding directory gone and its parent read-only, the append fails and
// the helper reports false, so applyObserveClosed defers the close for the
// next tick instead of advancing past an unwritten fact.
func TestForwardedCloseAppendFailureDefersClose(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, fwd := closeFactsEnv(t, clock)
	rt := env.runtime(t)

	// The binding directory lives under the owner root; removing it and
	// freezing the root makes its re-creation the failure. The log itself is
	// database state, so the guard read still works.
	ownerRoot := headOwnerPath(t, env)
	if err := os.RemoveAll(filepath.Join(ownerRoot, "api")); err != nil {
		t.Fatalf("remove binding dir: %v", err)
	}
	freezeRepo(t, ownerRoot)

	if appendForwardedCloseFacts(rt, "api", fwd, fullFactsFetch(), clock) {
		t.Fatal("appendForwardedCloseFacts = true, want false when the append fails")
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if got := countKind(entries, 1, store.KindReport); got != 0 {
		t.Errorf("report entries = %d, want none after the failed append", got)
	}
}

// TestForwardedCloseFactsRoundTrip pins the design's identical-look claim:
// servedRoundFacts, reached through ServedView, reads the head log back into
// exactly the facts the node shipped, field by field -- and the stopped
// shape, which has no report, still reads stopped.
func TestForwardedCloseFactsRoundTrip(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	env, fwd := closeFactsEnv(t, clock)
	rt := env.runtime(t)
	fetch := fullFactsFetch()
	if !appendForwardedCloseFacts(rt, "api", fwd, fetch, clock) {
		t.Fatal("appendForwardedCloseFacts = false")
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Round = 2
	if b.Serve == nil {
		b.Serve = &store.ServeFacts{}
	}
	b.Serve.ClosedRound = 1

	got := relevo.ServedView(b, entries, "", "")
	view := fetch.view
	if got.ReportOutcome != view.ReportOutcome || got.ReportNote != view.ReportNote {
		t.Errorf("round trip outcome %q note %q, want %q %q",
			got.ReportOutcome, got.ReportNote, view.ReportOutcome, view.ReportNote)
	}
	if got.GateResult != view.GateResult {
		t.Errorf("round trip gate = %q, want %q", got.GateResult, view.GateResult)
	}
	if got.DiffNote != view.DiffNote || got.DiffCommits != view.DiffCommits || got.DiffTree != view.DiffTree {
		t.Errorf("round trip diff = %q/%d/%q, want %q/%d/%q",
			got.DiffNote, got.DiffCommits, got.DiffTree, view.DiffNote, view.DiffCommits, view.DiffTree)
	}
	wantSwitches := []string{"switched to agy/test/m: rate-limited", "switched to claude/anthropic/sonnet: gated"}
	if strings.Join(got.Switches, "|") != strings.Join(wantSwitches, "|") {
		t.Errorf("round trip switches = %q, want %q", strings.Join(got.Switches, "|"), strings.Join(wantSwitches, "|"))
	}
	if got.Usage == nil || *got.Usage != *view.Usage {
		t.Errorf("round trip usage = %v, want the view's %v", got.Usage, view.Usage)
	}
	if got.Rusage == nil || *got.Rusage != *view.Rusage {
		t.Errorf("round trip rusage = %v, want the view's %v", got.Rusage, view.Rusage)
	}
	// PriorTokens round-trips: the report entry carries the node's prior
	// segment, and PriorTokensOf reads exactly that field back.
	if got.PriorTokens == nil || *got.PriorTokens != *view.PriorTokens {
		t.Errorf("round trip prior tokens = %v, want the view's %v", got.PriorTokens, view.PriorTokens)
	}
	if got.Stopped != "" {
		t.Errorf("round trip stopped = %q, want empty for a round that finished", got.Stopped)
	}

	// The stopped shape: no report, the stop word reads back.
	env2, fwd2 := closeFactsEnv(t, clock)
	rt2 := env2.runtime(t)
	stopFetch := fullFactsFetch()
	stopFetch.report = nil
	stopFetch.view.Stopped = "killed"
	if !appendForwardedCloseFacts(rt2, "api", fwd2, stopFetch, clock) {
		t.Fatal("stopped helper = false")
	}
	entries2, err := rt2.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	b2, err := rt2.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b2.Round = 2
	if b2.Serve == nil {
		b2.Serve = &store.ServeFacts{}
	}
	b2.Serve.ClosedRound = 1
	if got := relevo.ServedView(b2, entries2, "", "").Stopped; got != "killed" {
		t.Errorf("round trip stopped = %q, want killed", got)
	}
}
