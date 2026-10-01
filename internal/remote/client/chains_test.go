package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestCreateChainSpoolsChainAndBundle pins the create's form: one "chain"
// JSON field equal to the request, and the bundle bytes under "bundle".
//
// Mutation: drop copyBundleField from spoolCreateChain and the bundle is empty.
func TestCreateChainSpoolsChainAndBundle(t *testing.T) {
	var mu sync.Mutex
	var gotChain, gotBundle, gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chain := r.FormValue("chain")
		bundle := ""
		if f, _, err := r.FormFile("bundle"); err == nil {
			b, _ := io.ReadAll(f)
			bundle = string(b)
			_ = f.Close()
		}
		mu.Lock()
		gotChain, gotBundle = chain, bundle
		gotMethod, gotPath = r.Method, r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"test-chain","status":"running"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)

	req := remote.CreateChainRequest{Name: "test-chain", RepoID: "repo-id", BaseCommit: "base-commit"}
	bundle := []byte("bundle-bytes-0123456789")
	view, err := cl.CreateChain(context.Background(), "zen", req, bytes.NewReader(bundle))
	if err != nil {
		t.Fatalf("CreateChain: %v", err)
	}
	if view.Name != "test-chain" {
		t.Fatalf("view.Name = %q, want test-chain", view.Name)
	}

	wantJSON, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodPost || gotPath != "/v1/chains" {
		t.Fatalf("request = %s %s, want POST /v1/chains", gotMethod, gotPath)
	}
	if gotChain != string(wantJSON) {
		t.Fatalf("chain field = %q, want %q", gotChain, string(wantJSON))
	}
	if gotBundle != string(bundle) {
		t.Fatalf("bundle part = %q, want %q", gotBundle, string(bundle))
	}
}

// TestCreateChainRetryReReadsTheSpooledBody pins a retried create: the second
// request carries the first's chain JSON and bundle bytes.
//
// Mutation: build the body once without Seek(0, SeekStart) and the retry is
// empty.
func TestCreateChainRetryReReadsTheSpooledBody(t *testing.T) {
	type seen struct{ chain, bundle string }
	var mu sync.Mutex
	var reqs []seen
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chain := r.FormValue("chain")
		bundle := ""
		if f, _, err := r.FormFile("bundle"); err == nil {
			b, _ := io.ReadAll(f)
			bundle = string(b)
			_ = f.Close()
		}
		mu.Lock()
		reqs = append(reqs, seen{chain: chain, bundle: bundle})
		n := len(reqs)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html>502</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"test-chain","status":"running"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	injectSleep(t)

	req := remote.CreateChainRequest{Name: "test-chain", RepoID: "repo-id", BaseCommit: "base-commit"}
	bundle := []byte("bundle-bytes-0123456789")
	if _, err := cl.CreateChain(context.Background(), "zen", req, bytes.NewReader(bundle)); err != nil {
		t.Fatalf("CreateChain: %v", err)
	}

	wantJSON, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	mu.Lock()
	got := append([]seen(nil), reqs...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2 (one 502, one retry)", len(got))
	}
	if got[1].chain != string(wantJSON) {
		t.Fatalf("retry chain = %q, want %q", got[1].chain, string(wantJSON))
	}
	if got[1].bundle != string(bundle) {
		t.Fatalf("retry bundle = %q, want %q", got[1].bundle, string(bundle))
	}
}

// TestChainVerbsHitTheirRoutes pins each chain verb's method and path, with a
// name that must be path-escaped.
func TestChainVerbsHitTheirRoutes(t *testing.T) {
	type hit struct{ method, path, body string }
	var mu sync.Mutex
	var hits []hit
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, hit{method: r.Method, path: r.URL.EscapedPath(), body: string(body)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"a b/c","status":"running"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	ctx := context.Background()
	name := "a b/c"

	if _, err := cl.GetChain(ctx, "zen", name); err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if _, err := cl.ChainStop(ctx, "zen", name); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if _, err := cl.ChainResume(ctx, "zen", name, remote.ChainResumeRequest{}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if err := cl.ChainDone(ctx, "zen", name); err != nil {
		t.Fatalf("ChainDone: %v", err)
	}

	want := []hit{
		{http.MethodGet, "/v1/chains/a%20b%2Fc", ""},
		{http.MethodPost, "/v1/chains/a%20b%2Fc/stop", ""},
		{http.MethodPost, "/v1/chains/a%20b%2Fc/resume", "{}"},
		{http.MethodPost, "/v1/chains/a%20b%2Fc/done", ""},
	}
	mu.Lock()
	got := append([]hit(nil), hits...)
	mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("hits = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hit %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
