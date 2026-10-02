package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMergeKeepLeavesAConflictInTheTree pins the difference from Merge: the
// conflict survives in the tree -- MERGE_HEAD present, the markers written --
// and comes back as the unmerged paths.
func TestMergeKeepLeavesAConflictInTheTree(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir, c1 := initRepoWithCommit(t, "f1.txt", "1\n")
	wtDir := filepath.Join(t.TempDir(), "wt")
	if err := client.AddWorktree(ctx, repoDir, wtDir, "parent", c1); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// The parent moves f1.txt on its own branch; the child branch cuts from c1
	// and moves it the other way, so the merge conflicts on that one file.
	writeGitFile(t, wtDir, "f1.txt", "parent side\n")
	runGit(t, wtDir, "add", "f1.txt")
	runGit(t, wtDir, "commit", "-m", "parent move")

	runGit(t, repoDir, "checkout", "-b", "child", c1)
	writeGitFile(t, repoDir, "f1.txt", "child side\n")
	runGit(t, repoDir, "add", "f1.txt")
	runGit(t, repoDir, "commit", "-m", "child move")
	childRef := "refs/heads/child"

	paths, err := client.MergeKeep(ctx, wtDir, childRef)
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("MergeKeep = %v, want ErrMergeConflict", err)
	}
	if len(paths) != 1 || paths[0] != "f1.txt" {
		t.Errorf("unmerged paths = %v, want [f1.txt]", paths)
	}

	// The merge is still in progress, and the tree shows the markers.
	gitDir, gerr := client.run(ctx, wtDir, nil, "rev-parse", "--git-dir")
	if gerr != nil {
		t.Fatalf("rev-parse --git-dir: %v", gerr)
	}
	mergeHead := filepath.Join(strings.TrimSpace(string(gitDir)), "MERGE_HEAD")
	if _, err := os.Stat(mergeHead); err != nil {
		t.Fatalf("MERGE_HEAD absent after a kept conflict: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(wtDir, "f1.txt"))
	if err != nil {
		t.Fatalf("read f1.txt: %v", err)
	}
	if !strings.Contains(string(body), "<<<<<<<") {
		t.Errorf("f1.txt = %q, want conflict markers", body)
	}
}

// TestMergeKeepCleanJoins pins the clean pass: a non-conflicting merge returns
// nil and leaves no merge in progress.
func TestMergeKeepCleanJoins(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir, c1 := initRepoWithCommit(t, "f1.txt", "1\n")
	wtDir := filepath.Join(t.TempDir(), "wt")
	if err := client.AddWorktree(ctx, repoDir, wtDir, "parent", c1); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	runGit(t, repoDir, "checkout", "-b", "child", c1)
	writeGitFile(t, repoDir, "f2.txt", "child only\n")
	runGit(t, repoDir, "add", "f2.txt")
	runGit(t, repoDir, "commit", "-m", "child adds f2")

	paths, err := client.MergeKeep(ctx, wtDir, "refs/heads/child")
	if err != nil || paths != nil {
		t.Fatalf("MergeKeep = (%v, %v), want (nil, nil)", paths, err)
	}
	if _, err := os.Stat(filepath.Join(wtDir, "f2.txt")); err != nil {
		t.Errorf("f2.txt missing after a clean merge: %v", err)
	}
	gitDir, gerr := client.run(ctx, wtDir, nil, "rev-parse", "--git-dir")
	if gerr != nil {
		t.Fatalf("rev-parse --git-dir: %v", gerr)
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(gitDir)), "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Errorf("MERGE_HEAD present after a clean merge: %v", err)
	}
}

// TestMergeKeepMissingRefAborts pins the non-conflict failure: an unresolvable
// ref is not a conflict, so the original error comes back and no merge is left.
func TestMergeKeepMissingRefAborts(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	dir, _ := initRepoWithCommit(t, "f1.txt", "1\n")
	paths, err := client.MergeKeep(ctx, dir, "refs/heads/does-not-exist")
	if err == nil || errors.Is(err, ErrMergeConflict) {
		t.Fatalf("MergeKeep = (%v, %v), want the original error", paths, err)
	}
	if paths != nil {
		t.Errorf("unmerged paths = %v, want none", paths)
	}
}
