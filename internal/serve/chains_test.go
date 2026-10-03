package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainWireRequest is the create body every chain test starts from: one plan,
// the default actor settings and a git identity.
func chainWireRequest(name, repoID, base string) remote.CreateChainRequest {
	return remote.CreateChainRequest{
		Name:       name,
		RepoID:     repoID,
		BaseCommit: base,
		Plans:      []string{"# Plan 1\nDo the thing"},
		Settings: remote.ChainSettings{
			MaxCorrections: 2,
			ReviewerActor:  "reviewer",
			PlannerActor:   "researcher",
		},
		Author: &remote.GitIdentity{Name: "Test User", Email: "test@example.com"},
	}
}

// makeChainForm writes the multipart create: the "chain" JSON field and, when
// non-empty, one "bundle" file part.
func makeChainForm(t *testing.T, req remote.CreateChainRequest, bundleBytes []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal chain request: %v", err)
	}
	if err := mw.WriteField("chain", string(body)); err != nil {
		t.Fatalf("write chain field: %v", err)
	}
	writeBundlePart(t, mw, bundleBytes)
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// chainBundle points clientDir's refs/relevo/<name>/out at headSHA and snapshots
// it: the base bundle a chain create ships.
func chainBundle(t *testing.T, env *testEnv, clientDir, headSHA, name string) []byte {
	t.Helper()
	outRef := "refs/relevo/" + name + "/out"
	if err := env.gitClient.UpdateRef(context.Background(), clientDir, outRef, headSHA, ""); err != nil {
		t.Fatalf("updateRef %s: %v", outRef, err)
	}
	return snapshotRef(t, env, clientDir, outRef)
}

func createChainAs(t *testing.T, env *testEnv, kp remote.Keypair, clientDir, repoID, headSHA, name string) (*http.Response, []byte) {
	t.Helper()
	req := chainWireRequest(name, repoID, headSHA)
	bundle := chainBundle(t, env, clientDir, headSHA, name)
	form, ct := makeChainForm(t, req, bundle)
	return doSigned(t, env.ts, kp, "POST", "/v1/chains", form, ct)
}

func createChain(t *testing.T, env *testEnv, name string) (*http.Response, []byte) {
	t.Helper()
	return createChainAs(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, name)
}

func decodeChainView(t *testing.T, body []byte) remote.ChainView {
	t.Helper()
	var view remote.ChainView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal chain view: %v; body: %s", err, string(body))
	}
	return view
}

func decodeErrorBody(t *testing.T, body []byte) remote.ErrorBody {
	t.Helper()
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v; body: %s", err, string(body))
	}
	return errBody
}

// requireChainAbsent asserts a refused create left no chain row and no member
// binding.
func requireChainAbsent(t *testing.T, env *testEnv, name string) {
	t.Helper()
	rt := env.runtime(t)
	if _, err := rt.Store.Chain(name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("chain %q exists after the refusal: %v", name, err)
	}
	for _, member := range []string{name, name + "-rev", name + "-plan", name + "-sec"} {
		if _, err := rt.Store.Load(member); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("binding %q exists after the refusal: %v", member, err)
		}
	}
}

// requireChainRepoAbsent asserts the owner's bare repo was never created, which
// holds when a refusal fires before the absorb.
func requireChainRepoAbsent(t *testing.T, env *testEnv) {
	t.Helper()
	repoRoot, err := env.srv.repoRoot(env.id)
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(repoRoot, env.repoID+".git")
	if _, err := os.Stat(bare); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bare repo %s exists after the refusal (stat err = %v)", bare, err)
	}
}

func chainRepoPath(t *testing.T, env *testEnv) string {
	t.Helper()
	repoRoot, err := env.srv.repoRoot(env.id)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(repoRoot, env.repoID+".git")
}

// postResult is one concurrent request's outcome: its status, its body, and the
// error when it never got a usable response.
type postResult struct {
	status int
	body   []byte
	err    error
}

