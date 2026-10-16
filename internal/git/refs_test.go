package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeadCommit(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	if _, err := client.HeadCommit(ctx, initRepo(t)); err == nil {
		t.Fatal("HeadCommit on repo with no commits expected error, got nil")
	}

	repo, want := initRepoWithCommit(t, "a.txt", "hello\n")
	got, err := client.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if len(got) != 40 {
		t.Fatalf("expected 40-char commit hash, got %q", got)
	}
	if got != want {
		t.Fatalf("HeadCommit = %q, want %q", got, want)
	}
}

func TestBranchExists(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, _ := initRepoWithCommit(t, "a.txt", "hello\n")
	defaultBranch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current"))

	tests := []struct {
		name   string
		branch string
		want   bool
	}{
		{"short name", defaultBranch, true},
		{"refs/heads prefix", "refs/heads/" + defaultBranch, true},
		{"unknown branch", "nonexistent-branch-xyz", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exists, err := client.BranchExists(ctx, repo, tc.branch)
			if err != nil {
				t.Fatalf("BranchExists(%q): %v", tc.branch, err)
			}
			if exists != tc.want {
				t.Fatalf("BranchExists(%q) = %v, want %v", tc.branch, exists, tc.want)
			}
		})
	}
}

// TestDirty runs the state machine a row at a time, each step mutating the same
// repo before Dirty is asked again.
func TestDirty(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo := initRepo(t)

	steps := []struct {
		name  string
		act   func(t *testing.T, dir string)
		dirty bool
	}{
		{"unborn HEAD with no files", func(*testing.T, string) {}, false},
		{"untracked file", func(t *testing.T, dir string) { writeGitFile(t, dir, "a.txt", "hello\n") }, true},
		{"committed file", func(t *testing.T, dir string) {
			runGit(t, dir, "add", "a.txt")
			runGit(t, dir, "commit", "-m", "first")
		}, false},
		{"staged new file", func(t *testing.T, dir string) {
			writeGitFile(t, dir, "untracked.txt", "foo\n")
			runGit(t, dir, "add", "untracked.txt")
		}, true},
		{"committed staged file", func(t *testing.T, dir string) { runGit(t, dir, "commit", "-m", "second") }, false},
		{"modified tracked file", func(t *testing.T, dir string) { writeGitFile(t, dir, "untracked.txt", "bar\n") }, true},
		{"reverted edit", func(t *testing.T, dir string) { runGit(t, dir, "checkout", "--", "untracked.txt") }, false},
		{"ignored untracked file", func(t *testing.T, dir string) {
			writeGitFile(t, dir, ".gitignore", "ignored.txt\n")
			runGit(t, dir, "add", ".gitignore")
			runGit(t, dir, "commit", "-m", "ignore")
			writeGitFile(t, dir, "ignored.txt", "skip\n")
		}, false},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.act(t, repo)
			dirty, err := client.Dirty(ctx, repo)
			if err != nil {
				t.Fatalf("Dirty: %v", err)
			}
			if dirty != step.dirty {
				t.Fatalf("Dirty = %v, want %v", dirty, step.dirty)
			}
		})
	}
}

// TestClientErrorMapping checks the two sentinels every method must report:
// ErrNotRepo outside a repository, ErrGitUnavailable with no git binary.
func TestClientErrorMapping(t *testing.T) {
	ctx := context.Background()
	notRepoDir := t.TempDir()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	badClient := NewClient("nonexistent-git-binary-xyz", 5*time.Second, DefaultMaxPatchBytes)
	zero := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

	tests := []struct {
		name string
		call func(c *Client) error
	}{
		{"SnapshotTree", func(c *Client) error { _, err := c.SnapshotTree(ctx, notRepoDir); return err }},
		{"DiffTrees", func(c *Client) error { _, err := c.DiffTrees(ctx, notRepoDir, zero, zero); return err }},
		{"HeadCommit", func(c *Client) error { _, err := c.HeadCommit(ctx, notRepoDir); return err }},
		{"BranchExists", func(c *Client) error { _, err := c.BranchExists(ctx, notRepoDir, "main"); return err }},
		{"AddWorktree", func(c *Client) error {
			return c.AddWorktree(ctx, notRepoDir, filepath.Join(notRepoDir, "wt"), "branch", "HEAD")
		}},
		{"RemoveWorktree", func(c *Client) error {
			return c.RemoveWorktree(ctx, notRepoDir, filepath.Join(notRepoDir, "wt"), false)
		}},
		{"Dirty", func(c *Client) error { _, err := c.Dirty(ctx, notRepoDir); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(client); !errors.Is(err, ErrNotRepo) {
				t.Fatalf("outside a repo: got %v, want ErrNotRepo", err)
			}
			if err := tc.call(badClient); !errors.Is(err, ErrGitUnavailable) {
				t.Fatalf("with a missing binary: got %v, want ErrGitUnavailable", err)
			}
		})
	}
}

