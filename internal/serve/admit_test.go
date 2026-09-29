package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// requireRoundState fails unless name's stored round state is want.
func requireRoundState(t *testing.T, rt relevo.Runtime, name string, want remote.RoundState) {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	if got := relevo.RoundStateOf(b, entries); got != want {
		t.Errorf("%s round_state = %v, want %v", name, got, want)
	}
}

// TestRoundSpawnCarriesAuthorEnv: the round a binding's stored author starts runs
// the builder with that identity in its environment, so every commit it makes is
// the client's. A binding with no author gets no GIT_* additions at all.
func TestRoundSpawnCarriesAuthorEnv(t *testing.T) {
	authorEnv := []string{
		"GIT_AUTHOR_NAME=Ada Lovelace",
		"GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada Lovelace",
		"GIT_COMMITTER_EMAIL=ada@example.com",
	}

	t.Run("author set", func(t *testing.T) {
		env := setupTestEnv(t)
		author := &remote.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
		resp, body := sendRoundAs(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan", author)
		requireCreated(t, resp, body, "api")

		specs := startedSpecs(env)
		if len(specs) == 0 {
			t.Fatal("round started no process")
		}
		for _, want := range authorEnv {
			found := false
			for _, got := range specs[0].Env {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("spec.Env = %v, want it to contain %q", specs[0].Env, want)
			}
		}
		want := append(append([]string{}, authorEnv...), "RELEVO_RUNNER=api")
		if !reflect.DeepEqual(specs[0].Env, want) {
			t.Errorf("spec.Env = %v, want exactly %v", specs[0].Env, want)
		}
	})

	t.Run("no author", func(t *testing.T) {
		env := setupTestEnv(t)
		resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan")
		requireCreated(t, resp, body, "api")

		specs := startedSpecs(env)
		if len(specs) == 0 {
			t.Fatal("round started no process")
		}
		if want := []string{"RELEVO_RUNNER=api"}; !reflect.DeepEqual(specs[0].Env, want) {
			t.Errorf("spec.Env = %v, want exactly %v for a binding with no author", specs[0].Env, want)
		}
	})
}

// TestCreateBindingStoresAuthor: the git identity a client sends on the create
// request lands on the owner's ServeFacts, which the builder environment reads.
func TestCreateBindingStoresAuthor(t *testing.T) {
	env := setupTestEnv(t)

	author := &remote.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
	resp, body := sendRoundAs(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan", author)
	requireCreated(t, resp, body, "api")

	b, err := env.runtime(t).Store.Load("api")
	if err != nil {
		t.Fatalf("Load api: %v", err)
	}
	if b.Serve == nil {
		t.Fatal("binding has no ServeFacts")
	}
	if b.Serve.AuthorName != "Ada Lovelace" || b.Serve.AuthorEmail != "ada@example.com" {
		t.Errorf("ServeFacts author = %q <%q>, want Ada Lovelace <ada@example.com>",
			b.Serve.AuthorName, b.Serve.AuthorEmail)
	}
}

// TestCreateBindingRejectsBadAuthor: a malformed author is a 400 invalid and
// stores nothing, so a value that could forge a line in the builder's
// environment never reaches the store.
func TestCreateBindingRejectsBadAuthor(t *testing.T) {
	env := setupTestEnv(t)

	cases := []struct {
		name  string
		email string
	}{
		{"", "a@b"},
		{"Ada", ""},
		{"Ada\nX", "a@b"},
		{"Ada", "<a@b>"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q/%q", tc.name, tc.email), func(t *testing.T) {
			createBody, err := json.Marshal(remote.CreateBindingRequest{
				Name:       "api",
				RepoID:     env.repoID,
				BaseCommit: env.headSHA,
				Author:     &remote.GitIdentity{Name: tc.name, Email: tc.email},
			})
			if err != nil {
				t.Fatal(err)
			}
			resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(body))
			}
			var werr remote.ErrorBody
			if err := json.Unmarshal(body, &werr); err != nil {
				t.Fatalf("unmarshal error body: %v; body: %s", err, string(body))
			}
			if werr.Code != remote.CodeInvalid {
				t.Errorf("error code = %q, want %q", werr.Code, remote.CodeInvalid)
			}
			if _, lerr := env.runtime(t).Store.Load("api"); !errors.Is(lerr, store.ErrNotFound) {
				t.Errorf("Load api err = %v, want store.ErrNotFound", lerr)
			}
		})
	}
}

