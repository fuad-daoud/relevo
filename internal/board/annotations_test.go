package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// htmlBoardFor creates a live board.html at dir/name and returns its path.
func htmlBoardFor(t *testing.T, dir, name string) string {
	t.Helper()
	slug := filepath.Join(dir, name)
	if err := os.MkdirAll(slug, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(slug, htmlBoardFile)
	if err := os.WriteFile(path, []byte("<html><body>board</body></html>"), 0o600); err != nil {
		t.Fatalf("write board: %v", err)
	}
	return path
}

func writeAnnotations(t *testing.T, boardPath, body string) string {
	t.Helper()
	path := AnnotationsPath(boardPath)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write annotations: %v", err)
	}
	return path
}

// TestReadAnnotationsMissingIsEmpty pins the cold start: a board nobody has
// commented on has no notes and an empty etag, and reading creates nothing.
func TestReadAnnotationsMissingIsEmpty(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	entries, etag, err := ReadAnnotations(board)
	if err != nil {
		t.Fatalf("ReadAnnotations: %v", err)
	}
	if entries == nil || len(entries) != 0 {
		t.Errorf("entries = %#v, want an empty slice", entries)
	}
	if etag != "" {
		t.Errorf("etag = %q, want empty", etag)
	}
	if _, statErr := os.Stat(AnnotationsPath(board)); !os.IsNotExist(statErr) {
		t.Errorf("reading created the file: stat error = %v, want not-exist", statErr)
	}
}

// TestReadAnnotationsRefusesNonArray pins that a file which is not a JSON array
// is refused naming its path, and does not silently read as empty.
func TestReadAnnotationsRefusesNonArray(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	for _, body := range []string{`{"id":"a1"}`, `"hi"`, `null`, `7`} {
		writeAnnotations(t, board, body)
		_, _, err := ReadAnnotations(board)
		if err == nil || !strings.Contains(err.Error(), AnnotationsPath(board)) {
			t.Errorf("ReadAnnotations(%q) error = %v, want a refusal naming the path", body, err)
		}
	}
}

