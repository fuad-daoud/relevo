package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// addRepo makes a directory to stand in for the mastermind's repository.
func addRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// refusedAdd asserts a refusal cut no worktree and started no process: #303
// replaced the old "no tab, no start" pane assertions with the runner's own
// process list, since a local builder is a process relevo runs.
func refusedAdd(t *testing.T, rt Runtime, fg *fakeGit) {
	t.Helper()
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("a refusal must not cut a worktree: %+v", fg.addWorktreeCalls)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refusal must start no process, got %d", got)
	}
}

// TestAddRecordsBaseRef pins #136: a peer cut from a checkout records the
// branch that checkout had checked out, asked of the source repo -- not of
// the fresh worktree -- so `relevo land` knows what to rebase onto.
func TestAddRecordsBaseRef(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123", currentBranchResult: "main"}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: repo,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got.Binding.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want %q", got.Binding.BaseRef, "main")
	}
	stored, err := rt.Store.Load("frontend")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.BaseRef != "main" {
		t.Errorf("stored BaseRef = %q, want %q", stored.BaseRef, "main")
	}
	if len(fg.currentBranchCalls) != 1 {
		t.Fatalf("CurrentBranch calls = %+v, want 1", fg.currentBranchCalls)
	}
	if fg.currentBranchCalls[0].Dir != repo {
		t.Errorf("CurrentBranch asked about %q, want the source repo %q, not the fresh worktree",
			fg.currentBranchCalls[0].Dir, repo)
	}
}

// TestAddRecordsNoBaseRefWithoutBranch pins the other half: CurrentBranch
// failing (a detached source HEAD, or no repository at all) records ""
// rather than the literal "HEAD", so land asks for --onto instead of
// fetching a ref that does not exist.
func TestAddRecordsNoBaseRefWithoutBranch(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		headCommitID:     "commit-head-123",
		currentBranchErr: errors.New("detached"),
	}
	rt := newTestRuntime(t, fg)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.Binding.BaseRef != "" {
		t.Errorf("BaseRef = %q, want \"\"", got.Binding.BaseRef)
	}
}

// TestAddRecordsRepoFromCWDNotWorktree pins #172: the peer's RepoRef is
// captured from opts.Repo, the parent checkout the worktree is cut from --
// not from the fresh worktree directory (which, before AddWorktree runs,
// is nothing to capture facts about at all, and afterwards would merely
// report the same facts back over an extra git call).
func TestAddRecordsRepoFromCWDNotWorktree(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		headCommitID:       "commit-head-123",
		repoFactsOrigin:    "git@github.com:o/r.git",
		repoFactsCommonDir: "/repo/.git",
	}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: repo,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if len(fg.repoFactsCalls) != 1 {
		t.Fatalf("RepoFacts calls = %d, want 1", len(fg.repoFactsCalls))
	}
	if fg.repoFactsCalls[0].Dir != repo {
		t.Errorf("RepoFacts called with %q, want the source repo %q, not the fresh worktree path", fg.repoFactsCalls[0].Dir, repo)
	}

	wantRepoRef := &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"}
	if !reflect.DeepEqual(got.Binding.RepoRef, wantRepoRef) {
		t.Errorf("RepoRef = %+v, want %+v", got.Binding.RepoRef, wantRepoRef)
	}
}

