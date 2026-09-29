package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// orderedGit wraps fakeGit to record the order of the calls CreateScratch
// makes: fakeGit's per-method counters cannot tell one sequence from another.
type orderedGit struct {
	*fakeGit
	calls []string
}

func (g *orderedGit) HeadCommit(ctx context.Context, dir string) (string, error) {
	g.calls = append(g.calls, "head")
	return g.fakeGit.HeadCommit(ctx, dir)
}

func (g *orderedGit) SnapshotTree(ctx context.Context, dir string) (string, error) {
	g.calls = append(g.calls, "snapshot")
	return g.fakeGit.SnapshotTree(ctx, dir)
}

func (g *orderedGit) AddDetachedWorktree(ctx context.Context, dir, path, commit string) error {
	g.calls = append(g.calls, "add")
	return g.fakeGit.AddDetachedWorktree(ctx, dir, path, commit)
}

func (g *orderedGit) MaterializeTree(ctx context.Context, dir, tree string) error {
	g.calls = append(g.calls, "materialize")
	return g.fakeGit.MaterializeTree(ctx, dir, tree)
}

func (g *orderedGit) RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	g.calls = append(g.calls, "remove")
	return g.fakeGit.RemoveWorktree(ctx, dir, path, force)
}

func TestCreateScratchOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fg := &fakeGit{headCommitID: "head1", snapshotTreeID: "tree1"}
	og := &orderedGit{fakeGit: fg}
	rt := Runtime{Git: og, Store: store.New(t.TempDir())}
	b := store.Binding{Name: "api", CWD: "/repo"}

	s, err := CreateScratch(ctx, rt, b, 7)
	if err != nil {
		t.Fatalf("CreateScratch: %v", err)
	}

	want := []string{"head", "snapshot", "add", "materialize"}
	if !reflect.DeepEqual(og.calls, want) {
		t.Errorf("call order = %v, want %v", og.calls, want)
	}

	path := rt.Store.ScratchWorktreePath("api", 7)
	if len(fg.addDetachedWorktreeCalls) != 1 {
		t.Fatalf("AddDetachedWorktree calls = %d, want 1", len(fg.addDetachedWorktreeCalls))
	}
	if got := fg.addDetachedWorktreeCalls[0]; got.Dir != "/repo" || got.Path != path || got.Commit != "head1" {
		t.Errorf("AddDetachedWorktree = %+v, want Dir=/repo Path=%s Commit=head1", got, path)
	}
	if len(fg.materializeCalls) != 1 {
		t.Fatalf("MaterializeTree calls = %d, want 1", len(fg.materializeCalls))
	}
	if got := fg.materializeCalls[0]; got.dir != path || got.tree != "tree1" {
		t.Errorf("MaterializeTree = %+v, want dir=%s tree=tree1", got, path)
	}
	if s.Path != path || s.Head != "head1" || s.Tree != "tree1" {
		t.Errorf("Scratch = %+v, want Path=%s Head=head1 Tree=tree1", s, path)
	}
}

func TestCreateScratchMaterializeFailureRemoves(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fg := &fakeGit{headCommitID: "head1", snapshotTreeID: "tree1", materializeErr: errors.New("boom")}
	rt := Runtime{Git: fg, Store: store.New(t.TempDir())}
	b := store.Binding{Name: "api", CWD: "/repo"}

	if _, err := CreateScratch(ctx, rt, b, 1); err == nil {
		t.Fatal("CreateScratch succeeded, want the materialize error")
	} else {
		if !errors.Is(err, ErrScratch) {
			t.Errorf("error %v does not wrap ErrScratch", err)
		}
		if !strings.Contains(err.Error(), "materialize") {
			t.Errorf("error %q does not name the materialize step", err)
		}
	}

	path := rt.Store.ScratchWorktreePath("api", 1)
	found := false
	for _, c := range fg.removeWorktreeCalls {
		if c.Dir == "/repo" && c.Path == path && c.Force {
			found = true
		}
	}
	if !found {
		t.Errorf("RemoveWorktree(%s, force=true) not recorded: %+v", path, fg.removeWorktreeCalls)
	}
}

