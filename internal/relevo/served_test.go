package relevo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// servedTierCandidatesJSON has one builder candidate with a "read" tier
// default, so tests can exercise "candidate over policy" without a bespoke
// fixture per test.
const servedTierCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"],"tier":"read"}
]`

// servedNoTierCandidateJSON has one builder candidate with no tier of its
// own, so ServedBuilderTier's chain falls through to policy/harness.
const servedNoTierCandidateJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
]`

func TestResolveServedTier(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, servedTierCandidatesJSON)
	token := "claude/test/m"

	// explicit beats candidate beats policy beats harness.
	rt := Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "harness"}}}
	if got, err := ResolveServedTier(rt, token, "edit"); err != nil || got != harness.TierEdit {
		t.Fatalf("explicit edit: got %v, err %v, want edit, nil", got, err)
	}

	// candidate beats policy (explicit empty).
	rt = Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "harness"}}}
	if got, err := ResolveServedTier(rt, token, ""); err != nil || got != harness.TierRead {
		t.Fatalf("candidate over policy: got %v, err %v, want read, nil", got, err)
	}

	// policy beats harness (no candidate tier, no explicit).
	rt = Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "edit"}}}
	if got, err := ResolveServedTier(rt, "opencode/test/m", ""); err != nil || got != harness.TierEdit {
		t.Fatalf("policy over harness: got %v, err %v, want edit, nil", got, err)
	}

	// Unresolvable token contributes nothing: falls through to policy.
	rt = Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "edit"}}}
	if got, err := ResolveServedTier(rt, "claude/unknown/model", ""); err != nil || got != harness.TierEdit {
		t.Fatalf("unresolvable token: got %v, err %v, want edit, nil", got, err)
	}

	// Everything empty -> harness.
	rt = Runtime{Candidates: nil, Policy: policy.Policy{}}
	if got, err := ResolveServedTier(rt, "", ""); err != nil || got != harness.TierHarness {
		t.Fatalf("all empty: got %v, err %v, want harness, nil", got, err)
	}

	// Explicit above max_tier -> ErrTierAboveMax.
	rt = Runtime{Candidates: set, Policy: policy.Policy{}}
	if _, err := ResolveServedTier(rt, token, "yolo"); !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("explicit above max_tier: err = %v, want ErrTierAboveMax", err)
	}

	// Policy tier above max_tier with explicit "" -> ErrTierAboveMax.
	rt = Runtime{Candidates: nil, Policy: policy.Policy{Tier: map[string]string{"builder": "yolo"}}}
	if _, err := ResolveServedTier(rt, "", ""); !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("policy tier above max_tier: err = %v, want ErrTierAboveMax", err)
	}

	// explicit=yolo, MaxTier=yolo -> ok (allowYolo is never consulted).
	rt = Runtime{Candidates: nil, Policy: policy.Policy{MaxTier: "yolo"}}
	if got, err := ResolveServedTier(rt, "", "yolo"); err != nil || got != harness.TierYolo {
		t.Fatalf("explicit yolo at max_tier yolo: got %v, err %v, want yolo, nil", got, err)
	}

	// Malformed explicit -> ParseTier's error.
	rt = Runtime{Candidates: nil, Policy: policy.Policy{}}
	if _, err := ResolveServedTier(rt, "", "bogus"); err == nil {
		t.Fatal("malformed explicit: want error, got nil")
	}
}

func TestServedBuilderTier(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, servedNoTierCandidateJSON)

	// Policy tier set -> that tier (candidate token resolved via PickServedCandidate("") has no tier of its own).
	rt := Runtime{Candidates: set, Policy: policy.Policy{Tier: map[string]string{"builder": "edit"}}, Now: func() time.Time { return baseTime }, Gates: testGateKV(t)}
	if got := ServedBuilderTier(rt); got != harness.TierEdit {
		t.Fatalf("policy tier set: got %v, want edit", got)
	}

	// Refused chain (policy tier above max_tier) -> harness, not an error.
	rt = Runtime{Candidates: nil, Policy: policy.Policy{Tier: map[string]string{"builder": "yolo"}}, Now: func() time.Time { return baseTime }}
	if got := ServedBuilderTier(rt); got != harness.TierHarness {
		t.Fatalf("refused chain: got %v, want harness", got)
	}
}

