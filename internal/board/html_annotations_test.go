package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postAnnotations posts a body to /api/annotations with ifMatch, and returns the
// recorder. An empty token is sent as no token, so a caller can test the guard.
func postAnnotations(t *testing.T, s *HTMLServer, body, ifMatch, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/annotations", strings.NewReader(body))
	req.Host = testHost
	if token != "" {
		req.Header.Set("X-Relevo-Board-Token", token)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

// annotationsBoardServer returns a server over a board.html that exists on disk,
// which is the precondition every write test needs.
func annotationsBoardServer(t *testing.T) (*HTMLServer, string) {
	t.Helper()
	s, _ := testHTMLServer(t)
	if err := os.MkdirAll(filepath.Dir(s.BoardPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(s.BoardPath, []byte("<html><body>board</body></html>"), 0o600); err != nil {
		t.Fatalf("write board: %v", err)
	}
	return s, s.BoardPath
}

// TestHTMLAnnotationsRequiresToken pins that writing is guarded like reading: a
// post without the run's token is 401 and writes nothing, because the write is
// the only thing here a page could not already do by reading.
func TestHTMLAnnotationsRequiresToken(t *testing.T) {
	s, board := annotationsBoardServer(t)
	for _, token := range []string{"", "wrong-token"} {
		w := postAnnotations(t, s, `{"text":"hi"}`, "", token)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("post with token %q = %d, want 401", token, w.Code)
		}
	}
	if _, statErr := os.Stat(AnnotationsPath(board)); !os.IsNotExist(statErr) {
		t.Errorf("a refused post wrote the file: stat error = %v, want not-exist", statErr)
	}
}

// TestHTMLAnnotationsForeignHostForbidden pins that the Host guard covers the
// write route exactly as it covers the read one, so a rebinding attack cannot
// post either.
func TestHTMLAnnotationsForeignHostForbidden(t *testing.T) {
	s, board := annotationsBoardServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/annotations", strings.NewReader(`{"text":"hi"}`))
	req.Host = "evil.example.com"
	req.Header.Set("X-Relevo-Board-Token", testToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("post from a foreign Host = %d, want 403", w.Code)
	}
	if _, statErr := os.Stat(AnnotationsPath(board)); !os.IsNotExist(statErr) {
		t.Errorf("a forbidden post wrote the file: stat error = %v", statErr)
	}
}

// TestHTMLAnnotationsPostAppends pins the success answer: 201, the note in the
// body, an etag that moved, and by human even when the body claims otherwise.
func TestHTMLAnnotationsPostAppends(t *testing.T) {
	s, board := annotationsBoardServer(t)
	w := postAnnotations(t, s,
		`{"selector":"#hero","x":0.5,"y":0.25,"text":"a note","by":"mm_somebody_else"}`, "", testToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("post = %d, want 201 (%s)", w.Code, w.Body.String())
	}
	var doc annotationPostDoc
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if doc.AnnotationsEtag == "" {
		t.Error("annotationsEtag is empty after a create")
	}
	if doc.Annotation.By != humanBy {
		t.Errorf("by = %q, want %q: the server sets the author and never reads it from the body",
			doc.Annotation.By, humanBy)
	}
	if doc.Annotation.Selector != "#hero" || doc.Annotation.Text != "a note" ||
		doc.Annotation.X != 0.5 || doc.Annotation.Y != 0.25 {
		t.Errorf("annotation = %+v, want the posted selector, text and position", doc.Annotation)
	}

	// The etag the create answered with is the file's, so a second post naming
	// it succeeds.
	w2 := postAnnotations(t, s, `{"text":"second"}`, doc.AnnotationsEtag, testToken)
	if w2.Code != http.StatusCreated {
		t.Errorf("post at the returned etag = %d, want 201 (%s)", w2.Code, w2.Body.String())
	}
	entries, etag, err := ReadAnnotations(board)
	if err != nil || len(entries) != 2 || etag == "" {
		t.Errorf("entries = %+v etag = %q err = %v, want two entries and a real etag", entries, etag, err)
	}
}

// TestHTMLAnnotationsStaleIfMatchIs409 pins the compare-and-swap: a post naming
// an etag the file has moved past is 409 and writes nothing, so two people
// commenting at once never silently lose a note to a merge.
func TestHTMLAnnotationsStaleIfMatchIs409(t *testing.T) {
	s, board := annotationsBoardServer(t)
	first := postAnnotations(t, s, `{"text":"first"}`, "", testToken)
	if first.Code != http.StatusCreated {
		t.Fatalf("first post = %d, want 201", first.Code)
	}
	var doc annotationPostDoc
	if err := json.Unmarshal(first.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if w := postAnnotations(t, s, `{"text":"second"}`, doc.AnnotationsEtag, testToken); w.Code != http.StatusCreated {
		t.Fatalf("post at the live etag = %d, want 201", w.Code)
	}
	before, _ := os.ReadFile(AnnotationsPath(board))
	w := postAnnotations(t, s, `{"text":"third"}`, doc.AnnotationsEtag, testToken)
	if w.Code != http.StatusConflict {
		t.Fatalf("post at a stale etag = %d, want 409 (%s)", w.Code, w.Body.String())
	}
	if after, _ := os.ReadFile(AnnotationsPath(board)); string(after) != string(before) {
		t.Errorf("a 409 post wrote anyway:\n%s", after)
	}
}

// TestHTMLAnnotationsMissingBoardIs404 pins that a post cannot create a board.
// The store refuses a missing board.html, and the route reports that as 404
// rather than creating a file to hold the note.
func TestHTMLAnnotationsMissingBoardIs404(t *testing.T) {
	s, _ := testHTMLServer(t) // no board.html written
	w := postAnnotations(t, s, `{"text":"hi"}`, "", testToken)
	if w.Code != http.StatusNotFound {
		t.Errorf("post over a missing board = %d, want 404 (%s)", w.Code, w.Body.String())
	}
	if _, statErr := os.Stat(AnnotationsPath(s.BoardPath)); !os.IsNotExist(statErr) {
		t.Errorf("a 404 post created annotations: stat error = %v", statErr)
	}
}

// TestHTMLAnnotationsInvalidIs422 pins the input rules and the malformed-file
// rule as the same answer: both are 422, both write nothing.
func TestHTMLAnnotationsInvalidIs422(t *testing.T) {
	s, board := annotationsBoardServer(t)
	cases := []struct {
		name string
		body string
	}{
		{"not json", `{`},
		{"empty text", `{"text":"   "}`},
		{"x above one", `{"selector":"#a","x":1.5,"text":"hi"}`},
		{"y below zero", `{"selector":"#a","y":-1,"text":"hi"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := postAnnotations(t, s, tc.body, "", testToken); w.Code != http.StatusUnprocessableEntity {
				t.Errorf("post %s = %d, want 422 (%s)", tc.name, w.Code, w.Body.String())
			}
		})
	}
	if _, statErr := os.Stat(AnnotationsPath(board)); !os.IsNotExist(statErr) {
		t.Errorf("a 422 post wrote the file: stat error = %v", statErr)
	}

	// A malformed annotations file is the store's ErrInvalid, which is 422 here
	// rather than a 500: the post is refused because the file it would splice
	// into is not one it can parse.
	if err := os.WriteFile(AnnotationsPath(board), []byte(`{"not":"an array"}`), 0o600); err != nil {
		t.Fatalf("write annotations: %v", err)
	}
	if w := postAnnotations(t, s, `{"text":"hi"}`, "", testToken); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("post over a malformed file = %d, want 422 (%s)", w.Code, w.Body.String())
	}
}

// TestHTMLAnnotationsOversizedIs413 pins the body cap: a body over 64 KiB is
// refused whole, never read in part, so a note cannot land truncated.
func TestHTMLAnnotationsOversizedIs413(t *testing.T) {
	s, board := annotationsBoardServer(t)
	body := `{"text":"` + strings.Repeat("a", maxAnnotationBody) + `"}`
	if w := postAnnotations(t, s, body, "", testToken); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized post = %d, want 413 (%s)", w.Code, w.Body.String())
	}
	if _, statErr := os.Stat(AnnotationsPath(board)); !os.IsNotExist(statErr) {
		t.Errorf("a 413 post wrote the file: stat error = %v", statErr)
	}
	// A body under the wire cap but over the note cap is 422, not 413: the two
	// ceilings are distinct, and the wire one is only about refusing early.
	underWire := `{"text":"` + strings.Repeat("a", maxAnnotationBody-64) + `"}`
	if w := postAnnotations(t, s, underWire, "", testToken); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("post under the wire cap = %d, want 422 (%s)", w.Code, w.Body.String())
	}
}

// TestHTMLAnnotationsMethodNotAllowed pins that the write route is POST only,
// and says so: a GET or a DELETE is 405 with an Allow header naming POST, so a
// client learns the verb instead of guessing.
func TestHTMLAnnotationsMethodNotAllowed(t *testing.T) {
	s, _ := annotationsBoardServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/annotations", nil)
		req.Host = testHost
		req.Header.Set("X-Relevo-Board-Token", testToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/annotations = %d, want 405", method, w.Code)
		}
		if got := w.Header().Get("Allow"); got != http.MethodPost {
			t.Errorf("%s Allow = %q, want %q", method, got, http.MethodPost)
		}
	}
}

// TestHTMLBoardCarriesAnnotations pins that the board document carries the notes
// and their etag, so the shell can draw pins from the same read it renders from.
func TestHTMLBoardCarriesAnnotations(t *testing.T) {
	s, board := annotationsBoardServer(t)
	if w := postAnnotations(t, s, `{"selector":"#hero","x":0.5,"y":0.5,"text":"hi"}`, "", testToken); w.Code != http.StatusCreated {
		t.Fatalf("post = %d, want 201", w.Code)
	}
	w := doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken)
	doc := decodeBoardDoc(t, w)
	if len(doc.Annotations) != 1 || doc.Annotations[0].Text != "hi" {
		t.Errorf("annotations = %+v, want the posted note", doc.Annotations)
	}
	if doc.AnnotationsEtag == "" {
		t.Error("annotationsEtag is empty on a board that has notes")
	}
	// The etag in the document is the file's own, so the shell can post against
	// exactly what it was just handed.
	_, want, err := ReadAnnotations(board)
	if err != nil {
		t.Fatalf("ReadAnnotations: %v", err)
	}
	if doc.AnnotationsEtag != want {
		t.Errorf("annotationsEtag = %q, want the file's %q", doc.AnnotationsEtag, want)
	}
}

// TestHTMLBoardCarriesNoAnnotationsIsAnEmptyArray pins that [] is the cold-start
// value on the wire, never null, so the shell never has to tell "no notes" from
// "notes not loaded".
func TestHTMLBoardCarriesNoAnnotationsIsAnEmptyArray(t *testing.T) {
	s, _ := annotationsBoardServer(t)
	w := doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken)
	if !strings.Contains(w.Body.String(), `"annotations":[]`) {
		t.Errorf("body = %s, want an empty annotations array", w.Body.String())
	}
	doc := decodeBoardDoc(t, w)
	if doc.AnnotationsEtag != "" || doc.AnnotationsError != "" {
		t.Errorf("annotationsEtag = %q annotationsError = %q, want both empty",
			doc.AnnotationsEtag, doc.AnnotationsError)
	}
}

// TestHTMLBoardConditionalGetIs204 pins the poll's cheap answer: 204 only when
// both etags still hold. Either one moved is a full 200, because a new board
// re-renders the frame and new notes redraw the pins, and neither can be
// learned from a 204.
func TestHTMLBoardConditionalGetIs204(t *testing.T) {
	s, _ := annotationsBoardServer(t)
	if w := postAnnotations(t, s, `{"text":"hi"}`, "", testToken); w.Code != http.StatusCreated {
		t.Fatalf("post = %d, want 201", w.Code)
	}
	w := doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken)
	doc := decodeBoardDoc(t, w)

	match := "?etag=" + doc.Etag + "&aetag=" + doc.AnnotationsEtag
	if got := doHTML(t, s, http.MethodGet, "/api/board"+match, testHost, testToken); got.Code != http.StatusNoContent {
		t.Errorf("both etags matching = %d, want 204", got.Code)
	}
	if got := doHTML(t, s, http.MethodGet, "/api/board?aetag="+doc.AnnotationsEtag, testHost, testToken); got.Code != http.StatusOK {
		t.Errorf("only aetag given = %d, want 200", got.Code)
	}
	if got := doHTML(t, s, http.MethodGet, "/api/board?etag="+doc.Etag, testHost, testToken); got.Code != http.StatusOK {
		t.Errorf("only etag given = %d, want 200", got.Code)
	}

	// The board moved: 200 even though the annotations etag still matches.
	if err := os.WriteFile(s.BoardPath, []byte("<html><body>edited</body></html>"), 0o600); err != nil {
		t.Fatalf("rewrite board: %v", err)
	}
	if got := doHTML(t, s, http.MethodGet, "/api/board"+match, testHost, testToken); got.Code != http.StatusOK {
		t.Errorf("board etag moved = %d, want 200", got.Code)
	}

	// The annotations moved: 200 even though the board etag still matches.
	fresh := decodeBoardDoc(t, doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken))
	if w := postAnnotations(t, s, `{"text":"second"}`, fresh.AnnotationsEtag, testToken); w.Code != http.StatusCreated {
		t.Fatalf("second post = %d, want 201", w.Code)
	}
	moved := "?etag=" + fresh.Etag + "&aetag=" + fresh.AnnotationsEtag
	if got := doHTML(t, s, http.MethodGet, "/api/board"+moved, testHost, testToken); got.Code != http.StatusOK {
		t.Errorf("annotations etag moved = %d, want 200", got.Code)
	}
}

// TestHTMLBoardMalformedAnnotationsStillServesBoard pins that a bad notes file
// costs the notes and not the board: the board still renders, the annotations
// come back empty, and the reason is named so the shell can say it.
func TestHTMLBoardMalformedAnnotationsStillServesBoard(t *testing.T) {
	s, board := annotationsBoardServer(t)
	for _, body := range []string{`{"not":"an array"}`, `[{"id":"a1","text":"x"}]`} {
		if err := os.WriteFile(AnnotationsPath(board), []byte(body), 0o600); err != nil {
			t.Fatalf("write annotations: %v", err)
		}
		w := doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken)
		if w.Code != http.StatusOK {
			t.Errorf("board over %s = %d, want 200", body, w.Code)
			continue
		}
		doc := decodeBoardDoc(t, w)
		if doc.Annotations == nil || len(doc.Annotations) != 0 {
			t.Errorf("annotations = %#v, want an empty array", doc.Annotations)
		}
		if doc.AnnotationsError == "" {
			t.Errorf("annotationsError is empty over %s, want the reason", body)
		}
		if doc.Html == "" {
			t.Error("the board's bytes were lost because its notes were bad")
		}
	}
}

// TestHTMLBoardMalformedAnnotationsStillTakesA204 pins that the conditional GET
// is decided by the board alone when the notes cannot be read: an unreadable
// notes file must not make the poll spin on a document that never changes
// shape for the caller.
func TestHTMLBoardMalformedAnnotationsStillTakesA204(t *testing.T) {
	s, board := annotationsBoardServer(t)
	if err := os.WriteFile(AnnotationsPath(board), []byte(`{"not":"an array"}`), 0o600); err != nil {
		t.Fatalf("write annotations: %v", err)
	}
	doc := decodeBoardDoc(t, doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken))
	target := "?etag=" + doc.Etag + "&aetag=" + doc.AnnotationsEtag
	if got := doHTML(t, s, http.MethodGet, "/api/board"+target, testHost, testToken); got.Code != http.StatusNoContent {
		t.Errorf("poll with an unreadable notes file = %d, want 204", got.Code)
	}
}

// TestHTMLBoardHTMLIsFileBytesExactly pins the promise the whole comment feature
// rests on: nothing is injected into the board on the server. The bytes the
// browser receives are the bytes on disk, character for character, whether or
// not the board has notes.
func TestHTMLBoardHTMLIsFileBytesExactly(t *testing.T) {
	s, board := annotationsBoardServer(t)
	const page = "<!doctype html>\n<html>\n<body data-board-id=\"hero\">" +
		"<ul><li>one<li>two</ul>\n<script>var a = 1;" + "</" + "script>\n</body>\n</html>\n"
	if err := os.WriteFile(board, []byte(page), 0o600); err != nil {
		t.Fatalf("write board: %v", err)
	}
	doc := decodeBoardDoc(t, doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken))
	if doc.Html != page {
		t.Errorf("html = %q, want the file's bytes exactly", doc.Html)
	}
	if w := postAnnotations(t, s, `{"selector":"#hero","text":"hi"}`, "", testToken); w.Code != http.StatusCreated {
		t.Fatalf("post = %d, want 201", w.Code)
	}
	after := decodeBoardDoc(t, doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken))
	if after.Html != page {
		t.Errorf("html after a post = %q, want the file's bytes exactly", after.Html)
	}
}
