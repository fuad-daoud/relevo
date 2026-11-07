package serve

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// burstVerifyEnv is a head with bursting on and a real client key, so
// WhoAmI and GetBinding to a test node sign and verify like production. A
// non-nil clock pins s.cfg.Now so a test can move time past the recheck
// backoff.
func burstVerifyEnv(t *testing.T, kp remote.Keypair, hookEvents *countingHooks, clock *time.Time) *testEnv {
	t.Helper()
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	return setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		if hookEvents != nil {
			cfg.Hooks = hookEvents
		}
		if clock != nil {
			cfg.Now = func() time.Time { return *clock }
		}
	})
}

// startBurstNode runs a real serve server over httptest TLS: the head's
// client talks to it with the production client, no stub in between. A
// non-nil enroll key is enrolled on the node; nil leaves the head unknown,
// so the node answers 401 not_enrolled.
func startBurstNode(t *testing.T, enroll *remote.Keypair) (nodeSrv *Server, ts *httptest.Server, fp string) {
	t.Helper()
	nodeDB := testServeDB(t)
	nodeRoot := t.TempDir()
	fp, err := InitTLS(SecretStore{DB: nodeDB}, []string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatalf("node InitTLS: %v", err)
	}
	cert, err := LoadTLS(SecretStore{DB: nodeDB})
	if err != nil {
		t.Fatalf("node LoadTLS: %v", err)
	}
	nodeSrv, err = New(Config{
		DB: nodeDB, Root: nodeRoot, Now: time.Now,
		Candidates: mustBuilderCandidates(t), Audiences: []string{fp},
	})
	if err != nil {
		t.Fatalf("node New: %v", err)
	}
	if enroll != nil {
		if _, err := nodeSrv.clients.Add("head", remote.MarshalPublic(enroll.Public, "head"), "", time.Now()); err != nil {
			t.Fatalf("enroll head on node: %v", err)
		}
		trackOwnerTmp(t, nodeSrv, remote.IDOf(enroll.Public))
	}
	ts = httptest.NewUnstartedServer(nodeSrv.Handler())
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return nodeSrv, ts, fp
}

func mustBuilderCandidates(t *testing.T) *candidate.Set {
	t.Helper()
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// saveHeadForward stores a head binding with a sending forward, the shape
// placeForwards writes before lane C sends it.
func saveHeadForward(t *testing.T, env *testEnv, name, node, nodeBinding string, round int, since time.Time) {
	t.Helper()
	cwd := filepath.Join(t.TempDir(), name+"-wt")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	b := store.Binding{
		Name: name, CWD: cwd, State: store.StateActive, Round: round,
		BuilderCandidate: "claude/anthropic/haiku",
		Burst: &store.BurstFacts{Forward: &store.Forward{
			Node: node, NodeBinding: nodeBinding, Round: round,
			Phase: "sending", ClaimedAt: since.Add(time.Minute), QueuedSince: since,
		}},
	}
	if err := env.runtime(t).Store.Save(b); err != nil {
		t.Fatalf("save head forward %s: %v", name, err)
	}
}

// burstRowFor pins one table row for a node test server: ready and
// unverified, the state a merge leaves a newly offered id in.
func burstRowFor(id, url, fp string) burst.NodeRow {
	return burst.NodeRow{
		ID: id, Name: id + " cx43@hel1", URL: url,
		Fingerprint: fp, Cap: 2, State: "ready",
		LastSeen: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
	}
}

// TestBurstVerifyFingerprint pins the impersonator path: a node presenting
// a certificate the pin does not name stays unverified, carries
// Problem=fingerprint, and dispatches one burst_security hook -- still one
// after a re-dial past the backoff window, since a standing mismatch is not
// a new event. A draining row is not dialed at all: it can take no forward,
// so checking it would only cry wolf on a node being torn down.
func TestBurstVerifyFingerprint(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	hookEvents := &countingHooks{}
	env := burstVerifyEnv(t, headKP, hookEvents, &clock)
	_, nodeTS, _ := startBurstNode(t, nil)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
		burstRowFor("b1", nodeTS.URL, burstTestFingerprint),
		func() burst.NodeRow {
			r := burstRowFor("b9", nodeTS.URL, burstTestFingerprint)
			r.State = "draining"
			return r
		}(),
	}}

	env.srv.verifyNodes(context.Background())

	rows := env.srv.copyBurstTable().Nodes
	if rows[0].Verified || rows[0].Problem != burst.ReportProblemFingerprint {
		t.Errorf("b1 = verified %v problem %q, want unverified with problem fingerprint", rows[0].Verified, rows[0].Problem)
	}
	if got := hookEvents.count(hooks.EventBurstSecurity); got != 1 {
		t.Errorf("burst_security dispatches = %d, want 1", got)
	}
	if rows[1].Problem != "" || rows[1].Verified {
		t.Errorf("draining b9 = verified %v problem %q, want untouched", rows[1].Verified, rows[1].Problem)
	}

	// Past the backoff the row is re-dialled; the standing mismatch must
	// still not re-fire the hook.
	clock = clock.Add(verifyRecheckAfter + time.Second)
	env.srv.verifyNodes(context.Background())
	if got := hookEvents.count(hooks.EventBurstSecurity); got != 1 {
		t.Errorf("burst_security dispatches after a re-dial = %d, want still 1", got)
	}
}