// TestServedBuilderTierFollowsTheRoleRegistry pins that the builder tier a
// served round resolves comes from the runtime's roles registry, not from
// whichever candidate the legacy fallback happens to rank first.
func TestServedBuilderTierFollowsTheRoleRegistry(t *testing.T) {
	t.Parallel()
	set := candidateSet(t, servedNoTierCandidateJSON)

	actorsSection := map[string]roles.Actor{
		"builder": {
			Agent:      "plan-executor",
			Candidates: []roles.Entry{{Candidate: "claude/test/m"}},
			Tier:       "yolo",
		},
	}
	rf, _, err := roles.FromActors(nil, actorsSection)
	if err != nil {
		t.Fatalf("roles.FromActors: %v", err)
	}
	reg, err := roles.Build(rf, set, policy.Policy{MaxTier: "yolo"})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	rt := Runtime{
		Candidates: set,
		Policy:     policy.Policy{MaxTier: "yolo"},
		Registry:   reg,
		Gates:      testGateKV(t),
		Now:        func() time.Time { return baseTime },
	}
	if got := ServedBuilderTier(rt); got != harness.TierYolo {
		t.Fatalf("registry builder tier: got %v, want yolo", got)
	}

	// The same runtime without the registry falls back to the legacy
	// derivation, which carries no role tier: assert only that it is not
	// yolo, since which tier it names is not what this test pins.
	rt.Registry = nil
	if got := ServedBuilderTier(rt); got == harness.TierYolo {
		t.Fatalf("nil registry: got %v, want anything but yolo", got)
	}
}

// TestPickServedCandidateRefusesAnUnservedExplicitToken pins the pick's
// explicit refusal: a token the role refuses comes back as that refusal
// instead of being resolved outside the role's list, so the create that named
// it must answer rather than serve a different candidate. The omitted token
// still resolves to the role's ranked pick.
func TestPickServedCandidateRefusesAnUnservedExplicitToken(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/b", "claude/test/a"}},
	})
	rt := Runtime{
		Candidates: set,
		Registry:   reg,
		Gates:      testGateKV(t),
		Now:        func() time.Time { return baseTime },
	}

	// c is configured, but the row does not list it for builder.
	if _, _, err := PickServedCandidate(rt, "claude/test/c"); !errors.Is(err, ErrRoleNotServed) {
		t.Fatalf("unserved explicit token: err = %v, want ErrRoleNotServed", err)
	}

	// A listed explicit token still resolves.
	token, kind, err := PickServedCandidate(rt, "claude/test/a")
	if err != nil || token != "claude/test/a" || kind != "claude" {
		t.Fatalf("served explicit token: got %q/%q, err %v; want claude/test/a/claude, nil", token, kind, err)
	}

	// The omitted token is the ranked pick, first in the row's list.
	token, kind, err = PickServedCandidate(rt, "")
	if err != nil || token != "claude/test/b" || kind != "claude" {
		t.Fatalf("omitted token: got %q/%q, err %v; want claude/test/b/claude, nil", token, kind, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\nOutput: %s", strings.Join(args, " "), dir, err, string(out))
	}

	// A repo these tests create gets auto-maintenance off. Every `git commit`
	// otherwise spawns `git maintenance run --auto --quiet --detach`, which
	// outlives the command and writes under .git/objects while t.TempDir()'s
	// RemoveAll is removing the tree -- and that cleanup failure fails the
	// test, not just the teardown (#304). Repo-local config, so every later
	// git command on it inherits it, including ones the code under test runs.
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return string(out)
}

func TestRoundStateOf(t *testing.T) {
	t.Parallel()

	// Arm 1: StateNeedsYou
	b1 := store.Binding{
		State: store.StateNeedsYou,
		Round: 1,
	}
	if got := RoundStateOf(b1, nil); got != remote.RoundNeedsYou {
		t.Fatalf("arm 1 (needs_you): got %v, want %v", got, remote.RoundNeedsYou)
	}

	// Arm 2: RoundRunning (plan sent, report not yet sent)
	b2 := store.Binding{
		State: store.StateActive,
		Round: 1,
	}
	entries2 := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}
	if got := RoundStateOf(b2, entries2); got != remote.RoundRunning {
		t.Fatalf("arm 2 (running): got %v, want %v", got, remote.RoundRunning)
	}

	// Arm 3: RoundClosed (Serve.ClosedRound > Serve.AckedRound)
	b3 := store.Binding{
		State: store.StateActive,
		Round: 2,
		Serve: &store.ServeFacts{
			ClosedRound: 1,
			AckedRound:  0,
		},
	}
	// Entries has both plan and report for round 1
	entries3 := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport},
	}
	if got := RoundStateOf(b3, entries3); got != remote.RoundClosed {
		t.Fatalf("arm 3 (closed): got %v, want %v", got, remote.RoundClosed)
	}

	// Arm 4: RoundIdle
	b4 := store.Binding{
		State: store.StateActive,
		Round: 2,
		Serve: &store.ServeFacts{
			ClosedRound: 1,
			AckedRound:  1,
		},
	}
	if got := RoundStateOf(b4, entries3); got != remote.RoundIdle {
		t.Fatalf("arm 4 (idle): got %v, want %v", got, remote.RoundIdle)
	}
}

