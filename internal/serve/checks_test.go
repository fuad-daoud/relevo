package serve

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// postCheck asks the server to run one check on a served binding and returns the
// status and the run's view.
func postCheck(t *testing.T, env *testEnv, name string, req remote.CreateCheckRequest) (int, remote.CheckView) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := doSigned(t, env.ts, env.kp, http.MethodPost, "/v1/bindings/"+name+"/checks", body, "application/json")
	var view remote.CheckView
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatalf("decode check view: %v", err)
		}
	}
	return resp.StatusCode, view
}

// getCheck reads one run back and returns the status and the run's view.
func getCheck(t *testing.T, env *testEnv, name, id string) (int, remote.CheckView) {
	t.Helper()
	resp, raw := doSigned(t, env.ts, env.kp, http.MethodGet, "/v1/bindings/"+name+"/checks/"+id, nil, "")
	var view remote.CheckView
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatalf("decode check view: %v", err)
		}
	}
	return resp.StatusCode, view
}

// TestServedCheckRouteEndToEnd is the routes' whole round trip over HTTP: a
// POST starts the run, a daemon tick settles it, and a GET reads the result
// back. It pins the two statuses a POST answers with, since a client has to be
// able to tell a run it started from one it already had.
func TestServedCheckRouteEndToEnd(t *testing.T) {
	env := setupTestEnv(t)
	_, worktree := seedServedBinding(t, env, "api", store.ServeFacts{})
	ctx := t.Context()

	status, view := postCheck(t, env, "api", remote.CreateCheckRequest{ID: "c1", Command: "make check", Step: "verify"})
	if status != http.StatusCreated {
		t.Fatalf("POST status = %d, want %d", status, http.StatusCreated)
	}
	if view.ID != "c1" || view.Step != "verify" || view.Result != "" {
		t.Fatalf("view = %+v, want the fresh run with no result", view)
	}
	if len(env.runner.specs) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(env.runner.specs))
	}
	if got := env.runner.specs[0].Dir; got != worktree {
		t.Errorf("check ran in %q, want the binding's worktree %q", got, worktree)
	}

	// A repeated id answers from the run the binding holds and starts nothing.
	status, again := postCheck(t, env, "api", remote.CreateCheckRequest{ID: "c1", Command: "make check", Step: "verify"})
	if status != http.StatusOK {
		t.Fatalf("repeated POST status = %d, want %d", status, http.StatusOK)
	}
	if again.ID != view.ID || len(env.runner.specs) != 1 {
		t.Fatalf("repeated POST = %+v after %d Starts, want the stored run and no second run", again, len(env.runner.specs))
	}

	// A different id while the first is in flight is refused, on its own code.
	status, _ = postCheck(t, env, "api", remote.CreateCheckRequest{ID: "c2", Command: "go test ./..."})
	if status != http.StatusConflict {
		t.Fatalf("second run status = %d, want %d", status, http.StatusConflict)
	}

	// The daemon tick advances the run, and the client reads the result.
	env.runner.finish(env.runner.handles[0].PID)
	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	status, got := getCheck(t, env, "api", "c1")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if got.Result != "pass" {
		t.Fatalf("Result = %q, want pass", got.Result)
	}
	if got.LogTail == "" {
		t.Error("LogTail is empty, want the run's log")
	}

	// A run the binding never held is a 404, and a second tick is a stable read.
	if status, _ := getCheck(t, env, "api", "other"); status != http.StatusNotFound {
		t.Errorf("unknown run status = %d, want %d", status, http.StatusNotFound)
	}
	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, stable := getCheck(t, env, "api", "c1"); stable.Result != "pass" {
		t.Errorf("Result = %q after a second tick, want the settled pass", stable.Result)
	}
}