func TestCreateScratchRemovesLeftover(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fg := &fakeGit{headCommitID: "head1", snapshotTreeID: "tree1"}
	og := &orderedGit{fakeGit: fg}
	rt := Runtime{Git: og, Store: store.New(t.TempDir())}
	b := store.Binding{Name: "api", CWD: "/repo"}

	path := rt.Store.ScratchWorktreePath("api", 1)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := CreateScratch(ctx, rt, b, 1); err != nil {
		t.Fatalf("CreateScratch: %v", err)
	}

	want := []string{"remove", "head", "snapshot", "add", "materialize"}
	if !reflect.DeepEqual(og.calls, want) {
		t.Errorf("call order = %v, want %v", og.calls, want)
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
	}
	if got := fg.removeWorktreeCalls[0]; got.Dir != "/repo" || got.Path != path || !got.Force {
		t.Errorf("RemoveWorktree = %+v, want Dir=/repo Path=%s Force=true", got, path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("leftover directory still present after CreateScratch: %v", err)
	}
}

func TestScratchPathShape(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	got := st.ScratchWorktreePath("api", 7)
	want := filepath.Join(".worktrees", ".scratch", "api-007")
	if !strings.HasSuffix(got, want) {
		t.Errorf("ScratchWorktreePath(api, 7) = %q, want it to end with %q", got, want)
	}
}

// TestCreateScratchRealGit drives the lifecycle against real git: a dirty edit
// survives into the scratch tree, RemoveScratch takes the tree away (twice),
// and a sweep over three leftovers removes exactly the two whose round is
// closed.
func TestScratchRealGit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "first")
	// The dirty edit CreateScratch must carry over.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.New(t.TempDir())
	rt := Runtime{Git: git.NewClient("git", 0, 0), Store: st}
	b := store.Binding{Name: "api", CWD: repo}

	s, err := CreateScratch(ctx, rt, b, 1)
	if err != nil {
		t.Fatalf("CreateScratch: %v", err)
	}
	if want := st.ScratchWorktreePath("api", 1); s.Path != want {
		t.Errorf("Scratch.Path = %q, want %q", s.Path, want)
	}
	if s.Head == "" || s.Tree == "" {
		t.Errorf("Scratch = %+v, want a head and a tree", s)
	}
	got, err := os.ReadFile(filepath.Join(s.Path, "a.txt"))
	if err != nil {
		t.Fatalf("read scratch a.txt: %v", err)
	}
	if string(got) != "two\n" {
		t.Errorf("scratch a.txt = %q, want the dirty edit %q", got, "two\n")
	}

	if err := RemoveScratch(ctx, rt, b, 1); err != nil {
		t.Fatalf("RemoveScratch: %v", err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Errorf("scratch still present after RemoveScratch: %v", err)
	}
	if err := RemoveScratch(ctx, rt, b, 1); err != nil {
		t.Errorf("second RemoveScratch = %v, want nil", err)
	}

	// Three leftovers. api-002 has a binding record, so the sweep takes the
	// RemoveWorktree branch; web's have none and go through their .git file.
	for _, lo := range []struct {
		binding string
		round   int
	}{{"api", 2}, {"web", 3}, {"web", 4}} {
		if _, err := CreateScratch(ctx, rt, store.Binding{Name: lo.binding, CWD: repo}, lo.round); err != nil {
			t.Fatalf("CreateScratch(%s, %d): %v", lo.binding, lo.round, err)
		}
	}
	if err := st.Save(store.Binding{Name: "api", CWD: repo}); err != nil {
		t.Fatalf("save binding api: %v", err)
	}

	removed, err := SweepScratch(ctx, rt, func(binding string, round int) bool {
		return binding == "web" && round == 4
	})
	if err != nil {
		t.Fatalf("SweepScratch: %v", err)
	}

	want := []string{st.ScratchWorktreePath("api", 2), st.ScratchWorktreePath("web", 3)}
	if !reflect.DeepEqual(removed, want) {
		t.Errorf("SweepScratch removed = %v, want %v", removed, want)
	}
	for _, path := range want {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still present after the sweep: %v", path, err)
		}
	}
	if _, err := os.Stat(st.ScratchWorktreePath("web", 4)); err != nil {
		t.Errorf("kept scratch %s is gone: %v", st.ScratchWorktreePath("web", 4), err)
	}
}

// TestCreateScratchFromUsesTheGivenTree snapshots an earlier working state,
// moves the tree on, then creates from the earlier snapshot: the scratch holds
// the earlier state, not what is on disk at creation time.
func TestCreateScratchFromUsesTheGivenTree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "first")

	st := store.New(t.TempDir())
	rt := Runtime{Git: git.NewClient("git", 0, 0), Store: st}
	b := store.Binding{Name: "api", CWD: repo}

	// The earlier state: a.txt holds "two".
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, err := rt.Git.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	tree, err := rt.Git.SnapshotTree(ctx, repo)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}

	// The tree moves on; the snapshot taken above must win.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := CreateScratchFrom(ctx, rt, b, 1, head, tree)
	if err != nil {
		t.Fatalf("CreateScratchFrom: %v", err)
	}
	if s.Head != head || s.Tree != tree {
		t.Errorf("Scratch = %+v, want Head=%s Tree=%s", s, head, tree)
	}
	got, err := os.ReadFile(filepath.Join(s.Path, "a.txt"))
	if err != nil {
		t.Fatalf("read scratch a.txt: %v", err)
	}
	if string(got) != "two\n" {
		t.Errorf("scratch a.txt = %q, want the earlier state %q", got, "two\n")
	}

	if err := RemoveScratch(ctx, rt, b, 1); err != nil {
		t.Fatalf("RemoveScratch: %v", err)
	}
}