// TestRoundStateOfQueued pins #285: an open plan entry with a non-zero
// QueuedAt is queued, not running; zero QueuedAt is running as before; and
// needs_you still wins over queued, exactly as it wins over running.
//
// Mutation check: drop the `!b.QueuedAt.IsZero()` arm from RoundStateOf and
// this fails on the first case.
func TestRoundStateOfQueued(t *testing.T) {
	t.Parallel()

	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}

	queued := store.Binding{
		State:    store.StateActive,
		Round:    1,
		QueuedAt: time.Unix(1_700_000_000, 0),
	}
	if got := RoundStateOf(queued, entries); got != remote.RoundQueued {
		t.Fatalf("open plan + QueuedAt set: got %v, want %v", got, remote.RoundQueued)
	}

	running := store.Binding{
		State: store.StateActive,
		Round: 1,
	}
	if got := RoundStateOf(running, entries); got != remote.RoundRunning {
		t.Fatalf("open plan + zero QueuedAt: got %v, want %v", got, remote.RoundRunning)
	}

	needsYou := store.Binding{
		State:    store.StateNeedsYou,
		Round:    1,
		QueuedAt: time.Unix(1_700_000_000, 0),
	}
	if got := RoundStateOf(needsYou, entries); got != remote.RoundNeedsYou {
		t.Fatalf("needs_you + QueuedAt set: got %v, want %v (needs_you wins)", got, remote.RoundNeedsYou)
	}
}

func TestServedViewReportOutcome(t *testing.T) {
	t.Parallel()

	b := store.Binding{
		Name:             "api",
		State:            store.StateActive,
		Round:            3,
		BuilderCandidate: "claude-sonnet",
		RoundCap:         10,
		RoundTimeoutMS:   30000,
		Serve: &store.ServeFacts{
			ClosedRound:  2,
			AckedRound:   1,
			ResultCommit: "c222",
			DirtyCommit:  "d222",
		},
	}

	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done"},
		{Round: 2, Kind: store.KindReport, Outcome: "blocked"},
		{Round: 2, Kind: store.KindReport, Outcome: "halted"},
	}

	view := ServedView(b, entries, "", "")
	if view.ReportOutcome != "halted" {
		t.Fatalf("ReportOutcome: got %q, want %q", view.ReportOutcome, "halted")
	}
	if view.RoundState != remote.RoundClosed {
		t.Fatalf("RoundState: got %v, want %v", view.RoundState, remote.RoundClosed)
	}
	if view.ResultCommit != "c222" {
		t.Fatalf("ResultCommit: got %q, want %q", view.ResultCommit, "c222")
	}
	if view.DirtyCommit != "d222" {
		t.Fatalf("DirtyCommit: got %q, want %q", view.DirtyCommit, "d222")
	}

	// With no report entries for ClosedRound
	viewNoReports := ServedView(b, nil, "", "")
	if viewNoReports.ReportOutcome != "" {
		t.Fatalf("ReportOutcome with no entries: got %q, want %q", viewNoReports.ReportOutcome, "")
	}
}

