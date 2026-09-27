package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

func writePlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

// TestSendArmsSessionCursorForPaneBuilder pins Send's pane branch (#184):
// after the plan lands, the builder's cursor is cut at the located session
// record's current size, so the round's log holds only what the builder
// writes to its own record from here on.
// The role is selected with --agent at launch (#85); the plan prompt is
// the plan prompt, on round 1 as on every other.
// TestPromptRetryReportsANonStallFailureAsItself and
// TestPromptRetryReportsASecondStallAsAStall are gone with the pane delivery
// path itself (#303, closed-list item 1): promptWithRetry typed into a pane,
// and there is no pane to type into any more. The headless equivalent -- a
// process that cannot start -- is startRound's ErrRunnerUnavailable and the
// spawn-failed switch, both covered in headless_test.go.
func TestSendLogsThePlan(t *testing.T) {
	t.Parallel()

	rt, _ := seedBound(t)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	// seedBound's underlying Bind already wrote the builder bind's pick entry.
	if len(entries) != 2 || entries[0].Kind != store.KindPick || entries[1].Kind != store.KindPlan || entries[1].Direction != store.DirToBuilder {
		t.Fatalf("log = %+v", entries)
	}
	if !entries[1].Confirmed {
		t.Error("an outbound plan is confirmed the moment the process is started")
	}
}

func TestSendCapturesBaselineWithFakeGit(t *testing.T) {
	t.Parallel()

	rt, _ := seedBound(t)
	fg := &fakeGit{snapshotTreeID: "tree-abc123", headCommitID: "head-abc123"}
	rt.Git = fg

	src := writePlan(t, "# test plan")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Fatalf("round = %d, want 1", res.Round)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "tree-abc123" {
		t.Errorf("RoundBaselineTree = %q, want tree-abc123", b.RoundBaselineTree)
	}
	if b.RoundBaselineHead != "head-abc123" {
		t.Errorf("RoundBaselineHead = %q, want head-abc123", b.RoundBaselineHead)
	}
	if fg.snapshotCalls != 1 {
		t.Errorf("snapshotCalls = %d, want 1", fg.snapshotCalls)
	}
	if !b.FinishPending {
		t.Error("FinishPending = false, want true after Send opens a round")
	}
}

func TestSendHeadFailureLeavesTreeAndClearsHead(t *testing.T) {
	t.Parallel()

	rt, _ := seedBound(t)
	rt.Git = &fakeGit{snapshotTreeID: "tree-abc123", headCommitErr: errors.New("unborn HEAD")}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# test plan"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "tree-abc123" || b.RoundBaselineHead != "" {
		t.Errorf("baseline = (%q, %q), want (tree-abc123, \"\")", b.RoundBaselineTree, b.RoundBaselineHead)
	}
}

func TestSendBaselineFailureTolerated(t *testing.T) {
	t.Parallel()

	rt, _ := seedBound(t)
	fg := &fakeGit{snapshotTreeErr: errors.New("git broken")}
	rt.Git = fg

	src := writePlan(t, "# test plan")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Fatalf("round = %d, want 1", res.Round)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "" {
		t.Errorf("RoundBaselineTree = %q, want empty", b.RoundBaselineTree)
	}

	// Verify plan was still copied and logged
	copied, err := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if err != nil || string(copied) != "# test plan" {
		t.Fatalf("plan file error: %v, content: %q", err, string(copied))
	}
	log, err := rt.Store.ReadLog("webshop")
	if err != nil || len(log) == 0 || log[len(log)-1].Kind != store.KindPlan {
		t.Fatalf("expected plan log entry, got %v, err: %v", log, err)
	}
}

// A binding that appears only after the pre-lock load must never be addressed
// with the zero agent's empty pane id. See round 3, Task 1.
// TestSendRefusesPaused: a paused binding has no builder to address and its
// worktree is gone; the human resumes it first. No plan is staged.
func TestSendRefusesPaused(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	b := store.Binding{
		Name: "webshop", CWD: "/repo", Worktree: "/wt/webshop", Branch: "relevo/webshop",
		Planner: store.Endpoint{SessionID: "sess-architect", Kind: "claude"},
		Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		Round:   2, State: store.StatePaused,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save paused: %v", err)
	}

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{})
	if err == nil {
		t.Fatal("Send on a paused binding must be refused")
	}
	if !strings.Contains(err.Error(), "paused") {
		t.Errorf("err = %q, want it to mention paused", err)
	}
	if entries, _ := rt.Store.ReadLog("webshop"); len(entries) != 0 {
		t.Errorf("no plan may be staged: log = %+v", entries)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("no process may be started: %d specs", got)
	}
}

func TestSendUnchangedTreeBetweenRounds(t *testing.T) {
	t.Parallel()

	rt, b := seedBound(t)
	b.Round = 2
	b.RoundClosedTree = "tree-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rt.Git = &fakeGit{snapshotTreeID: "tree-1"}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Drift != "" {
		t.Errorf("got Drift %q, want empty", res.Drift)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			t.Fatalf("unexpected KindDrift entry: %+v", e)
		}
	}
}

