package relevo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// forkThreeWorkflow forks into three fixed children.
const forkThreeWorkflow = `name: parent
inputs: { plans: optional }
start: split
steps:
  split:
    fork:
      children:
        - { workflow: CHILD, task: "first" }
        - { workflow: CHILD, task: "second" }
        - { workflow: CHILD, task: "third" }
    on: { joined: merge, conflict: done }
  merge: { run: builder, seed: "merge", on: { done: done } }
`

// forkTwoChildJoinWorkflow is the two-child fork that joins straight into a
// check. It is the same shape as forkFixedWorkflow, kept separate so the join
// test's expected routing is stated where it is asserted.

// forkConflictWorkflow routes a conflicting merge to a builder round that is
// handed the conflict file, so the seed that names it can be read.
const forkConflictWorkflow = `name: parent
inputs: { plans: optional }
start: split
steps:
  split:
    fork:
      children:
        - { workflow: CHILD, task: "first" }
        - { workflow: CHILD, task: "second" }
    on: { joined: merge, conflict: resolve }
  resolve: { run: builder, seed: "The merge conflicted. Resolve it:\n{{split.conflict}}", on: { done: done } }
  merge: { run: builder, seed: "merge", on: { done: done } }
`

// forkMergeRepo builds a real repository for a fork's merge: the parent writer's
// own worktree on relevo/<parent>, and one branch per child key cut from the
// base commit. It returns the parent tree's directory and the repository.
func forkMergeRepo(t *testing.T, keys ...string) (parentWt, repo string) {
	t.Helper()
	repo = t.TempDir()
	runGit(t, repo, "-c", "commit.gpgsign=false", "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "commit", "--allow-empty", "-m", "base")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	parentWt = filepath.Join(t.TempDir(), "shop")
	runGit(t, repo, "worktree", "add", "-b", "relevo/shop", parentWt, base)
	for _, key := range keys {
		runGit(t, repo, "branch", "relevo/shop."+key, base)
	}
	return parentWt, repo
}

// forkCommitOn writes body into name on branch and commits it, the work a
// finished child leaves on its own branch.
func forkCommitOn(t *testing.T, repo, branch, name, body string) {
	t.Helper()
	runGit(t, repo, "checkout", branch)
	if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-m", "work on "+branch)
}

// forkCommitIn writes body into name in the tree dir and commits it, the work
// the parent writer did before the fork's merge.
func forkCommitIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", "parent "+name)
}

// forkMergeRuntime starts a fork parent on the given workflow, then swaps the
// fake git for a real client over a real repository: the merge a test watches is
// git's own, in real worktrees.
func forkMergeRuntime(t *testing.T, parentWorkflow string, keys ...string) (Runtime, string, string) {
	t.Helper()
	rt, _ := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	startedChain(t, rt, ChainOptions{Name: "shop", Workflow: writeWorkflowFile(t, strings.ReplaceAll(parentWorkflow, "CHILD", childFile))})

	parentWt, repo := forkMergeRepo(t, keys...)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("shop")
		if err != nil {
			return err
		}
		b.CWD = parentWt
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("point the parent writer at the real tree: %v", err)
	}
	rt.Git = git.NewClient("git", 0, 0)
	return rt, parentWt, repo
}

// forkMergeTrace is the reason the merge's own trace row carries.
func forkMergeTrace(t *testing.T, rt Runtime) string {
	t.Helper()
	rows, err := rt.Store.ChainEvents("shop")
	if err != nil {
		t.Fatalf("ChainEvents: %v", err)
	}
	for _, row := range rows {
		act, derr := workflow.DecodeAction(row.Action)
		if derr == nil && act.Kind == workflow.ActionMerge {
			return row.Reason
		}
	}
	t.Fatalf("no merge action in the trace: %+v", rows)
	return ""
}

// mergeInProgress reports whether dir's tree still holds an unfinished merge.
func mergeInProgress(t *testing.T, dir string) bool {
	t.Helper()
	gitDir := strings.TrimSpace(runGit(t, dir, "rev-parse", "--absolute-git-dir"))
	_, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD"))
	return err == nil
}

