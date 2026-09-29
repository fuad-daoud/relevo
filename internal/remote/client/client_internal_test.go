// This file is package client, not client_test: it replaces the unexported retry
// backoff and download deadline, which an external test cannot reach.
package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

func testClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return New(remote.Servers{"zen": remote.ServerEntry{URL: ts.URL, Insecure: true}}, kp, time.Now)
}

// injectSleep replaces the package's backoff sleep for one test and reports
// what was asked for.
func injectSleep(t *testing.T) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var slept []time.Duration
	orig := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		slept = append(slept, d)
		return nil
	}
	t.Cleanup(func() { sleep = orig })
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), slept...)
	}
}

type retryCase struct {
	name         string
	status       int
	succeedAfter int // answer 200 from this attempt on; 0 never succeeds
	call         func(*Client, context.Context) error
	wantAttempts int
	wantSleeps   []time.Duration
	wantErr      error
	wantInErr    []string
	wantNoHTML   bool
}

// retryPolicyCases pins each covered call's retry behaviour; wantSleeps is exact.
func retryPolicyCases() []retryCase {
	whoAmI := func(cl *Client, ctx context.Context) error {
		_, err := cl.WhoAmI(ctx, "zen")
		return err
	}
	getBinding := func(cl *Client, ctx context.Context) error {
		_, err := cl.GetBinding(ctx, "zen", "api")
		return err
	}
	return []retryCase{
		{
			name:         "a gateway body is an unreachable, never the CDN HTML",
			status:       http.StatusBadGateway,
			call:         whoAmI,
			wantAttempts: retryAttempts,
			wantSleeps:   []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
			wantErr:      ErrUnreachable,
			wantInErr:    []string{"502"},
			wantNoHTML:   true,
		},
		{
			name:         "a later attempt that succeeds ends the retry",
			status:       http.StatusServiceUnavailable,
			succeedAfter: 2,
			call:         getBinding,
			wantAttempts: 3,
			wantSleeps:   []time.Duration{time.Second, 2 * time.Second},
		},
		{
			name:         "attempts are exhausted at retryAttempts",
			status:       http.StatusBadGateway,
			call:         whoAmI,
			wantAttempts: retryAttempts,
			wantSleeps:   []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
			wantErr:      ErrUnreachable,
		},
		{
			name:         "Done is not idempotent and is never retried",
			status:       http.StatusBadGateway,
			call:         func(cl *Client, ctx context.Context) error { return cl.Done(ctx, "zen", "api") },
			wantAttempts: 1,
			wantErr:      ErrUnreachable,
		},
		{
			name:   "StartRound without the idempotent flag is one attempt",
			status: http.StatusBadGateway,
			call: func(cl *Client, ctx context.Context) error {
				_, err := cl.StartRound(ctx, "zen", "api", 1, []byte("# Plan"), nil, "", "", nil, false)
				return err
			},
			wantAttempts: 1,
			wantErr:      ErrUnreachable,
		},
	}
}

func TestRetryPolicy(t *testing.T) {
	for _, tc := range retryPolicyCases() {
		t.Run(tc.name, func(t *testing.T) {
			handler, attempts := flakyGateway(tc)
			ts := httptest.NewServer(handler)
			defer ts.Close()
			cl := testClient(t, ts)
			slept := injectSleep(t)

			err := tc.call(cl, context.Background())
			checkRetryOutcome(t, tc, err, attempts(), slept())
		})
	}
}