func TestSendChangedTreeBetweenRounds(t *testing.T) {
	t.Parallel()

	rt, b := seedBound(t)
	b.Round = 2
	b.RoundClosedTree = "tree-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rt.Git = &fakeGit{
		snapshotTreeID: "tree-2",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 1, Insertions: 5, Deletions: 2},
			Patch: []byte("patch content\n"),
		},
	}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 2 {
		t.Fatalf("res.Round = %d, want 2", res.Round)
	}
	if res.Drift == "" {
		t.Fatal("expected non-empty Drift line")
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	var driftEntries []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			driftEntries = append(driftEntries, e)
		}
	}
	if len(driftEntries) != 1 {
		t.Fatalf("got %d KindDrift entries, want 1", len(driftEntries))
	}
	de := driftEntries[0]
	if de.Round != 2 {
		t.Errorf("drift entry Round = %d, want opening round 2", de.Round)
	}
	if !de.Confirmed {
		t.Error("drift entry must have Confirmed == true")
	}
	if de.Direction != store.DirToPlanner {
		t.Errorf("drift entry Direction = %v, want DirToPlanner", de.Direction)
	}
	if de.Path == "" {
		t.Fatal("drift entry Path is empty")
	}
	// Drift is a round_file row now, not a file (R1-lite).
	patch, err := rt.Store.ReadFile(de.Path)
	if err != nil {
		t.Fatalf("read drift patch %s: %v", de.Path, err)
	}
	if string(patch) != "patch content\n" {
		t.Errorf("patch = %q, want %q", string(patch), "patch content\n")
	}
}

// TestSendDriftEntryPinsConfirmedDoesNotShadowPendingReport asserts that
// an unconsumed pending report is still returned by Pull after a Send with drift.
// An unconfirmed drift entry would shadow the report in pendingForPlanner.
func TestSendDriftEntryPinsConfirmedDoesNotShadowPendingReport(t *testing.T) {
	t.Parallel()

	rt, b := queuedBinding(t) // queues an unconfirmed report for round 1
	b.Round = 2
	b.RoundClosedTree = "tree-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rt.Git = &fakeGit{
		snapshotTreeID: "tree-2",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 1, Insertions: 5, Deletions: 2},
			Patch: []byte("patch content\n"),
		},
	}

	src := writePlan(t, "plan round 2")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Drift == "" {
		t.Fatal("expected drift to be detected")
	}

	payload, found, err := delivery.Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil || !found {
		t.Fatalf("pullPending: found=%v err=%v", found, err)
	}
	if !strings.Contains(payload, "001-report.md") {
		t.Fatalf("pullPending returned payload %q, want pending report", payload)
	}
}

func TestSendRound1NoRoundClosedTreeSilent(t *testing.T) {
	t.Parallel()

	rt, b := seedBound(t)
	if b.RoundClosedTree != "" {
		t.Fatalf("expected round 1 RoundClosedTree to be empty, got %q", b.RoundClosedTree)
	}

	rt.Git = &fakeGit{snapshotTreeID: "tree-1"}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Drift != "" {
		t.Errorf("res.Drift = %q, want empty", res.Drift)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindDrift {
			t.Fatalf("unexpected KindDrift entry on round 1: %+v", e)
		}
	}
}

func TestSendSuccessfulSendClearsRoundClosedTree(t *testing.T) {
	t.Parallel()

	rt, b := seedBound(t)
	b.Round = 2
	b.RoundClosedTree = "tree-closed"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	src := writePlan(t, "plan")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 2 {
		t.Fatalf("round = %d, want 2", res.Round)
	}

	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if b.RoundClosedTree != "" {
		t.Errorf("RoundClosedTree = %q, want empty after successful send", b.RoundClosedTree)
	}
}

// TestComposePromptNamesPlanReportAndMarkerInOrder pins the handoff contract:
// the builder is told the plan, the report and the completion marker, in that
// order, and told the marker is its last action (spec §3.3).
func TestComposePromptNamesPlanReportAndMarkerInOrder(t *testing.T) {
	t.Parallel()

	b := store.Binding{Name: "webshop", CWD: "/repo/webshop", Round: 3}
	got := composePrompt(Runtime{}, b, "/s/003-plan.md", "/s/003-report.md", "/s/003-done")

	wantOrigin := delivery.OriginLine("webshop", 3, store.DirToBuilder, store.KindPlan)
	firstLine := strings.SplitN(got, "\n", 2)[0]
	if firstLine != wantOrigin {
		t.Errorf("first line = %q, want origin line %q", firstLine, wantOrigin)
	}
	if !strings.Contains(got, "Your working tree is: /repo/webshop") {
		t.Errorf("prompt must name the working tree (#192), got:\n%s", got)
	}
	if !strings.Contains(got, "create the done marker,\nand do nothing else.") {
		t.Errorf("prompt must state the git-status halt rule, got:\n%s", got)
	}

	plan := strings.Index(got, "/s/003-plan.md")
	report := strings.Index(got, "/s/003-report.md")
	done := strings.Index(got, "/s/003-done")
	if plan < 0 || report < 0 || done < 0 || !(plan < report && report < done) {
		t.Fatalf("paths must appear plan < report < marker, got:\n%s", got)
	}
	if !strings.Contains(got, "as the very last thing you do") {
		t.Errorf("prompt must say the marker is the last action, got:\n%s", got)
	}
	if !strings.HasSuffix(got, "Reply here with only the report path.") {
		t.Errorf("prompt must end with the reply instruction, got:\n%s", got)
	}
}

func TestSendHeadlessTierYoloOverrideAndRoundClose(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	// Create an agy candidate without extra_args so TierYolo adds --dangerously-skip-permissions cleanly
	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)
	rt.Runner = fr
	_, err := Bind(context.Background(), rt, BindOptions{
		Name:      "webshop",
		Candidate: "agy/test/m",
		PlannerID: testPlannerName,
		CWD:       "/repo",
		Headless:  true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	src := writePlan(t, "# do yolo")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{Tier: "yolo", AllowYolo: true})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("round = %d, want 1", res.Round)
	}

	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.RoundTier != "yolo" {
		t.Errorf("RoundTier = %q, want %q", stored.RoundTier, "yolo")
	}

	if len(fr.specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(fr.specs))
	}
	hasYoloFlag := false
	for _, arg := range fr.specs[0].Argv {
		if arg == "--dangerously-skip-permissions" {
			hasYoloFlag = true
			break
		}
	}
	if !hasYoloFlag {
		t.Errorf("expected --dangerously-skip-permissions in argv, got %v", fr.specs[0].Argv)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var planEntry *store.LogEntry
	for i := range entries {
		if entries[i].Round == 1 && entries[i].Kind == store.KindPlan {
			planEntry = &entries[i]
			break
		}
	}
	if planEntry == nil {
		t.Fatal("no plan entry found in log")
	}
	if planEntry.Tier != "yolo" {
		t.Errorf("plan entry Tier = %q, want %q", planEntry.Tier, "yolo")
	}

	// Now round close via queueReport
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog("webshop")
		if err != nil {
			return err
		}
		next, err := queueReport(context.Background(), rt, tx, cur, entries, "/dev/null", "done", "test", nil, nil, nil, nil)
		if err != nil {
			return err
		}
		return tx.Save(next)
	})
	if err != nil {
		t.Fatalf("queueReport round close: %v", err)
	}

	closedB, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load after close: %v", err)
	}
	if closedB.RoundTier != "" {
		t.Errorf("RoundTier after round close = %q, want empty", closedB.RoundTier)
	}
}

