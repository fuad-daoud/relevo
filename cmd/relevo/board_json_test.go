package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board"
)

// The board writes under --json: one document per verb, empty stderr, and a
// follow-up read of the same CLI showing what the document claims it wrote.
// The fixtures are temp scene files and one temp git repository; no harness
// and no network is ever reached (the CI rule for cmd/relevo tests).

// boardJSONRepo makes a temp git repository and chdirs into it: the repo
// board verbs resolve their scene against the top level git reports, and the
// developer's own tree is never touched.
func boardJSONRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Chdir(dir)
	return dir
}

// boardJSONLiveDir makes one MasterMind's live directory under the isolated
// state root and returns it. An mm_ id resolves without touching a database.
func boardJSONLiveDir(t *testing.T, home, id string) string {
	t.Helper()
	liveDir := filepath.Join(home, "state", "relevo", "boards", id)
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return liveDir
}

// TestBoardAnnotateJSON pins the annotate document against a follow-up
// read, and the human line against the byte shape it had before --json
// existed.
func TestBoardAnnotateJSON(t *testing.T) {
	initRoot(t)
	repo := boardJSONRepo(t)
	scene := filepath.Join(repo, "board.excalidraw")
	if err := os.WriteFile(scene, []byte(`{"type":"excalidraw","elements":[]}`), 0o600); err != nil {
		t.Fatalf("write scene: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "annotate", "board.excalidraw", "--text", "hello", "--x", "5", "--y", "6", "--json"})
	})
	if err != nil {
		t.Fatalf("annotate --json: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	var doc AnnotateDoc
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("stdout = %q, want exactly one JSON object", stdout)
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if doc.ID == "" || doc.Text != "hello" || doc.X != 5 || doc.Y != 6 {
		t.Errorf("doc = %+v, want hello at (5, 6) under a minted id", doc)
	}

	// The follow-up read is the same CLI: board text lists the element the
	// document named.
	out := runDocs(t, "board", "text", "board.excalidraw", "--json")
	var els []board.AgentText
	if err := json.Unmarshal(out, &els); err != nil {
		t.Fatalf("text --json is not an element array: %v (%q)", err, out)
	}
	if len(els) != 1 || els[0] != (board.AgentText{ID: doc.ID, X: doc.X, Y: doc.Y, Text: doc.Text}) {
		t.Errorf("read back = %+v, want %+v", els, doc)
	}

	stdout, stderr, err = captureOutput(t, func() error {
		return run([]string{"board", "annotate", "board.excalidraw", "--text", "world"})
	})
	if err != nil {
		t.Fatalf("annotate human: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("human stderr = %q, want empty", stderr)
	}
	line := string(stdout)
	rest, ok := strings.CutPrefix(line, "board: annotated ")
	if !ok {
		t.Fatalf("stdout = %q, want \"board: annotated <path> (id <id>)\"", stdout)
	}
	printedPath, printedID, ok := strings.Cut(rest, " (id ")
	if !ok {
		t.Fatalf("stdout = %q, want \"board: annotated <path> (id <id>)\"", stdout)
	}
	printedID = strings.TrimSuffix(printedID, ")\n")
	if want, err := filepath.EvalSymlinks(scene); err == nil {
		if got, err := filepath.EvalSymlinks(printedPath); err != nil || got != want {
			t.Errorf("printed path = %q, want the resolved scene %q", printedPath, want)
		}
	} else {
		t.Fatalf("EvalSymlinks(scene): %v", err)
	}
	out = runDocs(t, "board", "text", "board.excalidraw", "--json")
	els = nil
	if err := json.Unmarshal(out, &els); err != nil {
		t.Fatalf("text --json after the human run: %v (%q)", err, out)
	}
	var found *board.AgentText
	for i := range els {
		if els[i].ID == printedID {
			found = &els[i]
		}
	}
	if found == nil || found.Text != "world" {
		t.Errorf("element %q = %+v, want the appended world element", printedID, found)
	}
}

// TestBoardCommentJSON pins the comment document on an explicit .excalidraw
// scene: the id and resolved path a follow-up `board comments --json` shows,
// the pointer the write selects, and the unchanged human line. The bare call
// targets the HTML board, so the scene cases name the scene explicitly.
func TestBoardCommentJSON(t *testing.T) {
	home, _ := initRoot(t)
	t.Chdir(t.TempDir())
	liveDir := boardJSONLiveDir(t, home, "mm_aaaaaaaaaaaa")
	liveScene := filepath.Join(liveDir, "board.excalidraw")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "comment", liveScene, "--text", "hi", "--json"})
	})
	if err != nil {
		t.Fatalf("comment --json: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	var doc CommentDoc
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("stdout = %q, want exactly one JSON object", stdout)
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if doc.ID == "" || !evaluatedPathsEqual(t, doc.Path, liveScene) {
		t.Errorf("doc = %+v, want an id at %s", doc, liveScene)
	}

	out := runDocs(t, "board", "comments", liveScene, "--json")
	var comments []board.Comment
	if err := json.Unmarshal(out, &comments); err != nil {
		t.Fatalf("comments --json is not an array: %v (%q)", err, out)
	}
	if len(comments) != 1 || comments[0].ID != doc.ID || comments[0].Text != "hi" {
		t.Errorf("read back = %+v, want one comment %s", comments, doc.ID)
	}

	// The pointer was written by the live select, so the next write names the
	// same scene from it.
	stdout, stderr, err = captureOutput(t, func() error {
		return run([]string{"board", "comment", liveScene, "--text", "again"})
	})
	if err != nil {
		t.Fatalf("comment human: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("human stderr = %q, want empty", stderr)
	}
	out = runDocs(t, "board", "comments", liveScene, "--json")
	comments = nil
	if err := json.Unmarshal(out, &comments); err != nil {
		t.Fatalf("comments --json after the human run: %v", err)
	}
	var again string
	for _, c := range comments {
		if c.Text == "again" {
			again = c.ID
		}
	}
	if again == "" {
		t.Fatalf("comments = %+v, want the appended again comment", comments)
	}
	evaluated, err := filepath.EvalSymlinks(liveScene)
	if err != nil {
		t.Fatalf("EvalSymlinks(liveScene): %v", err)
	}
	if want := fmt.Sprintf("comment: %s  %s\n", again, evaluated); string(stdout) != want {
		t.Errorf("human line = %q, want %q", stdout, want)
	}
}

// evaluatedPathsEqual compares two spellings of one path through EvalSymlinks,
// so a temp root that canonicalizes differently resolves in the test, not in
// the resolver: the test pins the target, not the spelling. The file half is
// compared by name because EvalSymlinks refuses a path whose tail is missing.
func evaluatedPathsEqual(t *testing.T, got, want string) bool {
	t.Helper()
	if filepath.Base(got) != filepath.Base(want) {
		return false
	}
	gotDir, err := filepath.EvalSymlinks(filepath.Dir(got))
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", got, err)
	}
	wantDir, err := filepath.EvalSymlinks(filepath.Dir(want))
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", want, err)
	}
	return filepath.Join(gotDir, filepath.Base(got)) == filepath.Join(wantDir, filepath.Base(want))
}

