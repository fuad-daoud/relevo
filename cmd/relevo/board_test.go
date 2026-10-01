package main

import (
	"errors"
	"strings"
	"testing"
)

// TestBoardIsAPeekVerb pins the route: board neither dials the machine
// database nor captures the agy environment.
func TestBoardIsAPeekVerb(t *testing.T) {
	if !isPeekArgs([]string{"board"}) {
		t.Error("isPeekArgs(board) = false, want true")
	}
	if mode, _, _ := routeForArgs([]string{"board"}); mode != routeNone {
		t.Errorf("routeForArgs(board) = %v, want none", mode)
	}
	if peek := installRouteForArgs([]string{"board"}); !peek {
		t.Error("installRouteForArgs(board) = false, want the peek that skips capture")
	}
}

// TestBoardRefusesOutsideRepo: without a repository the verb is usage.
func TestBoardRefusesOutsideRepo(t *testing.T) {
	orig := boardRepoRootFn
	boardRepoRootFn = func(string) (string, error) { return "", errors.New("not a git repository") }
	t.Cleanup(func() { boardRepoRootFn = orig })

	err := cmdBoard(nil)
	requireCLIError(t, err, codeUsage, "relevo help")
}

// TestBoardRefusesBadTheme: an unknown theme is usage and names the built-ins.
func TestBoardRefusesBadTheme(t *testing.T) {
	withTempRepoRoot(t)
	ce := requireCLIError(t, cmdBoard([]string{"--theme", "nope"}), codeUsage, "relevo help")
	if !strings.Contains(ce.message, "cockpit") || !strings.Contains(ce.message, "blueprint") {
		t.Errorf("refusal %q does not list the built-ins", ce.message)
	}
}

// TestBoardRefusesBadPath: a path that is not a scene is usage.
func TestBoardRefusesBadPath(t *testing.T) {
	withTempRepoRoot(t)
	requireCLIError(t, cmdBoard([]string{"notes.txt"}), codeUsage, "")
}

// TestBoardThemePrecedence: --theme wins, then relevo.boardTheme, then cockpit.
func TestBoardThemePrecedence(t *testing.T) {
	orig := boardGitConfig
	t.Cleanup(func() { boardGitConfig = orig })

	boardGitConfig = func(string, string) (string, error) { return "blueprint", nil }
	if th, err := boardResolveTheme("", ""); err != nil || th.Name != "blueprint" {
		t.Errorf("relevo.boardTheme = %v, %v; want blueprint", th, err)
	}
	if th, err := boardResolveTheme("", "cockpit"); err != nil || th.Name != "cockpit" {
		t.Errorf("--theme cockpit = %v, %v; want cockpit", th, err)
	}

	boardGitConfig = func(string, string) (string, error) { return "", nil }
	if th, err := boardResolveTheme("", ""); err != nil || th.Name != "cockpit" {
		t.Errorf("default = %v, %v; want cockpit", th, err)
	}
}

// withTempRepoRoot points the repo-root seam at a temp dir for one test.
func withTempRepoRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := boardRepoRootFn
	boardRepoRootFn = func(string) (string, error) { return dir, nil }
	t.Cleanup(func() { boardRepoRootFn = orig })
	return dir
}