// TestSendKillsTheBuilderWhenTheSendFailsAfterSpawn pins #436's second half:
// a failure after the process started must stop it and say so, so a busy db
// never leaves an untracked builder running.
func TestSendKillsTheBuilderWhenTheSendFailsAfterSpawn(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)

	before, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	old := sendAfterSpawn
	sendAfterSpawn = func(name string) error { return errors.New("injected") }
	t.Cleanup(func() { sendAfterSpawn = old })

	_, err = Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{})
	if err == nil {
		t.Fatal("Send returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "injected") || !strings.Contains(err.Error(), "was stopped") {
		t.Errorf("Send error = %q, want it to contain both %q and %q", err, "injected", "was stopped")
	}

	if len(fr.handles) != 1 {
		t.Fatalf("Start was called %d times, want 1", len(fr.handles))
	}
	if len(fr.kills) != 1 || fr.kills[0] != fr.handles[0] {
		t.Errorf("kills = %+v, want exactly the handle Start returned (%+v)", fr.kills, fr.handles[0])
	}

	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load after: %v", err)
	}
	if after.Builder.PID != before.Builder.PID {
		t.Errorf("builder PID = %d, want unchanged %d", after.Builder.PID, before.Builder.PID)
	}
	if after.Round != before.Round {
		t.Errorf("round = %d, want unchanged %d", after.Round, before.Round)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Kind == store.KindPlan && e.Round == 1 {
			t.Errorf("log has a plan entry for round 1, want none: %+v", e)
		}
	}
}

// TestSendRefusesWhileTheRoundsScopeIsActive pins #445: when the round's
// scope unit is still loaded, Send refuses before it spawns anything, with
// ErrScopeActive, and leaves no plan file or NEEDS YOU behind.
func TestSendRefusesWhileTheRoundsScopeIsActive(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fr.scopeActive = map[string]bool{scopeUnitName(b): true}

	_, err = Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{})
	if !errors.Is(err, ErrScopeActive) {
		t.Fatalf("Send = %v, want errors.Is(..., ErrScopeActive)", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("Start was called %d times, want 0", len(fr.specs))
	}
	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load after: %v", err)
	}
	if after.State == store.StateNeedsYou {
		t.Errorf("State = %q, want not NEEDS YOU", after.State)
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", b.Round)); !os.IsNotExist(err) {
		t.Errorf("plan file for round %d exists, want none (err=%v)", b.Round, err)
	}
}

// TestSendWithScopesOffNeverProbesAScope pins the guard's precondition: with
// rt.Scope nil, no scope unit can exist, so Send never asks the runner about
// one and the send proceeds normally.
func TestSendWithScopesOffNeverProbesAScope(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fr.scopeQueries) != 0 {
		t.Errorf("scopeQueries = %v, want none (rt.Scope is nil)", fr.scopeQueries)
	}
}

// closeWebshopRoundOne reconciles webshop's round 1 on its marker and report
// and persists the result, so a later Send sees round 2. Reconcile itself does
// not save -- the daemon's tick does -- so a test must.
func closeWebshopRoundOne(t *testing.T, rt Runtime, b store.Binding) store.Binding {
	t.Helper()
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the close", got.Round)
	}
	if err := rt.Store.Save(got); err != nil {
		t.Fatal(err)
	}
	return got
}

// roundTwoWithActivePreviousScope closes round 1 on its marker and report, so
// the binding is at round 2, then turns scopes on and scripts round 1's unit
// active: the state the send-time guard exists for. It returns the runtime, the
// round-2 binding and round 1's unit base name.
func roundTwoWithActivePreviousScope(t *testing.T, fr *fakeRunner) (Runtime, store.Binding, string) {
	t.Helper()
	rt, b := sentHeadless(t, fr)
	got := closeWebshopRoundOne(t, rt, b)
	rt.Scope = &spawn.ScopeSpec{}
	unit := scopeUnitNameFor(scopeRound, got.Owner, got.Name, 1, "")
	fr.scopeActive = map[string]bool{unit: true}
	return rt, got, unit
}

// TestSendReapsAnEarlierRoundsScopeBeforeItStarts pins the send-time guard's
// first half: the round before the one being sent still holds a scope, so Send
// ends it before staging round 2 and then starts the new process.
func TestSendReapsAnEarlierRoundsScopeBeforeItStarts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b, unit := roundTwoWithActivePreviousScope(t, fr)
	if len(fr.specs) != 1 {
		t.Fatalf("setup started %d processes, want 1", len(fr.specs))
	}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "round two"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fr.scopeStops) != 1 || fr.scopeStops[0] != unit {
		t.Errorf("scopeStops = %v, want [%s]", fr.scopeStops, unit)
	}
	if len(fr.specs) != 2 {
		t.Errorf("specs = %d, want 2: one new process for round 2", len(fr.specs))
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", b.Round)); err != nil {
		t.Errorf("plan for round %d: %v, want it to exist", b.Round, err)
	}
}

