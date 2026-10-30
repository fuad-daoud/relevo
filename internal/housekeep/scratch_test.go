package housekeep

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeGit is the slice of the git CLI the scratch tests drive: the HEAD and
// snapshot reads and the detached-worktree add, materialize and remove.
type fakeGit struct {
	headCommitID  string
	headCommitErr error

	snapshotTreeID  string
	snapshotTreeErr error

	addDetachedWorktreeCalls []addDetachedWorktreeCall
	addDetachedWorktreeErr   error

	materializeCalls []materializeCall
	materializeErr   error

	removeWorktreeCalls []removeWorktreeCall
	removeWorktreeErr   error
}

type addDetachedWorktreeCall struct{ Dir, Path, Commit string }

type materializeCall struct{ dir, tree string }

type removeWorktreeCall struct {
	Dir   string
	Path  string
	Force bool
}

func (f *fakeGit) HeadCommit(_ context.Context, dir string) (string, error) {
	if f.headCommitErr != nil {
		return "", f.headCommitErr
	}
	return f.headCommitID, nil
}

func (f *fakeGit) SnapshotTree(_ context.Context, dir string) (string, error) {
	if f.snapshotTreeErr != nil {
		return "", f.snapshotTreeErr
	}
	return f.snapshotTreeID, nil
}

func (f *fakeGit) AddDetachedWorktree(_ context.Context, dir, path, commit string) error {
	f.addDetachedWorktreeCalls = append(f.addDetachedWorktreeCalls, addDetachedWorktreeCall{
		Dir: dir, Path: path, Commit: commit,
	})
	return f.addDetachedWorktreeErr
}

func (f *fakeGit) MaterializeTree(_ context.Context, dir, tree string) error {
	f.materializeCalls = append(f.materializeCalls, materializeCall{dir, tree})
	return f.materializeErr
}

