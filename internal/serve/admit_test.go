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

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
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
	if got := remote.RoundStateOf(b, entries); got != want {
		t.Errorf("%s round_state = %v, want %v", name, got, want)
	}
}

// withoutShimPATH checks that a served writer round's env ends with a PATH
// entry led by the owner's git shim directory, and returns the env without it.
func withoutShimPATH(t *testing.T, env []string) []string {
	t.Helper()
	if len(env) == 0 {
		t.Fatal("empty spec.Env")
	}
	last := env[len(env)-1]
	path, ok := strings.CutPrefix(last, "PATH=")
	first, _, _ := strings.Cut(path, string(os.PathListSeparator))
	if !ok || filepath.Base(first) != ".shims" {
		t.Fatalf("last env entry %q, want PATH led by the git shim dir", last)
	}
	return env[:len(env)-1]
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
		specEnv := withoutShimPATH(t, specs[0].Env)
		for _, want := range authorEnv {
			found := false
			for _, got := range specEnv {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("spec.Env = %v, want it to contain %q", specs[0].Env, want)
			}
		}
		want := append(append([]string{}, authorEnv...), "RELEVO_RUNNER=api", "RELEVO_BASE="+env.headSHA)
		if !reflect.DeepEqual(specEnv, want) {
			t.Errorf("spec.Env = %v, want exactly %v", specEnv, want)
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
		if want := []string{"RELEVO_RUNNER=api", "RELEVO_BASE=" + env.headSHA}; !reflect.DeepEqual(withoutShimPATH(t, specs[0].Env), want) {
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

// TestAdmitDoesNotCountARoundThatNeverStarted: a queued round whose gated
// candidate's replacement refuses the round's tier is admitted with no process
// and no error, so that start must not advance the pass's running count -- the
// round queued behind it takes the slot the failed switch left free.
func TestAdmitDoesNotCountARoundThatNeverStarted(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Candidates = switchCandidateSet(t)
		cfg.Policy = policy.Policy{Order: map[string][]string{
			"builder": {testBuilderToken, "opencode/other/m"},
		}}
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	// B queues at tier read, which the opencode replacement refuses; C queues
	// at the default tier, which the same replacement takes.
	sendRoundAtTier(t, env, ownerB, "# Plan B", "read")
	sendRoundAtTier(t, env, ownerC, "# Plan C", "")

	// Gate the one candidate both queued rounds carry, so each admit has to
	// switch rather than start in place.
	if _, err := availability.Unavailable(relevo.AvailabilityDeps(env.runtime(t)), testBuilderToken, time.Now().Add(time.Hour), "quota"); err != nil {
		t.Fatalf("record gate: %v", err)
	}

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	closeRound(t, rtA, "api", bA.Builder.PID, env.runner)

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	bB, err := testRuntime(t, env.srv, ownerB.id).Store.Load("api")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if bB.Builder.PID != 0 {
		t.Errorf("B PID = %d, want 0: the switch had no replacement opencode would take", bB.Builder.PID)
	}
	if bB.State != store.StateBroken {
		t.Errorf("B state = %q, want %q", bB.State, store.StateBroken)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundRunning)
}

// switchCandidateSet is the default candidate plus one opencode candidate, so a
// gate on the first leaves a replacement for the policy order to pick.
func switchCandidateSet(t *testing.T) *candidate.Set {
	t.Helper()
	body := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]},
	           {"harness":"opencode","provider":"other","model":"m","roles":["builder"]}]`
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// sendRoundAtTier is sendRound for owner's own repo with an explicit round
// tier, which is what decides whether the replacement a switch picks can be
// launched at all.
func sendRoundAtTier(t *testing.T, env *testEnv, o ownerEnv, plan, tier string) {
	t.Helper()
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     o.repoID,
		BaseCommit: o.headSHA,
		Role:       "builder",
	})
	resp, body := doSigned(t, env.ts, o.kp, "POST", "/v1/bindings", createBody, "application/json")
	requireCreated(t, resp, body, string(o.id))

	outRef := "refs/relevo/api/out"
	if err := env.gitClient.UpdateRef(ctx, o.clientDir, outRef, o.headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	bundleBytes := snapshotRef(t, env, o.clientDir, outRef)

	formBytes, ct := makeRoundFormWithTier(t, 1, plan, tier, bundleBytes)
	resp, body = doSigned(t, env.ts, o.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("round start status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}
	if view := decodeView(t, body); view.RoundState != remote.RoundQueued {
		t.Fatalf("round_state = %q, want queued", view.RoundState)
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
		DiskFree:    plentyDisk,
		DB:          env.srv.cfg.DB,
		Root:        env.srv.cfg.Root,
		Candidates:  env.srv.cfg.Candidates,
		Runner:      newRunner,
		Git:         env.gitClient,
		Now:         time.Now,
		MaxBuilders: 1,
		StartedAt:   time.Unix(oldStartedAt+60, 0), // after A's own builder start
		Audiences:   []string{testAudience},
		// A budget no round can fill: the re-admit is not gated on the
		// host's live memory.
		Policy: policy.Policy{Serve: &policy.ServePolicy{MemoryBudget: "1T"}},
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

func TestServerCountsInFlightAcrossOwners(t *testing.T) {
	env := setupTestEnv(t)
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")

	total := func(m map[string]int) (n int) {
		for _, v := range m {
			n += v
		}
		return n
	}
	if got := total(env.srv.inFlightFor("")("")); got != 2 {
		t.Errorf("machine-wide in flight = %d, want 2 (one per owner)", got)
	}
	// The same binding name in both owners: excluding it applies to the
	// resolving owner's root only.
	rootB, err := env.srv.ownerRoot(ownerB.id)
	if err != nil {
		t.Fatal(err)
	}
	if got := total(env.srv.inFlightFor(rootB)("api")); got != 1 {
		t.Errorf("in flight excluding bob's api = %d, want 1", got)
	}
}

// plannerAdmitRegistry builds the file-mode registry the planner admit tests
// read: builder and a findings-only reviewer beside lite-planner over the
// architect agent, whose plan output the shipped default supplies, so the
// registry classifies it as a planner with no declared outputs.
func plannerAdmitRegistry(t *testing.T, set *candidate.Set) *roles.Registry {
	t.Helper()
	entry := []roles.Entry{{Candidate: testBuilderToken}}
	actors := map[string]roles.Actor{
		"builder":      {Agent: "plan-executor", Candidates: entry},
		"lite-planner": {Agent: "architect", Candidates: entry},
		"reviewer":     {Agent: "reviewer", Candidates: entry},
	}
	f, _, err := roles.FromActors(nil, actors)
	if err != nil {
		t.Fatalf("FromActors: %v", err)
	}
	reg, err := roles.Build(f, set, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return reg
}

// stampRole restamps name's stored Role, so a round sendRound created as a
// builder is admitted as the named actor.
func stampRole(t *testing.T, rt relevo.Runtime, name, role string) {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	b.Role = role
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// TestAdmitPlannerRunsWhileBuildersAreFull: with MaxBuilders 1 and a builder
// holding the only seat, a queued round restamped as a planner is running
// after the tick: planners admit against their own cap, not the builders'.
func TestAdmitPlannerRunsWhileBuildersAreFull(t *testing.T) {
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
	})
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	stampRole(t, testRuntime(t, env.srv, ownerB.id), "api", "lite-planner")

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	rtB := testRuntime(t, env.srv, ownerB.id)
	requireRoundState(t, rtB, "api", remote.RoundRunning)
	if b, err := rtB.Store.Load("api"); err != nil {
		t.Fatal(err)
	} else if b.Builder.PID == 0 {
		t.Error("B PID = 0, want a started planner round")
	}
}

// TestAdmitPlannerPassesQueuedBuilders: the planner and a builder queue
// behind a running builder; the tick starts the planner and leaves the
// builder queued with no process, so a planner behind queued builders is
// still admitted while builders wait on their cap.
func TestAdmitPlannerPassesQueuedBuilders(t *testing.T) {
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")

	stampRole(t, testRuntime(t, env.srv, ownerB.id), "api", "lite-planner")

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundRunning)
	rtC := testRuntime(t, env.srv, ownerC.id)
	requireRoundState(t, rtC, "api", remote.RoundQueued)
	if b, err := rtC.Store.Load("api"); err != nil {
		t.Fatal(err)
	} else if b.Builder.PID != 0 {
		t.Errorf("C PID = %d, want 0: the builder stays queued while planners run", b.Builder.PID)
	}
}

// TestAdmitPlannerPassesQueuedBuilderFirst: the builder queues ahead of the
// planner behind a running builder; the tick starts the planner and leaves
// the earlier builder queued with no process, so a capped class only skips
// its own rounds instead of stopping the pass.
func TestAdmitPlannerPassesQueuedBuilderFirst(t *testing.T) {
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")

	stampRole(t, testRuntime(t, env.srv, ownerC.id), "api", "lite-planner")

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundRunning)
	rtB := testRuntime(t, env.srv, ownerB.id)
	requireRoundState(t, rtB, "api", remote.RoundQueued)
	if b, err := rtB.Store.Load("api"); err != nil {
		t.Fatal(err)
	} else if b.Builder.PID != 0 {
		t.Errorf("B PID = %d, want 0: the builder stays queued while its cap is full", b.Builder.PID)
	}
}

// TestAdmitPlannerCapQueuesTheSecondPlanner: with serve.max_planners 1, the
// first planner runs while the second queues, and whoami shows both classes:
// builders 1/1 with nothing queued, planners 1/1 with one queued.
func TestAdmitPlannerCapQueuesTheSecondPlanner(t *testing.T) {
	one := 1
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MaxPlanners: &one}}
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	stampRole(t, testRuntime(t, env.srv, ownerB.id), "api", "lite-planner")
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundRunning)

	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	stampRole(t, testRuntime(t, env.srv, ownerC.id), "api", "lite-planner")
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	rtC := testRuntime(t, env.srv, ownerC.id)
	requireRoundState(t, rtC, "api", remote.RoundQueued)
	if b, err := rtC.Store.Load("api"); err != nil {
		t.Fatal(err)
	} else if b.Builder.PID != 0 {
		t.Errorf("C PID = %d, want 0: the planner cap holds at one", b.Builder.PID)
	}

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/whoami", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	var who remote.WhoAmI
	if err := json.Unmarshal(body, &who); err != nil {
		t.Fatalf("unmarshal whoami: %v", err)
	}
	if who.Builders == nil {
		t.Fatal("whoami.Builders = nil, want both classes")
	}
	got := *who.Builders
	if got.Running != 1 || got.Queued != 0 || got.Cap != 1 {
		t.Errorf("whoami builders = %+v, want {Running:1 Queued:0 Cap:1 ...}", got)
	}
	if got.PlannersRunning != 1 || got.PlannersQueued != 1 || got.PlannersCap != 1 {
		t.Errorf("whoami planners = %+v, want {PlannersRunning:1 PlannersQueued:1 PlannersCap:1}", got)
	}
}

// TestAdmitPlannerCapHoldsInOnePass: two planners queued together under
// serve.max_planners 1 start one at a time. The census counts only what was
// running when the pass began, so the pass must count what it admits itself.
func TestAdmitPlannerCapHoldsInOnePass(t *testing.T) {
	one := 1
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MaxPlanners: &one}}
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	stampRole(t, testRuntime(t, env.srv, ownerB.id), "api", "lite-planner")
	stampRole(t, testRuntime(t, env.srv, ownerC.id), "api", "lite-planner")

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundRunning)
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundQueued)
}

// TestAdmitPlannerLeavesTheBuilderSeatInOnePass: a planner admitted first does
// not use up the seat a queued builder behind it needs, or the builder would
// wait a tick with a seat free.
func TestAdmitPlannerLeavesTheBuilderSeatInOnePass(t *testing.T) {
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	stampRole(t, testRuntime(t, env.srv, ownerB.id), "api", "lite-planner")

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	closeRound(t, rtA, "api", bA.Builder.PID, env.runner)

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundRunning)
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundRunning)
}

// TestAdmitPlannerQueueViewReadsPlannerClass: with serve.max_planners 1 and
// two builder seats, one builder and one planner running and a second planner
// queued, the queued planner's row reads the planner class on both surfaces
// that paint a queue position: the AdminStatus row and the caller's own
// binding view. The caps differ on purpose (builders 2, planners 1), so a
// row falling back to the builder class reads Cap 2 instead of 1.
func TestAdmitPlannerQueueViewReadsPlannerClass(t *testing.T) {
	one := 1
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 2
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MaxPlanners: &one}}
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")
	ownerD := addOwner(t, env, "dave")

	// Both builder seats fill at send time, so C queues behind them and the
	// tick admits it against the planner cap.
	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	stampRole(t, testRuntime(t, env.srv, ownerC.id), "api", "lite-planner")
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundRunning)

	// Builders and planners are both full now, so D queues and stays queued
	// on the next tick.
	respD, bodyD := sendRound(t, env, ownerD.kp, ownerD.clientDir, ownerD.repoID, ownerD.headSHA, "api", "# Plan D")
	requireCreated(t, respD, bodyD, "D")
	stampRole(t, testRuntime(t, env.srv, ownerD.id), "api", "lite-planner")
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	rtD := testRuntime(t, env.srv, ownerD.id)
	requireRoundState(t, rtD, "api", remote.RoundQueued)

	owners, builders, err := AdminStatus(context.Background(), env.srv)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if builders.Running != 2 || builders.Queued != 0 || builders.Cap != 2 {
		t.Fatalf("builders = %+v, want {Running:2 Queued:0 Cap:2}", builders)
	}
	if builders.PlannersRunning != 1 || builders.PlannersQueued != 1 || builders.PlannersCap != 1 {
		t.Fatalf("builders planners = %+v, want {PlannersRunning:1 PlannersQueued:1 PlannersCap:1}", builders)
	}
	var queued *view.BindingStatus
	for i := range owners {
		if owners[i].Label != "dave" {
			continue
		}
		for j := range owners[i].Report.Bindings {
			if owners[i].Report.Bindings[j].Name == "api" {
				queued = &owners[i].Report.Bindings[j]
			}
		}
	}
	if queued == nil {
		t.Fatal("dave's api row missing from AdminStatus")
	}
	if queued.Queued == nil {
		t.Fatal("dave's queued planner row has no QueueView, want the planner class")
	}
	if queued.Queued.Running != 1 || queued.Queued.Cap != 1 {
		t.Errorf("admin row QueueView = %+v, want {Running:1 Cap:1} from the planner class", queued.Queued)
	}
	if queued.Queued.Position != 1 || queued.Queued.Ahead != 0 {
		t.Errorf("admin row QueueView = %+v, want {Position:1 Ahead:0} keeping the FIFO meaning", queued.Queued)
	}

	resp, body := doSigned(t, env.ts, ownerD.kp, "GET", "/v1/bindings/api", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get binding status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	got := decodeView(t, body)
	if got.Queue == nil {
		t.Fatal("caller's queue view is nil, want the planner class")
	}
	if got.Queue.Running != 1 || got.Queue.Cap != 1 {
		t.Errorf("caller's QueueView = %+v, want {Running:1 Cap:1} from the planner class", got.Queue)
	}
	if got.Queue.Position != 1 || got.Queue.Ahead != 0 {
		t.Errorf("caller's QueueView = %+v, want {Position:1 Ahead:0} keeping the FIFO meaning", got.Queue)
	}
}

// TestAdmitReviewerStillCountsAsBuilder: a queued round restamped as reviewer
// stays queued while builders are full -- reviewers hold builder seats -- and
// runs only once a freed builder slot admits it.
func TestAdmitReviewerStillCountsAsBuilder(t *testing.T) {
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
	})
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	stampRole(t, testRuntime(t, env.srv, ownerB.id), "api", "reviewer")

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	rtB := testRuntime(t, env.srv, ownerB.id)
	requireRoundState(t, rtB, "api", remote.RoundQueued)
	if b, err := rtB.Store.Load("api"); err != nil {
		t.Fatal(err)
	} else if b.Builder.PID != 0 {
		t.Errorf("B PID = %d, want 0: the reviewer holds no planner seat", b.Builder.PID)
	}

	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	closeRound(t, rtA, "api", bA.Builder.PID, env.runner)

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	requireRoundState(t, rtB, "api", remote.RoundRunning)
}

// fakeLiveInputs swaps the budget, round-dir and memory readers for one test,
// restoring them when it ends. The swap is process-wide, but the faking tests
// never run parallel and the parallel tests in this package never touch
// admission, so it cannot race another test's pass.
func fakeLiveInputs(t *testing.T, budget func() (string, error), round func(int) (string, error), total func() (int64, error)) {
	t.Helper()
	oldBudget, oldRound, oldTotal := liveBudgetDir, liveRoundCgroupDir, liveMemTotal
	if budget != nil {
		liveBudgetDir = budget
	}
	if round != nil {
		liveRoundCgroupDir = round
	}
	if total != nil {
		liveMemTotal = total
	}
	t.Cleanup(func() { liveBudgetDir, liveRoundCgroupDir, liveMemTotal = oldBudget, oldRound, oldTotal })
}

// admitZeroBudget restores the zero memory budget setupTestEnv overrides by
// default, so the plumbing tests below read their faked inputs alone.
func admitZeroBudget(t *testing.T, env *testEnv) {
	t.Helper()
	env.srv.mu.Lock()
	defer env.srv.mu.Unlock()
	if env.srv.reloaded.Policy.Serve != nil {
		env.srv.reloaded.Policy.Serve.MemoryBudget = ""
	}
}

// writeCgroupTree builds a fake cgroup dir holding the named files.
func writeCgroupTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// pressureBody renders a memory.pressure file with the given some avg10.
func pressureBody(avg10 string) string {
	return "some avg10=" + avg10 + " avg60=1.00 avg300=0.50 total=12345\n" +
		"full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
}

// seedCostHistory writes n identical samples so the p90 lookup has depth.
func seedCostHistory(t *testing.T, root, repoID, role string, base, burst int64, n int) {
	t.Helper()
	for i := range n {
		if _, err := appendCostSample(root, repoID, role, "seed", i+1, costSample{Base: base, Burst: burst, At: time.Now()}); err != nil {
			t.Fatalf("seed cost sample %d: %v", i+1, err)
		}
	}
}

// backdateQueuedAt moves a binding's queue stamp back, so the pass sees an
// older wait than the send just created.
func backdateQueuedAt(t *testing.T, rt relevo.Runtime, name string, ago time.Duration) {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	b.QueuedAt = time.Now().Add(-ago)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// getBindingView fetches one owner's binding view over the wire.
func getBindingView(t *testing.T, env *testEnv, kp remote.Keypair, name string) remote.BindingView {
	t.Helper()
	resp, body := doSigned(t, env.ts, kp, "GET", "/v1/bindings/"+name, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get binding %s status = %d, want 200; body: %s", name, resp.StatusCode, string(body))
	}
	return decodeView(t, body)
}

// TestCensusCarriesSeatCosts pins the census's cost lookup: a running round
// carries its repo-role history's p90 seat, a queued round without history
// carries the builder defaults, and the running round's pid rides along for
// the budget's non-round reading.
func TestCensusCarriesSeatCosts(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	const gb = int64(1 << 30)
	for i := range 6 {
		s := costSample{Base: int64(i+2) * gb, Burst: int64(i+6) * gb, At: time.Now()}
		if _, err := appendCostSample(env.srv.cfg.Root, env.repoID, "builder", "api", i+1, s); err != nil {
			t.Fatalf("seed cost sample %d: %v", i+1, err)
		}
	}

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")

	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if c.Running != 1 || len(c.RunningRounds) != 1 {
		t.Fatalf("census running = %d rounds %v, want 1 round", c.Running, c.RunningRounds)
	}
	got := c.RunningRounds[0]
	if got.Base != 7*gb || got.Burst != 11*gb {
		t.Errorf("running cost = {%d %d}, want {7GiB 11GiB}", got.Base, got.Burst)
	}
	rtA := testRuntime(t, env.srv, env.id)
	bA, err := rtA.Store.Load("api")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	if bA.Builder.PID == 0 {
		t.Fatal("A PID = 0, want the started round's pid")
	}
	if got.PID != bA.Builder.PID {
		t.Errorf("running PID = %d, want %d", got.PID, bA.Builder.PID)
	}
	if got.Planner {
		t.Error("running Planner = true, want false for a builder round")
	}
	if len(c.Queued) != 1 {
		t.Fatalf("census queued = %d, want 1", len(c.Queued))
	}
	if q := c.Queued[0]; q.Base != gb || q.Burst != 5*gb {
		t.Errorf("queued cost = {%d %d}, want builder defaults {1GiB 5GiB}", q.Base, q.Burst)
	}
}

// TestAdmissionInputs pins the pass inputs with faked IO: the override wins,
// a numeric slice reads minus its anon, an unlimited slice falls back to
// machine memory, only anon counts on either side, the brake trips at 10.0,
// and unreadable or out-of-slice round dirs subtract nothing.
func TestAdmissionInputs(t *testing.T) {
	const gb = int64(1 << 30)
	mem16 := func() (int64, error) { return 16 * gb, nil }

	t.Run("override wins", func(t *testing.T) {
		set, err := builderCandidateSet(t)
		if err != nil {
			t.Fatal(err)
		}
		env := setupTestEnv(t, func(cfg *Config) {
			cfg.Candidates = set
			cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MemoryBudget: "5G"}}
		})
		dir := writeCgroupTree(t, map[string]string{
			"memory.max":      "10737418240\n",
			"memory.stat":     "anon 1073741824\n",
			"memory.pressure": pressureBody("1.0"),
		})
		fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, mem16)
		budget, k, pressureOK := env.srv.admissionInputs(nil)
		if budget != 5*gb {
			t.Errorf("budget = %d, want 5GiB override", budget)
		}
		if k != 2 {
			t.Errorf("k = %d, want 2 with no history", k)
		}
		if !pressureOK {
			t.Error("pressureOK = false, want true at avg10 1.0")
		}
	})

	t.Run("slice minus anon", func(t *testing.T) {
		env := setupTestEnv(t)
		admitZeroBudget(t, env)
		dir := writeCgroupTree(t, map[string]string{
			"memory.max":      "10737418240\n",
			"memory.stat":     "anon 1073741824\n",
			"memory.pressure": pressureBody("1.0"),
		})
		fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, mem16)
		budget, k, pressureOK := env.srv.admissionInputs(nil)
		if budget != 9*gb {
			t.Errorf("budget = %d, want 10GiB slice minus 1GiB anon", budget)
		}
		if k != 2 {
			t.Errorf("k = %d, want 2 with no history", k)
		}
		if !pressureOK {
			t.Error("pressureOK = false, want true at avg10 1.0")
		}
	})

	t.Run("unlimited slice falls back to machine memory", func(t *testing.T) {
		env := setupTestEnv(t)
		admitZeroBudget(t, env)
		dir := writeCgroupTree(t, map[string]string{
			"memory.max":      "max\n",
			"memory.pressure": pressureBody("1.0"),
		})
		fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, mem16)
		budget, _, _ := env.srv.admissionInputs(nil)
		if budget != 14*gb {
			t.Errorf("budget = %d, want 16GiB machine minus 2GiB reserve", budget)
		}
	})

	t.Run("anon ignores page cache", func(t *testing.T) {
		env := setupTestEnv(t)
		admitZeroBudget(t, env)
		// memory.current sits near the slice limit while anon stays small:
		// page cache is reclaimable, so the budget is the slice minus the
		// small anon, not a shrunken one.
		dir := writeCgroupTree(t, map[string]string{
			"memory.max":      "10737418240\n",
			"memory.current":  "10695475200\n",
			"memory.stat":     "anon 1073741824\nfile 9619759104\n",
			"memory.pressure": pressureBody("1.0"),
		})
		fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, mem16)
		budget, _, _ := env.srv.admissionInputs(nil)
		if budget != 9*gb {
			t.Errorf("budget = %d, want 10GiB slice minus 1GiB anon", budget)
		}
	})

	t.Run("pressure brake", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			avg10    string
			pressure bool
			want     bool
		}{
			{name: "9.9 admits", avg10: "9.9", pressure: true, want: true},
			{name: "10.0 holds", avg10: "10.0", pressure: true, want: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env := setupTestEnv(t)
				files := map[string]string{"memory.max": "max\n"}
				if tc.pressure {
					files["memory.pressure"] = pressureBody(tc.avg10)
				}
				dir := writeCgroupTree(t, files)
				fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, mem16)
				_, _, pressureOK := env.srv.admissionInputs(nil)
				if pressureOK != tc.want {
					t.Errorf("pressureOK = %v at avg10 %s, want %v", pressureOK, tc.avg10, tc.want)
				}
			})
		}
		t.Run("missing pressure file admits", func(t *testing.T) {
			env := setupTestEnv(t)
			dir := writeCgroupTree(t, map[string]string{"memory.max": "max\n"})
			fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, mem16)
			_, _, pressureOK := env.srv.admissionInputs(nil)
			if !pressureOK {
				t.Error("pressureOK = false, want true with no pressure file")
			}
		})
	})

	t.Run("round dirs", func(t *testing.T) {
		env := setupTestEnv(t)
		admitZeroBudget(t, env)
		dir := writeCgroupTree(t, map[string]string{
			"memory.max":      "10737418240\n",
			"memory.stat":     "anon 4294967296\n",
			"memory.pressure": pressureBody("1.0"),
		})
		inside := filepath.Join(dir, "round-33")
		if err := os.MkdirAll(inside, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inside, "memory.stat"), []byte("anon 1073741824\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		outside := writeCgroupTree(t, map[string]string{"memory.stat": "anon 3221225472\n"})
		roundDir := func(pid int) (string, error) {
			switch pid {
			case 33:
				return inside, nil
			case 44:
				return dir, nil
			case 22:
				return outside, nil
			default:
				return "", errors.New("no cgroup")
			}
		}
		fakeLiveInputs(t, func() (string, error) { return dir, nil }, roundDir, mem16)
		// Unreadable and out-of-slice dirs subtract nothing; the round
		// inside subtracts its anon.
		budget, _, _ := env.srv.admissionInputs([]runningRound{{PID: 11}, {PID: 22}, {PID: 33}})
		if budget != 7*gb {
			t.Errorf("budget = %d, want 10GiB slice minus 3GiB non-round anon", budget)
		}
		// A round scoped at the budget dir itself and an inside round
		// larger than the dir's own anon clamp at zero, never negative.
		budget, _, _ = env.srv.admissionInputs([]runningRound{{PID: 33}, {PID: 44}})
		if budget != 10*gb {
			t.Errorf("budget = %d, want the whole 10GiB slice when anon clamps at zero", budget)
		}
	})

	t.Run("unreadable machine memory reads as no budget", func(t *testing.T) {
		env := setupTestEnv(t)
		admitZeroBudget(t, env)
		dir := writeCgroupTree(t, map[string]string{"memory.max": "max\n"})
		fakeLiveInputs(t,
			func() (string, error) { return dir, nil },
			nil,
			func() (int64, error) { return 0, errors.New("no meminfo") })
		budget, _, _ := env.srv.admissionInputs(nil)
		if budget != 0 {
			t.Errorf("budget = %d, want 0 with no slice, no override and no meminfo", budget)
		}
	})

	t.Run("budget dir error is brake-off", func(t *testing.T) {
		env := setupTestEnv(t)
		admitZeroBudget(t, env)
		fakeLiveInputs(t, func() (string, error) { return "", errors.New("no cgroup") }, nil, mem16)
		budget, _, pressureOK := env.srv.admissionInputs(nil)
		if budget != 14*gb {
			t.Errorf("budget = %d, want 16GiB machine minus 2GiB reserve", budget)
		}
		if !pressureOK {
			t.Error("pressureOK = false, want true with no budget dir to read a brake from")
		}
	})

	t.Run("numeric slice without memory.stat reads as full slice", func(t *testing.T) {
		env := setupTestEnv(t)
		admitZeroBudget(t, env)
		dir := writeCgroupTree(t, map[string]string{
			"memory.max":      "10737418240\n",
			"memory.pressure": pressureBody("1.0"),
		})
		fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, mem16)
		budget, _, _ := env.srv.admissionInputs(nil)
		if budget != 10*gb {
			t.Errorf("budget = %d, want the full 10GiB slice with no anon to subtract", budget)
		}
	})
}

// TestFindBudgetDir pins the budget walk: a service dir under a numeric slice
// reads the slice, a numeric dir reads itself, and a tree with no numeric
// limit reads the start dir, whose own pressure the caller then uses.
func TestFindBudgetDir(t *testing.T) {
	root := t.TempDir()
	slice := filepath.Join(root, "slice")
	service := filepath.Join(slice, "service")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMax := func(dir, max string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(max), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeMax(service, "max\n")
	writeMax(slice, "10737418240\n")
	if got := findBudgetDir(service); got != slice {
		t.Errorf("findBudgetDir(service) = %q, want slice %q", got, slice)
	}
	if got := findBudgetDir(slice); got != slice {
		t.Errorf("findBudgetDir(slice) = %q, want itself", got)
	}
	bare := filepath.Join(root, "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMax(bare, "max\n")
	if got := findBudgetDir(bare); got != bare {
		t.Errorf("findBudgetDir(bare) = %q, want start itself with no numeric limit", got)
	}
}

// TestAdmitMemoryHoldsBuilderAdmitsPlanner pins the memory hold through the
// stack: with a budget between a planner's and a builder's need, a queued
// builder stays queued while a queued planner that fits starts. The planner's
// need lands exactly on the budget, pinning the exact-fit boundary too.
func TestAdmitMemoryHoldsBuilderAdmitsPlanner(t *testing.T) {
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MemoryBudget: "11800M"}}
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	const gb = int64(1 << 30)
	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	if viewA := decodeView(t, bodyA); viewA.RoundState != remote.RoundRunning {
		t.Fatalf("A round_state = %q, want running", viewA.RoundState)
	}
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	} else if viewB.Queue == nil {
		t.Fatal("B Queue = nil, want the memory hold")
	} else if viewB.Queue.Wait != waitMemory || viewB.Queue.WaitBytes != 12*gb-11800*(1<<20) {
		t.Errorf("B wait = (%q, %d), want (memory, %d)", viewB.Queue.Wait, viewB.Queue.WaitBytes, 12*gb-11800*(1<<20))
	}
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")

	stampRole(t, testRuntime(t, env.srv, ownerC.id), "api", "lite-planner")

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundQueued)
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundRunning)

	// After the tick the planner's seat joins the running set, so B's
	// shortfall grows: both classes count toward one memory need.
	viewB := getBindingView(t, env, ownerB.kp, "api")
	if viewB.Queue == nil {
		t.Fatal("B Queue = nil, want the memory hold after the tick")
	}
	if viewB.Queue.Wait != waitMemory || viewB.Queue.WaitBytes != 25*gb/2-11800*(1<<20) {
		t.Errorf("B wait = (%q, %d), want (memory, %d)", viewB.Queue.Wait, viewB.Queue.WaitBytes, 25*gb/2-11800*(1<<20))
	}
}

// fairnessEnv builds the seat a fairness test needs: a 12GiB budget, a
// running builder at defaults, and a queued builder whose seeded history
// makes it unfittable, beside a later builder at defaults that fits.
func fairnessEnv(t *testing.T) (*testEnv, ownerEnv, ownerEnv) {
	t.Helper()
	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Candidates = set
		cfg.Registry = plannerAdmitRegistry(t, set)
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MemoryBudget: "12G"}}
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")
	seedCostHistory(t, env.srv.cfg.Root, ownerB.repoID, "builder", 8<<30, 8<<30, 6)

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	if viewA := decodeView(t, bodyA); viewA.RoundState != remote.RoundRunning {
		t.Fatalf("A round_state = %q, want running", viewA.RoundState)
	}
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}
	return env, ownerB, ownerC
}

// TestAdmitFairnessNineMinutesAdmits pins the young side of the fairness
// horizon: a builder held nine minutes does not block its class, so the
// later builder that fits starts.
func TestAdmitFairnessNineMinutesAdmits(t *testing.T) {
	env, ownerB, ownerC := fairnessEnv(t)

	backdateQueuedAt(t, testRuntime(t, env.srv, ownerB.id), "api", 9*time.Minute)

	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	if viewC := decodeView(t, bodyC); viewC.RoundState != remote.RoundRunning {
		t.Fatalf("C round_state = %q, want running: a nine-minute hold must not block its class", viewC.RoundState)
	}

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundQueued)
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundRunning)
}

// TestAdmitFairnessElevenMinutesBlocks pins the old side of the fairness
// horizon: a builder held eleven minutes blocks later builders of its class,
// even one that would fit. The skipped round stamps a memory wait with no
// bytes, since the decision never ran to measure one.
func TestAdmitFairnessElevenMinutesBlocks(t *testing.T) {
	env, ownerB, ownerC := fairnessEnv(t)

	backdateQueuedAt(t, testRuntime(t, env.srv, ownerB.id), "api", 11*time.Minute)

	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	if viewC := decodeView(t, bodyC); viewC.RoundState != remote.RoundQueued {
		t.Fatalf("C round_state = %q, want queued: an eleven-minute hold blocks its class", viewC.RoundState)
	}

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	rtC := testRuntime(t, env.srv, ownerC.id)
	requireRoundState(t, rtC, "api", remote.RoundQueued)
	if b, err := rtC.Store.Load("api"); err != nil {
		t.Fatal(err)
	} else if b.Builder.PID != 0 {
		t.Errorf("C PID = %d, want 0: the blocked round takes no seat", b.Builder.PID)
	}
	viewC := getBindingView(t, env, ownerC.kp, "api")
	if viewC.Queue == nil {
		t.Fatal("C Queue = nil, want the fairness wait")
	}
	if viewC.Queue.Wait != waitMemory || viewC.Queue.WaitBytes != 0 {
		t.Errorf("C wait = (%q, %d), want (memory, 0)", viewC.Queue.Wait, viewC.Queue.WaitBytes)
	}

	const gb = int64(1 << 30)
	viewB := getBindingView(t, env, ownerB.kp, "api")
	if viewB.Queue == nil {
		t.Fatal("B Queue = nil, want the memory hold")
	}
	if viewB.Queue.Wait != waitMemory || viewB.Queue.WaitBytes != 25*gb-12*gb {
		t.Errorf("B wait = (%q, %d), want (memory, %d)", viewB.Queue.Wait, viewB.Queue.WaitBytes, 25*gb-12*gb)
	}
}

// TestAdmitOversizeAloneStarts pins the oversize rule through the stack: a
// candidate whose default cost exceeds the budget starts when no builder
// runs, and is memory-held once one does.
func TestAdmitOversizeAloneStarts(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MemoryBudget: "10G"}}
	})
	ownerB := addOwner(t, env, "bob")

	const gb = int64(1 << 30)
	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	if viewA := decodeView(t, bodyA); viewA.RoundState != remote.RoundRunning {
		t.Fatalf("A round_state = %q, want running: an oversize candidate alone in its class starts", viewA.RoundState)
	}
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerB.id), "api", remote.RoundQueued)
	viewB := getBindingView(t, env, ownerB.kp, "api")
	if viewB.Queue == nil {
		t.Fatal("B Queue = nil, want the memory hold")
	}
	if viewB.Queue.Wait != waitMemory || viewB.Queue.WaitBytes != 2*gb {
		t.Errorf("B wait = (%q, %d), want (memory, %d)", viewB.Queue.Wait, viewB.Queue.WaitBytes, 2*gb)
	}
}

// TestAdmitHeldRoundsReportWaits pins the user-visible waits: a memory-held
// round reports its shortfall, while ceiling and pressure holds report no
// bytes.
func TestAdmitHeldRoundsReportWaits(t *testing.T) {
	const gb = int64(1 << 30)

	t.Run("memory", func(t *testing.T) {
		env := setupTestEnv(t, func(cfg *Config) {
			cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MemoryBudget: "10G"}}
		})
		ownerB := addOwner(t, env, "bob")

		respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
		requireCreated(t, respA, bodyA, "A")
		respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
		requireCreated(t, respB, bodyB, "B")

		if err := env.srv.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		viewB := getBindingView(t, env, ownerB.kp, "api")
		if viewB.Queue == nil {
			t.Fatal("B Queue = nil, want the memory hold")
		}
		if viewB.Queue.Wait != waitMemory || viewB.Queue.WaitBytes != 2*gb {
			t.Errorf("B wait = (%q, %d), want (memory, %d)", viewB.Queue.Wait, viewB.Queue.WaitBytes, 2*gb)
		}
	})

	t.Run("ceiling", func(t *testing.T) {
		env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
		ownerB := addOwner(t, env, "bob")

		respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
		requireCreated(t, respA, bodyA, "A")
		if viewA := decodeView(t, bodyA); viewA.RoundState != remote.RoundRunning {
			t.Fatalf("A round_state = %q, want running", viewA.RoundState)
		}
		respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
		requireCreated(t, respB, bodyB, "B")

		if err := env.srv.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		viewB := getBindingView(t, env, ownerB.kp, "api")
		if viewB.Queue == nil {
			t.Fatal("B Queue = nil, want the ceiling hold")
		}
		if viewB.Queue.Wait != waitCeiling || viewB.Queue.WaitBytes != 0 {
			t.Errorf("B wait = (%q, %d), want (ceiling, 0)", viewB.Queue.Wait, viewB.Queue.WaitBytes)
		}
	})

	t.Run("pressure", func(t *testing.T) {
		env := setupTestEnv(t)
		dir := writeCgroupTree(t, map[string]string{
			"memory.max":      "max\n",
			"memory.stat":     "anon 0\n",
			"memory.pressure": pressureBody("10.0"),
		})
		fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, nil)

		respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
		requireCreated(t, respA, bodyA, "A")
		if viewA := decodeView(t, bodyA); viewA.RoundState != remote.RoundQueued {
			t.Fatalf("A round_state = %q, want queued behind the pressure brake", viewA.RoundState)
		}

		if err := env.srv.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		viewA := getBindingView(t, env, env.kp, "api")
		if viewA.Queue == nil {
			t.Fatal("A Queue = nil, want the pressure hold")
		}
		if viewA.Queue.Wait != waitPressure || viewA.Queue.WaitBytes != 0 {
			t.Errorf("A wait = (%q, %d), want (pressure, 0)", viewA.Queue.Wait, viewA.Queue.WaitBytes)
		}
	})
}

// TestAdminHeldRoundReportsWait pins the admin threading: a memory-held
// round's shortfall reaches the admin and fleet surfaces with the same wait
// and bytes the caller's own queue view carries, since adminOwnerStatus
// threads the pass's waits into every queued row.
func TestAdminHeldRoundReportsWait(t *testing.T) {
	const gb = int64(1 << 30)
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MemoryBudget: "10G"}}
	})
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	owners, _, err := AdminStatus(context.Background(), env.srv)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	var adminRow *view.BindingStatus
	for i := range owners {
		if owners[i].Label != "bob" {
			continue
		}
		for j := range owners[i].Report.Bindings {
			if owners[i].Report.Bindings[j].Name == "api" {
				adminRow = &owners[i].Report.Bindings[j]
			}
		}
	}
	if adminRow == nil {
		t.Fatal("bob's api row missing from AdminStatus")
	}
	if adminRow.Queued == nil {
		t.Fatal("bob's admin row has no QueueView, want the memory hold")
	}
	if adminRow.Queued.Wait != waitMemory || adminRow.Queued.WaitBytes != 2*gb {
		t.Errorf("admin wait = (%q, %d), want (memory, %d)", adminRow.Queued.Wait, adminRow.Queued.WaitBytes, 2*gb)
	}

	flat, err := FlatStatus(context.Background(), env.srv)
	if err != nil {
		t.Fatalf("FlatStatus: %v", err)
	}
	var fleetRow *view.BindingStatus
	for i := range flat.Bindings {
		if flat.Bindings[i].OwnerLabel == "bob" && flat.Bindings[i].Name == "api" {
			fleetRow = &flat.Bindings[i]
		}
	}
	if fleetRow == nil {
		t.Fatal("bob's api row missing from FlatStatus")
	}
	if fleetRow.Queued == nil {
		t.Fatal("bob's fleet row has no QueueView, want the memory hold")
	}
	if fleetRow.Queued.Wait != waitMemory || fleetRow.Queued.WaitBytes != 2*gb {
		t.Errorf("fleet wait = (%q, %d), want (memory, %d)", fleetRow.Queued.Wait, fleetRow.Queued.WaitBytes, 2*gb)
	}
}