// TestBoardPromoteJSON pins the promote document against the copied board:
// the live source and repo destination the read-back shows, and the unchanged
// human line on a forced re-copy of the same paths.
func TestBoardPromoteJSON(t *testing.T) {
	home, _ := initRoot(t)
	liveDir := boardJSONLiveDir(t, home, "mm_aaaaaaaaaaaa")
	liveBoard := filepath.Join(liveDir, "api", "board.html")
	if err := os.MkdirAll(filepath.Dir(liveBoard), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(liveBoard, []byte("<h1>api</h1>"), 0o600); err != nil {
		t.Fatalf("write live board: %v", err)
	}
	repo := boardJSONRepo(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "promote", "--mastermind", "mm_aaaaaaaaaaaa", "--board", "api", "--json"})
	})
	if err != nil {
		t.Fatalf("promote --json: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	var doc PromoteDoc
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("stdout = %q, want exactly one JSON object", stdout)
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if doc.Live != liveBoard {
		t.Errorf("live = %q, want %s", doc.Live, liveBoard)
	}
	wantDst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	if got, err := filepath.EvalSymlinks(doc.Dst); err != nil {
		t.Fatalf("EvalSymlinks(dst): %v", err)
	} else if want, err := filepath.EvalSymlinks(wantDst); err != nil || got != want {
		t.Errorf("repo = %q, want the promoted board at %s", doc.Dst, wantDst)
	}
	if got, err := os.ReadFile(doc.Dst); err != nil || string(got) != "<h1>api</h1>" {
		t.Errorf("promoted bytes = %q/%v, want the live board's", got, err)
	}

	stdout, stderr, err = captureOutput(t, func() error {
		return run([]string{"board", "promote", "--mastermind", "mm_aaaaaaaaaaaa", "--board", "api", "--force"})
	})
	if err != nil {
		t.Fatalf("promote human: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("human stderr = %q, want empty", stderr)
	}
	if want := fmt.Sprintf("board: promoted %s -> %s\n", doc.Live, doc.Dst); string(stdout) != want {
		t.Errorf("human line = %q, want %q", stdout, want)
	}
}

// TestBoardJSONForcedFailures pins that a bad input under --json is a coded
// refusal the envelope can render: usage with the help hint, and nothing
// printed on either stream before report. The boardRefusal mapping is the
// single route, so no board refusal escapes it uncoded.
func TestBoardJSONForcedFailures(t *testing.T) {
	initRoot(t)
	t.Chdir(t.TempDir())

	for _, c := range []struct {
		name string
		args []string
		msg  string
	}{
		{"annotate without text", []string{"board", "annotate", "board.excalidraw", "--json"}, "--text is required"},
		{"comment without text", []string{"board", "comment", "--json"}, "--text is required"},
		{"promote with a scene path", []string{"board", "promote", "docs/boards/api/board.html", "--json"}, "takes no scene path"},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, runErr := captureOutput(t, func() error { return run(c.args) })
			ce := requireCLIError(t, runErr, codeUsage, "relevo help")
			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != catalogExit(codeUsage) {
				t.Errorf("exit = %v, want %d (usage)", runErr, catalogExit(codeUsage))
			}
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
			if len(stderr) != 0 {
				t.Errorf("stderr = %q, want empty before report", stderr)
			}
			if !strings.Contains(ce.message, c.msg) {
				t.Errorf("message = %q, want it to contain %q", ce.message, c.msg)
			}
		})
	}
}

