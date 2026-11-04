package serve

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// freezeRepo makes every dir and file under root read-only, so a git write
// fails while reads still work. The cleanup restores write access.
func freezeRepo(t *testing.T, root string) {
	t.Helper()
	freeze := func(mode fs.FileMode) func(path string, d fs.DirEntry, err error) error {
		return func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return os.Chmod(path, mode)
			}
			return os.Chmod(path, mode&0o666)
		}
	}
	if err := filepath.WalkDir(root, freeze(0o555)); err != nil {
		t.Fatalf("freeze repo: %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, freeze(0o755))
	})
}

// forwardEnv is a head with bursting on and one local seat: the seat filler
// below leaves every round the test sends queued, so it can be placed onto
// a node.
func forwardEnv(t *testing.T, headKP remote.Keypair) *testEnv {
	t.Helper()
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	return setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		cfg.MaxBuilders = 1
	})
}

// placeForward sends one round through the head so it queues behind the full
// local seat, then places it as a sending forward: the shape placeForwards
// writes before the send, with a real plan file, bare repo and out ref
// behind it. The node binding is renamed to a fixed name so a scripted node
// can answer to it whatever owner key the test generated.
func placeForward(t *testing.T, env *testEnv, name string, now time.Time) store.Forward {
	t.Helper()
	saveBurstBinding(t, env.runtime(t), "zz", time.Time{}, 4242)
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, name, "# Plan for "+name)
	requireCreated(t, resp, body, name)
	if got := env.srv.placeForwards(now); got != 1 {
		t.Fatalf("placed = %d, want 1", got)
	}
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, name)
	fwd := burstStoredForward(b)
	if fwd == nil {
		t.Fatalf("%s forward = nil, want a sending claim", name)
	}
	fwd.NodeBinding = "nb-" + name
	b.Burst.Forward = fwd
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("rename node binding: %v", err)
	}
	return *fwd
}

// queueForward sets a single-row table and places one forward on it.
func queueForward(t *testing.T, env *testEnv, name string, row burst.NodeRow, now time.Time) store.Forward {
	t.Helper()
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
	return placeForward(t, env, name, now)
}

// stubRow pins one verified ready row for a scripted node.
func stubRow(t *testing.T, id, url, fp string) burst.NodeRow {
	t.Helper()
	row := burstRowFor(id, url, fp)
	row.Verified = true
	return row
}

// startBurstNodeWithRunner is startBurstNode with a script runner behind
// it. The node's round preflight refuses without one, so a forwarded round
// only starts against a node that can run it.
func startBurstNodeWithRunner(t *testing.T, enroll *remote.Keypair) (*Server, *httptest.Server, string) {
	t.Helper()
	nodeSrv, ts, fp := startBurstNode(t, enroll)
	runner := newScriptRunner()
	nodeSrv.cfg.Runner = runner
	nodeSrv.mu.Lock()
	nodeSrv.reloaded.Runner = runner
	nodeSrv.mu.Unlock()
	return nodeSrv, ts, fp
}

// startStubNode runs a scripted TLS node: the head's production client pins
// the fingerprint, while the handler answers whatever the case scripts. A
// real node cannot produce refusals or server failures on demand, so the
// refusal and failure cases script one here behind the same pinned TLS.
func startStubNode(t *testing.T, handler http.Handler) (string, string) {
	t.Helper()
	db := testServeDB(t)
	fp, err := InitTLS(SecretStore{DB: db}, []string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatalf("stub InitTLS: %v", err)
	}
	cert, err := LoadTLS(SecretStore{DB: db})
	if err != nil {
		t.Fatalf("stub LoadTLS: %v", err)
	}
	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts.URL, fp
}

// closedPortURL is a URL nothing listens on: dialling it refuses fast, so
// a case that must not dial stays fast and any dial would mark the row.
func closedPortURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return "http://" + addr
}

// nodeScript is one scripted node's answers: each endpoint answers its
// status with a view or an error body, and every hit is counted so a test
// can prove a node was or was not dialled. redirect, when set to an exact
// path, answers that path with a redirect instead.
type nodeScript struct {
	hits         atomic.Int64
	getStatus    int
	getView      remote.BindingView
	getCode      remote.Code
	getMsg       string
	createStatus int
	createCode   remote.Code
	createMsg    string
	startStatus  int
	startView    remote.BindingView
	startCode    remote.Code
	startMsg     string
	stopStatus   int
	stopView     remote.BindingView
	stopCode     remote.Code
	stopMsg      string
	// stopWait, when non-nil, holds the stop answer until the test closes
	// it, so the head's return can be observed before the node answers.
	stopWait chan struct{}
	// stopHits counts the stop answers, apart from every other hit, so a
	// test can say exactly how many stops the node saw.
	stopHits atomic.Int64
	// unavailableStatus answers the node-side unavailable; zero answers 200.
	// unavailableWait, when non-nil, holds the answer until the test closes
	// it, so a gate's return can be observed before the node answers. The
	// hold also ends when the request's context does, so a test that fails
	// before releasing never wedges the stub's close in cleanup.
	unavailableStatus int
	unavailableMsg    string
	unavailableWait   chan struct{}
	// unavailableHits counts the unavailable answers, apart from every
	// other hit, so a test can say exactly how many gates the node saw.
	unavailableHits atomic.Int64
	// availableStatus answers the node-side available; zero answers 200.
	// availableWait ends with the request's context too, for the same
	// cleanup reason as unavailableWait.
	availableStatus int
	availableMsg    string
	availableWait   chan struct{}
	// availableHits counts the available answers, apart from every other
	// hit.
	availableHits atomic.Int64
	// gateMu guards gates, the gates a scripted node received in arrival
	// order, so a test can say the head forwarded the gate it recorded.
	gateMu sync.Mutex
	gates  []gateSeen
	// unbindStatus answers the node-side unbind; zero answers 200.
	unbindStatus int
	unbindCode   remote.Code
	unbindMsg    string
	// unbindWait, when non-nil, holds the unbind answer until the test
	// closes it, so a release's non-blocking can be observed.
	unbindWait chan struct{}
	// unbindHits counts the unbind answers, apart from every other hit.
	unbindHits atomic.Int64
	// startBodies records the bytes each round start carried, so a test can
	// tell a full bundle from an incremental or empty one.
	startMu     sync.Mutex
	startBodies [][]byte
	redirect    string
}

