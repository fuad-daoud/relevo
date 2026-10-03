package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// Aliased because most of these tests hold a local named board, which would
	// otherwise shadow the package.
	boardpkg "github.com/fuad-daoud/relevo/internal/board"
)

// bpath is the annotations file beside an HTML board. The local name keeps the
// assertions below readable next to the board package's own vocabulary.
func bpath(board string) string { return boardpkg.AnnotationsPath(board) }

// bread reads an HTML board's annotations.
func bread(board string) ([]boardpkg.Annotation, string, error) {
	return boardpkg.ReadAnnotations(board)
}

// htmlBoardCmdFor seeds a live HTML board for the CLI tests and returns
// (liveRoot, boardHTML path). It uses only the in-process seams -- no harness, no
// browser, no daemon -- so CI can run it.
func htmlBoardCmdFor(t *testing.T, name string) (string, string) {
	t.Helper()
	liveRoot := boardStateRoot(t)
	stubRepoSeamAbsent(t)
	_, board := liveHTMLBoardFor(t, liveRoot, "mm_aaaaaaaaaaaa", name)
	return liveRoot, board
}

// repoHTMLBoardFor seeds a repo-scope HTML board and returns the repo root and
// the board's path.
func repoHTMLBoardFor(t *testing.T) (string, string) {
	t.Helper()
	boardStateRoot(t)
	repoRoot := withTempRepoRoot(t)
	slug := filepath.Join(repoRoot, "docs", "boards", "api")
	if err := os.MkdirAll(slug, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	board := filepath.Join(slug, "board.html")
	if err := os.WriteFile(board, []byte("<html><body>board</body></html>"), 0o600); err != nil {
		t.Fatalf("write board.html: %v", err)
	}
	return repoRoot, board
}

// TestBoardCommentsHTMLJSONShape pins the machine shape on an HTML board: one
// compact JSON array whose objects are exactly id, selector, x, y, text, by, at
// in order, and [] -- never null -- when there are none.
func TestBoardCommentsHTMLJSONShape(t *testing.T) {
	_, board := htmlBoardCmdFor(t, "api")
	ann := bpath(board)
	if err := os.WriteFile(ann, []byte(
		`[{"id":"a1","selector":"#hero","x":0.5,"y":0.25,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z"},`+
			`{"id":"a2","selector":"","x":0,"y":0,"text":"board level","by":"human","at":"2026-10-02T12:00:00Z"}]`),
		0o600); err != nil {
		t.Fatalf("write annotations: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return cmdBoardComments([]string{board, "--json"})
	})
	if err != nil {
		t.Fatalf("board comments --json: %v (stderr %s)", err, stderr)
	}
	want := `[{"id":"a1","selector":"#hero","x":0.5,"y":0.25,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z"},` +
		`{"id":"a2","selector":"","x":0,"y":0,"text":"board level","by":"human","at":"2026-10-02T12:00:00Z"}]` + "\n"
	if string(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	// No annotations at all is [].
	if err := os.WriteFile(ann, []byte("[]\n"), 0o600); err != nil {
		t.Fatalf("write annotations: %v", err)
	}
	stdout, _, err = captureOutput(t, func() error { return cmdBoardComments([]string{board, "--json"}) })
	if err != nil {
		t.Fatalf("board comments --json (none): %v", err)
	}
	if string(stdout) != "[]\n" {
		t.Errorf("stdout = %q, want []\\n", stdout)
	}
}

// TestBoardCommentsHTMLTextRows pins the text mode: one tab-separated row per
// entry carrying all seven fields, and nothing else on stdout.
func TestBoardCommentsHTMLTextRows(t *testing.T) {
	_, board := htmlBoardCmdFor(t, "api")
	if err := os.WriteFile(bpath(board), []byte(
		`[{"id":"a1","selector":"#hero","x":0.5,"y":0.25,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z"}]`),
		0o600); err != nil {
		t.Fatalf("write annotations: %v", err)
	}
	stdout, _, err := captureOutput(t, func() error { return cmdBoardComments([]string{board}) })
	if err != nil {
		t.Fatalf("board comments: %v", err)
	}
	want := "a1\t#hero\t0.5\t0.25\thi\thuman\t2026-10-01T12:00:00Z\n"
	if string(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestBoardCommentsHTMLMissingBoardIsEmpty pins that listing is read-only and
// total: a board that is not there is an empty list and exit 0, and nothing is
// created. This is the same contract the Excalidraw reader has always had.
func TestBoardCommentsHTMLMissingBoardIsEmpty(t *testing.T) {
	_, board := htmlBoardCmdFor(t, "api")
	if err := os.Remove(board); err != nil {
		t.Fatalf("remove board: %v", err)
	}
	stdout, stderr, err := captureOutput(t, func() error {
		return cmdBoardComments([]string{board, "--json"})
	})
	if err != nil {
		t.Fatalf("board comments over a missing board: %v", err)
	}
	if string(stdout) != "[]\n" {
		t.Errorf("stdout = %q, want []\\n", stdout)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if _, statErr := os.Stat(bpath(board)); !os.IsNotExist(statErr) {
		t.Errorf("listing created the annotations file: stat error = %v", statErr)
	}
}

// TestBoardCommentsHTMLNeverWritesPointer pins that the reader never writes the
// pointer, whatever it resolves to: listing is not opening the board.
func TestBoardCommentsHTMLNeverWritesPointer(t *testing.T) {
	liveRoot, _ := htmlBoardCmdFor(t, "board")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComments([]string{"--json"})
	}); err != nil {
		t.Fatalf("board comments: %v", err)
	}
	// stdout is [] because the board has no annotations yet; the assertion that
	// matters is the pointer below.
	if _, statErr := os.Stat(filepath.Join(liveRoot, "mm_aaaaaaaaaaaa", "current")); !os.IsNotExist(statErr) {
		t.Errorf("board comments wrote the pointer: stat error = %v, want not-exist", statErr)
	}
}

// TestBoardCommentHTMLAppendsAndPrintsLine pins the success line and the append
// on an HTML board: comment: <id>  <annotations path> -- the annotations path,
// not the board's, because that is the file the note landed in.
func TestBoardCommentHTMLAppendsAndPrintsLine(t *testing.T) {
	_, board := htmlBoardCmdFor(t, "api")
	stubBoardClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))

	stdout, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{board, "--text", "a note", "--by", "human",
			"--selector", "#hero", "--x", "0.5", "--y", "0.25"})
	})
	if err != nil {
		t.Fatalf("board comment: %v", err)
	}
	line := strings.TrimSuffix(string(stdout), "\n")
	rest, ok := strings.CutPrefix(line, "comment: ")
	if !ok || !strings.HasSuffix(rest, "  "+bpath(board)) {
		t.Fatalf("stdout = %q, want \"comment: <id>  %s\"", stdout, bpath(board))
	}
	gotID := strings.TrimSuffix(rest, "  "+bpath(board))
	entries, _, err := bread(board)
	if err != nil {
		t.Fatalf("ReadAnnotations: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != gotID ||
		entries[0].Text != "a note" || entries[0].Selector != "#hero" ||
		entries[0].X != 0.5 || entries[0].Y != 0.25 || entries[0].By != "human" {
		t.Errorf("entries = %+v (printed id %q), want the appended note", entries, gotID)
	}
}

// TestBoardCommentHTMLRefusesMissingBoard pins D6: a comment on an HTML board
// that is not there is refused with the refused code and exit 2, and creates
// nothing. A bare call used to create the scene it was commenting on.
func TestBoardCommentHTMLRefusesMissingBoard(t *testing.T) {
	_, board := htmlBoardCmdFor(t, "api")
	if err := os.Remove(board); err != nil {
		t.Fatalf("remove board: %v", err)
	}
	ce := requireCLIError(t,
		cmdBoardComment([]string{board, "--text", "hi", "--by", "human"}), codeRefused, "")
	if !strings.Contains(ce.message, "does not exist") {
		t.Errorf("refusal %q does not say the board is missing", ce.message)
	}
	if _, statErr := os.Stat(board); !os.IsNotExist(statErr) {
		t.Errorf("a refused comment created board.html: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(bpath(board)); !os.IsNotExist(statErr) {
		t.Errorf("a refused comment created annotations: stat error = %v", statErr)
	}
}

// TestBoardCommentHTMLSelectorFallbackDefaults pins the defaults: --selector
// alone places the note at 0,0 and no --selector at all makes it a board-level
// note. Both are usable defaults rather than errors, because both are things a
// person wants to write.
func TestBoardCommentHTMLSelectorFallbackDefaults(t *testing.T) {
	_, board := htmlBoardCmdFor(t, "api")
	stubBoardClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))

	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{board, "--text", "with selector", "--by", "human",
			"--selector", "#hero"})
	}); err != nil {
		t.Fatalf("board comment with a selector: %v", err)
	}
	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{board, "--text", "no selector", "--by", "human"})
	}); err != nil {
		t.Fatalf("board comment without a selector: %v", err)
	}
	entries, _, err := bread(board)
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

