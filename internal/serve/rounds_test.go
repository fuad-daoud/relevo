package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// plannerCandidateSet is the haiku candidate a served planner binding runs. It
// lists only roles harness knows -- candidate.Load drops a candidate whose role
// is unknown -- while the planner row in plannerRegistry assigns it, which is
// the only place file-mode roles.json assigns candidates.
func plannerCandidateSet(t *testing.T) *candidate.Set {
	t.Helper()
	body := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
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

// plannerRegistry is the server's own roles.json for the force test: a builder
// row and a planner reader row whose claude definition agent is the
// mastermind's architect, which is what turns the seed cap on.
func plannerRegistry(t *testing.T, set *candidate.Set) *roles.Registry {
	t.Helper()
	reader := "reader"
	reg, err := roles.Build(&roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"claude/anthropic/haiku"}},
		"planner": {
			Shape:       &reader,
			Candidates:  []string{"claude/anthropic/haiku"},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "architect"}},
		},
	}}, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return reg
}

// TestStartRoundForceSendsAnOverCapPlannerSeed pins the server half: the
// round request's force field is applied to the same seed-cap check a local
// send uses, so an over-cap planner seed is admitted (201) with force and
// refused (not 201) without it.
func TestStartRoundForceSendsAnOverCapPlannerSeed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		force bool
	}{
		{"forced", true},
		{"unforced", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := plannerCandidateSet(t)
			env := setupTestEnv(t, func(cfg *Config) {
				cfg.Candidates = set
				cfg.Registry = plannerRegistry(t, set)
			})

			createBody, _ := json.Marshal(remote.CreateBindingRequest{
				Name:       "seed",
				RepoID:     env.repoID,
				BaseCommit: env.headSHA,
				Role:       "planner",
			})
			resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
			requireStatus(t, resp, body, http.StatusCreated)

			outRef := "refs/relevo/seed/out"
			if err := env.gitClient.UpdateRef(context.Background(), env.clientDir, outRef, env.headSHA, ""); err != nil {
				t.Fatalf("updateRef out: %v", err)
			}
			plan := strings.Repeat("x", 4097)
			formBytes, ct := makeRoundFormForce(t, 1, plan, snapshotRef(t, env, env.clientDir, outRef), tc.force)
			resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/seed/rounds", formBytes, ct)

			if tc.force {
				requireCreated(t, resp, body, "forced planner seed")
				view := decodeView(t, body)
				if view.Round != 1 {
					t.Fatalf("view.Round = %d, want 1", view.Round)
				}
				if view.RoundState == remote.RoundIdle {
					t.Fatalf("RoundState = %q, want a non-idle round", view.RoundState)
				}
				return
			}
			if resp.StatusCode == http.StatusCreated {
				t.Fatalf("unforced over-cap send started a round: status = %d; body: %s", resp.StatusCode, string(body))
			}
			if !strings.Contains(string(body), "4096") {
				t.Fatalf("unforced refusal body = %q, want it to name the 4096-byte cap", string(body))
			}
		})
	}
}

// countingHooks records every dispatched hook event so a test can assert that a
// lifecycle event fired once. Dispatch is called from the handler goroutines, so
// the count is taken under a mutex.
type countingHooks struct {
	mu     sync.Mutex
	events []hooks.Event
}

func (c *countingHooks) Dispatch(_ context.Context, event hooks.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

// count is how many events of type t were dispatched.
func (c *countingHooks) count(t hooks.EventType) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ev := range c.events {
		if ev.Type == t {
			n++
		}
	}
	return n
}

