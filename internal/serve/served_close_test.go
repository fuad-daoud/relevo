package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// stubGit is the git seam CloseServedRound needs: RefSHA, Dirty,
// SnapshotTree, CommitTree and UpdateRef, with error injection per method for
// the failure paths. Every other method reaches the embedded nil interface
// and panics, so a test that touches more of the seam fails loudly.
type stubGit struct {
	relevo.Git
	calls         int
	refSHA        map[string]string
	refSHAErr     error
	dirty         bool
	dirtyErr      error
	snapshotTree  string
	snapshotErr   error
	commitSHA     string
	commitErr     error
	updateErr     error
	snapshotCalls int
	commitCalls   int
	updateCalls   int
}

func (g *stubGit) RefSHA(ctx context.Context, dir, ref string) (string, bool, error) {
	g.calls++
	if g.refSHAErr != nil {
		return "", false, g.refSHAErr
	}
	if g.refSHA != nil {
		sha, ok := g.refSHA[ref]
		return sha, ok, nil
	}
	return "fakerefsha", true, nil
}

func (g *stubGit) Dirty(ctx context.Context, dir string) (bool, error) {
	g.calls++
	if g.dirtyErr != nil {
		return false, g.dirtyErr
	}
	return g.dirty, nil
}

func (g *stubGit) SnapshotTree(ctx context.Context, dir string) (string, error) {
	g.calls++
	g.snapshotCalls++
	if g.snapshotErr != nil {
		return "", g.snapshotErr
	}
	return g.snapshotTree, nil
}

func (g *stubGit) CommitTree(ctx context.Context, dir, tree, parent, message string) (string, error) {
	g.calls++
	g.commitCalls++
	if g.commitErr != nil {
		return "", g.commitErr
	}
	if g.commitSHA != "" {
		return g.commitSHA, nil
	}
	return "fakecommit", nil
}

func (g *stubGit) UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA string) error {
	g.calls++
	g.updateCalls++
	return g.updateErr
}