func TestServedViewDiffFacts(t *testing.T) {
	t.Parallel()

	b := store.Binding{
		Name:             "api",
		State:            store.StateActive,
		Round:            3,
		BuilderCandidate: "claude-sonnet",
		Serve: &store.ServeFacts{
			ClosedRound: 2,
			AckedRound:  1,
		},
	}

	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindDiff, Note: "old round's diff", Commits: 9, Tree: "dirty"},
		{Round: 2, Kind: store.KindDiff, Note: "1 file, +1 -0; 1 commit, clean", Commits: 1, Tree: "clean"},
	}

	view := ServedView(b, entries, "", "")
	if view.DiffNote != "1 file, +1 -0; 1 commit, clean" {
		t.Fatalf("DiffNote: got %q, want the round 2 diff entry's note", view.DiffNote)
	}
	if view.DiffCommits != 1 {
		t.Fatalf("DiffCommits: got %d, want 1", view.DiffCommits)
	}
	if view.DiffTree != "clean" {
		t.Fatalf("DiffTree: got %q, want clean", view.DiffTree)
	}

	// With no diff entry for ClosedRound, every fact stays zero.
	viewNoDiff := ServedView(b, entries[:1], "", "")
	if viewNoDiff.DiffNote != "" || viewNoDiff.DiffCommits != 0 || viewNoDiff.DiffTree != "" {
		t.Fatalf("diff facts with no matching entry: got %+v, want all zero", viewNoDiff)
	}
}

// TestServedViewStopped pins #344: the view names how the closed round was
// stopped, read from the KindStop entry closeStopped writes for that round,
// and leaves the field empty when the only stop entry names an earlier round.
func TestServedViewStopped(t *testing.T) {
	t.Parallel()

	b := store.Binding{
		Name:  "api",
		State: store.StateActive,
		Round: 3,
		Serve: &store.ServeFacts{ClosedRound: 2, AckedRound: 1},
	}

	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindStop, Note: "stopped/killed"},
		{Round: 2, Kind: store.KindStop, Note: "stopped/killed"},
	}
	if view := ServedView(b, entries, "", ""); view.Stopped != "killed" {
		t.Fatalf("Stopped = %q, want killed", view.Stopped)
	}

	// The same entry on an earlier round leaves the field empty.
	if view := ServedView(b, entries[:1], "", ""); view.Stopped != "" {
		t.Fatalf("Stopped = %q with only an earlier round's stop entry, want empty", view.Stopped)
	}

	// No stop entry at all: still empty.
	if view := ServedView(b, nil, "", ""); view.Stopped != "" {
		t.Fatalf("Stopped = %q with no stop entry, want empty", view.Stopped)
	}
}

// TestServedViewCarriesClosedRoundUsage checks that the view ships the
// closed round's usage the way it ships ReportOutcome (#216): from the
// newest KindReport entry for Serve.ClosedRound, and only from it.
func TestServedViewCarriesClosedRoundUsage(t *testing.T) {
	t.Parallel()

	closed := usage.Usage{
		Harness: "opencode",
		Model:   "haiku",
		Tokens:  usage.Tokens{In: 1000, Out: 200},
		Cost:    usage.Cost{USD: 0.12, Basis: usage.Measured},
	}
	old := usage.Usage{
		Harness: "claude",
		Cost:    usage.Cost{Basis: usage.Unknown},
		Note:    "shared cwd",
	}
	b := store.Binding{
		Name:             "api",
		State:            store.StateActive,
		Round:            3,
		BuilderCandidate: "claude-sonnet",
		Serve: &store.ServeFacts{
			ClosedRound: 2,
			AckedRound:  1,
		},
	}

	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done", Usage: &old},
		{Round: 2, Kind: store.KindReport, Outcome: "blocked", Usage: &closed},
	}

	view := ServedView(b, entries, "", "")
	if view.Usage == nil || *view.Usage != closed {
		t.Fatalf("Usage = %+v, want the closed round's report entry's usage", view.Usage)
	}

	// The closed round's report carries no usage: nil, not the older
	// round's figure.
	entriesNoUsage := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done", Usage: &old},
		{Round: 2, Kind: store.KindReport, Outcome: "blocked"},
	}
	viewNoUsage := ServedView(b, entriesNoUsage, "", "")
	if viewNoUsage.Usage != nil {
		t.Fatalf("Usage = %+v, want nil when the closed round's report has none", viewNoUsage.Usage)
	}

	// No report at all: nil.
	viewNoReports := ServedView(b, nil, "", "")
	if viewNoReports.Usage != nil {
		t.Fatalf("Usage with no entries = %+v, want nil", viewNoReports.Usage)
	}
}

