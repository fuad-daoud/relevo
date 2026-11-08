package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// gateFanoutEnv is a head with bursting on and one stub row per script, the
// shape a gate fan-out dials from. The clock the test drives is returned
// through the pointer.
func gateFanoutEnv(t *testing.T, scripts []*nodeScript, clock *time.Time) (*testEnv, []burst.NodeRow) {
	t.Helper()
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return *clock }
	})
	rows := make([]burst.NodeRow, len(scripts))
	for i, s := range scripts {
		url, fp := startStubNode(t, s.serve())
		rows[i] = stubRow(t, fmt.Sprintf("b%d", i+1), url, fp)
	}
	env.srv.burstTable = &burst.NodeTable{Nodes: rows}
	return env, rows
}

// gateCallResult is one background gate request's answer.
type gateCallResult struct {
	resp *http.Response
	body []byte
	err  error
}

// postGateAsync posts body to path in the background, so the test can read
// the head's answer while a node holds its own: a head that waited on the
// node never answers in time. The request is built on the test goroutine;
// only the round trip runs behind it.
func postGateAsync(t *testing.T, env *testEnv, path string, body []byte) <-chan gateCallResult {
	t.Helper()
	req := signedRequest(t, env.kp, "POST", path, body)
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(env.ts.URL, "http://")
	out := make(chan gateCallResult, 1)
	go func() {
		var res gateCallResult
		res.resp, res.err = http.DefaultClient.Do(req)
		if res.err == nil {
			defer func() { _ = res.resp.Body.Close() }()
			res.body, res.err = io.ReadAll(res.resp.Body)
		}
		out <- res
	}()
	return out
}

// awaitGateCall reads one background answer, failing when the head held it
// past the bound instead of answering around its nodes.
func awaitGateCall(t *testing.T, call <-chan gateCallResult, what string) (int, []byte) {
	t.Helper()
	select {
	case res := <-call:
		if res.err != nil {
			t.Fatalf("%s: %v", what, res.err)
		}
		return res.resp.StatusCode, res.body
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: the head never answered: it blocked on a node", what)
		return 0, nil
	}
}

// gateRowsCase is one row a gate fan-out must or must not dial: the mutation
// to its stub row and the hits its script must see.
type gateRowsCase struct {
	name   string
	mutate func(*burst.NodeRow)
	hits   int64
}

// gateRowsCases pins which rows take gate traffic: the two live rows take
// the gate, while a draining, unverified, gone or problem row takes none --
// the node's own builder gates itself on its next limit instead.
//
// Mutation check: drop any arm of the gate and that row's case dials.
func gateRowsCases() []gateRowsCase {
	return []gateRowsCase{
		{"held live", func(r *burst.NodeRow) {}, 1},
		{"failing live", func(r *burst.NodeRow) {}, 1},
		{"draining", func(r *burst.NodeRow) { r.State = "draining" }, 0},
		{"unverified", func(r *burst.NodeRow) { r.Verified = false }, 0},
		{"gone", func(r *burst.NodeRow) { r.Gone = true }, 0},
		{"problem", func(r *burst.NodeRow) { r.Problem = burst.ReportProblemUnreachable }, 0},
	}
}

// captureGateLogs routes the log through buf for the test, restoring the
// default afterwards.
func captureGateLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// lockedBuffer is the fan-out log sink: detached fan-out goroutines write to
// it while the test still reads, so both sides take the lock.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestUnavailableFansOutLiveNodesOnly pins the server-wide unavailable path:
// after the local ledger write the head dials every verified live node with
// the gate it received, without waiting for any answer. A draining, gone,
// unverified or problem row is assertably undialled; a node that refuses
// the gate only reaches the log and the join hook, never the 200 the head
// already answered.
func TestUnavailableFansOutLiveNodesOnly(t *testing.T) {
	clock := time.Now().UTC()
	release := make(chan struct{})
	held := &nodeScript{unavailableWait: release}
	failing := &nodeScript{unavailableMsg: "node boom"}
	failing.unavailableStatus.Store(http.StatusInternalServerError)
	scripts := []*nodeScript{held, failing, {}, {}, {}, {}}
	env, rows := gateFanoutEnv(t, scripts, &clock)
	env.srv.mu.Lock()
	tc := gateRowsCases()
	for i := range scripts {
		tc[i].mutate(&env.srv.burstTable.Nodes[i])
	}
	env.srv.mu.Unlock()

	join := &hookJoin{}
	env.srv.gateFanoutHook = join.hook
	logs := captureGateLogs(t)

	const token, reason = "claude/anthropic/haiku", "rate limited test"
	payload, err := json.Marshal(remote.UnavailableRequest{Token: token, Reason: reason})
	if err != nil {
		t.Fatalf("marshal unavailable request: %v", err)
	}
	call := postGateAsync(t, env, "/v1/unavailable", payload)
	if status, _ := awaitGateCall(t, call, "unavailable"); status != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200", status)
	}
	if got := len(rateLimitedEntries(t, env.srv)); got != 1 {
		t.Errorf("rate-limit entries = %d, want the one the head recorded before fanning out", got)
	}

	// The held node saw the gate while its answer is still pending: the
	// head answered around it.
	tickUntil(t, func() bool { return held.unavailableHits.Load() == 1 }, "the held node never saw the gate")
	// The failing node refused on its own: its attempt landed with the 500
	// carried, while the held one is still out.
	tickUntil(t, func() bool { return failing.unavailableHits.Load() == 1 }, "the failing node never saw the gate")
	tickUntil(t, func() bool { return join.count() == 1 }, "the failing attempt never landed")
	if join.allNil() {
		t.Error("failing node landed nil, want its 500 carried to the join hook")
	}
	close(release)
	tickUntil(t, func() bool { return join.count() == 2 }, "the held attempt never landed after the release")

	for i, s := range scripts {
		if got := s.unavailableHits.Load(); got != tc[i].hits {
			t.Errorf("row %s hits = %d, want %d", tc[i].name, got, tc[i].hits)
		}
	}
	for _, s := range []*nodeScript{held, failing} {
		seen := s.gateTokens()
		if len(seen) != 1 || seen[0].token != token || seen[0].reason != reason {
			t.Errorf("node gates = %+v, want the one posted gate (token %q)", seen, token)
		}
	}
	if got := strings.Count(logs.String(), "gate fan-out failed"); got != 1 {
		t.Errorf("failure log lines = %d, want exactly the one failing node logged", got)
	}
	if !strings.Contains(logs.String(), "node="+rows[1].ID) {
		t.Errorf("failure log has no node=%s line:\n%s", rows[1].ID, logs.String())
	}
}