// signedPost builds a ready-to-fire signed POST. It runs on the test goroutine
// because signedRequest may t.Fatalf, which is only legal there; the request is
// then handed to fireConcurrently.
func signedPost(t *testing.T, ts *httptest.Server, kp remote.Keypair, path string, body []byte, contentType string) *http.Request {
	t.Helper()
	req := signedRequest(t, kp, "POST", path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(ts.URL, "http://")
	return req
}

// fireConcurrently issues every request at once and returns their outcomes in
// the order given. The client carries a timeout, so a request that blocks fails
// its own assertion rather than hanging the suite.
func fireConcurrently(reqs ...*http.Request) []postResult {
	out := make([]postResult, len(reqs))
	done := make(chan struct{}, len(reqs))
	client := &http.Client{Timeout: 30 * time.Second}
	start := make(chan struct{})
	for i, req := range reqs {
		go func() {
			defer func() { done <- struct{}{} }()
			<-start
			resp, err := client.Do(req)
			if err != nil {
				out[i] = postResult{err: err}
				return
			}
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			out[i] = postResult{status: resp.StatusCode, body: body, err: readErr}
		}()
	}
	close(start)
	for range reqs {
		<-done
	}
	return out
}

// requireCreatedAndRetried asserts the duplicate-request shape: across the
// concurrent requests, exactly one 201 and exactly one 200 retry. It returns
// both bodies so a caller can compare the created view against the retried one.
func requireCreatedAndRetried(t *testing.T, label string, results []postResult) (created, retried []byte) {
	t.Helper()
	var createdBodies, retriedBodies [][]byte
	for _, res := range results {
		if res.err != nil {
			t.Fatalf("%s: a concurrent request failed: %v", label, res.err)
		}
		switch res.status {
		case http.StatusCreated:
			createdBodies = append(createdBodies, res.body)
		case http.StatusOK:
			retriedBodies = append(retriedBodies, res.body)
		default:
			t.Fatalf("%s: a concurrent request answered %d; body: %s", label, res.status, string(res.body))
		}
	}
	if len(createdBodies) != 1 || len(retriedBodies) != 1 {
		t.Fatalf("%s: concurrent requests = %d created + %d retried, want 1 created + 1 retried",
			label, len(createdBodies), len(retriedBodies))
	}
	return createdBodies[0], retriedBodies[0]
}

func TestCreateChainRunsPlanOneOnTheServer(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	resp, body := createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusCreated)
	view := decodeChainView(t, body)
	if view.Name != "shop" || view.Status != string(chain.StatusRunning) {
		t.Fatalf("view = %+v, want shop running", view)
	}
	if len(view.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(view.Members))
	}

	rt := env.runtime(t)
	if _, err := rt.Store.Chain("shop"); err != nil {
		t.Fatalf("chain row: %v", err)
	}
	builder, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("load builder: %v", err)
	}
	if builder.Owner != string(env.id) {
		t.Errorf("builder owner = %q, want %q", builder.Owner, env.id)
	}
	if builder.Worktree == "" || builder.Branch != "relevo/shop" || builder.CWD != builder.Worktree {
		t.Fatalf("builder = worktree %q branch %q cwd %q, want the served pair", builder.Worktree, builder.Branch, builder.CWD)
	}
	if _, err := os.Stat(builder.Worktree); err != nil {
		t.Fatalf("builder worktree missing: %v", err)
	}
	head, ok, err := env.gitClient.RefSHA(ctx, builder.Worktree, "HEAD")
	if err != nil || !ok || head != env.headSHA {
		t.Fatalf("worktree HEAD = (%q, %v, %v), want %q", head, ok, err, env.headSHA)
	}

	for _, name := range []string{"shop-rev", "shop-plan"} {
		m, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if m.CWD != builder.Worktree {
			t.Errorf("%s CWD = %q, want the builder's worktree %q", name, m.CWD, builder.Worktree)
		}
		if m.Worktree != "" || m.Branch != "" {
			t.Errorf("%s worktree/branch = %q/%q, want both empty", name, m.Worktree, m.Branch)
		}
	}

	entries, err := rt.Store.ReadLog("shop")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	switch st := relevo.RoundStateOf(builder, entries); st {
	case remote.RoundQueued, remote.RoundRunning:
	default:
		t.Fatalf("plan 1 round state = %q, want queued or running", st)
	}
}

func TestCreateChainRefusesUntrustedFields(t *testing.T) {
	tooLongName := strings.Repeat("x", chainMaxNameLen+1)
	bigPlan := strings.Repeat("z", maxChainPlanBytes+1)
	manyPlans := make([]string, maxChainPlans+1)
	for i := range manyPlans {
		manyPlans[i] = "plan"
	}

	cases := []struct {
		name string
		mut  func(*remote.CreateChainRequest)
		want int
	}{
		{"invalid name", func(r *remote.CreateChainRequest) { r.Name = "invalid/name" }, http.StatusBadRequest},
		{"name too long", func(r *remote.CreateChainRequest) { r.Name = tooLongName }, http.StatusBadRequest},
		{"bad repo id", func(r *remote.CreateChainRequest) { r.RepoID = "short" }, http.StatusBadRequest},
		{"uppercase repo id", func(r *remote.CreateChainRequest) { r.RepoID = strings.ToUpper(r.RepoID) }, http.StatusBadRequest},
		{"bad base commit", func(r *remote.CreateChainRequest) { r.BaseCommit = "not-a-sha" }, http.StatusBadRequest},
		{"missing author", func(r *remote.CreateChainRequest) { r.Author = nil }, http.StatusBadRequest},
		{"bad author", func(r *remote.CreateChainRequest) {
			r.Author = &remote.GitIdentity{Name: "a\nb", Email: "e@example.com"}
		}, http.StatusBadRequest},
		{"no plans", func(r *remote.CreateChainRequest) { r.Plans = nil }, http.StatusBadRequest},
		{"empty plan", func(r *remote.CreateChainRequest) { r.Plans = []string{"   "} }, http.StatusBadRequest},
		{"plan too big", func(r *remote.CreateChainRequest) { r.Plans = []string{bigPlan} }, http.StatusBadRequest},
		{"too many plans", func(r *remote.CreateChainRequest) { r.Plans = manyPlans }, http.StatusBadRequest},
		{"max_corrections too high", func(r *remote.CreateChainRequest) { r.Settings.MaxCorrections = maxChainCorrections + 1 }, http.StatusBadRequest},
		{"regate too high", func(r *remote.CreateChainRequest) { r.Settings.Regate = maxChainRegate + 1 }, http.StatusBadRequest},
		{"empty reviewer actor", func(r *remote.CreateChainRequest) { r.Settings.ReviewerActor = "" }, http.StatusBadRequest},
		{"empty planner actor", func(r *remote.CreateChainRequest) { r.Settings.PlannerActor = "" }, http.StatusBadRequest},
		{"security on without a security actor", func(r *remote.CreateChainRequest) {
			r.Settings.Security = true
			r.Settings.SecurityActor = ""
		}, http.StatusBadRequest},
		{"unknown client binding part", func(r *remote.CreateChainRequest) {
			r.ClientBindingIDs = map[string]string{"nope": "x"}
		}, http.StatusBadRequest},
		{"unknown actor", func(r *remote.CreateChainRequest) { r.Settings.ReviewerActor = "ghost" }, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestEnv(t)
			req := chainWireRequest("shop", env.repoID, env.headSHA)
			tc.mut(&req)
			form, ct := makeChainForm(t, req, nil)
			resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, tc.want, string(body))
			}
			requireChainAbsent(t, env, "shop")
			requireChainRepoAbsent(t, env)
		})
	}
}

