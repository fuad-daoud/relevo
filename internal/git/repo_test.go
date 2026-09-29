package git

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoFactsNoRemote(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := initRepo(t)

	originURL, commonDir, err := client.RepoFacts(ctx, repoDir)
	if err != nil {
		t.Fatalf("RepoFacts: %v", err)
	}
	if originURL != "" {
		t.Errorf("originURL = %q, want empty", originURL)
	}
	// Canonical, as RepoFacts returns it: macOS resolves /var to /private/var.
	wantCommonDir, err := filepath.EvalSymlinks(filepath.Join(repoDir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if commonDir != wantCommonDir {
		t.Errorf("commonDir = %q, want %q", commonDir, wantCommonDir)
	}
	if !filepath.IsAbs(commonDir) {
		t.Errorf("commonDir %q is not absolute", commonDir)
	}
}

func TestRepoFactsWithOrigin(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := initRepo(t)
	runGit(t, repoDir, "remote", "add", "origin", "git@github.com:o/r.git")

	originURL, commonDir, err := client.RepoFacts(ctx, repoDir)
	if err != nil {
		t.Fatalf("RepoFacts: %v", err)
	}
	if originURL != "git@github.com:o/r.git" {
		t.Errorf("originURL = %q, want the raw remote value unnormalised", originURL)
	}
	wantCommonDir, err := filepath.EvalSymlinks(filepath.Join(repoDir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if commonDir != wantCommonDir {
		t.Errorf("commonDir = %q, want %q", commonDir, wantCommonDir)
	}
}

func TestRepoFactsFromWorktree(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir, headSHA := initRepoWithCommit(t, "file.txt", "content\n")

	if err := client.CreateBranch(ctx, repoDir, "wt", headSHA); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	worktreeDir := filepath.Join(t.TempDir(), "wt")
	if err := client.CheckoutWorktree(ctx, repoDir, worktreeDir, "wt"); err != nil {
		t.Fatalf("CheckoutWorktree: %v", err)
	}

	_, mainCommonDir, err := client.RepoFacts(ctx, repoDir)
	if err != nil {
		t.Fatalf("RepoFacts(main): %v", err)
	}
	_, worktreeCommonDir, err := client.RepoFacts(ctx, worktreeDir)
	if err != nil {
		t.Fatalf("RepoFacts(worktree): %v", err)
	}
	if worktreeCommonDir != mainCommonDir {
		t.Errorf("RepoFacts(worktree) commonDir = %q, want the main tree's %q", worktreeCommonDir, mainCommonDir)
	}
}

func TestRepoFactsNotARepo(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	if _, _, err := client.RepoFacts(ctx, t.TempDir()); err == nil {
		t.Fatal("RepoFacts on a non-repo directory: got nil error, want one")
	}
}

func TestIdentityReadsConfig(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	repoDir := initRepo(t) // repo-local user.name "Test", user.email "test@example.com"

	name, email, err := client.Identity(ctx, repoDir)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if name != "Test" || email != "test@example.com" {
		t.Errorf("Identity = (%q, %q), want (%q, %q)", name, email, "Test", "test@example.com")
	}
}

// TestIdentityUnsetIsEmptyNotError pins that a key `git config --get` cannot find
// exits 1 with no output: an empty value, not a failure. HOME and
// XDG_CONFIG_HOME point at empty temp dirs and the system config is off, so
// nothing can resolve a global user.name/user.email from this machine.
func TestIdentityUnsetIsEmptyNotError(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(emptyHome, "xdg"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// No repo-local identity either: this repo is init'd without one.
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	name, email, err := client.Identity(ctx, repoDir)
	if err != nil {
		t.Fatalf("Identity with nothing set: %v", err)
	}
	if name != "" || email != "" {
		t.Errorf("Identity = (%q, %q), want (\"\", \"\")", name, email)
	}
}

func TestCommitAllCommitsEverything(t *testing.T) {
	ctx := context.Background()
	dir, _ := initRepoWithCommit(t, "tracked.txt", "hello\n")

	writeGitFile(t, dir, "new.txt", "new\n")
	writeGitFile(t, dir, "tracked.txt", "changed\n")

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	sha, err := client.CommitAll(ctx, dir, "msg")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if sha == "" {
		t.Fatal("CommitAll returned an empty sha")
	}
	if got := strings.TrimSpace(runGit(t, dir, "status", "--porcelain")); got != "" {
		t.Errorf("status after CommitAll = %q, want clean", got)
	}
	if got := strings.TrimSpace(runGit(t, dir, "log", "-1", "--format=%s")); got != "msg" {
		t.Errorf("last commit subject = %q, want msg", got)
	}
	if head := mustHead(t, dir); head != sha {
		t.Errorf("HEAD = %q, want returned sha %q", head, sha)
	}

	// Nothing left to commit: ("", nil), not an error.
	sha2, err := client.CommitAll(ctx, dir, "again")
	if err != nil {
		t.Fatalf("second CommitAll: %v", err)
	}
	if sha2 != "" {
		t.Errorf("second CommitAll sha = %q, want empty", sha2)
	}
}

func TestTreeFingerprintChangesOnEditAndCommit(t *testing.T) {
	ctx := context.Background()
	dir, _ := initRepoWithCommit(t, "a.txt", "one\n")
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	first, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint: %v", err)
	}
	again, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (again): %v", err)
	}
	if first != again {
		t.Fatalf("fingerprint changed with no edit: %q then %q", first, again)
	}

	writeGitFile(t, dir, "a.txt", "two\n")
	edited, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (edited): %v", err)
	}
	if edited == first {
		t.Fatalf("fingerprint unchanged after editing a tracked file: %q", edited)
	}

	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-m", "edit")
	committed, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (committed): %v", err)
	}
	if committed == edited {
		t.Fatalf("fingerprint unchanged after committing: %q", committed)
	}

	writeGitFile(t, dir, "untracked.txt", "new\n")
	untracked, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (untracked): %v", err)
	}
	if untracked == committed {
		t.Fatalf("fingerprint unchanged after adding an untracked file: %q", untracked)
	}
}

