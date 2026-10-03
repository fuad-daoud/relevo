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

func testHTMLServer(t *testing.T) (*HTMLServer, string) {
	t.Helper()
	dir := t.TempDir()
	board := filepath.Join(dir, "api", "board.html")
	return &HTMLServer{
		Token:     testToken,
		BoardPath: board,
		Scope:     ScopeLive,
		Name:      "api",
		Host:      testHost,
	}, dir
}

func doHTML(t *testing.T, s *HTMLServer, method, target, host, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	if token != "" {
		req.Header.Set("X-Relevo-Board-Token", token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func decodeBoardDoc(t *testing.T, w *httptest.ResponseRecorder) boardDoc {
	t.Helper()
	var doc boardDoc
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode board doc: %v (%s)", err, w.Body.String())
	}
	return doc
}

// TestHTMLBoardRequiresToken: the API is the only route that hands over the
// board, so a missing or wrong token is 401 and reveals nothing.
func TestHTMLBoardRequiresToken(t *testing.T) {
	s, _ := testHTMLServer(t)
	for _, token := range []string{"", "wrong-token"} {
		w := doHTML(t, s, http.MethodGet, "/api/board", testHost, token)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET with token %q = %d, want 401", token, w.Code)
		}
	}
}

// TestHTMLBoardForeignHostForbidden: the Host check runs ahead of every route,
// so a rebound name learns nothing about which of them exist.
func TestHTMLBoardForeignHostForbidden(t *testing.T) {
	s, _ := testHTMLServer(t)
	for _, target := range []string{"/", "/shell.js", "/api/board", "/opencode-120"} {
		for _, token := range []string{"", testToken} {
			w := doHTML(t, s, http.MethodGet, target, "evil.example", token)
			if w.Code != http.StatusForbidden {
				t.Errorf("GET %s from a foreign Host = %d, want 403", target, w.Code)
			}
		}
	}
}

// TestHTMLBoardMissingIsNew: a missing board.html is not an error. The shell
// needs the isNew flag to say "no board yet" instead of rendering nothing.
func TestHTMLBoardMissingIsNew(t *testing.T) {
	s, _ := testHTMLServer(t)
	w := doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", w.Code, w.Body.String())
	}
	doc := decodeBoardDoc(t, w)
	if !doc.IsNew || doc.Html != "" || doc.Etag != "" {
		t.Errorf("GET missing = %+v, want isNew with empty html and etag", doc)
	}
	if doc.Scope != ScopeLive || doc.Name != "api" {
		t.Errorf("GET missing = scope %q name %q, want live/api", doc.Scope, doc.Name)
	}
}

func TestHTMLBoardServesBytes(t *testing.T) {
	s, dir := testHTMLServer(t)
	body := "<h1>board</h1>"
	if err := os.MkdirAll(filepath.Dir(s.BoardPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.BoardPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write board: %v", err)
	}

	doc := decodeBoardDoc(t, doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken))
	if doc.IsNew || doc.Html != body {
		t.Errorf("doc = %+v, want the bytes and not isNew", doc)
	}
	if doc.Etag != Etag([]byte(body)) {
		t.Errorf("etag = %q, want %q", doc.Etag, Etag([]byte(body)))
	}
	if len(doc.External) != 0 {
		t.Errorf("external = %v, want none", doc.External)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Errorf("the GET created %d entries in the board dir, err %v; want none", len(entries), err)
	}
}

// TestHTMLBoardIsGetOnly: the board is a file the user edits, not an endpoint,
// so anything but GET is 405 with the method named.
func TestHTMLBoardIsGetOnly(t *testing.T) {
	s, _ := testHTMLServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := doHTML(t, s, method, "/api/board", testHost, testToken)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/board = %d, want 405", method, w.Code)
		}
		if allow := w.Header().Get("Allow"); allow != "GET" {
			t.Errorf("%s Allow = %q, want GET", method, allow)
		}
	}
}

// TestHTMLSecurityHeaders: the policy is the offline guarantee, so it is on
// every response and names no remote origin.
func TestHTMLSecurityHeaders(t *testing.T) {
	s, _ := testHTMLServer(t)
	for _, target := range []string{"/", "/shell.js", "/api/board"} {
		w := doHTML(t, s, http.MethodGet, target, testHost, testToken)
		csp := w.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Fatalf("GET %s: no CSP", target)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: no nosniff", target)
		}
		if w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("GET %s: no no-referrer", target)
		}
		for _, banned := range []string{"http://", "https://"} {
			if strings.Contains(csp, banned) {
				t.Errorf("GET %s: CSP names %s: %s", target, banned, csp)
			}
		}
	}
	if got := doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken).Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on /api/board = %q, want no-store", got)
	}
	if got := doHTML(t, s, http.MethodGet, "/", testHost, "").Header().Get("Cache-Control"); got != "" {
		t.Errorf("Cache-Control on the shell = %q, want none", got)
	}
}

// TestHTMLShellServedUnderOwnerSegment: the shell is at "/" and under one owner
// segment, and nothing deeper.
func TestHTMLShellServedUnderOwnerSegment(t *testing.T) {
	s, _ := testHTMLServer(t)
	for _, p := range []string{"/", "/opencode-120", "/opencode-120/"} {
		w := doHTML(t, s, http.MethodGet, p, testHost, "")
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q", p, ct)
		}
	}
	if w := doHTML(t, s, http.MethodGet, "/opencode-120/board", testHost, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET /opencode-120/board = %d, want 404", w.Code)
	}
}

// TestHTMLShellAssetsNeedNoToken: the page loads before the fragment carries a
// token, so the shell and its script must be readable without one.
func TestHTMLShellAssetsNeedNoToken(t *testing.T) {
	s, _ := testHTMLServer(t)
	for _, target := range []string{"/", "/shell.js", "/comments.js", "/overlay.js"} {
		if w := doHTML(t, s, http.MethodGet, target, testHost, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s without a token = %d, want 200", target, w.Code)
		}
	}
}

// TestHTMLBoardExternalPopulated: a board that names a remote load says so
// in the response, so the shell can banner it. The anchor href is not a load
// and rides along unreported.
func TestHTMLBoardExternalPopulated(t *testing.T) {
	s, _ := testHTMLServer(t)
	body := `<img src="https://cdn.example/logo.png"><a href="//cdn.example/docs">docs</a>`
	if err := os.MkdirAll(filepath.Dir(s.BoardPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.BoardPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write board: %v", err)
	}
	doc := decodeBoardDoc(t, doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken))
	want := []string{"https://cdn.example/logo.png"}
	if len(doc.External) != len(want) {
		t.Fatalf("external = %v, want %v", doc.External, want)
	}
	for i, ref := range want {
		if doc.External[i] != ref {
			t.Errorf("external[%d] = %q, want %q", i, doc.External[i], ref)
		}
	}
}

// TestHTMLBoardOversizedIsAnError: a board past the cap is a 500 naming the
// reason, never a partial read.
func TestHTMLBoardOversizedIsAnError(t *testing.T) {
	s, _ := testHTMLServer(t)
	if err := os.MkdirAll(filepath.Dir(s.BoardPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(s.BoardPath)
	if err != nil {
		t.Fatalf("create board: %v", err)
	}
	if err := f.Truncate(maxBody + 1); err != nil {
		t.Fatalf("grow board: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close board: %v", err)
	}
	w := doHTML(t, s, http.MethodGet, "/api/board", testHost, testToken)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("oversized board = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "cap") {
		t.Errorf("refusal %q does not name the cap", w.Body.String())
	}
}
