package board

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubBoardClock freezes the comment clock.
func stubBoardClock(t *testing.T, deps *Deps, at time.Time) {
	t.Helper()
	deps.Now = func() time.Time { return at }
}

// liveBoardFor seeds a live directory for id and returns (liveDir, scenePath).
func liveBoardFor(t *testing.T, liveRoot, id string) (string, string) {
	t.Helper()
	liveDir := filepath.Join(liveRoot, id)
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return liveDir, filepath.Join(liveDir, "board.excalidraw")
}

func writeSceneAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write scene: %v", err)
	}
}

// TestBoardCommentsJSONShape pins the machine shape: one compact JSON array
// whose objects are exactly id, x, y, text, by, at in order, and [] -- never
// null -- when there are none.
func TestBoardCommentsJSONShape(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	id := "mm_aaaaaaaaaaaa"
	_, scene := liveBoardFor(t, liveRoot, id)
	writeSceneAt(t, scene, `{"type":"excalidraw","elements":[`+
		`{"id":"c1","type":"text","x":1,"y":2,"text":"hi","customData":{"relevo":{"comment":true,"by":"human","at":"2026-10-01T12:00:00Z"}}},`+
		`{"id":"c2","type":"text","x":3,"y":4,"text":"yo","customData":{"relevo":{"comment":true,"by":"mm_x","at":"2026-10-02T12:00:00Z"}}}`+
		`]}`)

	stdout, stderr, err := captureOutput(t, func() error { return Comments(deps, t.TempDir(), id, "", "", true) })
	if err != nil {
		t.Fatalf("board comments --json: %v (stderr %s)", err, stderr)
	}
	want := `[{"id":"c1","x":1,"y":2,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z"},` +
		`{"id":"c2","x":3,"y":4,"text":"yo","by":"mm_x","at":"2026-10-02T12:00:00Z"}]` + "\n"
	if string(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	// Zero comments is [].
	writeSceneAt(t, scene, `{"type":"excalidraw","elements":[]}`)
	stdout, _, err = captureOutput(t, func() error { return Comments(deps, t.TempDir(), id, "", "", true) })
	if err != nil {
		t.Fatalf("board comments --json (none): %v", err)
	}
	if string(stdout) != "[]\n" {
		t.Errorf("stdout = %q, want []\\n", stdout)
	}
}

// TestBoardCommentsTextRows pins the text mode: one tab-separated row per
// comment, nothing else on stdout.
func TestBoardCommentsTextRows(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	id := "mm_aaaaaaaaaaaa"
	_, scene := liveBoardFor(t, liveRoot, id)
	writeSceneAt(t, scene, `{"type":"excalidraw","elements":[`+
		`{"id":"c1","type":"text","x":1,"y":2,"text":"hi","customData":{"relevo":{"comment":true,"by":"human","at":"2026-10-01T12:00:00Z"}}}`+
		`]}`)

	stdout, _, err := captureOutput(t, func() error { return Comments(deps, t.TempDir(), id, "", "", false) })
	if err != nil {
		t.Fatalf("board comments: %v", err)
	}
	want := "c1\t1\t2\thi\thuman\t2026-10-01T12:00:00Z\n"
	if string(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestBoardCommentsNeverWritesPointer pins that reading comments is read-only:
// the live pointer is not created.
func TestBoardCommentsNeverWritesPointer(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	id := "mm_aaaaaaaaaaaa"

	stdout, _, err := captureOutput(t, func() error { return Comments(deps, t.TempDir(), id, "", "", false) })
	if err != nil {
		t.Fatalf("board comments: %v", err)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if _, statErr := os.Stat(filepath.Join(liveRoot, id, "current")); !os.IsNotExist(statErr) {
		t.Errorf("board comments wrote the pointer: stat error = %v, want not-exist", statErr)
	}
}

// TestBoardCommentWritesPointer pins that writing a comment selects like
// board: a live scene that did not come from the pointer writes it.
func TestBoardCommentWritesPointer(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	stubBoardClock(t, &deps, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	id := "mm_aaaaaaaaaaaa"
	t.Setenv("RELEVO_MASTERMIND", id)

	stdout, _, err := captureOutput(t, func() error {
		return WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Text: "hello"})
	})
	if err != nil {
		t.Fatalf("board comment: %v", err)
	}
	if !strings.HasPrefix(string(stdout), "comment: ") {
		t.Errorf("stdout = %q, want a comment: line", stdout)
	}
	got, err := os.ReadFile(filepath.Join(liveRoot, id, "current"))
	if err != nil {
		t.Fatalf("read pointer: %v", err)
	}
	if string(got) != "board\n" {
		t.Errorf("pointer = %q, want %q", got, "board\n")
	}
}

// TestBoardCommentByDefaultsToMasterMind pins the author default: with
// RELEVO_MASTERMIND set and no --by, the marker carries the MasterMind id.
func TestBoardCommentByDefaultsToMasterMind(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	stubBoardClock(t, &deps, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	id := "mm_aaaaaaaaaaaa"
	_, scene := liveBoardFor(t, liveRoot, id)
	t.Setenv("RELEVO_MASTERMIND", id)

	if _, _, err := captureOutput(t, func() error {
		return WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Text: "hello"})
	}); err != nil {
		t.Fatalf("board comment: %v", err)
	}
	comments, err := ReadComments(scene)
	if err != nil {
		t.Fatalf("ReadComments: %v", err)
	}
	if len(comments) != 1 || comments[0].By != id {
		t.Errorf("comments = %+v, want one with by %s", comments, id)
	}
}

// TestBoardCommentPrintsLineAndAppends pins the success line and the append:
// comment: <id>  <path>, and the scene reads the comment back.
func TestBoardCommentPrintsLineAndAppends(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	stubBoardClock(t, &deps, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	id := "mm_aaaaaaaaaaaa"
	_, scene := liveBoardFor(t, liveRoot, id)
	writeSceneAt(t, scene, `{"type":"excalidraw","elements":[]}`)
	t.Setenv("RELEVO_MASTERMIND", id)

	x, y := 5.0, 6.0
	stdout, _, err := captureOutput(t, func() error {
		return WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Text: "a note", By: "human", X: &x, Y: &y})
	})
	if err != nil {
		t.Fatalf("board comment: %v", err)
	}
	line := strings.TrimSuffix(string(stdout), "\n")
	rest, ok := strings.CutPrefix(line, "comment: ")
	if !ok || !strings.HasSuffix(rest, "  "+scene) {
		t.Fatalf("stdout = %q, want \"comment: <id>  %s\"", stdout, scene)
	}
	gotID := strings.TrimSuffix(rest, "  "+scene)
	comments, err := ReadComments(scene)
	if err != nil {
		t.Fatalf("ReadComments: %v", err)
	}
	if gotID == "" || len(comments) != 1 || comments[0].ID != gotID ||
		comments[0].Text != "a note" || comments[0].X != 5 || comments[0].Y != 6 {
		t.Errorf("comments = %+v (printed id %q), want the appended one at (5, 6)", comments, gotID)
	}
}

// TestBoardCommentRefusalCodes pins the refusal map: a malformed marker and a
// non-scene are refused, and a bad author is usage.
func TestBoardCommentRefusalCodes(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	id := "mm_aaaaaaaaaaaa"
	_, scene := liveBoardFor(t, liveRoot, id)

	t.Run("malformed marker", func(t *testing.T) {
		writeSceneAt(t, scene, `{"type":"excalidraw","elements":[`+
			`{"id":"bad1","type":"rectangle","text":"x","customData":{"relevo":{"comment":true,"by":"a","at":"2026-01-01T00:00:00Z"}}}]}`)
		err := Comments(deps, t.TempDir(), id, "", scene, false)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("comments(malformed marker) = %v, want ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "bad1") {
			t.Errorf("refusal %q does not name the element id", err)
		}
	})

	t.Run("non-scene", func(t *testing.T) {
		writeSceneAt(t, scene, `not json`)
		err := Comments(deps, t.TempDir(), id, "", scene, false)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("comments(non-scene) = %v, want ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), scene) {
			t.Errorf("refusal %q does not name the path", err)
		}
	})

	t.Run("bad author flag", func(t *testing.T) {
		err := WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Arg: scene, Text: "x", By: strings.Repeat("a", 65)})
		if !errors.Is(err, ErrUsage) {
			t.Fatalf("comment(bad author) = %v, want ErrUsage", err)
		}
		if !strings.Contains(err.Error(), "author") {
			t.Errorf("refusal %q does not name the author", err)
		}
	})

	t.Run("bad author from environment", func(t *testing.T) {
		t.Setenv("RELEVO_MASTERMIND", "bad\tby")
		err := WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Arg: scene, Text: "x"})
		if !errors.Is(err, ErrUsage) {
			t.Errorf("comment(bad author env) = %v, want ErrUsage", err)
		}
	})
}