// TestBoardCommentHTMLXYNeedSelector pins that a position without an element to
// be a fraction of is usage rather than a silent default: 0.5 of nothing is not
// a place a person meant.
func TestBoardCommentHTMLXYNeedSelector(t *testing.T) {
	_, board := htmlBoardCmdFor(t, "api")
	// A complete position with no element is the case this refusal exists for.
	ce := requireCLIError(t,
		cmdBoardComment([]string{board, "--text", "x", "--x", "0.5", "--y", "0.5"}), codeUsage, "")
	if !strings.Contains(ce.message, "--selector") {
		t.Errorf("refusal %q does not name --selector", ce.message)
	}
	// A half a position is the other rule, checked first.
	half := requireCLIError(t,
		cmdBoardComment([]string{board, "--text", "x", "--x", "0.5"}), codeUsage, "")
	if !strings.Contains(half.message, "go together") {
		t.Errorf("refusal %q does not say --x and --y go together", half.message)
	}
	// And --x with --selector is accepted, which is the case the refusal exists
	// to distinguish.
	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{board, "--text", "x", "--by", "human",
			"--selector", "#hero", "--x", "0.5", "--y", "0.5"})
	}); err != nil {
		t.Fatalf("board comment with a selector and a position: %v", err)
	}
}

// TestBoardCommentSelectorOnExcalidrawIsUsage pins the other side of the fork: an
// explicit .excalidraw path has no elements to select, so --selector is a
// mistake there too, and it is refused rather than silently dropped.
func TestBoardCommentSelectorOnExcalidrawIsUsage(t *testing.T) {
	liveRoot := boardStateRoot(t)
	stubRepoSeamAbsent(t)
	stubBoardTheme(t)
	stubBoardClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	id := "mm_aaaaaaaaaaaa"
	_, scene := liveBoardFor(t, liveRoot, id)
	writeScene(t, scene, `{"type":"excalidraw","elements":[]}`)

	ce := requireCLIError(t,
		cmdBoardComment([]string{scene, "--text", "x", "--selector", "#hero"}), codeUsage, "")
	if !strings.Contains(ce.message, "selector") {
		t.Errorf("refusal %q does not name the selector", ce.message)
	}
	// Nothing was written: the flag was refused before any append.
	comments, err := boardpkg.ReadComments(scene)
	if err != nil {
		t.Fatalf("ReadComments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("comments = %+v, want none", comments)
	}
}

// TestBoardCommentHTMLRepoNeverWritesPointer pins that a repo-scope HTML board
// is never a pointer write, exactly as on the Excalidraw side.
func TestBoardCommentHTMLRepoNeverWritesPointer(t *testing.T) {
	repoRoot, board := repoHTMLBoardFor(t)
	stubBoardClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))

	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{board, "--text", "hi", "--by", "human"})
	}); err != nil {
		t.Fatalf("board comment on a repo board: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, "docs", "boards", "current")); !os.IsNotExist(statErr) {
		t.Errorf("a repo comment created a pointer: stat error = %v, want not-exist", statErr)
	}
	entries, _, err := bread(board)
	if err != nil || len(entries) != 1 {
		t.Errorf("entries = %+v err = %v, want one", entries, err)
	}
}