// TestServedViewCarriesRusage pins the closed round's cgroup measurement on
// the wire, the same way TestServedViewCarriesClosedRoundUsage pins Usage
// (#244, #216).
func TestServedViewCarriesRusage(t *testing.T) {
	t.Parallel()

	closed := store.Rusage{CPUMS: 12300, PeakMemBytes: 850 << 20}
	b := store.Binding{
		Name:             "api",
		State:            store.StateActive,
		Round:            3,
		BuilderCandidate: "claude-sonnet",
		Serve: &store.ServeFacts{
			ClosedRound: 2,
			AckedRound:  1,
		},
	}

	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done"},
		{Round: 2, Kind: store.KindReport, Outcome: "blocked", Rusage: &closed},
	}
	view := ServedView(b, entries, "", "")
	if view.Rusage == nil || *view.Rusage != closed {
		t.Fatalf("Rusage = %+v, want the closed round's report entry's rusage", view.Rusage)
	}

	// The closed round's report carries no rusage: nil, not an older one,
	// and nil when the round was not a scope (a plain spawn).
	entriesNoRusage := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done", Rusage: &closed},
		{Round: 2, Kind: store.KindReport, Outcome: "blocked"},
	}
	viewNoRusage := ServedView(b, entriesNoRusage, "", "")
	if viewNoRusage.Rusage != nil {
		t.Fatalf("Rusage = %+v, want nil when the closed round's report has none", viewNoRusage.Rusage)
	}
}

// TestServedViewCarriesStalledSince pins #252's wire field: a stalled binding
// ships its stamp to the client, and an unstalled one ships the zero time.
func TestServedViewCarriesStalledSince(t *testing.T) {
	t.Parallel()

	b := store.Binding{
		Name:  "api",
		State: store.StateActive,
		Round: 1,
	}
	stalled := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	b.StalledSince = stalled

	view := ServedView(b, nil, "", "")
	if !view.StalledSince.Equal(stalled) {
		t.Fatalf("StalledSince = %s, want %s", view.StalledSince, stalled)
	}

	b.StalledSince = time.Time{}
	view = ServedView(b, nil, "", "")
	if !view.StalledSince.IsZero() {
		t.Fatalf("StalledSince = %s, want zero for an unstalled binding", view.StalledSince)
	}
}