// TestCreateChainUnknownActorIsTyped pins the refusal's wire code: an actor no
// role defines is 400 unknown_actor, the twin of relevo.ErrUnknownRole the
// client re-types, not a bare invalid.
func TestCreateChainUnknownActorIsTyped(t *testing.T) {
	env := setupTestEnv(t)
	req := chainWireRequest("shop", env.repoID, env.headSHA)
	req.Settings.ReviewerActor = "ghost"
	form, ct := makeChainForm(t, req, nil)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("decode error body: %v; body: %s", err, string(body))
	}
	if errBody.Code != remote.CodeUnknownActor {
		t.Errorf("error code = %q, want %q; body: %s", errBody.Code, remote.CodeUnknownActor, string(body))
	}
	requireChainAbsent(t, env, "shop")
	requireChainRepoAbsent(t, env)
}

// TestCreateChainRefusesATierAboveMax pins the preflight's tier mapping: a
// server whose policy tier for the builder is above its max_tier refuses the
// create 422 tier_above_max with nothing created.
func TestCreateChainRefusesATierAboveMax(t *testing.T) {
	env := setupTestEnv(t, func(c *Config) {
		c.Policy = policy.Policy{Tier: map[string]string{"builder": "edit"}, MaxTier: "read"}
	})

	req := chainWireRequest("shop", env.repoID, env.headSHA)
	form, ct := makeChainForm(t, req, nil)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	requireStatus(t, resp, body, http.StatusUnprocessableEntity)
	if errBody := decodeErrorBody(t, body); errBody.Code != remote.CodeTierAboveMax {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeTierAboveMax)
	}
	requireChainAbsent(t, env, "shop")
	requireChainRepoAbsent(t, env)
}

func TestCreateChainRefusesABundleWithoutTheBase(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	// A second commit the bundle's out ref will point at, while base_commit
	// still names the first.
	if err := os.WriteFile(filepath.Join(env.clientDir, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, env.clientDir, "add", "second.txt")
	runGit(t, env.clientDir, "commit", "-m", "second commit")
	secondSHA, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "HEAD")
	if err != nil || !ok {
		t.Fatalf("second head: %v, ok=%v", err, ok)
	}
	if secondSHA == env.headSHA {
		t.Fatal("second commit sha equals the base")
	}

	req := chainWireRequest("shop", env.repoID, env.headSHA)
	bundle := chainBundle(t, env, env.clientDir, secondSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	requireStatus(t, resp, body, http.StatusUnprocessableEntity)
	if errBody := decodeErrorBody(t, body); errBody.Code != remote.CodeNotFastForward {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeNotFastForward)
	}
	requireChainAbsent(t, env, "shop")

	bare := chainRepoPath(t, env)
	if _, ok, err := env.gitClient.RefSHA(ctx, bare, "refs/heads/relevo/shop"); err != nil || ok {
		t.Errorf("branch relevo/shop = (ok %v, err %v), want none", ok, err)
	}
	if _, ok, err := env.gitClient.RefSHA(ctx, bare, "refs/relevo/shop/out"); err != nil || ok {
		t.Errorf("out ref = (ok %v, err %v), want none after the unwind", ok, err)
	}
}

func TestCreateChainUnwindsTheWorktree(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()
	rt := env.runtime(t)

	// Force ServedChainCreate to fail after the checkout: a file where the
	// chain's plan directory would be, so the plan copies cannot be made.
	ownerRoot, err := env.srv.ownerRoot(env.id)
	if err != nil {
		t.Fatal(err)
	}
	chainsDir := filepath.Join(ownerRoot, ".chains")
	if err := os.MkdirAll(chainsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chainsDir, "shop"), []byte("in the way\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, body := createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusInternalServerError)

	if _, err := os.Stat(rt.Store.WorktreePath("shop")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worktree %s still exists (stat err = %v), want it removed", rt.Store.WorktreePath("shop"), err)
	}
	bare := chainRepoPath(t, env)
	if _, ok, err := env.gitClient.RefSHA(ctx, bare, "refs/heads/relevo/shop"); err != nil || ok {
		t.Errorf("branch relevo/shop = (ok %v, err %v), want none", ok, err)
	}
	if _, ok, err := env.gitClient.RefSHA(ctx, bare, "refs/relevo/shop/out"); err != nil || ok {
		t.Errorf("out ref = (ok %v, err %v), want none", ok, err)
	}
}

func TestCreateChainRetryIsIdempotentOnlyWhenIdentical(t *testing.T) {
	env := setupTestEnv(t)

	resp, body := createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusCreated)
	first := decodeChainView(t, body)

	rt := env.runtime(t)
	bindingsBefore, err := rt.Store.List()
	if err != nil {
		t.Fatal(err)
	}

	// An identical retry, bundle and all, returns 200 with the same view.
	req := chainWireRequest("shop", env.repoID, env.headSHA)
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	requireStatus(t, resp, body, http.StatusOK)
	second := decodeChainView(t, body)
	if second.Name != first.Name || second.Status != first.Status || second.Base != first.Base {
		t.Fatalf("retry view = %+v, want the first view %+v", second, first)
	}
	bindingsAfter, err := rt.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(bindingsAfter) != len(bindingsBefore) {
		t.Fatalf("bindings after the retry = %d, want %d (an identical retry creates nothing)", len(bindingsAfter), len(bindingsBefore))
	}

	// A different plan under the same name is a conflict.
	changed := chainWireRequest("shop", env.repoID, env.headSHA)
	changed.Plans = []string{"# A different plan"}
	form, ct = makeChainForm(t, changed, nil)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	requireStatus(t, resp, body, http.StatusConflict)
	if errBody := decodeErrorBody(t, body); errBody.Code != remote.CodeInvalid || errBody.Message != "chain exists" {
		t.Fatalf("error = (%q, %q), want (invalid, chain exists)", errBody.Code, errBody.Message)
	}
}

