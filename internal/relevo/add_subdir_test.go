package relevo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// addSubdirRound runs the add, then one Send, and returns what the add stored
// and where the round's builder process started: roundTree only becomes a
// directory when a round launches, so the Send is what makes the rule visible.
func addSubdirRound(t *testing.T, fg *fakeGit, opts AddOptions) (Runtime, AddResult, string, error) {
	t.Helper()
	rt := newTestRuntime(t, fg)
	res, err := Add(context.Background(), rt, opts)
	if err != nil {
		return rt, res, "", err
	}
	if _, err := Send(context.Background(), rt, opts.Name, writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	specs := runnerOf(t, rt).specs
	if len(specs) != 1 {
		t.Fatalf("builder spawns = %d, want 1", len(specs))
	}
	return rt, res, specs[0].Dir, nil
}

// TestAddFromASubdirectoryRunsTheRoundThere pins the rule on the add route: a
// bind started below the root stores the tree root in CWD, the prefix in
// Subdir, and starts the round's builder in the joined directory. Dropping
// the join in roundTree, or storing the subdirectory inside CWD, both fail
// here.
func TestAddFromASubdirectoryRunsTheRoundThere(t *testing.T) {
	fg := &fakeGit{
		headCommitID:     "commit-head-123",
		showPrefixResult: "src/api/",
		dirAtResult:      true,
	}
	rt, res, roundDir, err := addSubdirRound(t, fg, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err != nil {
		t.Fatalf("Add/Send: %v", err)
	}
	if res.Binding.CWD != res.Binding.Worktree || res.Binding.CWD == "" {
		t.Errorf("CWD = %q, want the tree root %q", res.Binding.CWD, res.Binding.Worktree)
	}
	if res.Binding.Subdir != "src/api" {
		t.Errorf("Subdir = %q, want %q", res.Binding.Subdir, "src/api")
	}
	if want := filepath.Join(res.Binding.CWD, "src/api"); roundDir != want {
		t.Errorf("builder ran in %q, want the round's directory %q (binding %+v, runtime %+v)", roundDir, want, res.Binding, rt.Store)
	}
}

// TestAddRefusesASubdirectoryTheBaseCommitDoesNotHave pins the refusal: a
// base that has no directory at the prefix refuses with the subdirectory and
// the base named, the cut's worktree and branch are rolled back, and no
// binding is stored. Skipping the DirAt read would run the round in a path
// that does not exist.
func TestAddRefusesASubdirectoryTheBaseCommitDoesNotHave(t *testing.T) {
	fg := &fakeGit{
		headCommitID:     "commit-head-123",
		showPrefixResult: "ghosted/",
		dirAtResult:      false,
	}
	rt := newTestRuntime(t, fg)
	repo := addRepo(t)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: repo,
	})
	if err == nil {
		t.Fatal("Add succeeded, want the missing-subdirectory refusal")
	}
	if !strings.Contains(err.Error(), "ghosted") || !strings.Contains(err.Error(), "commit-head-123") {
		t.Fatalf("err = %v, want the subdirectory and the base named", err)
	}
	if _, lerr := rt.Store.Load("frontend"); lerr == nil {
		t.Error("a binding was stored after the refusal")
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Errorf("RemoveWorktree calls = %d, want the cut's tree rolled back", len(fg.removeWorktreeCalls))
	}
	if len(fg.deleteBranchCalls) != 1 {
		t.Errorf("DeleteBranch calls = %d, want the cut's branch rolled back", len(fg.deleteBranchCalls))
	}
}

// TestAddFromTheRepositoryRootKeepsTheRoundAtTheRoot pins the unchanged case:
// a bind started at the root stores no Subdir and the round runs in the
// worktree itself. Joining an empty prefix into "./", or storing a dot, would
// land here.
func TestAddFromTheRepositoryRootKeepsTheRoundAtTheRoot(t *testing.T) {
	fg := &fakeGit{headCommitID: "commit-head-123"}
	_, res, roundDir, err := addSubdirRound(t, fg, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err != nil {
		t.Fatalf("Add/Send: %v", err)
	}
	if res.Binding.Subdir != "" {
		t.Errorf("Subdir = %q, want none at the root", res.Binding.Subdir)
	}
	if roundDir != res.Binding.CWD || roundDir != res.Binding.Worktree {
		t.Errorf("round ran in %q, want the worktree itself %q", roundDir, res.Binding.Worktree)
	}
}

// TestAddWithCWDNeverConsultsThePrefix pins the --cwd escape: the prepared
// directory is the round's directory, so no ShowPrefix runs and no Subdir is
// stored. Resolving the prefix on this path would land the round in dir/dir.
func TestAddWithCWDNeverConsultsThePrefix(t *testing.T) {
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newTestRuntime(t, fg)

	dir := addRepo(t)
	res, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		Repo: dir, CWD: dir,
	})
	if err != nil {
		t.Fatalf("Add --cwd: %v", err)
	}
	if fg.showPrefixCalls != 0 {
		t.Errorf("ShowPrefix calls = %d, want none on --cwd", fg.showPrefixCalls)
	}
	if res.Binding.Subdir != "" {
		t.Errorf("Subdir = %q, want none on --cwd", res.Binding.Subdir)
	}
	if res.Binding.CWD != dir {
		t.Errorf("CWD = %q, want the prepared directory %q", res.Binding.CWD, dir)
	}
}

// TestAddBranchAdoptsTheSubdirectoryToo pins the --branch path: a bind that
// adopted an existing branch from below the root carries the same prefix. The
// --branch adopt falls back to creating a tracking branch from origin, which
// the fake answers.
func TestAddBranchAdoptsTheSubdirectoryToo(t *testing.T) {
	fg := &fakeGit{
		headCommitID:     "commit-head-123",
		showPrefixResult: "src/api/",
		dirAtResult:      true,
	}
	_, res, roundDir, err := addSubdirRound(t, fg, AddOptions{
		Name: "frontend", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		Repo: addRepo(t), Branch: "feature/api-auth",
	})
	if err != nil {
		t.Fatalf("Add/Send --branch: %v", err)
	}
	if res.Binding.Subdir != "src/api" {
		t.Errorf("Subdir = %q, want %q on --branch too", res.Binding.Subdir, "src/api")
	}
	if want := filepath.Join(res.Binding.Worktree, "src/api"); roundDir != want {
		t.Errorf("round ran in %q, want the round's directory %q", roundDir, want)
	}
}