// TestServedCheckRefusesReaderAndOtherTenant pins the two bindings a check
// route must never reach: one of another tenant's, which the guard answers as
// not-found rather than forbidden, and a reader of the caller's own, which has
// no check to run. Both are refused on the POST; the other tenant's binding is
// unreachable on the GET and the gate route too.
func TestServedCheckRefusesReaderAndOtherTenant(t *testing.T) {
	env := setupTestEnv(t)
	seedServedBinding(t, env, "reader", store.ServeFacts{})
	makeReader(t, env, "reader")

	t.Run("a reader has no check", func(t *testing.T) {
		status, _ := postCheck(t, env, "reader", remote.CreateCheckRequest{ID: "c1", Command: "make check"})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		if len(env.runner.specs) != 0 {
			t.Fatalf("Start calls = %d, want none", len(env.runner.specs))
		}
	})

	t.Run("another tenant's binding is not found", func(t *testing.T) {
		other := addOwner(t, env, "bob")
		otherRT := testRuntime(t, env.srv, other.id)
		saveForeignBinding(t, otherRT, "secret", string(other.id), strings.Repeat("b", 64))

		assertRoutesRefuseBinding(t, env, "secret")

		// The binding is untouched: the guard refused before anything loaded.
		stored, err := otherRT.Store.Load("secret")
		if err != nil {
			t.Fatal(err)
		}
		if stored.CheckRun != nil {
			t.Errorf("CheckRun = %+v on another tenant's binding, want none", stored.CheckRun)
		}
	})

	// A binding that sits in the caller's own store but whose owner column
	// names somebody else is the case the ownership check exists for: the store
	// a name is read from already scopes tenants apart, so this is the one
	// disagreement the guard has to catch on its own.
	t.Run("a binding owned by somebody else is not found", func(t *testing.T) {
		other := addOwner(t, env, "bob")
		saveForeignBinding(t, env.runtime(t), "stray", string(other.id), strings.Repeat("c", 64))

		assertRoutesRefuseBinding(t, env, "stray")
	})
}

// makeReader turns an existing served binding into a reader.
func makeReader(t *testing.T, env *testEnv, name string) {
	t.Helper()
	rt := env.runtime(t)
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	b.Shape = store.ShapeReader
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
}

// saveForeignBinding writes a served binding owned by owner into rt's store.
func saveForeignBinding(t *testing.T, rt relevo.Runtime, name, owner, repoID string) {
	t.Helper()
	worktree := rt.Store.WorktreePath(name)
	if err := rt.Store.Save(store.Binding{
		Name:     name,
		Owner:    owner,
		CWD:      worktree,
		Worktree: worktree,
		State:    store.StateDone,
		Round:    1,
		Serve:    &store.ServeFacts{RepoID: repoID},
	}); err != nil {
		t.Fatal(err)
	}
}

// assertRoutesRefuseBinding asks all three check and gate routes about a
// binding the caller may not reach, and requires each to answer not-found and
// to start nothing. Both ways of naming another tenant's binding are held to
// it, so the routes' guard is proved once instead of per case.
func assertRoutesRefuseBinding(t *testing.T, env *testEnv, name string) {
	t.Helper()
	before := len(env.runner.specs)

	if status, _ := postCheck(t, env, name, remote.CreateCheckRequest{ID: "c1", Command: "make check"}); status != http.StatusNotFound {
		t.Errorf("POST %s/checks = %d, want %d", name, status, http.StatusNotFound)
	}
	if status, _ := getCheck(t, env, name, "c1"); status != http.StatusNotFound {
		t.Errorf("GET %s/checks/c1 = %d, want %d", name, status, http.StatusNotFound)
	}

	gate := "make check"
	body, err := json.Marshal(remote.SetGateRequest{Gate: &gate})
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := doSigned(t, env.ts, env.kp, http.MethodPost, "/v1/bindings/"+name+"/gate", body, "application/json")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST %s/gate = %d, want %d", name, resp.StatusCode, http.StatusNotFound)
	}
	if len(env.runner.specs) != before {
		t.Errorf("Start calls = %d, want the refusals to start nothing", len(env.runner.specs))
	}
}