// TestDuplicateChainCreateIsAtomicPerOwner pins the exclusion chainFinishCreate
// takes: two identical concurrent creates for one owner yield one 201 and one
// 200 retry, the chain row the winner wrote is the one that survives, and the
// branch and worktree are cut once.
//
// Without the section both requests clear the post-absorb re-check while the
// chain does not exist yet and both go on to cut the branch and create the row,
// so the second create answers 201 as well.
func TestDuplicateChainCreateIsAtomicPerOwner(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	req := chainWireRequest("shop", env.repoID, env.headSHA)
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)

	results := fireConcurrently(
		signedPost(t, env.ts, env.kp, "/v1/chains", form, ct),
		signedPost(t, env.ts, env.kp, "/v1/chains", form, ct),
	)
	createdBody, retriedBody := requireCreatedAndRetried(t, "concurrent chain create", results)

	created := decodeChainView(t, createdBody)
	retried := decodeChainView(t, retriedBody)
	if retried.Name != created.Name || retried.Status != created.Status || retried.Base != created.Base {
		t.Errorf("retried view = %+v, want the created view %+v", retried, created)
	}

	// The row the winner wrote is intact: the store still holds exactly one
	// chain and one binding per part, and the builder's worktree is the one the
	// winner checked out rather than a second cut over it.
	rt := env.runtime(t)
	row, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatalf("chain row after the pair: %v", err)
	}
	if row.Status != created.Status {
		t.Errorf("stored chain status = %q, want the created view's %q", row.Status, created.Status)
	}
	bindings, err := rt.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 3 {
		t.Errorf("bindings = %d, want 3 (one per member; a second create would add three more)", len(bindings))
	}
	builder, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("load builder: %v", err)
	}
	if _, statErr := os.Stat(builder.Worktree); statErr != nil {
		t.Errorf("builder worktree missing after the pair: %v", statErr)
	}
	head, ok, err := env.gitClient.RefSHA(ctx, builder.Worktree, "HEAD")
	if err != nil || !ok || head != env.headSHA {
		t.Errorf("worktree HEAD = (%q, %v, %v), want %q", head, ok, err, env.headSHA)
	}
	branch, ok, err := env.gitClient.RefSHA(ctx, chainRepoPath(t, env), "refs/heads/relevo/shop")
	if err != nil || !ok || branch != env.headSHA {
		t.Errorf("branch = (%q, %v, %v), want %q", branch, ok, err, env.headSHA)
	}
}

// TestDuplicateChainCreateDoesNotBlockAnotherOwner pins the width of that
// section: it is one owner's, so a second owner creating its own chain of the
// same name is never waited on by the first owner's in-flight create.
func TestDuplicateChainCreateDoesNotBlockAnotherOwner(t *testing.T) {
	env := setupTestEnv(t)
	ownerB := addOwner(t, env, "bob")

	reqA := chainWireRequest("shop", env.repoID, env.headSHA)
	formA, ctA := makeChainForm(t, reqA, chainBundle(t, env, env.clientDir, env.headSHA, "shop"))
	reqB := chainWireRequest("shop", ownerB.repoID, ownerB.headSHA)
	formB, ctB := makeChainForm(t, reqB, chainBundle(t, env, ownerB.clientDir, ownerB.headSHA, "shop"))

	results := fireConcurrently(
		signedPost(t, env.ts, env.kp, "/v1/chains", formA, ctA),
		signedPost(t, env.ts, ownerB.kp, "/v1/chains", formB, ctB),
	)
	for i, res := range results {
		if res.err != nil {
			t.Fatalf("concurrent cross-owner create %d failed: %v", i, res.err)
		}
		if res.status != http.StatusCreated {
			t.Fatalf("cross-owner create %d answered %d; body: %s", i, res.status, string(res.body))
		}
	}

	// Each owner holds its own chain of the same name, over its own bare repo.
	rtA := testRuntime(t, env.srv, env.id)
	rowA, err := rtA.Store.Chain("shop")
	if err != nil {
		t.Fatalf("A chain row: %v", err)
	}
	rtB := testRuntime(t, env.srv, ownerB.id)
	rowB, err := rtB.Store.Chain("shop")
	if err != nil {
		t.Fatalf("B chain row: %v", err)
	}
	if rowA.Repo == rowB.Repo {
		t.Errorf("both owners' chains name the same repo %q; the sections are not per-owner", rowA.Repo)
	}
}