func TestCloseServedRoundClean(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)

	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relevo/api")

	branchHead := strings.TrimSpace(runGit(t, bare, "rev-parse", "refs/heads/relevo/api"))

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relevo/api")

	rt := relevo.Runtime{Git: client}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relevo/api",
		Worktree: wt,
		Round:    2, // queueReport already advanced Round from 1 to 2
		Serve: &store.ServeFacts{
			BareRepo: bare,
		},
	}

	res := CloseServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 1 {
		t.Fatalf("ClosedRound: got %d, want 1", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != branchHead {
		t.Fatalf("ResultCommit: got %q, want %q", res.Serve.ResultCommit, branchHead)
	}
	if res.Serve.DirtyCommit != "" {
		t.Fatalf("DirtyCommit on clean worktree: got %q, want empty", res.Serve.DirtyCommit)
	}

	// Side ref must not exist
	sideSHA, ok, err := client.RefSHA(ctx, bare, "refs/relevo/api/round-1")
	if err != nil || ok {
		t.Fatalf("side ref exists on clean worktree: sha=%q, ok=%v, err=%v", sideSHA, ok, err)
	}

	// Branch must be unchanged
	headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relevo/api")
	if err != nil || !ok || headAfter != branchHead {
		t.Fatalf("branch changed: got %q, want %q", headAfter, branchHead)
	}
}

func TestCloseServedRoundDirty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)

	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relevo/api")

	branchHead := strings.TrimSpace(runGit(t, bare, "rev-parse", "refs/heads/relevo/api"))

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relevo/api")

	// Make worktree dirty with an untracked file
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("dirty work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := relevo.Runtime{Git: client}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relevo/api",
		Worktree: wt,
		Round:    2,
		Serve: &store.ServeFacts{
			BareRepo: bare,
		},
	}

	res := CloseServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 1 {
		t.Fatalf("ClosedRound: got %d, want 1", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != branchHead {
		t.Fatalf("ResultCommit: got %q, want %q", res.Serve.ResultCommit, branchHead)
	}
	if res.Serve.DirtyCommit == "" {
		t.Fatal("DirtyCommit is empty on dirty worktree")
	}

	// Side ref must exist in bare repo and equal DirtyCommit
	sideSHA, ok, err := client.RefSHA(ctx, bare, "refs/relevo/api/round-1")
	if err != nil || !ok {
		t.Fatalf("side ref missing: ok=%v, err=%v", ok, err)
	}
	if sideSHA != res.Serve.DirtyCommit {
		t.Fatalf("side ref %q != DirtyCommit %q", sideSHA, res.Serve.DirtyCommit)
	}

	// Side ref parent must be branch head
	parent := strings.TrimSpace(runGit(t, bare, "rev-parse", sideSHA+"^"))
	if parent != branchHead {
		t.Fatalf("side ref parent %q != branchHead %q", parent, branchHead)
	}

	// Branch must be unchanged
	headAfter, ok, err := client.RefSHA(ctx, bare, "refs/heads/relevo/api")
	if err != nil || !ok || headAfter != branchHead {
		t.Fatalf("branch changed: got %q, want %q", headAfter, branchHead)
	}
}

func TestCloseServedRoundGitFailureKeepsFacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fGit := &stubGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "commit123",
		},
		dirtyErr: errors.New("dirty failure"),
	}

	rt := relevo.Runtime{Git: fGit}
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relevo/api",
		Worktree: "/tmp/fake-wt",
		Round:    2,
		Serve: &store.ServeFacts{
			BareRepo:     "/tmp/fake-bare",
			ClosedRound:  0,
			ResultCommit: "old-head",
			DirtyCommit:  "",
		},
	}

	// Must not panic, and must leave facts unchanged
	res := CloseServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 0 {
		t.Fatalf("ClosedRound changed on git error: got %d, want 0", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != "old-head" {
		t.Fatalf("ResultCommit changed on git error: got %q, want old-head", res.Serve.ResultCommit)
	}
	if res.Serve.DirtyCommit != "" {
		t.Fatalf("DirtyCommit changed on git error: got %q, want empty", res.Serve.DirtyCommit)
	}
}

// TestCloseServedRoundRecordsABranchlessReader pins the branchless close: a
// reader with no branch records ClosedRound with no git call and no git facts,
// while the writer path is unchanged.
func TestCloseServedRoundRecordsABranchlessReader(t *testing.T) {
	t.Parallel()

	fg := &stubGit{}
	rt := relevo.Runtime{Git: fg}
	reader := store.Binding{
		Name: "shop-rev", Owner: "alice", CWD: "/build/shop", Round: 2,
		Shape: store.ShapeReader, Serve: &store.ServeFacts{BareRepo: "/bare/shop.git"},
	}
	got := CloseServedRound(context.Background(), rt, reader)
	if got.Serve.ClosedRound != 1 {
		t.Errorf("reader ClosedRound = %d, want 1", got.Serve.ClosedRound)
	}
	if got.Serve.ResultCommit != "" || got.Serve.DirtyCommit != "" {
		t.Errorf("reader result/dirty = %q/%q, want both empty", got.Serve.ResultCommit, got.Serve.DirtyCommit)
	}
	if fg.calls != 0 {
		t.Errorf("git calls = %d, want none for a branchless reader", fg.calls)
	}

	writer := store.Binding{
		Name: "shop", Owner: "alice", CWD: "/build/shop", Worktree: "/build/shop",
		Branch: "relevo/shop", Round: 2, Shape: store.ShapeWriter,
		Serve: &store.ServeFacts{BareRepo: "/bare/shop.git"},
	}
	gotWriter := CloseServedRound(context.Background(), rt, writer)
	if gotWriter.Serve.ClosedRound != 1 || gotWriter.Serve.ResultCommit != "fakerefsha" {
		t.Errorf("writer close = closed %d result %q, want 1/fakerefsha", gotWriter.Serve.ClosedRound, gotWriter.Serve.ResultCommit)
	}
	if fg.calls == 0 {
		t.Error("the writer's close must have made a git call")
	}
}

// TestCloseServedRoundRecordsCost pins the cost hook: closing a round with a
// measured burst writes one sample to the cost file, and closing the same
// round again leaves exactly one sample behind.
func TestCloseServedRoundRecordsCost(t *testing.T) {
	t.Parallel()

	env := setupTestEnv(t)
	ctx := context.Background()
	repoID := strings.Repeat("a", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	burst, base := int64(2<<30), int64(1<<30)

	rt := env.runtime(t)
	b := store.Binding{
		Name:     "api",
		Owner:    string(env.id),
		CWD:      t.TempDir(),
		Branch:   "relevo/api",
		Worktree: "/fake/wt",
		Round:    2,
		Serve:    &store.ServeFacts{BareRepo: "/fake/bare", RepoID: repoID},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := rt.Store.AppendLog("api", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		TS: at, Rusage: &store.Rusage{BaseBytes: &base, BurstBytes: &burst},
	}); err != nil {
		t.Fatalf("append log: %v", err)
	}

	costRoot := t.TempDir()
	costRT := relevo.Runtime{Git: &stubGit{}, Store: rt.Store, CostRoot: costRoot}

	res := CloseServedRound(ctx, costRT, b)
	if res.Serve.ClosedRound != 1 || res.Serve.ResultCommit != "fakerefsha" {
		t.Fatalf("close = closed %d result %q, want 1/fakerefsha",
			res.Serve.ClosedRound, res.Serve.ResultCommit)
	}
	readSample := func() costFile {
		t.Helper()
		cf, err := loadCostFile(costFilePath(costRoot, repoID, "builder"))
		if err != nil {
			t.Fatalf("load cost file: %v", err)
		}
		return cf
	}
	cf := readSample()
	if cf.LastRound != 1 || len(cf.Samples) != 1 {
		t.Fatalf("cost file = last %d samples %d, want 1 and 1", cf.LastRound, len(cf.Samples))
	}
	if cf.Samples[0] != (costSample{Base: base, Burst: burst, At: at}) {
		t.Fatalf("sample = %+v, want base %d burst %d at %v", cf.Samples[0], base, burst, at)
	}

	again := CloseServedRound(ctx, costRT, res)
	if again.Serve.ClosedRound != 1 {
		t.Fatalf("second close = closed %d, want 1", again.Serve.ClosedRound)
	}
	if cf := readSample(); cf.LastRound != 1 || len(cf.Samples) != 1 {
		t.Fatalf("after retry: last %d samples %d, want 1 and 1", cf.LastRound, len(cf.Samples))
	}
}

// TestCloseServedRoundBranchlessReaderRecordsCost pins the branchless cost
// path: a reader close with no branch still records the round's burst, since
// the hook runs on the round facts rather than the git facts.
func TestCloseServedRoundBranchlessReaderRecordsCost(t *testing.T) {
	t.Parallel()

	env := setupTestEnv(t)
	ctx := context.Background()
	repoID := strings.Repeat("a", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	burst, base := int64(2<<30), int64(1<<30)

	rt := env.runtime(t)
	b := store.Binding{
		Name:  "api",
		Owner: string(env.id),
		CWD:   "/build/shop",
		Round: 2,
		Shape: store.ShapeReader,
		Serve: &store.ServeFacts{BareRepo: "/bare/shop.git", RepoID: repoID},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := rt.Store.AppendLog("api", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		TS: at, Rusage: &store.Rusage{BaseBytes: &base, BurstBytes: &burst},
	}); err != nil {
		t.Fatalf("append log: %v", err)
	}

	costRoot := t.TempDir()
	costRT := relevo.Runtime{Git: &stubGit{}, Store: rt.Store, CostRoot: costRoot}

	res := CloseServedRound(ctx, costRT, b)
	if res.Serve.ClosedRound != 1 {
		t.Fatalf("ClosedRound: got %d, want 1", res.Serve.ClosedRound)
	}
	cf, err := loadCostFile(costFilePath(costRoot, repoID, "builder"))
	if err != nil {
		t.Fatalf("load cost file: %v", err)
	}
	if cf.LastRound != 1 || len(cf.Samples) != 1 {
		t.Fatalf("cost file = last %d samples %d, want 1 and 1", cf.LastRound, len(cf.Samples))
	}
	if cf.Samples[0] != (costSample{Base: base, Burst: burst, At: at}) {
		t.Fatalf("sample = %+v, want base %d burst %d at %v", cf.Samples[0], base, burst, at)
	}
}

// TestCloseServedRoundGitFailureRecordsNoCost pins the dirty-check failure's
// exclusion: a close that cannot stamp its facts records no cost, since the
// hook runs only after the facts land.
func TestCloseServedRoundGitFailureRecordsNoCost(t *testing.T) {
	t.Parallel()

	env := setupTestEnv(t)
	ctx := context.Background()
	repoID := strings.Repeat("a", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	burst, base := int64(2<<30), int64(1<<30)

	rt := env.runtime(t)
	b := store.Binding{
		Name:     "api",
		Owner:    string(env.id),
		CWD:      "/build/shop",
		Branch:   "relevo/api",
		Worktree: "/tmp/fake-wt",
		Round:    2,
		Serve: &store.ServeFacts{
			BareRepo:     "/fake/bare",
			RepoID:       repoID,
			ClosedRound:  0,
			ResultCommit: "old-head",
			DirtyCommit:  "",
		},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := rt.Store.AppendLog("api", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		TS: at, Rusage: &store.Rusage{BaseBytes: &base, BurstBytes: &burst},
	}); err != nil {
		t.Fatalf("append log: %v", err)
	}

	costRoot := t.TempDir()
	fGit := &stubGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "commit123",
		},
		dirtyErr: errors.New("dirty failure"),
	}
	costRT := relevo.Runtime{Git: fGit, Store: rt.Store, CostRoot: costRoot}

	res := CloseServedRound(ctx, costRT, b)
	if res.Serve.ClosedRound != 0 {
		t.Fatalf("ClosedRound changed on git error: got %d, want 0", res.Serve.ClosedRound)
	}
	if _, err := os.Stat(costFilePath(costRoot, repoID, "builder")); !os.IsNotExist(err) {
		t.Fatalf("cost file exists after git failure: err=%v", err)
	}
}

// TestCloseServedRoundCostFailureKeepsFacts pins the hook's warn-only rule: a
// corrupt cost file never stops the close from stamping its facts.
func TestCloseServedRoundCostFailureKeepsFacts(t *testing.T) {
	t.Parallel()

	env := setupTestEnv(t)
	ctx := context.Background()
	repoID := strings.Repeat("a", 64)

	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relevo/api")

	branchHead := strings.TrimSpace(runGit(t, bare, "rev-parse", "refs/heads/relevo/api"))

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relevo/api")

	burst := int64(2 << 30)
	rt := env.runtime(t)
	b := store.Binding{
		Name:     "api",
		Owner:    string(env.id),
		CWD:      wt,
		Branch:   "relevo/api",
		Worktree: wt,
		Round:    2,
		Serve: &store.ServeFacts{
			BareRepo: bare,
			RepoID:   repoID,
		},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := rt.Store.AppendLog("api", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		TS: time.Now(), Rusage: &store.Rusage{BurstBytes: &burst},
	}); err != nil {
		t.Fatalf("append log: %v", err)
	}

	costRoot := t.TempDir()
	badPath := costFilePath(costRoot, repoID, "builder")
	if err := os.MkdirAll(filepath.Dir(badPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt.CostRoot = costRoot

	res := CloseServedRound(ctx, rt, b)
	if res.Serve.ClosedRound != 1 {
		t.Fatalf("ClosedRound: got %d, want 1", res.Serve.ClosedRound)
	}
	if res.Serve.ResultCommit != branchHead {
		t.Fatalf("ResultCommit: got %q, want %q", res.Serve.ResultCommit, branchHead)
	}
	if res.Serve.DirtyCommit != "" {
		t.Fatalf("DirtyCommit on clean worktree: got %q, want empty", res.Serve.DirtyCommit)
	}
}

// TestRuntimeAtWiresCloseServedRound pins the served-close hook: every owner
// runtime carries the serve implementation, so a hook nobody fills can never
// reach production silently.
func TestRuntimeAtWiresCloseServedRound(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	trackTmpRoot(t, s, filepath.Join(root, "bindings", "aaa"))
	rt := s.runtimeAt(filepath.Join(root, "bindings", "aaa"))
	if rt.CloseServedRound == nil {
		t.Fatal("CloseServedRound is nil: owner runtimes must serve the close")
	}
	if rt.CostRoot != root {
		t.Fatalf("CostRoot = %q, want %q", rt.CostRoot, root)
	}
}

func TestCloseServedRoundRecordsSameNameFromTwoOwners(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoID := strings.Repeat("e", 64)
	costRoot := t.TempDir()
	burst, base := int64(2<<30), int64(1<<30)
	for _, owner := range []string{"owner-a", "owner-b"} {
		env := setupTestEnv(t)
		rt := env.runtime(t)
		b := store.Binding{
			Name:  "api",
			Owner: owner,
			CWD:   "/build/shop",
			Round: 2,
			Shape: store.ShapeReader,
			Serve: &store.ServeFacts{BareRepo: "/bare/shop.git", RepoID: repoID},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("save %s: %v", owner, err)
		}
		if err := rt.Store.AppendLog("api", store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Rusage: &store.Rusage{BaseBytes: &base, BurstBytes: &burst},
		}); err != nil {
			t.Fatalf("append log %s: %v", owner, err)
		}
		CloseServedRound(ctx, relevo.Runtime{Git: &stubGit{}, Store: rt.Store, CostRoot: costRoot}, b)
	}
	cf, err := loadCostFile(costFilePath(costRoot, repoID, "builder"))
	if err != nil {
		t.Fatalf("load cost file: %v", err)
	}
	if len(cf.Samples) != 2 {
		t.Fatalf("samples = %d, want 2: another owner's binding of the same name closed its own round", len(cf.Samples))
	}
}