// TestSendRefusesWhenAnEarlierRoundsScopeCannotBeEnded pins the refusal: when
// the previous round's scope will not end, Send refuses with ErrScopeActive
// before it stages anything, so no plan file and no NEEDS YOU are left behind.
func TestSendRefusesWhenAnEarlierRoundsScopeCannotBeEnded(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b, _ := roundTwoWithActivePreviousScope(t, fr)
	fr.scopeStopErr = errors.New("systemctl: stop failed")

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "round two"), SendOptions{})
	if !errors.Is(err, ErrScopeActive) {
		t.Fatalf("Send = %v, want errors.Is(..., ErrScopeActive)", err)
	}
	if len(fr.specs) != 1 {
		t.Errorf("Start was called %d times, want 1 (the setup's)", len(fr.specs))
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", b.Round)); !os.IsNotExist(err) {
		t.Errorf("plan file for round %d exists, want none (err=%v)", b.Round, err)
	}
}

// proberOnlyRunner is a Runner that can see a scope but not end one: it embeds
// spawn.Runner for the method set and adds only ScopeActive, so a type
// assertion to spawn.ScopeStopper must fail.
type proberOnlyRunner struct {
	spawn.Runner
	active map[string]bool
}

func (r proberOnlyRunner) ScopeActive(_ context.Context, unit string) (bool, error) {
	return r.active[unit], nil
}

// TestSendRefusesAScopeItsRunnerCannotEnd pins the missing-stopper case: a
// runner that can see the previous round's loaded scope but cannot end it
// refuses the send rather than start a second builder beside it.
func TestSendRefusesAScopeItsRunnerCannotEnd(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b, unit := roundTwoWithActivePreviousScope(t, fr)
	rt.Runner = proberOnlyRunner{active: map[string]bool{unit: true}}

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "round two"), SendOptions{})
	if !errors.Is(err, ErrScopeActive) {
		t.Fatalf("Send = %v, want errors.Is(..., ErrScopeActive)", err)
	}
	if len(fr.specs) != 1 {
		t.Errorf("Start was called %d times, want 1 (the setup's)", len(fr.specs))
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", b.Round)); !os.IsNotExist(err) {
		t.Errorf("plan file for round %d exists, want none (err=%v)", b.Round, err)
	}
}

// TestSendWithScopesOffNeverProbesAnEarlierRoundsScope pins the earlier-round
// guard's precondition: with rt.Scope nil, a round-2 send never asks about
// round 1's scope and proceeds normally.
func TestSendWithScopesOffNeverProbesAnEarlierRoundsScope(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	closeWebshopRoundOne(t, rt, b)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "round two"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fr.scopeQueries) != 0 {
		t.Errorf("scopeQueries = %v, want none (rt.Scope is nil)", fr.scopeQueries)
	}
	if len(fr.scopeStops) != 0 {
		t.Errorf("scopeStops = %v, want none (rt.Scope is nil)", fr.scopeStops)
	}
}

func TestSendHeadlessNoTierDefaultsToHarness(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)

	src := writePlan(t, "# do default")
	res, err := Send(context.Background(), rt, "webshop", src, SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("round = %d, want 1", res.Round)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var planEntry *store.LogEntry
	for i := range entries {
		if entries[i].Round == 1 && entries[i].Kind == store.KindPlan {
			planEntry = &entries[i]
			break
		}
	}
	if planEntry == nil {
		t.Fatal("no plan entry found in log")
	}
	if planEntry.Tier != "harness" {
		t.Errorf("plan entry Tier = %q, want %q", planEntry.Tier, "harness")
	}

	// Verify argv is identical to pre-#141 (contains extra_args from candidate)
	if len(fr.specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(fr.specs))
	}
	planPath := rt.Store.PlanPath("webshop", 1)
	reportPath := rt.Store.ReportPath("webshop", 1)
	donePath := rt.Store.DonePath("webshop", 1)
	b, _ := rt.Store.Load("webshop")
	wantPrompt := composePrompt(rt, b, planPath, reportPath, donePath)
	wantArgv := []string{
		"agy", "-p", wantPrompt, "--model", "m", "--agent", "plan-executor",
		"--output-format", "stream-json", "--print-timeout", "24h0m0s", "--add-dir", "/repo",
		"--dangerously-skip-permissions",
	}
	if !reflect.DeepEqual(fr.specs[0].Argv, wantArgv) {
		t.Errorf("argv =\n%v\nwant =\n%v", fr.specs[0].Argv, wantArgv)
	}
	if !b.FinishPending {
		t.Error("FinishPending = false, want true after Send opens a round")
	}
}

// TestSendDryRunPaneMakesNoWrites pins the dry run's contract for a pane
// binding: it describes the round Send would open and writes nothing -- no
// staged plan, no log entry, no prompt, no change to the binding (#149).
// endProcess scripts the binding's recorded process as exited, so a second
// Send into the same binding is allowed: one process per round (#99 §5.2).
func endProcess(t *testing.T, rt Runtime, b store.Binding) {
	t.Helper()
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("runtime has no fakeRunner")
	}
	if b.Builder.PID != 0 {
		fr.script(b.Builder.PID, false)
	}
}

// TestSendDryRunHeadlessShowsArgv pins that a dry run of a headless binding
// reports the launch it would use -- the harness binary first -- without
// starting anything.
func TestSendDryRunHeadlessShowsArgv(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)

	d, err := SendDryRun(context.Background(), rt, "webshop", writePlan(t, "# do the thing"), SendOptions{})
	if err != nil {
		t.Fatalf("SendDryRun: %v", err)
	}
	if d.Mode != "headless" {
		t.Errorf("Mode = %q, want headless", d.Mode)
	}
	h, ok := harness.Lookup("agy")
	if !ok || h.Binary == "" {
		t.Fatal("no agy harness to name")
	}
	if !strings.HasPrefix(d.Where, h.Binary) {
		t.Errorf("Where = %q, want it to start with the harness binary %q", d.Where, h.Binary)
	}
	if len(fr.specs) != 0 {
		t.Errorf("a dry run must start nothing: %+v", fr.specs)
	}
}

