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

// TestBoardCommentJSON pins the comment document on a live scene: the id and
// resolved path a follow-up `board comments --json` shows, the pointer the
// write selects, and the unchanged human line.
func TestBoardCommentJSON(t *testing.T) {
	home, _ := initRoot(t)
	t.Chdir(t.TempDir())
	liveDir := boardJSONLiveDir(t, home, "mm_aaaaaaaaaaaa")
	liveScene := filepath.Join(liveDir, "board.excalidraw")
	t.Setenv("RELEVO_MASTERMIND", "mm_aaaaaaaaaaaa")

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"board", "comment", "--text", "hi", "--json"})
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
	if doc.ID == "" || doc.Path != liveScene {
		t.Errorf("doc = %+v, want an id at %s", doc, liveScene)
	}

	out := runDocs(t, "board", "comments", "--json")
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
		return run([]string{"board", "comment", "--text", "again"})
	})
	if err != nil {
		t.Fatalf("comment human: %v (stderr: %s)", err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("human stderr = %q, want empty", stderr)
	}
	out = runDocs(t, "board", "comments", "--json")
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
	if want := fmt.Sprintf("comment: %s  %s\n", again, liveScene); string(stdout) != want {
		t.Errorf("human line = %q, want %q", stdout, want)
	}
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
