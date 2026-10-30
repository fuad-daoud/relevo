package board

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// promoteEnv isolates the state root, seeds one MasterMind and gives a repo
// root, then writes a live board under that MasterMind and returns both roots
// plus the live board's path.
func promoteEnv(t *testing.T, deps *Deps, name string) (repo, liveBoard string) {
	t.Helper()
	repo = withTempRepoRoot(t, deps)
	liveRoot, liveDir := boardHTMLEnv(t)
	if err := WritePointer(liveDir, name); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	liveBoard = seedBoardHTML(t, liveDir, []byte("<h1>"+name+"</h1>"))
	_ = liveRoot
	return repo, liveBoard
}

// TestBoardPromoteCopiesTheLiveBoard: the happy path writes
// <repo>/docs/boards/<slug>/board.html with the live board's bytes.
func TestBoardPromoteCopiesTheLiveBoard(t *testing.T) {
	deps := testDeps(t)
	repo, _ := promoteEnv(t, &deps, "api")

	stdout, _, err := captureOutput(t, func() error {
		return PromoteToRepo(deps, t.TempDir(), "alpha", "", "", false)
	})
	if err != nil {
		t.Fatalf("PromoteToRepo: %v", err)
	}
	dst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	if out := string(stdout); !strings.Contains(out, "board: promoted") || !strings.Contains(out, dst) {
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
	deps := testDeps(t)
	repo, _ := promoteEnv(t, &deps, "api")
	_, liveDir := boardHTMLEnv(t)
	if err := os.MkdirAll(filepath.Join(liveDir, "other"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "other", "board.html"), []byte("<h1>other</h1>"), 0o644); err != nil {
		t.Fatalf("write other board: %v", err)
	}
	if err := WritePointer(liveDir, "api"); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}

	if _, _, err := captureOutput(t, func() error {
		return PromoteToRepo(deps, t.TempDir(), "alpha", "other", "renamed", false)
	}); err != nil {
		t.Fatalf("PromoteToRepo: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repo, "docs", "boards", "renamed", "board.html"))
	if err != nil {
		t.Fatalf("read promoted board: %v", err)
	}
	if string(got) != "<h1>other</h1>" {
		t.Errorf("promoted %q, want the --board board's bytes", got)
	}
	// Promoting is not opening: the pointer still names the board the user is
	// looking at.
	name, present, err := Pointer(liveDir)
	if err != nil || !present || name != "api" {
		t.Errorf("pointer = %q/%v/%v, want api/true/nil -- promote must not write it", name, present, err)
	}
}

// TestBoardPromoteRefusesAMissingSource: nothing to copy is refused, and it is
// the refused code rather than usage, so a caller can tell it from a bad flag.
func TestBoardPromoteRefusesAMissingSource(t *testing.T) {
	deps := testDeps(t)
	_ = withTempRepoRoot(t, &deps)
	boardHTMLEnv(t)

	err := PromoteToRepo(deps, t.TempDir(), "alpha", "absent", "", false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("PromoteToRepo(missing source) = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "board.html") {
		t.Errorf("refusal %q does not name the missing board", err)
	}
}

// TestBoardPromoteRefusesAnExistingTarget: an existing repo board is refused
// naming both the target and --force, and the old bytes survive.
func TestBoardPromoteRefusesAnExistingTarget(t *testing.T) {
	deps := testDeps(t)
	repo, _ := promoteEnv(t, &deps, "api")
	dst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dst, []byte("committed"), 0o644); err != nil {
		t.Fatalf("write committed board: %v", err)
	}

	err := PromoteToRepo(deps, t.TempDir(), "alpha", "", "", false)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("PromoteToRepo(existing target) = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), dst) || !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal %q names neither the target nor --force", err)
	}
	got, readErr := os.ReadFile(dst)
	if readErr != nil || string(got) != "committed" {
		t.Errorf("the refused promote left %q/%v, want the committed bytes", got, readErr)
	}
}

func TestBoardPromoteForceOverwrites(t *testing.T) {
	deps := testDeps(t)
	repo, _ := promoteEnv(t, &deps, "api")
	dst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dst, []byte("committed"), 0o644); err != nil {
		t.Fatalf("write committed board: %v", err)
	}

	if _, _, err := captureOutput(t, func() error {
		return PromoteToRepo(deps, t.TempDir(), "alpha", "", "", true)
	}); err != nil {
		t.Fatalf("PromoteToRepo --force: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "<h1>api</h1>" {
		t.Errorf("forced promote left %q/%v, want the live bytes", got, err)
	}
}

// TestBoardPromoteRefusesABadSlug: --to is a scene slug, and a value that is
// not one is usage before any file work.
func TestBoardPromoteRefusesABadSlug(t *testing.T) {
	deps := testDeps(t)
	repo, _ := promoteEnv(t, &deps, "api")

	err := PromoteToRepo(deps, t.TempDir(), "alpha", "", "Not A Slug", false)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("PromoteToRepo(--to bad slug) = %v, want ErrUsage", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "docs")); !os.IsNotExist(statErr) {
		t.Errorf("the refused promote created %s: stat error = %v, want not-exist", repo, statErr)
	}
}

// TestBoardPromoteRefusesOutsideARepository: promoting lands in the repo, so
// outside one there is nowhere to put it.
func TestBoardPromoteRefusesOutsideARepository(t *testing.T) {
	deps := testDeps(t)
	boardHTMLEnv(t)

	if err := PromoteToRepo(deps, t.TempDir(), "alpha", "", "", false); !errors.Is(err, ErrUsage) {
		t.Errorf("PromoteToRepo(outside repo) = %v, want ErrUsage", err)
	}
}
