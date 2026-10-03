package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestCreateCheckHitsRoute pins CreateCheck's method, path and body escaping.
//
// Mutation: swap the check path segment (/v1/bindings/.../checks -> /check) and
// the path assertion fails.
func TestCreateCheckHitsRoute(t *testing.T) {
	var mu sync.Mutex
	var gotMethod, gotPath, gotBody string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotMethod, gotPath, gotBody = r.Method, r.URL.EscapedPath(), string(body)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chk-1","command":"make check","step":"test","result":"pass"}`))
	}))
	defer ts.Close()

	cl := testClient(t, ts)
	req := remote.CreateCheckRequest{ID: "chk-1", Command: "make check", Step: "test"}
	view, err := cl.CreateCheck(context.Background(), "zen", "a b/c", req)
	if err != nil {
		t.Fatalf("CreateCheck: %v", err)
	}
	if view.ID != "chk-1" || view.Result != "pass" {
		t.Fatalf("view = %+v, want ID chk-1, Result pass", view)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodPost || gotPath != "/v1/bindings/a%20b%2Fc/checks" {
		t.Fatalf("request = %s %s, want POST /v1/bindings/a%%20b%%2Fc/checks", gotMethod, gotPath)
	}
	wantBody := `{"id":"chk-1","command":"make check","step":"test"}`
	if gotBody != wantBody {
		t.Fatalf("body = %q, want %q", gotBody, wantBody)
	}
}

// TestGetCheckHitsRoute pins GetCheck's method, path and parameter escaping.
func TestGetCheckHitsRoute(t *testing.T) {
	var mu sync.Mutex
	var gotMethod, gotPath string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotMethod, gotPath = r.Method, r.URL.EscapedPath()
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chk/1","command":"make check","step":"test","result":"pass"}`))
	}))
	defer ts.Close()

	cl := testClient(t, ts)
	view, err := cl.GetCheck(context.Background(), "zen", "a b/c", "chk/1")
	if err != nil {
		t.Fatalf("GetCheck: %v", err)
	}
	if view.ID != "chk/1" || view.Result != "pass" {
		t.Fatalf("view = %+v, want ID chk/1, Result pass", view)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodGet || gotPath != "/v1/bindings/a%20b%2Fc/checks/chk%2F1" {
		t.Fatalf("request = %s %s, want GET /v1/bindings/a%%20b%%2Fc/checks/chk%%2F1", gotMethod, gotPath)
	}
}

// TestSetGateHitsRoute pins SetGate's method, path and body escaping.
func TestSetGateHitsRoute(t *testing.T) {
	var mu sync.Mutex
	var gotMethod, gotPath, gotBody string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotMethod, gotPath, gotBody = r.Method, r.URL.EscapedPath(), string(body)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	cl := testClient(t, ts)
	gate := "make test"
	regate := 3
	req := remote.SetGateRequest{Gate: &gate, Regate: &regate}
	if err := cl.SetGate(context.Background(), "zen", "a b/c", req); err != nil {
		t.Fatalf("SetGate: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodPost || gotPath != "/v1/bindings/a%20b%2Fc/gate" {
		t.Fatalf("request = %s %s, want POST /v1/bindings/a%%20b%%2Fc/gate", gotMethod, gotPath)
	}
	wantBody := `{"gate":"make test","regate":3}`
	if gotBody != wantBody {
		t.Fatalf("body = %q, want %q", gotBody, wantBody)
	}
}

// TestCreateCheck409DecodesCheckRunning pins the decoding of a 409 conflict
// response carrying CodeCheckRunning.
//
// Mutation: return nil on a 409 in CreateCheck and this test fails.
func TestCreateCheck409DecodesCheckRunning(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"check_running","message":"check is already running on binding"}`))
	}))
	defer ts.Close()

	cl := testClient(t, ts)
	_, err := cl.CreateCheck(context.Background(), "zen", "test-binding", remote.CreateCheckRequest{
		ID:      "chk-2",
		Command: "make check",
	})
	if err == nil {
		t.Fatal("CreateCheck err = nil, want 409 conflict error")
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v (%T), want *HTTPError", err, err)
	}
	if httpErr.Status != http.StatusConflict {
		t.Errorf("status = %d, want 409", httpErr.Status)
	}
	if httpErr.Body.Code != remote.CodeCheckRunning {
		t.Errorf("code = %q, want %q", httpErr.Body.Code, remote.CodeCheckRunning)
	}
}

// TestCheckAndGate404SurfacesAsHTTPError pins that an old server without the
// check or gate routes answers 404 which surfaces as *HTTPError unchanged.
func TestCheckAndGate404SurfacesAsHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found","message":"route not found"}`))
	}))
	defer ts.Close()

	cl := testClient(t, ts)
	ctx := context.Background()

	_, err := cl.CreateCheck(ctx, "zen", "test-binding", remote.CreateCheckRequest{ID: "chk-1"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Errorf("CreateCheck err = %v, want *HTTPError 404", err)
	}

	_, err = cl.GetCheck(ctx, "zen", "test-binding", "chk-1")
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Errorf("GetCheck err = %v, want *HTTPError 404", err)
	}

	err = cl.SetGate(ctx, "zen", "test-binding", remote.SetGateRequest{})
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Errorf("SetGate err = %v, want *HTTPError 404", err)
	}
}