func TestChainRoutesAreOwnerScoped(t *testing.T) {
	env := setupTestEnv(t)
	ownerB := addOwner(t, env, "bob")

	resp, body := createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusCreated)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{"GET", "/v1/chains/shop"},
		{"POST", "/v1/chains/shop/stop"},
		{"POST", "/v1/chains/shop/resume"},
		{"POST", "/v1/chains/shop/done"},
	} {
		resp, body := doSigned(t, env.ts, ownerB.kp, tc.method, tc.path, nil, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s as another owner = %d, want 404; body: %s", tc.method, tc.path, resp.StatusCode, string(body))
		}
	}

	// The same name under another owner is a different chain: it is allowed.
	resp, body = createChainAs(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "shop")
	requireStatus(t, resp, body, http.StatusCreated)
	view := decodeChainView(t, body)
	if view.Name != "shop" || view.Status != string(chain.StatusRunning) {
		t.Fatalf("B's chain view = %+v, want its own running shop", view)
	}
}

func TestChainStopResumeDoneOverTheWire(t *testing.T) {
	env := setupTestEnv(t, func(c *Config) { c.MaxBuilders = 1 })

	// Occupy the single builder slot so the chain's plan 1 stays queued.
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "decoy", "# Decoy")
	requireCreated(t, resp, body, "decoy")

	resp, body = createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusCreated)

	rt := env.runtime(t)
	builder, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatal(err)
	}
	if builder.QueuedAt.IsZero() {
		t.Fatal("plan 1 is not queued: QueuedAt is zero")
	}

	// Stopping the queued member dequeues it, which stops the chain.
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/stop", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	var stopResp remote.ChainStopResponse
	if err := json.Unmarshal(body, &stopResp); err != nil {
		t.Fatalf("unmarshal stop response: %v", err)
	}
	if stopResp.Action != "dequeued" {
		t.Errorf("stop action = %q, want dequeued", stopResp.Action)
	}
	if stopResp.Chain.Status != string(chain.StatusStopped) {
		t.Errorf("chain status after stop = %q, want stopped", stopResp.Chain.Status)
	}

	// Resume re-queues the member (the slot is still held).
	resumeBody, _ := json.Marshal(remote.ChainResumeRequest{})
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/resume", resumeBody, "application/json")
	requireStatus(t, resp, body, http.StatusOK)
	if view := decodeChainView(t, body); view.Status != string(chain.StatusRunning) {
		t.Fatalf("chain status after resume = %q, want running", view.Status)
	}
	builder, err = rt.Store.Load("shop")
	if err != nil {
		t.Fatal(err)
	}
	if builder.QueuedAt.IsZero() {
		t.Error("the resumed member is not queued: QueuedAt is zero")
	}

	// Done is refused while the chain is running.
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/done", nil, "")
	requireStatus(t, resp, body, http.StatusConflict)
	if errBody := decodeErrorBody(t, body); errBody.Code != remote.CodeChainRunning {
		t.Fatalf("done-while-running code = %q, want %q", errBody.Code, remote.CodeChainRunning)
	}

	// Stop again, then done works.
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/stop", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/done", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	view := decodeChainView(t, body)
	if view.Status != string(chain.StatusDone) {
		t.Fatalf("chain status after done = %q, want done", view.Status)
	}
	c, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != string(chain.StatusDone) {
		t.Errorf("stored chain status = %q, want done", c.Status)
	}
}

// TestResumeChainRefusesOutOfBoundSettings pins the resume route's bounds: a
// max_corrections or regate outside the create's range answers 400 and leaves
// the chain row byte-identical.
func TestResumeChainRefusesOutOfBoundSettings(t *testing.T) {
	env := setupTestEnv(t, func(c *Config) { c.MaxBuilders = 1 })

	// Occupy the single builder slot so the chain's plan 1 stays queued.
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "decoy", "# Decoy")
	requireCreated(t, resp, body, "decoy")

	resp, body = createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusCreated)

	// Stop the chain so the resume route is reachable.
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/stop", nil, "")
	requireStatus(t, resp, body, http.StatusOK)

	rt := env.runtime(t)
	before, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatal(err)
	}
	ip := func(v int) *int { return &v }

	for _, tc := range []struct {
		name string
		req  remote.ChainResumeRequest
	}{
		{"max_corrections below zero", remote.ChainResumeRequest{MaxCorrections: ip(-1)}},
		{"max_corrections above the cap", remote.ChainResumeRequest{MaxCorrections: ip(maxChainCorrections + 1)}},
		{"regate below zero", remote.ChainResumeRequest{Regate: ip(-1)}},
		{"regate above the cap", remote.ChainResumeRequest{Regate: ip(maxChainRegate + 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqBody, _ := json.Marshal(tc.req)
			resp, out := doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/resume", reqBody, "application/json")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(out))
			}
			after, err := rt.Store.Chain("shop")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("the refused resume changed the chain row:\nbefore: %+v\nafter:  %+v", before, after)
			}
		})
	}
}

