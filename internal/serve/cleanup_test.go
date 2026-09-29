package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

func requireRefGone(t *testing.T, ctx context.Context, gc *git.Client, repo, ref string) {
	t.Helper()
	if _, ok, err := gc.RefSHA(ctx, repo, ref); err != nil || ok {
		t.Errorf("%s = (ok %v, err %v); want it gone", ref, ok, err)
	}
}

func requireRefPresent(t *testing.T, ctx context.Context, gc *git.Client, repo, ref string) {
	t.Helper()
	if _, ok, err := gc.RefSHA(ctx, repo, ref); err != nil || !ok {
		t.Errorf("%s = (ok %v, err %v); want it present", ref, ok, err)
	}
}

func requireArchived(t *testing.T, rt relevo.Runtime, name string) {
	t.Helper()
	archived, err := rt.Store.ListArchived()
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	for _, rec := range archived {
		if rec.Binding.Name == name {
			return
		}
	}
	t.Errorf("binding %s has no archived record", name)
}

// TestSettled pins the settled predicate's boundary: DONE with Serve facts and
// no live process, consult or gate, and either acked and past ackedGrace or past
// settledGrace.
func TestSettled(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	acked := now.Add(-ackedGrace - time.Minute)
	aged := now.Add(-settledGrace - time.Hour)

	cases := []struct {
		name string
		b    store.Binding
		want bool
	}{
		{
			name: "acked but just done",
			b:    store.Binding{State: store.StateDone, Serve: &store.ServeFacts{ClosedRound: 2, AckedRound: 2}, UpdatedAt: fresh},
			want: false,
		},
		{
			name: "acked and past the acked grace",
			b:    store.Binding{State: store.StateDone, Serve: &store.ServeFacts{ClosedRound: 2, AckedRound: 2}, UpdatedAt: acked},
			want: true,
		},
		{
			name: "not acked but older than the grace",
			b:    store.Binding{State: store.StateDone, Serve: &store.ServeFacts{ClosedRound: 2, AckedRound: 1}, UpdatedAt: aged},
			want: true,
		},
		{
			name: "not acked and fresh",
			b:    store.Binding{State: store.StateDone, Serve: &store.ServeFacts{ClosedRound: 2, AckedRound: 1}, UpdatedAt: fresh},
			want: false,
		},
		{
			name: "builder running",
			b:    store.Binding{State: store.StateDone, Builder: store.Endpoint{PID: 4242}, Serve: &store.ServeFacts{ClosedRound: 1, AckedRound: 1}, UpdatedAt: fresh},
			want: false,
		},
		{
			name: "consult running",
			b:    store.Binding{State: store.StateDone, Consults: []store.Consult{{State: store.ConsultRunning}}, Serve: &store.ServeFacts{ClosedRound: 1, AckedRound: 1}, UpdatedAt: fresh},
			want: false,
		},
		{
			name: "gate running",
			b:    store.Binding{State: store.StateDone, GateRun: &store.GateRun{PID: 7}, Serve: &store.ServeFacts{ClosedRound: 1, AckedRound: 1}, UpdatedAt: fresh},
			want: false,
		},
		{
			name: "not done",
			b:    store.Binding{State: store.StateActive, Serve: &store.ServeFacts{ClosedRound: 1, AckedRound: 1}, UpdatedAt: aged},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := settled(tc.b, now); got != tc.want {
				t.Fatalf("settled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCollectSettledOnTick: the acked binding is collected (gone from List,
// worktree removed, branch and refs deleted, record archived) while the fresh
// un-acked one is untouched.
func TestCollectSettledOnTick(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	ackedBare, ackedWT := seedServedBinding(t, env, "api", store.ServeFacts{
		RepoID:      env.repoID,
		ClosedRound: 1,
		AckedRound:  1,
	})
	freshBare, freshWT := seedServedBinding(t, env, "beta", store.ServeFacts{
		RepoID:      env.repoID,
		ClosedRound: 1,
		AckedRound:  0,
	})

	// Both bindings were just saved; run the tick two hours on, past the acked
	// grace (an hour) and well short of the un-acked one (seven days).
	base := env.srv.cfg.Now
	env.srv.cfg.Now = func() time.Time { return base().Add(2 * time.Hour) }

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	rt := env.runtime(t)
	bindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := map[string]bool{}
	for _, b := range bindings {
		names[b.Name] = true
	}
	if names["api"] {
		t.Error("acked binding api still in List after the tick")
	}
	if !names["beta"] {
		t.Error("fresh un-acked binding beta was collected; want it untouched")
	}

	if _, err := os.Stat(ackedWT); !os.IsNotExist(err) {
		t.Errorf("acked worktree still present (stat err = %v); want it removed", err)
	}
	requireRefGone(t, ctx, env.gitClient, ackedBare, "refs/heads/relevo/api")
	refs, err := env.gitClient.ListRefs(ctx, ackedBare, "refs/relevo/api/")
	if err != nil {
		t.Fatalf("list acked refs: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("acked refs/relevo/api/* = %v; want none", refs)
	}
	requireArchived(t, rt, "api")

	// The fresh binding keeps everything it had.
	if _, err := os.Stat(freshWT); err != nil {
		t.Errorf("fresh worktree stat = %v; want it present", err)
	}
	requireRefPresent(t, ctx, env.gitClient, freshBare, "refs/heads/relevo/beta")
	freshRefs, err := env.gitClient.ListRefs(ctx, freshBare, "refs/relevo/beta/")
	if err != nil {
		t.Fatalf("list fresh refs: %v", err)
	}
	if len(freshRefs) != 2 {
		t.Errorf("fresh refs/relevo/beta/* = %v; want the two seeded refs", freshRefs)
	}
}

func TestUnusedRepos(t *testing.T) {
	tests := []struct {
		name  string
		repos []string
		live  []store.Binding
		want  []string
	}{
		{
			name:  "repo named by live binding Serve.BareRepo is kept",
			repos: []string{"/root/repos/owner/repo1.git"},
			live: []store.Binding{
				{Name: "b1", Serve: &store.ServeFacts{BareRepo: "/root/repos/owner/repo1.git"}},
			},
			want: nil,
		},
		{
			name:  "repo named by nobody is returned",
			repos: []string{"/root/repos/owner/unused.git"},
			live: []store.Binding{
				{Name: "b1", Serve: &store.ServeFacts{BareRepo: "/root/repos/owner/repo1.git"}},
			},
			want: []string{"/root/repos/owner/unused.git"},
		},
		{
			name:  "Serve == nil with Repo set counts as reference",
			repos: []string{"/root/repos/owner/local.git"},
			live: []store.Binding{
				{Name: "b2", Repo: "/root/repos/owner/local.git"},
			},
			want: nil,
		},
		{
			name:  "path cleaning e.g. trailing slash still matches",
			repos: []string{"/root/repos/owner/repo1.git/"},
			live: []store.Binding{
				{Name: "b1", Serve: &store.ServeFacts{BareRepo: "/root/repos/owner/repo1.git"}},
			},
			want: nil,
		},
		{
			name: "input order is kept",
			repos: []string{
				"/root/repos/owner/unused-c.git",
				"/root/repos/owner/used-b.git",
				"/root/repos/owner/unused-a.git",
			},
			live: []store.Binding{
				{Name: "b1", Serve: &store.ServeFacts{BareRepo: "/root/repos/owner/used-b.git"}},
			},
			want: []string{
				"/root/repos/owner/unused-c.git",
				"/root/repos/owner/unused-a.git",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := unusedRepos(tc.repos, tc.live)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("unusedRepos() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTickPrunesAnUnusedRepo(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	ownerDir, ok := env.id.Dir()
	if !ok {
		t.Fatal("client id has no owner dir")
	}

	// Owner O has repo A with a live binding, and repo B with only an archived
	// binding.
	env.repoID = "repo-a"
	bareA, _ := seedServedBinding(t, env, "live-binding", store.ServeFacts{RepoID: env.repoID})

	env.repoID = "repo-b"
	bareB, _ := seedServedBinding(t, env, "archived-binding", store.ServeFacts{RepoID: env.repoID})
	rt := env.runtime(t)
	if _, err := relevo.Unbind(ctx, rt, "archived-binding", true); err != nil {
		t.Fatalf("unbind archived-binding: %v", err)
	}

	// Unrelated file repos/<O>/notes.txt and non-owner dir repos/not-an-owner/x.git.
	ownerRepoDir := filepath.Join(env.srv.cfg.Root, "repos", ownerDir)
	notesPath := filepath.Join(ownerRepoDir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("some notes"), 0644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	nonOwnerDir := filepath.Join(env.srv.cfg.Root, "repos", "not-an-owner", "x.git")
	if err := os.MkdirAll(nonOwnerDir, 0755); err != nil {
		t.Fatalf("mkdir non-owner dir: %v", err)
	}

	// Another owner with an unreadable store (its record row cannot be
	// decoded): on List error pruneUnusedRepos must skip the owner and
	// preserve its repo.
	failOwnerHex := strings.Repeat("b", 64)
	failBareRepo := filepath.Join(env.srv.cfg.Root, "repos", failOwnerHex, "unpruned.git")
	if err := env.gitClient.InitBare(ctx, failBareRepo); err != nil {
		t.Fatalf("init bare failOwner: %v", err)
	}
	failOwner, ok := remote.IDFromDir(failOwnerHex)
	if !ok {
		t.Fatalf("failOwner hex %q has no id", failOwnerHex)
	}
	if _, err := env.srv.cfg.DB.RecordPut(db.Record{Owner: string(failOwner), Name: "broken", Round: 1, JSON: "{"}); err != nil {
		t.Fatalf("RecordPut failOwner: %v", err)
	}

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if _, err := os.Stat(bareA); err != nil {
		t.Errorf("repo A stat err = %v, want it to exist", err)
	}
	if _, err := os.Stat(bareB); !os.IsNotExist(err) {
		t.Errorf("repo B stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(notesPath); err != nil {
		t.Errorf("notes.txt stat err = %v, want it to exist", err)
	}
	if _, err := os.Stat(nonOwnerDir); err != nil {
		t.Errorf("not-an-owner/x.git stat err = %v, want it to exist", err)
	}
	if _, err := os.Stat(failBareRepo); err != nil {
		t.Errorf("failOwner bare repo stat err = %v, want it preserved on List error", err)
	}
}

func TestTickPruneRemovesTheEmptyOwnerDir(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	ownerDir, ok := env.id.Dir()
	if !ok {
		t.Fatal("client id has no owner dir")
	}

	bare, _ := seedServedBinding(t, env, "only-binding", store.ServeFacts{RepoID: env.repoID})
	rt := env.runtime(t)
	if _, err := relevo.Unbind(ctx, rt, "only-binding", true); err != nil {
		t.Fatalf("unbind: %v", err)
	}

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Errorf("bare repo stat err = %v, want not exist", err)
	}
	ownerRepoDir := filepath.Join(env.srv.cfg.Root, "repos", ownerDir)
	if _, err := os.Stat(ownerRepoDir); !os.IsNotExist(err) {
		t.Errorf("owner repo dir %s stat err = %v, want not exist", ownerRepoDir, err)
	}
}

func TestCreateAfterPruneRecreatesTheRepo(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bare, _ := seedServedBinding(t, env, "first-binding", store.ServeFacts{RepoID: env.repoID})
	rt := env.runtime(t)
	if _, err := relevo.Unbind(ctx, rt, "first-binding", true); err != nil {
		t.Fatalf("unbind: %v", err)
	}

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Fatalf("bare repo %s still exists after tick", bare)
	}

	createBody, err := json.Marshal(remote.CreateBindingRequest{
		Name:       "recreated",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
	})
	if err != nil {
		t.Fatalf("marshal create body: %v", err)
	}
	req := signedRequest(t, env.kp, "POST", "/v1/bindings", createBody)
	rec := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare repo %s stat err = %v; want it recreated", bare, err)
	}

	b, err := rt.Store.Load("recreated")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.Serve == nil || b.Serve.BareRepo != bare {
		t.Fatalf("binding BareRepo = %v, want %s", b.Serve, bare)
	}
}
