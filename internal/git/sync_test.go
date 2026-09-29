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

func TestMergeFF(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir, c1 := initRepoWithCommit(t, "f1.txt", "1\n")
	wtDir := filepath.Join(t.TempDir(), "wt")
	if err := client.AddWorktree(ctx, repoDir, wtDir, "feature", c1); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// A ref ahead of the worktree fast-forwards it.
	writeGitFile(t, repoDir, "f2.txt", "2\n")
	runGit(t, repoDir, "add", "f2.txt")
	runGit(t, repoDir, "commit", "-m", "c2")
	c2 := mustHead(t, repoDir)
	refOut := "refs/relevo/test/out"
	runGit(t, repoDir, "update-ref", refOut, c2)

	if err := client.MergeFF(ctx, wtDir, refOut); err != nil {
		t.Fatalf("MergeFF expected success, got %v", err)
	}
	if head, err := client.HeadCommit(ctx, wtDir); err != nil || head != c2 {
		t.Fatalf("wt HEAD = %q, want %s", head, c2)
	}
	if _, err := os.Stat(filepath.Join(wtDir, "f2.txt")); err != nil {
		t.Fatalf("f2.txt missing in worktree after ff: %v", err)
	}

	// A diverged ref is not a fast-forward.
	writeGitFile(t, wtDir, "wt_only.txt", "wt\n")
	runGit(t, wtDir, "add", "wt_only.txt")
	runGit(t, wtDir, "commit", "-m", "c3 in wt")
	writeGitFile(t, repoDir, "repo_only.txt", "repo\n")
	runGit(t, repoDir, "add", "repo_only.txt")
	runGit(t, repoDir, "commit", "-m", "c4 in repo")
	refDiverged := "refs/relevo/test/diverged"
	runGit(t, repoDir, "update-ref", refDiverged, mustHead(t, repoDir))

	if err := client.MergeFF(ctx, wtDir, refDiverged); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("MergeFF on diverged ref: got %v, want ErrNotFastForward", err)
	}
}

func TestMergeFFDirtyConflict(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir, _ := initRepoWithCommit(t, "f1.txt", "1\n")
	writeGitFile(t, repoDir, "f2.txt", "2\n")
	runGit(t, repoDir, "add", "f2.txt")
	runGit(t, repoDir, "commit", "-m", "c2")
	c2 := mustHead(t, repoDir)

	wtDir := filepath.Join(t.TempDir(), "wt")
	if err := client.AddWorktree(ctx, repoDir, wtDir, "feature", c2); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	runGit(t, repoDir, "checkout", "-b", "conflict-branch", c2)
	writeGitFile(t, repoDir, "f2.txt", "modified in conflict branch\n")
	runGit(t, repoDir, "add", "f2.txt")
	runGit(t, repoDir, "commit", "-m", "conflict commit")
	refConflict := "refs/relevo/test/conflict"
	runGit(t, repoDir, "update-ref", refConflict, mustHead(t, repoDir))

	// f2.txt is dirty in the worktree, so the fast-forward would overwrite it.
	writeGitFile(t, wtDir, "f2.txt", "dirty in wt\n")
	if err := client.MergeFF(ctx, wtDir, refConflict); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("MergeFF with dirty conflicting file: got %v, want ErrMergeConflict", err)
	}
}

// TestConflictAbortsAndListsPaths drives both integration paths against the same
// conflict: unmerged paths come back, ErrMergeConflict is returned, the
// integration is aborted, and HEAD is where it was.
func TestConflictAbortsAndListsPaths(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)

	tests := []struct {
		name      string
		integrate func(ctx context.Context, work string) ([]string, error)
	}{
		{"rebase", func(ctx context.Context, work string) ([]string, error) {
			return client.Rebase(ctx, work, "origin/main")
		}},
		{"merge", func(ctx context.Context, work string) ([]string, error) {
			return client.Merge(ctx, work, "origin/main")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			work, featHead := conflictingClone(t)
			if err := client.Fetch(ctx, work, "origin", "main"); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			paths, err := tc.integrate(ctx, work)
			if !errors.Is(err, ErrMergeConflict) {
				t.Fatalf("%s err = %v, want ErrMergeConflict", tc.name, err)
			}
			if len(paths) != 1 || paths[0] != "file.txt" {
				t.Errorf("conflict paths = %v, want [file.txt]", paths)
			}
			if out := strings.TrimSpace(runGit(t, work, "status", "--porcelain")); out != "" {
				t.Errorf("worktree is not clean after %s --abort: %q", tc.name, out)
			}
			if got := strings.TrimSpace(runGit(t, work, "rev-parse", "HEAD")); got != featHead {
				t.Errorf("HEAD = %s, want %s (the abort must leave it where it was)", got, featHead)
			}
		})
	}
}