// countLogKind is how many of the binding's log entries carry kind.
func countLogKind(entries []store.LogEntry, kind store.Kind) int {
	n := 0
	for _, e := range entries {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// countAcceptEntries is how many queue entries name an accept rather than an
// admit. Both are store.KindQueue: recordRoundAccepted writes "queued (x/y
// builders busy)" when the round is accepted, and Admit writes "started after
// ... queued" when its slot frees, so the accept is the one a duplicate start
// would duplicate.
func countAcceptEntries(entries []store.LogEntry) int {
	n := 0
	for _, e := range entries {
		if e.Kind == store.KindQueue && strings.HasPrefix(e.Note, "queued (") {
			n++
		}
	}
	return n
}

// TestDuplicateRoundStartIsAtomicPerOwner pins the exclusion finishRoundStart
// takes: two identical concurrent round starts for one binding yield one 201
// and one 200 retry, exactly one prompt entry, one queue entry and one queued
// hook, and one builder process.
//
// Without the re-decision under the section both requests pass handleStartRound's
// unlocked decision and both reach Send, so each writes its own prompt entry,
// queue entry and queued hook.
func TestDuplicateRoundStartIsAtomicPerOwner(t *testing.T) {
	hookEvents := &countingHooks{}
	env := setupTestEnv(t, func(cfg *Config) { cfg.Hooks = hookEvents })

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)

	results := fireConcurrently(
		signedPost(t, env.ts, env.kp, "/v1/bindings/api/rounds", formBytes, ct),
		signedPost(t, env.ts, env.kp, "/v1/bindings/api/rounds", formBytes, ct),
	)
	createdBody, retriedBody := requireCreatedAndRetried(t, "concurrent round start", results)

	created := decodeView(t, createdBody)
	retried := decodeView(t, retriedBody)
	if retried.Round != created.Round || retried.Name != created.Name {
		t.Errorf("retried view = %+v, want the created view %+v", retried, created)
	}

	rt := env.runtime(t)
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if n := countLogKind(entries, store.KindPrompt); n != 1 {
		t.Errorf("prompt entries = %d, want 1 (a second start would append its own)", n)
	}
	if n := countAcceptEntries(entries); n != 1 {
		t.Errorf("accept queue entries = %d, want 1 (a second start would record its own accept)", n)
	}
	if n := hookEvents.count(hooks.EventRoundQueued); n != 1 {
		t.Errorf("round_queued hooks = %d, want 1", n)
	}
	if n := len(startedSpecs(env)); n != 1 {
		t.Errorf("runner starts = %d, want 1 (Admit's ErrNotQueued is the backstop, not the fix)", n)
	}
}

// TestDuplicateRoundStartDoesNotBlockAnotherOwner pins the width of the round
// start's section: it is one owner's, so owner B's round start is never waited
// on by owner A's in-flight start of the same binding name.
func TestDuplicateRoundStartDoesNotBlockAnotherOwner(t *testing.T) {
	env := setupTestEnv(t)
	ownerB := addOwner(t, env, "bob")

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name: "api", RepoID: env.repoID, BaseCommit: env.headSHA, Role: "builder",
	})
	if resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("A create binding status = %d; body: %s", resp.StatusCode, string(body))
	}
	if resp, body := doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings", createBody, "application/json"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("B create binding status = %d; body: %s", resp.StatusCode, string(body))
	}

	outRef := "refs/relevo/api/out"
	if err := env.gitClient.UpdateRef(context.Background(), env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatalf("A out ref: %v", err)
	}
	if err := env.gitClient.UpdateRef(context.Background(), ownerB.clientDir, outRef, ownerB.headSHA, ""); err != nil {
		t.Fatalf("B out ref: %v", err)
	}
	formA, ctA := makeRoundForm(t, 1, "# Plan A", snapshotRef(t, env, env.clientDir, outRef))
	formB, ctB := makeRoundForm(t, 1, "# Plan B", snapshotRef(t, env, ownerB.clientDir, outRef))

	results := fireConcurrently(
		signedPost(t, env.ts, env.kp, "/v1/bindings/api/rounds", formA, ctA),
		signedPost(t, env.ts, ownerB.kp, "/v1/bindings/api/rounds", formB, ctB),
	)
	for i, res := range results {
		if res.err != nil {
			t.Fatalf("cross-owner round start %d failed: %v", i, res.err)
		}
		if res.status != http.StatusCreated {
			t.Fatalf("cross-owner round start %d answered %d; body: %s", i, res.status, string(res.body))
		}
	}
}