// boardJSONHTMLBoard seeds id's live single-file board under name and returns
// the board.html path.
func boardJSONHTMLBoard(t *testing.T, home, id, name string) string {
	t.Helper()
	liveDir := boardJSONLiveDir(t, home, id)
	page := filepath.Join(liveDir, name, "board.html")
	if err := os.WriteFile(page, []byte("<html><body>board</body></html>"), 0o600); err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("write board.html: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(page), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(page, []byte("<html><body>board</body></html>"), 0o600); err != nil {
			t.Fatalf("write board.html: %v", err)
		}
	}
	return page
}

// boardJSONAnnotations writes an annotations file beside a board.
func boardJSONAnnotations(t *testing.T, page, body string) string {
	t.Helper()
	path := board.AnnotationsPath(page)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write annotations: %v", err)
	}
	return path
}

// TestBoardCommentsHTMLJSONShape pins the machine shape on an HTML board: one
// compact JSON array whose objects are id, selector, x, y, text, by, at and the
// normalized thread in order, and [] -- never null -- when there are none. The
// stored entries name no thread, so each reads as a thread of its own.
func TestBoardCommentsHTMLJSONShape(t *testing.T) {
	home, _ := initRoot(t)
	page := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	boardJSONAnnotations(t, page,
		`[{"id":"a1","selector":"#hero","x":0.5,"y":0.25,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z"},`+
			`{"id":"a2","selector":"","x":0,"y":0,"text":"board level","by":"human","at":"2026-10-02T12:00:00Z"}]`)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "comments", page, "--json"})
	})
	if err != nil {
		t.Fatalf("board comments --json: %v (stderr %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	want := `[{"id":"a1","selector":"#hero","x":0.5,"y":0.25,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z","thread":"a1"},` +
		`{"id":"a2","selector":"","x":0,"y":0,"text":"board level","by":"human","at":"2026-10-02T12:00:00Z","thread":"a2"}]` + "\n"
	if string(stdout) != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	// No annotations at all is [].
	boardJSONAnnotations(t, page, "[]\n")
	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"board", "comments", page, "--json"})
	})
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
	home, _ := initRoot(t)
	page := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	boardJSONAnnotations(t, page,
		`[{"id":"a1","selector":"#hero","x":0.5,"y":0.25,"text":"hi","by":"human","at":"2026-10-01T12:00:00Z"}]`)
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"board", "comments", page})
	})
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
// created. This is the same contract the scene reader has.
func TestBoardCommentsHTMLMissingBoardIsEmpty(t *testing.T) {
	home, _ := initRoot(t)
	page := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	if err := os.Remove(page); err != nil {
		t.Fatalf("remove board: %v", err)
	}
	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "comments", page, "--json"})
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
	if _, statErr := os.Stat(board.AnnotationsPath(page)); !os.IsNotExist(statErr) {
		t.Errorf("listing created the annotations file: stat error = %v", statErr)
	}
}