// TestSendDryRunGateNote pins the advisory gate note: a rate-limited candidate
// still dry-runs, but the note says the daemon would switch after the start.
func TestSendDryRunGateNote(t *testing.T) {
	t.Parallel()

	rt, _ := seedBound(t)

	if _, err := availability.Unavailable(AvailabilityDeps(rt), testAgyRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	d, err := SendDryRun(context.Background(), rt, "webshop", writePlan(t, "# x"), SendOptions{})
	if err != nil {
		t.Fatalf("SendDryRun: %v", err)
	}
	if !strings.Contains(d.GateNote, "rate-limited") {
		t.Errorf("GateNote = %q, want it to say rate-limited", d.GateNote)
	}
	if !strings.Contains(d.GateNote, "would switch") {
		t.Errorf("GateNote = %q, want it to say the daemon would switch", d.GateNote)
	}
}

// TestSendDryRunErrorsMatchSend pins §6: for every precondition, the dry run
// returns the identical error Send would, exit-1 text included, and writes
// nothing on the way.
func TestSendDryRunErrorsMatchSend(t *testing.T) {
	t.Parallel()

	type dryRunCase struct {
		name  string
		opts  SendOptions
		setup func(t *testing.T) (Runtime, *fakeRunner, string, string)
	}

	broken := func(t *testing.T) (Runtime, *fakeRunner, string, string) {
		rt, _ := seedBound(t)
		b, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		b.State = store.StateBroken
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		return rt, nil, "webshop", writePlan(t, "# x")
	}
	capped := func(t *testing.T) (Runtime, *fakeRunner, string, string) {
		rt, _ := seedBound(t)
		b, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		b.Round, b.RoundCap = 5, 3
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		return rt, nil, "webshop", writePlan(t, "# x")
	}
	headlessBusy := func(t *testing.T) (Runtime, *fakeRunner, string, string) {
		fr := newFakeRunner()
		rt, _ := seedHeadless(t, fr)
		// Prime a live process: unscripted, the fake reports it alive forever.
		if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# one"), SendOptions{}); err != nil {
			t.Fatalf("prime Send: %v", err)
		}
		return rt, fr, "webshop", writePlan(t, "# two")
	}
	headlessNoRunner := func(t *testing.T) (Runtime, *fakeRunner, string, string) {
		rt, _ := seedHeadless(t, newFakeRunner())
		rt.Runner = nil
		return rt, nil, "webshop", writePlan(t, "# x")
	}

	// The missing-plan case's path is fixed once: the error text names the
	// file, and t.TempDir() mints a new directory on every call.
	missingPlan := filepath.Join(t.TempDir(), "nope.md")
	cases := []dryRunCase{
		{"missing plan file", SendOptions{}, func(t *testing.T) (Runtime, *fakeRunner, string, string) {
			rt, _ := seedBound(t)
			return rt, nil, "webshop", missingPlan
		}},
		{"unknown binding", SendOptions{}, func(t *testing.T) (Runtime, *fakeRunner, string, string) {
			rt, _ := seedBound(t)
			return rt, nil, "ghost", writePlan(t, "# x")
		}},
		{"broken binding", SendOptions{}, broken},
		{"round cap", SendOptions{}, capped},
		{"headless busy", SendOptions{}, headlessBusy},
		{"headless without runner", SendOptions{}, headlessNoRunner},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rtDry, frDry, nameDry, fileDry := c.setup(t)
			rtSend, _, nameSend, fileSend := c.setup(t)

			nBefore, err := rtDry.Store.ReadLog(nameDry)
			if err != nil {
				t.Fatalf("ReadLog: %v", err)
			}
			planPath := rtDry.Store.PlanPath(nameDry, 1)
			_, statBefore := os.Stat(planPath)
			planExisted := statBefore == nil
			specsBefore := 0
			if frDry != nil {
				specsBefore = len(frDry.specs)
			}

			_, dryErr := SendDryRun(context.Background(), rtDry, nameDry, fileDry, c.opts)
			if dryErr == nil {
				t.Fatal("SendDryRun: want the error Send gives, got nil")
			}
			_, sendErr := Send(context.Background(), rtSend, nameSend, fileSend, c.opts)
			if sendErr == nil {
				t.Fatal("Send: want an error, got nil")
			}
			if dryErr.Error() != sendErr.Error() {
				t.Errorf("dry run error %q != send error %q", dryErr, sendErr)
			}

			// The dry run wrote nothing.
			if frDry != nil && len(frDry.specs) != specsBefore {
				t.Errorf("dry run started a process: %+v", frDry.specs[specsBefore:])
			}
			nAfter, err := rtDry.Store.ReadLog(nameDry)
			if err != nil {
				t.Fatalf("ReadLog: %v", err)
			}
			if len(nAfter) != len(nBefore) {
				t.Errorf("dry run changed the log: %d -> %d", len(nBefore), len(nAfter))
			}
			_, statAfter := os.Stat(planPath)
			if (statAfter == nil) != planExisted {
				t.Error("dry run changed whether the plan file exists")
			}
		})
	}
}