// TestAvailableFansOutLiveNodesOnly pins the same path for the lifted gate:
// a clear with nothing to lift still answers 200 and reaches every verified
// live node, while gated rows stay undialled, the head never blocks and a
// refusing node only logs.
func TestAvailableFansOutLiveNodesOnly(t *testing.T) {
	clock := time.Now().UTC()
	release := make(chan struct{})
	held := &nodeScript{availableWait: release}
	failing := &nodeScript{availableStatus: http.StatusInternalServerError, availableMsg: "node boom"}
	scripts := []*nodeScript{held, failing, {}, {}, {}, {}}
	env, rows := gateFanoutEnv(t, scripts, &clock)
	env.srv.mu.Lock()
	tc := gateRowsCases()
	for i := range scripts {
		tc[i].mutate(&env.srv.burstTable.Nodes[i])
	}
	env.srv.mu.Unlock()

	join := &hookJoin{}
	env.srv.gateFanoutHook = join.hook
	logs := captureGateLogs(t)

	const subject = "anthropic"
	payload, err := json.Marshal(remote.AvailableRequest{Subject: subject})
	if err != nil {
		t.Fatalf("marshal available request: %v", err)
	}
	call := postGateAsync(t, env, "/v1/available", payload)
	status, body := awaitGateCall(t, call, "available")
	if status != http.StatusOK {
		t.Fatalf("available status = %d, want 200", status)
	}
	var avail remote.AvailableResponse
	if err := json.Unmarshal(body, &avail); err != nil {
		t.Fatalf("decode available response: %v", err)
	}
	if avail.Provider != subject {
		t.Errorf("available provider = %q, want %q", avail.Provider, subject)
	}

	tickUntil(t, func() bool { return held.availableHits.Load() == 1 }, "the held node never saw the clear")
	tickUntil(t, func() bool { return failing.availableHits.Load() == 1 }, "the failing node never saw the clear")
	tickUntil(t, func() bool { return join.count() == 1 }, "the failing attempt never landed")
	if join.allNil() {
		t.Error("failing node landed nil, want its 500 carried to the join hook")
	}
	close(release)
	tickUntil(t, func() bool { return join.count() == 2 }, "the held attempt never landed after the release")

	for i, s := range scripts {
		if got := s.availableHits.Load(); got != tc[i].hits {
			t.Errorf("row %s hits = %d, want %d", tc[i].name, got, tc[i].hits)
		}
	}
	for _, s := range []*nodeScript{held, failing} {
		seen := s.gateTokens()
		if len(seen) != 1 || seen[0].token != subject || seen[0].reason != "" {
			t.Errorf("node gates = %+v, want the one posted subject %q", seen, subject)
		}
	}
	if got := strings.Count(logs.String(), "gate fan-out failed"); got != 1 {
		t.Errorf("failure log lines = %d, want exactly the one failing node logged", got)
	}
	if !strings.Contains(logs.String(), "node="+rows[1].ID) {
		t.Errorf("failure log has no node=%s line:\n%s", rows[1].ID, logs.String())
	}
}