// TestBoardCommentsHTMLNeverWritesPointer pins that the reader never writes
// the pointer, whatever it resolves to: listing is not opening the board.
func TestBoardCommentsHTMLNeverWritesPointer(t *testing.T) {
	home, _ := initRoot(t)
	boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "board")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"board", "comments", "--json"})
	})
	if err != nil {
		t.Fatalf("board comments: %v", err)
	}
	if string(stdout) != "[]\n" {
		t.Errorf("stdout = %q, want []\\n", stdout)
	}
	if _, statErr := os.Stat(filepath.Join(boardJSONLiveDir(t, home, "mm_aaaaaaaaaaaa"), "current")); !os.IsNotExist(statErr) {
		t.Errorf("board comments wrote the pointer: stat error = %v, want not-exist", statErr)
	}
}

// TestBoardCommentHTMLAppendsAndPrintsLine pins the success line and the
// append on an HTML board: comment: <id>  <annotations path> -- the
// annotations path, not the board's, because that is the file the note
// landed in.
func TestBoardCommentHTMLAppendsAndPrintsLine(t *testing.T) {
	home, _ := initRoot(t)
	page := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"board", "comment", page, "--text", "a note", "--by", "human",
			"--selector", "#hero", "--x", "0.5", "--y", "0.25"})
	})
	if err != nil {
		t.Fatalf("board comment: %v", err)
	}
	rest, ok := strings.CutPrefix(string(stdout), "comment: ")
	if !ok {
		t.Fatalf("stdout = %q, want \"comment: <wrote id>  <annotations path>\"", stdout)
	}
	printedID, printedPath, ok := strings.Cut(rest, "  ")
	if !ok {
		t.Fatalf("stdout = %q, want \"comment: <wrote id>  <annotations path>\"", stdout)
	}
	printedPath = strings.TrimSuffix(printedPath, "\n")
	if printedID == "" || !evaluatedPathsEqual(t, printedPath, board.AnnotationsPath(page)) {
		t.Errorf("stdout = %q, want an id with the annotations path %s", stdout, board.AnnotationsPath(page))
	}
	entries, _, err := board.ReadAnnotations(page)
	if err != nil {
		t.Fatalf("ReadAnnotations: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != printedID || entries[0].Text != "a note" ||
		entries[0].Selector != "#hero" || entries[0].X != 0.5 || entries[0].Y != 0.25 ||
		entries[0].By != "human" {
		t.Errorf("entries = %+v, want the appended note under %q", entries, printedID)
	}
}

// TestBoardCommentHTMLRefusesMissingBoard pins: a comment on an HTML board
// that is not there is refused with the refused code and exit 2, and creates
// nothing: a comment must not conjure the scene it names.
func TestBoardCommentHTMLRefusesMissingBoard(t *testing.T) {
	home, _ := initRoot(t)
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	path := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove board: %v", err)
	}
	ce := requireCLIError(t,
		cmdBoardComment([]string{path, "--text", "hi", "--by", "human"}), codeRefused, "")
	if !strings.Contains(ce.message, "does not exist") {
		t.Errorf("refusal %q does not say the board is missing", ce.message)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("a refused comment created board.html: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(board.AnnotationsPath(path)); !os.IsNotExist(statErr) {
		t.Errorf("a refused comment created annotations: stat error = %v", statErr)
	}
}

// TestBoardCommentHTMLSelectorFallbackDefaults pins the defaults: --selector
// alone places the note at 0,0 and no --selector at all makes it a board-level
// note. Both are usable defaults rather than errors, because both are things a
// person wants to write.
func TestBoardCommentHTMLSelectorFallbackDefaults(t *testing.T) {
	home, _ := initRoot(t)
	page := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{page, "--text", "with selector", "--by", "human",
			"--selector", "#hero"})
	}); err != nil {
		t.Fatalf("board comment with a selector: %v", err)
	}
	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{page, "--text", "no selector", "--by", "human"})
	}); err != nil {
		t.Fatalf("board comment without a selector: %v", err)
	}
	entries, _, err := board.ReadAnnotations(page)
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
	home, _ := initRoot(t)
	page := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	// A complete position with no element is the case this refusal exists for.
	ce := requireCLIError(t,
		cmdBoardComment([]string{page, "--text", "x", "--x", "0.5", "--y", "0.5"}), codeUsage, "")
	if !strings.Contains(ce.message, "--selector") {
		t.Errorf("refusal %q does not name --selector", ce.message)
	}
	// A half a position is the other rule, checked first.
	half := requireCLIError(t,
		cmdBoardComment([]string{page, "--text", "x", "--x", "0.5"}), codeUsage, "")
	if !strings.Contains(half.message, "go together") {
		t.Errorf("refusal %q does not say --x and --y go together", half.message)
	}
	// And --x with --selector is accepted, which is the case the refusal exists
	// to distinguish.
	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{page, "--text", "x", "--by", "human",
			"--selector", "#hero", "--x", "0.5", "--y", "0.5"})
	}); err != nil {
		t.Fatalf("board comment with a selector and a position: %v", err)
	}
}