// recordStart keeps one start request's body for bundle assertions.
func (s *nodeScript) recordStart(body []byte) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.startBodies = append(s.startBodies, body)
}

// gateSeen is one gate a scripted node received: the token the unavailable
// carried, or the subject the available carried with an empty reason.
type gateSeen struct {
	token  string
	reason string
}

// recordGate keeps one received gate for value assertions: the head
// forwards the gate it recorded, not just a knock on the node's door.
func (s *nodeScript) recordGate(seen gateSeen) {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	s.gates = append(s.gates, seen)
}

// gateTokens reports every gate the node received, in arrival order.
func (s *nodeScript) gateTokens() []gateSeen {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	return append([]gateSeen(nil), s.gates...)
}

func (s *nodeScript) serve() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if s.redirect != "" && r.URL.Path == s.redirect {
			http.Redirect(w, r, "https://elsewhere.example/rounds", http.StatusFound)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/bindings/"):
			if s.getStatus >= 400 {
				writeJSON(w, s.getStatus, remote.ErrorBody{Code: s.getCode, Message: s.getMsg})
			} else {
				writeJSON(w, s.getStatus, s.getView)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/bindings":
			if s.createStatus >= 400 {
				writeJSON(w, s.createStatus, remote.ErrorBody{Code: s.createCode, Message: s.createMsg})
			} else {
				writeJSON(w, s.createStatus, s.getView)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/unbind"):
			s.unbindHits.Add(1)
			if s.unbindWait != nil {
				<-s.unbindWait
			}
			status := s.unbindStatus
			if status == 0 {
				status = http.StatusOK
			}
			if status >= 400 {
				writeJSON(w, status, remote.ErrorBody{Code: s.unbindCode, Message: s.unbindMsg})
			} else {
				writeJSON(w, status, map[string]any{})
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rounds"):
			body, _ := io.ReadAll(r.Body)
			s.recordStart(body)
			if s.startStatus >= 400 {
				writeJSON(w, s.startStatus, remote.ErrorBody{Code: s.startCode, Message: s.startMsg})
			} else {
				writeJSON(w, s.startStatus, s.startView)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			s.stopHits.Add(1)
			if s.stopWait != nil {
				<-s.stopWait
			}
			if s.stopStatus >= 400 {
				writeJSON(w, s.stopStatus, remote.ErrorBody{Code: s.stopCode, Message: s.stopMsg})
			} else {
				writeJSON(w, s.stopStatus, s.stopView)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/unavailable":
			s.unavailableHits.Add(1)
			var req remote.UnavailableRequest
			if body, err := io.ReadAll(r.Body); err == nil {
				_ = json.Unmarshal(body, &req)
			}
			s.recordGate(gateSeen{token: req.Token, reason: req.Reason})
			if s.unavailableWait != nil {
				select {
				case <-s.unavailableWait:
				case <-r.Context().Done():
				}
			}
			status := s.unavailableStatus
			if status == 0 {
				status = http.StatusOK
			}
			if status >= 400 {
				writeJSON(w, status, remote.ErrorBody{Message: s.unavailableMsg})
			} else {
				writeJSON(w, status, map[string]any{})
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/available":
			s.availableHits.Add(1)
			var req remote.AvailableRequest
			if body, err := io.ReadAll(r.Body); err == nil {
				_ = json.Unmarshal(body, &req)
			}
			s.recordGate(gateSeen{token: req.Subject})
			if s.availableWait != nil {
				select {
				case <-s.availableWait:
				case <-r.Context().Done():
				}
			}
			status := s.availableStatus
			if status == 0 {
				status = http.StatusOK
			}
			if status >= 400 {
				writeJSON(w, status, remote.ErrorBody{Message: s.availableMsg})
			} else {
				writeJSON(w, status, remote.AvailableResponse{})
			}
		default:
			http.NotFound(w, r)
		}
	})
}

// lastStartHasBundle reports whether the newest recorded start carried a
// bundle part: the ship's full cut rides the form, an empty incremental one
// does not.
func (s *nodeScript) lastStartHasBundle() bool {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if len(s.startBodies) == 0 {
		return false
	}
	return bytes.Contains(s.startBodies[len(s.startBodies)-1], []byte("bundle.bundle"))
}

// stubRoundView is the minimal view a scripted node answers.
func stubRoundView(name string, round int, state remote.RoundState) remote.BindingView {
	return remote.BindingView{Name: name, Round: round, RoundState: state}
}

// TestBurstSendConfirmsRunning pins the sending-to-running path: against a
// real node the create and the start both land, and the confirm sets the
// phase, the sent stamp, the ship base, the node binding and the row's
// forwarded mark. A standing row problem proves a success clears it.
func TestBurstSendConfirmsRunning(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	_, nodeTS, nodeFP := startBurstNodeWithRunner(t, &headKP)
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	row := stubRow(t, "b1", nodeTS.URL, nodeFP)
	fwd := queueForward(t, env, "api", row, clock)

	// A standing row problem proves a success clears it. It is set after the
	// placement, which takes no forward for a troubled row.
	env.srv.mu.Lock()
	env.srv.burstTable.Nodes[0].Problem = burst.ReportProblemUnreachable
	env.srv.burstTable.Nodes[0].ProblemSince = clock.Add(-time.Minute)
	env.srv.mu.Unlock()

	if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}

	rt := env.runtime(t)
	back := loadBurstBinding(t, rt, "api")
	have := burstStoredForward(back)
	if have == nil {
		t.Fatalf("api forward = nil, want it confirmed running on b1")
	}
	if have.Phase != "running" || have.Node != "b1" || have.Round != 1 {
		t.Errorf("api forward = %+v, want running on b1 round 1", have)
	}
	if !have.SentAt.Equal(clock) {
		t.Errorf("SentAt = %v, want %v", have.SentAt, clock)
	}
	if back.Burst == nil {
		t.Fatalf("Burst = nil, want the ship base and node binding recorded")
	}
	outSHA, ok, rerr := env.srv.cfg.Git.RefSHA(context.Background(), back.Serve.BareRepo, "refs/relevo/api/out")
	if rerr != nil || !ok || back.Burst.Shipped["b1"] != outSHA {
		t.Errorf("Shipped[b1] = %q, want out ref %q (ok=%v err=%v)", back.Burst.Shipped["b1"], outSHA, ok, rerr)
	}
	if len(back.Burst.NodeBindings) != 1 || back.Burst.NodeBindings[0] != fwd.NodeBinding {
		t.Errorf("NodeBindings = %+v, want [%q]", back.Burst.NodeBindings, fwd.NodeBinding)
	}
	tab := env.srv.copyBurstTable().Nodes[0]
	if tab.ForwardedAt == nil || !tab.ForwardedAt.Equal(clock) {
		t.Errorf("ForwardedAt = %v, want %v", tab.ForwardedAt, clock)
	}
	if tab.Problem != "" || !tab.ProblemSince.IsZero() {
		t.Errorf("Problem = %q since %v, want the success to clear both", tab.Problem, tab.ProblemSince)
	}

	cl, cerr := env.srv.burstNodeClient([]burst.NodeRow{row})
	if cerr != nil {
		t.Fatalf("node client: %v", cerr)
	}
	view, verr := cl.GetBinding(context.Background(), "b1", fwd.NodeBinding)
	if verr != nil {
		t.Fatalf("node GetBinding %s: %v", fwd.NodeBinding, verr)
	}
	if view.Round != 1 {
		t.Errorf("node round = %d, want the forwarded round 1", view.Round)
	}
}

// answerSetup queues one forward against a scripted node and returns the
// pieces the answer cases assert on.
func answerSetup(t *testing.T, script *nodeScript, clock time.Time) (*testEnv, burst.NodeRow, store.Forward) {
	t.Helper()
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	url, fp := startStubNode(t, script.serve())
	row := stubRow(t, "b1", url, fp)
	fwd := queueForward(t, env, "api", row, clock)
	return env, row, fwd
}

// requireRequeued pins the refusal path: the forward is gone and the round
// waits locally again at the FIFO place it held before the claim.
func requireRequeued(t *testing.T, env *testEnv, fwd store.Forward) {
	t.Helper()
	back := loadBurstBinding(t, env.runtime(t), "api")
	if burstStoredForward(back) != nil {
		t.Errorf("api forward = %+v, want it cleared", back.Burst.Forward)
	}
	if !back.QueuedAt.Equal(fwd.QueuedSince) {
		t.Errorf("api QueuedAt = %v, want the original %v", back.QueuedAt, fwd.QueuedSince)
	}
}

// requireStillSending pins the stay path: the claim is untouched and the
// round stays out.
func requireStillSending(t *testing.T, env *testEnv, name string) {
	t.Helper()
	fwd := burstStoredForward(loadBurstBinding(t, env.runtime(t), name))
	if fwd == nil || fwd.Phase != "sending" {
		t.Errorf("%s forward = %+v, want the sending claim kept", name, fwd)
	}
}

// TestBurstSendAnswers pins every StartRound and create answer: a started
// round and an already-started answer both confirm, a definite refusal
// requeues to the FIFO place with the node's message logged, and a failure
// or a redirect keeps the claim for the next tick.
func TestBurstSendAnswers(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	running := stubRoundView("nb-api", 1, remote.RoundRunning)

	t.Run("409 started confirms", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
			createStatus: http.StatusCreated,
			startStatus:  http.StatusConflict, startCode: remote.CodeRoundStarted, startMsg: "round 1 already started",
		}
		env, _, fwd := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
			t.Fatalf("confirmed = %d, want 1", got)
		}
		back := loadBurstBinding(t, env.runtime(t), "api")
		have := burstStoredForward(back)
		if have == nil || have.Phase != "running" || have.Node != "b1" {
			t.Errorf("api forward = %+v, want running on b1", have)
		}
		if back.Burst == nil || back.Burst.Shipped["b1"] == "" || len(back.Burst.NodeBindings) != 1 || back.Burst.NodeBindings[0] != fwd.NodeBinding {
			t.Errorf("Burst = %+v, want the ship base and node binding recorded", back.Burst)
		}
	})

	t.Run("create conflict proceeds to the start", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
			createStatus: http.StatusConflict, createCode: remote.CodeInvalid, createMsg: "binding exists",
			startStatus: http.StatusCreated, startView: running,
		}
		env, _, _ := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
			t.Fatalf("confirmed = %d, want 1", got)
		}
		requireConfirmedRunning(t, env)
	})

	t.Run("422 requeues with its FIFO place", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusOK, getView: running,
			startStatus: http.StatusUnprocessableEntity, startCode: remote.CodeInvalid, startMsg: "tier above max",
		}
		env, _, fwd := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireRequeued(t, env, fwd)
	})

	t.Run("create refusal requeues", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
			createStatus: http.StatusUnprocessableEntity, createCode: remote.CodeInvalid, createMsg: "bad repo",
		}
		env, _, fwd := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireRequeued(t, env, fwd)
	})

	t.Run("500 stays sending and marks unreachable", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusOK, getView: running,
			startStatus: http.StatusInternalServerError, startCode: remote.Code("internal"), startMsg: "boom",
		}
		env, _, _ := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
		first := env.srv.copyBurstTable().Nodes[0]
		if first.Problem != burst.ReportProblemUnreachable || first.ProblemSince.IsZero() {
			t.Fatalf("Problem = %q since %v, want unreachable stamped", first.Problem, first.ProblemSince)
		}
		if got := env.srv.sendForwards(context.Background(), clock.Add(time.Minute)); got != 0 {
			t.Fatalf("confirmed on retry = %d, want 0", got)
		}
		second := env.srv.copyBurstTable().Nodes[0]
		if second.Problem != burst.ReportProblemUnreachable || !second.ProblemSince.Equal(first.ProblemSince) {
			t.Errorf("Problem = %q since %v, want the first failure kept", second.Problem, second.ProblemSince)
		}
		b := loadBurstBinding(t, env.runtime(t), "api")
		if _, ok, err := env.srv.cfg.Git.RefSHA(context.Background(), b.Serve.BareRepo, "refs/relevo/nb-api/out"); err != nil || ok {
			t.Errorf("forward ref present (ok=%v err=%v), want the failed send to restore the staged ref", ok, err)
		}
	})

	t.Run("create failure stays sending", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
			createStatus: http.StatusInternalServerError, createCode: remote.Code("internal"), createMsg: "boom",
		}
		env, _, _ := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
		if got := env.srv.copyBurstTable().Nodes[0].Problem; got != burst.ReportProblemUnreachable {
			t.Errorf("Problem = %q, want unreachable", got)
		}
	})

	t.Run("node down stays sending and retries", func(t *testing.T) {
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		queueForward(t, env, "api", stubRow(t, "b1", closedPortURL(t), burstTestFingerprint), clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
		if got := env.srv.copyBurstTable().Nodes[0].Problem; got != burst.ReportProblemUnreachable {
			t.Errorf("Problem = %q, want unreachable", got)
		}
		b := loadBurstBinding(t, env.runtime(t), "api")
		if _, ok, err := env.srv.cfg.Git.RefSHA(context.Background(), b.Serve.BareRepo, "refs/relevo/nb-api/out"); err != nil || ok {
			t.Errorf("forward ref present (ok=%v err=%v), want the unreachable send to restore the staged ref", ok, err)
		}
	})

	t.Run("check refusal requeues", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusUnauthorized, getCode: remote.CodeNotEnrolled, getMsg: "not enrolled",
		}
		env, _, fwd := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireRequeued(t, env, fwd)
		if got := env.srv.copyBurstTable().Nodes[0].Problem; got != "" {
			t.Errorf("Problem = %q, want no mark for a refused check", got)
		}
		rt := env.runtime(t)
		b := loadBurstBinding(t, rt, "api")
		if _, ok, err := env.srv.cfg.Git.RefSHA(context.Background(), b.Serve.BareRepo, "refs/relevo/nb-api/out"); err != nil || ok {
			t.Errorf("forward ref present (ok=%v err=%v), want the refused check to restore the staged ref", ok, err)
		}
	})

	t.Run("redirect stays sending without a problem", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusOK, getView: running,
			startStatus: http.StatusCreated, startView: running,
			redirect: "/v1/bindings/nb-api/rounds",
		}
		env, _, _ := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
		if got := env.srv.copyBurstTable().Nodes[0].Problem; got != "" {
			t.Errorf("Problem = %q, want no mark for a redirect", got)
		}
		b := loadBurstBinding(t, env.runtime(t), "api")
		if _, ok, err := env.srv.cfg.Git.RefSHA(context.Background(), b.Serve.BareRepo, "refs/relevo/nb-api/out"); err != nil || ok {
			t.Errorf("forward ref present (ok=%v err=%v), want the redirected send to restore the staged ref", ok, err)
		}
	})

	t.Run("create redirect stays sending without a problem", func(t *testing.T) {
		script := &nodeScript{
			getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
			redirect: "/v1/bindings",
		}
		env, _, _ := answerSetup(t, script, clock)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
		if got := env.srv.copyBurstTable().Nodes[0].Problem; got != "" {
			t.Errorf("Problem = %q, want no mark for a redirect", got)
		}
	})
}