// TestAdmitCapQueuesSecondOwner: with MaxBuilders 1, a second owner's round
// accepts (201) but is queued, not started, and the wire carries its position.
func TestAdmitCapQueuesSecondOwner(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	if viewA := decodeView(t, bodyA); viewA.RoundState != remote.RoundRunning {
		t.Fatalf("A round_state = %q, want running", viewA.RoundState)
	}

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	viewB := decodeView(t, bodyB)
	if viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}
	if viewB.Queue == nil {
		t.Fatal("B Queue = nil, want a QueueView")
	}
	if viewB.Queue.Position != 1 || viewB.Queue.Ahead != 0 || viewB.Queue.Running != 1 || viewB.Queue.Cap != 1 {
		t.Errorf("B Queue = %+v, want {Position:1 Ahead:0 Running:1 Cap:1 ...}", viewB.Queue)
	}
	if viewB.Queue.Since.IsZero() {
		t.Error("B Queue.Since is zero, want the accept time")
	}

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/whoami", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	var who remote.WhoAmI
	if err := json.Unmarshal(body, &who); err != nil {
		t.Fatalf("unmarshal whoami: %v", err)
	}
	if who.Builders == nil || who.Builders.Running != 1 || who.Builders.Queued != 1 || who.Builders.Cap != 1 {
		t.Errorf("whoami.Builders = %+v, want {Running:1 Queued:1 Cap:1 ...}", who.Builders)
	}

	rtB := testRuntime(t, env.srv, ownerB.id)
	bB, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if bB.QueuedAt.IsZero() {
		t.Error("B QueuedAt is zero, want set")
	}
	if bB.Builder.PID != 0 {
		t.Errorf("B PID = %d, want 0", bB.Builder.PID)
	}
	if len(env.runner.specs) != 1 {
		t.Errorf("runner specs = %d, want 1 (only A started)", len(env.runner.specs))
	}
}

// TestAdmitAfterSlotFrees: once A's round closes, the freed slot admits B on the
// next Tick, and the log carries both halves of the wait as separate entries.
func TestAdmitAfterSlotFrees(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	closeRound(t, rtA, "api", bA.Builder.PID, env.runner)

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	requireRoundState(t, testRuntime(t, env.srv, env.id), "api", remote.RoundClosed)
	rtB := testRuntime(t, env.srv, ownerB.id)
	requireRoundState(t, rtB, "api", remote.RoundRunning)

	if len(env.runner.specs) != 2 {
		t.Errorf("runner specs = %d, want 2 (A then B)", len(env.runner.specs))
	}

	var queueNotes []string
	entriesB, err := rtB.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog B: %v", err)
	}
	for _, e := range entriesB {
		if e.Kind == store.KindQueue {
			queueNotes = append(queueNotes, e.Note)
		}
	}
	if len(queueNotes) != 2 {
		t.Fatalf("B queue notes = %v, want 2 entries", queueNotes)
	}
	if !strings.HasPrefix(queueNotes[0], "queued (1/1 builders busy)") {
		t.Errorf("B first queue note = %q, want it to start with %q", queueNotes[0], "queued (1/1 builders busy)")
	}
	if !strings.HasPrefix(queueNotes[1], "started after ") || !strings.HasSuffix(queueNotes[1], "queued") {
		t.Errorf("B second queue note = %q, want %q...%q", queueNotes[1], "started after ", "queued")
	}
}

// TestAdmitStrictFIFO: with MaxBuilders 1 and two queued rounds, the older one
// is admitted first every time a slot frees, and a round still queued reports
// its correct (shrinking) position.
func TestAdmitStrictFIFO(t *testing.T) {
	clock := time.Now()
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return clock }
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	if viewA := decodeView(t, bodyA); viewA.RoundState != remote.RoundRunning {
		t.Fatalf("A round_state = %q, want running", viewA.RoundState)
	}

	clock = clock.Add(2 * time.Second)
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	clock = clock.Add(2 * time.Second)
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	viewC := decodeView(t, bodyC)
	if viewC.RoundState != remote.RoundQueued || viewC.Queue == nil || viewC.Queue.Position != 2 {
		t.Fatalf("C view = %+v, want queued at position 2", viewC)
	}

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	closeRound(t, rtA, "api", bA.Builder.PID, env.runner)

	clock = clock.Add(2 * time.Second)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	rtB := testRuntime(t, env.srv, ownerB.id)
	requireRoundState(t, rtB, "api", remote.RoundRunning)
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundQueued)
	if pos := queuePosition(t, env, ownerC.id); pos != 1 {
		t.Errorf("C position after tick 1 = %d, want 1", pos)
	}

	bB, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	closeRound(t, rtB, "api", bB.Builder.PID, env.runner)
	clock = clock.Add(time.Minute)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundRunning)
}

