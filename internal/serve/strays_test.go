package serve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// strayFixtureRepo commits two files and pushes one branch per name to a bare
// repo, so each worktree below checks out identical content.
func strayFixtureRepo(t *testing.T, gitClient *git.Client, branches ...string) string {
	t.Helper()
	ctx := context.Background()
	src := t.TempDir()
	runGit(t, src, "init")
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "nested.txt"), []byte("nested-data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-m", "init")
	bare := filepath.Join(t.TempDir(), "origin.git")
	if err := gitClient.InitBare(ctx, bare); err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	for _, branch := range branches {
		runGit(t, src, "push", bare, "HEAD:refs/heads/"+branch)
	}
	return bare
}

func checkoutStray(t *testing.T, gitClient *git.Client, bare, path, branch string) {
	t.Helper()
	if err := gitClient.CheckoutWorktree(context.Background(), bare, path, branch); err != nil {
		t.Fatalf("CheckoutWorktree %s: %v", path, err)
	}
}

// TestListOwnerStraysListsOnlyUnownedTrees pins the walk: a live-owned tree
// and a path-owned tree drop out, a tree whose binding is archived lists as a
// stray, and the three unowned trees list with their exact reasons, sizes and
// removal commands, while the scratch and fresh entries do not.
func TestListOwnerStraysListsOnlyUnownedTrees(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	gitClient := git.NewClient("git", 0, 0)
	s := newAdminServer(t, now, func(c *Config) { c.Git = gitClient })
	id := enrol(t, s, "alice")
	rt := ownerRuntime(t, s, id)

	bare := strayFixtureRepo(t, gitClient, "relevo/live", "relevo/gone", "relevo/clean", "relevo/dirty", "relevo/oldname")
	wtDir := rt.Store.WorktreeDir()
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	liveWT := filepath.Join(wtDir, "live")
	goneWT := filepath.Join(wtDir, "gone")
	cleanWT := filepath.Join(wtDir, "clean")
	dirtyWT := filepath.Join(wtDir, "dirty")
	oldnameWT := filepath.Join(wtDir, "oldname")
	checkoutStray(t, gitClient, bare, liveWT, "relevo/live")
	checkoutStray(t, gitClient, bare, goneWT, "relevo/gone")
	checkoutStray(t, gitClient, bare, cleanWT, "relevo/clean")
	checkoutStray(t, gitClient, bare, dirtyWT, "relevo/dirty")
	checkoutStray(t, gitClient, bare, oldnameWT, "relevo/oldname")

	save := func(name, wt string) {
		t.Helper()
		b := store.Binding{
			Name: name, Owner: string(id), CWD: wt, Worktree: wt,
			State: store.StateActive, Round: 1, Builder: store.Endpoint{Kind: "claude"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
	}
	save("live", liveWT)
	save("gone", goneWT)
	if _, err := rt.Store.Archive("gone"); err != nil {
		t.Fatalf("Archive gone: %v", err)
	}
	// The row owns oldname by worktree path alone: the names differ.
	save("renamed", oldnameWT)

	if _, err := rt.Store.Load("gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Load(gone) = %v, want NotFound: the fixture needs an archived row", err)
	}
	archived, err := rt.Store.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if !slices.ContainsFunc(archived, func(a store.ArchivedBinding) bool { return a.Binding.Name == "gone" }) {
		t.Fatal("ListArchived lacks gone: the fixture needs an archived row that owns nothing")
	}

	if err := os.WriteFile(filepath.Join(dirtyWT, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nogit := filepath.Join(wtDir, "nogit")
	if err := os.MkdirAll(nogit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nogit, "data.bin"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(nogit, "extlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "no-such"), filepath.Join(nogit, "dangling")); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(nogit, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	scratch := filepath.Join(wtDir, ".scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "leftover.txt"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(wtDir, "fresh")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh, "fresh.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := now.Add(-time.Hour)
	for _, dir := range []string{liveWT, goneWT, cleanWT, dirtyWT, oldnameWT, nogit, scratch} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(fresh, now, now); err != nil {
		t.Fatal(err)
	}

	got, err := listOwnerStrays(ctx, rt, "alice", now)
	if err != nil {
		t.Fatalf("listOwnerStrays: %v", err)
	}
	var names []string
	byName := map[string]StrayWorktree{}
	for _, st := range got {
		names = append(names, st.Name)
		byName[st.Name] = st
	}
	if !slices.Equal(names, []string{"clean", "dirty", "gone", "nogit"}) {
		t.Fatalf("stray names = %v, want [clean dirty gone nogit]", names)
	}

	gitSize := func(wt string) int64 {
		t.Helper()
		fi, err := os.Stat(filepath.Join(wt, ".git"))
		if err != nil {
			t.Fatalf("stat .git in %s: %v", wt, err)
		}
		return fi.Size()
	}
	// Each worktree holds f.txt (6) and sub/nested.txt (12) plus its own .git
	// file; dirty adds dirty.txt (6) and nogit holds only data.bin (10), the
	// symlinks and the locked dir contributing nothing.
	want := map[string]struct {
		reason string
		size   int64
	}{
		"clean": {reason: "clean", size: 18 + gitSize(cleanWT)},
		"dirty": {reason: "dirty", size: 24 + gitSize(dirtyWT)},
		"gone":  {reason: "clean", size: 18 + gitSize(goneWT)},
		"nogit": {reason: "git does not recognise it", size: 10},
	}
	for name, w := range want {
		st := byName[name]
		if st.Reason != w.reason {
			t.Errorf("%s reason = %q, want %q", name, st.Reason, w.reason)
		}
		if st.Size != w.size {
			t.Errorf("%s size = %d, want %d", name, st.Size, w.size)
		}
		if st.Path != filepath.Join(wtDir, name) {
			t.Errorf("%s path = %q, want %q", name, st.Path, filepath.Join(wtDir, name))
		}
		if want := "relevo serve remove-stray --owner alice " + name; st.Remove != want {
			t.Errorf("%s remove = %q, want %q", name, st.Remove, want)
		}
	}
}

// TestClassifyStrayReadsUnprovableTreesAsUnrecognised pins the branches the
// walk never reaches with a healthy tree: no .git at all, a .git that is not
// a file, and a .git file git itself refuses.
func TestClassifyStrayReadsUnprovableTreesAsUnrecognised(t *testing.T) {
	ctx := context.Background()
	gitClient := git.NewClient("git", 0, 0)
	cases := map[string]func(t *testing.T) string{
		"no git file": func(t *testing.T) string {
			return t.TempDir()
		},
		"git is a directory": func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"git refuses the tree": func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			if got := classifyStray(ctx, gitClient, setup(t)); got != "git does not recognise it" {
				t.Errorf("classify = %q, want %q", got, "git does not recognise it")
			}
		})
	}
}

// TestAdminStatusFillsStraysPerOwner pins the wiring: a stray under an
// owner's worktree directory shows up on that owner's status with the removal
// command for its label.
func TestAdminStatusFillsStraysPerOwner(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	s := newAdminServer(t, now)
	id := enrol(t, s, "alice")
	saveOwnerBinding(t, s, id, "app-a")
	rt := ownerRuntime(t, s, id)
	stray := filepath.Join(rt.Store.WorktreeDir(), "leftover")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "work.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Hour)
	if err := os.Chtimes(stray, old, old); err != nil {
		t.Fatal(err)
	}

	owners, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if len(owners) != 1 {
		t.Fatalf("owners = %d, want 1", len(owners))
	}
	if len(owners[0].Strays) != 1 {
		t.Fatalf("strays = %+v, want the one leftover tree", owners[0].Strays)
	}
	st := owners[0].Strays[0]
	if st.Name != "leftover" || st.Reason != "git does not recognise it" || st.Size != 5 {
		t.Errorf("stray = %+v, want leftover/unrecognised/size 5", st)
	}
	if want := "relevo serve remove-stray --owner alice leftover"; st.Remove != want {
		t.Errorf("remove = %q, want %q", st.Remove, want)
	}
}

// TestListOwnerStraysSkipsWhatRemovalRefuses pins the listing to the removal: a
// file and a symlink never list, since their command could only be refused, and
// RemoveStray still refuses each by name.
func TestListOwnerStraysSkipsWhatRemovalRefuses(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	s := newAdminServer(t, now)
	id := enrol(t, s, "alice")
	rt := ownerRuntime(t, s, id)
	wtDir := rt.Store.WorktreeDir()
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(wtDir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(wtDir, "alink")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}

	got, err := listOwnerStrays(ctx, rt, "alice", now)
	if err != nil {
		t.Fatalf("listOwnerStrays: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("strays = %+v, want none", got)
	}
	for _, name := range []string{"afile", "alink"} {
		if _, err := RemoveStray(ctx, s, "alice", name); !errors.Is(err, ErrStrayNotDir) {
			t.Errorf("RemoveStray(%s) = %v, want ErrStrayNotDir", name, err)
		}
	}
}

// TestListOwnerStraysDropsANameALiveBindingHolds pins ownership by name alone: a
// live binding that records no worktree path still owns the directory of its
// name, the case a path comparison cannot see.
func TestListOwnerStraysDropsANameALiveBindingHolds(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	s := newAdminServer(t, now)
	id := enrol(t, s, "alice")
	saveOwnerBinding(t, s, id, "app-a")
	rt := ownerRuntime(t, s, id)
	b, err := rt.Store.Load("app-a")
	if err != nil {
		t.Fatal(err)
	}
	if b.Worktree != "" {
		t.Fatalf("fixture records worktree %q, want none", b.Worktree)
	}
	dir := filepath.Join(rt.Store.WorktreeDir(), "app-a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	got, err := listOwnerStrays(context.Background(), rt, "alice", now)
	if err != nil {
		t.Fatalf("listOwnerStrays: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("strays = %+v, want none", got)
	}
}

// TestFlatStatusLeavesStraysUnwalked pins the cost rule: the polling fleet read
// runs no git against a stray tree, while the one-shot status read does. The
// wrapper logs every git call, so the second half proves the log can see one.
func TestFlatStatusLeavesStraysUnwalked(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "git.log")
	wrapper := filepath.Join(dir, "git-wrapper")
	script := "#!/bin/sh\necho \"$PWD $*\" >> '" + logPath + "'\nexec git \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newAdminServer(t, now, func(c *Config) { c.Git = git.NewClient(wrapper, 0, 0) })
	id := enrol(t, s, "alice")
	rt := ownerRuntime(t, s, id)
	stray := filepath.Join(rt.Store.WorktreeDir(), "leftover")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Hour)
	if err := os.Chtimes(stray, old, old); err != nil {
		t.Fatal(err)
	}
	gitTouchedStray := func() bool {
		blob, err := os.ReadFile(logPath)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Contains(string(blob), "leftover")
	}

	if _, err := FlatStatus(context.Background(), s); err != nil {
		t.Fatalf("FlatStatus: %v", err)
	}
	if gitTouchedStray() {
		t.Errorf("FlatStatus ran git against the stray tree")
	}

	owners, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if len(owners) != 1 || len(owners[0].Strays) != 1 {
		t.Fatalf("owners = %+v, want one owner with the one stray", owners)
	}
	if !gitTouchedStray() {
		t.Errorf("AdminStatus ran no git against the stray tree, so the log proves nothing")
	}
}

// TestStatusDocumentCarriesStrays pins the document half: an owner with
// strays carries them, and an owner without keeps the key dropped so the
// strays-less document stays byte-identical.
func TestStatusDocumentCarriesStrays(t *testing.T) {
	owners := []OwnerStatus{{
		Owner:  remote.ClientID("SHA256:alice11111111111111111111111111111111111111"),
		Label:  "alice",
		Report: view.Report{},
		Strays: []StrayWorktree{{
			Name: "leftover", Path: "/srv/.worktrees/leftover",
			Reason: "git does not recognise it", Size: 5,
			Remove: "relevo serve remove-stray --owner alice leftover",
		}},
	}}
	blob, err := json.Marshal(StatusDocument(owners, remote.BuildersView{}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	owner0 := doc["owners"].([]any)[0].(map[string]any)
	strays, ok := owner0["strays"].([]any)
	if !ok || len(strays) != 1 {
		t.Fatalf("strays = %v, want the one entry", owner0["strays"])
	}
	entry := strays[0].(map[string]any)
	for key, want := range map[string]any{
		"name": "leftover", "path": "/srv/.worktrees/leftover",
		"reason": "git does not recognise it", "size": float64(5),
		"remove": "relevo serve remove-stray --owner alice leftover",
	} {
		if entry[key] != want {
			t.Errorf("strays[0][%s] = %v, want %v", key, entry[key], want)
		}
	}

	bare, err := json.Marshal(StatusDocument([]OwnerStatus{{
		Owner:  remote.ClientID("SHA256:alice11111111111111111111111111111111111111"),
		Label:  "alice",
		Report: view.Report{},
	}}, remote.BuildersView{}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var bareDoc map[string]any
	if err := json.Unmarshal(bare, &bareDoc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := bareDoc["owners"].([]any)[0].(map[string]any)["strays"]; ok {
		t.Error("strays key present with no strays, want it dropped")
	}
}

// removeStrayFixture builds one owner's worktree directory with an entry per
// removal case: clean and dirty worktrees, a directory git does not
// recognise, a worktree whose binding is archived, a live-owned worktree, a
// worktree a renamed binding owns by path alone, a symlink, a regular file,
// and a fresh directory. Every entry but the fresh one is aged past the
// guard; the symlink keeps its fresh age because only a link's own age is
// readable and the shape check refuses it first anyway.
func removeStrayFixture(t *testing.T, now time.Time) (*Server, relevo.Runtime, string) {
	t.Helper()
	gitClient := git.NewClient("git", 0, 0)
	s := newAdminServer(t, now, func(c *Config) { c.Git = gitClient })
	id := enrol(t, s, "alice")
	rt := ownerRuntime(t, s, id)

	bare := strayFixtureRepo(t, gitClient,
		"relevo/live", "relevo/gone", "relevo/clean", "relevo/dirty", "relevo/oldname")
	wtDir := rt.Store.WorktreeDir()
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	checkout := func(name, branch string) string {
		t.Helper()
		path := filepath.Join(wtDir, name)
		checkoutStray(t, gitClient, bare, path, branch)
		return path
	}
	checkout("live", "relevo/live")
	checkout("gone", "relevo/gone")
	dirtyWT := checkout("dirty", "relevo/dirty")
	oldnameWT := checkout("oldname", "relevo/oldname")
	checkout("clean", "relevo/clean")

	save := func(name, wt string) {
		t.Helper()
		b := store.Binding{
			Name: name, Owner: string(id), CWD: wt, Worktree: wt,
			State: store.StateActive, Round: 1, Builder: store.Endpoint{Kind: "claude"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
	}
	save("live", filepath.Join(wtDir, "live"))
	save("gone", filepath.Join(wtDir, "gone"))
	if _, err := rt.Store.Archive("gone"); err != nil {
		t.Fatalf("Archive gone: %v", err)
	}
	// The row owns oldname by worktree path alone: the names differ.
	save("renamed", oldnameWT)
	// The row owns alias by name alone: its tree lives outside this owner's
	// worktree directory, so only the name check can refuse wtDir/alias.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	save("alias", elsewhere)
	aliasWT := filepath.Join(wtDir, "alias")
	if err := os.MkdirAll(aliasWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aliasWT, "data.bin"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dirtyWT, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nogit := filepath.Join(wtDir, "nogit")
	if err := os.MkdirAll(nogit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nogit, "data.bin"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The link points at a directory so a Stat-instead-of-Lstat mutant reads
	// it as one; the target stays empty so even the mutant deletes nothing of
	// value before the test goes red on the wrong outcome.
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(wtDir, "link")); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(wtDir, "file")
	if err := os.WriteFile(plain, []byte("plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(wtDir, "fresh")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh, "fresh.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := now.Add(-time.Hour)
	for _, dir := range []string{
		filepath.Join(wtDir, "live"), filepath.Join(wtDir, "gone"),
		filepath.Join(wtDir, "clean"), dirtyWT, oldnameWT, nogit, plain, aliasWT, fresh,
	} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(fresh, now, now); err != nil {
		t.Fatal(err)
	}
	return s, rt, bare
}

// TestRemoveStrayTable pins the engine rule: clean, dirty and unrecognised
// strays remove with the listing's facts, and so does a tree whose binding is
// archived, because an archived record owns nothing. Live-owned names and
// paths, invalid names, symlinks, regular files, fresh entries and missing
// entries are refused with their sentinel.
func TestRemoveStrayTable(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	s, rt, _ := removeStrayFixture(t, now)
	wtDir := rt.Store.WorktreeDir()

	t.Run("removes", func(t *testing.T) {
		for _, c := range []struct{ name, reason string }{
			{"clean", strayClean},
			{"dirty", strayDirty},
			{"nogit", strayUnrecognised},
			{"gone", strayClean},
		} {
			got, err := RemoveStray(ctx, s, "alice", c.name)
			if err != nil {
				t.Fatalf("RemoveStray(%s): %v", c.name, err)
			}
			if got.Name != c.name || got.Path != filepath.Join(wtDir, c.name) || got.Reason != c.reason {
				t.Errorf("removed = %+v, want name %s reason %s", got, c.name, c.reason)
			}
			if got.Size <= 0 {
				t.Errorf("removed %s size = %d, want positive", c.name, got.Size)
			}
			if _, err := os.Lstat(filepath.Join(wtDir, c.name)); !os.IsNotExist(err) {
				t.Errorf("entry %s still exists after removal: %v", c.name, err)
			}
		}
	})

	t.Run("refuses", func(t *testing.T) {
		for _, c := range []struct {
			name string
			want error
		}{
			{"live", ErrStrayOwned},
			{"oldname", ErrStrayOwned},
			{"alias", ErrStrayOwned},
			{"", store.ErrInvalidName},
			{".scratch", store.ErrInvalidName},
			{"a/b", store.ErrInvalidName},
			{"..", store.ErrInvalidName},
			{"link", ErrStrayNotDir},
			{"file", ErrStrayNotDir},
			{"fresh", ErrStrayFresh},
			{"missing", store.ErrNotFound},
		} {
			if _, err := RemoveStray(ctx, s, "alice", c.name); !errors.Is(err, c.want) {
				t.Errorf("RemoveStray(%q) = %v, want %v", c.name, err, c.want)
			}
		}
	})

	// clean is already gone above, so its absence cannot mask the order: the
	// owner resolves before any entry is read.
	t.Run("unknown owner", func(t *testing.T) {
		if _, err := RemoveStray(ctx, s, "nosuch", "clean"); !errors.Is(err, ErrNoSuchClient) {
			t.Errorf("RemoveStray unknown owner = %v, want %v", err, ErrNoSuchClient)
		}
	})
}

// TestRemoveStrayClearsGitRegistration pins the removal half the listing never
// sees: a recognised tree goes through git from inside the tree, so the bare
// repository no longer lists it and its branch deletes.
func TestRemoveStrayClearsGitRegistration(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	s, rt, bare := removeStrayFixture(t, now)
	cleanWT := filepath.Join(rt.Store.WorktreeDir(), "clean")

	if _, err := RemoveStray(ctx, s, "alice", "clean"); err != nil {
		t.Fatalf("RemoveStray(clean): %v", err)
	}
	if out := runGit(t, bare, "worktree", "list"); strings.Contains(out, cleanWT) {
		t.Errorf("worktree list still names %s after removal:\n%s", cleanWT, out)
	}
	runGit(t, bare, "branch", "-D", "relevo/clean")
}

// TestRemoveStrayLockedTreeReportsWithoutDeleting pins the failure half: when
// git's removal fails, the error surfaces and the entry is left alone, never
// retried as a plain removal.
func TestRemoveStrayLockedTreeReportsWithoutDeleting(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	s, rt, bare := removeStrayFixture(t, now)
	cleanWT := filepath.Join(rt.Store.WorktreeDir(), "clean")

	runGit(t, bare, "worktree", "lock", cleanWT)
	if _, err := RemoveStray(ctx, s, "alice", "clean"); err == nil {
		t.Fatal("RemoveStray locked clean: got nil, want the git failure")
	}
	if _, err := os.Stat(cleanWT); err != nil {
		t.Errorf("locked entry gone after the failed removal: %v", err)
	}
	if out := runGit(t, bare, "worktree", "list"); !strings.Contains(out, cleanWT) {
		t.Errorf("worktree list no longer names %s, want the failed removal to leave the registration:\n%s", cleanWT, out)
	}
}

// TestStrayRemoveCommandParsesAsRemoveStrayArgv pins the listing's removal
// command against the verb's argv: every Remove the walk emits splits into
// the exact argv the verb parses, so the two cannot drift.
func TestStrayRemoveCommandParsesAsRemoveStrayArgv(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	s := newAdminServer(t, now)
	id := enrol(t, s, "alice")
	rt := ownerRuntime(t, s, id)

	stray := filepath.Join(rt.Store.WorktreeDir(), "leftover")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "work.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stray, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := listOwnerStrays(ctx, rt, "alice", now)
	if err != nil {
		t.Fatalf("listOwnerStrays: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("strays = %+v, want the one leftover tree", got)
	}
	fields := strings.Fields(strings.TrimPrefix(got[0].Remove, "relevo "))
	want := []string{"serve", "remove-stray", "--owner", "alice", "leftover"}
	if !slices.Equal(fields, want) {
		t.Errorf("remove argv = %q, want %q", fields, want)
	}
}

// TestStrayOwnerRefUsesLabelOrFullID pins the --owner value: the label when
// the owner carries one, else the full id, since the short fallback does not
// resolve back to the owner.
func TestStrayOwnerRefUsesLabelOrFullID(t *testing.T) {
	s := newAdminServer(t, time.Now())
	alice := enrol(t, s, "alice")
	if got := strayOwnerRef(s, alice); got != "alice" {
		t.Errorf("labeled owner ref = %q, want alice", got)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	ghost := remote.IDOf(kp.Public)
	if got, want := strayOwnerRef(s, ghost), string(ghost); got != want {
		t.Errorf("unknown owner ref = %q, want %q", got, want)
	}
	kp2, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	plain := remote.IDOf(kp2.Public)
	if _, err := s.clients.Add("", remote.MarshalPublic(kp2.Public, ""), "", time.Now()); err != nil {
		t.Fatalf("Add unlabeled client: %v", err)
	}
	if got := strayOwnerRef(s, plain); got != string(plain) {
		t.Errorf("unlabeled owner ref = %q, want %q", got, string(plain))
	}
}