// TestReadAnnotationsRefusesEntryWithoutBy pins that an entry with no author is
// refused naming its index and id, so a person can find the bad line.
func TestReadAnnotationsRefusesEntryWithoutBy(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	writeAnnotations(t, board, `[
{"id":"a1","selector":"","x":0,"y":0,"text":"ok","by":"human","at":"2026-10-01T00:00:00Z"},
{"id":"a2","selector":"","x":0,"y":0,"text":"no author","at":"2026-10-01T00:00:00Z"}]`)
	_, _, err := ReadAnnotations(board)
	if err == nil {
		t.Fatal("ReadAnnotations accepted an entry with no author")
	}
	for _, want := range []string{"a2", "1", "author"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

// TestReadAnnotationsRefusesBadAt pins the timestamp rule: an at that is not
// RFC3339 is refused, and the refusal names the entry.
func TestReadAnnotationsRefusesBadAt(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	writeAnnotations(t, board, `[{"id":"a9","text":"x","by":"human","at":"yesterday"}]`)
	_, _, err := ReadAnnotations(board)
	if err == nil || !strings.Contains(err.Error(), "a9") || !strings.Contains(err.Error(), "RFC3339") {
		t.Errorf("ReadAnnotations error = %v, want a refusal naming a9 and RFC3339", err)
	}
}

// TestReadAnnotationsToleratesUnknownFields pins forward compatibility: a note
// carrying keys this reader does not know is still read, not refused.
func TestReadAnnotationsToleratesUnknownFields(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	writeAnnotations(t, board,
		`[{"id":"a1","selector":"#h","x":0.5,"y":0.5,"text":"hi","by":"human",`+
			`"at":"2026-10-01T00:00:00Z","tone":"loud","replyTo":"a0"}]`)
	entries, etag, err := ReadAnnotations(board)
	if err != nil {
		t.Fatalf("ReadAnnotations: %v", err)
	}
	if len(entries) != 1 || entries[0].Text != "hi" || etag == "" {
		t.Errorf("entries = %+v etag = %q, want the one entry and a real etag", entries, etag)
	}
}

// TestReadAnnotationsRefusesSymlink pins that a symlinked annotations file is
// refused rather than followed: it is either a link out of the board's own
// directory or nothing this code should read through.
func TestReadAnnotationsRefusesSymlink(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	elsewhere := filepath.Join(t.TempDir(), "real.json")
	if err := os.WriteFile(elsewhere, []byte(`[{"id":"a1","text":"x","by":"human","at":"2026-10-01T00:00:00Z"}]`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(elsewhere, AnnotationsPath(board)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := ReadAnnotations(board); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("ReadAnnotations error = %v, want a symlink refusal", err)
	}
}

// TestAppendAnnotationKeepsPriorBytes is the byte-stability pin: appending must
// splice, not re-marshal. The prior file's exact prefix survives, so formatting,
// unknown keys and hand-made entries are never rewritten.
func TestAppendAnnotationKeepsPriorBytes(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	prior := "[\n" +
		`{"id":"a1","selector":"#h","x":0.5,"y":0.5,"text":"first","by":"human","at":"2026-10-01T00:00:00Z","tone":"loud"}` +
		"\n]\n"
	writeAnnotations(t, board, prior)

	if _, _, err := AppendAnnotation(board, AnnotationRequest{
		Text: "second", By: "human", At: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}, nil); err != nil {
		t.Fatalf("AppendAnnotation: %v", err)
	}
	got, err := os.ReadFile(AnnotationsPath(board))
	if err != nil {
		t.Fatalf("read annotations: %v", err)
	}
	if !strings.HasPrefix(string(got), prior[:len(prior)-3]) {
		t.Errorf("file no longer starts with the prior bytes:\n%s", got)
	}
	if strings.Count(string(got), `"tone":"loud"`) != 1 {
		t.Errorf("the unknown key was rewritten:\n%s", got)
	}
	if !strings.Contains(string(got), `"text":"second"`) {
		t.Errorf("the appended entry is missing:\n%s", got)
	}
}

// TestAppendAnnotationLeavesBoardHTMLByteIdentical pins the promise that makes
// this feature safe to layer on an HTML board: commenting never edits the board.
func TestAppendAnnotationLeavesBoardHTMLByteIdentical(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	before, err := os.ReadFile(board)
	if err != nil {
		t.Fatalf("read board: %v", err)
	}
	for i := range 3 {
		if _, _, err := AppendAnnotation(board, AnnotationRequest{
			Selector: "#h", X: 0.25, Y: float64(i) / 2, Text: "note", By: "human",
			At: time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC),
		}, nil); err != nil {
			t.Fatalf("AppendAnnotation %d: %v", i, err)
		}
	}
	after, err := os.ReadFile(board)
	if err != nil {
		t.Fatalf("re-read board: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("board.html changed:\nbefore %q\nafter  %q", before, after)
	}
	entries, _, err := ReadAnnotations(board)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries = %+v err = %v, want three", entries, err)
	}
}

// TestAppendAnnotationRefusesMissingBoard pins that an append creates no board:
// a comment on a board that is not there is refused, not a side effect.
func TestAppendAnnotationRefusesMissingBoard(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "api", htmlBoardFile)
	_, _, err := AppendAnnotation(missing, AnnotationRequest{Text: "x", By: "human"}, nil)
	if err == nil || !strings.Contains(err.Error(), ErrNotFound.Error()) {
		t.Fatalf("AppendAnnotation error = %v, want a not-found refusal", err)
	}
	if _, statErr := os.Stat(AnnotationsPath(missing)); !os.IsNotExist(statErr) {
		t.Errorf("a refused append created annotations: stat error = %v, want not-exist", statErr)
	}
}

// TestAppendAnnotationStaleEtagConflicts is the compare-and-swap pin: a page
// that posted against an etag the file has since moved past is refused and
// writes nothing, so nothing is ever merged.
func TestAppendAnnotationStaleEtagConflicts(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	if _, etag, err := AppendAnnotation(board, AnnotationRequest{
		Text: "first", By: "human", At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}, nil); err != nil {
		t.Fatalf("first append: %v", err)
	} else if etag == "" {
		t.Fatal("first append returned an empty etag")
	} else {
		stale := etag
		before, _ := os.ReadFile(AnnotationsPath(board))
		if _, _, err := AppendAnnotation(board, AnnotationRequest{Text: "second", By: "human"}, &stale); err != nil {
			t.Fatalf("append at the current etag: %v", err)
		}
		// The etag moved, so the same value is now stale.
		after, _ := os.ReadFile(AnnotationsPath(board))
		_, _, err = AppendAnnotation(board, AnnotationRequest{Text: "third", By: "human"}, &stale)
		if err == nil || !strings.Contains(err.Error(), ErrConflict.Error()) {
			t.Fatalf("stale append error = %v, want a conflict", err)
		}
		if got, _ := os.ReadFile(AnnotationsPath(board)); string(got) != string(after) {
			t.Errorf("a conflicted append wrote anyway:\n%s", got)
		}
		_ = before
	}
}

// TestAppendAnnotationValidation is the table of refusals: each case is an input
// the store must turn away rather than write.
func TestAppendAnnotationValidation(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		req  AnnotationRequest
		want string
	}{
		{"empty text", AnnotationRequest{Text: "   ", By: "human"}, "text"},
		{"empty author", AnnotationRequest{Text: "x", By: "  "}, "author"},
		{"oversized text", AnnotationRequest{Text: strings.Repeat("a", 8193), By: "human"}, "8192"},
		{"oversized selector", AnnotationRequest{Selector: strings.Repeat("a", 1025), Text: "x", By: "human"}, "1024"},
		{"control in selector", AnnotationRequest{Selector: "#a\nb", Text: "x", By: "human"}, "control"},
		{"x above one", AnnotationRequest{Selector: "#a", X: 1.5, Text: "x", By: "human"}, "fraction"},
		{"y below zero", AnnotationRequest{Selector: "#a", Y: -0.5, Text: "x", By: "human"}, "fraction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			board := htmlBoardFor(t, t.TempDir(), "api")
			req := tc.req
			req.At = at
			if _, _, err := AppendAnnotation(board, req, nil); err == nil {
				t.Fatalf("AppendAnnotation accepted %+v", tc.req)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q does not name %q", err, tc.want)
			}
			if _, statErr := os.Stat(AnnotationsPath(board)); !os.IsNotExist(statErr) {
				t.Errorf("a refused append wrote the file: stat error = %v", statErr)
			}
		})
	}
}