func TestRevListCount(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, base := initRepoWithCommit(t, "a.txt", "one\n")

	n, err := client.RevListCount(ctx, repo, base, base)
	if err != nil || n != 0 {
		t.Fatalf("RevListCount(base..base) = %d, %v; want 0, nil", n, err)
	}

	for i, name := range []string{"b.txt", "c.txt"} {
		writeGitFile(t, repo, name, "x\n")
		runGit(t, repo, "add", name)
		runGit(t, repo, "commit", "-m", fmt.Sprintf("commit %d", i+2))
	}
	head := mustHead(t, repo)
	n, err = client.RevListCount(ctx, repo, base, head)
	if err != nil || n != 2 {
		t.Fatalf("RevListCount(base..head) = %d, %v; want 2, nil", n, err)
	}

	if _, err := client.RevListCount(ctx, repo, "0123456789abcdef0123456789abcdef01234567", head); err == nil {
		t.Fatal("RevListCount with an unresolvable ref returned nil error")
	}
	if _, err := client.RevListCount(ctx, t.TempDir(), base, head); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("RevListCount outside a repo: err = %v, want ErrNotRepo", err)
	}
}

func TestRootCommit(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	if _, err := client.RootCommit(ctx, initRepo(t)); !errors.Is(err, ErrRefMissing) {
		t.Fatalf("RootCommit(empty): got err %v, want ErrRefMissing", err)
	}

	repo, shas := repoWithCommits(t, "1\n", "2\n")
	root, err := client.RootCommit(ctx, repo)
	if err != nil {
		t.Fatalf("RootCommit: %v", err)
	}
	if root != shas[0] {
		t.Fatalf("RootCommit = %q, want first commit %q", root, shas[0])
	}
}

func TestRefSHAMissingIsOkFalse(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, headSHA := initRepoWithCommit(t, "f.txt", "hi\n")

	sha, ok, err := client.RefSHA(ctx, repo, "refs/heads/nope")
	if err != nil || ok || sha != "" {
		t.Fatalf("RefSHA(refs/heads/nope) = (%q, %v, %v); want (\"\", false, nil)", sha, ok, err)
	}

	sha, ok, err = client.RefSHA(ctx, repo, "HEAD")
	if err != nil || !ok || sha != headSHA {
		t.Fatalf("RefSHA(HEAD) = (%q, %v, %v); want (%q, true, nil)", sha, ok, err, headSHA)
	}
}

func TestUpdateRefCreatesAndCAS(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, shas := repoWithCommits(t, "1\n", "2\n")
	c1, c2 := shas[0], shas[1]
	ref := "refs/heads/testref"

	if err := client.UpdateRef(ctx, repo, ref, c1, ""); err != nil {
		t.Fatalf("UpdateRef create: %v", err)
	}
	if sha := strings.TrimSpace(runGit(t, repo, "rev-parse", ref)); sha != c1 {
		t.Fatalf("ref after create = %q, want %q", sha, c1)
	}

	// A CAS mismatch errors and leaves the ref where it was.
	wrongSHA := "0123456789abcdef0123456789abcdef01234567"
	if err := client.UpdateRef(ctx, repo, ref, c2, wrongSHA); err == nil {
		t.Fatal("UpdateRef CAS with wrong old sha succeeded, want error")
	}
	if sha := strings.TrimSpace(runGit(t, repo, "rev-parse", ref)); sha != c1 {
		t.Fatalf("ref after failed CAS = %q, want %q", sha, c1)
	}

	if err := client.UpdateRef(ctx, repo, ref, c2, c1); err != nil {
		t.Fatalf("UpdateRef CAS with correct old sha: %v", err)
	}
	if sha := strings.TrimSpace(runGit(t, repo, "rev-parse", ref)); sha != c2 {
		t.Fatalf("ref after successful CAS = %q, want %q", sha, c2)
	}
}

