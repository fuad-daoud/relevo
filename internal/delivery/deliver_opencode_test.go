package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// opencodeRowMS is a session row's time_created stamp. OpenCode writes all
// three counted columns in epoch milliseconds, so the read-back's bound
// compares them against the entry's queuedAt in the same unit.
const opencodeRowMS = "1789021109130"

// fakeSqliteExec is the fake usage.Exec deliver_opencode_test.go controls
// directly: it records every query and answers seen()'s "select count(*)"
// with a count that flips from 0 to 1 once callNumber reaches seenFrom (0
// means never). err, when set, is returned as a Run failure instead.
type fakeSqliteExec struct {
	queries  []string
	calls    int
	seenFrom int
	err      error
}

func (f *fakeSqliteExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(args) > 0 && strings.Contains(args[len(args)-1], "sqlite_master") {
		return []byte(`[{"name":"session_inbox"}]`), nil
	}
	f.calls++
	if len(args) > 0 {
		f.queries = append(f.queries, args[len(args)-1])
	}
	n := 0
	if f.seenFrom > 0 && f.calls >= f.seenFrom {
		n = 1
	}
	return []byte(strconv.Itoa(n)), nil
}

func writeOpencodeServiceFile(t *testing.T, dir, url, password string, pid int) string {
	t.Helper()
	p := filepath.Join(dir, "service.json")
	data, err := json.Marshal(opencodeService{URL: url, Password: password, PID: pid, Version: "2.0.12"})
	if err != nil {
		t.Fatalf("marshal service.json: %v", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write service.json: %v", err)
	}
	return p
}

func opencodeMasterMind(sessionID string) store.Endpoint {
	return store.Endpoint{Kind: "opencode", SessionID: sessionID, PaneID: "w2:p3"}
}

// aliveAlways and aliveNever stand in for the pid-liveness check so tests
// never depend on a real process's pid.
func aliveAlways(int) bool { return true }
func aliveNever(int) bool  { return false }

func TestOpencodeDeliverHappyPath(t *testing.T) {
	t.Parallel()

	var gotPath, gotAuth, gotContentType string
	var gotBody []byte
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "the-password", 1)
	exec := &fakeSqliteExec{seenFrom: 2} // not seen pre-POST, seen on the first confirm poll

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	payload := "relevo: round 1 · to MasterMind · about runner \"w\" (not the human)\n\nThe runner finished round 1. Report: /x/001-report.md"
	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), payload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeAdmitted {
		t.Fatalf("out = %v, reason = %q, want OutcomeAdmitted", out, reason)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if gotPath != "/api/session/ses_abc123/prompt" {
		t.Errorf("path = %q, want /api/session/ses_abc123/prompt", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	wantAuth := "Basic " + basicAuth("opencode", "the-password")
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
	assertOpencodePromptBody(t, gotBody, payload)

	// The read-back, not the 2xx, is what confirms the payload.
	out, reason, err = d.Confirm(context.Background(), opencodeMasterMind("ses_abc123"), payload, time.Time{})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if out != OutcomeDelivered {
		t.Fatalf("Confirm = %v, reason = %q, want OutcomeDelivered", out, reason)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1: the read-back must not POST", requests)
	}
}

// assertOpencodePromptBody checks the JSON body one POST carried.
func assertOpencodePromptBody(t *testing.T, raw []byte, payload string) {
	t.Helper()
	var body struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
		Resume   bool   `json:"resume"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if body.Text != payload {
		t.Errorf("body.Text = %q, want the payload verbatim", body.Text)
	}
	if body.Delivery != "queue" {
		t.Errorf("body.Delivery = %q, want %q", body.Delivery, "queue")
	}
	if !body.Resume {
		t.Error("body.Resume = false, want true")
	}
}

// TestOpencodeDeliverSilentTwoHundred is the test the design exists for
// a 2xx from the wrong server process must never be treated as
// delivery. The fake Exec always answers 0, so the origin is never seen: the
// POST is admission only, and only the read-back may ever confirm it.
func TestOpencodeDeliverSilentTwoHundred(t *testing.T) {
	t.Parallel()

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	exec := &fakeSqliteExec{} // seenFrom 0: never seen

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeAdmitted {
		t.Fatalf("out = %v, reason = %q, want OutcomeAdmitted", out, reason)
	}
	if reason != "posted; awaiting the session" {
		t.Errorf("reason = %q", reason)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly one POST", requests)
	}
}

// TestOpencodeDeliverIdempotentSkipsPost proves a retry never double-posts:
// when the origin is already in the db, Deliver confirms without touching
// the network at all.
func TestOpencodeDeliverIdempotentSkipsPost(t *testing.T) {
	t.Parallel()

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	exec := &fakeSqliteExec{seenFrom: 1} // already seen on the very first query

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Fatalf("out = %v, reason = %q, want OutcomeDelivered/\"already present\"", out, reason)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want zero: idempotence must skip the POST entirely", requests)
	}
}

func TestOpencodeDeliverNotMineCases(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, "http://127.0.0.1:1", "pw", 1)

	cases := []struct {
		name       string
		mastermind store.Endpoint
		noExec     bool
		reason     string
	}{
		{name: "claude mastermind", mastermind: store.Endpoint{Kind: "claude", SessionID: "ses_abc123"}, reason: ""},
		{name: "empty session id", mastermind: store.Endpoint{Kind: "opencode", SessionID: ""}, reason: "no opencode session id"},
		{name: "malformed session id", mastermind: store.Endpoint{Kind: "opencode", SessionID: "ses_bad!id"}, reason: "no opencode session id"},
		{name: "nil Exec", mastermind: store.Endpoint{Kind: "opencode", SessionID: "ses_abc123"}, noExec: true, reason: "no sqlite3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &OpencodeDeliverer{StateFiles: []string{stateFile}, Alive: aliveAlways}
			if !tc.noExec {
				d.Exec = &fakeSqliteExec{}
			}

			out, reason, err := d.Deliver(context.Background(), tc.mastermind, "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
			if err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if out != OutcomeNotMine {
				t.Fatalf("out = %v, want OutcomeNotMine", out)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

// TestOpencodeConfirmRefusesWhatItCannotRead pins Confirm's guards: the
// read-back half answers "not mine" for exactly what Deliver answers it for.
func TestOpencodeConfirmRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, "http://127.0.0.1:1", "pw", 1)

	cases := []struct {
		name       string
		mastermind store.Endpoint
		noExec     bool
		reason     string
	}{
		{name: "claude mastermind", mastermind: store.Endpoint{Kind: "claude", SessionID: "ses_abc123"}, reason: ""},
		{name: "empty session id", mastermind: store.Endpoint{Kind: "opencode", SessionID: ""}, reason: "no opencode session id"},
		{name: "malformed session id", mastermind: store.Endpoint{Kind: "opencode", SessionID: "ses_bad!id"}, reason: "no opencode session id"},
		{name: "nil Exec", mastermind: store.Endpoint{Kind: "opencode", SessionID: "ses_abc123"}, noExec: true, reason: "no sqlite3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &OpencodeDeliverer{StateFiles: []string{stateFile}, Alive: aliveAlways}
			if !tc.noExec {
				d.Exec = &fakeSqliteExec{}
			}

			out, reason, err := d.Confirm(context.Background(), tc.mastermind, "relevo: round 1\n\nbody", time.Time{})
			if err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			if out != OutcomeNotMine {
				t.Fatalf("out = %v, want OutcomeNotMine", out)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

func TestOpencodeDeliverUnavailableCases(t *testing.T) {
	t.Parallel()

	goodDir := t.TempDir()
	deadPidFile := writeOpencodeServiceFile(t, t.TempDir(), "http://127.0.0.1:49999", "pw", 999999)
	nonLoopbackFile := writeOpencodeServiceFile(t, goodDir, "http://example.com:8080", "pw", 1)

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()
	errFile := writeOpencodeServiceFile(t, t.TempDir(), errSrv.URL, "pw", 1)

	transportErrFile := writeOpencodeServiceFile(t, t.TempDir(), "http://127.0.0.1:1", "pw", 1)

	cases := []struct {
		name             string
		stateFiles       []string
		alive            func(int) bool
		exec             *fakeSqliteExec
		wantReasonPrefix string
	}{
		{name: "missing state file", stateFiles: []string{filepath.Join(t.TempDir(), "nope.json")}, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service not running"},
		{name: "dead pid", stateFiles: []string{deadPidFile}, alive: aliveNever, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service not running"},
		{name: "non-loopback url", stateFiles: []string{nonLoopbackFile}, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service url is not loopback"},
		{name: "500 from server", stateFiles: []string{errFile}, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "post: 500"},
		{name: "transport error", stateFiles: []string{transportErrFile}, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "post:"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &OpencodeDeliverer{StateFiles: tc.stateFiles, DBPath: filepath.Join(t.TempDir(), "opencode.db"), Exec: tc.exec, Alive: tc.alive}
			// A refused POST is answered without waiting: the caller's lock is a
			// global one, so a sleep here would stall every other binding's tick.
			start := time.Now()
			out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
			took := time.Since(start)
			if err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if out != OutcomeUnavailable {
				t.Fatalf("out = %v, reason = %q, want OutcomeUnavailable", out, reason)
			}
			if !strings.HasPrefix(reason, tc.wantReasonPrefix) {
				t.Errorf("reason = %q, want prefix %q", reason, tc.wantReasonPrefix)
			}
			if took > 2*time.Second {
				t.Errorf("Deliver took %s, want no waiting between attempts: an unreachable service must answer promptly", took)
			}
		})
	}
}

// opencodeDownServer is a service that refuses the first failures POSTs and takes
// the one after them: the opencode-down-then-recovered shape the across-tick
// retry exists for. It records every POST it is asked for.
type opencodeDownServer struct {
	mu       sync.Mutex
	posts    int
	failures int // POSTs answered 503 before the service takes one
}

func (s *opencodeDownServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.posts++
		failing := s.posts <= s.failures
		s.mu.Unlock()
		if failing {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *opencodeDownServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posts
}

// TestOpencodeDeliverRetriesAcrossTicks is the push that must not strand: a
// service that refuses the first POSTs is retried on the following ticks, each
// call POSTing exactly once, and the payload is admitted once the service takes
// one rather than being written off at a short fallback so a pane path with no
// collector behind it would take it. The read-back that follows confirms it,
// and no POST is repeated after that.
//
// No call sleeps between POSTs: the retry is the tick, so the per-call cost is
// one request and the entry stays pending in between.
func TestOpencodeDeliverRetriesAcrossTicks(t *testing.T) {
	t.Parallel()

	svc := &opencodeDownServer{failures: 2}
	srv := svc.start(t)

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	// seenFrom 0: the session has not taken the payload yet, so every call's
	// read-back below finds nothing.
	exec := &fakeSqliteExec{}

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	payload := "relevo: round 1 · to MasterMind · about runner \"w\" (not the human)\n\nThe runner finished round 1."
	queuedAt := time.Now().Add(-time.Minute)
	endpoint := opencodeMasterMind("ses_abc123")

	// The first two ticks the service refuses the one POST each of them sends.
	// A refusal is unavailable, never given up on: the entry stays pending and
	// the next tick tries again under the same 30-minute horizon.
	for tick, wantPosts := range []int{1, 2} {
		out, reason, err := d.Deliver(context.Background(), endpoint, payload, "/x/001-report.md", queuedAt)
		if err != nil {
			t.Fatalf("Deliver on tick %d: %v", tick+1, err)
		}
		if out != OutcomeUnavailable {
			t.Fatalf("tick %d: out = %v, reason = %q, want OutcomeUnavailable: a refused POST is retried on the next tick, not given up on", tick+1, out, reason)
		}
		if !strings.HasPrefix(reason, "post: ") {
			t.Errorf("tick %d: reason = %q, want the POST refusal as the reason", tick+1, reason)
		}
		if got := svc.count(); got != wantPosts {
			t.Fatalf("tick %d: POSTs = %d, want %d: one call POSTs once", tick+1, got, wantPosts)
		}
	}

	// The third tick the service takes the payload and it is admitted.
	out, reason, err := d.Deliver(context.Background(), endpoint, payload, "/x/001-report.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver on tick 3: %v", err)
	}
	if out != OutcomeAdmitted || reason != "posted; awaiting the session" {
		t.Fatalf("tick 3 Deliver = (%v, %q), want OutcomeAdmitted after the service recovered", out, reason)
	}
	if got := svc.count(); got != 3 {
		t.Fatalf("POSTs = %d, want 3: the two refusals and the one that took it", got)
	}

	// The session takes the turn and the read-back confirms it, with no further
	// POST: an outage is ridden out, not double-sent.
	exec.seenFrom = exec.calls + 1
	out, reason, err = d.Confirm(context.Background(), endpoint, payload, queuedAt)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if out != OutcomeDelivered {
		t.Fatalf("Confirm = (%v, %q), want OutcomeDelivered once the session took it", out, reason)
	}
	if got := svc.count(); got != 3 {
		t.Fatalf("POSTs = %d after the confirming read-back, want 3: a read-back never sends", got)
	}
}

// TestOpencodeDeliverSeenFirstSkipsThePOSTOnALaterTick pins the idempotence
// rule across ticks: once the session holds the payload, the next call confirms
// it before anything is sent, so the retries an outage causes never re-POST a
// payload that already landed. The refusal below is one that had in fact
// queued the turn, which is exactly the case a retry must not duplicate.
func TestOpencodeDeliverSeenFirstSkipsThePOSTOnALaterTick(t *testing.T) {
	t.Parallel()

	svc := &opencodeDownServer{failures: 1}
	srv := svc.start(t)

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	exec := &fakeSqliteExec{} // nothing seen yet

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	endpoint := opencodeMasterMind("ses_abc123")
	queuedAt := time.Now().Add(-time.Minute)

	// The first tick's read-back finds nothing, so the one POST it sends is the
	// refused one that had queued the turn all the same.
	out, reason, err := d.Deliver(context.Background(), endpoint, "relevo: round 1\n\nbody", "/x/r.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeUnavailable || !strings.HasPrefix(reason, "post: ") {
		t.Fatalf("Deliver = (%v, %q), want OutcomeUnavailable on the refusal", out, reason)
	}
	if got := svc.count(); got != 1 {
		t.Fatalf("POSTs = %d, want 1", got)
	}

	// From the next read-back on, the session holds the payload.
	exec.seenFrom = exec.calls + 1

	// The next tick confirms it with no request at all.
	out, reason, err = d.Deliver(context.Background(), endpoint, "relevo: round 1\n\nbody", "/x/r.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Fatalf("Deliver = (%v, %q), want OutcomeDelivered/\"already present\"", out, reason)
	}
	if got := svc.count(); got != 1 {
		t.Fatalf("POSTs = %d, want 1: a retry must never re-POST a payload the session holds", got)
	}

	// And it stays confirmed on the tick after that, still without a POST.
	out, reason, err = d.Deliver(context.Background(), endpoint, "relevo: round 1\n\nbody", "/x/r.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Fatalf("Deliver = (%v, %q), want OutcomeDelivered/\"already present\" on the following tick", out, reason)
	}
	if got := svc.count(); got != 1 {
		t.Fatalf("POSTs = %d, want 1 across every later tick", got)
	}
}

// TestOpencodeDeliverFallsBackAfterFallbackAfter proves the give-up gate still
// ends a payload an opencode cannot take: once the opencode push horizon has
// passed, the entry is OutcomeNotMine and reported for the pane path rather
// than retried forever. The horizon is the long one, because an opencode give-up
// has no collector behind it, so the gate sits at the end of the retry window
// and not just after it.
func TestOpencodeDeliverFallsBackAfterFallbackAfter(t *testing.T) {
	t.Parallel()

	stateFile := writeOpencodeServiceFile(t, t.TempDir(), "http://127.0.0.1:1", "pw", 1)
	queuedAt := time.Unix(1000, 0)
	now := queuedAt.Add(OpencodePushHorizon + time.Second) // past the opencode horizon

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile}, // deliberately unusable (dead pid) -- would be OutcomeUnavailable before the horizon check
		DBPath:     filepath.Join(t.TempDir(), "opencode.db"),
		Exec:       &fakeSqliteExec{},
		Alive:      aliveNever,
		Now:        func() time.Time { return now },
	}

	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine {
		t.Fatalf("out = %v, reason = %q, want OutcomeNotMine", out, reason)
	}
	if !strings.Contains(reason, "opencode push gave up after") {
		t.Errorf("reason = %q, want it to name the fallback", reason)
	}
}

// TestOpencodeDeliverConfirmsAPayloadSeenPastFallback proves the read-back
// precedes the give-up gate: a payload whose text is already in the session is
// confirmed past the push horizon, and never POSTs.
func TestOpencodeDeliverConfirmsAPayloadSeenPastFallback(t *testing.T) {
	t.Parallel()

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	exec := &fakeSqliteExec{seenFrom: 1}

	queuedAt := time.Unix(1000, 0)
	now := queuedAt.Add(OpencodePushHorizon + time.Second) // past the opencode horizon

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
		Now:        func() time.Time { return now },
	}

	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", "/x/001-report.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Fatalf("out = %v, reason = %q, want OutcomeDelivered/\"already present\"", out, reason)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want zero: the read-back must skip the POST", requests)
	}
}

func TestOpencodeDeliverLogsGiveUpOncePerPayload(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	stateFile := writeOpencodeServiceFile(t, t.TempDir(), "http://127.0.0.1:1", "pw", 1)
	queuedAt := time.Unix(1000, 0)
	now := queuedAt.Add(OpencodePushHorizon + time.Second)

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(t.TempDir(), "opencode.db"),
		Exec:       &fakeSqliteExec{},
		Alive:      aliveNever,
		Now:        func() time.Time { return now },
	}

	payload := "relevo: round 1\n\nbody"
	endpoint := opencodeMasterMind("ses_abc123")

	// 1. call Deliver three times with the same payload and queuedAt, each past the horizon
	for _, sec := range []time.Duration{
		OpencodePushHorizon + time.Second,
		OpencodePushHorizon + 2*time.Second,
		OpencodePushHorizon + 3*time.Second,
	} {
		now = queuedAt.Add(sec)
		out, reason, err := d.Deliver(context.Background(), endpoint, payload, "/x/r.md", queuedAt)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		// 2. assert that every call returned OutcomeNotMine with the reason containing opencode push gave up after
		if out != OutcomeNotMine {
			t.Fatalf("out = %v, want OutcomeNotMine", out)
		}
		if !strings.Contains(reason, "opencode push gave up after") {
			t.Errorf("reason = %q, want it to contain 'opencode push gave up after'", reason)
		}
	}

	countLogs := func() int {
		return strings.Count(logged.String(), "push not confirmed")
	}

	// 3. assert that the buffer contains push not confirmed exactly once
	if got := countLogs(); got != 1 {
		t.Fatalf("got %d 'push not confirmed' log lines, want 1; logs:\n%s", got, logged.String())
	}

	// 4. call once more with a different queuedAt (queuedAt+1s) and a now past its fallback, and assert the count is 2
	queuedAt2 := queuedAt.Add(time.Second)
	now = queuedAt2.Add(OpencodePushHorizon + time.Second)
	out, reason, err := d.Deliver(context.Background(), endpoint, payload, "/x/r.md", queuedAt2)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine || !strings.Contains(reason, "opencode push gave up after") {
		t.Fatalf("out = %v, reason = %q, want OutcomeNotMine with give up", out, reason)
	}
	if got := countLogs(); got != 2 {
		t.Fatalf("got %d 'push not confirmed' log lines after second queuedAt, want 2; logs:\n%s", got, logged.String())
	}

	// 5. set now one hour past the first call and call with the first payload again, and assert the count is 3
	now = queuedAt.Add(OpencodePushHorizon + time.Second + time.Hour)
	out, reason, err = d.Deliver(context.Background(), endpoint, payload, "/x/r.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine || !strings.Contains(reason, "opencode push gave up after") {
		t.Fatalf("out = %v, reason = %q, want OutcomeNotMine with give up", out, reason)
	}
	if got := countLogs(); got != 3 {
		t.Fatalf("got %d 'push not confirmed' log lines after 1 hour, want 3; logs:\n%s", got, logged.String())
	}
}

// TestOpencodeDeliverPasswordNeverLeaks runs several scenarios with a
// distinctive password and asserts it never appears in a returned reason
// or error.
func TestOpencodeDeliverPasswordNeverLeaks(t *testing.T) {
	t.Parallel()

	const password = "sekrit-do-not-log-me"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, password, 1)

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       &fakeSqliteExec{},
		Alive:      aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
	if out != OutcomeUnavailable {
		t.Fatalf("out = %v, want OutcomeUnavailable (500 from server)", out)
	}
	if strings.Contains(reason, password) {
		t.Errorf("reason leaks the password: %q", reason)
	}
	if err != nil && strings.Contains(err.Error(), password) {
		t.Errorf("error leaks the password: %v", err)
	}
}

// opencodeStateFiles is the state-file fixture the preference test chooses
// between, with the two request counters its fake services record.
type opencodeStateFiles struct {
	stateWithURL       string
	configPasswordOnly string
	configWithURL      string
	missing            string
	dbPath             string
	stateRequests      int
	configRequests     int
}

// newOpencodeStateFiles starts two fake opencode services and writes the
// service files a deliverer can be pointed at.
func newOpencodeStateFiles(t *testing.T) *opencodeStateFiles {
	t.Helper()
	f := &opencodeStateFiles{}
	stateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.stateRequests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(stateSrv.Close)
	configSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.configRequests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(configSrv.Close)

	dir := t.TempDir()
	f.stateWithURL = writeOpencodeServiceFile(t, dir, stateSrv.URL, "pw", 1)
	pwDir := filepath.Join(dir, "pw-only")
	if err := os.MkdirAll(pwDir, 0o755); err != nil {
		t.Fatalf("mkdir pw-only: %v", err)
	}
	f.configPasswordOnly = writeOpencodeServiceFile(t, pwDir, "", "pw", 1)
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	f.configWithURL = writeOpencodeServiceFile(t, configDir, configSrv.URL, "pw", 1)
	f.missing = filepath.Join(dir, "nonexistent.json")
	f.dbPath = filepath.Join(dir, "opencode.db")
	return f
}

// opencodeDeliverOnce runs one Deliver of the fixed payload through d and
// returns its outcome and reason.
func opencodeDeliverOnce(t *testing.T, d *OpencodeDeliverer) (Outcome, string) {
	t.Helper()
	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	return out, reason
}

func TestOpencodeDeliverStateFilesPreference(t *testing.T) {
	t.Parallel()

	f := newOpencodeStateFiles(t)

	t.Run("stateWithURL and configPasswordOnly uses state url", func(t *testing.T) {
		f.stateRequests, f.configRequests = 0, 0
		d := &OpencodeDeliverer{
			StateFiles: []string{f.stateWithURL, f.configPasswordOnly},
			DBPath:     f.dbPath,
			Exec:       &fakeSqliteExec{seenFrom: 2},
			Alive:      aliveAlways,
		}
		out, reason := opencodeDeliverOnce(t, d)
		if out != OutcomeAdmitted {
			t.Fatalf("out = %v, reason = %q, want OutcomeAdmitted", out, reason)
		}
		if f.stateRequests != 1 || f.configRequests != 0 {
			t.Errorf("stateRequests = %d, configRequests = %d; want 1 and 0", f.stateRequests, f.configRequests)
		}
	})

	t.Run("missing and configWithURL uses config url", func(t *testing.T) {
		f.stateRequests, f.configRequests = 0, 0
		d := &OpencodeDeliverer{
			StateFiles: []string{f.missing, f.configWithURL},
			DBPath:     f.dbPath,
			Exec:       &fakeSqliteExec{seenFrom: 2},
			Alive:      aliveAlways,
		}
		out, reason := opencodeDeliverOnce(t, d)
		if out != OutcomeAdmitted {
			t.Fatalf("out = %v, reason = %q, want OutcomeAdmitted", out, reason)
		}
		if f.stateRequests != 0 || f.configRequests != 1 {
			t.Errorf("stateRequests = %d, configRequests = %d; want 0 and 1", f.stateRequests, f.configRequests)
		}
	})

	t.Run("passwordOnly behaves as unusable file", func(t *testing.T) {
		d := &OpencodeDeliverer{
			StateFiles: []string{f.configPasswordOnly},
			DBPath:     f.dbPath,
			Exec:       &fakeSqliteExec{},
			Alive:      aliveAlways,
		}
		out, reason := opencodeDeliverOnce(t, d)
		if out != OutcomeUnavailable || reason != "opencode service not running" {
			t.Fatalf("out = %v, reason = %q; want OutcomeUnavailable and service-not-running", out, reason)
		}
	})
}

func basicAuth(user, pass string) string {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	req.SetBasicAuth(user, pass)
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
}

// TestOpencodeConfirmSeen covers the two shapes a delivered turn can take
// OpenCode 2.0.14's session_message row, and the pre-2.0 part/message
// pair. Only a user turn counts, and either table alone confirms.
func TestOpencodeConfirmSeen(t *testing.T) {
	t.Parallel()

	const origin = "relevo: round 1 to builder"

	tables := []string{
		"create table message (id text, session_id text, time_created integer, data text)",
		"create table part (id text, message_id text, session_id text, data text)",
		"create table session_message (id text, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)",
	}
	userText := `{"type":"text","text":"` + origin + `"}`
	v2UserText := `{"text":"` + origin + `"}`

	cases := []struct {
		name  string
		stmts []string
		want  bool
	}{
		{
			name: "session_inbox row whose payload carries the origin -> seen",
			stmts: append(append([]string{}, tables...),
				"create table session_inbox (id text, session_id text, type text, payload text, delivery text, enqueued_seq integer, time_created integer)",
				"insert into session_inbox values ('inbox1', 'ses_x', 'message', '{\"text\":\""+origin+"\"}', 'queue', 0, "+opencodeRowMS+")"),
			want: true,
		},
		{
			name: "db with no session_inbox table still works",
			stmts: append(append([]string{}, tables...),
				"insert into session_message values ('m1', 'ses_x', 'user', 0, '"+opencodeRowMS+"', 1, '"+v2UserText+"')"),
			want: true,
		},
		{
			name: "session_message user row containing the origin -> seen",
			stmts: append(append([]string{}, tables...),
				"insert into session_message values ('m1', 'ses_x', 'user', 0, '"+opencodeRowMS+"', 1, '"+v2UserText+"')"),
			want: true,
		},
		{
			name: "legacy part/message user row containing the origin -> seen",
			stmts: append(append([]string{}, tables...),
				"insert into message values ('msg1', 'ses_x', '"+opencodeRowMS+"', '{\"role\":\"user\"}')",
				"insert into part values ('p1', 'msg1', 'ses_x', '"+userText+"')"),
			want: true,
		},
		{
			name: "assistant row containing the origin -> not seen",
			stmts: append(append([]string{}, tables...),
				"insert into session_message values ('m1', 'ses_x', 'assistant', 0, '"+opencodeRowMS+"', 1, '"+v2UserText+"')",
				"insert into message values ('msg1', 'ses_x', '"+opencodeRowMS+"', '{\"role\":\"assistant\"}')",
				"insert into part values ('p1', 'msg1', 'ses_x', '"+userText+"')"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := sqliteFixture(t, tc.stmts...)
			d := &OpencodeDeliverer{DBPath: db, Exec: cliExec{}}

			got, err := d.seen(context.Background(), "ses_x", origin, time.Time{})
			if err != nil {
				t.Fatalf("seen: %v", err)
			}
			if got != tc.want {
				t.Errorf("seen = %v, want %v", got, tc.want)
			}
		})
	}
}

// opencodeDeliverWant runs one Deliver and asserts its outcome and reason.
func opencodeDeliverWant(t *testing.T, d *OpencodeDeliverer, payload, ref string, queuedAt time.Time, want Outcome, wantReason string) {
	t.Helper()
	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), payload, ref, queuedAt)
	if err != nil {
		t.Fatalf("Deliver(%s): %v", ref, err)
	}
	if out != want || reason != wantReason {
		t.Fatalf("Deliver(%s) = (%v, %q), want (%v, %q)", ref, out, reason, want, wantReason)
	}
}

// opencodeConfirmWant runs one Confirm and asserts its outcome and reason.
func opencodeConfirmWant(t *testing.T, d *OpencodeDeliverer, payload string, queuedAt time.Time, want Outcome, wantReason string) {
	t.Helper()
	out, reason, err := d.Confirm(context.Background(), opencodeMasterMind("ses_abc123"), payload, queuedAt)
	if err != nil {
		t.Fatalf("Confirm(%s): %v", firstPayloadLine(payload), err)
	}
	if out != want || reason != wantReason {
		t.Fatalf("Confirm(%s) = (%v, %q), want (%v, %q)", firstPayloadLine(payload), out, reason, want, wantReason)
	}
}

// opencodePosts reports the POST count under mu.
func opencodePosts(mu *sync.Mutex, posts *int) int {
	mu.Lock()
	defer mu.Unlock()
	return *posts
}

// TestOpencodeDeliverPostsOnce pins the split between the push and the
// read-back: one Deliver POSTs once and reports the payload admitted, every
// following Confirm POSTs nothing, and the read-back alone reports delivered
// once the session holds the turn.
func TestOpencodeDeliverPostsOnce(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	db := sqliteFixture(t,
		"create table message (id text, session_id text, time_created integer, data text)",
		"create table part (id text, message_id text, session_id text, data text)",
		"create table session_message (id text, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)",
		"create table session_inbox (id text, session_id text, type text, payload text, delivery text, enqueued_seq integer, time_created integer)",
	)

	startTime := time.Unix(1000, 0)
	curTime := startTime
	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     db,
		Exec:       cliExec{},
		Alive:      aliveAlways,
		Now:        func() time.Time { return curTime },
	}
	payload1 := "relevo: round 1 · to MasterMind · payload 1\n\nbody 1"

	// The first Deliver admits the payload with exactly one POST.
	opencodeDeliverWant(t, d, payload1, "/x/001-report.md", startTime, OutcomeAdmitted, "posted; awaiting the session")
	if got := opencodePosts(&mu, &posts); got != 1 {
		t.Fatalf("after the admitting Deliver, got %d POSTs, want 1", got)
	}

	// While the session has not taken the turn, a read-back reports the payload
	// still admitted and never POSTs again.
	for i := 1; i <= 2; i++ {
		curTime = curTime.Add(2 * time.Second)
		opencodeConfirmWant(t, d, payload1, startTime, OutcomeAdmitted, "posted; awaiting the session")
		if got := opencodePosts(&mu, &posts); got != 1 {
			t.Fatalf("after confirm %d, got %d POSTs, want 1", i, got)
		}
	}

	// Once the row appears in session_inbox, the read-back reports delivered
	// with no new POST.
	out, err := exec.Command("sqlite3", db,
		"insert into session_inbox values ('inbox1', 'ses_abc123', 'message', '"+payload1+"', 'queue', 0, '"+strconv.FormatInt(startTime.UnixMilli(), 10)+"')").CombinedOutput()
	if err != nil {
		t.Fatalf("insert session_inbox: %v: %s", err, out)
	}
	curTime = curTime.Add(2 * time.Second)
	opencodeConfirmWant(t, d, payload1, startTime, OutcomeDelivered, "")
	if got := opencodePosts(&mu, &posts); got != 1 {
		t.Fatalf("after the delivered read-back, got %d POSTs, want 1", got)
	}

	// A second payload, with its own origin, is admitted by its own POST.
	curTime = curTime.Add(2 * time.Second)
	payload2 := "relevo: round 1 · to MasterMind · payload 2\n\nbody 2"
	opencodeDeliverWant(t, d, payload2, "/x/002-report.md", startTime, OutcomeAdmitted, "posted; awaiting the session")
	if got := opencodePosts(&mu, &posts); got != 2 {
		t.Fatalf("after payload 2, got %d POSTs, want 2", got)
	}
}

// TestOpencodeConfirmQueryBoundsRowsByQueuedAt pins the read-back's time bound:
// a non-zero queuedAt puts the not-before stamp on all three counted sources
// (the legacy pair's rides on the joined message row, which is the only one of
// the two that has a clock), and a zero queuedAt emits today's unbounded query.
// The ' escape and the hasInbox gate are the same either way.
func TestOpencodeConfirmQueryBoundsRowsByQueuedAt(t *testing.T) {
	t.Parallel()

	queuedAt := time.UnixMilli(1789021109130)
	notBefore := strconv.FormatInt(queuedAt.Add(-opencodeClockSkew).UnixMilli(), 10)

	unbounded := opencodeConfirmQuery("ses_x", "origin", true, time.Time{})
	if strings.Contains(unbounded, "time_created") {
		t.Errorf("a zero queuedAt emitted a time bound: %q", unbounded)
	}
	if !strings.Contains(unbounded, "session_inbox") {
		t.Errorf("unbounded query = %q, want the inbox source a 2.0 database has", unbounded)
	}

	bounded := opencodeConfirmQuery("ses_x", "origin", true, queuedAt)
	if !strings.Contains(bounded, "and m.time_created >= "+notBefore) {
		t.Errorf("bounded query = %q, want the legacy join bounded on m.time_created", bounded)
	}
	if n := strings.Count(bounded, "time_created >= "+notBefore); n != 3 {
		t.Errorf("bounded query bounds %d sources, want 3: %q", n, bounded)
	}
	if n := strings.Count(bounded, "like '%origin%'"); n != 3 {
		t.Errorf("bounded query matches the origin on %d sources, want 3: %q", n, bounded)
	}
	if strings.Contains(opencodeConfirmQuery("ses_x", "origin", false, queuedAt), "session_inbox") {
		t.Error("a database without session_inbox must not be queried for one")
	}
	quoted := opencodeConfirmQuery("ses_x", "o'rigin", true, queuedAt)
	if !strings.Contains(quoted, "like '%o''rigin%'") {
		t.Errorf("quoted query = %q, want the ' doubled", quoted)
	}
}

// TestOpencodeDeliverIgnoresOlderMatchingRow pins the bound against a real
// database. Two consult findings payloads for one binding name carry
// byte-identical origin lines -- the findings origin names no round -- so the
// first entry's row would answer "already present" for the second one forever.
// With the bound the older row does not count, the second entry POSTs once, and
// each entry retried with its own queuedAt still confirms its own row without a
// second POST.
func TestOpencodeDeliverIgnoresOlderMatchingRow(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	db := sqliteFixture(t,
		"create table message (id text, session_id text, time_created integer, data text)",
		"create table part (id text, message_id text, session_id text, data text)",
		"create table session_message (id text, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)",
		"create table session_inbox (id text, session_id text, type text, payload text, delivery text, enqueued_seq integer, time_created integer)",
	)

	firstAt := time.Unix(1000, 0)
	secondAt := firstAt.Add(time.Hour)
	curTime := firstAt
	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     db,
		Exec:       cliExec{},
		Alive:      aliveAlways,
		Now:        func() time.Time { return curTime },
	}

	// Both payloads are consult findings about the same runner, so their origin
	// lines are byte-identical and only the queue time tells them apart.
	origin := `relevo: consult · to MasterMind · about runner "ev-903-fix" (not the human)`
	payload1 := origin + "\n\nfindings 1"
	payload2 := origin + "\n\nfindings 2"

	enqueue := func(id, payload string, at time.Time) {
		t.Helper()
		out, err := exec.Command("sqlite3", db,
			"insert into session_inbox values ('"+id+"', 'ses_abc123', 'message', '"+payload+"', 'queue', 0, "+
				strconv.FormatInt(at.UnixMilli(), 10)+")").CombinedOutput()
		if err != nil {
			t.Fatalf("enqueue %s: %v: %s", id, err, out)
		}
	}

	// The first entry is admitted by its own POST and takes the session.
	opencodeDeliverWant(t, d, payload1, "/x/001-findings.md", firstAt, OutcomeAdmitted, "posted; awaiting the session")
	enqueue("inbox1", payload1, firstAt)
	if got := opencodePosts(&mu, &posts); got != 1 {
		t.Fatalf("after the first entry, got %d POSTs, want 1", got)
	}

	// The same entry retried with its own queuedAt still finds its own row.
	opencodeDeliverWant(t, d, payload1, "/x/001-findings.md", firstAt, OutcomeDelivered, "already present")
	if got := opencodePosts(&mu, &posts); got != 1 {
		t.Fatalf("after the first entry's retry, got %d POSTs, want 1", got)
	}

	// A second findings payload for the same runner, queued an hour later, must
	// not be answered by the first entry's row: it POSTs and is admitted.
	curTime = secondAt
	opencodeDeliverWant(t, d, payload2, "/x/002-findings.md", secondAt, OutcomeAdmitted, "posted; awaiting the session")
	if got := opencodePosts(&mu, &posts); got != 2 {
		t.Fatalf("after the second entry, got %d POSTs, want 2", got)
	}

	// And once the session holds its own row, its retry confirms without a POST.
	enqueue("inbox2", payload2, secondAt)
	opencodeDeliverWant(t, d, payload2, "/x/002-findings.md", secondAt, OutcomeDelivered, "already present")
	if got := opencodePosts(&mu, &posts); got != 2 {
		t.Fatalf("after the second entry's retry, got %d POSTs, want 2: a retry never re-sends", got)
	}
}

// TestOpencodeConfirmOnceNeverPolls pins the repeat tick's read-back: one query
// of the session and no poll. The fake only reports the origin seen from its
// fifth query on, so a poll would reach it and report delivered; a single
// read-back cannot, and leaves the payload admitted.
func TestOpencodeConfirmOnceNeverPolls(t *testing.T) {
	t.Parallel()

	exec := &fakeSqliteExec{seenFrom: 5}
	d := &OpencodeDeliverer{DBPath: filepath.Join(t.TempDir(), "opencode.db"), Exec: exec}

	out, reason, err := d.ConfirmOnce(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", time.Time{})
	if err != nil {
		t.Fatalf("ConfirmOnce: %v", err)
	}
	if out != OutcomeAdmitted || reason != "posted; awaiting the session" {
		t.Fatalf("out/reason = %v/%q, want OutcomeAdmitted/\"posted; awaiting the session\"", out, reason)
	}
	if exec.calls >= 5 {
		t.Fatalf("session queries = %d, want fewer than 5: a poll would have reached the seen count and delivered", exec.calls)
	}
	if exec.calls != 1 {
		t.Fatalf("session queries = %d, want exactly 1 read-back", exec.calls)
	}
}

// TestOpencodeConfirmOnceNeverPosts pins the other half of the repeat tick's
// contract: even with a live service file in reach -- the thing that lets the
// push half send -- ConfirmOnce makes no request at all. A send added to
// ConfirmOnce would show up here as a POST.
func TestOpencodeConfirmOnceNeverPosts(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	db := sqliteFixture(t,
		"create table message (id text, session_id text, time_created integer, data text)",
		"create table part (id text, message_id text, session_id text, data text)",
		"create table session_message (id text, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)",
		"create table session_inbox (id text, session_id text, type text, payload text, delivery text, enqueued_seq integer, time_created integer)",
	)

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     db,
		Exec:       cliExec{},
		Alive:      aliveAlways,
	}

	payload := "relevo: round 1 · to MasterMind · payload 1\n\nbody 1"
	out, reason, err := d.ConfirmOnce(context.Background(), opencodeMasterMind("ses_abc123"), payload, time.Time{})
	if err != nil {
		t.Fatalf("ConfirmOnce: %v", err)
	}
	if out != OutcomeAdmitted || reason != "posted; awaiting the session" {
		t.Fatalf("out/reason = %v/%q, want OutcomeAdmitted/\"posted; awaiting the session\"", out, reason)
	}
	if got := opencodePosts(&mu, &posts); got != 0 {
		t.Fatalf("POSTs = %d, want 0: a repeat tick reads back, it cannot send", got)
	}
}

// TestOpencodeConfirmOnceSeen pins the confirming read-back: the single query
// finds the origin, so the repeat tick reports the payload delivered.
func TestOpencodeConfirmOnceSeen(t *testing.T) {
	t.Parallel()

	exec := &fakeSqliteExec{seenFrom: 1}
	d := &OpencodeDeliverer{DBPath: filepath.Join(t.TempDir(), "opencode.db"), Exec: exec}

	out, reason, err := d.ConfirmOnce(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", time.Time{})
	if err != nil {
		t.Fatalf("ConfirmOnce: %v", err)
	}
	if out != OutcomeDelivered || reason != "" {
		t.Fatalf("out/reason = %v/%q, want OutcomeDelivered and an empty reason", out, reason)
	}
	if exec.calls != 1 {
		t.Fatalf("session queries = %d, want exactly 1 read-back", exec.calls)
	}
}

// TestOpencodeConfirmOnceRefusesWhatItCannotRead pins the repeat tick's guards
// and its read error: the single query is attempted only for the mastermind the
// other halves accept, and a failing sqlite3 is unavailable rather than
// delivered.
func TestOpencodeConfirmOnceRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		mastermind store.Endpoint
		noExec     bool
		reason     string
	}{
		{name: "claude mastermind", mastermind: store.Endpoint{Kind: "claude", SessionID: "ses_abc123"}, reason: ""},
		{name: "empty session id", mastermind: store.Endpoint{Kind: "opencode", SessionID: ""}, reason: "no opencode session id"},
		{name: "malformed session id", mastermind: store.Endpoint{Kind: "opencode", SessionID: "ses_bad!id"}, reason: "no opencode session id"},
		{name: "nil Exec", mastermind: store.Endpoint{Kind: "opencode", SessionID: "ses_abc123"}, noExec: true, reason: "no sqlite3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &OpencodeDeliverer{DBPath: filepath.Join(t.TempDir(), "opencode.db")}
			if !tc.noExec {
				d.Exec = &fakeSqliteExec{}
			}

			out, reason, err := d.ConfirmOnce(context.Background(), tc.mastermind, "relevo: round 1\n\nbody", time.Time{})
			if err != nil {
				t.Fatalf("ConfirmOnce: %v", err)
			}
			if out != OutcomeNotMine {
				t.Fatalf("out = %v, want OutcomeNotMine", out)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}

	t.Run("a failing sqlite3 is unavailable", func(t *testing.T) {
		d := &OpencodeDeliverer{
			DBPath: filepath.Join(t.TempDir(), "opencode.db"),
			Exec:   &fakeSqliteExec{err: errors.New("boom: no such table\nsecond line")},
		}

		out, reason, err := d.ConfirmOnce(context.Background(), opencodeMasterMind("ses_abc123"), "relevo: round 1\n\nbody", time.Time{})
		if err != nil {
			t.Fatalf("ConfirmOnce: %v", err)
		}
		if out != OutcomeUnavailable || reason != "sqlite3: boom: no such table" {
			t.Fatalf("out/reason = %v/%q, want OutcomeUnavailable and the first error line", out, reason)
		}
	})
}
