package housekeep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeRefs is the slice of the git CLI ref cleanup drives: the head
// existence probe, the relevo ref listing, the pushed check, and the branch
// and ref deletes.
type fakeRefs struct {
	refSHA    map[string]string
	refSHAErr error

	listRefsResult   []string
	listRefsByPrefix map[string][]string
	listRefsErr      error
	listRefsCalls    []listRefsCall

	refOnRemote      map[string]bool
	refOnRemoteErr   error
	refOnRemoteCalls []refOnRemoteCall

	deleteBranchCalls []deleteBranchCall
	deleteBranchErr   error

	deleteRefCalls []deleteRefCall
	deleteRefErr   error

	refSHACalls []refSHACall
}

type refSHACall struct {
	Dir, Ref string
}

type listRefsCall struct {
	Dir, Prefix string
}

type refOnRemoteCall struct {
	Dir, Ref string
}

type deleteBranchCall struct {
	Dir, Branch string
}

type deleteRefCall struct {
	Dir, Ref string
}

func (f *fakeRefs) RefSHA(_ context.Context, dir, ref string) (string, bool, error) {
	f.refSHACalls = append(f.refSHACalls, refSHACall{Dir: dir, Ref: ref})
	if f.refSHAErr != nil {
		return "", false, f.refSHAErr
	}
	if f.refSHA != nil {
		sha, ok := f.refSHA[ref]
		return sha, ok, nil
	}
	return "fakerefsha", true, nil
}

func (f *fakeRefs) ListRefs(_ context.Context, dir, prefix string) ([]string, error) {
	f.listRefsCalls = append(f.listRefsCalls, listRefsCall{Dir: dir, Prefix: prefix})
	if f.listRefsErr != nil {
		return nil, f.listRefsErr
	}
	if f.listRefsByPrefix != nil {
		return f.listRefsByPrefix[prefix], nil
	}
	return f.listRefsResult, nil
}

func (f *fakeRefs) RefOnRemote(_ context.Context, dir, ref string) (bool, error) {
	f.refOnRemoteCalls = append(f.refOnRemoteCalls, refOnRemoteCall{Dir: dir, Ref: ref})
	if f.refOnRemoteErr != nil {
		return false, f.refOnRemoteErr
	}
	return f.refOnRemote[ref], nil
}

func (f *fakeRefs) DeleteBranch(_ context.Context, dir, branch string) error {
	f.deleteBranchCalls = append(f.deleteBranchCalls, deleteBranchCall{Dir: dir, Branch: branch})
	return f.deleteBranchErr
}

func (f *fakeRefs) DeleteRef(_ context.Context, dir, ref string) error {
	f.deleteRefCalls = append(f.deleteRefCalls, deleteRefCall{Dir: dir, Ref: ref})
	return f.deleteRefErr
}

// gcTestDeps is a Deps over a temp store for GC tests: no git, a teardown
// that reports nothing, and an ask that releases. Tests that need git or a
// livelier engine set the fields they need.
func gcTestDeps(t *testing.T) Deps {
	t.Helper()
	return Deps{
		Store: store.New(t.TempDir()),
		Teardown: func(context.Context, store.Binding, bool) TeardownOutcome {
			return TeardownOutcome{}
		},
		AskUnbind: func(context.Context, store.Binding, bool) error { return nil },
	}
}

