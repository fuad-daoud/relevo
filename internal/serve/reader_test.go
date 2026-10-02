package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// withReviewerCandidate makes the shared haiku candidate serve the reviewer
// role too, so a served reader round can resolve a builder to run.
func withReviewerCandidate(t *testing.T) func(*Config) {
	t.Helper()
	return func(cfg *Config) {
		body := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder","reviewer"]}]`
		path := filepath.Join(t.TempDir(), "candidates.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		set, err := candidate.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Candidates = set
	}
}

// startReaderRound creates a reader binding over the wire and starts round 1.
func startReaderRound(t *testing.T, env *testEnv, name string) remote.BindingView {
	t.Helper()
	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       name,
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "reviewer",
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	requireStatus(t, resp, body, http.StatusCreated)
	if view := decodeView(t, body); view.Shape != store.ShapeReader {
		t.Fatalf("created Shape = %q, want %q", view.Shape, store.ShapeReader)
	}

	outRef := "refs/relevo/" + name + "/out"
	if err := env.gitClient.UpdateRef(context.Background(), env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatal(err)
	}
	formBytes, ct := makeRoundForm(t, 1, "review the code", snapshotRef(t, env, env.clientDir, outRef))
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/"+name+"/rounds", formBytes, ct)
	requireCreated(t, resp, body, "reader round")
	return decodeView(t, body)
}

// TestReaderRoundRunsInScratchWorktree pins A5 R7's server half: a served
// reader round's process runs in the round's scratch worktree, never the
// binding's own tree, and the scratch is gone once the round closes.
func TestReaderRoundRunsInScratchWorktree(t *testing.T) {
	env := setupTestEnv(t, withReviewerCandidate(t))
	startReaderRound(t, env, "review")
	rt := env.runtime(t)

	b, err := rt.Store.Load("review")
	if err != nil {
		t.Fatal(err)
	}
	scratch := rt.Store.ScratchWorktreePath("review", 1)
	specs := startedSpecs(env)
	if len(specs) == 0 {
		t.Fatal("reader round spawned no process")
	}
	if specs[0].Dir != scratch {
		t.Fatalf("spawn Dir = %q, want scratch %q", specs[0].Dir, scratch)
	}
	if specs[0].Dir == b.CWD {
		t.Fatalf("spawn Dir = the binding's own tree %q, want the scratch %q", b.CWD, scratch)
	}

	// A reader closes on its output file, not the writer's report path:
	// give the round one so the close has a report to queue.
	out := rt.Store.OutputPath("review", 1, "reviewer", "findings")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("the findings\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	finishRound(t, env, rt, "review", 1)

	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch %s still exists after close (stat err = %v)", scratch, err)
	}
	reloaded, err := rt.Store.Load("review")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Serve == nil || reloaded.Serve.ClosedRound != 1 {
		t.Fatalf("Serve = %+v, want ClosedRound 1", reloaded.Serve)
	}
}