// TestBurstSendRefusalRestoresForwardRef pins the staged-ref restore: a
// refused send removes the ref it staged, so a later send with new work
// stages fresh instead of wedging on the leftover. Without the restore the
// second send never dials: the guard rejects a ref that is neither the
// ship base nor the out ref, and the round stays sending.
func TestBurstSendRefusalRestoresForwardRef(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	script := &nodeScript{
		getStatus: http.StatusOK, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		startStatus: http.StatusUnprocessableEntity, startCode: remote.CodeInvalid, startMsg: "tier above max",
	}
	env, _, fwd := answerSetup(t, script, clock)
	ctx := context.Background()
	rt := env.runtime(t)
	if got := env.srv.sendForwards(ctx, clock); got != 0 {
		t.Fatalf("confirmed = %d, want 0", got)
	}
	requireRequeued(t, env, fwd)

	b := loadBurstBinding(t, rt, "api")
	fwdRef := "refs/relevo/nb-api/out"
	if _, ok, err := env.srv.cfg.Git.RefSHA(ctx, b.Serve.BareRepo, fwdRef); err != nil || ok {
		t.Errorf("forward ref present (ok=%v err=%v), want the refusal to remove the staged ref", ok, err)
	}

	outSHA, ok, err := env.srv.cfg.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/relevo/api/out")
	if err != nil || !ok {
		t.Fatalf("out ref: %v, ok=%v", err, ok)
	}
	tree := strings.TrimSpace(runGit(t, b.Serve.BareRepo, "rev-parse", outSHA+"^{tree}"))
	next, err := rt.Git.CommitTree(ctx, b.Serve.BareRepo, tree, outSHA, "round 2 work")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	if err := rt.Git.UpdateRef(ctx, b.Serve.BareRepo, "refs/relevo/api/out", next, outSHA); err != nil {
		t.Fatalf("advance out ref: %v", err)
	}
	// A new round queues at its own time; the refusal hold names round 1's
	// stamp, so it does not hold round 2.
	round2 := loadBurstBinding(t, rt, "api")
	round2.Round = 2
	round2.QueuedAt = clock.Add(30 * time.Second)
	if err := rt.Store.Save(round2); err != nil {
		t.Fatalf("advance round: %v", err)
	}
	writeHeadPlan(t, env, "api", 2)
	if got := env.srv.placeForwards(clock.Add(time.Minute)); got != 1 {
		t.Fatalf("placed = %d, want 1", got)
	}
	// The re-place mints the deterministic owner-prefixed name again, so
	// keep the fixed valid name: production reuses the same node binding
	// across attempts, which is what wedges on a leftover staged ref.
	replaced := loadBurstBinding(t, rt, "api")
	replaced.Burst.Forward.NodeBinding = "nb-api"
	if err := rt.Store.Save(replaced); err != nil {
		t.Fatalf("rename node binding: %v", err)
	}
	script.getView = stubRoundView("nb-api", 2, remote.RoundRunning)
	script.startStatus = http.StatusCreated
	script.startView = stubRoundView("nb-api", 2, remote.RoundRunning)
	before := script.hits.Load()
	if got := env.srv.sendForwards(ctx, clock.Add(2*time.Minute)); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	if got := script.hits.Load(); got <= before {
		t.Errorf("node hits = %d, want the re-placed round dialled", got)
	}
	requireConfirmedRunning(t, env)
}