func TestAddCreatesAWorktreeBindingAtRoundOne(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: repo,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got.Binding.Round != 1 {
		t.Errorf("a peer builder starts at round 1, got %d", got.Binding.Round)
	}
	if got.Binding.State != store.StateActive {
		t.Errorf("state = %q, want active", got.Binding.State)
	}
	if got.Binding.ForkedFrom != "" || got.Binding.ForkedAtRound != 0 {
		t.Errorf("a peer builder was never forked from anything, got %+v", got.Binding)
	}
	if got.Branch != "relevo/frontend" {
		t.Errorf("branch = %q, want relevo/frontend", got.Branch)
	}
	if got.Base != "commit-head-123" {
		t.Errorf("base = %q, want the repo HEAD", got.Base)
	}
	if got.Binding.Repo != repo {
		t.Errorf("Binding.Repo = %q, want the source repo %q (#192)", got.Binding.Repo, repo)
	}

	stored, err := rt.Store.Load("frontend")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Branch != "relevo/frontend" || stored.Base != "commit-head-123" {
		t.Errorf("stored branch/base = (%q, %q), want (relevo/frontend, commit-head-123)", stored.Branch, stored.Base)
	}

	want := rt.Store.WorktreePath("frontend")
	if got.Worktree != want || got.Binding.CWD != want {
		t.Errorf("worktree = %q, cwd = %q, want both %q", got.Worktree, got.Binding.CWD, want)
	}
	if got.Binding.Worktree != want {
		t.Errorf("relevo must record the tree it created so it may remove it, got %q", got.Binding.Worktree)
	}

	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("addWorktreeCalls = %+v", fg.addWorktreeCalls)
	}
	call := fg.addWorktreeCalls[0]
	if call.Dir != repo || call.Path != want || call.Branch != "relevo/frontend" || call.Commit != "commit-head-123" {
		t.Errorf("worktree cut wrongly: %+v", call)
	}

	// The round log holds nothing relayed yet, only the pick entry recording
	// why relevo chose the builder it spawned (#61 step 2).
	entries, err := rt.Store.ReadLog("frontend")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("a fresh peer binding has relayed nothing but its pick, got %+v", entries)
	}
}

func TestAddRefusesAnAmbiguousCandidateBeforeCuttingAWorktree(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: "", MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if !errors.Is(err, ErrAmbiguousCandidate) {
		t.Fatalf("want ErrAmbiguousCandidate, got %v", err)
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("expected 0 addWorktreeCalls, got %d", len(fg.addWorktreeCalls))
	}
}

func TestAddResolvesTheOnlyBuilderCandidate(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)

	res, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: "", MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Binding.BuilderCandidate != "agy/test/m" {
		t.Errorf("BuilderCandidate = %q, want agy/test/m", res.Binding.BuilderCandidate)
	}
}

func TestAddRefusesADuplicateName(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	if _, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: repo,
	}); err != nil {
		t.Fatalf("first Add: %v", err)
	}

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: repo,
	})
	if err == nil {
		t.Fatal("a name already in use must be refused")
	}
}

// TestAddRefusesALongNameBeforeCuttingAWorktree pins #64: a 25-character name
// builds a 33-character builder agent name, and Add must refuse it before the
// worktree is cut -- a refused name leaves nothing behind.
func TestAddRefusesALongNameBeforeCuttingAWorktree(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)

	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33

	_, err := Add(context.Background(), rt, AddOptions{
		Name: name, Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("a refused name must not cut a worktree, calls = %+v", fg.addWorktreeCalls)
	}
	refusedAdd(t, rt, fg)
	// #303 deleted the pane client's ErrInvalidAgentName with it; the same
	// refusal is now store.ValidName's own text, wrapped by builderAgentName
	// with the length budget in it.
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Add err = %v, want the agent-name length refusal", err)
	}
	if _, loadErr := rt.Store.Load(name); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound: a refused name saves no binding", loadErr)
	}
}

func TestAddBindsAPreparedDirectoryWithCWD(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)
	prepared := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "legacy", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		Repo: addRepo(t), CWD: prepared,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.Binding.CWD != prepared {
		t.Errorf("cwd = %q, want the prepared directory %q", got.Binding.CWD, prepared)
	}
	if got.Worktree != "" || got.Binding.Worktree != "" {
		t.Error("relevo did not create this tree, so it must never record ownership of it")
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("--cwd must not cut a worktree, calls = %+v", fg.addWorktreeCalls)
	}
	if got.Binding.Branch != "" || got.Binding.Base != "" {
		t.Errorf("--cwd created no branch, so none may be recorded: (%q, %q)", got.Binding.Branch, got.Binding.Base)
	}
}

func TestAddRefusesATreeAnotherBindingDrives(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)
	prepared := addRepo(t)

	if err := rt.Store.Save(store.Binding{
		Name: "incumbent", CWD: prepared, Round: 1, State: store.StateActive,
		Builder: store.Endpoint{Mode: store.ModeHeadless},
	}); err != nil {
		t.Fatalf("seed incumbent: %v", err)
	}

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		Repo: addRepo(t), CWD: prepared,
	})
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("want ErrCWDTaken, got %v", err)
	}
	if len(fg.deleteBranchCalls) != 0 {
		t.Errorf("--cwd adopts a pre-existing tree, so rollback must delete no branch: %+v", fg.deleteBranchCalls)
	}
}