// TestAdmitFreeSlotButQueueNonEmpty: A's process is dead but no Tick has observed
// it yet, so census still counts A as running and a brand new send must never
// jump the existing queue.
func TestAdmitFreeSlotButQueueNonEmpty(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	env.runner.finish(bA.Builder.PID) // dead, but no Tick has reaped it yet

	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	viewC := decodeView(t, bodyC)
	if viewC.RoundState != remote.RoundQueued || viewC.Queue == nil || viewC.Queue.Position != 2 {
		t.Fatalf("C view = %+v, want queued at position 2 (must not jump B)", viewC)
	}

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundRunning)
	if pos := queuePosition(t, env, ownerC.id); pos != 1 {
		t.Errorf("C position after tick = %d, want 1", pos)
	}
}

// TestRestartRequeuesDeadBuilder: a server standing in for a daemon restart,
// with a runner that never started A's pid, re-queues A instead of relaunching
// it, then admits it again within the same Tick because the cap allows it.
func TestRestartRequeuesDeadBuilder(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	oldStartedAt := bA.Builder.StartedAt

	newRunner := newScriptRunner()
	restarted, err := New(Config{
		DB:          env.srv.cfg.DB,
		Root:        env.srv.cfg.Root,
		Candidates:  env.srv.cfg.Candidates,
		Runner:      newRunner,
		Git:         env.gitClient,
		Now:         time.Now,
		MaxBuilders: 1,
		StartedAt:   time.Unix(oldStartedAt+60, 0), // after A's own builder start
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	entries, err := rtA.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var queueNotes []string
	for _, e := range entries {
		if e.Kind == store.KindQueue {
			queueNotes = append(queueNotes, e.Note)
		}
	}
	// The original send/admit already logged its own pair; the restart adds a
	// second pair on top, so only the last two entries are this test's concern.
	if len(queueNotes) != 4 {
		t.Fatalf("queue notes = %v, want 4 (send+admit, then re-queue+re-admit)", queueNotes)
	}
	if queueNotes[2] != "re-queued (builder lost to a restart)" {
		t.Errorf("third queue note = %q, want %q", queueNotes[2], "re-queued (builder lost to a restart)")
	}
	if !strings.HasPrefix(queueNotes[3], "started after ") {
		t.Errorf("fourth queue note = %q, want it to start with %q", queueNotes[3], "started after ")
	}

	got, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.PID == 0 {
		t.Error("PID = 0, want a fresh pid from the restarted server's runner")
	}
	if got.RoundStartedAt.IsZero() {
		t.Error("RoundStartedAt is zero, want stamped by the re-admit")
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero (re-admitted within the same tick)", got.QueuedAt)
	}
	if len(newRunner.specs) != 1 {
		t.Errorf("newRunner specs = %d, want 1 (the re-admit spawn)", len(newRunner.specs))
	}
}

// TestUnbindDropsQueued: unbinding a queued binding removes it from the queue
// outright, and a Tick afterwards has nothing new to admit.
func TestUnbindDropsQueued(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	resp, body := doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings/api/unbind", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unbind status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(c.Queued) != 0 {
		t.Errorf("census.Queued = %+v, want empty", c.Queued)
	}

	specsBefore := len(env.runner.specs)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(env.runner.specs) != specsBefore {
		t.Errorf("specs after tick = %d, want unchanged at %d (nothing left to admit)", len(env.runner.specs), specsBefore)
	}
}

// TestWireUnbindStopsRunningRound: POST /unbind has no running-round guard and
// no --force, yet it does stop a running remote round -- the builder process is
// killed and the binding is archived out of the owner's live store.
func TestWireUnbindStopsRunningRound(t *testing.T) {
	env := setupTestEnv(t)

	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, resp, body, "A")
	if view := decodeView(t, body); view.RoundState != remote.RoundRunning {
		t.Fatalf("round_state = %q, want running", view.RoundState)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	pid := b.Builder.PID
	if pid == 0 {
		t.Fatal("builder PID = 0, want a running process")
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/unbind", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unbind status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	env.runner.mu.Lock()
	alive := env.runner.alive[pid]
	env.runner.mu.Unlock()
	if alive {
		t.Errorf("builder pid %d still alive after unbind, want killed", pid)
	}
	if _, err := rt.Store.Load("api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Load after unbind: err = %v, want ErrNotFound", err)
	}
}

// TestDoneRefusesQueuedRound: a queued round has no process to stop and nothing
// to hand back, so done is refused exactly like an open round.
func TestDoneRefusesQueuedRound(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	resp, body := doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings/api/done", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("done status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v; body: %s", err, string(body))
	}
	wantMsg := fmt.Sprintf("round %d is queued; relevo stop to drop it from the queue, or unbind", 1)
	if errBody.Message != wantMsg {
		t.Errorf("error message = %q, want %q", errBody.Message, wantMsg)
	}
}