// TestForkMergeJoinsEveryChildInKeyOrder pins the clean join: both children are
// merged into the parent writer's tree, in key order, and the fork routes
// joined. Nothing runs after it but the step the author wired.
func TestForkMergeJoinsEveryChildInKeyOrder(t *testing.T) {
	t.Parallel()

	rt, parentWt, repo := forkMergeRuntime(t, forkTwoChildJoinWorkflow, "1", "2")
	forkCommitOn(t, repo, "relevo/shop.1", "child1.txt", "one\n")
	forkCommitOn(t, repo, "relevo/shop.2", "child2.txt", "two\n")

	forkEndChild(t, rt, "shop.1", "done", "")
	forkEndChild(t, rt, "shop.2", "done", "")

	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	got := st.Results["split"]
	if got.Status != chainMergeJoined {
		t.Fatalf("fork result = %+v, want joined", got)
	}
	for _, name := range []string{"child1.txt", "child2.txt"} {
		if _, err := os.Stat(filepath.Join(parentWt, name)); err != nil {
			t.Errorf("%s missing from the parent tree after the join: %v", name, err)
		}
	}
	if mergeInProgress(t, parentWt) {
		t.Error("a joined merge left an unfinished merge in the tree")
	}
	if trace := forkMergeTrace(t, rt); trace != "merged 1, 2 → joined" {
		t.Errorf("merge trace = %q, want %q", trace, "merged 1, 2 → joined")
	}

	// The join runs only what the author wired: a check.
	row, cerr := rt.Store.ChainCheck("shop", 1)
	if cerr != nil {
		t.Fatalf("ChainCheck: %v", cerr)
	}
	if row.Step != "verify" {
		t.Errorf("check row step = %q, want verify", row.Step)
	}
}

// TestForkMergeKeepsTheConflictAndNamesTheUnmergedBranches pins the second child
// of three conflicting: the merge stays in the tree, the third child is listed
// as not yet merged, and the conflict report names the paths.
func TestForkMergeKeepsTheConflictAndNamesTheUnmergedBranches(t *testing.T) {
	t.Parallel()

	rt, parentWt, repo := forkMergeRuntime(t, forkThreeWorkflow, "1", "2", "3")
	// The parent and the second child move base.txt different ways; the first
	// child adds its own file and merges clean.
	forkCommitIn(t, parentWt, "base.txt", "parent side\n")
	forkCommitOn(t, repo, "relevo/shop.1", "child1.txt", "one\n")
	forkCommitOn(t, repo, "relevo/shop.2", "base.txt", "child side\n")

	for _, key := range []string{"1", "2", "3"} {
		forkEndChild(t, rt, "shop."+key, "done", "")
	}

	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	got := st.Results["split"]
	if got.Status != chainMergeConflict {
		t.Fatalf("fork result = %+v, want conflict", got)
	}
	if !mergeInProgress(t, parentWt) {
		t.Error("the conflicting merge was aborted: the builder must be handed the tree holding it")
	}

	conflicts := got.Artifacts["conflict"]
	if len(conflicts) != 1 || conflicts[0] == "" {
		t.Fatalf("conflict artifact = %v, want the one report file", conflicts)
	}
	body, rerr := rt.Store.ReadFile(conflicts[0])
	if rerr != nil {
		t.Fatalf("read the conflict report: %v", rerr)
	}
	report := string(body)
	if !strings.Contains(report, "base.txt") {
		t.Errorf("conflict report = %q, want the conflicted path", report)
	}
	if !strings.Contains(report, "not yet merged:\nrelevo/shop.3\n") {
		t.Errorf("conflict report = %q, want the third child under not yet merged", report)
	}
	if trace := forkMergeTrace(t, rt); trace != "merged 1 → conflict" {
		t.Errorf("merge trace = %q, want %q", trace, "merged 1 → conflict")
	}
}