// TestAddRollbackDeletesTheBranchItCreated pins #437: when Add cut a worktree
// and created relevo/<name>, a later refusal rolls both back, so a retry does
// not fail with "branch already exists".
func TestAddRollbackDeletesTheBranchItCreated(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)

	if err := rt.Store.Save(store.Binding{
		Name: "incumbent", CWD: rt.Store.WorktreePath("frontend"), Round: 1, State: store.StateActive,
		Builder: store.Endpoint{Mode: store.ModeHeadless},
	}); err != nil {
		t.Fatalf("seed incumbent: %v", err)
	}

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		Repo: addRepo(t),
	})
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("want ErrCWDTaken, got %v", err)
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Errorf("removeWorktreeCalls = %+v, want 1", fg.removeWorktreeCalls)
	}
	if len(fg.deleteBranchCalls) != 1 || fg.deleteBranchCalls[0].Branch != "relevo/frontend" {
		t.Errorf("deleteBranchCalls = %+v, want one with Branch %q", fg.deleteBranchCalls, "relevo/frontend")
	}
}

func TestAddHeadlessCutsTheWorktreeAndSpawnsNothing(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: repo, Headless: true,
	})
	if err != nil {
		t.Fatalf("Add --headless: %v", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Fatalf("add starts no process, got %d", got)
	}
	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("the worktree is still cut: addWorktreeCalls = %+v", fg.addWorktreeCalls)
	}
	ep := got.Binding.Builder
	if !ep.Headless() || ep.AgentName != "frontend-builder" || ep.Kind != "agy" {
		t.Errorf("builder = %+v, want headless frontend-builder/agy", ep)
	}
	if ep.PaneID != "" || ep.PID != 0 || ep.LogPath != "" {
		t.Errorf("spec §3.1 invariants broken: %+v", ep)
	}
	if got.Binding.BuilderCandidate != testAgyRef || got.Binding.Round != 1 {
		t.Errorf("candidate/round = %q/%d", got.Binding.BuilderCandidate, got.Binding.Round)
	}
}

// TestAddBranchLocalChecksOutWithoutCutting pins the local half of
// `add --branch`: a branch that already exists locally is checked out into
// relevo's own worktree, with no worktree cut and no branch created.
func TestAddBranchLocalChecksOutWithoutCutting(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		branchExists: true,
		refSHA:       map[string]string{"refs/heads/feature/api-auth": "tip123"},
	}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "api-auth", Branch: "feature/api-auth", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: repo,
	})
	if err != nil {
		t.Fatalf("Add --branch: %v", err)
	}

	wantWT := rt.Store.WorktreePath("api-auth")
	want := []checkoutWorktreeCall{{Dir: repo, Path: wantWT, Branch: "feature/api-auth"}}
	if !reflect.DeepEqual(fg.checkoutWorktreeCalls, want) {
		t.Errorf("checkoutWorktreeCalls = %+v, want %+v", fg.checkoutWorktreeCalls, want)
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("--branch must not cut a new worktree: %+v", fg.addWorktreeCalls)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Errorf("--branch must not create a branch: %+v", fg.createBranchCalls)
	}
	if len(fg.createTrackingBranchCalls) != 0 {
		t.Errorf("a local branch needs no tracking branch: %+v", fg.createTrackingBranchCalls)
	}
	if got.Binding.Branch != "feature/api-auth" {
		t.Errorf("Branch = %q, want feature/api-auth", got.Binding.Branch)
	}
	if got.Base != "tip123" || got.Binding.Base != "tip123" {
		t.Errorf("Base = %q (binding %q), want the branch tip tip123", got.Base, got.Binding.Base)
	}
	if !got.Binding.ExistingBranch {
		t.Error("ExistingBranch must record that relevo did not create the branch")
	}
	if got.Worktree != wantWT || got.Binding.CWD != wantWT {
		t.Errorf("worktree = %q, cwd = %q, want both %q", got.Worktree, got.Binding.CWD, wantWT)
	}
}