// TestServedResumeMapsWireToParams pins the served resume's mapping: every wire
// field that was given becomes the workflow param it fills, so the engine a
// served chain runs reads the value through its own params.
func TestServedResumeMapsWireToParams(t *testing.T) {
	t.Parallel()

	maxCorrections, regate := 5, 2
	security := true
	gate := "make test"
	opts, bad := resumeOptionsFromWire("shop", remote.ChainResumeRequest{
		MaxCorrections: &maxCorrections, Regate: &regate, Security: &security, Gate: &gate,
		ReviewerActor: "reviewer", PlannerActor: "planner", SecurityActor: "security",
	})
	if bad != "" {
		t.Fatalf("resumeOptionsFromWire = %q, want no refusal", bad)
	}
	want := map[string]string{
		"max_corrections": "5", "regate": "2", "scan": "true", "gate": "make test",
		"reviewer": "reviewer", "planner": "planner", "security": "security",
	}
	if !reflect.DeepEqual(opts.Params, want) {
		t.Errorf("params = %v, want %v", opts.Params, want)
	}

	// A body that names nothing maps onto no params, so a resume that changes
	// nothing leaves the stored definition alone.
	empty, bad := resumeOptionsFromWire("shop", remote.ChainResumeRequest{})
	if bad != "" {
		t.Fatalf("resumeOptionsFromWire(empty) = %q, want no refusal", bad)
	}
	if len(empty.Params) != 0 {
		t.Errorf("params for an empty body = %v, want none", empty.Params)
	}

	// A present empty gate clears the check, and the param reads empty too.
	clear := ""
	noGate, bad := resumeOptionsFromWire("shop", remote.ChainResumeRequest{Gate: &clear})
	if bad != "" {
		t.Fatalf("resumeOptionsFromWire(--no-gate) = %q, want no refusal", bad)
	}
	if got, ok := noGate.Params["gate"]; !ok || got != "" {
		t.Errorf("gate param = %q (present %v), want present and empty", got, ok)
	}
}

// TestServedResumeRefusesAGateChangeOnAPlacedWriter pins the wire half of the
// refusal: a gate built by resumeOptionsFromWire is refused as a usage error
// when the chain's writer member runs on a server, because relevo has no route
// that updates a served binding's gate.
func TestServedResumeRefusesAGateChangeOnAPlacedWriter(t *testing.T) {
	env := setupTestEnv(t, func(c *Config) { c.MaxBuilders = 1 })

	// Occupy the single builder slot so the chain's plan 1 stays queued, and
	// stop the chain so the resume route is reachable.
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "decoy", "# Decoy")
	requireCreated(t, resp, body, "decoy")
	resp, body = createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusCreated)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/chains/shop/stop", nil, "")
	requireStatus(t, resp, body, http.StatusOK)

	rt := env.runtime(t)
	b, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("Load the chain's writer: %v", err)
	}
	b.Builder.Mode = store.ModeRemote
	b.Builder.Server = "zen"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("place the writer remotely: %v", err)
	}

	gate := "make test"
	opts, bad := resumeOptionsFromWire("shop", remote.ChainResumeRequest{Gate: &gate})
	if bad != "" {
		t.Fatalf("resumeOptionsFromWire = %q, want no refusal", bad)
	}
	if _, err := relevo.ChainResume(context.Background(), rt, opts); err == nil {
		t.Fatal("ChainResume with a wire gate on a placed writer = nil, want the refusal")
	} else if !errors.Is(err, relevo.ErrRefused) {
		t.Errorf("ChainResume = %v, want relevo.ErrRefused", err)
	}
}

// TestWriteChainResumeErrorMapsTheOpenRoundRefusal pins the wire shape: an open
// member round on a resume is a 409 round_open, not the 500 the default arm
// used to write, so the client can rebuild the typed refusal.
func TestWriteChainResumeErrorMapsTheOpenRoundRefusal(t *testing.T) {
	rec := httptest.NewRecorder()
	writeChainResumeError(rec, &relevo.RoundOpenError{Member: "x-plan", Round: 2})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var body remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != remote.CodeRoundOpen {
		t.Errorf("code = %q, want %q", body.Code, remote.CodeRoundOpen)
	}
	if !strings.Contains(body.Message, "relevo stop x-plan") {
		t.Errorf("message = %q, want it to name the stop command", body.Message)
	}
}