// TestRenderDryRunShape pins the exact rendered shape of a dry run: the seven
// labelled lines, in order, with the 1024-based size.
func TestRenderDryRunShape(t *testing.T) {
	t.Parallel()

	d := DryRun{
		Name:          "api-auth",
		Round:         5,
		Mode:          "headless",
		Candidate:     "agy/google/gemini-3.8-flash-high",
		CandidateName: "gemini-3.8-flash-high",
		Where:         "/usr/bin/agy -p",
		PlanPath:      "/home/p/.local/state/relevo/api-auth/005-plan.md",
		PlanFrom:      "./plan.md",
		PlanBytes:     4198,
		ReportPath:    "/home/p/.local/state/relevo/api-auth/005-report.md",
		DonePath:      "/home/p/.local/state/relevo/api-auth/005-done",
		Tier:          "yolo",
		PromptHead: []string{
			`relevo: round 5 · to runner "api-auth" · from the planner (not the human)`,
			"Your working tree is: /home/p/.worktrees/api-auth",
		},
	}
	want := `would send round 5 to api-auth
  runner    headless gemini-3.8-flash-high
  where     /usr/bin/agy -p
  tier      yolo
  plan      /home/p/.local/state/relevo/api-auth/005-plan.md  (staged from ./plan.md, 4.1 KiB)
  report    /home/p/.local/state/relevo/api-auth/005-report.md
  marker    /home/p/.local/state/relevo/api-auth/005-done
  prompt    relevo: round 5 · to runner "api-auth" · from the planner (not the human)
            Your working tree is: /home/p/.worktrees/api-auth
`
	got := RenderDryRun(d)
	if got != want {
		t.Errorf("RenderDryRun:\n%s\nwant:\n%s", got, want)
	}

	// The seven labelled lines appear in this order.
	at := -1
	for _, label := range []string{"runner", "where", "tier", "plan", "report", "marker", "prompt"} {
		i := strings.Index(got, "  "+label+" ")
		if i < 0 {
			t.Fatalf("no %q line in:\n%s", label, got)
		}
		if i < at {
			t.Errorf("label %q is out of order", label)
		}
		at = i
	}

	// A gated candidate's note rides on the builder line.
	d.GateNote = "rate-limited until 00:26; the daemon would switch after start"
	if !strings.Contains(RenderDryRun(d), "(rate-limited until 00:26; the daemon would switch after start)") {
		t.Errorf("the gate note must render on the builder line:\n%s", RenderDryRun(d))
	}
}

// TestVerifyPolicyDefault pins #144's trigger: an explicit SendOptions.Verify
// wins, and a plain Send takes policy.json verify.default.
func TestVerifyPolicyDefault(t *testing.T) {
	t.Parallel()

	rt, _ := seedBound(t)
	rt.Policy.Verify = &policy.VerifyPolicy{Default: true}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !b.RoundVerify {
		t.Errorf("RoundVerify = false, want true from policy verify.default")
	}
	endProcess(t, rt, b)

	// An explicit --no-verify beats the policy default.
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "again"), SendOptions{Verify: ptr(false)}); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundVerify {
		t.Errorf("RoundVerify = true after Send{Verify: false}, want false")
	}
}

// switchSetup binds webshop on agy/test/m with a fake runner -- the setup
// TestSendHeadlessTierYoloOverrideAndRoundClose uses -- for the --candidate
// tests. Every local builder is headless, so the runner drives the round.
func switchSetup(t *testing.T) (Runtime, *fakeRunner) {
	t.Helper()
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name:      "webshop",
		Candidate: testAgyRef,
		PlannerID: testPlannerName,
		CWD:       "/repo",
		Headless:  true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return rt, fr
}

// TestSendBuilderMovesTheCandidateAndPersists pins §5.2 (a): --candidate starts
// the round on the named candidate, files its pick entry before the plan
// entry, and the change persists into the next plain send.
//
// Mutation check (required): drop the in-lock applyBuilder, leaving only the
// preflight substitution, and this test fails on the persisted
// BuilderCandidate assertion.
func TestSendBuilderMovesTheCandidateAndPersists(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# go"), SendOptions{Builder: testClaudeRef})
	if err != nil {
		t.Fatalf("Send --builder: %v", err)
	}
	if !strings.Contains(res.Pick, "claude-m") || !strings.Contains(res.Pick, "policy bypassed") {
		t.Errorf("res.Pick = %q, want a pick line naming claude-m with policy bypassed", res.Pick)
	}

	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.BuilderCandidate != testClaudeRef {
		t.Fatalf("BuilderCandidate = %q, want %q", stored.BuilderCandidate, testClaudeRef)
	}
	if stored.Builder.Kind != "claude" {
		t.Errorf("Builder.Kind = %q, want claude", stored.Builder.Kind)
	}

	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	if fr.specs[0].Argv[0] != "claude" {
		t.Errorf("argv[0] = %q, want the new candidate's binary claude", fr.specs[0].Argv[0])
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	planIdx := -1
	for i, e := range entries {
		if e.Round == 1 && e.Kind == store.KindPlan {
			planIdx = i
			break
		}
	}
	if planIdx < 1 {
		t.Fatalf("plan entry not found after a bind pick: %+v", entries)
	}
	if prev := entries[planIdx-1]; prev.Kind != store.KindPick || prev.Direction != store.DirToPlanner || !strings.Contains(prev.Note, testClaudeRef) {
		t.Errorf("entry before the plan = %+v, want a pick naming %s", prev, testClaudeRef)
	}

	endProcess(t, rt, stored)
	res2, err := Send(context.Background(), rt, "webshop", writePlan(t, "# again"), SendOptions{})
	if err != nil {
		t.Fatalf("plain Send: %v", err)
	}
	if res2.Pick != "" {
		t.Errorf("res2.Pick = %q, want empty for a plain send", res2.Pick)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2", len(fr.specs))
	}
	if fr.specs[1].Argv[0] != "claude" {
		t.Errorf("second argv[0] = %q, want claude (the change must persist)", fr.specs[1].Argv[0])
	}
}

// TestSendRecordsPickAndPlanInOrderInOneWrite pins #471: a --candidate send
// writes the pick entry and the plan entry in one write, so they land with
// consecutive seqs in that order, and the binding carries the spawned pid.
func TestSendRecordsPickAndPlanInOrderInOneWrite(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# go"), SendOptions{Builder: testClaudeRef}); err != nil {
		t.Fatalf("Send --builder: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(fr.handles) != 1 {
		t.Fatalf("handles = %d, want 1", len(fr.handles))
	}
	if b.Builder.PID != fr.handles[0].PID {
		t.Errorf("Builder.PID = %d, want the spawned %d", b.Builder.PID, fr.handles[0].PID)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	planIdx := -1
	for i, e := range entries {
		if e.Round == b.Round && e.Kind == store.KindPlan {
			planIdx = i
			break
		}
	}
	if planIdx < 1 {
		t.Fatalf("no plan entry for round %d after a pick: %+v", b.Round, entries)
	}
	pick := entries[planIdx-1]
	if pick.Kind != store.KindPick {
		t.Fatalf("entry before the plan = %+v, want the round's pick", pick)
	}
	if pick.Round != b.Round {
		t.Errorf("pick.Round = %d, want %d", pick.Round, b.Round)
	}
	if pick.Seq == 0 || entries[planIdx].Seq != pick.Seq+1 {
		t.Errorf("seqs = pick %d, plan %d; want consecutive", pick.Seq, entries[planIdx].Seq)
	}

	endProcess(t, rt, b)
}

// TestSendBuilderRefusedWhileRoundOpen pins §5.2 (b): an open round is
// refused before anything is staged or spawned.
func TestSendBuilderRefusedWhileRoundOpen(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# one"), SendOptions{}); err != nil {
		t.Fatalf("prime Send: %v", err)
	}

	before, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	planPath := rt.Store.PlanPath("webshop", 1)
	beforePlan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read staged plan: %v", err)
	}

	_, err = Send(context.Background(), rt, "webshop", writePlan(t, "# two"), SendOptions{Builder: testClaudeRef})
	if err == nil {
		t.Fatal("Send --builder on an open round must be refused")
	}
	if !strings.Contains(err.Error(), "has round 1 open") || !strings.Contains(err.Error(), "relevo stop webshop ends it") {
		t.Errorf("err = %q, want the round-open refusal", err.Error())
	}

	after, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("log grew from %d to %d entries; a refusal writes nothing", len(before), len(after))
	}
	afterPlan, err := os.ReadFile(planPath)
	if err != nil || string(afterPlan) != string(beforePlan) {
		t.Errorf("plan was restaged: got (%q, %v), want %q", string(afterPlan), err, string(beforePlan))
	}
	if len(fr.specs) != 1 {
		t.Errorf("a refused send started a process: %d specs", len(fr.specs))
	}
}