// conflictingClone is a work clone on feat whose origin/main edited the same path
// feat did, so integrating the two conflicts.
func conflictingClone(t *testing.T) (work, featHead string) {
	t.Helper()
	seed := initRepo(t)
	runGit(t, seed, "checkout", "-b", "main")
	writeGitFile(t, seed, "file.txt", "base\n")
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "commit", "-m", "base")

	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(origin), "clone", "--bare", seed, origin)
	runGit(t, seed, "remote", "add", "origin", origin)

	work = filepath.Join(t.TempDir(), "work")
	runGit(t, filepath.Dir(work), "clone", origin, work)
	runGit(t, work, "checkout", "-b", "feat")
	// A clone carries no local identity, and the client runs git without the
	// runGit fixture env, so a replaying rebase needs one.
	runGit(t, work, "config", "user.name", "Test")
	runGit(t, work, "config", "user.email", "test@example.com")
	writeGitFile(t, work, "file.txt", "feat\n")
	runGit(t, work, "add", "file.txt")
	runGit(t, work, "commit", "-m", "feat edit")
	featHead = mustHead(t, work)

	writeGitFile(t, seed, "file.txt", "main\n")
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "commit", "-m", "main edit")
	runGit(t, seed, "push", "origin", "main")
	return work, featHead
}

// TestFetchRebasePush drives the git primitives the land flow uses against real
// repositories: fetch and rebase onto a moved origin/main, push, then rebase
// again and push with --force-with-lease, which a rewritten branch needs.
func TestFetchRebasePush(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 10*time.Second, DefaultMaxPatchBytes)

	seed := initRepo(t)
	runGit(t, seed, "checkout", "-b", "main")
	writeGitFile(t, seed, "base.txt", "v1\n")
	runGit(t, seed, "add", "base.txt")
	runGit(t, seed, "commit", "-m", "main 1")

	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(origin), "clone", "--bare", seed, origin)
	runGit(t, seed, "remote", "add", "origin", origin)

	work := filepath.Join(t.TempDir(), "work")
	runGit(t, filepath.Dir(work), "clone", origin, work)
	runGit(t, work, "checkout", "-b", "feat")
	runGit(t, work, "config", "user.name", "Test")
	runGit(t, work, "config", "user.email", "test@example.com")
	writeGitFile(t, work, "feat.txt", "feat\n")
	runGit(t, work, "add", "feat.txt")
	runGit(t, work, "commit", "-m", "feat 1")

	// origin/main gains a commit the work clone has not seen.
	writeGitFile(t, seed, "main2.txt", "v2\n")
	runGit(t, seed, "add", "main2.txt")
	runGit(t, seed, "commit", "-m", "main 2")
	runGit(t, seed, "push", "origin", "main")

	if err := client.Fetch(ctx, work, "origin", "main"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	paths, err := client.Rebase(ctx, work, "origin/main")
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("Rebase conflicts = %v, want none", paths)
	}
	// runGit fails the test when origin/main is not an ancestor of HEAD.
	runGit(t, work, "merge-base", "--is-ancestor", "origin/main", "HEAD")

	if err := client.Push(ctx, work, "origin", "feat", false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	exists, err := client.RemoteBranchExists(ctx, work, "origin", "feat")
	if err != nil {
		t.Fatalf("RemoteBranchExists: %v", err)
	}
	if !exists {
		t.Fatal("RemoteBranchExists = false after Push, want true")
	}

	// origin/main moves again, so the rebase rewrites feat and the push needs
	// the lease.
	writeGitFile(t, seed, "main3.txt", "v3\n")
	runGit(t, seed, "add", "main3.txt")
	runGit(t, seed, "commit", "-m", "main 3")
	runGit(t, seed, "push", "origin", "main")

	if err := client.Fetch(ctx, work, "origin", "main"); err != nil {
		t.Fatalf("Fetch (2): %v", err)
	}
	if paths, err := client.Rebase(ctx, work, "origin/main"); err != nil || len(paths) != 0 {
		t.Fatalf("Rebase (2) = (%v, %v), want no conflicts", paths, err)
	}
	if err := client.Push(ctx, work, "origin", "feat", true); err != nil {
		t.Fatalf("Push --force-with-lease: %v", err)
	}
}

// TestClientSetsNoOptionalLocks pins GIT_OPTIONAL_LOCKS=0 on both command paths:
// the client's binary is a shell script in place of git, and it prints the
// variable's value where each path parses its output.
func TestClientSetsNoOptionalLocks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	fakeGit := filepath.Join(dir, "fake-git")
	script := `#!/bin/sh
case "$*" in
*--numstat*)
	printf '1\t1\tfile.txt\n'
	;;
*diff*)
	printf 'GIT_OPTIONAL_LOCKS=%s\n' "${GIT_OPTIONAL_LOCKS:-unset}"
	;;
*)
	printf '%s\n' "${GIT_OPTIONAL_LOCKS:-unset}"
	;;
esac
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	client := NewClient(fakeGit, 5*time.Second, DefaultMaxPatchBytes)

	// The run path: every Client method but DiffTrees' patch read uses it.
	sha, err := client.HeadCommit(ctx, dir)
	if err != nil {
		t.Fatalf("HeadCommit through the fake git: %v", err)
	}
	if sha != "0" {
		t.Errorf("run path GIT_OPTIONAL_LOCKS = %q, want %q", sha, "0")
	}

	// The diff path builds its own exec.Command, so it does not inherit run's
	// environment by construction.
	d, err := client.DiffTrees(ctx, dir, "from", "to")
	if err != nil {
		t.Fatalf("DiffTrees through the fake git: %v", err)
	}
	if got := strings.TrimSpace(string(d.Patch)); got != "GIT_OPTIONAL_LOCKS=0" {
		t.Errorf("diff path reported %q, want GIT_OPTIONAL_LOCKS=0", got)
	}
}