// TestAddBranchOriginOnlyTracksFirst pins the origin half: when only
// origin/<branch> exists, relevo first makes a local tracking branch.
func TestAddBranchOriginOnlyTracksFirst(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		branchExists: false,
		refSHA: map[string]string{
			"refs/remotes/origin/feature/x": "o1",
			"refs/heads/feature/x":          "o1",
		},
	}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "x", Branch: "feature/x", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: repo,
	})
	if err != nil {
		t.Fatalf("Add --branch: %v", err)
	}

	want := []createTrackingBranchCall{{Dir: repo, Branch: "feature/x", Upstream: "origin/feature/x"}}
	if !reflect.DeepEqual(fg.createTrackingBranchCalls, want) {
		t.Errorf("createTrackingBranchCalls = %+v, want %+v", fg.createTrackingBranchCalls, want)
	}
	if len(fg.checkoutWorktreeCalls) != 1 || fg.checkoutWorktreeCalls[0].Branch != "feature/x" {
		t.Errorf("checkoutWorktreeCalls = %+v, want one checkout of feature/x", fg.checkoutWorktreeCalls)
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("--branch must not cut a new worktree: %+v", fg.addWorktreeCalls)
	}
	if got.Base != "o1" {
		t.Errorf("Base = %q, want the branch tip o1", got.Base)
	}
	if !got.Binding.ExistingBranch {
		t.Error("ExistingBranch must be recorded")
	}
}

// TestAddBranchMissingRefuses pins that a branch on neither the local repo nor
// origin is a refusal before any git write.
func TestAddBranchMissingRefuses(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{branchExists: false, refSHA: map[string]string{}}
	rt := newTestRuntime(t, fg)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "x", Branch: "feature/x", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err == nil || !strings.Contains(err.Error(), "not found locally or on origin") {
		t.Fatalf("got %v, want a 'not found locally or on origin' refusal", err)
	}

	if len(fg.addWorktreeCalls)+len(fg.checkoutWorktreeCalls)+len(fg.createTrackingBranchCalls)+
		len(fg.createBranchCalls)+len(fg.deleteBranchCalls) != 0 {
		t.Errorf("a missing branch must reach no git write: add=%+v checkout=%+v tracking=%+v create=%+v delete=%+v",
			fg.addWorktreeCalls, fg.checkoutWorktreeCalls, fg.createTrackingBranchCalls,
			fg.createBranchCalls, fg.deleteBranchCalls)
	}
}

// TestAddBranchCheckedOutRefuses pins the refusal when the existing branch is
// checked out in another worktree, before any builder is resolved or started.
func TestAddBranchCheckedOutRefuses(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		branchExists:        true,
		refSHA:              map[string]string{"refs/heads/feature/x": "tip"},
		checkoutWorktreeErr: git.ErrBranchCheckedOut,
	}
	rt := newTestRuntime(t, fg)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "x", Branch: "feature/x", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err == nil || !strings.Contains(err.Error(), "checked out in another worktree") {
		t.Fatalf("got %v, want a 'checked out in another worktree' refusal", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refused checkout must start no process, got %d", got)
	}
	if len(fg.deleteBranchCalls) != 0 {
		t.Errorf("a refused checkout must delete no branch: %+v", fg.deleteBranchCalls)
	}
}

// TestAddBranchDrivenByLiveBindingRefuses pins the guard: a branch a live
// binding already drives cannot be adopted, while a DONE binding does not block.
func TestAddBranchDrivenByLiveBindingRefuses(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		branchExists: true,
		refSHA:       map[string]string{"refs/heads/feature/x": "tip"},
	}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	if err := rt.Store.Save(store.Binding{
		Name: "incumbent", CWD: repo, Branch: "feature/x", Round: 3, State: store.StateActive,
	}); err != nil {
		t.Fatalf("seed incumbent: %v", err)
	}

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "other", Branch: "feature/x", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: repo,
	})
	if err == nil || !strings.Contains(err.Error(), "incumbent") {
		t.Fatalf("a branch driven by a live binding must refuse, got %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("the guard must run before any checkout: %+v", fg.checkoutWorktreeCalls)
	}

	// The same branch on a DONE binding does not block.
	if err := rt.Store.Save(store.Binding{
		Name: "incumbent", CWD: repo, Branch: "feature/x", Round: 3, State: store.StateDone,
	}); err != nil {
		t.Fatalf("mark incumbent done: %v", err)
	}
	got, err := Add(context.Background(), rt, AddOptions{
		Name: "other", Branch: "feature/x", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: repo,
	})
	if err != nil {
		t.Fatalf("a DONE binding must not block: %v", err)
	}
	if got.Binding.Branch != "feature/x" {
		t.Errorf("Branch = %q, want feature/x", got.Binding.Branch)
	}
}