// TestBurstVerifyKeyNotEnrolled pins the key path: a node that answers over
// the pinned connection but never enrolled this head leaves the row
// unverified with Problem=key and no security hook -- a refusal, not an
// attack.
func TestBurstVerifyKeyNotEnrolled(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	hookEvents := &countingHooks{}
	env := burstVerifyEnv(t, headKP, hookEvents, nil)
	_, nodeTS, nodeFP := startBurstNode(t, nil)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
		burstRowFor("b1", nodeTS.URL, nodeFP),
	}}

	env.srv.verifyNodes(context.Background())

	row := env.srv.copyBurstTable().Nodes[0]
	if row.Verified || row.Problem != burst.ReportProblemKey {
		t.Errorf("b1 = verified %v problem %q, want unverified with problem key", row.Verified, row.Problem)
	}
	if got := hookEvents.count(hooks.EventBurstSecurity); got != 0 {
		t.Errorf("burst_security dispatches = %d, want none for a key refusal", got)
	}
}

// TestBurstVerifyUnreachableRetried pins the dropped-exchange path: a node
// answering 5xx stays unverified with Problem=unreachable, and once the
// recheck backoff passes it is dialled again -- one hit per pass, retried
// rather than stuck. The 500 answers without the gateway wrapper so the
// client does not retry inside the pass; the ErrUnreachable arm itself is
// pinned by the pure classifier test.
func TestBurstVerifyUnreachableRetried(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := burstVerifyEnv(t, headKP, nil, &clock)
	var hits atomic.Int64
	nodeTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(nodeTS.Close)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
		burstRowFor("b1", nodeTS.URL, burstTestFingerprint),
	}}

	env.srv.verifyNodes(context.Background())
	row := env.srv.copyBurstTable().Nodes[0]
	if row.Verified || row.Problem != burst.ReportProblemUnreachable {
		t.Errorf("b1 = verified %v problem %q, want unverified with problem unreachable", row.Verified, row.Problem)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("node hits after one pass = %d, want 1", got)
	}

	clock = clock.Add(verifyRecheckAfter + time.Second)
	env.srv.verifyNodes(context.Background())
	row = env.srv.copyBurstTable().Nodes[0]
	if row.Verified || row.Problem != burst.ReportProblemUnreachable {
		t.Errorf("b1 after a second pass = verified %v problem %q, want still unverified and unreachable", row.Verified, row.Problem)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("node hits after two passes = %d, want 2: the failure must not stick", got)
	}
}