// TestStopDropsQueued: with the cap taken, a second owner's queued round stops
// with 200 as "dequeued", and the queue census no longer lists it.
func TestStopDropsQueued(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	resp, body := doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings/api/stop", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	view := decodeView(t, body)
	if view.Stopped != "dequeued" {
		t.Errorf("stopped = %q, want dequeued", view.Stopped)
	}
	if view.RoundState != remote.RoundClosed {
		t.Errorf("round_state = %q, want closed", view.RoundState)
	}

	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(c.Queued) != 0 {
		t.Errorf("census.Queued = %+v, want empty", c.Queued)
	}
}

// TestGetBindingQueuePosition: three queued rounds report positions 1, 2 and 3
// in FIFO (QueuedAt) order.
func TestGetBindingQueuePosition(t *testing.T) {
	clock := time.Now()
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return clock }
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")
	ownerD := addOwner(t, env, "dave")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	for _, o := range []ownerEnv{ownerB, ownerC, ownerD} {
		clock = clock.Add(2 * time.Second)
		resp, body := sendRound(t, env, o.kp, o.clientDir, o.repoID, o.headSHA, "api", "# Plan")
		requireCreated(t, resp, body, string(o.id))
	}

	for i, o := range []ownerEnv{ownerB, ownerC, ownerD} {
		resp, body := doSigned(t, env.ts, o.kp, "GET", "/v1/bindings/api", nil, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get binding status = %d, want 200; body: %s", resp.StatusCode, string(body))
		}
		if view := decodeView(t, body); view.Queue == nil || view.Queue.Position != i+1 {
			t.Errorf("owner %d Queue = %+v, want Position %d", i, view.Queue, i+1)
		}
	}
}

// TestHeldCPUsCrossOwnerCensus: owner A's live round pins core 0 in its own
// store, and owner B's round -- started through HeldCPUs -- takes core 1.
func TestHeldCPUsCrossOwnerCensus(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Scope = &spawn.ScopeSpec{CPUWeight: 100, AllowedCPUs: "0-1"}
	})
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	if bA.RoundCPU == nil {
		t.Fatal("A RoundCPU = nil, want 0")
	}
	if *bA.RoundCPU != 0 {
		t.Errorf("A RoundCPU = %d, want 0", *bA.RoundCPU)
	}

	rtB := testRuntime(t, env.srv, ownerB.id)
	bB, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if bB.RoundCPU == nil {
		t.Fatal("B RoundCPU = nil, want 1: A's core 0 is held across the owner boundary")
	}
	if *bB.RoundCPU != 1 {
		t.Errorf("B RoundCPU = %d, want 1: A's core 0 is held across the owner boundary", *bB.RoundCPU)
	}
}

// TestHeldCPUsSkipsAFailingOwner: an owner whose store cannot be listed is
// skipped and its error returned first, but the cores of the owners that did
// list are still returned.
func TestHeldCPUsSkipsAFailingOwner(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Scope = &spawn.ScopeSpec{CPUWeight: 100, AllowedCPUs: "0-1"}
	})
	ownerB := addOwner(t, env, "bob")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")

	ownerC := addOwner(t, env, "carol")
	cDir, ok := ownerC.id.Dir()
	if !ok {
		t.Fatal("carol's client id has no dir")
	}
	// A record row that cannot be decoded, so carol's List errors where B's
	// does not.
	if err := os.MkdirAll(filepath.Join(env.srv.cfg.Root, "bindings", cDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.cfg.DB.RecordPut(db.Record{
		Owner: string(ownerC.id),
		Name:  "broken",
		Round: 1,
		JSON:  "{",
	}); err != nil {
		t.Fatal(err)
	}

	aDir, ok := env.id.Dir()
	if !ok {
		t.Fatal("alice's client id has no dir")
	}
	rootA := filepath.Join(env.srv.cfg.Root, "bindings", aDir)
	rtA := testRuntime(t, env.srv, env.id)
	err := rtA.Store.WithLock(func(tx *store.Tx) error {
		held, herr := env.srv.heldCPUs(rootA, tx, "nobody")
		if herr == nil {
			t.Error("heldCPUs: want the unreadable owner's error, got nil")
		}
		for _, c := range held {
			if c == 0 {
				return nil
			}
		}
		t.Errorf("held = %v, want B's core 0 despite the failing owner", held)
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}