// TestBoardCommentInputRefusals pins the flag rules: --text is required, and
// --x and --y go together.
func TestBoardCommentInputRefusals(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	id := "mm_aaaaaaaaaaaa"
	_, scene := liveBoardFor(t, liveRoot, id)
	x := 1.0

	if err := WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Arg: scene}); !errors.Is(err, ErrUsage) {
		t.Errorf("comment(no text) = %v, want ErrUsage", err)
	}
	if err := WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Arg: scene, Text: ""}); !errors.Is(err, ErrUsage) {
		t.Errorf("comment(empty text) = %v, want ErrUsage", err)
	}
	if err := WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Arg: scene, Text: "x", X: &x}); !errors.Is(err, ErrUsage) {
		t.Errorf("comment(lone x) = %v, want ErrUsage", err)
	}
	if err := WriteComment(deps, t.TempDir(), CommentInput{Ref: id, Arg: scene, Text: "x", Y: &x}); !errors.Is(err, ErrUsage) {
		t.Errorf("comment(lone y) = %v, want ErrUsage", err)
	}
}

// TestBoardCommentRepoScopeNeverWritesPointer: a repo scene is never a live
// pointer write.
func TestBoardCommentRepoScopeNeverWritesPointer(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	stubBoardClock(t, &deps, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	repoRoot := withTempRepoRoot(t, &deps)

	scene := filepath.Join(repoRoot, "docs", "boards", "api.excalidraw")
	if err := os.MkdirAll(filepath.Dir(scene), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, _, err := captureOutput(t, func() error {
		return WriteComment(deps, t.TempDir(), CommentInput{Arg: scene, Text: "hi", By: "human"})
	}); err != nil {
		t.Fatalf("board comment repo scene: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, "docs", "boards", "current")); !os.IsNotExist(statErr) {
		t.Errorf("a repo comment created a pointer: stat error = %v, want not-exist", statErr)
	}
	// And the scene exists with the comment in it.
	comments, err := ReadComments(scene)
	if err != nil {
		t.Fatalf("ReadComments: %v", err)
	}
	if len(comments) != 1 {
		t.Errorf("comments = %+v, want one", comments)
	}
}