// TestClientRefs covers the ref helpers the server's settled-binding cleanup
// uses: create, list by prefix, delete, and delete a missing one.
func TestClientRefs(t *testing.T) {
	ctx := context.Background()
	c := NewClient("git", 0, 0)
	repo, sha := initRepoWithCommit(t, "a.txt", "a\n")

	for _, ref := range []string{
		"refs/relevo/api/out",
		"refs/relevo/api/round-1",
		"refs/relevo/other/out",
	} {
		if err := c.UpdateRef(ctx, repo, ref, sha, ""); err != nil {
			t.Fatalf("update-ref %s: %v", ref, err)
		}
	}

	got, err := c.ListRefs(ctx, repo, "refs/relevo/api/")
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	want := []string{"refs/relevo/api/out", "refs/relevo/api/round-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ListRefs = %v, want %v", got, want)
	}

	if err := c.DeleteRef(ctx, repo, "refs/relevo/api/out"); err != nil {
		t.Fatalf("DeleteRef existing: %v", err)
	}
	got, err = c.ListRefs(ctx, repo, "refs/relevo/api/")
	if err != nil {
		t.Fatalf("ListRefs after delete: %v", err)
	}
	if strings.Join(got, ",") != "refs/relevo/api/round-1" {
		t.Fatalf("ListRefs after delete = %v, want [refs/relevo/api/round-1]", got)
	}

	// A missing ref is success, and deleting it changes nothing.
	if err := c.DeleteRef(ctx, repo, "refs/relevo/api/out"); err != nil {
		t.Fatalf("DeleteRef missing: %v", err)
	}

	// The other binding's ref is untouched.
	if gotSHA, ok, err := c.RefSHA(ctx, repo, "refs/relevo/other/out"); err != nil || !ok || gotSHA != sha {
		t.Fatalf("refs/relevo/other/out = (%q, %v, %v), want %q", gotSHA, ok, err, sha)
	}
}

func TestListTagsPeelsAnnotated(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir, sha1 := initRepoWithCommit(t, "a.txt", "one\n")
	runGit(t, repoDir, "-c", "tag.gpgsign=false", "tag", "lw")

	writeGitFile(t, repoDir, "b.txt", "two\n")
	runGit(t, repoDir, "add", "b.txt")
	runGit(t, repoDir, "commit", "-m", "second")
	sha2 := mustHead(t, repoDir)
	runGit(t, repoDir, "-c", "tag.gpgsign=false", "tag", "-a", "-m", "x", "an")

	tags, err := client.ListTags(ctx, repoDir)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("ListTags = %v, want 2 tags", tags)
	}
	if tags["lw"] != sha1 {
		t.Errorf("tags[lw] = %q, want %q", tags["lw"], sha1)
	}
	if tags["an"] != sha2 {
		t.Errorf("tags[an] = %q, want %q (the peeled commit, not the tag object)", tags["an"], sha2)
	}

	empty, err := client.ListTags(ctx, initRepo(t))
	if err != nil {
		t.Fatalf("ListTags (untagged): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListTags (untagged) = %v, want an empty map", empty)
	}
}

func TestCurrentBranchDetached(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, _ := initRepoWithCommit(t, "file.txt", "v1\n")

	name, err := client.CurrentBranch(ctx, repo)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if name == "" || name == "HEAD" {
		t.Errorf("CurrentBranch = %q, want the checked-out branch name", name)
	}

	runGit(t, repo, "checkout", "--detach", mustHead(t, repo))
	name, err = client.CurrentBranch(ctx, repo)
	if err != nil {
		t.Fatalf("CurrentBranch (detached): %v", err)
	}
	if name != "" {
		t.Errorf("CurrentBranch (detached) = %q, want %q", name, "")
	}
}

// TestRefOnRemote covers the report of whether ref's commit was pushed, through
// a local branch, a branch with an extra commit, an ancestor and a relevo ref.
func TestRefOnRemote(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	c := NewClient("git", 0, 0)
	repoDir := initRepo(t)
	runGit(t, repoDir, "remote", "add", "origin", bareRepo(t))

	writeGitFile(t, repoDir, "file.txt", "v1\n")
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "commit 1")
	ancestorSHA := mustHead(t, repoDir)
	runGit(t, repoDir, "branch", "ancestor-branch", ancestorSHA)

	writeGitFile(t, repoDir, "file.txt", "v2\n")
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "commit 2")
	pushedSHA := mustHead(t, repoDir)

	currentBranch := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))
	runGit(t, repoDir, "push", "-u", "origin", currentBranch)
	runGit(t, repoDir, "fetch", "origin")
	runGit(t, repoDir, "branch", "pushed-branch", pushedSHA)

	// A branch at the pushed commit, and at one of its ancestors, counts as
	// pushed; a branch with one extra local commit does not.
	runGit(t, repoDir, "checkout", "-b", "unpushed-branch", pushedSHA)
	writeGitFile(t, repoDir, "file.txt", "v3\n")
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "commit 3")

	if err := c.UpdateRef(ctx, repoDir, "refs/relevo/x/round-1", pushedSHA, ""); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	tests := []struct {
		name string
		ref  string
		want bool
	}{
		{"local branch at the pushed commit", "refs/heads/pushed-branch", true},
		{"branch with an extra local commit", "refs/heads/unpushed-branch", false},
		{"branch at an ancestor of the pushed commit", "refs/heads/ancestor-branch", true},
		{"relevo ref at the pushed commit", "refs/relevo/x/round-1", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			on, err := c.RefOnRemote(ctx, repoDir, tc.ref)
			if err != nil {
				t.Fatalf("RefOnRemote: %v", err)
			}
			if on != tc.want {
				t.Fatalf("RefOnRemote(%s) = %v, want %v", tc.ref, on, tc.want)
			}
		})
	}

	if _, err := c.RefOnRemote(ctx, repoDir, "refs/heads/nonexistent"); err == nil {
		t.Error("RefOnRemote on a nonexistent ref: got nil error, want one")
	}
}