// TestWriteChainResumeErrorMapsTheChainDone pins the done refusal's wire shape:
// a finished chain is 409 chain_done, the code the client rebuilds as the
// typed done refusal, not a bare invalid.
func TestWriteChainResumeErrorMapsTheChainDone(t *testing.T) {
	rec := httptest.NewRecorder()
	writeChainResumeError(rec, errors.New("chain shop is done"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var body remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != remote.CodeChainDone {
		t.Errorf("code = %q, want %q", body.Code, remote.CodeChainDone)
	}
}

func TestWhoAmIAdvertisesChain(t *testing.T) {
	s, _ := newTestServer(t, 0)
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), "", time.Now()); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedRequest(t, kp, "GET", "/v1/whoami", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var who remote.WhoAmI
	if err := json.NewDecoder(rec.Body).Decode(&who); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !slices.Contains(who.Features, remote.FeatureChain) {
		t.Errorf("Features = %v, want %q", who.Features, remote.FeatureChain)
	}
}

func TestRoundBundleRefusesABranchlessMember(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	b := store.Binding{
		Name: "reader", Owner: string(env.id), CWD: "/somewhere", Round: 2,
		Shape: store.ShapeReader,
		Serve: &store.ServeFacts{BareRepo: "/bare/reader.git", ClosedRound: 1},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/reader/rounds/1/bundle", nil, "")
	requireStatus(t, resp, body, http.StatusNotFound)
	if errBody := decodeErrorBody(t, body); errBody.Message != "no branch" {
		t.Fatalf("message = %q, want %q", errBody.Message, "no branch")
	}
}

// TestServedCreateSettingsOnlyMapsToDefault pins the old-client/new-server
// compatibility: a settings-only create requires no workflow in the request
// and stores the default definition with settings mapped onto its params.
func TestServedCreateSettingsOnlyMapsToDefault(t *testing.T) {
	env := setupTestEnv(t)
	req := chainWireRequest("shop", env.repoID, env.headSHA)
	req.Settings.ReviewerActor = "reviewer"
	req.Settings.PlannerActor = "researcher"
	req.Settings.MaxCorrections = 3
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	rt := env.runtime(t)
	row, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatalf("Chain(shop): %v", err)
	}
	if len(row.WorkflowJSON) == 0 {
		t.Fatal("expected WorkflowJSON to be stored")
	}
	def, err := workflow.Parse(row.WorkflowJSON)
	if err != nil {
		t.Fatalf("parse stored workflow: %v", err)
	}
	if def.Name != workflow.Default().Name {
		t.Errorf("stored def name = %q, want default %q", def.Name, workflow.Default().Name)
	}
	if got := def.Params["reviewer"].Str; got != "reviewer" {
		t.Errorf("param reviewer = %q, want reviewer", got)
	}
	if got := def.Params["planner"].Str; got != "researcher" {
		t.Errorf("param planner = %q, want researcher", got)
	}
	if got := def.Params["max_corrections"].Int; got != 3 {
		t.Errorf("param max_corrections = %d, want 3", got)
	}
}

// TestServedCreateWorkflowValidatesAgainstServerActors pins that a custom
// workflow is validated against the server's own actors: an unknown actor
// returns 400 unknown_actor.
func TestServedCreateWorkflowValidatesAgainstServerActors(t *testing.T) {
	env := setupTestEnv(t)
	req := chainWireRequest("shop", env.repoID, env.headSHA)
	const customJSON = `{"name":"bad-actor","inputs":{"plans":"required"},"start":"a","steps":{"a":{"run":"ghost","on":{"done":"done"}}}}`
	req.Workflow = json.RawMessage(customJSON)
	req.ClientActorIDs = map[string]string{"ghost": "ghost-link"}
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(body))
	}
	errBody := decodeErrorBody(t, body)
	if errBody.Code != remote.CodeUnknownActor {
		t.Errorf("error code = %q, want %q; body: %s", errBody.Code, remote.CodeUnknownActor, string(body))
	}
	requireChainAbsent(t, env, "shop")
}

// TestServedCreateRefusesBadEdgeAndReaderAsWriter pins that structural workflow
// defects and shape mismatches return 400 invalid with the first problem text.
func TestServedCreateRefusesBadEdgeAndReaderAsWriter(t *testing.T) {
	t.Run("bad edge", func(t *testing.T) {
		env := setupTestEnv(t)
		req := chainWireRequest("shop", env.repoID, env.headSHA)
		const customJSON = `{"name":"bad-edge","inputs":{"plans":"required"},"start":"a","steps":{"a":{"run":"builder","on":{"nope":"done"}}}}`
		req.Workflow = json.RawMessage(customJSON)
		bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
		form, ct := makeChainForm(t, req, bundle)
		resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(body))
		}
		errBody := decodeErrorBody(t, body)
		if errBody.Code != remote.CodeInvalid {
			t.Errorf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
		}
		if !strings.Contains(errBody.Message, "undeclared") && !strings.Contains(errBody.Message, "rule 3") {
			t.Errorf("message = %q, want first problem text mentioning undeclared outcome", errBody.Message)
		}
		requireChainAbsent(t, env, "shop")
	})

	t.Run("reader as writer", func(t *testing.T) {
		env := setupTestEnv(t)
		req := chainWireRequest("shop", env.repoID, env.headSHA)
		req.Settings.ReviewerActor = "builder"
		bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
		form, ct := makeChainForm(t, req, bundle)
		resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(body))
		}
		errBody := decodeErrorBody(t, body)
		if errBody.Code != remote.CodeInvalid {
			t.Errorf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
		}
		if !strings.Contains(errBody.Message, "must be a") {
			t.Errorf("message = %q, want problem text mentioning must be a reader", errBody.Message)
		}
		requireChainAbsent(t, env, "shop")
	})
}

// TestServedCreateResolvesShippedSeedFromServerBinary pins that shipped seeds
// are resolved from the server binary: unknown seeds are rejected, valid ones run.
func TestServedCreateResolvesShippedSeedFromServerBinary(t *testing.T) {
	t.Run("bad shipped seed", func(t *testing.T) {
		env := setupTestEnv(t)
		req := chainWireRequest("shop", env.repoID, env.headSHA)
		const customJSON = `{"name":"bad-seed","inputs":{"plans":"required"},"start":"a","steps":{"a":{"run":"builder","seed":"shipped:nonexistent","on":{"done":"done"}}}}`
		req.Workflow = json.RawMessage(customJSON)
		bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
		form, ct := makeChainForm(t, req, bundle)
		resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(body))
		}
		errBody := decodeErrorBody(t, body)
		if errBody.Code != remote.CodeInvalid {
			t.Errorf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
		}
		if !strings.Contains(errBody.Message, "not a shipped seed") {
			t.Errorf("message = %q, want problem text", errBody.Message)
		}
		requireChainAbsent(t, env, "shop")
	})

	t.Run("good shipped seed", func(t *testing.T) {
		env := setupTestEnv(t)
		req := chainWireRequest("shop", env.repoID, env.headSHA)
		const customJSON = `{"name":"good-seed","inputs":{"plans":"required"},"start":"a","steps":{"a":{"run":"builder","seed":"shipped:review","on":{"done":"done"}}}}`
		req.Workflow = json.RawMessage(customJSON)
		bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
		form, ct := makeChainForm(t, req, bundle)
		resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
		requireStatus(t, resp, body, http.StatusCreated)
	})
}