// TestBurstSendRefusalRestoresPriorForwardRef pins the restore's present
// arm: a forward ref that already pointed at the ship base comes back to
// that value after a refusal, instead of keeping the staged out sha.
func TestBurstSendRefusalRestoresPriorForwardRef(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	script := &nodeScript{
		getStatus: http.StatusOK, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		startStatus: http.StatusUnprocessableEntity, startCode: remote.CodeInvalid, startMsg: "tier above max",
	}
	env, _, fwd := answerSetup(t, script, clock)
	ctx := context.Background()
	rt := env.runtime(t)

	b := loadBurstBinding(t, rt, "api")
	outSHA, ok, err := env.srv.cfg.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/relevo/api/out")
	if err != nil || !ok {
		t.Fatalf("out ref: %v, ok=%v", err, ok)
	}
	tree := strings.TrimSpace(runGit(t, b.Serve.BareRepo, "rev-parse", outSHA+"^{tree}"))
	prior, err := rt.Git.CommitTree(ctx, b.Serve.BareRepo, tree, outSHA, "prior ship")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	fwdRef := "refs/relevo/nb-api/out"
	if err := rt.Git.UpdateRef(ctx, b.Serve.BareRepo, fwdRef, prior, ""); err != nil {
		t.Fatalf("seed forward ref: %v", err)
	}
	b.Burst.Shipped = map[string]string{"b1": prior}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("seed ship base: %v", err)
	}

	if got := env.srv.sendForwards(ctx, clock); got != 0 {
		t.Fatalf("confirmed = %d, want 0", got)
	}
	requireRequeued(t, env, fwd)
	if cur, ok, err := env.srv.cfg.Git.RefSHA(ctx, b.Serve.BareRepo, fwdRef); err != nil || !ok || cur != prior {
		t.Errorf("forward ref = %q (ok=%v err=%v), want the prior %q back", cur, ok, err, prior)
	}
}

