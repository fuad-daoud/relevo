package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestActorRequestsTheActorsRoute pins the probe's wire shape: the actor is a
// path segment, the pinned candidate travels as a query parameter, and both
// are escaped. A candidate the client omitted is omitted on the wire too, so
// the server answers with its own pick exactly as it would for a create.
func TestActorRequestsTheActorsRoute(t *testing.T) {
	t.Parallel()

	var paths, queries []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"actor":"builder","shape":"writer","accepted":true,"pick":"claude/test/m"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)

	view, err := cl.Actor(context.Background(), "zen", "builder", "claude/test/m")
	if err != nil {
		t.Fatalf("Actor: %v", err)
	}
	if view.Actor != "builder" || !view.Accepted || view.Pick != "claude/test/m" {
		t.Fatalf("view = %+v, want the served actor view", view)
	}

	if _, err := cl.Actor(context.Background(), "zen", "coder/v2", ""); err != nil {
		t.Fatalf("Actor without a pin: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("paths = %v, want two requests", paths)
	}
	if paths[0] != "/v1/actors/builder" {
		t.Errorf("path = %q, want /v1/actors/builder", paths[0])
	}
	if queries[0] != "candidate=claude%2Ftest%2Fm" {
		t.Errorf("query = %q, want the escaped candidate pin", queries[0])
	}
	if paths[1] != "/v1/actors/coder%2Fv2" {
		t.Errorf("path = %q, want the escaped actor", paths[1])
	}
	if queries[1] != "" {
		t.Errorf("query = %q, want none without a candidate pin", queries[1])
	}
}

// TestActorCarriesAHTTPErrorBody pins the refusal path a probe classifies on:
// a 401 not-enrolled answer comes back as an *HTTPError carrying the server's
// own code, never as a bare unreachable.
func TestActorCarriesAHTTPErrorBody(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"not_enrolled","message":"not enrolled"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)

	_, err := cl.Actor(context.Background(), "zen", "builder", "")
	var httpErr *HTTPError
	if err == nil || !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want an *HTTPError", err)
	}
	if httpErr.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", httpErr.Status)
	}
}