// TestBoardCommentHTMLLeavesBoardByteIdentical pins the promise on the CLI side
// too: board.html is never touched, so a note is never an edit to the board.
func TestBoardCommentHTMLLeavesBoardByteIdentical(t *testing.T) {
	_, board := htmlBoardCmdFor(t, "api")
	before, err := os.ReadFile(board)
	if err != nil {
		t.Fatalf("read board: %v", err)
	}
	for i := range 3 {
		if _, _, err := captureOutput(t, func() error {
			return cmdBoardComment([]string{board, "--text", "n", "--by", "human",
				"--selector", "#hero", "--x", "0.5", "--y", "0.5"})
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	after, err := os.ReadFile(board)
	if err != nil {
		t.Fatalf("re-read board: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("board.html changed:\nbefore %q\nafter  %q", before, after)
	}
	entries, _, err := bread(board)
	if err != nil || len(entries) != 3 {
		t.Errorf("entries = %+v err = %v, want three", entries, err)
	}
}

// TestBoardCommentHTMLBareCallTargetsTheHTMLBoard pins D8 from the other side: a
// bare call with no path and no --board goes to the HTML board, not to a
// .excalidraw scene. If it went to the scene, the note would land in a file the
// HTML board never reads.
func TestBoardCommentHTMLBareCallTargetsTheHTMLBoard(t *testing.T) {
	// A bare call resolves to the default board name when there is no pointer,
	// so the board under test is named board rather than api.
	liveRoot, board := htmlBoardCmdFor(t, "board")
	stubBoardClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{"--text", "bare", "--by", "human"})
	}); err != nil {
		t.Fatalf("bare board comment: %v", err)
	}
	if _, statErr := os.Stat(bpath(board)); statErr != nil {
		t.Errorf("the bare call did not write annotations beside the board: %v", statErr)
	}
	// And no .excalidraw scene was created anywhere in the live root.
	err := filepath.Walk(liveRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".excalidraw") {
			t.Errorf("the bare call created the Excalidraw scene %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