// TestBurstVerifySurvivesSameURLMerge pins the carry-through: a node that
// answers WhoAmI over its pinned connection verifies, and a merge that
// keeps its URL and fingerprint keeps Verified true -- while a merge that
// changes the URL clears it, which is why verify re-checks both before
// applying.
func TestBurstVerifySurvivesSameURLMerge(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := burstVerifyEnv(t, headKP, nil, nil)
	_, nodeTS, nodeFP := startBurstNode(t, &headKP)
	row := burstRowFor("b1", nodeTS.URL, nodeFP)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}

	env.srv.verifyNodes(context.Background())
	if got := env.srv.copyBurstTable().Nodes[0]; !got.Verified || got.Problem != "" {
		t.Fatalf("b1 after WhoAmI = verified %v problem %q, want verified and clear", got.Verified, got.Problem)
	}

	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	same := burst.ReportResponse{Nodes: []burst.ReportNodeOut{{
		ID: "b1", Name: "b1 cx43@hel1", URL: nodeTS.URL,
		Fingerprint: nodeFP, Cap: 2, State: "ready",
	}}}
	env.srv.burstTable.Merge(at, same)
	if got := env.srv.copyBurstTable().Nodes[0]; !got.Verified {
		t.Errorf("b1 after a same-URL merge = %+v, want Verified carried", got)
	}

	moved := burst.ReportResponse{Nodes: []burst.ReportNodeOut{{
		ID: "b1", Name: "b1 cx43@hel1", URL: "https://b1-moved.example",
		Fingerprint: nodeFP, Cap: 2, State: "ready",
	}}}
	env.srv.burstTable.Merge(at, moved)
	if got := env.srv.copyBurstTable().Nodes[0]; got.Verified {
		t.Errorf("b1 after a changed-URL merge = %+v, want Verified cleared", got)
	}
}

// TestBurstVerifyProblemMapping pins every arm of the pure classifier,
// including the ones no test server produces: a 403 refusal is a key
// problem like a 401, and a 404 or an unknown failure leaves the row alone
// for the next cycle.
func TestBurstVerifyProblemMapping(t *testing.T) {
	httpErr := func(status int, code remote.Code) error {
		return &client.HTTPError{Status: status, Body: remote.ErrorBody{Code: code}}
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"pin mismatch", client.ErrCertChanged, burst.ReportProblemFingerprint},
		{"wrapped pin mismatch", errors.New("tls: " + client.ErrCertChanged.Error()), burst.ReportProblemFingerprint},
		{"401 not enrolled", httpErr(401, remote.CodeNotEnrolled), burst.ReportProblemKey},
		{"401 other code", httpErr(401, remote.CodeBadSignature), burst.ReportProblemKey},
		{"403", httpErr(403, remote.Code("forbidden")), burst.ReportProblemKey},
		{"500", httpErr(500, remote.Code("internal")), burst.ReportProblemUnreachable},
		{"gateway wrapped", errBurstWrapUnreachable(), burst.ReportProblemUnreachable},
		{"plain unreachable", client.ErrUnreachable, burst.ReportProblemUnreachable},
		{"404 leaves the row alone", httpErr(404, remote.CodeNotFound), ""},
		{"wrong audience leaves the row alone", client.ErrWrongAudience, ""},
		{"unknown failure leaves the row alone", errors.New("boom"), ""},
		{"no failure names no problem", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := burstVerifyProblem(c.err); got != c.want {
				t.Errorf("burstVerifyProblem(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// errBurstWrapUnreachable is an unreachable failure under another message,
// the shape a gateway status takes after the client's response mapping.
func errBurstWrapUnreachable() error {
	return fmt.Errorf("server returned 503 Service Unavailable: %w", client.ErrUnreachable)
}

// TestBurstVerifyOffAndBadKey pins the two guard arms: with bursting off
// the table is untouched, and an unparsable client key skips the check
// rather than failing the tick.
func TestBurstVerifyOffAndBadKey(t *testing.T) {
	t.Run("bursting off leaves the table alone", func(t *testing.T) {
		env := setupTestEnv(t, func(cfg *Config) { cfg.Burst = burstTestPolicy() })
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
			burstRowFor("b1", "http://127.0.0.1:1", burstTestFingerprint),
		}}
		env.srv.verifyNodes(context.Background())
		row := env.srv.copyBurstTable().Nodes[0]
		if row.Verified || row.Problem != "" {
			t.Errorf("b1 = verified %v problem %q, want untouched while bursting is off", row.Verified, row.Problem)
		}
	})
	t.Run("unparsable key skips the check", func(t *testing.T) {
		env := setupTestEnv(t, func(cfg *Config) {
			cfg.Burst = burstTestPolicy()
			cfg.ClientKey = []byte("not a key")
			cfg.Provider = &recordingBurstProvider{}
		})
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
			burstRowFor("b1", "http://127.0.0.1:1", burstTestFingerprint),
		}}
		env.srv.verifyNodes(context.Background())
		row := env.srv.copyBurstTable().Nodes[0]
		if row.Verified || row.Problem != "" {
			t.Errorf("b1 = verified %v problem %q, want untouched with an unparsable key", row.Verified, row.Problem)
		}
	})
}

