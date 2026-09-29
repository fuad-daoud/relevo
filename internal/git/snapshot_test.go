package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotTreeCapturesWorkingTree(t *testing.T) {
	ctx := context.Background()
	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "initial.txt", "hello\n")
	runGit(t, repoDir, "add", "initial.txt")
	runGit(t, repoDir, "commit", "-m", "initial")
	writeGitFile(t, repoDir, ".gitignore", "ignored.txt\n")
	runGit(t, repoDir, "add", ".gitignore")
	runGit(t, repoDir, "commit", "-m", "add gitignore")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	statusBefore := runGit(t, repoDir, "status", "--porcelain")

	tree1, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}
	if tree1 == "" {
		t.Fatal("tree1 is empty")
	}
	if got := runGit(t, repoDir, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("git status changed after snapshot: got %q, want %q", got, statusBefore)
	}

	tree2, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree 2: %v", err)
	}
	if tree1 != tree2 {
		t.Fatalf("tree2 (%s) != tree1 (%s) with no changes", tree2, tree1)
	}

	// An ignored file is not part of the snapshot.
	writeGitFile(t, repoDir, "ignored.txt", "secret\n")
	treeIgnored, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree with ignored file: %v", err)
	}
	if treeIgnored != tree1 {
		t.Fatalf("treeIgnored (%s) != tree1 (%s) after adding ignored file", treeIgnored, tree1)
	}

	// An untracked file is.
	writeGitFile(t, repoDir, "new_file.txt", "line1\nline2\n")
	tree3, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree 3: %v", err)
	}
	if tree3 == tree1 {
		t.Fatal("tree3 equals tree1 after adding untracked file")
	}
	if got := runGit(t, repoDir, "status", "--porcelain"); !strings.Contains(got, "?? new_file.txt") {
		t.Fatalf("git status does not show new_file.txt as untracked: %s", got)
	}
}

func TestDiffTreesStatAndPatch(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initRepoWithCommit(t, "initial.txt", "hello\n")
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	tree1, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}
	tree2, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree 2: %v", err)
	}

	emptyDiff, err := client.DiffTrees(ctx, repoDir, tree1, tree2)
	if err != nil {
		t.Fatalf("DiffTrees empty: %v", err)
	}
	if !emptyDiff.Stat.Empty() {
		t.Fatalf("expected empty stat, got %+v", emptyDiff.Stat)
	}
	if emptyDiff.Patch != nil {
		t.Fatalf("expected nil patch for empty diff, got %q", string(emptyDiff.Patch))
	}

	writeGitFile(t, repoDir, "new_file.txt", "line1\nline2\n")
	tree3, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree 3: %v", err)
	}

	diff, err := client.DiffTrees(ctx, repoDir, tree1, tree3)
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	if diff.Stat.FilesChanged != 1 || diff.Stat.Insertions != 2 || diff.Stat.Deletions != 0 {
		t.Fatalf("unexpected stat: %+v", diff.Stat)
	}
	if diff.Truncated {
		t.Fatal("unexpected truncated diff")
	}
	if !strings.Contains(string(diff.Patch), "+line1") || !strings.Contains(string(diff.Patch), "+line2") {
		t.Fatalf("patch missing content: %s", string(diff.Patch))
	}
}

func TestDiffWorktreeStat(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initRepoWithCommit(t, "a.txt", "hello\n")
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	tree := strings.TrimSpace(runGit(t, repoDir, "write-tree"))

	// A clean worktree against its own tree diffs to zero.
	stat, err := client.DiffWorktreeStat(ctx, repoDir, tree)
	if err != nil {
		t.Fatalf("DiffWorktreeStat clean: %v", err)
	}
	if !stat.Empty() {
		t.Fatalf("expected empty stat on a clean worktree, got %+v", stat)
	}

	// Edit the tracked file and stage a new one: the tree is now behind both.
	writeGitFile(t, repoDir, "a.txt", "hello\nworld\n")
	writeGitFile(t, repoDir, "b.txt", "new\n")
	runGit(t, repoDir, "add", "b.txt")

	stat, err = client.DiffWorktreeStat(ctx, repoDir, tree)
	if err != nil {
		t.Fatalf("DiffWorktreeStat dirty: %v", err)
	}
	if stat.FilesChanged != 2 || stat.Insertions != 2 || stat.Deletions != 0 {
		t.Fatalf("unexpected stat: %+v", stat)
	}

	if _, err := client.DiffWorktreeStat(ctx, t.TempDir(), tree); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("DiffWorktreeStat outside a repo: got %v, want ErrNotRepo", err)
	}
}

