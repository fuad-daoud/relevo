package board

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const (
	testHost  = "127.0.0.1:43210"
	testToken = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
)

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	th, err := Lookup("cockpit")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	scene := filepath.Join(t.TempDir(), "board.excalidraw")
	return &Server{Token: testToken, ScenePath: scene, Theme: th, Host: testHost}, scene
}

func do(t *testing.T, s *Server, method, target, host, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	}
	req.Host = host
	if token != "" {
		req.Header.Set("X-Relevo-Board-Token", token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func putBody(scene, svg string) []byte {
	return []byte(`{"scene":` + scene + `,"svg":` + strconv.Quote(svg) + `}`)
}

func TestSceneRequiresToken(t *testing.T) {
	s, _ := testServer(t)
	for _, token := range []string{"", "wrong-token"} {
		w := do(t, s, http.MethodGet, "/api/scene", testHost, token, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET with token %q = %d, want 401", token, w.Code)
		}
		w = do(t, s, http.MethodPut, "/api/scene", testHost, token, putBody(sceneJSON, "<svg/>"))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("PUT with token %q = %d, want 401", token, w.Code)
		}
	}
}

func TestForeignHostForbidden(t *testing.T) {
	s, _ := testServer(t)
	w := do(t, s, http.MethodGet, "/api/scene", "evil.example", testToken, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("GET from a foreign Host = %d, want 403", w.Code)
	}
	w = do(t, s, http.MethodGet, "/assets/index.html", "evil.example", "", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("asset from a foreign Host = %d, want 403", w.Code)
	}
}

func TestOversizedBody(t *testing.T) {
	s, _ := testServer(t)
	body := bytes.Repeat([]byte(" "), maxBody+1)
	w := do(t, s, http.MethodPut, "/api/scene", testHost, testToken, body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized PUT = %d, want 413", w.Code)
	}
}

func TestInvalidBody(t *testing.T) {
	s, _ := testServer(t)
	cases := map[string][]byte{
		"not json":      []byte("nope"),
		"wrong scene":   putBody(`{"type":"other","elements":[]}`, "<svg/>"),
		"no elements":   putBody(`{"type":"excalidraw"}`, "<svg/>"),
		"elements null": putBody(`{"type":"excalidraw","elements":null}`, "<svg/>"),
		"bad svg":       putBody(sceneJSON, "<html></html>"),
	}
	for name, body := range cases {
		w := do(t, s, http.MethodPut, "/api/scene", testHost, testToken, body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: PUT = %d, want 422", name, w.Code)
		}
	}
}

func TestStaleIfMatchConflicts(t *testing.T) {
	s, scene := testServer(t)
	if err := os.WriteFile(scene, []byte(sceneJSON), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/scene", bytes.NewReader(putBody(sceneJSON, "<svg/>")))
	req.Host = testHost
	req.Header.Set("X-Relevo-Board-Token", testToken)
	req.Header.Set("If-Match", "deadbeef")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("stale If-Match = %d, want 409", w.Code)
	}
}

func TestPutReturnsNewEtag(t *testing.T) {
	s, scene := testServer(t)
	newScene := `{"type":"excalidraw","elements":[{"id":"a"}]}`
	w := do(t, s, http.MethodPut, "/api/scene", testHost, testToken, putBody(newScene, "<svg></svg>"))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200: %s", w.Code, w.Body.String())
	}
	var out etagDoc
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode etag: %v", err)
	}
	if want := Etag([]byte(newScene)); out.Etag != want {
		t.Errorf("etag = %q, want %q", out.Etag, want)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(scene), "board.svg")); err != nil {
		t.Errorf("companion svg was not written: %v", err)
	}

	got := do(t, s, http.MethodGet, "/api/scene", testHost, testToken, nil)
	var doc sceneDoc
	if err := json.Unmarshal(got.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode scene: %v", err)
	}
	if doc.IsNew || doc.Etag != out.Etag {
		t.Errorf("GET after PUT = isNew %v etag %q, want false/%q", doc.IsNew, doc.Etag, out.Etag)
	}
	if string(doc.Scene) != newScene {
		t.Errorf("GET scene = %s, want %s", doc.Scene, newScene)
	}
}

func TestGetMissingIsNew(t *testing.T) {
	s, _ := testServer(t)
	w := do(t, s, http.MethodGet, "/api/scene", testHost, testToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", w.Code)
	}
	var doc sceneDoc
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !doc.IsNew || doc.Etag != "" || string(doc.Scene) != "null" {
		t.Errorf("GET missing = %+v, want isNew/null/empty etag", doc)
	}
	if doc.Theme == nil || doc.Theme.Name != "cockpit" {
		t.Errorf("GET theme = %+v, want cockpit", doc.Theme)
	}
}

func TestAssetsAndPageNeedNoToken(t *testing.T) {
	s, _ := testServer(t)
	w := do(t, s, http.MethodGet, "/assets/index.html", testHost, "", nil)
	if w.Code != http.StatusOK {
		t.Errorf("GET /assets/index.html = %d, want 200", w.Code)
	}
	w = do(t, s, http.MethodGet, "/", testHost, "", nil)
	if w.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("GET / Content-Type = %q", ct)
	}
}

// TestPageServedUnderOwnerSegment pins the readable URL: the page is served at
// "/" and under one owner segment ("/<mastermind>"), and anything deeper is
// not the page.
func TestPageServedUnderOwnerSegment(t *testing.T) {
	s, _ := testServer(t)
	for _, p := range []string{"/", "/opencode-120", "/opencode-120/"} {
		w := do(t, s, http.MethodGet, p, testHost, "", nil)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, w.Code)
		}
	}
	if w := do(t, s, http.MethodGet, "/opencode-120/board", testHost, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("GET /opencode-120/board = %d, want 404", w.Code)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	s, _ := testServer(t)
	w := do(t, s, http.MethodGet, "/api/scene", testHost, testToken, nil)
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Error("no CSP on the api response")
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff on the api response")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("no no-store on /api")
	}
	w = do(t, s, http.MethodGet, "/", testHost, "", nil)
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Error("no CSP on the page response")
	}
}