// TestForkConflictEdgeSendsTheReportToTheBuilder pins the wiring: the parent's
// conflict edge sends its builder a seed that names the conflict report, and the
// tree the builder is handed still holds the unfinished merge.
func TestForkConflictEdgeSendsTheReportToTheBuilder(t *testing.T) {
	t.Parallel()

	rt, parentWt, repo := forkMergeRuntime(t, forkConflictWorkflow, "1", "2")
	forkCommitIn(t, parentWt, "base.txt", "parent side\n")
	forkCommitOn(t, repo, "relevo/shop.1", "child1.txt", "one\n")
	forkCommitOn(t, repo, "relevo/shop.2", "base.txt", "child side\n")

	forkEndChild(t, rt, "shop.1", "done", "")
	forkEndChild(t, rt, "shop.2", "done", "")

	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "resolve" || st.Awaiting.Member != "builder" {
		t.Fatalf("awaiting = %+v, want the builder on the conflict edge", st.Awaiting)
	}
	if !mergeInProgress(t, parentWt) {
		t.Error("the builder was sent without the merge in its tree")
	}

	staged, serr := rt.Store.ReadFile(rt.Store.PromptPath("shop", 1))
	if serr != nil {
		t.Fatalf("read the staged prompt: %v", serr)
	}
	prompt := string(staged)
	if !strings.Contains(prompt, "Resolve it:") {
		t.Fatalf("prompt = %q, want the conflict seed", prompt)
	}
	if !strings.Contains(prompt, "fork-conflict.txt") {
		t.Errorf("prompt = %q, want it to name the conflict report copy", prompt)
	}
	// The seed names a copy under the chain's input directory, so the path it
	// carries is a real file the builder can open.
	for _, line := range strings.Split(prompt, "\n") {
		if !strings.Contains(line, "fork-conflict.txt") {
			continue
		}
		body, rerr := os.ReadFile(strings.TrimSpace(line))
		if rerr != nil {
			t.Fatalf("open the report the prompt names (%q): %v", line, rerr)
		}
		if !strings.Contains(string(body), "base.txt") {
			t.Errorf("the report copy = %q, want the conflicted path", body)
		}
		return
	}
}

// TestForkMergeGitFailureHaltsTheParent pins a failure that is not a conflict:
// there is nothing in a tree the author can resolve, so the parent halts naming
// the child and git's message.
func TestForkMergeGitFailureHaltsTheParent(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	startedChain(t, rt, ChainOptions{Name: "shop", Workflow: writeWorkflowFile(t, forkFixedWorkflow(childFile))})

	// No repository at all, so every merge fails for a reason of its own.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("shop")
		if err != nil {
			return err
		}
		b.CWD = t.TempDir()
		return tx.Save(b)
	}); err != nil {
		t.Fatalf("point the parent writer at a plain directory: %v", err)
	}
	rt.Git = git.NewClient("git", 0, 0)

	forkEndChild(t, rt, "shop.1", "done", "")
	forkEndChild(t, rt, "shop.2", "done", "")

	parent := flowChainRow(t, rt)
	if parent.Status != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q, want halted", parent.Status)
	}
	if !strings.Contains(parent.Reason, "child shop.1 could not merge into the parent") {
		t.Errorf("parent reason = %q, want it to name the child", parent.Reason)
	}
}

// forkTwoChildJoinWorkflow is the two-child fork that joins straight into a
// check: nothing runs after the merge but the step the author wired. The
// rebase step exists only so the workflow declares the writer run step rule 7
// asks for -- the join never takes that edge.
const forkTwoChildJoinWorkflow = `name: parent
inputs: { plans: optional }
start: split
steps:
  split:
    fork:
      children:
        - { workflow: CHILD, task: "first" }
        - { workflow: CHILD, task: "second" }
    on: { joined: verify, conflict: rebase }
  rebase: { run: builder, seed: "rebase the fork", on: { done: verify } }
  verify: { check: "true", on: { green: done, red: { halt: "red" } } }
`

// TestForkConflictReportIsAKeyedRowFile pins the conflict report's own shape: a
// row-only round-file key under the parent chain, resolvable back to the member
// and round it was keyed with.
func TestForkConflictReportIsAKeyedRowFile(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	key := st.ForkConflictPath("shop", 4)
	if got, want := filepath.Base(key), "004-fork-conflict.txt"; got != want {
		t.Errorf("ForkConflictPath base = %q, want %q", got, want)
	}
	member, round, ok := st.RoundFileTarget(key)
	if !ok || member != "shop" || round != 4 {
		t.Errorf("RoundFileTarget(%q) = (%q, %d, %v), want (shop, 4, true)", key, member, round, ok)
	}
	if st.DiskRegularFile(key) {
		t.Error("a conflict report key is row-only, so a file at that name is a plant")
	}
}