func (f *fakeGit) RemoveWorktree(_ context.Context, dir, path string, force bool) error {
	f.removeWorktreeCalls = append(f.removeWorktreeCalls, removeWorktreeCall{
		Dir: dir, Path: path, Force: force,
	})
	return f.removeWorktreeErr
}

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
	deps := Deps{Store: store.New(t.TempDir()), Git: og}
	b := store.Binding{Name: "api", CWD: "/repo"}

	s, err := CreateScratch(ctx, deps, b, 7)
	if err != nil {
		t.Fatalf("CreateScratch: %v", err)
	}

	want := []string{"head", "snapshot", "add", "materialize"}
	if !reflect.DeepEqual(og.calls, want) {
		t.Errorf("call order = %v, want %v", og.calls, want)
	}

	path := deps.Store.ScratchWorktreePath("api", 7)
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
	deps := Deps{Store: store.New(t.TempDir()), Git: fg}
	b := store.Binding{Name: "api", CWD: "/repo"}

	if _, err := CreateScratch(ctx, deps, b, 1); err == nil {
		t.Fatal("CreateScratch succeeded, want the materialize error")
	} else {
		if !errors.Is(err, ErrScratch) {
			t.Errorf("error %v does not wrap ErrScratch", err)
		}
		if !strings.Contains(err.Error(), "materialize") {
			t.Errorf("error %q does not name the materialize step", err)
		}
	}

	path := deps.Store.ScratchWorktreePath("api", 1)
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
	deps := Deps{Store: store.New(t.TempDir()), Git: og}
	b := store.Binding{Name: "api", CWD: "/repo"}

	path := deps.Store.ScratchWorktreePath("api", 1)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := CreateScratch(ctx, deps, b, 1); err != nil {
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

// scratchRunGit runs git in dir with the test identity pinned, so the
// real-git tests below do not depend on the developer's own config.
func scratchRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// scratchSeedRepo builds a real git repository holding one committed file,
// so a scratch worktree has something to carry over.
func scratchSeedRepo(t *testing.T, initial string) string {
	t.Helper()
	repo := t.TempDir()
	scratchRunGit(t, repo, "init")
	scratchRunGit(t, repo, "config", "user.name", "Test")
	scratchRunGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	scratchRunGit(t, repo, "add", "a.txt")
	scratchRunGit(t, repo, "commit", "-m", "first")
	return repo
}

// TestScratchRealGit drives the lifecycle against real git: a dirty edit
// survives into the scratch tree, RemoveScratch takes the tree away (twice),
// and a sweep over three leftovers removes exactly the two whose round is
// closed.
func TestScratchRealGit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	repo := scratchSeedRepo(t, "one\n")
	// The dirty edit CreateScratch must carry over.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.New(t.TempDir())
	deps := Deps{Store: st, Git: git.NewClient("git", 0, 0)}
	b := store.Binding{Name: "api", CWD: repo}

	s, err := CreateScratch(ctx, deps, b, 1)
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

	if err := RemoveScratch(ctx, deps, b, 1); err != nil {
		t.Fatalf("RemoveScratch: %v", err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Errorf("scratch still present after RemoveScratch: %v", err)
	}
	if err := RemoveScratch(ctx, deps, b, 1); err != nil {
		t.Errorf("second RemoveScratch = %v, want nil", err)
	}

	scratchSweepClosedRounds(t, ctx, deps, repo)
}

// scratchSweepClosedRounds leaves three leftover scratches and sweeps with
// round 4 kept: api-002 has a binding record, so the sweep takes the
// RemoveWorktree branch, while web's have none and go through their .git
// file. Exactly the two whose round is closed are removed.
func scratchSweepClosedRounds(t *testing.T, ctx context.Context, deps Deps, repo string) {
	t.Helper()
	st := deps.Store
	for _, lo := range []struct {
		binding string
		round   int
	}{{"api", 2}, {"web", 3}, {"web", 4}} {
		if _, err := CreateScratch(ctx, deps, store.Binding{Name: lo.binding, CWD: repo}, lo.round); err != nil {
			t.Fatalf("CreateScratch(%s, %d): %v", lo.binding, lo.round, err)
		}
	}
	if err := st.Save(store.Binding{Name: "api", CWD: repo}); err != nil {
		t.Fatalf("save binding api: %v", err)
	}

	removed, err := SweepScratch(ctx, deps, func(binding string, round int) bool {
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

	repo := scratchSeedRepo(t, "one\n")

	st := store.New(t.TempDir())
	deps := Deps{Store: st, Git: git.NewClient("git", 0, 0)}
	b := store.Binding{Name: "api", CWD: repo}

	// The earlier state: a.txt holds "two".
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, err := deps.Git.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	tree, err := deps.Git.SnapshotTree(ctx, repo)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}

	// The tree moves on; the snapshot taken above must win.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := CreateScratchFrom(ctx, deps, b, 1, head, tree)
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

	if err := RemoveScratch(ctx, deps, b, 1); err != nil {
		t.Fatalf("RemoveScratch: %v", err)
	}
}

// TestSweepScratchDoesNotFollowSymlinkedScratchDir pins that a .scratch entry
// swapped for a symlink out of .worktrees is refused by the sweep, by the
// per-entry removal and by the .git read, so a directory outside the binding is
// never removed.
func TestSweepScratchDoesNotFollowSymlinkedScratchDir(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	deps := Deps{Store: st, Git: &fakeGit{}}

	if err := os.MkdirAll(st.WorktreeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := t.TempDir()
	work := filepath.Join(victim, "x-001")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: /repo/.git/worktrees/x-001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, st.ScratchWorktreeDir()); err != nil {
		t.Fatal(err)
	}

	if _, err := SweepScratch(ctx, deps, nil); err == nil {
		t.Errorf("SweepScratch followed a symlinked .scratch, want a refusal")
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("the outside worktree was removed by the sweep: %v", err)
	}

	root, err := st.WorktreeRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	rel := filepath.Join(".scratch", "x-001")
	if err := removeScratchEntry(ctx, deps, root, "x", rel, st.ScratchWorktreePath("x", 1)); err == nil {
		t.Errorf("removeScratchEntry followed an escaping .scratch, want a refusal")
	}
	if _, err := os.Stat(work); err != nil {
		t.Errorf("the outside worktree was removed by removeScratchEntry: %v", err)
	}

	if err := removeScratchPath(st, st.ScratchWorktreePath("x", 1)); err == nil {
		t.Errorf("removeScratchPath followed an escaping .scratch, want a refusal")
	}
	if _, err := os.Stat(work); err != nil {
		t.Errorf("the outside worktree was removed by removeScratchPath: %v", err)
	}
}

// TestRemoveScratchSkipsWhenTheSourceAndTheScratchAreGone pins the shared-tree
// release: a chain done removes the builder's worktree first, and every reader
// cut from that tree then cleans a scratch whose source no longer exists. Both
// gone means the scratch is already removed -- no git call, no cleanup warning.
func TestRemoveScratchSkipsWhenTheSourceAndTheScratchAreGone(t *testing.T) {
	st := store.New(t.TempDir())
	fg := &fakeGit{}
	deps := Deps{Store: st, Git: fg}
	b := store.Binding{
		Name: "reader-bind", CWD: filepath.Join(t.TempDir(), "gone"), Shape: store.ShapeReader, Round: 1,
	}

	if err := RemoveScratch(context.Background(), deps, b, 1); err != nil {
		t.Fatalf("RemoveScratch = %v, want nil when the source tree and the scratch are both gone", err)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree calls = %d, want 0", len(fg.removeWorktreeCalls))
	}
}

// TestRemoveScratchRemovesARealScratchEvenWhenTheSourceIsGone keeps the guard
// narrow: a scratch path that still exists is removed even when its source
// tree is gone.
func TestRemoveScratchRemovesARealScratchEvenWhenTheSourceIsGone(t *testing.T) {
	st := store.New(t.TempDir())
	fg := &fakeGit{}
	deps := Deps{Store: st, Git: fg}
	scratch := st.ScratchWorktreePath("reader-bind", 1)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	b := store.Binding{
		Name: "reader-bind", CWD: filepath.Join(t.TempDir(), "gone"), Shape: store.ShapeReader, Round: 1,
	}

	if err := RemoveScratch(context.Background(), deps, b, 1); err != nil {
		t.Fatalf("RemoveScratch: %v, want nil", err)
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Errorf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
	}
}

// TestRoundTreeKeepsTheRootUntouched pins the root case: no subdirectory, the
// tree itself, unchanged. A join that turned an empty prefix into "./" would
// leave a different string.
func TestRoundTreeKeepsTheRootUntouched(t *testing.T) {
	t.Parallel()

	deps := Deps{Store: store.New(t.TempDir())}
	b := store.Binding{Name: "a", CWD: "/tree"}
	if got := RoundTree(deps, b); got != "/tree" {
		t.Errorf("RoundTree = %q, want the tree itself", got)
	}
}

// TestReaderRoundRunsInTheScratchSubdirectory pins the reader side of the
// round's directory: the throwaway worktree plus the same subdirectory. A
// join left on the writer side only would put the scratch round at the
// worktree's root.
func TestReaderRoundRunsInTheScratchSubdirectory(t *testing.T) {
	t.Parallel()

	deps := Deps{Store: store.New(t.TempDir())}
	b := store.Binding{Name: "rdr", CWD: "/tree", Shape: store.ShapeReader, Round: 3, Subdir: "src/api"}
	want := filepath.Join(deps.Store.ScratchWorktreePath("rdr", 3), "src", "api")
	if got := RoundTree(deps, b); got != want {
		t.Errorf("RoundTree = %q, want %q", got, want)
	}
}