func seedDone(t *testing.T, deps Deps, name, cwd string) {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: cwd,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
		Round:      3, State: store.StateDone,
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// seedDoneFor is seedDone with an explicit MasterMindID, for the GC scope tests.
func seedDoneFor(t *testing.T, deps Deps, name, cwd, mastermindID string) {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: cwd,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
		Round:      3, State: store.StateDone,
		MasterMindID: mastermindID,
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// TestGCClearsOnlyThisMasterMindsBindings pins the scope rule: GC in
// mastermind-scoped mode only clears bindings whose MasterMindID matches,
// leaving another mastermind's bindings and a legacy (MasterMindID-less)
// binding untouched.
func TestGCClearsOnlyThisMasterMindsBindings(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	seedDoneFor(t, deps, "a1", "/repo-a1", "pl_aaa")
	seedDoneFor(t, deps, "b1", "/repo-b1", "pl_bbb")
	seedDoneFor(t, deps, "legacy", "/repo-legacy", "")

	got, err := GC(context.Background(), deps, GCOptions{MasterMindID: "pl_aaa", Delete: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a1" || got[0].MasterMindID != "pl_aaa" {
		t.Fatalf("gc result = %+v, want only a1 with MasterMindID pl_aaa", got)
	}

	remaining, err := deps.Store.List()
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

// TestGCAllMasterMindsClearsEveryDoneBinding pins the AllMasterMinds rule: it
// clears every DONE binding regardless of mastermind, including a legacy
// MasterMindID-less one.
func TestGCAllMasterMindsClearsEveryDoneBinding(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	seedDoneFor(t, deps, "a1", "/repo-a1", "pl_aaa")
	seedDoneFor(t, deps, "b1", "/repo-b1", "pl_bbb")
	seedDoneFor(t, deps, "legacy", "/repo-legacy", "")

	got, err := GC(context.Background(), deps, GCOptions{AllMasterMinds: true, Delete: true})
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

// TestGCRefusesWithoutScope pins the scope refusal: GC refuses to run with
// neither a MasterMindID nor AllMasterMinds, and refuses when both are set, so
// no caller can get "everything" by leaving the scope empty.
func TestGCRefusesWithoutScope(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	seedDoneFor(t, deps, "a1", "/repo-a1", "pl_aaa")
	seedDoneFor(t, deps, "b1", "/repo-b1", "pl_bbb")
	seedDoneFor(t, deps, "legacy", "/repo-legacy", "")

	if _, err := GC(context.Background(), deps, GCOptions{}); !errors.Is(err, ErrGCNoScope) {
		t.Fatalf("GC with no scope: err = %v, want ErrGCNoScope", err)
	}
	if _, err := GC(context.Background(), deps, GCOptions{MasterMindID: "pl_aaa", AllMasterMinds: true}); !errors.Is(err, ErrGCNoScope) {
		t.Fatalf("GC with both scopes: err = %v, want ErrGCNoScope", err)
	}

	remaining, err := deps.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 3 {
		t.Errorf("all three bindings must survive a scope error, got %d", len(remaining))
	}
}

// TestGCMasterMindDryRunListsOnlyThisMasterMind pins the scoped dry run: it
// lists only that mastermind's bindings and changes nothing.
func TestGCMasterMindDryRunListsOnlyThisMasterMind(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	seedDoneFor(t, deps, "a1", "/repo-a1", "pl_aaa")
	seedDoneFor(t, deps, "b1", "/repo-b1", "pl_bbb")
	seedDoneFor(t, deps, "legacy", "/repo-legacy", "")

	got, err := GC(context.Background(), deps, GCOptions{MasterMindID: "pl_bbb", DryRun: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Name != "b1" {
		t.Fatalf("gc result = %+v, want only b1", got)
	}

	remaining, err := deps.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 3 {
		t.Errorf("dry run must change nothing, got %d bindings", len(remaining))
	}
}

func TestGCClearsOnlyDoneBindings(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	seedDone(t, deps, "finished", "/repo-done")

	live := store.Binding{
		Name: "live", CWD: "/repo-live",
		MasterMind: store.Endpoint{PaneID: "w1:p3"},
		Builder:    store.Endpoint{PaneID: "w1:p4"},
		Round:      1, State: store.StateActive,
	}
	if err := deps.Store.Save(live); err != nil {
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
	if err := deps.Store.Save(broken); err != nil {
		t.Fatalf("save broken: %v", err)
	}

	got, err := GC(context.Background(), deps, GCOptions{Delete: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Name != "finished" || !got[0].Deleted {
		t.Fatalf("gc result = %+v, want only the done binding deleted", got)
	}

	remaining, err := deps.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("live and broken bindings must survive, got %d", len(remaining))
	}
}

func TestGCDryRunChangesNothing(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	seedDone(t, deps, "finished", "/repo-done")

	got, err := GC(context.Background(), deps, GCOptions{DryRun: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Deleted || got[0].Archived {
		t.Fatalf("dry run must report without acting, got %+v", got)
	}
	if _, err := deps.Store.Load("finished"); err != nil {
		t.Errorf("dry run deleted the binding: %v", err)
	}
}

func TestGCArchivesByDefault(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	seedDone(t, deps, "finished", "/repo-done")
	if err := deps.Store.AppendLog("finished", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	got, err := GC(context.Background(), deps, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || !got[0].Archived || got[0].Deleted {
		t.Fatalf("gc result = %+v, want an archive not a delete", got)
	}
	archived, err := deps.Store.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 || archived[0].Binding.Name != "finished" {
		t.Fatalf("ListArchived = %+v, want the archived binding", archived)
	}
	events, err := deps.Store.ArchivedLog(archived[0].RecordID)
	if err != nil || len(events) != 1 || events[0].Kind != store.KindPrompt {
		t.Errorf("ArchivedLog = %+v, %v; want the plan entry the archive kept", events, err)
	}
}

func TestGCWorktreeTeardown(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	type teardownCall struct {
		name   string
		dryRun bool
	}
	var calls []teardownCall
	deps.Teardown = func(_ context.Context, b store.Binding, dryRun bool) TeardownOutcome {
		calls = append(calls, teardownCall{name: b.Name, dryRun: dryRun})
		if b.Worktree == "" {
			return TeardownOutcome{}
		}
		return TeardownOutcome{Removed: b.Worktree}
	}

	seedDone(t, deps, "ordinary", "/repo-ordinary")

	wt := t.TempDir()
	forked := store.Binding{
		Name: "forked", CWD: wt,
		Worktree: wt,
		State:    store.StateDone, Round: 2,
		MasterMind: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := deps.Store.Save(forked); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), deps, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}

	checkGCTeardownResults(t, res, wt)
	if len(calls) != 2 {
		t.Fatalf("teardown calls = %d, want one per binding", len(calls))
	}
	for _, c := range calls {
		if c.dryRun {
			t.Error("teardown must run with dryRun: false")
		}
	}

	// Verify both bindings were cleared from the store
	list, err := deps.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("all bindings should be cleared, found %d", len(list))
	}
}

// checkGCTeardownResults pins the per-binding worktree fields of a GC over
// one plain binding and one the teardown removed.
func checkGCTeardownResults(t *testing.T, res []GCResult, wt string) {
	t.Helper()
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
}

func TestGCAfterDoneReportsGone(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	wt := t.TempDir()
	b := store.Binding{
		Name:       "webshop",
		CWD:        wt,
		Worktree:   wt,
		Branch:     "relevo/webshop",
		State:      store.StateDone,
		Round:      1,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	// The temp dir still exists on disk -- so before GC, do os.RemoveAll(wt)
	// to model what the teardown's removal left behind.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	deps.Teardown = func(_ context.Context, b store.Binding, _ bool) TeardownOutcome {
		return TeardownOutcome{Gone: b.Worktree}
	}

	gcRes, err := GC(context.Background(), deps, GCOptions{AllMasterMinds: true})
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
// store itself owns: relevo.db and its WAL sidecars, so a raw ReadDir sees
// more than the bindings.
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

	deps := gcTestDeps(t)
	var sawDryRun bool
	deps.Teardown = func(_ context.Context, b store.Binding, dryRun bool) TeardownOutcome {
		if b.Worktree == "" {
			return TeardownOutcome{}
		}
		sawDryRun = dryRun
		return TeardownOutcome{Removed: b.Worktree}
	}

	seedDone(t, deps, "ordinary", "/repo-ordinary")

	wt := t.TempDir()
	forked := store.Binding{
		Name: "forked", CWD: wt,
		Worktree: wt,
		State:    store.StateDone, Round: 2,
		MasterMind: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := deps.Store.Save(forked); err != nil {
		t.Fatal(err)
	}

	// Read state root before
	entriesBefore := rootNames(t, deps.Store.Dir(""))

	res, err := GC(context.Background(), deps, GCOptions{DryRun: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC dry run: %v", err)
	}

	for _, r := range res {
		if r.Name == "forked" && r.WorktreeRemoved != wt {
			t.Errorf("dry-run should report WorktreeRemoved, got %q", r.WorktreeRemoved)
		}
	}
	if !sawDryRun {
		t.Error("dry-run must reach the teardown as dryRun: true")
	}

	// Verify disk state unchanged
	entriesAfter := rootNames(t, deps.Store.Dir(""))
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

func gcKeptDeps(t *testing.T) (Deps, string) {
	t.Helper()
	deps := gcTestDeps(t)

	wt := t.TempDir()
	forked := store.Binding{
		Name: "forked-dirty-err", CWD: wt,
		Worktree: wt,
		State:    store.StateDone, Round: 2,
		MasterMind: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := deps.Store.Save(forked); err != nil {
		t.Fatal(err)
	}
	deps.Teardown = func(_ context.Context, b store.Binding, _ bool) TeardownOutcome {
		return TeardownOutcome{Kept: b.Worktree, Reason: "dirty check failed: git lock busy"}
	}
	return deps, wt
}

func TestGCKeepsBindingWithKeptWorktree(t *testing.T) {
	t.Parallel()

	deps, wt := gcKeptDeps(t)

	for _, dry := range []bool{true, false} {
		res, err := GC(context.Background(), deps, GCOptions{Delete: true, DryRun: dry, AllMasterMinds: true})
		if err != nil {
			t.Fatalf("GC dry=%v: %v", dry, err)
		}
		if len(res) != 1 {
			t.Fatalf("expected 1 result, got %d", len(res))
		}
		r := res[0]
		if r.WorktreeKept != wt || r.KeptReason != "dirty check failed: git lock busy" {
			t.Errorf("kept = %q (%q), want %q with the dirty-check reason", r.WorktreeKept, r.KeptReason, wt)
		}
		if !r.BindingKept || r.Deleted || r.Archived {
			t.Errorf("result = %+v, want BindingKept and neither archived nor deleted", r)
		}
		if got, err := deps.Store.Load("forked-dirty-err"); err != nil || got.State != store.StateDone {
			t.Errorf("binding = %+v, err %v; want it to stay DONE", got.State, err)
		}
	}
}

func TestGCForceArchivesKeptWorktree(t *testing.T) {
	t.Parallel()

	deps, wt := gcKeptDeps(t)

	dry, err := GC(context.Background(), deps, GCOptions{Force: true, DryRun: true, AllMasterMinds: true})
	if err != nil || len(dry) != 1 || dry[0].BindingKept {
		t.Fatalf("forced dry run = %+v, %v; want a result that is not BindingKept", dry, err)
	}

	res, err := GC(context.Background(), deps, GCOptions{Delete: true, Force: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 || !res[0].Deleted || res[0].BindingKept || res[0].WorktreeKept != wt {
		t.Fatalf("result = %+v, want deleted with the worktree reported kept", res)
	}
	if _, err := deps.Store.Load("forked-dirty-err"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding state should be deleted, got err = %v", err)
	}
}

func TestGCDeleteRemovesTheDirectory(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	seedDone(t, deps, "finished", "/repo-done")

	got, err := GC(context.Background(), deps, GCOptions{Delete: true, AllMasterMinds: true})
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

	if _, err := deps.Store.Load("finished"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding state still exists in store: %v", err)
	}

	if archived, err := deps.Store.ListArchived(); err != nil || len(archived) != 0 {
		t.Errorf("ListArchived = %+v, %v; want nothing archived", archived, err)
	}
}

func TestGCReportsAnAlreadyGoneWorktree(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	deps.Teardown = func(_ context.Context, b store.Binding, _ bool) TeardownOutcome {
		return TeardownOutcome{Gone: b.Worktree}
	}

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
	if err := deps.Store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := GC(context.Background(), deps, GCOptions{AllMasterMinds: true})
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
	if !r.Archived || r.Deleted {
		t.Errorf("binding was not archived: %+v", r)
	}
}

// TestGCIgnoresPaused: gc sweeps only DONE, so a paused binding -- worktree
// released but the binding very much alive -- survives it untouched.
func TestGCIgnoresPaused(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)

	b := store.Binding{
		Name: "parked", CWD: "/repo-parked", Worktree: "/wt/parked",
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{Kind: "agy"},
		Round:      3, State: store.StatePaused,
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatalf("save paused: %v", err)
	}

	got, err := GC(context.Background(), deps, GCOptions{Delete: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("gc considered %+v, want no paused binding", got)
	}
	if _, err := deps.Store.Load("parked"); err != nil {
		t.Fatalf("paused binding must survive gc: %v", err)
	}
}