// TestRestoreForwardStageConflict pins the restore's concurrent-writer arm:
// a compare-and-swap that loses only warns, and the ref keeps the newer
// stage instead of coming back to the prior value.
func TestRestoreForwardStageConflict(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	queueForward(t, env, "api", stubRow(t, "b1", closedPortURL(t), burstTestFingerprint), clock)
	ctx := context.Background()
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "api")

	outSHA, ok, rerr := env.srv.cfg.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/relevo/api/out")
	if rerr != nil || !ok {
		t.Fatalf("out ref: %v, ok=%v", rerr, ok)
	}
	tree := strings.TrimSpace(runGit(t, b.Serve.BareRepo, "rev-parse", outSHA+"^{tree}"))
	staged, err := rt.Git.CommitTree(ctx, b.Serve.BareRepo, tree, outSHA, "staged")
	if err != nil {
		t.Fatalf("CommitTree staged: %v", err)
	}
	moved, err := rt.Git.CommitTree(ctx, b.Serve.BareRepo, tree, staged, "concurrent stage")
	if err != nil {
		t.Fatalf("CommitTree concurrent: %v", err)
	}
	fwdRef := "refs/relevo/nb-api/out"
	if err := rt.Git.UpdateRef(ctx, b.Serve.BareRepo, fwdRef, outSHA, ""); err != nil {
		t.Fatalf("seed forward ref: %v", err)
	}
	stage := &forwardStage{bare: b.Serve.BareRepo, ref: fwdRef, prior: outSHA, hadPrior: true, staged: staged}
	if err := rt.Git.UpdateRef(ctx, b.Serve.BareRepo, fwdRef, staged, outSHA); err != nil {
		t.Fatalf("stage forward ref: %v", err)
	}
	if err := rt.Git.UpdateRef(ctx, b.Serve.BareRepo, fwdRef, moved, staged); err != nil {
		t.Fatalf("concurrent stage: %v", err)
	}

	restoreForwardStage(ctx, rt, stage)
	if cur, ok, err := env.srv.cfg.Git.RefSHA(ctx, b.Serve.BareRepo, fwdRef); err != nil || !ok || cur != moved {
		t.Errorf("forward ref = %q (ok=%v err=%v), want the concurrent stage %q kept", cur, ok, err, moved)
	}

	restoreForwardStage(ctx, rt, nil)
}

// requireConfirmedRunning pins the confirm: the forward runs with the sent
// stamp and the row carries the forwarded mark.
func requireConfirmedRunning(t *testing.T, env *testEnv) {
	t.Helper()
	back := loadBurstBinding(t, env.runtime(t), "api")
	have := burstStoredForward(back)
	if have == nil || have.Phase != "running" {
		t.Errorf("api forward = %+v, want running", have)
	}
}

// TestBurstSendSkipsUndialable pins the node filter: a forward lands only on
// a verified ready row, so a row that lost either mark after the placement
// is never dialled. Dropping either term dials the stub and fails the hit
// count.
func TestBurstSendSkipsUndialable(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	running := stubRoundView("nb-api", 1, remote.RoundRunning)

	flip := func(t *testing.T, name string, flip func(*burst.NodeRow)) {
		t.Helper()
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		script := &nodeScript{
			getStatus: http.StatusOK, getView: running,
			startStatus: http.StatusCreated, startView: running,
		}
		url, fp := startStubNode(t, script.serve())
		queueForward(t, env, "api", stubRow(t, "b1", url, fp), clock)
		env.srv.mu.Lock()
		flip(&env.srv.burstTable.Nodes[0])
		env.srv.mu.Unlock()
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("%s: confirmed = %d, want 0", name, got)
		}
		requireStillSending(t, env, "api")
		if got := script.hits.Load(); got != 0 {
			t.Errorf("%s: node hits = %d, want the node never dialled", name, got)
		}
	}

	t.Run("unverified", func(t *testing.T) {
		flip(t, "unverified", func(r *burst.NodeRow) { r.Verified = false })
	})
	t.Run("draining", func(t *testing.T) {
		flip(t, "draining", func(r *burst.NodeRow) { r.State = "draining" })
	})
}

