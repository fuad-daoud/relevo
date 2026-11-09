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

func writeSceneAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write scene: %v", err)
	}
}

// resolvedPathEqual compares a resolver-returned path against the raw temp
// path the test built it from: the resolver canonicalizes symlinked parents,
// so the test evaluates the directory both spellings share and compares from
// there. The test pins the target, not the spelling.
func resolvedPathEqual(t *testing.T, got, want string) bool {
	t.Helper()
	if filepath.Base(got) != filepath.Base(want) {
		return false
	}
	gotDir, err := filepath.EvalSymlinks(filepath.Dir(got))
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", filepath.Dir(got), err)
	}
	wantDir, err := filepath.EvalSymlinks(filepath.Dir(want))
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", filepath.Dir(want), err)
	}
	return gotDir == wantDir
}

// TestBoardCommentExcalidrawPathIsUsage pins that an explicit .excalidraw
// path reaches neither verb: the HTML board's shape rule refuses it as usage
// -- even with --selector, which the scene branch refused with its own
// message -- and nothing is created.
func TestBoardCommentExcalidrawPathIsUsage(t *testing.T) {
	liveRoot := boardStateRoot(t)
	deps := testDeps(t)
	stubBoardClock(t, &deps, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	repoRoot := withTempRepoRoot(t, &deps)

	sceneBody := `{"type":"excalidraw","elements":[]}`
	repoScene := filepath.Join(repoRoot, "docs", "boards", "api.excalidraw")
	if err := os.MkdirAll(filepath.Dir(repoScene), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeSceneAt(t, repoScene, sceneBody)
	liveScene := filepath.Join(liveRoot, "mm_aaaaaaaaaaaa", "board.excalidraw")
	if err := os.MkdirAll(filepath.Dir(liveScene), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeSceneAt(t, liveScene, sceneBody)

	for _, scene := range []string{repoScene, liveScene} {
		if err := Comments(deps, repoRoot, "", "", scene, false); !errors.Is(err, ErrUsage) {
			t.Errorf("comments(%s) = %v, want ErrUsage", scene, err)
		} else if !strings.Contains(err.Error(), "board.html") {
			t.Errorf("comments(%s) refusal %q does not name the board shape", scene, err)
		}
		sel := "#hero"
		if _, err := WriteComment(deps, repoRoot, CommentInput{Arg: scene, Text: "hi", By: "human", Selector: sel}); !errors.Is(err, ErrUsage) {
			t.Errorf("comment(%s with selector) = %v, want ErrUsage", scene, err)
		} else if !strings.Contains(err.Error(), "board.html") {
			t.Errorf("comment(%s with selector) refusal %q does not name the board shape", scene, err)
		}
	}
	if got, err := os.ReadFile(repoScene); err != nil || string(got) != sceneBody {
		t.Errorf("repo scene = %q, %v: the refused write must leave it alone", got, err)
	}
	if _, statErr := os.Stat(AnnotationsPath(repoScene)); !os.IsNotExist(statErr) {
		t.Errorf("the refused write created annotations: stat error = %v", statErr)
	}
}

// TestBoardCommentHTMLWritesAnnotation pins the HTML branch of the write: the
// note lands in the annotations file beside the board under a minted id, the
// write reports that file (not the board's) so the CLI line can name it, and
// board.html is untouched.
func TestBoardCommentHTMLWritesAnnotation(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	deps := testDeps(t)
	x, y := 0.5, 0.25
	board := seedBoardHTML(t, liveDir, []byte("<html><body>board</body></html>"))
	before, err := os.ReadFile(board)
	if err != nil {
		t.Fatalf("read board: %v", err)
	}
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	w, err := WriteComment(deps, t.TempDir(), CommentInput{
		Ref: "", Arg: board, Text: "hi", By: "human", Selector: "#hero", X: &x, Y: &y,
	})
	if err != nil {
		t.Fatalf("board comment on an HTML board: %v", err)
	}
	if w.ID == "" || !resolvedPathEqual(t, w.Path, AnnotationsPath(board)) {
		t.Errorf("write = %+v, want an id at %s", w, AnnotationsPath(board))
	}
	entries, etag, err := ReadAnnotations(board)
	if err != nil {
		t.Fatalf("ReadAnnotations: %v", err)
	}
	if len(entries) != 1 || entries[0].ID == "" || entries[0].ID != w.ID ||
		entries[0].Selector != "#hero" || entries[0].X != 0.5 || entries[0].Y != 0.25 ||
		entries[0].Text != "hi" || entries[0].By != "human" || etag == "" {
		t.Errorf("entries = %+v etag %q, want the note under %q", entries, etag, w.ID)
	}
	// A CLI note names no thread to join, so it starts one: thread is its own id.
	if entries[0].Thread != w.ID {
		t.Errorf("entries[0].Thread = %q, want the note's own id %q", entries[0].Thread, w.ID)
	}
	after, err := os.ReadFile(board)
	if err != nil {
		t.Fatalf("re-read board: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("board.html changed:\nbefore %q\nafter  %q", before, after)
	}
}

// TestBoardCommentHTMLRefusesMissingBoard: a comment on an HTML board that is
// not there is refused and creates neither the board nor its annotations.
func TestBoardCommentHTMLRefusesMissingBoard(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	deps := testDeps(t)
	path := seedBoardHTML(t, liveDir, []byte("<html><body>board</body></html>"))
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove board: %v", err)
	}

	_, err := WriteComment(deps, t.TempDir(), CommentInput{Arg: path, Text: "hi", By: "human"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("comment on a missing board = %v, want ErrNotFound", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the write created board.html: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(AnnotationsPath(path)); !os.IsNotExist(statErr) {
		t.Errorf("the write created annotations: stat error = %v", statErr)
	}
}

// TestBoardCommentHTMLRefusesAPositionWithoutSelector: a position is a fraction
// of an element's box, so --x and --y without --selector are usage rather than
// a silent default.
func TestBoardCommentHTMLRefusesAPositionWithoutSelector(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	deps := testDeps(t)
	board := seedBoardHTML(t, liveDir, []byte("<html><body>board</body></html>"))
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	x := 0.5

	_, err := WriteComment(deps, t.TempDir(), CommentInput{Arg: board, Text: "x", By: "human", X: &x, Y: &x})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("comment(x and y without a selector) = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "--selector") {
		t.Errorf("refusal %q does not name --selector", err)
	}
	_, err = WriteComment(deps, t.TempDir(), CommentInput{Arg: board, Text: "x", By: "human", Y: &x})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("comment(lone y) = %v, want ErrUsage", err)
	}
}

// TestBoardCommentHTMLSelectorDefaults pins the defaults: --selector alone
// places the note at 0,0, and no --selector makes it a board-level note.
func TestBoardCommentHTMLSelectorDefaults(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	deps := testDeps(t)
	board := seedBoardHTML(t, liveDir, []byte("<html><body>board</body></html>"))
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	if _, err := WriteComment(deps, t.TempDir(), CommentInput{Arg: board, Text: "with", By: "human", Selector: "#hero"}); err != nil {
		t.Fatalf("comment with a selector: %v", err)
	}
	if _, err := WriteComment(deps, t.TempDir(), CommentInput{Arg: board, Text: "without", By: "human"}); err != nil {
		t.Fatalf("comment without a selector: %v", err)
	}
	entries, _, err := ReadAnnotations(board)
	if err != nil {
		t.Fatalf("ReadAnnotations: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want two", entries)
	}
	if entries[0].Selector != "#hero" || entries[0].X != 0 || entries[0].Y != 0 {
		t.Errorf("entry with a selector = %+v, want #hero at (0,0)", entries[0])
	}
	if entries[1].Selector != "" || entries[1].X != 0 || entries[1].Y != 0 {
		t.Errorf("entry without a selector = %+v, want a board-level note", entries[1])
	}
}

// TestBoardCommentByDefaultPrefersExplicitOverEnvironment pins the author
// default on the HTML write path: an explicit value wins, else the
// RELEVO_MASTERMIND environment, else human.
func TestBoardCommentByDefaultPrefersExplicitOverEnvironment(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	deps := testDeps(t)
	board := seedBoardHTML(t, liveDir, []byte("<html><body>board</body></html>"))
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	envWrite, err := WriteComment(deps, t.TempDir(), CommentInput{Arg: board, Text: "env"})
	if err != nil {
		t.Fatalf("comment without --by: %v", err)
	}
	explicitWrite, err := WriteComment(deps, t.TempDir(), CommentInput{Arg: board, Text: "explicit", By: "ada"})
	if err != nil {
		t.Fatalf("comment with --by: %v", err)
	}
	t.Setenv("RELEVO_MASTERMIND", "")
	humanWrite, err := WriteComment(deps, t.TempDir(), CommentInput{Arg: board, Text: "anon"})
	if err != nil {
		t.Fatalf("comment without --by or environment: %v", err)
	}
	entries, _, err := ReadAnnotations(board)
	if err != nil {
		t.Fatalf("ReadAnnotations: %v", err)
	}
	byOf := map[string]string{}
	for _, e := range entries {
		byOf[e.ID] = e.By
	}
	if byOf[envWrite.ID] != "mm_aaaaaaaaaaaa" {
		t.Errorf("default author = %q, want the environment MasterMind", byOf[envWrite.ID])
	}
	if byOf[explicitWrite.ID] != "ada" {
		t.Errorf("explicit author = %q, want ada", byOf[explicitWrite.ID])
	}
	if byOf[humanWrite.ID] != "human" {
		t.Errorf("fallback author = %q, want human", byOf[humanWrite.ID])
	}
}

// TestBoardCommentsHTMLRows pins the HTML branch of the reader: tab-separated
// rows carrying all seven fields, and --json the compact store array.
func TestBoardCommentsHTMLRows(t *testing.T) {
	_, liveDir := boardHTMLEnv(t)
	deps := testDeps(t)
	board := seedBoardHTML(t, liveDir, []byte("<html><body>board</body></html>"))
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	stdout, _, err := captureOutput(t, func() error { return Comments(deps, t.TempDir(), "", "", board, false) })
	if err != nil {
		t.Fatalf("board comments on an HTML board: %v", err)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	want := `[{"id":"a1","selector":"#hero","x":0.5,"y":0.25,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z"}]`
	if err := os.WriteFile(AnnotationsPath(board), []byte(want), 0o600); err != nil {
		t.Fatalf("write annotations: %v", err)
	}
	stdout, _, err = captureOutput(t, func() error { return Comments(deps, t.TempDir(), "", "", board, false) })
	if err != nil {
		t.Fatalf("board comments: %v", err)
	}
	if got := string(stdout); got != "a1\t#hero\t0.5\t0.25\thi\thuman\t2026-10-01T12:00:00Z\n" {
		t.Errorf("stdout = %q, want the tab row", got)
	}
	stdout, _, err = captureOutput(t, func() error { return Comments(deps, t.TempDir(), "", "", board, true) })
	if err != nil {
		t.Fatalf("board comments --json: %v", err)
	}
	// The stored entry names no thread, so --json carries the normalized one:
	// each legacy note reads as a thread of its own.
	if got := string(stdout); got != `[{"id":"a1","selector":"#hero","x":0.5,"y":0.25,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z","thread":"a1"}]`+"\n" {
		t.Errorf("stdout = %q, want the store JSON with the normalized thread", got)
	}
}
