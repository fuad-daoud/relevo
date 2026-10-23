package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// windowSet is claude/opencode builders where claude carries a window around
// the current time: field is "only" (claude usable now) or "avoid" (claude
// excluded now).
func windowSet(t *testing.T, field string) *candidate.Set {
	t.Helper()
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour).Format("15:04")+"Z", now.Add(time.Hour).Format("15:04")+"Z"
	body := fmt.Sprintf(`[
		{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"],
		 "windows":{%q:[{"from":%q,"to":%q}]}},
		{"harness":"opencode","provider":"anthropic","model":"haiku","roles":["builder"]}
	]`, field, from, to)
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

func TestServedSendRepicksOnTheServer(t *testing.T) {
	env := setupTestEnv(t, func(c *Config) {
		c.Candidates = windowSet(t, "only")
		c.Policy = policy.Policy{Order: map[string][]string{"builder": {"claude/anthropic/haiku", "opencode/anthropic/haiku"}}}
	})

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name: "api", RepoID: env.repoID, BaseCommit: env.headSHA, Role: "builder",
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	requireStatus(t, resp, body, http.StatusCreated)
	outRef := "refs/relevo/api/out"
	if err := env.gitClient.UpdateRef(context.Background(), env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	bundleBytes := snapshotRef(t, env, env.clientDir, outRef)

	formBytes, ct := makeRoundForm(t, 1, "# Round 1 Plan", bundleBytes)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)
	if got := decodeView(t, body).Candidate; got != "claude/anthropic/haiku" {
		t.Fatalf("round 1 on %q, want claude/anthropic/haiku inside its window", got)
	}
	rt := env.runtime(t)
	finishRound(t, env, rt, "api", 1)

	// The server's config now excludes claude at this instant.
	env.srv.mu.Lock()
	env.srv.reloaded.Candidates = windowSet(t, "avoid")
	env.srv.mu.Unlock()

	formBytes, ct = makeRoundForm(t, 2, "# Round 2 Plan", bundleBytes)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)
	if got := decodeView(t, body).Candidate; got != "opencode/anthropic/haiku" {
		t.Errorf("round 2 started on %q, want opencode/anthropic/haiku once claude's window excluded it", got)
	}
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.BuilderCandidate != "opencode/anthropic/haiku" {
		t.Errorf("stored candidate = %q, want opencode/anthropic/haiku", b.BuilderCandidate)
	}
}
