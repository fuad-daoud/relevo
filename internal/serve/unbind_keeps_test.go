package serve

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestUnbindDirtyWorktreeAnswers409(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	_, wt := seedActiveWithNoRound(t, env, rt, "dirty")
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/dirty/unbind", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("unbind status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	var eb remote.ErrorBody
	if err := json.Unmarshal(body, &eb); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if eb.Code != remote.CodeWorktreeKept {
		t.Errorf("code = %q, want %q", eb.Code, remote.CodeWorktreeKept)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree missing: %v", err)
	}
	if _, err := os.Stat(rt.Store.Dir("dirty")); err != nil {
		t.Errorf("binding directory missing: %v", err)
	}
}

func TestUnbindKeptAnswerCarriesTheReasonOnly(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	_, wt := seedActiveWithNoRound(t, env, rt, "dirty")
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/dirty/unbind", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("unbind status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	var eb remote.ErrorBody
	if err := json.Unmarshal(body, &eb); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	t.Logf("message: %s", eb.Message)
	if !strings.Contains(eb.Message, "uncommitted changes") {
		t.Errorf("message %q lacks the reason", eb.Message)
	}
	if strings.Contains(eb.Message, "--force") || strings.Contains(eb.Message, "serve unbind") {
		t.Errorf("message %q carries a verb hint", eb.Message)
	}
}

func TestUnbindForceArchivesDirtyWorktree(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	_, wt := seedActiveWithNoRound(t, env, rt, "dirty")
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/dirty/unbind?force=1", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forced unbind status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if _, err := rt.Store.Load("dirty"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding still loads after forced unbind: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("dirty worktree must survive the forced unbind: %v", err)
	}
}
