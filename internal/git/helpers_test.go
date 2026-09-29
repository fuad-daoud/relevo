package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs git in dir with a fixed identity and the machine's own git config
// ignored: a global commit.gpgsign would otherwise send every fixture commit to
// gpg. A repo it initialises also gets auto-maintenance off, so no detached
// `git maintenance run` outlives the command and races t.TempDir()'s cleanup.
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
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return string(out)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// initRepo is a throwaway repository with commit and tag signing off.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	return dir
}

// initRepoWithCommit is initRepo plus one committed file, returning the repo and
// the commit's sha.
func initRepoWithCommit(t *testing.T, name, body string) (string, string) {
	t.Helper()
	dir := initRepo(t)
	writeGitFile(t, dir, name, body)
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", "first")
	return dir, mustHead(t, dir)
}

// repoWithTwoCommits is a repo whose first commit writes file.txt and whose
// second overwrites it; it returns the repo and both shas.
func repoWithTwoCommits(t *testing.T) (dir, first, second string) {
	t.Helper()
	dir = initRepo(t)
	writeGitFile(t, dir, "file.txt", "v1\n")
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", "first commit")
	first = mustHead(t, dir)
	writeGitFile(t, dir, "file.txt", "v2\n")
	runGit(t, dir, "commit", "-am", "second commit")
	second = mustHead(t, dir)
	return dir, first, second
}

// repoWithCommits creates one file and one commit per body, returning the repo
// and the commit shas in order.
func repoWithCommits(t *testing.T, bodies ...string) (string, []string) {
	t.Helper()
	dir := initRepo(t)
	shas := make([]string, 0, len(bodies))
	for i, body := range bodies {
		name := fmt.Sprintf("f%d.txt", i+1)
		writeGitFile(t, dir, name, body)
		runGit(t, dir, "add", name)
		runGit(t, dir, "commit", "-m", fmt.Sprintf("c%d", i+1))
		shas = append(shas, mustHead(t, dir))
	}
	return dir, shas
}

// bareRepo is a throwaway bare repository.
func bareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--bare")
	return dir
}

func mustHead(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func writeGitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resolvedTempDir is t.TempDir() with symlinks resolved: macOS resolves /var to
// /private/var, and git reports the resolved path.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

// dirtyWorktree is a repo whose working state is uncommitted: an unstaged edit,
// a staged edit, a deletion, an untracked file and an ignored one.
func dirtyWorktree(t *testing.T) string {
	t.Helper()
	repoDir := initRepo(t)
	writeGitFile(t, repoDir, "a.txt", "a1\n")
	writeGitFile(t, repoDir, "b.txt", "b1\n")
	writeGitFile(t, repoDir, "c.txt", "c1\n")
	writeGitFile(t, repoDir, ".gitignore", "*.log\n")
	runGit(t, repoDir, "add", "a.txt", "b.txt", "c.txt", ".gitignore")
	runGit(t, repoDir, "commit", "-m", "first")

	writeGitFile(t, repoDir, "a.txt", "a2\n")
	writeGitFile(t, repoDir, "b.txt", "b2\n")
	runGit(t, repoDir, "add", "b.txt")
	if err := os.Remove(filepath.Join(repoDir, "c.txt")); err != nil {
		t.Fatal(err)
	}
	writeGitFile(t, repoDir, "d.txt", "d1\n")
	writeGitFile(t, repoDir, "e.log", "ignored\n")
	return repoDir
}
