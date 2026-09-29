package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAddWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir, first, second := repoWithTwoCommits(t)
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	statusBefore := runGit(t, repoDir, "status", "--porcelain")

	wtDir := filepath.Join(t.TempDir(), "wt1")
	branch := "relevo/test-wt"
	if err := client.AddWorktree(ctx, repoDir, wtDir, branch, first); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if fi, err := os.Stat(wtDir); err != nil || !fi.IsDir() {
		t.Fatalf("worktree directory %s does not exist or is not a directory", wtDir)
	}
	wtHead, err := client.HeadCommit(ctx, wtDir)
	if err != nil {
		t.Fatalf("HeadCommit on worktree: %v", err)
	}
	if wtHead != first {
		t.Fatalf("worktree HEAD is %s, want %s", wtHead, first)
	}
	if got := runGit(t, repoDir, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("source tree status changed: got %q, want %q", got, statusBefore)
	}
	srcHead, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit on repo: %v", err)
	}
	if srcHead != second {
		t.Fatalf("repo HEAD is %s, want %s", srcHead, second)
	}

	// A second worktree on the same branch is refused and leaves nothing behind.
	wtDir2 := filepath.Join(t.TempDir(), "wt2")
	if err := client.AddWorktree(ctx, repoDir, wtDir2, branch, second); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("second AddWorktree got %v, want ErrBranchExists", err)
	}
	if _, err := os.Stat(wtDir2); !os.IsNotExist(err) {
		t.Fatalf("wtDir2 was not cleaned up after error: %v", err)
	}
}

func TestRemoveWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir, first, _ := repoWithTwoCommits(t)
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	wtDir := filepath.Join(t.TempDir(), "wt")
	branch := "relevo/test-wt"
	if err := client.AddWorktree(ctx, repoDir, wtDir, branch, first); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// Without force a dirty tree is refused, and the directory stays.
	dirtyFile := filepath.Join(wtDir, "uncommitted.txt")
	writeGitFile(t, wtDir, "uncommitted.txt", "dirty\n")
	if err := client.RemoveWorktree(ctx, repoDir, wtDir, false); !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("RemoveWorktree on dirty tree got %v, want ErrWorktreeDirty", err)
	}
	if _, err := os.Stat(wtDir); os.IsNotExist(err) {
		t.Fatal("wtDir was removed despite ErrWorktreeDirty")
	}

	if err := os.Remove(dirtyFile); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveWorktree(ctx, repoDir, wtDir, false); err != nil {
		t.Fatalf("RemoveWorktree on clean tree failed: %v", err)
	}
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatal("wtDir still exists after successful RemoveWorktree")
	}

	// The branch holds commits and must survive the worktree removal.
	exists, err := client.BranchExists(ctx, repoDir, branch)
	if err != nil {
		t.Fatalf("BranchExists after removal: %v", err)
	}
	if !exists {
		t.Fatal("branch was removed after RemoveWorktree; branch must survive")
	}
	if got := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "refs/heads/"+branch)); got != first {
		t.Fatalf("branch resolves to %s, want %s", got, first)
	}
}

func TestRemoveWorktreeForce(t *testing.T) {
	ctx := context.Background()
	repoDir, first, _ := repoWithTwoCommits(t)
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	wtDir := filepath.Join(t.TempDir(), "wt3")
	if err := client.AddWorktree(ctx, repoDir, wtDir, "relevo/test-wt3", first); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	writeGitFile(t, wtDir, "dirty.txt", "dirty")

	if err := client.RemoveWorktree(ctx, repoDir, wtDir, true); err != nil {
		t.Fatalf("RemoveWorktree with force: true failed: %v", err)
	}
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatal("wtDir3 still exists after forced RemoveWorktree")
	}
}

func TestAddWorktreeCleansUpOnError(t *testing.T) {
	ctx := context.Background()
	repoDir, _, _ := repoWithTwoCommits(t)
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	wtDir := filepath.Join(t.TempDir(), "wt-bad")
	err := client.AddWorktree(ctx, repoDir, wtDir, "relevo/bad", "0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("AddWorktree with invalid commit expected error, got nil")
	}
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatal("wtDir was not cleaned up on error")
	}
}

func TestCheckoutWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initRepoWithCommit(t, "file.txt", "v1\n")
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	wt1 := filepath.Join(t.TempDir(), "wt1")
	if err := client.AddWorktree(ctx, repoDir, wt1, "feature", "HEAD"); err != nil {
		t.Fatalf("AddWorktree failed: %v", err)
	}
	if err := client.RemoveWorktree(ctx, repoDir, wt1, false); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}

	// An existing branch checks out into a new worktree.
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if err := client.CheckoutWorktree(ctx, repoDir, wt2, "feature"); err != nil {
		t.Fatalf("CheckoutWorktree wt2 failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt2, ".git")); err != nil {
		t.Fatalf("wt2/.git does not exist: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, wt2, "rev-parse", "--abbrev-ref", "HEAD")); got != "feature" {
		t.Fatalf("wt2 branch is %q, want %q", got, "feature")
	}

	// The branch is now checked out in wt2, so a second checkout is refused and
	// cleaned up.
	wt3 := filepath.Join(t.TempDir(), "wt3")
	err := client.CheckoutWorktree(ctx, repoDir, wt3, "feature")
	if !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("CheckoutWorktree wt3 got %v, want ErrBranchCheckedOut", err)
	}
	if _, err := os.Stat(wt3); !os.IsNotExist(err) {
		t.Fatalf("wt3 was not cleaned up after error: %v", err)
	}

	// An unknown branch is another error.
	wt4 := filepath.Join(t.TempDir(), "wt4")
	err = client.CheckoutWorktree(ctx, repoDir, wt4, "no-such-branch")
	if err == nil {
		t.Fatal("CheckoutWorktree wt4 expected error, got nil")
	}
	if errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("CheckoutWorktree wt4 got ErrBranchCheckedOut, want another error")
	}
}

func TestAddDetachedWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir, first, second := repoWithTwoCommits(t)
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	statusBefore := runGit(t, repoDir, "status", "--porcelain")
	branchesBefore := runGit(t, repoDir, "branch", "--format=%(refname)")

	wtDir := filepath.Join(t.TempDir(), "verify-001")
	if err := client.AddDetachedWorktree(ctx, repoDir, wtDir, first); err != nil {
		t.Fatalf("AddDetachedWorktree failed: %v", err)
	}
	if fi, err := os.Stat(wtDir); err != nil || !fi.IsDir() {
		t.Fatalf("worktree directory %s does not exist or is not a directory", wtDir)
	}

	wtHead, err := client.HeadCommit(ctx, wtDir)
	if err != nil {
		t.Fatalf("HeadCommit on worktree: %v", err)
	}
	if wtHead != first {
		t.Fatalf("worktree HEAD is %s, want %s", wtHead, first)
	}

	// Detached: symbolic-ref refuses to name a branch for it.
	if out, err := exec.Command("git", "-C", wtDir, "symbolic-ref", "-q", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("symbolic-ref succeeded on a detached worktree: %s", string(out))
	}

	if got := runGit(t, repoDir, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("source tree status changed: got %q, want %q", got, statusBefore)
	}
	srcHead, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit on repo: %v", err)
	}
	if srcHead != second {
		t.Fatalf("repo HEAD is %s, want %s", srcHead, second)
	}
	// No branch was created for the throwaway tree.
	if got := runGit(t, repoDir, "branch", "--format=%(refname)"); got != branchesBefore {
		t.Fatalf("branches changed: got %q, want %q", got, branchesBefore)
	}

	if err := client.RemoveWorktree(ctx, repoDir, wtDir, true); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after removal: %v", err)
	}
}

// TestRemoveWorktreeAlreadyGone is the contabo case: the directory is gone and
// git no longer lists it, so its stale registration must be pruned before the
// branch can be deleted.
func TestRemoveWorktreeAlreadyGone(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 10*time.Second, 0)
	repoDir, head := initRepoWithCommit(t, "file.txt", "v1\n")

	wtDir := filepath.Join(t.TempDir(), "gone")
	if err := client.AddWorktree(ctx, repoDir, wtDir, "relevo/gone", head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := os.RemoveAll(wtDir); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "worktree", "prune")

	if err := client.RemoveWorktree(ctx, repoDir, wtDir, true); err != nil {
		t.Fatalf("RemoveWorktree on a gone path = %v, want nil", err)
	}
	if err := client.DeleteBranch(ctx, repoDir, "relevo/gone"); err != nil {
		t.Fatalf("DeleteBranch after removing a gone worktree = %v, want nil (stale registration pruned)", err)
	}
}

func TestClientWorktreeRepair(t *testing.T) {
	ctx := context.Background()
	tmp := resolvedTempDir(t)

	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")

	writeGitFile(t, repo, "a.txt", "hello\n")
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "initial")

	if err := os.MkdirAll(filepath.Join(tmp, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	runGit(t, repo, "worktree", "add", filepath.Join(tmp, "a", "wt"))

	// A state-root move renames the directory out from under git.
	if err := os.Rename(filepath.Join(tmp, "a"), filepath.Join(tmp, "b")); err != nil {
		t.Fatalf("move a to b: %v", err)
	}
	moved := filepath.Join(tmp, "b", "wt")

	c := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)
	if err := c.WorktreeRepair(ctx, repo, moved); err != nil {
		t.Fatalf("WorktreeRepair: %v", err)
	}
	if list := runGit(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(list, moved) {
		t.Errorf("worktree list does not name %s:\n%s", moved, list)
	}
	runGit(t, moved, "status")
}

// TestWorktreeRepairAfterBareRepoAndWorktreeBothMove pins that a repair driven
// from the moved bare repo is enough when the bare repo and the worktree moved
// together.
func TestWorktreeRepairAfterBareRepoAndWorktreeBothMove(t *testing.T) {
	ctx := context.Background()
	tmp := resolvedTempDir(t)

	a := filepath.Join(tmp, "a")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}

	bare := filepath.Join(a, "r.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("mkdir r.git: %v", err)
	}
	runGit(t, bare, "init", "--bare")
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	seed := filepath.Join(tmp, "seed")
	runGit(t, tmp, "clone", bare, seed)
	runGit(t, seed, "config", "maintenance.auto", "false")
	runGit(t, seed, "config", "gc.auto", "0")
	writeGitFile(t, seed, "a.txt", "hello\n")
	runGit(t, seed, "add", "a.txt")
	runGit(t, seed, "commit", "-m", "initial")
	runGit(t, seed, "push", "origin", "main")

	runGit(t, bare, "worktree", "add", filepath.Join(a, "wt"), "main")

	if err := os.Rename(a, filepath.Join(tmp, "b")); err != nil {
		t.Fatalf("move a to b: %v", err)
	}
	movedRepo := filepath.Join(tmp, "b", "r.git")
	moved := filepath.Join(tmp, "b", "wt")

	c := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)
	if err := c.WorktreeRepair(ctx, movedRepo, moved); err != nil {
		t.Fatalf("WorktreeRepair: %v", err)
	}
	runGit(t, moved, "status")
}