// TestBurstSendSuperseded pins the confirm-time match: the node's yes for a
// claim the head already moved past -- here the round advanced -- applies
// nothing. Dropping the match check would confirm the stale forward.
func TestBurstSendSuperseded(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	script := &nodeScript{
		getStatus: http.StatusOK, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		startStatus: http.StatusCreated, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
	}
	url, fp := startStubNode(t, script.serve())
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	queueForward(t, env, "api", stubRow(t, "b1", url, fp), clock)

	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "api")
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("advance round: %v", err)
	}

	if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
		t.Fatalf("confirmed = %d, want 0", got)
	}
	if got := script.hits.Load(); got == 0 {
		t.Error("node hits = 0, want the send attempted before the match refused it")
	}
	requireStillSending(t, env, "api")
	back := loadBurstBinding(t, rt, "api")
	if back.Burst == nil || len(back.Burst.Shipped) != 0 || len(back.Burst.NodeBindings) != 0 {
		t.Errorf("Burst = %+v, want nothing recorded for a superseded claim", back.Burst)
	}
	if tab := env.srv.copyBurstTable().Nodes[0]; tab.ForwardedAt != nil {
		t.Errorf("ForwardedAt = %v, want no mark for a superseded claim", tab.ForwardedAt)
	}
	if _, ok, err := env.srv.cfg.Git.RefSHA(context.Background(), back.Serve.BareRepo, "refs/relevo/nb-api/out"); err != nil || ok {
		t.Errorf("forward ref present (ok=%v err=%v), want the superseded send to restore the staged ref", ok, err)
	}
}

// saveSendableBinding stores a head binding with a sending forward and the
// given serve facts, without any plan or repo behind it: the input cases
// fill in only what their arm reads.
func saveSendableBinding(t *testing.T, env *testEnv, name string, serve *store.ServeFacts, since time.Time) {
	t.Helper()
	cwd := filepath.Join(t.TempDir(), name+"-wt")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	b := store.Binding{
		Name: name, CWD: cwd, State: store.StateActive, Round: 1,
		BuilderCandidate: "claude/anthropic/haiku",
		Serve:            serve,
		Burst: &store.BurstFacts{Forward: &store.Forward{
			Node: "b1", NodeBinding: "nb-" + name, Round: 1,
			Phase: "sending", ClaimedAt: since.Add(time.Minute), QueuedSince: since,
		}},
	}
	if err := env.runtime(t).Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// writeHeadPlan stages the round's plan file the send ships.
func writeHeadPlan(t *testing.T, env *testEnv, name string, round int) {
	t.Helper()
	path := env.runtime(t).Store.PromptPath(name, round)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Plan for "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBurstSendStaysSendingOnBadInputs pins the ship guards: every missing
// input keeps the claim without dialling. The rows point at a closed port,
// so any dial would mark the row unreachable and fail the problem check.
func TestBurstSendStaysSendingOnBadInputs(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	since := clock.Add(-time.Minute)
	repoID := strings.Repeat("a", 64)

	setup := func(t *testing.T) (*testEnv, burst.NodeRow) {
		t.Helper()
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		row := stubRow(t, "b1", closedPortURL(t), burstTestFingerprint)
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}
		return env, row
	}
	send := func(t *testing.T, env *testEnv) {
		t.Helper()
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
		if got := env.srv.copyBurstTable().Nodes[0].Problem; got != "" {
			t.Errorf("Problem = %q, want no mark: nothing may have dialled", got)
		}
	}

	t.Run("no repo facts", func(t *testing.T) {
		env, _ := setup(t)
		saveSendableBinding(t, env, "api", nil, since)
		send(t, env)
	})

	t.Run("plan missing", func(t *testing.T) {
		env, _ := setup(t)
		bare := t.TempDir()
		if err := env.srv.cfg.Git.InitBare(context.Background(), bare); err != nil {
			t.Fatalf("init bare: %v", err)
		}
		saveSendableBinding(t, env, "api", &store.ServeFacts{RepoID: repoID, BareRepo: bare}, since)
		send(t, env)
	})

	t.Run("tags unreadable", func(t *testing.T) {
		env, _ := setup(t)
		saveSendableBinding(t, env, "api", &store.ServeFacts{RepoID: repoID, BareRepo: filepath.Join(t.TempDir(), "missing.git")}, since)
		writeHeadPlan(t, env, "api", 1)
		send(t, env)
	})

	t.Run("out ref missing", func(t *testing.T) {
		env, _ := setup(t)
		bare := t.TempDir()
		if err := env.srv.cfg.Git.InitBare(context.Background(), bare); err != nil {
			t.Fatalf("init bare: %v", err)
		}
		saveSendableBinding(t, env, "api", &store.ServeFacts{RepoID: repoID, BareRepo: bare}, since)
		writeHeadPlan(t, env, "api", 1)
		send(t, env)
	})

	t.Run("unknown ship base falls back to the full bundle", func(t *testing.T) {
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		script := &nodeScript{
			getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
			createStatus: http.StatusCreated,
			startStatus:  http.StatusCreated, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
		}
		url, fp := startStubNode(t, script.serve())
		queueForward(t, env, "api", stubRow(t, "b1", url, fp), clock)
		rt := env.runtime(t)
		b := loadBurstBinding(t, rt, "api")
		if b.Burst.Shipped == nil {
			b.Burst.Shipped = map[string]string{}
		}
		b.Burst.Shipped["b1"] = strings.Repeat("b", 40)
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("preset ship base: %v", err)
		}
		if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
			t.Fatalf("confirmed = %d, want 1", got)
		}
		requireConfirmedRunning(t, env)
	})

	t.Run("forward ref owned by another binding stays sending", func(t *testing.T) {
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		queueForward(t, env, "api", stubRow(t, "b1", closedPortURL(t), burstTestFingerprint), clock)
		rt := env.runtime(t)
		b := loadBurstBinding(t, rt, "api")
		// A foreign out ref under the forward's name: the send must not
		// clobber it, and the closed-port row proves nothing dialled. The
		// ref must point at a real object, which git demands of any ref.
		outSHA, ok, rerr := env.srv.cfg.Git.RefSHA(context.Background(), b.Serve.BareRepo, "refs/relevo/api/out")
		if rerr != nil || !ok {
			t.Fatalf("out ref: %v, ok=%v", rerr, ok)
		}
		foreign := strings.TrimSpace(runGit(t, b.Serve.BareRepo, "commit-tree", outSHA+"^{tree}", "-m", "foreign"))
		if err := env.srv.cfg.Git.UpdateRef(context.Background(), b.Serve.BareRepo, "refs/relevo/nb-api/out", foreign, ""); err != nil {
			t.Fatalf("stage foreign ref: %v", err)
		}
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
		if got := env.srv.copyBurstTable().Nodes[0].Problem; got != "" {
			t.Errorf("Problem = %q, want no mark: nothing may have dialled", got)
		}
		cur, ok, err := env.srv.cfg.Git.RefSHA(context.Background(), b.Serve.BareRepo, "refs/relevo/nb-api/out")
		if err != nil || !ok || cur != foreign {
			t.Errorf("forward ref = %q (ok=%v err=%v), want the foreign ref untouched", cur, ok, err)
		}
	})

	t.Run("unstageable forward ref stays sending", func(t *testing.T) {
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		queueForward(t, env, "api", stubRow(t, "b1", closedPortURL(t), burstTestFingerprint), clock)
		rt := env.runtime(t)
		b := loadBurstBinding(t, rt, "api")
		// A bare repo git can read but not write: the stage fails while
		// every read the ship needs still works. Chmod walks the tree
		// because git writes under refs/, not the bare root.
		freezeRepo(t, b.Serve.BareRepo)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Fatalf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
		if got := env.srv.copyBurstTable().Nodes[0].Problem; got != "" {
			t.Errorf("Problem = %q, want no mark: nothing may have dialled", got)
		}
	})

	t.Run("malformed ship base falls back to the full bundle", func(t *testing.T) {
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		script := &nodeScript{
			getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
			createStatus: http.StatusCreated,
			startStatus:  http.StatusCreated, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
		}
		url, fp := startStubNode(t, script.serve())
		queueForward(t, env, "api", stubRow(t, "b1", url, fp), clock)
		rt := env.runtime(t)
		b := loadBurstBinding(t, rt, "api")
		if b.Burst.Shipped == nil {
			b.Burst.Shipped = map[string]string{}
		}
		// Not even a sha: the merge-base check fails, which reads as an
		// unknown base and ships everything.
		b.Burst.Shipped["b1"] = "zzz-not-a-sha"
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("preset ship base: %v", err)
		}
		if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
			t.Fatalf("confirmed = %d, want 1", got)
		}
		requireConfirmedRunning(t, env)
	})
}