// TestTreeFingerprintUnbornHead pins that a repo with no commit still produces a
// fingerprint from the status alone rather than erroring.
func TestTreeFingerprintUnbornHead(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	fp, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint on an unborn branch: %v", err)
	}
	if fp == "" {
		t.Fatal("fingerprint on an unborn branch is empty")
	}

	writeGitFile(t, dir, "a.txt", "one\n")
	edited, err := client.TreeFingerprint(ctx, dir)
	if err != nil {
		t.Fatalf("TreeFingerprint (untracked): %v", err)
	}
	if edited == fp {
		t.Fatalf("fingerprint unchanged after adding an untracked file: %q", edited)
	}
}

func TestNormalizeOriginURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"scp-like", "git@github.com:o/r.git", "https://github.com/o/r"},
		{"ssh scheme", "ssh://git@github.com/o/r.git", "https://github.com/o/r"},
		{"https trailing git and slash", "https://github.com/o/r.git/", "https://github.com/o/r"},
		{"http stays http, host lowercased, path case kept", "http://GitHub.com/o/R", "http://github.com/o/R"},
		{"unrecognised string returned trimmed", "  not a url  ", "not a url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeOriginURL(tc.in); got != tc.want {
				t.Errorf("NormalizeOriginURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOwnerRepo(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"https", "https://github.com/o/r", "o/r"},
		{"http", "http://github.com/o/R", "o/R"},
		{"ssh normalised to https", NormalizeOriginURL("ssh://git@github.com/o/r.git"), "o/r"},
		{"scp normalised to https", NormalizeOriginURL("git@github.com:o/r.git"), "o/r"},
		{"extra path segments take the last two", "https://gitlab.com/group/sub/repo", "sub/repo"},
		{"trailing slash", "https://github.com/o/r/", "o/r"},
		{"no host", "https://github.com", ""},
		{"no path", "https://", ""},
		{"one path segment", "https://github.com/o", ""},
		{"local path", "/home/dev/projects/webshop", ""},
		{"unrecognised string", "not a url", ""},
		{"missing origin", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := OwnerRepo(tc.in); got != tc.want {
				t.Errorf("OwnerRepo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