func TestCloseServedRoundClean(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)

	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relevo/api")

	branchHead := strings.TrimSpace(runGit(t, bare, "rev-parse", "refs/heads/relevo/api"))

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relevo/api")

	rt := Runtime{Git: client}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relevo/api",
		Worktree: wt,
		Round:    2, // queueReport already advanced Round from 1 to 2
		Serve: &store.ServeFacts{
			BareRepo: bare,
		},
	}

	res := closeServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 1 {
		t.Fatalf("ClosedRound: got %d, want 1", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != branchHead {
		t.Fatalf("ResultCommit: got %q, want %q", res.Serve.ResultCommit, branchHead)
	}
	if res.Serve.DirtyCommit != "" {
		t.Fatalf("DirtyCommit on clean worktree: got %q, want empty", res.Serve.DirtyCommit)
	}

	// Side ref must not exist
	sideSHA, ok, err := client.RefSHA(ctx, bare, "refs/relevo/api/round-1")
	if err != nil || ok {
		t.Fatalf("side ref exists on clean worktree: sha=%q, ok=%v, err=%v", sideSHA, ok, err)
	}

	// Branch must be unchanged
	headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relevo/api")
	if err != nil || !ok || headAfter != branchHead {
		t.Fatalf("branch changed: got %q, want %q", headAfter, branchHead)
	}
}

func TestCloseServedRoundDirty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)

	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relevo/api")

	branchHead := strings.TrimSpace(runGit(t, bare, "rev-parse", "refs/heads/relevo/api"))

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relevo/api")

	// Make worktree dirty with an untracked file
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("dirty work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{Git: client}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relevo/api",
		Worktree: wt,
		Round:    2,
		Serve: &store.ServeFacts{
			BareRepo: bare,
		},
	}

	res := closeServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 1 {
		t.Fatalf("ClosedRound: got %d, want 1", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != branchHead {
		t.Fatalf("ResultCommit: got %q, want %q", res.Serve.ResultCommit, branchHead)
	}
	if res.Serve.DirtyCommit == "" {
		t.Fatal("DirtyCommit is empty on dirty worktree")
	}

	// Side ref must exist in bare repo and equal DirtyCommit
	sideSHA, ok, err := client.RefSHA(ctx, bare, "refs/relevo/api/round-1")
	if err != nil || !ok {
		t.Fatalf("side ref missing: ok=%v, err=%v", ok, err)
	}
	if sideSHA != res.Serve.DirtyCommit {
		t.Fatalf("side ref %q != DirtyCommit %q", sideSHA, res.Serve.DirtyCommit)
	}

	// Side ref parent must be branch head
	parent := strings.TrimSpace(runGit(t, bare, "rev-parse", sideSHA+"^"))
	if parent != branchHead {
		t.Fatalf("side ref parent %q != branchHead %q", parent, branchHead)
	}

	// Branch must be unchanged
	headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relevo/api")
	if err != nil || !ok || headAfter != branchHead {
		t.Fatalf("branch changed: got %q, want %q", headAfter, branchHead)
	}
}

func TestCloseServedRoundGitFailureKeepsFacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fGit := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "commit123",
		},
		dirtyErr: errors.New("dirty failure"),
	}

	rt := Runtime{Git: fGit}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relevo/api",
		Worktree: "/tmp/fake-wt",
		Round:    2,
		Serve: &store.ServeFacts{
			BareRepo:     "/tmp/fake-bare",
			ClosedRound:  0,
			ResultCommit: "old-head",
			DirtyCommit:  "",
		},
	}

	// Must not panic, and must leave facts unchanged
	res := closeServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 0 {
		t.Fatalf("ClosedRound changed on git error: got %d, want 0", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != "old-head" {
		t.Fatalf("ResultCommit changed on git error: got %q, want old-head", res.Serve.ResultCommit)
	}
	if res.Serve.DirtyCommit != "" {
		t.Fatalf("DirtyCommit changed on git error: got %q, want empty", res.Serve.DirtyCommit)
	}
}

func TestDeliverAndSettleOwnedLeavesQueued(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:       "api",
		Owner:      "client1",
		State:      store.StateActive,
		Round:      1,
		CWD:        t.TempDir(),
		MasterMind: store.Endpoint{SessionID: "sess1", PaneID: "p1"},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("api", store.LogEntry{
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindReport,
		Payload:   "the report",
		Confirmed: false,
	}); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{Store: st, Now: time.Now}
	var got store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = deliverAndSettle(ctx, rt, tx, b)
		return err
	})
	if err != nil {
		t.Fatalf("deliverAndSettle: %v", err)
	}

	if got.State != store.StateActive {
		t.Fatalf("state changed: got %v, want %v", got.State, store.StateActive)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[0].Confirmed {
		t.Fatalf("payload was confirmed: %+v", entries)
	}
}