// TestBurstSendClassify pins the pure decision the send feeds: which answers
// refuse the round back to the queue and which failures keep the claim.
func TestBurstSendClassify(t *testing.T) {
	httpErr := func(status int, code remote.Code) error {
		return &client.HTTPError{Status: status, Body: remote.ErrorBody{Code: code}}
	}
	wrapUnreachable := func() error {
		return &urlError{msg: "dial refused", wrapped: client.ErrUnreachable}
	}
	refused := []struct {
		name string
		err  error
		want bool
	}{
		{"400 refuses", httpErr(400, remote.CodeInvalid), true},
		{"404 refuses", httpErr(404, remote.CodeNotFound), true},
		{"422 refuses", httpErr(422, remote.CodeInvalid), true},
		{"409 confirms, not refuses", httpErr(409, remote.CodeRoundStarted), false},
		{"500 stays", httpErr(500, remote.Code("internal")), false},
		{"unreachable stays", client.ErrUnreachable, false},
		{"wrapped unreachable stays", wrapUnreachable(), false},
		{"unknown failure stays", errors.New("boom"), false},
		{"nil is no refusal", nil, false},
	}
	for _, c := range refused {
		t.Run("refused/"+c.name, func(t *testing.T) {
			if got := burstForwardRefused(c.err); got != c.want {
				t.Errorf("burstForwardRefused(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
	unreachable := []struct {
		name string
		err  error
		want bool
	}{
		{"plain unreachable", client.ErrUnreachable, true},
		{"wrapped unreachable", wrapUnreachable(), true},
		{"500 is a server failure", httpErr(500, remote.Code("internal")), true},
		{"503 is a server failure", httpErr(503, remote.Code("busy")), true},
		{"409 is an answer", httpErr(409, remote.CodeRoundStarted), false},
		{"404 is an answer", httpErr(404, remote.CodeNotFound), false},
		{"redirect is an answer", client.ErrRedirect, false},
		{"unknown failure is not", errors.New("boom"), false},
		{"nil is not", nil, false},
	}
	for _, c := range unreachable {
		t.Run("unreachable/"+c.name, func(t *testing.T) {
			if got := burstForwardUnreachable(c.err); got != c.want {
				t.Errorf("burstForwardUnreachable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// urlError is an error wrapping another, the shape a transport failure takes
// under the client's own wrapping.
type urlError struct {
	msg     string
	wrapped error
}

func (e *urlError) Error() string { return e.msg + ": " + e.wrapped.Error() }
func (e *urlError) Unwrap() error { return e.wrapped }

// TestBurstForwardRowMarks pins the table marks without the network: the
// unreachable mark stamps its start once, a success clears it, the forwarded
// mark lands, and a row that moved under the send is left alone.
func TestBurstForwardRowMarks(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	row := stubRow(t, "b1", "https://b1.example", burstTestFingerprint)
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{row}}

	env.srv.noteNodeUnreachable(now, row)
	got := env.srv.copyBurstTable().Nodes[0]
	if got.Problem != burst.ReportProblemUnreachable || !got.ProblemSince.Equal(now) {
		t.Fatalf("Problem = %q since %v, want unreachable stamped at %v", got.Problem, got.ProblemSince, now)
	}
	env.srv.noteNodeUnreachable(now.Add(time.Minute), row)
	if got := env.srv.copyBurstTable().Nodes[0]; !got.ProblemSince.Equal(now) {
		t.Errorf("ProblemSince = %v, want the first failure at %v kept", got.ProblemSince, now)
	}

	moved := row
	moved.URL = "https://b1-moved.example"
	env.srv.noteNodeUnreachable(now.Add(2*time.Minute), moved)
	env.srv.clearNodeProblem(moved)
	env.srv.noteForwardSent(moved, now.Add(2*time.Minute))
	if got := env.srv.copyBurstTable().Nodes[0]; got.Problem != burst.ReportProblemUnreachable || got.ForwardedAt != nil {
		t.Errorf("row after moved marks = %+v, want the checked row left alone", got)
	}

	env.srv.clearNodeProblem(row)
	if got := env.srv.copyBurstTable().Nodes[0]; got.Problem != "" || !got.ProblemSince.IsZero() {
		t.Errorf("Problem = %q since %v, want both cleared", got.Problem, got.ProblemSince)
	}
	env.srv.noteForwardSent(row, now)
	if got := env.srv.copyBurstTable().Nodes[0]; got.ForwardedAt == nil || !got.ForwardedAt.Equal(now) {
		t.Errorf("ForwardedAt = %v, want %v", got.ForwardedAt, now)
	}

	env.srv.burstTable = nil
	env.srv.noteNodeUnreachable(now, row)
	env.srv.clearNodeProblem(row)
	env.srv.noteForwardSent(row, now)
}

// TestBurstSendOffAndKey pins the send gates: with bursting off, with no
// table, or with an unusable client key the send confirms nothing and the
// claim stays.
func TestBurstSendOffAndKey(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	since := clock.Add(-time.Minute)

	t.Run("bursting off sends nothing", func(t *testing.T) {
		env := setupTestEnv(t, func(cfg *Config) { cfg.Burst = burstTestPolicy() })
		saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Errorf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
	})

	t.Run("nil table sends nothing", func(t *testing.T) {
		headKP, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		env := forwardEnv(t, headKP)
		saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
		env.srv.burstTable = nil
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Errorf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
	})

	t.Run("unparsable key sends nothing", func(t *testing.T) {
		env := setupTestEnv(t, func(cfg *Config) {
			cfg.Burst = burstTestPolicy()
			cfg.ClientKey = []byte("not a key")
			cfg.Provider = &recordingBurstProvider{}
		})
		saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
		env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{
			stubRow(t, "b1", closedPortURL(t), burstTestFingerprint),
		}}
		if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
			t.Errorf("confirmed = %d, want 0", got)
		}
		requireStillSending(t, env, "api")
	})
}

// TestBurstTickSendsAndReportsConfirmed pins the tick wiring: a forward the
// node refused with a server failure stays sending, and once the node
// answers the tick verifies the moved row, then the next tick confirms the
// forward and reports forwarded with nothing placed.
func TestBurstTickSendsAndReportsConfirmed(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	// nodeResp keeps the one test node listed the way a provider response
	// would: an empty answer would mark every row gone on merge.
	nodeResp := func(url, fp string) burst.ReportResponse {
		return burst.ReportResponse{
			Version: burst.ReportVersion,
			Nodes: []burst.ReportNodeOut{{
				ID: "b1", Name: "b1 cx43@hel1", URL: url,
				Fingerprint: fp, Cap: 2, State: "ready",
			}},
			ReportEvery: "30s",
		}
	}
	env := forwardEnv(t, headKP)
	script := &nodeScript{
		getStatus: http.StatusOK, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		startStatus: http.StatusInternalServerError, startCode: remote.Code("internal"), startMsg: "boom",
	}
	url, fp := startStubNode(t, script.serve())
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	queueForward(t, env, "api", stubRow(t, "b1", url, fp), clock)
	fake, ok := env.srv.burstProvider.(*recordingBurstProvider)
	if !ok {
		t.Fatalf("provider = %T, want the recording fake", env.srv.burstProvider)
	}
	fake.resp = nodeResp(url, fp)

	env.srv.burstTick(context.Background())
	requireStillSending(t, env, "api")
	if got := fake.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want the first tick to report", got)
	}

	_, nodeTS, nodeFP := startBurstNodeWithRunner(t, &headKP)
	fake.resp = nodeResp(nodeTS.URL, nodeFP)
	env.srv.mu.Lock()
	back := &env.srv.burstTable.Nodes[0]
	back.URL, back.Fingerprint, back.Verified = nodeTS.URL, nodeFP, false
	env.srv.mu.Unlock()
	env.srv.burstTick(context.Background())
	// The verify pass runs after the send: the tick that re-verifies the
	// moved row confirms nothing yet, and nothing is owed a report.
	requireStillSending(t, env, "api")
	if got := fake.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want the verifying tick to owe no report", got)
	}

	env.srv.burstTick(context.Background())

	requireConfirmedRunning(t, env)
	tab := env.srv.copyBurstTable().Nodes[0]
	if !tab.Verified || tab.Problem != "" {
		t.Errorf("row = verified %v problem %q, want verified and clear", tab.Verified, tab.Problem)
	}
	if got := fake.calls(); got != 2 {
		t.Fatalf("provider calls = %d, want the confirm to report out of cycle", got)
	}
	if got := fake.last().Reason; got != burst.ReportReasonForwarded {
		t.Errorf("reason = %q, want forwarded for the confirmed round", got)
	}
}

// TestBurstRefusedRoundHeldFromPlacement pins the refusal hold: a round a
// node refused is not claimed again on the next tick, where the same node
// would refuse it the same way, and is placeable again once the hold ends.
func TestBurstRefusedRoundHeldFromPlacement(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	script := &nodeScript{
		getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
		createStatus: http.StatusUnprocessableEntity, createCode: remote.CodeInvalid, createMsg: "no candidate serves actor",
	}
	env, _, fwd := answerSetup(t, script, clock)
	if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
		t.Fatalf("confirmed = %d, want 0", got)
	}
	requireRequeued(t, env, fwd)

	if got := env.srv.placeForwards(clock.Add(time.Second)); got != 0 {
		t.Fatalf("placed inside the hold = %d, want 0", got)
	}
	if f := burstStoredForward(loadBurstBinding(t, env.runtime(t), "api")); f != nil {
		t.Fatalf("api forward = %+v, want none inside the hold", f)
	}
	if got := env.srv.placeForwards(clock.Add(burstRefusalHold)); got != 1 {
		t.Fatalf("placed after the hold = %d, want 1", got)
	}
}

// TestBurstRefusalHoldKeys pins what the hold names: one round, kept across
// its requeue by the queue stamp, so a later round of the same binding and a
// different binding are both free, and an entry past the hold is forgotten
// when the next refusal lands.
func TestBurstRefusalHoldKeys(t *testing.T) {
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	h := newBurstHealth()
	k := burstRefusalKey("owner/api", at)
	h.noteRefused(k, at)
	if !h.refusalHeld(k, at.Add(burstRefusalHold-time.Second)) {
		t.Error("refused round not held inside the hold")
	}
	if h.refusalHeld(burstRefusalKey("owner/api", at.Add(time.Minute)), at) {
		t.Error("a later round of the same binding is held")
	}
	if h.refusalHeld(burstRefusalKey("owner/web", at), at) {
		t.Error("another binding is held")
	}
	h.noteRefused(burstRefusalKey("owner/web", at), at.Add(burstRefusalHold))
	if _, kept := h.refused[k]; kept {
		t.Error("an entry past the hold was kept")
	}
	var nilHealth *burstHealth
	nilHealth.noteRefused(k, at)
	if nilHealth.refusalHeld(k, at) {
		t.Error("nil health holds a round")
	}
}
