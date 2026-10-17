package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedActiveWithNoRound seeds a served binding and puts it back into ACTIVE
// with no round in flight and no builder, so a done is not refused for a reason
// other than the worktree's dirtiness.
func seedActiveWithNoRound(t *testing.T, env *testEnv, rt relevo.Runtime, name string) (string, string) {
	t.Helper()
	_, wt := seedServedBinding(t, env, name, store.ServeFacts{RepoID: env.repoID})
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	b.State = store.StateActive
	b.RoundStartedAt = time.Time{}
	b.Builder = store.Endpoint{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	return name, wt
}

func TestDoneKeepsDirtyWorktree(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()
	rt := env.runtime(t)

	_, dirtyWT := seedActiveWithNoRound(t, env, rt, "dirty")

	if err := os.WriteFile(filepath.Join(dirtyWT, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/dirty/done", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("done status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	var view remote.BindingView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal view: %v", err)
	}
	if view.WorktreeKept != dirtyWT {
		t.Errorf("view.WorktreeKept = %q, want %q", view.WorktreeKept, dirtyWT)
	}
	if view.KeptReason != "uncommitted changes" {
		t.Errorf("view.KeptReason = %q, want %q", view.KeptReason, "uncommitted changes")
	}
	if _, err := os.Stat(dirtyWT); err != nil {
		t.Errorf("dirty worktree missing from disk: %v", err)
	}

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if _, err := os.Stat(rt.Store.Dir("dirty")); err != nil {
		t.Errorf("binding directory missing after tick: %v", err)
	}

	_, cleanWT := seedActiveWithNoRound(t, env, rt, "clean")

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/clean/done", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("done status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	var cleanView remote.BindingView
	if err := json.Unmarshal(body, &cleanView); err != nil {
		t.Fatalf("unmarshal clean view: %v", err)
	}
	if cleanView.WorktreeKept != "" {
		t.Errorf("cleanView.WorktreeKept = %q, want empty", cleanView.WorktreeKept)
	}
	if _, err := os.Stat(cleanWT); !os.IsNotExist(err) {
		t.Errorf("clean worktree still present after done (stat err = %v); want removed", err)
	}
}