func TestInitBare(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	barePath := filepath.Join(t.TempDir(), "sub", "test.git")

	if err := client.InitBare(ctx, barePath); err != nil {
		t.Fatalf("InitBare failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(barePath, "HEAD")); err != nil {
		t.Fatalf("HEAD file does not exist: %v", err)
	}
	if err := client.InitBare(ctx, barePath); err != nil {
		t.Fatalf("second InitBare failed: %v", err)
	}
}

func TestCreateBranch(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir, headSHA := initRepoWithCommit(t, "file.txt", "content\n")

	if err := client.CreateBranch(ctx, repoDir, "feature", headSHA); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	sha, ok, err := client.RefSHA(ctx, repoDir, "refs/heads/feature")
	if err != nil || !ok {
		t.Fatalf("RefSHA failed: ok=%v, err=%v", ok, err)
	}
	if sha != headSHA {
		t.Fatalf("branch sha = %q, want %q", sha, headSHA)
	}

	if err := client.CreateBranch(ctx, repoDir, "feature", headSHA); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("second CreateBranch got %v, want ErrBranchExists", err)
	}
	if err := client.CreateBranch(ctx, repoDir, "refs/heads/feature", headSHA); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("prefixed CreateBranch got %v, want ErrBranchExists", err)
	}
}

func TestDeleteBranch(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir, headSHA := initRepoWithCommit(t, "file.txt", "content\n")

	if err := client.CreateBranch(ctx, repoDir, "feature", headSHA); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := client.DeleteBranch(ctx, repoDir, "feature"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if exists, err := client.BranchExists(ctx, repoDir, "feature"); err != nil || exists {
		t.Fatalf("branch after DeleteBranch: exists=%v err=%v, want false, nil", exists, err)
	}

	// A missing branch is nil, whether it was just removed or never existed.
	if err := client.DeleteBranch(ctx, repoDir, "feature"); err != nil {
		t.Fatalf("DeleteBranch on already-removed branch: got %v, want nil", err)
	}
	if err := client.DeleteBranch(ctx, repoDir, "never-existed"); err != nil {
		t.Fatalf("DeleteBranch on never-created branch: got %v, want nil", err)
	}

	// The prefixed form addresses the same branch.
	if err := client.CreateBranch(ctx, repoDir, "feature2", headSHA); err != nil {
		t.Fatalf("CreateBranch feature2: %v", err)
	}
	if err := client.DeleteBranch(ctx, repoDir, "refs/heads/feature2"); err != nil {
		t.Fatalf("DeleteBranch with refs/heads/ prefix: %v", err)
	}
	if exists, err := client.BranchExists(ctx, repoDir, "feature2"); err != nil || exists {
		t.Fatalf("feature2 after prefixed DeleteBranch: exists=%v err=%v, want false, nil", exists, err)
	}
}

func TestCreateTrackingBranch(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	// feat exists only as refs/remotes/origin/feat in repoA: it is created in
	// the bare clone B and fetched back.
	repoA, _ := initRepoWithCommit(t, "file.txt", "v1\n")
	sha := mustHead(t, repoA)
	repoB := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(repoB), "clone", "--bare", repoA, repoB)
	runGit(t, repoA, "remote", "add", "origin", repoB)
	runGit(t, repoA, "fetch", "origin")
	runGit(t, repoB, "branch", "feat", sha)
	runGit(t, repoA, "fetch", "origin")

	if exists, err := client.BranchExists(ctx, repoA, "feat"); err != nil || exists {
		t.Fatalf("feat must not exist locally before tracking: exists=%v err=%v", exists, err)
	}
	if err := client.CreateTrackingBranch(ctx, repoA, "feat", "origin/feat"); err != nil {
		t.Fatalf("CreateTrackingBranch: %v", err)
	}
	if up := strings.TrimSpace(runGit(t, repoA, "rev-parse", "--abbrev-ref", "feat@{u}")); up != "origin/feat" {
		t.Fatalf("feat upstream = %q, want origin/feat", up)
	}
	if err := client.CreateTrackingBranch(ctx, repoA, "feat", "origin/feat"); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("second CreateTrackingBranch = %v, want ErrBranchExists", err)
	}
}