// TestAddBranchWithCwdRefused pins that Add itself refuses the flag pair, not
// only the CLI.
func TestAddBranchWithCwdRefused(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "x", Branch: "feature/x", CWD: addRepo(t), Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("got %v, want an 'exclusive' refusal", err)
	}
	if len(fg.addWorktreeCalls)+len(fg.checkoutWorktreeCalls)+len(fg.createTrackingBranchCalls) != 0 {
		t.Errorf("the flag refusal must reach no git write")
	}
}

// TestDefaultBindingName pins the derivation store.ValidName accepts, and that
// an underivable branch returns ValidName's own error.
func TestDefaultBindingName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		branch  string
		want    string
		wantErr bool
	}{
		{"feature/api-auth", "api-auth", false},
		{"v2", "v2", false},
		{"Fix/Login_Form", "login_form", false},
		{"//", "", true},
	}
	for _, c := range cases {
		got, err := DefaultBindingName(c.branch)
		if c.wantErr {
			if err == nil {
				t.Errorf("DefaultBindingName(%q) = %q, want an error", c.branch, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("DefaultBindingName(%q): %v", c.branch, err)
			continue
		}
		if got != c.want {
			t.Errorf("DefaultBindingName(%q) = %q, want %q", c.branch, got, c.want)
		}
		if err := store.ValidName(got); err != nil {
			t.Errorf("store.ValidName(%q) = %v, want nil", got, err)
		}
	}
}

// TestUnbindExistingBranchNeverDeletes pins the invariant README states: relevo
// deletes a branch in zero places, so neither unbind nor done+gc may remove an
// ExistingBranch binding's adopted branch.
func TestUnbindExistingBranchNeverDeletes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	setup := func(t *testing.T, fg *fakeGit) (Runtime, string) {
		t.Helper()
		rt := newTestRuntime(t, fg)
		repo := addRepo(t)
		if _, err := Add(ctx, rt, AddOptions{
			Name: "api-auth", Branch: "feature/api-auth", Candidate: testAgyRef,
			MasterMindID: testMasterMindName, Repo: repo,
		}); err != nil {
			t.Fatalf("Add --branch: %v", err)
		}
		// The recorded worktree must exist for teardown to reach the removal
		// path; a missing directory reads as "gone" and removes nothing.
		if err := os.MkdirAll(rt.Store.WorktreePath("api-auth"), 0o755); err != nil {
			t.Fatal(err)
		}
		return rt, repo
	}

	t.Run("unbind removes the worktree and never the branch", func(t *testing.T) {
		fg := &fakeGit{
			branchExists: true,
			refSHA:       map[string]string{"refs/heads/feature/api-auth": "tip123"},
		}
		rt, _ := setup(t, fg)

		if _, err := Unbind(ctx, rt, "api-auth", false); err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if len(fg.removeWorktreeCalls) != 1 {
			t.Errorf("removeWorktreeCalls = %+v, want 1", fg.removeWorktreeCalls)
		}
		if len(fg.deleteBranchCalls) != 0 {
			t.Errorf("unbind deleted the adopted branch: %+v", fg.deleteBranchCalls)
		}
	})

	t.Run("done then gc never deletes the branch", func(t *testing.T) {
		fg := &fakeGit{
			branchExists: true,
			refSHA:       map[string]string{"refs/heads/feature/api-auth": "tip123"},
		}
		rt, _ := setup(t, fg)

		if _, err := Done(ctx, rt, "api-auth"); err != nil {
			t.Fatalf("Done: %v", err)
		}
		if _, err := GC(ctx, rt, GCOptions{AllMasterMinds: true}); err != nil {
			t.Fatalf("GC: %v", err)
		}
		if len(fg.deleteBranchCalls) != 0 {
			t.Errorf("done+gc deleted the adopted branch: %+v", fg.deleteBranchCalls)
		}
	})
}

// TestAddRefusesUnsupportedTierBeforeWorktree pins the early tier refusal
// (#303 §1): Add renders the candidate's headless launch through
// resolveBuilder's headlessLaunch before the round can start, so a harness
// that cannot honour the tier is refused and the worktree Add just cut is
// rolled back -- no binding, no kept tree.
func TestAddRefusesUnsupportedTierBeforeWorktree(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, Repo: repo, Tier: "read",
	})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want harness.ErrTierUnsupported", err)
	}
	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("AddWorktree calls = %d, want 1 (cut before the launch check)", len(fg.addWorktreeCalls))
	}
	if len(fg.removeWorktreeCalls) != 1 || !fg.removeWorktreeCalls[0].Force {
		t.Errorf("rollback = %+v, want one forced RemoveWorktree", fg.removeWorktreeCalls)
	}
	if _, err := rt.Store.Load("frontend"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding must not exist, got %v", err)
	}
}

