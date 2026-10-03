package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board"
)

// promoteEnv isolates the state root, seeds one MasterMind and gives a repo
// root, then writes a live board under that MasterMind and returns both roots
// plus the live board's path.
func promoteEnv(t *testing.T, name string) (repo, liveBoard string) {
	t.Helper()
	repo = withTempRepoRoot(t)
	liveRoot, liveDir := boardHTMLEnv(t)
	if err := board.WritePointer(liveDir, name); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	liveBoard = seedBoardHTML(t, liveDir, []byte("<h1>"+name+"</h1>"))
	_ = liveRoot
	return repo, liveBoard
}

// TestBoardPromoteCopiesTheLiveBoard: the happy path writes
// <repo>/docs/boards/<slug>/board.html with the live board's bytes.
func TestBoardPromoteCopiesTheLiveBoard(t *testing.T) {
	repo, _ := promoteEnv(t, "api")

	out := captureStdout(t, func() {
		if err := cmdBoardPromote([]string{"--mastermind", "alpha"}); err != nil {
			t.Fatalf("cmdBoardPromote: %v", err)
		}
	})
	dst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	if !strings.Contains(out, "board: promoted") || !strings.Contains(out, dst) {
		t.Errorf("printed %q, want the promoted line naming %s", out, dst)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read promoted board: %v", err)
	}
	if string(got) != "<h1>api</h1>" {
		t.Errorf("promoted %q, want the live board's bytes", got)
	}
}

// TestBoardPromoteHonoursBoardFlagAndTo: --board picks which live board, --to
// picks the repo name, and neither writes the pointer.
func TestBoardPromoteHonoursBoardFlagAndTo(t *testing.T) {
	repo, _ := promoteEnv(t, "api")
	_, liveDir := boardHTMLEnv(t)
	if err := os.MkdirAll(filepath.Join(liveDir, "other"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "other", "board.html"), []byte("<h1>other</h1>"), 0o644); err != nil {
		t.Fatalf("write other board: %v", err)
	}
	if err := board.WritePointer(liveDir, "api"); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}

	captureStdout(t, func() {
		if err := cmdBoardPromote([]string{"--mastermind", "alpha", "--board", "other", "--to", "renamed"}); err != nil {
			t.Fatalf("cmdBoardPromote: %v", err)
		}
	})
	got, err := os.ReadFile(filepath.Join(repo, "docs", "boards", "renamed", "board.html"))
	if err != nil {
		t.Fatalf("read promoted board: %v", err)
	}
	if string(got) != "<h1>other</h1>" {
		t.Errorf("promoted %q, want the --board board's bytes", got)
	}
	// Promoting is not opening: the pointer still names the board the user is
	// looking at.
	name, present, err := board.Pointer(liveDir)
	if err != nil || !present || name != "api" {
		t.Errorf("pointer = %q/%v/%v, want api/true/nil -- promote must not write it", name, present, err)
	}
}

// TestBoardPromoteRefusesAMissingSource: nothing to copy is refused, and it is
// the refused code rather than usage, so a caller can tell it from a bad flag.
func TestBoardPromoteRefusesAMissingSource(t *testing.T) {
	withTempRepoRoot(t)
	boardHTMLEnv(t)

	err := cmdBoardPromote([]string{"--mastermind", "alpha", "--board", "absent"})
	requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(err.Error(), "board.html") {
		t.Errorf("refusal %q does not name the missing board", err)
	}
}

// TestBoardPromoteRefusesAnExistingTarget: an existing repo board is refused
// naming both the target and --force, and the old bytes survive.
func TestBoardPromoteRefusesAnExistingTarget(t *testing.T) {
	repo, _ := promoteEnv(t, "api")
	dst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dst, []byte("committed"), 0o644); err != nil {
		t.Fatalf("write committed board: %v", err)
	}

	err := cmdBoardPromote([]string{"--mastermind", "alpha"})
	requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(err.Error(), dst) || !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal %q names neither the target nor --force", err)
	}
	got, readErr := os.ReadFile(dst)
	if readErr != nil || string(got) != "committed" {
		t.Errorf("the refused promote left %q/%v, want the committed bytes", got, readErr)
	}
}

func TestBoardPromoteForceOverwrites(t *testing.T) {
	repo, _ := promoteEnv(t, "api")
	dst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dst, []byte("committed"), 0o644); err != nil {
		t.Fatalf("write committed board: %v", err)
	}

	captureStdout(t, func() {
		if err := cmdBoardPromote([]string{"--mastermind", "alpha", "--force"}); err != nil {
			t.Fatalf("cmdBoardPromote --force: %v", err)
		}
	})
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "<h1>api</h1>" {
		t.Errorf("forced promote left %q/%v, want the live bytes", got, err)
	}
}

// TestBoardPromoteRefusesABadSlug: --to is a scene slug, and a value that is
// not one is usage before any file work.
func TestBoardPromoteRefusesABadSlug(t *testing.T) {
	repo, _ := promoteEnv(t, "api")

	requireCLIError(t, cmdBoardPromote([]string{"--mastermind", "alpha", "--to", "Not A Slug"}), codeUsage, "relevo help")
	if _, statErr := os.Stat(filepath.Join(repo, "docs")); !os.IsNotExist(statErr) {
		t.Errorf("the refused promote created %s: stat error = %v, want not-exist", repo, statErr)
	}
}

// TestBoardPromoteRefusesOutsideARepository: promoting lands in the repo, so
// outside one there is nowhere to put it.
func TestBoardPromoteRefusesOutsideARepository(t *testing.T) {
	stubRepoSeamAbsent(t)
	boardHTMLEnv(t)

	requireCLIError(t, cmdBoardPromote([]string{"--mastermind", "alpha"}), codeUsage, "")
}

func TestBoardPromoteRefusesAScenePath(t *testing.T) {
	repo, _ := promoteEnv(t, "api")

	err := cmdBoardPromote([]string{"docs/boards/api/board.html"})
	requireCLIError(t, err, codeUsage, "relevo help")
	if _, statErr := os.Stat(filepath.Join(repo, "docs")); !os.IsNotExist(statErr) {
		t.Errorf("the refused promote created %s: stat error = %v, want not-exist", repo, statErr)
	}
}

// TestBoardPromoteIsAPeekVerb: promote is a copy, not a read of the machine, so
// it installs no route and skips the agy environment capture like every other
// board subverb.
func TestBoardPromoteIsAPeekVerb(t *testing.T) {
	args := []string{"board", "promote"}
	if !isPeekArgs(args) {
		t.Error("isPeekArgs(board promote) = false, want true")
	}
	if mode, _, _ := routeForArgs(args); mode != routeNone {
		t.Errorf("routeForArgs(board promote) = %v, want none", mode)
	}
	if peek := installRouteForArgs(args); !peek {
		t.Error("installRouteForArgs(board promote) = false, want the peek that skips capture")
	}
}

// captureStdout runs fn with stdout redirected and returns what it printed. It
// reads the pipe only after the writer is closed, so a small print cannot fill
// the pipe buffer and deadlock the verb under test.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = orig
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out)
}