// whoami asks the server what it is and returns the decoded answer.
func whoami(t *testing.T, env *testEnv) remote.WhoAmI {
	t.Helper()
	resp, raw := doSigned(t, env.ts, env.kp, http.MethodGet, "/v1/whoami", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami status = %d, want 200: %s", resp.StatusCode, raw)
	}
	var who remote.WhoAmI
	if err := json.Unmarshal(raw, &who); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	return who
}

// TestWhoAmIAdvertisesCheck pins that the server a client asks about check runs
// says so. A client reads the token before it sends a check, so a server that
// stayed quiet would have it post into a route that is not there.
func TestWhoAmIAdvertisesCheck(t *testing.T) {
	env := setupTestEnv(t)
	who := whoami(t, env)
	if !slices.Contains(who.Features, remote.FeatureCheck) {
		t.Errorf("Features = %v, want %q", who.Features, remote.FeatureCheck)
	}
}

// TestOldClientIgnoresCheckFeature pins that advertising check costs a client
// that has never heard of it nothing: the feature list is a set of unknown
// tokens to an old binary, and every field it does read decodes unchanged. The
// old shape below is the WhoAmI as it stood before check existed.
func TestOldClientIgnoresCheckFeature(t *testing.T) {
	env := setupTestEnv(t)
	resp, raw := doSigned(t, env.ts, env.kp, http.MethodGet, "/v1/whoami", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami status = %d, want 200", resp.StatusCode)
	}

	var old struct {
		ID            string   `json:"id"`
		Label         string   `json:"label"`
		ServerVersion int      `json:"server_version"`
		Transports    []string `json:"transports"`
		Features      []string `json:"features"`
		MaxTier       string   `json:"max_tier"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("an old client must decode whoami unchanged: %v", err)
	}
	if old.ID != string(env.id) || old.Label != "alice" {
		t.Errorf("old client read id %q label %q, want %q and alice", old.ID, old.Label, env.id)
	}
	if !slices.Contains(old.Features, remote.FeatureCheck) {
		t.Errorf("Features = %v, want the new token to reach the old client untouched", old.Features)
	}
	if old.ServerVersion != remote.Version || old.MaxTier == "" || len(old.Transports) == 0 {
		t.Errorf("old client read %+v, want the fields it already knew filled as before", old)
	}
}

// TestServedCheckRefusesAnOversizedRequest pins the bound the POST body and
// the command are read under: a body past the cap is refused whole rather than
// decoded as far as it goes.
func TestServedCheckRefusesAnOversizedRequest(t *testing.T) {
	env := setupTestEnv(t)
	seedServedBinding(t, env, "api", store.ServeFacts{})

	body := []byte(`{"id":"c1","command":"` + strings.Repeat("x", maxCheckBodyBytes) + `"}`)
	resp, _ := doSigned(t, env.ts, env.kp, http.MethodPost, "/v1/bindings/api/checks", body, "application/json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if len(env.runner.specs) != 0 {
		t.Errorf("Start calls = %d, want none", len(env.runner.specs))
	}
}

// TestServedSetGateRouteWritesTheBinding pins the gate route end to end: the
// command and repair budget it was sent are what the binding then carries, and
// the view it answers with says so.
func TestServedSetGateRouteWritesTheBinding(t *testing.T) {
	env := setupTestEnv(t)
	seedServedBinding(t, env, "api", store.ServeFacts{})

	gate, regate := "make check", 2
	body, err := json.Marshal(remote.SetGateRequest{Gate: &gate, Regate: &regate})
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := doSigned(t, env.ts, env.kp, http.MethodPost, "/v1/bindings/api/gate", body, "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, http.StatusOK, raw)
	}
	var view remote.BindingView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}

	stored, err := env.runtime(t).Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Gate != gate || stored.Regate != regate {
		t.Fatalf("stored Gate = %q, Regate = %d; want %q, %d", stored.Gate, stored.Regate, gate, regate)
	}
	if view.Name != "api" {
		t.Errorf("view.Name = %q, want the binding the gate was set on", view.Name)
	}
}
