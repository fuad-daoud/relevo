package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func seedDone(t *testing.T, rt Runtime, name, cwd string) {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: cwd,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
		Round:      3, State: store.StateDone,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// seedDoneFor is seedDone with an explicit MasterMindID, for the GC scope tests
// (#482).
func seedDoneFor(t *testing.T, rt Runtime, name, cwd, mastermindID string) {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: cwd,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
		Round:      3, State: store.StateDone,
		MasterMindID: mastermindID,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// TestGCClearsOnlyThisMasterMindsBindings pins #482: GC in mastermind-scoped mode
// only clears bindings whose MasterMindID matches, leaving another mastermind's
// bindings and a legacy (MasterMindID-less) binding untouched.
func TestGCClearsOnlyThisMasterMindsBindings(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedDoneFor(t, rt, "a1", "/repo-a1", "pl_aaa")
	seedDoneFor(t, rt, "b1", "/repo-b1", "pl_bbb")
	seedDoneFor(t, rt, "legacy", "/repo-legacy", "")

	got, err := GC(context.Background(), rt, GCOptions{MasterMindID: "pl_aaa", Delete: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a1" || got[0].MasterMindID != "pl_aaa" {
		t.Fatalf("gc result = %+v, want only a1 with MasterMindID pl_aaa", got)
	}

	remaining, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, b := range remaining {
		names[b.Name] = true
	}
	if !names["b1"] || !names["legacy"] {
		t.Errorf("b1 and legacy must survive, remaining = %+v", remaining)
	}
}

// TestGCAllMasterMindsClearsEveryDoneBinding pins #482: AllMasterMinds clears every
// DONE binding regardless of mastermind, including a legacy MasterMindID-less one.
func TestGCAllMasterMindsClearsEveryDoneBinding(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedDoneFor(t, rt, "a1", "/repo-a1", "pl_aaa")
	seedDoneFor(t, rt, "b1", "/repo-b1", "pl_bbb")
	seedDoneFor(t, rt, "legacy", "/repo-legacy", "")

	got, err := GC(context.Background(), rt, GCOptions{AllMasterMinds: true, Delete: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("gc result = %+v, want all three bindings", got)
	}
	want := map[string]string{"a1": "pl_aaa", "b1": "pl_bbb", "legacy": ""}
	for _, r := range got {
		if wantID, ok := want[r.Name]; !ok || r.MasterMindID != wantID {
			t.Errorf("result %+v, want MasterMindID %q for %s", r, wantID, r.Name)
		}
	}
}

// TestGCRefusesWithoutScope pins #482: GC refuses to run with neither a
// MasterMindID nor AllMasterMinds, and refuses when both are set, so no caller can
// get "everything" by leaving the scope empty.
func TestGCRefusesWithoutScope(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedDoneFor(t, rt, "a1", "/repo-a1", "pl_aaa")
	seedDoneFor(t, rt, "b1", "/repo-b1", "pl_bbb")
	seedDoneFor(t, rt, "legacy", "/repo-legacy", "")

	if _, err := GC(context.Background(), rt, GCOptions{}); !errors.Is(err, ErrGCNoScope) {
		t.Fatalf("GC with no scope: err = %v, want ErrGCNoScope", err)
	}
	if _, err := GC(context.Background(), rt, GCOptions{MasterMindID: "pl_aaa", AllMasterMinds: true}); !errors.Is(err, ErrGCNoScope) {
		t.Fatalf("GC with both scopes: err = %v, want ErrGCNoScope", err)
	}

	remaining, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 3 {
		t.Errorf("all three bindings must survive a scope error, got %d", len(remaining))
	}
}

// TestGCMasterMindDryRunListsOnlyThisMasterMind pins #482: a mastermind-scoped dry run
// lists only that mastermind's bindings and changes nothing.
func TestGCMasterMindDryRunListsOnlyThisMasterMind(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedDoneFor(t, rt, "a1", "/repo-a1", "pl_aaa")
	seedDoneFor(t, rt, "b1", "/repo-b1", "pl_bbb")
	seedDoneFor(t, rt, "legacy", "/repo-legacy", "")

	got, err := GC(context.Background(), rt, GCOptions{MasterMindID: "pl_bbb", DryRun: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Name != "b1" {
		t.Fatalf("gc result = %+v, want only b1", got)
	}

	remaining, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 3 {
		t.Errorf("dry run must change nothing, got %d bindings", len(remaining))
	}
}

func TestGCClearsOnlyDoneBindings(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedDone(t, rt, "finished", "/repo-done")

	live := store.Binding{
		Name: "live", CWD: "/repo-live",
		MasterMind: store.Endpoint{PaneID: "w1:p3"},
		Builder:    store.Endpoint{PaneID: "w1:p4"},
		Round:      1, State: store.StateActive,
	}
	if err := rt.Store.Save(live); err != nil {
		t.Fatalf("save live: %v", err)
	}
	// A binding needing a human must survive: removing it would throw away
	// the state that explains why it stopped.
	broken := store.Binding{
		Name: "broke", CWD: "/repo-broke",
		MasterMind: store.Endpoint{PaneID: "w1:p5"},
		Builder:    store.Endpoint{PaneID: "w1:p6"},
		Round:      2, State: store.StateBroken,
	}
	if err := rt.Store.Save(broken); err != nil {
		t.Fatalf("save broken: %v", err)
	}

	got, err := GC(context.Background(), rt, GCOptions{Delete: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Name != "finished" || !got[0].Deleted {
		t.Fatalf("gc result = %+v, want only the done binding deleted", got)
	}

	remaining, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("live and broken bindings must survive, got %d", len(remaining))
	}
}

func TestGCDryRunChangesNothing(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedDone(t, rt, "finished", "/repo-done")

	got, err := GC(context.Background(), rt, GCOptions{DryRun: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Deleted || got[0].Archived {
		t.Fatalf("dry run must report without acting, got %+v", got)
	}
	if _, err := rt.Store.Load("finished"); err != nil {
		t.Errorf("dry run deleted the binding: %v", err)
	}
}

func TestGCArchivesByDefault(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedDone(t, rt, "finished", "/repo-done")
	if err := rt.Store.AppendLog("finished", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	got, err := GC(context.Background(), rt, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || !got[0].Archived || got[0].Deleted {
		t.Fatalf("gc result = %+v, want an archive not a delete", got)
	}
	archived, err := rt.Store.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 || archived[0].Binding.Name != "finished" {
		t.Fatalf("ListArchived = %+v, want the archived binding", archived)
	}
	events, err := rt.Store.ArchivedLog(archived[0].RecordID)
	if err != nil || len(events) != 1 || events[0].Kind != store.KindPrompt {
		t.Errorf("ArchivedLog = %+v, %v; want the plan entry the archive kept", events, err)
	}
}

func TestGCWorktreeTeardown(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{}
	rt := newRuntime(t)
	rt.Git = fg

	seedDone(t, rt, "ordinary", "/repo-ordinary")

	wt := t.TempDir()
	forked := store.Binding{
		Name: "forked", CWD: wt,
		Worktree: wt,
		State:    store.StateDone, Round: 2,
		MasterMind: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(forked); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), rt, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}

	var foundOrdinary, foundForked bool
	for _, r := range res {
		if r.Name == "ordinary" {
			foundOrdinary = true
			if r.WorktreeRemoved != "" || r.WorktreeKept != "" {
				t.Errorf("ordinary binding should have empty worktree fields: %+v", r)
			}
		}
		if r.Name == "forked" {
			foundForked = true
			if r.WorktreeRemoved != wt {
				t.Errorf("WorktreeRemoved = %q, want %q", r.WorktreeRemoved, wt)
			}
		}
	}
	if !foundOrdinary || !foundForked {
		t.Errorf("missing results: ordinary=%v, forked=%v", foundOrdinary, foundForked)
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
	}
	if fg.removeWorktreeCalls[0].Force {
		t.Error("teardown must use force: false")
	}

	// Verify both bindings deleted from store
	list, err := rt.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("all bindings should be cleared, found %d", len(list))
	}
}

func TestGCAfterDoneReportsGone(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{dirtyResult: false}
	rt := newRuntime(t)
	rt.Git = fg

	wt := t.TempDir()
	b := store.Binding{
		Name:       "webshop",
		CWD:        wt,
		Worktree:   wt,
		Branch:     "relevo/webshop",
		State:      store.StateActive,
		Round:      1,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, "webshop")
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeRemoved != wt {
		t.Fatalf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
	}

	// The temp dir still exists on disk because fakeGit removes nothing --
	// so before GC, do os.RemoveAll(wt) to model what real git did.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	gcRes, err := GC(context.Background(), rt, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(gcRes) != 1 {
		t.Fatalf("GC results = %d, want 1", len(gcRes))
	}
	if gcRes[0].WorktreeGone != wt {
		t.Errorf("GC WorktreeGone = %q, want %q", gcRes[0].WorktreeGone, wt)
	}
	if !gcRes[0].Archived {
		t.Error("GC Archived is false, want the binding archived")
	}
}

// rootNames lists a state root's entry names, ignoring the database files the
// store itself owns: relevo.db and its WAL sidecars (P3a: the record lives
// there, so a raw ReadDir sees more than the bindings).
func rootNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "relevo.db") {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

func TestGCWorktreeDryRun(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{}
	rt := newRuntime(t)
	rt.Git = fg

	seedDone(t, rt, "ordinary", "/repo-ordinary")

	wt := t.TempDir()
	forked := store.Binding{
		Name: "forked", CWD: wt,
		Worktree: wt,
		State:    store.StateDone, Round: 2,
		MasterMind: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(forked); err != nil {
		t.Fatal(err)
	}

	// Read state root before
	entriesBefore := rootNames(t, rt.Store.Dir(""))

	res, err := GC(context.Background(), rt, GCOptions{DryRun: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC dry run: %v", err)
	}

	for _, r := range res {
		if r.Name == "forked" && r.WorktreeRemoved != wt {
			t.Errorf("dry-run should report WorktreeRemoved, got %q", r.WorktreeRemoved)
		}
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("dry-run must not call RemoveWorktree, got %d calls", len(fg.removeWorktreeCalls))
	}

	// Verify disk state unchanged
	entriesAfter := rootNames(t, rt.Store.Dir(""))
	if len(entriesBefore) != len(entriesAfter) {
		t.Errorf("entries changed: before=%v, after=%v", entriesBefore, entriesAfter)
		return
	}
	for i := range entriesBefore {
		if entriesBefore[i] != entriesAfter[i] {
			t.Errorf("entry mismatch at %d: %s != %s", i, entriesBefore[i], entriesAfter[i])
		}
	}
}

func TestGCWorktreeDirtyCheckError(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{dirtyErr: errors.New("git lock busy\ndetails")}
	rt := newRuntime(t)
	rt.Git = fg

	wt := t.TempDir()
	forked := store.Binding{
		Name: "forked-dirty-err", CWD: wt,
		Worktree: wt,
		State:    store.StateDone, Round: 2,
		MasterMind: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(forked); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), rt, GCOptions{Delete: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}
	r := res[0]
	if r.Name != "forked-dirty-err" {
		t.Errorf("Name = %q, want forked-dirty-err", r.Name)
	}
	if r.WorktreeRemoved != "" {
		t.Errorf("WorktreeRemoved = %q, want empty", r.WorktreeRemoved)
	}
	if r.WorktreeKept != wt {
		t.Errorf("WorktreeKept = %q, want %q", r.WorktreeKept, wt)
	}
	if r.KeptReason != "dirty check failed: git lock busy" {
		t.Errorf("KeptReason = %q, want 'dirty check failed: git lock busy'", r.KeptReason)
	}
	if !r.Deleted {
		t.Errorf("Deleted = false, want true")
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree should not be called on dirty check error, got %d calls", len(fg.removeWorktreeCalls))
	}

	// Verify binding deleted from store
	if _, err := rt.Store.Load("forked-dirty-err"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding state should be deleted, got err = %v", err)
	}
}

func TestGCDeleteRemovesTheDirectory(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	seedDone(t, rt, "finished", "/repo-done")

	got, err := GC(context.Background(), rt, GCOptions{Delete: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if !got[0].Deleted {
		t.Errorf("Deleted = false, want true")
	}
	if got[0].Archived {
		t.Error("Archived = true, want a delete")
	}

	if _, err := rt.Store.Load("finished"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding state still exists in store: %v", err)
	}

	if archived, err := rt.Store.ListArchived(); err != nil || len(archived) != 0 {
		t.Errorf("ListArchived = %+v, %v; want nothing archived", archived, err)
	}
}

func TestGCReportsAnAlreadyGoneWorktree(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{}
	rt := newRuntime(t)
	rt.Git = fg

	missingWT := filepath.Join(t.TempDir(), "nonexistent-worktree")
	b := store.Binding{
		Name:       "finished-gone",
		CWD:        "/repo-done",
		Worktree:   missingWT,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
		Round:      3,
		State:      store.StateDone,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := GC(context.Background(), rt, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	r := got[0]
	if r.WorktreeGone != missingWT {
		t.Errorf("WorktreeGone = %q, want %q", r.WorktreeGone, missingWT)
	}
	if r.WorktreeKept != "" {
		t.Errorf("WorktreeKept = %q, want empty", r.WorktreeKept)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
	if !r.Archived || r.Deleted {
		t.Errorf("binding was not archived: %+v", r)
	}
}

// TestGCIgnoresPaused: gc sweeps only DONE, so a paused binding -- worktree
// released but the binding very much alive -- survives it untouched.
func TestGCIgnoresPaused(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	b := store.Binding{
		Name: "parked", CWD: "/repo-parked", Worktree: "/wt/parked",
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{Kind: "agy"},
		Round:      3, State: store.StatePaused,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save paused: %v", err)
	}

	got, err := GC(context.Background(), rt, GCOptions{Delete: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("gc considered %+v, want no paused binding", got)
	}
	if _, err := rt.Store.Load("parked"); err != nil {
		t.Fatalf("paused binding must survive gc: %v", err)
	}
}
