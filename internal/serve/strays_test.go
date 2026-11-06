package serve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
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