// TestSendRefusedWhileDoneMarkerNotIngested pins that a round whose builder
// has already written its completion marker, but whose close the daemon has
// not yet ingested, is refused: the round is over, and restaging its plan
// would start a second builder the daemon would close on the stale marker.
func TestSendRefusedWhileDoneMarkerNotIngested(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# one"), SendOptions{}); err != nil {
		t.Fatalf("prime Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	endProcess(t, rt, b)

	touch(t, rt.Store.DonePath("webshop", 1))
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("# report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	before, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	planPath := rt.Store.PlanPath("webshop", 1)
	beforePlan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read staged plan: %v", err)
	}

	_, err = Send(context.Background(), rt, "webshop", writePlan(t, "# two"), SendOptions{})
	if err == nil {
		t.Fatal("Send must be refused while the round's done marker is on disk")
	}
	if !errors.Is(err, ErrReportPending) {
		t.Errorf("err = %v, want it to wrap ErrReportPending", err)
	}
	if !strings.Contains(err.Error(), "001-done") {
		t.Errorf("err = %q, want it to name the 001-done marker", err.Error())
	}

	after, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("log grew from %d to %d entries; a refusal writes nothing", len(before), len(after))
	}
	afterPlan, err := os.ReadFile(planPath)
	if err != nil || string(afterPlan) != string(beforePlan) {
		t.Errorf("plan was restaged: got (%q, %v), want %q", string(afterPlan), err, string(beforePlan))
	}
	if len(fr.specs) != 1 {
		t.Errorf("a refused send started a process: %d specs", len(fr.specs))
	}
}

// TestSendRefusedWhileReportNotIngested is the same refusal for a round whose
// builder wrote only its report: the report alone proves the builder is done,
// even though the marker has not appeared yet.
func TestSendRefusedWhileReportNotIngested(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# one"), SendOptions{}); err != nil {
		t.Fatalf("prime Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	endProcess(t, rt, b)

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("# report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	before, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	planPath := rt.Store.PlanPath("webshop", 1)
	beforePlan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read staged plan: %v", err)
	}

	_, err = Send(context.Background(), rt, "webshop", writePlan(t, "# two"), SendOptions{})
	if err == nil {
		t.Fatal("Send must be refused while the round's report is on disk")
	}
	if !errors.Is(err, ErrReportPending) {
		t.Errorf("err = %v, want it to wrap ErrReportPending", err)
	}
	if !strings.Contains(err.Error(), "001-report.md") {
		t.Errorf("err = %q, want it to name 001-report.md", err.Error())
	}

	after, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("log grew from %d to %d entries; a refusal writes nothing", len(before), len(after))
	}
	afterPlan, err := os.ReadFile(planPath)
	if err != nil || string(afterPlan) != string(beforePlan) {
		t.Errorf("plan was restaged: got (%q, %v), want %q", string(afterPlan), err, string(beforePlan))
	}
	if len(fr.specs) != 1 {
		t.Errorf("a refused send started a process: %d specs", len(fr.specs))
	}
}