func TestReconcileHeadlessOwnedCloseRecordsFacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	fr := newFakeRunner()
	expectedSHA := "commit-1234567890"
	fGit := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": expectedSHA,
		},
	}

	wtDir := t.TempDir()
	bareDir := t.TempDir()

	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		State:    store.StateActive,
		Round:    1,
		RoundCap: 10,
		CWD:      wtDir,
		Builder:  store.Endpoint{Mode: store.ModeHeadless, PID: 1234},
		Branch:   "relevo/api",
		Worktree: wtDir,
		Serve: &store.ServeFacts{
			BareRepo: bareDir,
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	// Round 1 plan entry
	if err := st.AppendLog("api", store.LogEntry{
		Round:     1,
		Direction: store.DirToBuilder,
		Kind:      store.KindPrompt,
	}); err != nil {
		t.Fatal(err)
	}

	// Write report and marker
	if err := os.WriteFile(st.ReportPath("api", 1), []byte("report\n```relevo\nstatus: done\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.DonePath("api", 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{
		Store:  st,
		Git:    fGit,
		Runner: fr,
		Now:    time.Now,
	}

	var reconciled store.Binding
	err := st.WithLock(func(tx *store.Tx) error {
		var err error
		reconciled, err = Reconcile(ctx, rt, tx, b)
		return err
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if reconciled.Round != 2 {
		t.Fatalf("Round: got %d, want 2", reconciled.Round)
	}
	if reconciled.Serve == nil {
		t.Fatal("Serve is nil")
	}
	if reconciled.Serve.ClosedRound != 1 {
		t.Fatalf("Serve.ClosedRound: got %d, want 1", reconciled.Serve.ClosedRound)
	}
	if reconciled.Serve.ResultCommit != expectedSHA {
		t.Fatalf("Serve.ResultCommit: got %q, want %q", reconciled.Serve.ResultCommit, expectedSHA)
	}
}

// ownedExitFixture is an owned (served) binding with round 1's plan logged and
// its builder process gone, for the close paths that do not see a marker.
func ownedExitFixture(t *testing.T) (Runtime, store.Binding, *fakeRunner, string) {
	t.Helper()
	st := store.New(t.TempDir())
	fr := newFakeRunner()
	sha := "commit-abcdef"
	wtDir := t.TempDir()
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		State:    store.StateActive,
		Round:    1,
		RoundCap: 10,
		CWD:      wtDir,
		Builder:  store.Endpoint{Mode: store.ModeHeadless, PID: 1234},
		Branch:   "relevo/api",
		Worktree: wtDir,
		Serve:    &store.ServeFacts{BareRepo: t.TempDir()},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatal(err)
	}
	fr.script(1234, false)
	fr.exit(1234, 0)
	rt := Runtime{
		Store:  st,
		Git:    &fakeGit{refSHA: map[string]string{"refs/heads/relevo/api": sha}},
		Runner: fr,
		Now:    time.Now,
	}
	return rt, b, fr, sha
}

func reconcileOwned(t *testing.T, rt Runtime, b store.Binding) store.Binding {
	t.Helper()
	var got store.Binding
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = Reconcile(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return got
}

// TestReconcileHeadlessOwnedUnmarkedCloseRecordsFacts: a served builder that
// exits after writing its report but without the done marker closes the round
// unmarked -- and must record ClosedRound as a marked close does, or the owner
// sees the round as idle and never fetches it (the flaky
// TestRemoteRoundCollectedAfterClientWasAway hit exactly this).
func TestReconcileHeadlessOwnedUnmarkedCloseRecordsFacts(t *testing.T) {
	t.Parallel()

	rt, b, _, sha := ownedExitFixture(t)
	if err := os.WriteFile(rt.Store.ReportPath("api", 1), []byte("report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := reconcileOwned(t, rt, b)
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 (closed)", got.Round)
	}
	if got.Serve == nil || got.Serve.ClosedRound != 1 {
		t.Fatalf("Serve.ClosedRound = %+v, want 1", got.Serve)
	}
	if got.Serve.ResultCommit != sha {
		t.Errorf("Serve.ResultCommit = %q, want %q", got.Serve.ResultCommit, sha)
	}
}

// TestReconcileHeadlessOwnedStopRequestedExitRecordsFacts: a served builder
// that exits while a stop is requested closes through closeStopped, and the
// served round's facts are recorded there too.
func TestReconcileHeadlessOwnedStopRequestedExitRecordsFacts(t *testing.T) {
	t.Parallel()

	rt, b, _, _ := ownedExitFixture(t)
	b.StopRequestedAt = time.Now().Add(-time.Second)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	got := reconcileOwned(t, rt, b)
	if got.Serve == nil || got.Serve.ClosedRound != 1 {
		t.Fatalf("Serve.ClosedRound = %+v, want 1", got.Serve)
	}
}

func TestLiveViewOf(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	started := at.Add(-10 * time.Minute)
	lastProg := at.Add(-2 * time.Minute)
	exploring := at.Add(-5 * time.Minute)
	gateStarted := at.Add(-1 * time.Minute)

	u := &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}}
	row := view.BindingStatus{
		Headless: &view.HeadlessInfo{
			PID:       1234,
			StartedAt: started,
			ExitCode:  "3",
			Tail:      []string{"line1", "line2"},
		},
		LiveUsage:        u,
		RoundPriorTokens: usage.Tokens{In: 500, Out: 250},
		Live:             &view.LiveDiff{Files: 4, Added: 20, Removed: 5, Shared: true},
		LastProgressAt:   lastProg,
	}
	b := store.Binding{
		ExploringSince: exploring,
		GateRun: &store.GateRun{
			StartedAt: gateStarted.Unix(),
		},
	}

	v := liveViewOf(row, b, at)
	if v == nil {
		t.Fatal("liveViewOf returned nil")
	}
	if !v.At.Equal(at) {
		t.Errorf("At = %v, want %v", v.At, at)
	}
	if v.PID != 1234 {
		t.Errorf("PID = %d, want 1234", v.PID)
	}
	if !v.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", v.StartedAt, started)
	}
	if v.ExitCode != "3" {
		t.Errorf("ExitCode = %q, want 3", v.ExitCode)
	}
	if len(v.Tail) != 2 || v.Tail[0] != "line1" || v.Tail[1] != "line2" {
		t.Errorf("Tail = %v, want [line1 line2]", v.Tail)
	}
	if v.Usage != u {
		t.Errorf("Usage = %v, want %v", v.Usage, u)
	}
	if v.PriorTokens != (usage.Tokens{In: 500, Out: 250}) {
		t.Errorf("PriorTokens = %+v, want in:500 out:250", v.PriorTokens)
	}
	if v.Diff == nil || v.Diff.Files != 4 || v.Diff.Added != 20 || v.Diff.Removed != 5 {
		t.Errorf("Diff = %+v, want Files:4 Added:20 Removed:5", v.Diff)
	}
	if !v.LastProgressAt.Equal(lastProg) {
		t.Errorf("LastProgressAt = %v, want %v", v.LastProgressAt, lastProg)
	}
	if !v.ExploringSince.Equal(exploring) {
		t.Errorf("ExploringSince = %v, want %v", v.ExploringSince, exploring)
	}
	if !v.GatingSince.Equal(time.Unix(gateStarted.Unix(), 0)) {
		t.Errorf("GatingSince = %v, want %v", v.GatingSince, time.Unix(gateStarted.Unix(), 0))
	}

	// nil Headless, LiveUsage and Live give zero values and nil pointers
	rowNil := view.BindingStatus{
		LastProgressAt: lastProg,
	}
	bNil := store.Binding{}
	vNil := liveViewOf(rowNil, bNil, at)
	if vNil.PID != 0 {
		t.Errorf("PID = %d, want 0", vNil.PID)
	}
	if !vNil.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero", vNil.StartedAt)
	}
	if vNil.ExitCode != "" {
		t.Errorf("ExitCode = %q, want empty", vNil.ExitCode)
	}
	if vNil.Tail != nil {
		t.Errorf("Tail = %v, want nil", vNil.Tail)
	}
	if vNil.Usage != nil {
		t.Errorf("Usage = %v, want nil", vNil.Usage)
	}
	if vNil.Diff != nil {
		t.Errorf("Diff = %v, want nil", vNil.Diff)
	}
	if !vNil.ExploringSince.IsZero() {
		t.Errorf("ExploringSince = %v, want zero", vNil.ExploringSince)
	}
	if !vNil.GatingSince.IsZero() {
		t.Errorf("GatingSince = %v, want zero", vNil.GatingSince)
	}
}