// TestBurstVerifyStandingProblemRecheckBackoff pins the recheck pacing: a
// node carrying a fingerprint problem is dialled once across two rapid
// checks and re-dialled only after the backoff passes; a re-dial keeps the
// problem's first-failure stamp, and the check failure logs once -- on the
// transition, not every pass. The stub answers the pin-mismatch message as
// an HTTP failure so every dial is counted at the handler, which a real TLS
// pin mismatch would eat before the request line.
func TestBurstVerifyStandingProblemRecheckBackoff(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	hookEvents := &countingHooks{}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	env := burstVerifyEnv(t, headKP, hookEvents, &clock)
	var hits atomic.Int64
	url, fp := startStubNode(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writeJSON(w, http.StatusInternalServerError, remote.ErrorBody{
			Code: remote.Code("internal"), Message: client.ErrCertChanged.Error(),
		})
	}))
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
		burstRowFor("b1", url, fp),
	}}

	env.srv.verifyNodes(context.Background())
	first := env.srv.copyBurstTable().Nodes[0]
	if first.Problem != burst.ReportProblemFingerprint || !first.ProblemSince.Equal(clock) {
		t.Fatalf("b1 = problem %q since %v, want fingerprint stamped at %v", first.Problem, first.ProblemSince, clock)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("node hits after the first check = %d, want 1", got)
	}

	// A rapid second check inside the window leaves the standing problem
	// alone: no dial, no log, no hook.
	clock = clock.Add(30 * time.Second)
	env.srv.verifyNodes(context.Background())
	if got := hits.Load(); got != 1 {
		t.Errorf("node hits inside the backoff = %d, want the standing problem not re-dialled", got)
	}

	// Past the window the node is re-dialled, but the standing problem
	// keeps its first-failure stamp and says nothing new.
	clock = clock.Add(31 * time.Second)
	env.srv.verifyNodes(context.Background())
	if got := hits.Load(); got != 2 {
		t.Errorf("node hits past the backoff = %d, want 2: the problem is re-checked, not stuck", got)
	}
	row := env.srv.copyBurstTable().Nodes[0]
	if !row.ProblemSince.Equal(first.ProblemSince) {
		t.Errorf("ProblemSince = %v, want the first failure %v kept", row.ProblemSince, first.ProblemSince)
	}
	if got := strings.Count(logs.String(), "node check failed"); got != 1 {
		t.Errorf("check-failure log lines = %d, want 1: only the transition logs", got)
	}
	if got := hookEvents.count(hooks.EventBurstSecurity); got != 1 {
		t.Errorf("burst_security dispatches = %d, want 1: a standing mismatch fires once", got)
	}
}