// TestBoardCommentHTMLRepoNeverWritesPointer pins that a repo-scope HTML board
// is never a pointer write, exactly as on the Excalidraw side.
func TestBoardCommentHTMLRepoNeverWritesPointer(t *testing.T) {
	repo := boardJSONRepo(t)
	dir := filepath.Join(repo, "docs", "boards", "api")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "board.html")
	if err := os.WriteFile(path, []byte("<html><body>board</body></html>"), 0o600); err != nil {
		t.Fatalf("write board.html: %v", err)
	}

	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{path, "--text", "hi", "--by", "human"})
	}); err != nil {
		t.Fatalf("board comment on a repo board: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "docs", "boards", "current")); !os.IsNotExist(statErr) {
		t.Errorf("a repo comment created a pointer: stat error = %v, want not-exist", statErr)
	}
	entries, _, err := board.ReadAnnotations(path)
	if err != nil || len(entries) != 1 {
		t.Errorf("entries = %+v err = %v, want one", entries, err)
	}
}

// TestBoardCommentHTMLLeavesBoardByteIdentical pins the promise on the CLI side
// too: board.html is never touched, so a note is never an edit to the board.
func TestBoardCommentHTMLLeavesBoardByteIdentical(t *testing.T) {
	home, _ := initRoot(t)
	page := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	before, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("read board: %v", err)
	}
	for i := range 3 {
		if _, _, err := captureOutput(t, func() error {
			return cmdBoardComment([]string{page, "--text", "n", "--by", "human",
				"--selector", "#hero", "--x", "0.5", "--y", "0.5"})
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	after, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("re-read board: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("board.html changed:\nbefore %q\nafter  %q", before, after)
	}
	entries, _, err := board.ReadAnnotations(page)
	if err != nil || len(entries) != 3 {
		t.Errorf("entries = %+v err = %v, want three", entries, err)
	}
}

// TestBoardCommentHTMLBareCallTargetsTheHTMLBoard pins that a bare call with no
// path and no --board goes to the HTML board, not to a .excalidraw scene. If it
// went to the scene, the note would land in a file the HTML board never reads.
func TestBoardCommentHTMLBareCallTargetsTheHTMLBoard(t *testing.T) {
	home, _ := initRoot(t)
	// A bare call resolves to the default board name when there is no pointer,
	// so the board under test is named board rather than api.
	path := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "board")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	if _, _, err := captureOutput(t, func() error {
		return cmdBoardComment([]string{"--text", "bare", "--by", "human"})
	}); err != nil {
		t.Fatalf("bare board comment: %v", err)
	}
	if _, statErr := os.Stat(board.AnnotationsPath(path)); statErr != nil {
		t.Errorf("the bare call did not write annotations beside the board: %v", statErr)
	}
	// And no .excalidraw scene was created anywhere in the live root.
	err := filepath.Walk(filepath.Join(home, "state", "relevo", "boards"),
		func(name string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(name, ".excalidraw") {
				t.Errorf("the bare call created the Excalidraw scene %s", name)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestBoardPromotePrintsAnnotationsLine pins the second success line: when the
// source had an annotations file, promote says so, naming both paths, so a
// person can see the notes came along without opening the repo.
func TestBoardPromotePrintsAnnotationsLine(t *testing.T) {
	home, _ := initRoot(t)
	live := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	boardJSONAnnotations(t, live,
		`[{"id":"a1","selector":"#hero","x":0.5,"y":0.5,"text":"hi","by":"human","at":"2026-10-01T00:00:00Z"}]`)
	repo := boardJSONRepo(t)

	stdout, _, err := captureOutput(t, func() error {
		return cmdBoardPromote([]string{"--board", "api", "--to", "api"})
	})
	if err != nil {
		t.Fatalf("board promote: %v", err)
	}
	dst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	wantSecond := "board: promoted " + board.AnnotationsPath(live) +
		" -> " + board.AnnotationsPath(dst) + "\n"
	if !strings.HasSuffix(string(stdout), wantSecond) {
		t.Errorf("stdout = %q, want it to end with %q", stdout, wantSecond)
	}
	// The first line is still the board's own, unchanged.
	first, _, _ := strings.Cut(string(stdout), "\n")
	if !strings.HasPrefix(first, "board: promoted ") || !strings.HasSuffix(first, dst) {
		t.Errorf("first line = %q, want the board promotion line", first)
	}
}

// TestBoardPromotePrintsOneLineWithoutAnnotations pins that the second line is
// not printed when there was nothing to copy: an unconditional second line would
// name a file that does not exist.
func TestBoardPromotePrintsOneLineWithoutAnnotations(t *testing.T) {
	home, _ := initRoot(t)
	live := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	boardJSONRepo(t)

	stdout, _, err := captureOutput(t, func() error {
		return cmdBoardPromote([]string{"--board", "api", "--to", "api"})
	})
	if err != nil {
		t.Fatalf("board promote: %v", err)
	}
	if got := strings.Count(string(stdout), "\n"); got != 1 {
		t.Errorf("stdout = %q, want exactly one line", stdout)
	}
	if !strings.HasPrefix(string(stdout), "board: promoted "+live) {
		t.Errorf("stdout = %q, want the board promotion line", stdout)
	}
}

// TestBoardPromotePromotesTheAnnotationsOnDisk pins the effect rather than the
// line: the repo board has its notes beside it after the promotion.
func TestBoardPromotePromotesTheAnnotationsOnDisk(t *testing.T) {
	home, _ := initRoot(t)
	live := boardJSONHTMLBoard(t, home, "mm_aaaaaaaaaaaa", "api")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")
	boardJSONAnnotations(t, live,
		`[{"id":"a1","selector":"#hero","x":0.5,"y":0.5,"text":"hi","by":"human","at":"2026-10-01T00:00:00Z"}]`)
	repo := boardJSONRepo(t)

	if _, _, err := captureOutput(t, func() error {
		return cmdBoardPromote([]string{"--board", "api", "--to", "api"})
	}); err != nil {
		t.Fatalf("board promote: %v", err)
	}
	dst := filepath.Join(repo, "docs", "boards", "api", "board.html")
	got, err := os.ReadFile(board.AnnotationsPath(dst))
	if err != nil {
		t.Fatalf("read promoted annotations: %v", err)
	}
	if !strings.Contains(string(got), `"text":"hi"`) {
		t.Errorf("promoted annotations = %q, want the note", got)
	}
}