// TestBlobAtResolvesOnlyARegularFileAtTheRef pins what a seed may copy from a
// base branch: a regular file resolves to its blob, while a path the ref lacks,
// a directory, a symlink and a prefix of a real name all answer not-found, and
// a ref that does not exist is an error.
func TestBlobAtResolvesOnlyARegularFileAtTheRef(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	dir := initRepo(t)
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeGitFile(t, dir, "docs/spec.md", "the spec\n")
	if err := os.Symlink("docs/spec.md", filepath.Join(dir, "link.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "spec")
	head := mustHead(t, dir)

	oid, ok, err := client.BlobAt(ctx, dir, head, "docs/spec.md")
	if err != nil || !ok {
		t.Fatalf("BlobAt(docs/spec.md) = %q, %v, %v; want the file's blob", oid, ok, err)
	}
	if want := strings.TrimSpace(runGit(t, dir, "rev-parse", head+":docs/spec.md")); oid != want {
		t.Errorf("BlobAt oid = %s, want %s", oid, want)
	}
	for _, path := range []string{"docs/missing.md", "docs", "link.md", "docs/spec"} {
		if oid, ok, err := client.BlobAt(ctx, dir, head, path); err != nil || ok {
			t.Errorf("BlobAt(%s) = %q, %v, %v; want not found", path, oid, ok, err)
		}
	}
	if _, _, err := client.BlobAt(ctx, dir, "no-such-ref", "docs/spec.md"); err == nil {
		t.Error("BlobAt at a ref that does not exist = nil error, want one")
	}
}

// TestShowPrefixNamesTheSubdirectoryAndIsEmptyAtTheRoot pins the two answers a
// bind needs: a caller sitting in a subdirectory is given that path (with
// git's own trailing slash), a caller at the root an empty prefix.
// Mutation: dropping the TrimSpace would leave the newline in the answer.
func TestShowPrefixNamesTheSubdirectoryAndIsEmptyAtTheRoot(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, _ := initRepoWithCommit(t, "a.txt", "hello\n")
	if err := os.MkdirAll(filepath.Join(repo, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "deep", "file.txt"), []byte("deep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "sub")
	runGit(t, repo, "commit", "-m", "subdir")

	root, err := client.ShowPrefix(ctx, repo)
	if err != nil {
		t.Fatalf("ShowPrefix at the root: %v", err)
	}
	if root != "" {
		t.Fatalf("ShowPrefix at the root = %q, want empty", root)
	}

	inSub, err := client.ShowPrefix(ctx, filepath.Join(repo, "sub", "deep"))
	if err != nil {
		t.Fatalf("ShowPrefix in a subdirectory: %v", err)
	}
	if inSub != "sub/deep/" {
		t.Fatalf("ShowPrefix = %q, want %q", inSub, "sub/deep/")
	}
}

// TestDirAtReportsADirectoryOnly pins tree-only answer: a commit that has a
// directory at path is true, a file at that path is false, a path with no
// entry at all is false, and the path is the root's even when git runs in a
// subdirectory.
// Mutation: accepting a blob would let a bind cut a round that runs in a file.
func TestDirAtReportsADirectoryOnly(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repo, _ := initRepoWithCommit(t, "a.txt", "hello\n")
	if err := os.MkdirAll(filepath.Join(repo, "mud"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mud", "inside.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "mud")
	runGit(t, repo, "commit", "-m", "mud")
	tip := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	tests := []struct {
		name string
		from string
		path string
		ref  string
		want bool
	}{
		{"the tree is there", "", "mud", tip, true},
		{"the root itself is not a file", "", "a.txt", tip, false},
		{"no entry at all", "", "ghost", tip, false},
		{"a path is the root's when git runs below it", "mud", "mud", tip, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := client.DirAt(ctx, filepath.Join(repo, tc.from), tc.ref, tc.path)
			if err != nil {
				t.Fatalf("DirAt(%q): %v", tc.path, err)
			}
			if ok != tc.want {
				t.Fatalf("DirAt(%q) = %v, want %v", tc.path, ok, tc.want)
			}
		})
	}
}
