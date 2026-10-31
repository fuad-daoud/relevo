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

// TestRuntimeAtWiresCloseServedRound pins the served-close hook: every owner
// runtime carries the serve implementation, so a hook nobody fills can never
// reach production silently.
func TestRuntimeAtWiresCloseServedRound(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt := s.runtimeAt(filepath.Join(root, "bindings", "aaa"))
	if rt.CloseServedRound == nil {
		t.Fatal("CloseServedRound is nil: owner runtimes must serve the close")
	}
}