// TestAdminGatesFanOut pins the other two entries to the same ledger: a
// server-side gate and clear through AdminUnavailable and AdminAvailable
// reach the live nodes, while a refused gate fans out nothing.
func TestAdminGatesFanOut(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{}
	env, _ := gateFanoutEnv(t, []*nodeScript{script}, &clock)
	join := &hookJoin{}
	env.srv.gateFanoutHook = join.hook

	const token, reason = "claude/anthropic/haiku", "rate limited test"
	provider, err := AdminUnavailable(env.srv, token, time.Time{}, reason)
	if err != nil {
		t.Fatalf("AdminUnavailable: %v", err)
	}
	if provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", provider)
	}
	tickUntil(t, func() bool { return script.unavailableHits.Load() == 1 }, "the node never saw the admin gate")
	tickUntil(t, join.fired, "the admin gate fan-out never landed")
	if err := join.first(); err != nil {
		t.Errorf("fan-out err = %v, want the gate landed", err)
	}
	if seen := script.gateTokens(); len(seen) != 1 || seen[0].token != token || seen[0].reason != reason {
		t.Errorf("node gates = %+v, want the one admin gate", seen)
	}

	cleared, removed, err := AdminAvailable(env.srv, "anthropic")
	if err != nil {
		t.Fatalf("AdminAvailable: %v", err)
	}
	if cleared != "anthropic" || removed != 1 {
		t.Errorf("clear = (%q, %d), want (anthropic, 1)", cleared, removed)
	}
	tickUntil(t, func() bool { return script.availableHits.Load() == 1 }, "the node never saw the admin clear")
	if seen := script.gateTokens(); len(seen) != 2 || seen[1].token != "anthropic" {
		t.Errorf("node gates = %+v, want the clear after the gate", seen)
	}

	if _, err := AdminUnavailable(env.srv, "bogus", time.Time{}, reason); err == nil {
		t.Error("AdminUnavailable(bogus) = nil, want the unknown subject refused")
	}
	if _, _, err := AdminAvailable(env.srv, "bogus"); err == nil {
		t.Error("AdminAvailable(bogus) = nil, want the unknown subject refused")
	}
	if got := script.unavailableHits.Load(); got != 1 {
		t.Errorf("unavailable hits = %d, want still 1: a refused gate fans out nothing", got)
	}
	if got := script.availableHits.Load(); got != 1 {
		t.Errorf("available hits = %d, want still 1: a refused clear fans out nothing", got)
	}
}

// TestUnavailableFanoutCarriesRecordedUntil pins that a gate fan-out sends
// the head's own Until: a gate recorded with an explicit two-hour expiry
// reaches the node carrying that same expiry on the wire.
func TestUnavailableFanoutCarriesRecordedUntil(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{}
	env, _ := gateFanoutEnv(t, []*nodeScript{script}, &clock)
	join := &hookJoin{}
	env.srv.gateFanoutHook = join.hook

	const token, reason = "claude/anthropic/haiku", "rate limited test"
	until := clock.Add(2 * time.Hour)
	if _, err := AdminUnavailable(env.srv, token, until, reason); err != nil {
		t.Fatalf("AdminUnavailable: %v", err)
	}
	tickUntil(t, func() bool { return script.unavailableHits.Load() == 1 }, "the node never saw the admin gate")
	tickUntil(t, join.fired, "the admin gate fan-out never landed")
	if err := join.first(); err != nil {
		t.Fatalf("fan-out err = %v, want the gate landed", err)
	}
	seen := script.gateTokens()
	if len(seen) != 1 {
		t.Fatalf("node gates = %+v, want the one admin gate", seen)
	}
	if !seen[0].until.Equal(until) {
		t.Errorf("node until = %v, want the head's %v", seen[0].until, until)
	}
}

// TestNoResetHeadGateReachesNodeFinite pins the two halves together: a head
// gate recorded for a limit reason with no parseable reset and no explicit
// expiry still reaches the node carrying a finite Until, bounded by the
// head's policy default.
func TestNoResetHeadGateReachesNodeFinite(t *testing.T) {
	clock := time.Now().UTC()
	script := &nodeScript{}
	env, _ := gateFanoutEnv(t, []*nodeScript{script}, &clock)
	join := &hookJoin{}
	env.srv.gateFanoutHook = join.hook

	const token = "claude/anthropic/haiku"
	const reason = "API error 429 (FreeUsageLimitError): Free usage limit reached"
	payload, err := json.Marshal(remote.UnavailableRequest{Token: token, Reason: reason})
	if err != nil {
		t.Fatalf("marshal unavailable request: %v", err)
	}
	call := postGateAsync(t, env, "/v1/unavailable", payload)
	if status, _ := awaitGateCall(t, call, "unavailable"); status != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200", status)
	}

	entries := rateLimitedEntries(t, env.srv)
	if len(entries) != 1 {
		t.Fatalf("rate-limit entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Until.IsZero() {
		t.Fatal("head Until is zero: a no-reset limit must bound, not hold until cleared")
	}

	tickUntil(t, func() bool { return script.unavailableHits.Load() == 1 }, "the node never saw the gate")
	tickUntil(t, join.fired, "the gate fan-out never landed")
	seen := script.gateTokens()
	if len(seen) != 1 {
		t.Fatalf("node gates = %+v, want the one posted gate", seen)
	}
	if !seen[0].until.Equal(entries[0].Until) {
		t.Errorf("node until = %v, want the head's %v", seen[0].until, entries[0].Until)
	}
}