// TestAddStoresTicketWithRepoHint pins #637's local add half: Add parses
// --ticket once against opts.Repo, so the binding carries the canonical form.
func TestAddStoresTicketWithRepoHint(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123", repoFactsOrigin: "git@github.com:o/r.git"}
	rt := newTestRuntime(t, fg)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		Repo: addRepo(t), Feature: "auth", Ticket: "607",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.Binding.Ticket != "o/r#607" {
		t.Errorf("Ticket = %q, want o/r#607", got.Binding.Ticket)
	}
	if got.Binding.Feature != "auth" {
		t.Errorf("Feature = %q, want auth", got.Binding.Feature)
	}
	stored, err := rt.Store.Load("frontend")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Ticket != "o/r#607" {
		t.Errorf("stored Ticket = %q, want o/r#607", stored.Ticket)
	}
}

// TestAddUsesBaseWhenGiven pins the local cut's --base: when --base names a
// ref, the worktree is cut from the commit that ref resolves to, and HEAD is
// left out of it entirely; with no --base, HEAD stays the default.
func TestAddUsesBaseWhenGiven(t *testing.T) {
	t.Parallel()

	t.Run("a named base", func(t *testing.T) {
		t.Parallel()

		fg := &fakeGit{
			headCommitID: "commit-head-123",
			refSHA:       map[string]string{"release/v1": "commit-base-456"},
		}
		rt := newTestRuntime(t, fg)
		repo := addRepo(t)

		got, err := Add(context.Background(), rt, AddOptions{
			Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName,
			Repo: repo, Base: "release/v1",
		})
		if err != nil {
			t.Fatalf("Add --base: %v", err)
		}
		if len(fg.refSHACalls) != 1 || fg.refSHACalls[0].Ref != "release/v1" || fg.refSHACalls[0].Dir != repo {
			t.Fatalf("RefSHA calls = %+v, want one for release/v1 in %s", fg.refSHACalls, repo)
		}
		if len(fg.addWorktreeCalls) != 1 || fg.addWorktreeCalls[0].Commit != "commit-base-456" {
			t.Fatalf("AddWorktree calls = %+v, want the resolved base commit", fg.addWorktreeCalls)
		}
		if got.Base != "commit-base-456" {
			t.Errorf("Base = %q, want the resolved base commit", got.Base)
		}
		if fg.headCalls != 0 {
			t.Errorf("HeadCommit calls = %d, want 0 when --base is given", fg.headCalls)
		}
	})

	t.Run("no base keeps HEAD", func(t *testing.T) {
		t.Parallel()

		fg := &fakeGit{headCommitID: "commit-head-123"}
		rt := newTestRuntime(t, fg)
		repo := addRepo(t)

		got, err := Add(context.Background(), rt, AddOptions{
			Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: repo,
		})
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if len(fg.addWorktreeCalls) != 1 || fg.addWorktreeCalls[0].Commit != "commit-head-123" {
			t.Fatalf("AddWorktree calls = %+v, want HEAD", fg.addWorktreeCalls)
		}
		if got.Base != "commit-head-123" {
			t.Errorf("Base = %q, want HEAD", got.Base)
		}
		if len(fg.refSHACalls) != 0 {
			t.Errorf("RefSHA calls = %+v, want none when --base is omitted", fg.refSHACalls)
		}
	})

	t.Run("an unresolvable base is refused before the cut", func(t *testing.T) {
		t.Parallel()

		fg := &fakeGit{headCommitID: "commit-head-123", refSHA: map[string]string{}}
		rt := newTestRuntime(t, fg)

		_, err := Add(context.Background(), rt, AddOptions{
			Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName,
			Repo: addRepo(t), Base: "nope",
		})
		if err == nil || !strings.Contains(err.Error(), `base "nope" not found`) {
			t.Fatalf("err = %v, want base \"nope\" not found", err)
		}
		if len(fg.addWorktreeCalls) != 0 {
			t.Errorf("a refused base must cut no worktree: %+v", fg.addWorktreeCalls)
		}
	})
}
