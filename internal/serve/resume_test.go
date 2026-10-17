package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestResumeAdoptsMatchingWorktree(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	_, wt := seedServedBinding(t, env, "resume-wt", store.ServeFacts{RepoID: env.repoID})
	uncommitted := filepath.Join(wt, "draft.txt")
	if err := os.WriteFile(uncommitted, []byte("draft text\n"), 0o644); err != nil {
		t.Fatalf("write uncommitted file: %v", err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/resume-wt/resume", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	b, err := rt.Store.Load("resume-wt")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.State != store.StateActive {
		t.Errorf("state = %s, want active", b.State)
	}

	got, err := os.ReadFile(uncommitted)
	if err != nil || string(got) != "draft text\n" {
		t.Errorf("uncommitted file = %q, %v; want draft text intact", got, err)
	}
}

func TestResumeRefusesPlainDirectory(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()
	rt := env.runtime(t)

	bare, wt := seedServedBinding(t, env, "resume-plain", store.ServeFacts{RepoID: env.repoID})
	if err := env.gitClient.RemoveWorktree(ctx, bare, wt, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir plain dir: %v", err)
	}
	marker := filepath.Join(wt, "plain.txt")
	if err := os.WriteFile(marker, []byte("plain content\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/resume-plain/resume", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("resume status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Errorf("code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}
	for _, want := range []string{wt, bare, "relevo/resume-plain", "move the directory aside", "relevo unbind"} {
		if !strings.Contains(errBody.Message, want) {
			t.Errorf("error message %q does not contain %q", errBody.Message, want)
		}
	}

	b, err := rt.Store.Load("resume-plain")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.State != store.StateDone {
		t.Errorf("state = %s, want done (refusal leaves state unchanged)", b.State)
	}

	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "plain content\n" {
		t.Errorf("marker file = %q, %v; want directory untouched", got, err)
	}
}

func TestResumeRefusesWorktreeOnAnotherBranch(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	bare, wt := seedServedBinding(t, env, "resume-branch", store.ServeFacts{RepoID: env.repoID})
	runGit(t, env.clientDir, "push", bare, env.headSHA+":refs/heads/other-branch")
	runGit(t, wt, "checkout", "other-branch")

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/resume-branch/resume", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("resume status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Errorf("code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}

	b, err := rt.Store.Load("resume-branch")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.State != store.StateDone {
		t.Errorf("state = %s, want done", b.State)
	}
}

func TestResumeRefusesWorktreeOfAnotherBareRepo(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()
	rt := env.runtime(t)

	bare, wt := seedServedBinding(t, env, "resume-other-repo", store.ServeFacts{RepoID: env.repoID})
	if err := env.gitClient.RemoveWorktree(ctx, bare, wt, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	otherBare := filepath.Join(t.TempDir(), "other.git")
	if err := env.gitClient.InitBare(ctx, otherBare); err != nil {
		t.Fatalf("init other bare: %v", err)
	}
	branch := "relevo/resume-other-repo"
	runGit(t, env.clientDir, "push", otherBare, env.headSHA+":refs/heads/"+branch)
	if err := env.gitClient.CheckoutWorktree(ctx, otherBare, wt, branch); err != nil {
		t.Fatalf("checkout worktree from other repo: %v", err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/resume-other-repo/resume", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("resume status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Errorf("code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}

	b, err := rt.Store.Load("resume-other-repo")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.State != store.StateDone {
		t.Errorf("state = %s, want done", b.State)
	}
}

func TestResumeRestoresMissingWorktree(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()
	rt := env.runtime(t)

	bare, wt := seedServedBinding(t, env, "resume-missing", store.ServeFacts{RepoID: env.repoID})
	if err := env.gitClient.RemoveWorktree(ctx, bare, wt, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	_ = os.RemoveAll(wt)

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/resume-missing/resume", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	b, err := rt.Store.Load("resume-missing")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.State != store.StateActive {
		t.Errorf("state = %s, want active", b.State)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("restored worktree stat = %v; want it present", err)
	}
}

func TestResumeRefusesSubdirectoryOfWorktree(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	_, wt := seedServedBinding(t, env, "resume-sub", store.ServeFacts{RepoID: env.repoID})
	sub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	b, err := rt.Store.Load("resume-sub")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	b.Worktree = sub
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save binding: %v", err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/resume-sub/resume", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("resume status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Errorf("code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}
}