// TestServedCreateRefusesFileSeed pins that file: seeds are refused on served
// creates, requiring the client to inline them instead.
func TestServedCreateRefusesFileSeed(t *testing.T) {
	env := setupTestEnv(t)
	req := chainWireRequest("shop", env.repoID, env.headSHA)
	const customJSON = `{"name":"file-seed","inputs":{"plans":"required"},"start":"a","steps":{"a":{"run":"builder","seed":"file:seeds/review.md","on":{"done":"done"}}}}`
	req.Workflow = json.RawMessage(customJSON)
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, string(body))
	}
	errBody := decodeErrorBody(t, body)
	if errBody.Code != remote.CodeInvalid {
		t.Errorf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}
	if !strings.Contains(errBody.Message, "file seeds must be inlined by the client") {
		t.Errorf("message = %q, want file seeds must be inlined by the client", errBody.Message)
	}
	requireChainAbsent(t, env, "shop")
}

// TestServedCreateRetryComparesDefinition pins the retry behavior for workflow
// creates: identical requests with different JSON key order succeed with 200,
// while changed definitions return 409 conflict.
func TestServedCreateRetryComparesDefinition(t *testing.T) {
	env := setupTestEnv(t)
	req := chainWireRequest("shop", env.repoID, env.headSHA)
	const wf1 = `{"name":"custom","inputs":{"plans":"required","task":"none"},"start":"a","steps":{"a":{"run":"builder","on":{"done":"done"}}}}`
	req.Workflow = json.RawMessage(wf1)
	req.ClientActorIDs = map[string]string{"builder": "b-id"}
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	// Same definition with different key order returns 200.
	const wfKeyOrder = `{"inputs":{"task":"none","plans":"required"},"steps":{"a":{"on":{"done":"done"},"run":"builder"}},"start":"a","name":"custom"}`
	reqRetry := chainWireRequest("shop", env.repoID, env.headSHA)
	reqRetry.Settings = remote.ChainSettings{}
	reqRetry.Workflow = json.RawMessage(wfKeyOrder)
	reqRetry.ClientActorIDs = map[string]string{"builder": "b-id"}
	formRetry, ctRetry := makeChainForm(t, reqRetry, nil)
	respRetry, bodyRetry := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", formRetry, ctRetry)
	requireStatus(t, respRetry, bodyRetry, http.StatusOK)

	// Changed definition returns 409 conflict.
	const wfChanged = `{"name":"custom","inputs":{"plans":"required","task":"none"},"start":"a","params":{"timeout":{"kind":"int","int":30}},"steps":{"a":{"run":"builder","on":{"done":"done"}}}}`
	reqDiff := chainWireRequest("shop", env.repoID, env.headSHA)
	reqDiff.Workflow = json.RawMessage(wfChanged)
	reqDiff.ClientActorIDs = map[string]string{"builder": "b-id"}
	formDiff, ctDiff := makeChainForm(t, reqDiff, nil)
	respDiff, bodyDiff := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", formDiff, ctDiff)
	if respDiff.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", respDiff.StatusCode, string(bodyDiff))
	}
}

// TestServedChainViewCarriesWorkflowAndState pins that a chain view for a
// workflow chain returns the stored workflow definition and engine state.
func TestServedChainViewCarriesWorkflowAndState(t *testing.T) {
	env := setupTestEnv(t)
	req := chainWireRequest("shop", env.repoID, env.headSHA)
	const customJSON = `{"name":"custom-view","inputs":{"plans":"required"},"start":"a","steps":{"a":{"run":"builder","on":{"done":"done"}}}}`
	req.Workflow = json.RawMessage(customJSON)
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	respGet, bodyGet := doSigned(t, env.ts, env.kp, "GET", "/v1/chains/shop", nil, "")
	requireStatus(t, respGet, bodyGet, http.StatusOK)
	view := decodeChainView(t, bodyGet)
	if len(view.Workflow) == 0 {
		t.Fatal("view.Workflow is empty")
	}
	if len(view.State) == 0 {
		t.Fatal("view.State is empty")
	}
	def, err := workflow.Parse(view.Workflow)
	if err != nil {
		t.Fatalf("parse view.Workflow: %v", err)
	}
	if def.Name != "custom-view" {
		t.Errorf("workflow name = %q, want custom-view", def.Name)
	}
}

// TestWhoAmIAdvertisesWorkflow pins that the server advertises the workflow
// feature token so clients know custom workflow creates are supported.
func TestWhoAmIAdvertisesWorkflow(t *testing.T) {
	s, _ := newTestServer(t, 0)
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), "", time.Now()); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedRequest(t, kp, "GET", "/v1/whoami", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var who remote.WhoAmI
	if err := json.NewDecoder(rec.Body).Decode(&who); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !slices.Contains(who.Features, remote.FeatureWorkflow) {
		t.Errorf("Features = %v, want %q", who.Features, remote.FeatureWorkflow)
	}
}