func flakyGateway(tc retryCase) (http.HandlerFunc, func() int) {
	var mu sync.Mutex
	attempts := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if tc.succeedAfter > 0 && n > tc.succeedAfter {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"api","round_state":"idle"}`))
			return
		}
		w.WriteHeader(tc.status)
		_, _ = w.Write([]byte("<html>" + strconv.Itoa(tc.status) + "</html>"))
	}
	return handler, func() int {
		mu.Lock()
		defer mu.Unlock()
		return attempts
	}
}

func checkRetryOutcome(t *testing.T, tc retryCase, err error, attempts int, sleeps []time.Duration) {
	t.Helper()
	if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, tc.wantErr)
	}
	if tc.wantErr == nil && err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if tc.wantNoHTML && strings.Contains(err.Error(), "<html") {
		t.Fatalf("err = %q, want no HTML body in the error text", err)
	}
	for _, want := range tc.wantInErr {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to contain %q", err, want)
		}
	}
	if attempts != tc.wantAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, tc.wantAttempts)
	}
	if !slices.Equal(sleeps, tc.wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", sleeps, tc.wantSleeps)
	}
}

// TestStartRoundRetryReReadsTheSpooledBody pins a retried StartRound: the
// second request carries the first's plan and bundle bytes.
//
// Mutation: drop startRoundAttempt's Seek(0, SeekStart) and they come out empty.
func TestStartRoundRetryReReadsTheSpooledBody(t *testing.T) {
	type seen struct{ plan, bundle string }
	var mu sync.Mutex
	var reqs []seen
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan := r.FormValue("plan")
		bundle := ""
		if f, _, err := r.FormFile("bundle"); err == nil {
			b, _ := io.ReadAll(f)
			bundle = string(b)
			_ = f.Close()
		}
		mu.Lock()
		reqs = append(reqs, seen{plan: plan, bundle: bundle})
		n := len(reqs)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html>502</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"api","round_state":"running"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	injectSleep(t)

	plan := []byte("# Round 1 plan\n")
	bundle := []byte("bundle-bytes-0123456789")
	view, err := cl.StartRound(context.Background(), "zen", "api", 1, plan, bytes.NewReader(bundle), "", "", nil, true)
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if view.Name != "api" {
		t.Fatalf("view.Name = %q, want api", view.Name)
	}

	mu.Lock()
	got := append([]seen(nil), reqs...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2 (one 502, one retry)", len(got))
	}
	if got[1].plan != string(plan) {
		t.Fatalf("retry plan = %q, want %q", got[1].plan, string(plan))
	}
	if got[1].bundle != string(bundle) {
		t.Fatalf("retry bundle = %q, want %q", got[1].bundle, string(bundle))
	}
}

func TestClientVersionHeader(t *testing.T) {
	var mu sync.Mutex
	var heads []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		heads = append(heads, r.Header.Get(remote.HeaderClientVersion))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"api"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)

	old := Version
	Version = "v9.9.9-test"
	t.Cleanup(func() { Version = old })

	ctx := context.Background()
	if _, err := cl.WhoAmI(ctx, "zen"); err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if _, err := cl.GetBinding(ctx, "zen", "api"); err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if _, err := cl.Ack(ctx, "zen", "api", 1); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), heads...)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("requests = %d, want 3", len(got))
	}
	for i, h := range got {
		if h != "v9.9.9-test" {
			t.Fatalf("request %d header %s = %q, want the client's version on every request", i, remote.HeaderClientVersion, h)
		}
	}
}

// TestRoundFileDeadline pins RoundFile's per-attempt deadline: a server slower
// than the test-shortened deadline is cut short with a retryable ErrUnreachable.
//
// Mutation: drop the WithTimeout from RoundFile and the call succeeds.
func TestRoundFileDeadline(t *testing.T) {
	const slowServerFallback = 10 * time.Second

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return // client gave up: no reply
		case <-time.After(slowServerFallback):
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("report\n"))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	slept := injectSleep(t)

	old := roundFileDeadline
	roundFileDeadline = 50 * time.Millisecond
	t.Cleanup(func() { roundFileDeadline = old })

	start := time.Now()
	rc, err := cl.RoundFile(context.Background(), "zen", "api", 1, "report")
	elapsed := time.Since(start)
	if rc != nil {
		_ = rc.Close()
	}
	if err == nil {
		t.Fatal("RoundFile against a server slower than the deadline succeeded")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want it to wrap ErrUnreachable", err)
	}
	if got := slept(); len(got) != retryAttempts-1 {
		t.Fatalf("sleeps = %v, want %d backoffs", got, retryAttempts-1)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("RoundFile took %s: the deadline did not cut the call short", elapsed)
	}
}

func TestRoundFileFrom(t *testing.T) {
	t.Run("headers present", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/bindings/api/rounds/1/files/log" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			if r.URL.Query().Get("from") != "10" {
				t.Fatalf("unexpected from query: %s", r.URL.Query().Get("from"))
			}
			w.Header().Set(remote.HeaderFileSize, "42")
			w.Header().Set(remote.HeaderFileFrom, "10")
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("remainder"))
		}))
		defer ts.Close()

		cl := testClient(t, ts)
		rc, fr, err := cl.RoundFileFrom(context.Background(), "zen", "api", 1, "log", 10)
		if err != nil {
			t.Fatalf("RoundFileFrom: %v", err)
		}
		defer func() { _ = rc.Close() }()

		if !fr.Honored {
			t.Fatal("fr.Honored = false, want true")
		}
		if fr.From != 10 {
			t.Fatalf("fr.From = %d, want 10", fr.From)
		}
		if fr.Size != 42 {
			t.Fatalf("fr.Size = %d, want 42", fr.Size)
		}
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(body) != "remainder" {
			t.Fatalf("body = %q, want remainder", string(body))
		}
	})

	t.Run("headers absent", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("whole file"))
		}))
		defer ts.Close()

		cl := testClient(t, ts)
		rc, fr, err := cl.RoundFileFrom(context.Background(), "zen", "api", 1, "log", 10)
		if err != nil {
			t.Fatalf("RoundFileFrom: %v", err)
		}
		defer func() { _ = rc.Close() }()

		if fr.Honored {
			t.Fatal("fr.Honored = true, want false")
		}
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(body) != "whole file" {
			t.Fatalf("body = %q, want whole file", string(body))
		}
	})
}