// TestAppendAnnotationValidationForcesOriginForABoardLevelNote pins the one
// rule that cannot be refused: a note with no selector has no box to be a
// fraction of, so its position is defined to be the origin.
func TestAppendAnnotationValidationForcesOriginForABoardLevelNote(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	got, _, err := AppendAnnotation(board, AnnotationRequest{
		X: 0.9, Y: 0.9, Text: "board level", By: "human",
		At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}, nil)
	if err != nil {
		t.Fatalf("AppendAnnotation: %v", err)
	}
	if got.X != 0 || got.Y != 0 {
		t.Errorf("board-level note at (%v, %v), want (0, 0)", got.X, got.Y)
	}
}

// TestAppendAnnotationIDsAreUnique pins that the id is drawn from crypto/rand
// and never repeats inside one file, which is what a click focuses.
func TestAppendAnnotationIDsAreUnique(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	seen := map[string]bool{}
	for i := range 25 {
		got, _, err := AppendAnnotation(board, AnnotationRequest{
			Selector: "#h", X: 0.5, Y: 0.5, Text: "n", By: "human",
			At: time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC),
		}, nil)
		if err != nil {
			t.Fatalf("AppendAnnotation %d: %v", i, err)
		}
		if !strings.HasPrefix(got.ID, "a") || len(got.ID) != 1+annotationIDHex {
			t.Errorf("id = %q, want an a plus %d hex characters", got.ID, annotationIDHex)
		}
		if strings.ToLower(got.ID) != got.ID {
			t.Errorf("id = %q, want lowercase", got.ID)
		}
		if seen[got.ID] {
			t.Fatalf("id %q was drawn twice", got.ID)
		}
		seen[got.ID] = true
	}
	if len(seen) != 25 {
		t.Errorf("ids = %d, want 25", len(seen))
	}
}

// TestAppendAnnotationSplicesIntoAnEmptyArray pins the cold-start splice: the
// first note lands in a file the store creates, in the one-entry-per-line shape
// the format promises.
func TestAppendAnnotationSplicesIntoAnEmptyArray(t *testing.T) {
	board := htmlBoardFor(t, t.TempDir(), "api")
	if _, _, err := AppendAnnotation(board, AnnotationRequest{
		Selector: "#h", X: 0.5, Y: 0.5, Text: "first", By: "human",
		At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}, nil); err != nil {
		t.Fatalf("AppendAnnotation: %v", err)
	}
	got, err := os.ReadFile(AnnotationsPath(board))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(string(got), "[\n") || !strings.HasSuffix(string(got), "\n]\n") {
		t.Errorf("file = %q, want one entry per line inside an array", got)
	}
	if strings.Count(string(got), "\n") != 3 {
		t.Errorf("file = %q, want exactly one entry line", got)
	}
}