// TestSendAfterExitWithoutReportStillResends pins the other side of the rule:
// a builder that died without writing either file leaves the round re-sendable,
// exactly as before.
func TestSendAfterExitWithoutReportStillResends(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# one"), SendOptions{}); err != nil {
		t.Fatalf("prime Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	endProcess(t, rt, b)

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# two"), SendOptions{})
	if err != nil {
		t.Fatalf("Send after an exit without a report: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("Round = %d, want 1 (Send never advances the round)", res.Round)
	}
	if len(fr.specs) != 2 {
		t.Errorf("specs = %d, want a second process started", len(fr.specs))
	}
}

// TestSendProceedsOnceRoundClosed pins that the refusal is tied to the current
// round, not to the files: once the round closes (the daemon ingests the marker
// and advances), the next round has no marker and the send goes through, staged
// at the new round's plan path.
func TestSendProceedsOnceRoundClosed(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# one"), SendOptions{}); err != nil {
		t.Fatalf("prime Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	endProcess(t, rt, b)
	touch(t, rt.Store.DonePath("webshop", 1))
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("# report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if err := rt.Store.Save(got); err != nil {
		t.Fatalf("save the closed binding: %v", err)
	}
	beforePlan, err := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if err != nil {
		t.Fatalf("read round-1 plan: %v", err)
	}

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# two"), SendOptions{})
	if err != nil {
		t.Fatalf("Send after the round closed: %v", err)
	}
	if res.Round != 2 {
		t.Errorf("Round = %d, want 2", res.Round)
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", 2)); err != nil {
		t.Errorf("plan not staged at 002-plan.md: %v", err)
	}
	afterPlan, err := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if err != nil || string(afterPlan) != string(beforePlan) {
		t.Errorf("round-1 plan changed: got (%q, %v), want %q", string(afterPlan), err, string(beforePlan))
	}
	if len(fr.specs) != 2 {
		t.Errorf("specs = %d, want a second process started", len(fr.specs))
	}
}

// TestSendBuilderUnknownTokenWritesNothing pins §5.2 (c): a token that does
// not resolve is refused with ErrBadBuilder before any write.
func TestSendBuilderUnknownTokenWritesNothing(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)
	before, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}

	_, err = Send(context.Background(), rt, "webshop", writePlan(t, "# x"), SendOptions{Builder: "agy/test/nope"})
	if err == nil {
		t.Fatal("Send --builder with an unknown token must be refused")
	}
	if !errors.Is(err, ErrBadBuilder) {
		t.Errorf("err = %v, want it to wrap ErrBadBuilder", err)
	}
	if !strings.Contains(err.Error(), "unknown candidate") {
		t.Errorf("err = %q, want the unknown-candidate cause", err.Error())
	}

	after, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("log grew from %d to %d entries; a refusal writes nothing", len(before), len(after))
	}
	if len(fr.specs) != 0 {
		t.Errorf("a refused send started a process: %d specs", len(fr.specs))
	}
}

// TestSendBuilderGatedTokenProceeds pins §5.2 (d): an explicit pick of a
// rate-limited candidate proceeds, and the pick line records the bypass and
// names the live gate.
func TestSendBuilderGatedTokenProceeds(t *testing.T) {
	t.Parallel()

	rt, _ := switchSetup(t)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testClaudeRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# go"), SendOptions{Builder: testClaudeRef})
	if err != nil {
		t.Fatalf("Send --builder with a gated token: %v", err)
	}
	if !strings.Contains(res.Pick, "policy bypassed") {
		t.Errorf("res.Pick = %q, want policy bypassed", res.Pick)
	}
	if !strings.Contains(res.Pick, "gated: rate-limited") {
		t.Errorf("res.Pick = %q, want the live gate named", res.Pick)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", stored.BuilderCandidate, testClaudeRef)
	}
}

// TestSendDryRunBuilderMakesNoWrites pins §5.2 (e): the dry run reports the
// new candidate and its argv, and writes nothing.
func TestSendDryRunBuilderMakesNoWrites(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)
	before, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}

	d, err := SendDryRun(context.Background(), rt, "webshop", writePlan(t, "# dry"), SendOptions{Builder: testClaudeRef})
	if err != nil {
		t.Fatalf("SendDryRun --builder: %v", err)
	}
	if d.Candidate != testClaudeRef {
		t.Errorf("Candidate = %q, want %q", d.Candidate, testClaudeRef)
	}
	if !strings.HasPrefix(d.Where, "claude") {
		t.Errorf("Where = %q, want it to name the new candidate's binary", d.Where)
	}

	if len(fr.specs) != 0 {
		t.Errorf("a dry run started a process: %d specs", len(fr.specs))
	}
	after, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("log grew from %d to %d entries; a dry run writes nothing", len(before), len(after))
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", 1)); !os.IsNotExist(err) {
		t.Errorf("a dry run staged a plan: %v", err)
	}
}

// TestSendBuilderSameCandidateIsPlainSend pins §5.2 (f): naming the binding's
// own candidate is a no-op -- no pick entry, no tier change, and the send
// proceeds exactly as a plain one.
func TestSendBuilderSameCandidateIsPlainSend(t *testing.T) {
	t.Parallel()

	rt, fr := switchSetup(t)

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# go"), SendOptions{Builder: testAgyRef})
	if err != nil {
		t.Fatalf("Send --builder with the current token: %v", err)
	}
	if res.Pick != "" {
		t.Errorf("res.Pick = %q, want empty for a no-op", res.Pick)
	}
	if len(fr.specs) != 1 || fr.specs[0].Argv[0] != "agy" {
		t.Fatalf("specs = %+v, want exactly one agy round", fr.specs)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var picks int
	var plan bool
	for _, e := range entries {
		if e.Round == 1 && e.Kind == store.KindPick {
			picks++
		}
		if e.Round == 1 && e.Kind == store.KindPlan {
			plan = true
		}
	}
	if picks != 1 {
		t.Errorf("round 1 pick entries = %d, want only the bind's", picks)
	}
	if !plan {
		t.Error("no plan entry: the send did not proceed normally")
	}
}