func TestClientDiffTruncation(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initRepoWithCommit(t, "a.txt", "hello\n")
	client := NewClient("git", 5*time.Second, 10) // tiny 10-byte cap

	tree1, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}

	writeGitFile(t, repoDir, "b.txt", strings.Repeat("a long line of text\n", 5))
	tree2, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}

	diff, err := client.DiffTrees(ctx, repoDir, tree1, tree2)
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	if !diff.Truncated {
		t.Fatal("expected Truncated to be true")
	}
	if diff.Patch != nil {
		t.Fatal("expected Patch to be nil when Truncated")
	}
	if diff.Stat.FilesChanged != 1 {
		t.Fatalf("expected 1 file changed, got %d", diff.Stat.FilesChanged)
	}
}

// TestSnapshotTreeCleanTreeNeedsNoTempIndex points TMPDIR at a missing directory
// so the temp-index path's os.MkdirTemp would fail if it ran: only the clean
// fast path can produce a tree here.
func TestSnapshotTreeCleanTreeNeedsNoTempIndex(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir, _ := initRepoWithCommit(t, "a.txt", "hello\n")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	want := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD^{tree}"))
	got, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree on a clean tree: %v", err)
	}
	if got != want {
		t.Fatalf("SnapshotTree = %s, want HEAD^{tree} = %s", got, want)
	}

	// Ignored files do not count as dirty.
	writeGitFile(t, repoDir, ".gitignore", "*.log\n")
	runGit(t, repoDir, "add", ".gitignore")
	runGit(t, repoDir, "commit", "-m", "ignore logs")
	writeGitFile(t, repoDir, "x.log", "noise\n")

	wantIgnored := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD^{tree}"))
	gotIgnored, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree with an ignored file: %v", err)
	}
	if gotIgnored != wantIgnored {
		t.Fatalf("SnapshotTree with an ignored file = %s, want HEAD^{tree} = %s", gotIgnored, wantIgnored)
	}
}

func TestSnapshotTreeDirtyTreeCapturesChanges(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir, _ := initRepoWithCommit(t, "a.txt", "hello\n")
	writeGitFile(t, repoDir, "new_file.txt", "line1\nline2\n")

	statusBefore := runGit(t, repoDir, "status", "--porcelain")
	headTree := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD^{tree}"))

	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree on a dirty tree: %v", err)
	}
	if tree == headTree {
		t.Fatalf("SnapshotTree = HEAD^{tree} (%s) with an untracked file present", tree)
	}
	if lsTree := runGit(t, repoDir, "ls-tree", "-r", "--name-only", tree); !strings.Contains(lsTree, "new_file.txt") {
		t.Fatalf("tree %s does not list new_file.txt:\n%s", tree, lsTree)
	}
	if got := runGit(t, repoDir, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("git status changed after snapshot: got %q, want %q", got, statusBefore)
	}
}

// TestSnapshotTreeRacyCleanEntry pins the mtime the temp index keeps. An entry
// whose mtime is not older than the index's own is racy-clean: git must re-read
// it, and a same-size edit written in the index's timestamp tick must be
// captured, not taken from the cached stat.
func TestSnapshotTreeRacyCleanEntry(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// A fixed past tick, so setup does not depend on the machine's speed. The
	// file's mtime and the index's mtime are made identical.
	tick := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "a.txt", "one\n")
	if err := os.Chtimes(filepath.Join(repoDir, "a.txt"), tick, tick); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "a.txt")
	runGit(t, repoDir, "commit", "-m", "first")

	// Same size, same mtime: the edit is indistinguishable from the recorded
	// content by stat alone, so git must re-read a.txt.
	writeGitFile(t, repoDir, "a.txt", "two\n")
	if err := os.Chtimes(filepath.Join(repoDir, "a.txt"), tick, tick); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(repoDir, ".git", "index"), tick, tick); err != nil {
		t.Fatal(err)
	}

	// No runGit call from here to SnapshotTree: runGit does not set
	// GIT_OPTIONAL_LOCKS=0, so e.g. `git status` would take the index lock,
	// rewrite the index and smudge the racy entry.
	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree on a racy-clean tree: %v", err)
	}
	if blob := runGit(t, repoDir, "cat-file", "-p", tree+":a.txt"); blob != "two\n" {
		t.Fatalf("snapshot a.txt = %q, want %q: a same-size edit in the index's own timestamp tick kept the old blob", blob, "two\n")
	}
}

