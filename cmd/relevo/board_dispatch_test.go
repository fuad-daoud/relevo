package main

import (
	"os"
	"testing"
)

// TestBoardIsAPeekVerb pins the route: board and its subverbs neither dial the
// machine database nor capture the agy environment.
func TestBoardIsAPeekVerb(t *testing.T) {
	for _, args := range [][]string{{"board"}, {"board", "url"}, {"board", "comments"}, {"board", "comment"}} {
		if !isPeekArgs(args) {
			t.Errorf("isPeekArgs(%q) = false, want true", args)
		}
		if mode, _, _ := routeForArgs(args); mode != routeNone {
			t.Errorf("routeForArgs(%q) = %v, want none", args, mode)
		}
		if peek := installRouteForArgs(args); !peek {
			t.Errorf("installRouteForArgs(%q) = false, want the peek that skips capture", args)
		}
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

// TestBoardPromoteRefusesAScenePath pins the dispatch: promote takes no scene
// path, so a path is usage before any repository or file work.
func TestBoardPromoteRefusesAScenePath(t *testing.T) {
	t.Chdir(t.TempDir())
	err := cmdBoardPromote([]string{"docs/boards/api/board.html"})
	_ = requireCLIError(t, err, codeUsage, "relevo help")
}

// TestBoardPromoteRefusesOutsideARepository: promoting lands in the repo, so
// outside one there is nowhere to put it.
func TestBoardPromoteRefusesOutsideARepository(t *testing.T) {
	t.Chdir(t.TempDir()) // no git repository here
	_ = requireCLIError(t, cmdBoardPromote([]string{"--mastermind", "alpha"}), codeUsage, "")
}

// TestBoardTextWordIsRefusedAsAScenePath pins that text and annotate are
// not board subverbs: the words fall through to the board flag set, where the
// extra positional (or the unknown --text flag) is usage before any file
// work, and the scene is left untouched.
// TestBoardThemeFlagIsUnknown pins that the board verb takes no palette
// flag: an unknown --theme is usage before any repository or file work.
func TestBoardThemeFlagIsUnknown(t *testing.T) {
	boardJSONRepo(t)
	_ = requireCLIError(t, cmdBoard([]string{"--theme", "x"}), codeUsage, "")
}

func TestBoardTextWordIsRefusedAsAScenePath(t *testing.T) {
	textArgs := []string{"text", "board.excalidraw"}
	annotateArgs := []string{"annotate", "board.excalidraw", "--text", "hi"}
	for _, args := range [][]string{textArgs, annotateArgs} {
		boardJSONRepo(t)
		scene := "board.excalidraw"
		before := []byte(`{"type":"excalidraw","elements":[]}`)
		if err := os.WriteFile(scene, before, 0o600); err != nil {
			t.Fatalf("write scene: %v", err)
		}
		_ = requireCLIError(t, cmdBoard(args), codeUsage, "")
		after, err := os.ReadFile(scene)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(after) != string(before) {
			t.Errorf("cmdBoard(%q) wrote the scene: before %q, after %q", args, before, after)
		}
	}
}
