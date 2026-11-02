package main

import "testing"

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