func TestServedViewPriorTokens(t *testing.T) {
	t.Parallel()

	b := store.Binding{
		Name:             "api",
		State:            store.StateActive,
		Round:            3,
		BuilderCandidate: "claude-sonnet",
		Serve: &store.ServeFacts{
			ClosedRound: 2,
			AckedRound:  1,
		},
	}

	// 1. Server entries with two switches carrying usage in the closed round -> view.PriorTokens is their sum
	entries := []store.LogEntry{
		{
			Round: 2, Kind: store.KindSwitch, Direction: store.DirToMasterMind,
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}},
		},
		{
			Round: 2, Kind: store.KindSwitch, Direction: store.DirToMasterMind,
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}},
		},
		{
			Round: 2, Kind: store.KindReport, Direction: store.DirToMasterMind, Outcome: "done",
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 300, Out: 40}},
		},
	}
	view := ServedView(b, entries, "", "")
	if view.PriorTokens == nil {
		t.Fatal("view.PriorTokens is nil, want sum of switches")
	}
	want := usage.Tokens{In: 300, Out: 80}
	if *view.PriorTokens != want {
		t.Errorf("view.PriorTokens = %+v, want %+v", *view.PriorTokens, want)
	}

	// 2. None -> nil
	entriesNoSwitch := []store.LogEntry{
		{
			Round: 2, Kind: store.KindReport, Direction: store.DirToMasterMind, Outcome: "done",
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 300, Out: 40}},
		},
	}
	viewNoSwitch := ServedView(b, entriesNoSwitch, "", "")
	if viewNoSwitch.PriorTokens != nil {
		t.Errorf("viewNoSwitch.PriorTokens = %+v, want nil", viewNoSwitch.PriorTokens)
	}

	viewEmpty := ServedView(b, nil, "", "")
	if viewEmpty.PriorTokens != nil {
		t.Errorf("viewEmpty.PriorTokens = %+v, want nil", viewEmpty.PriorTokens)
	}
}