func TestSnapshotTreeUnbornHeadFallsBack(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "new_file.txt", "line1\n")

	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree with an unborn HEAD: %v", err)
	}
	if lsTree := runGit(t, repoDir, "ls-tree", "-r", "--name-only", tree); !strings.Contains(lsTree, "new_file.txt") {
		t.Fatalf("tree %s does not list new_file.txt:\n%s", tree, lsTree)
	}
}

func TestMaterializeTreeCarriesWorkingState(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	repoDir := dirtyWorktree(t)
	statusBefore := runGit(t, repoDir, "status", "--porcelain")
	branchesBefore := runGit(t, repoDir, "branch", "--list")
	indexBefore, err := os.ReadFile(filepath.Join(repoDir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	head, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}

	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := client.AddDetachedWorktree(ctx, repoDir, scratch, head); err != nil {
		t.Fatalf("AddDetachedWorktree: %v", err)
	}
	if err := client.MaterializeTree(ctx, scratch, tree); err != nil {
		t.Fatalf("MaterializeTree: %v", err)
	}

	for _, c := range []struct{ name, want string }{
		{"a.txt", "a2\n"},
		{"b.txt", "b2\n"},
		{"d.txt", "d1\n"},
	} {
		got, err := os.ReadFile(filepath.Join(scratch, c.name))
		if err != nil {
			t.Fatalf("scratch %s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("scratch %s = %q, want %q", c.name, got, c.want)
		}
	}
	for _, name := range []string{"c.txt", "e.log"} {
		if _, err := os.Stat(filepath.Join(scratch, name)); !os.IsNotExist(err) {
			t.Errorf("scratch %s should be absent, stat err = %v", name, err)
		}
	}
	if got := strings.TrimSpace(runGit(t, scratch, "rev-parse", "HEAD")); got != head {
		t.Errorf("scratch HEAD = %s, want %s", got, head)
	}

	// Every difference is unstaged, and the untracked file stays untracked.
	wantStatus := " M a.txt\n M b.txt\n D c.txt\n?? d.txt\n"
	if got := runGit(t, scratch, "status", "--porcelain"); got != wantStatus {
		t.Errorf("scratch status --porcelain =\n%q\nwant\n%q", got, wantStatus)
	}

	// The source is untouched.
	if got := runGit(t, repoDir, "status", "--porcelain"); got != statusBefore {
		t.Errorf("source status changed: got %q, want %q", got, statusBefore)
	}
	indexAfter, err := os.ReadFile(filepath.Join(repoDir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexAfter, indexBefore) {
		t.Error("source index changed")
	}
	if got := runGit(t, repoDir, "branch", "--list"); got != branchesBefore {
		t.Errorf("source branches changed: got %q, want %q", got, branchesBefore)
	}
}

func TestScratchWritesNeverReachTheSource(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	repoDir := dirtyWorktree(t)
	statusBefore := runGit(t, repoDir, "status", "--porcelain")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	head, err := client.HeadCommit(ctx, repoDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	tree, err := client.SnapshotTree(ctx, repoDir)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}

	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := client.AddDetachedWorktree(ctx, repoDir, scratch, head); err != nil {
		t.Fatalf("AddDetachedWorktree: %v", err)
	}
	if err := client.MaterializeTree(ctx, scratch, tree); err != nil {
		t.Fatalf("MaterializeTree: %v", err)
	}

	writeGitFile(t, scratch, "z.txt", "z\n")
	writeGitFile(t, scratch, "a.txt", "scratch\n")

	if got := runGit(t, repoDir, "status", "--porcelain"); got != statusBefore {
		t.Errorf("source status changed: got %q, want %q", got, statusBefore)
	}
	if got, err := os.ReadFile(filepath.Join(repoDir, "a.txt")); err != nil || string(got) != "a2\n" {
		t.Errorf("source a.txt = %q (err %v), want %q", got, err, "a2\n")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "z.txt")); !os.IsNotExist(err) {
		t.Errorf("source gained z.txt: %v", err)
	}

	if err := client.RemoveWorktree(ctx, repoDir, scratch, true); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch still present after removal: %v", err)
	}
	if list := runGit(t, repoDir, "worktree", "list", "--porcelain"); strings.Contains(list, scratch) {
		t.Errorf("worktree list still names the scratch:\n%s", list)
	}
}
