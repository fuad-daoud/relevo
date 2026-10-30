package housekeep

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// N2: TestRepoDirOf
func TestRepoDirOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		b    store.Binding
		want string
	}{
		{
			name: "repo set",
			b: store.Binding{
				CWD:  "/cwd/path",
				Repo: "/repo/path",
			},
			want: "/repo/path",
		},
		{
			name: "repo empty with CommonDir set",
			b: store.Binding{
				CWD: "/cwd/path",
				RepoRef: &store.RepoRef{
					CommonDir: "/common/dir",
				},
			},
			want: "/common/dir",
		},
		{
			name: "both empty",
			b: store.Binding{
				CWD: "/cwd/path",
			},
			want: "",
		},
		{
			name: "both empty with non-nil RepoRef",
			b: store.Binding{
				CWD:     "/cwd/path",
				RepoRef: &store.RepoRef{},
			},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := repoDirOf(tc.b)
			if got != tc.want {
				t.Errorf("repoDirOf() = %q, want %q", got, tc.want)
			}
			if got != "" && got == tc.b.CWD {
				t.Errorf("repoDirOf() returned CWD %q", got)
			}
		})
	}
}

// N3: TestBindingRefCandidates
func TestBindingRefCandidates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tests := []struct {
		name     string
		b        store.Binding
		listRefs []string
		wantRefs []string
	}{
		{
			name: "relevo-cut local branch",
			b: store.Binding{
				Name:           "api",
				Branch:         "relevo/api",
				ExistingBranch: false,
			},
			listRefs: []string{"refs/relevo/api/out", "refs/relevo/api/round-1"},
			wantRefs: []string{"refs/heads/relevo/api", "refs/relevo/api/out", "refs/relevo/api/round-1"},
		},
		{
			name: "ExistingBranch local",
			b: store.Binding{
				Name:           "api",
				Branch:         "relevo/api",
				ExistingBranch: true,
			},
			listRefs: []string{"refs/relevo/api/out"},
			wantRefs: []string{"refs/relevo/api/out"},
		},
		{
			name: "adopted remote",
			b: store.Binding{
				Name:           "api",
				Branch:         "feature/x",
				ExistingBranch: true,
				Builder:        store.Endpoint{Mode: store.ModeRemote},
			},
			listRefs: []string{"refs/relevo/api/out"},
			wantRefs: []string{"refs/heads/relevo/api", "refs/relevo/api/out"},
		},
		{
			name: "duplicates are removed",
			b: store.Binding{
				Name:           "api",
				Branch:         "relevo/api",
				ExistingBranch: false,
			},
			listRefs: []string{"refs/heads/relevo/api", "refs/relevo/api/out", "refs/relevo/api/out"},
			wantRefs: []string{"refs/heads/relevo/api", "refs/relevo/api/out"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fg := &fakeRefs{listRefsResult: tc.listRefs}
			deps := Deps{Refs: fg}
			got, err := bindingRefCandidates(ctx, deps, "/repo", tc.b)
			if err != nil {
				t.Fatalf("bindingRefCandidates: %v", err)
			}
			if len(got) != len(tc.wantRefs) {
				t.Fatalf("got %v, want %v", got, tc.wantRefs)
			}
			for i := range got {
				if got[i] != tc.wantRefs[i] {
					t.Errorf("[%d] got %q, want %q", i, got[i], tc.wantRefs[i])
				}
			}
		})
	}
}

// N4: TestCleanRefs
func TestCleanRefs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("on remote: branch uses deleteBranchCalls and non-branch uses deleteRefCalls", func(t *testing.T) {
		testCleanRefsOnRemote(t, ctx)
	})

	t.Run("not on remote", func(t *testing.T) {
		testCleanRefsNotOnRemote(t, ctx)
	})

	t.Run("refOnRemoteErr", func(t *testing.T) {
		testCleanRefsCheckFailed(t, ctx)
	})

	t.Run("deleteBranchErr", func(t *testing.T) {
		testCleanRefsDeleteFailed(t, ctx)
	})

	t.Run("dryRun", func(t *testing.T) {
		testCleanRefsDryRun(t, ctx)
	})
}

func testCleanRefsOnRemote(t *testing.T, ctx context.Context) {
	t.Helper()
	fg := &fakeRefs{
		refOnRemote: map[string]bool{
			"refs/heads/relevo/api": true,
			"refs/relevo/api/out":   true,
		},
	}
	deps := Deps{Refs: fg}
	got := cleanRefs(ctx, deps, "/repo", []string{"refs/heads/relevo/api", "refs/relevo/api/out"}, false)
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 outcomes", got)
	}
	if !got[0].Deleted || got[0].Ref != "refs/heads/relevo/api" {
		t.Errorf("got[0] = %+v, want Deleted", got[0])
	}
	if !got[1].Deleted || got[1].Ref != "refs/relevo/api/out" {
		t.Errorf("got[1] = %+v, want Deleted", got[1])
	}
	if len(fg.deleteBranchCalls) != 1 || fg.deleteBranchCalls[0].Branch != "refs/heads/relevo/api" {
		t.Errorf("deleteBranchCalls = %v", fg.deleteBranchCalls)
	}
	if len(fg.deleteRefCalls) != 1 || fg.deleteRefCalls[0].Ref != "refs/relevo/api/out" {
		t.Errorf("deleteRefCalls = %v", fg.deleteRefCalls)
	}
}

func testCleanRefsNotOnRemote(t *testing.T, ctx context.Context) {
	t.Helper()
	fg := &fakeRefs{
		refOnRemote: map[string]bool{},
	}
	deps := Deps{Refs: fg}
	got := cleanRefs(ctx, deps, "/repo", []string{"refs/heads/relevo/api"}, false)
	if len(got) != 1 {
		t.Fatalf("got %v, want 1 outcome", got)
	}
	if got[0].Deleted || got[0].WouldDelete || got[0].Reason != "not on any remote-tracking ref" {
		t.Errorf("got[0] = %+v, want reason 'not on any remote-tracking ref'", got[0])
	}
	if len(fg.deleteBranchCalls) != 0 || len(fg.deleteRefCalls) != 0 {
		t.Errorf("expected no delete calls, got branch: %v, ref: %v", fg.deleteBranchCalls, fg.deleteRefCalls)
	}
}

func testCleanRefsCheckFailed(t *testing.T, ctx context.Context) {
	t.Helper()
	fg := &fakeRefs{
		refOnRemoteErr: errors.New("remote check boom"),
	}
	deps := Deps{Refs: fg}
	got := cleanRefs(ctx, deps, "/repo", []string{"refs/heads/relevo/api"}, false)
	if len(got) != 1 {
		t.Fatalf("got %v, want 1 outcome", got)
	}
	wantReason := "check failed: remote check boom"
	if got[0].Reason != wantReason {
		t.Errorf("got reason %q, want %q", got[0].Reason, wantReason)
	}
	if len(fg.deleteBranchCalls) != 0 {
		t.Errorf("expected no delete calls, got %v", fg.deleteBranchCalls)
	}
}

func testCleanRefsDeleteFailed(t *testing.T, ctx context.Context) {
	t.Helper()
	fg := &fakeRefs{
		refOnRemote:     map[string]bool{"refs/heads/relevo/api": true},
		deleteBranchErr: errors.New("cannot delete branch checked out"),
	}
	deps := Deps{Refs: fg}
	got := cleanRefs(ctx, deps, "/repo", []string{"refs/heads/relevo/api"}, false)
	if len(got) != 1 {
		t.Fatalf("got %v, want 1 outcome", got)
	}
	wantReason := "delete failed: cannot delete branch checked out"
	if got[0].Reason != wantReason {
		t.Errorf("got reason %q, want %q", got[0].Reason, wantReason)
	}
}

func testCleanRefsDryRun(t *testing.T, ctx context.Context) {
	t.Helper()
	fg := &fakeRefs{
		refOnRemote: map[string]bool{
			"refs/heads/relevo/api": true,
			"refs/relevo/api/out":   true,
		},
	}
	deps := Deps{Refs: fg}
	got := cleanRefs(ctx, deps, "/repo", []string{"refs/heads/relevo/api", "refs/relevo/api/out"}, true)
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 outcomes", got)
	}
	for i, r := range got {
		if !r.WouldDelete || r.Deleted || r.Reason != "" {
			t.Errorf("[%d] = %+v, want WouldDelete", i, r)
		}
	}
	if len(fg.deleteBranchCalls) != 0 || len(fg.deleteRefCalls) != 0 {
		t.Errorf("expected no delete calls on dry-run, got branch: %v, ref: %v", fg.deleteBranchCalls, fg.deleteRefCalls)
	}
}

// N5: TestSweepRefs
func TestSweepRefs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	deps := gcTestDeps(t)

	// Save a live binding api in StateDone
	b := store.Binding{
		Name:       "api",
		CWD:        "/repo",
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
		Round:      1,
		State:      store.StateDone,
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatalf("save api: %v", err)
	}

	fg := &fakeRefs{
		listRefsByPrefix: map[string][]string{
			"refs/heads/relevo/": {
				"refs/heads/relevo/api",
				"refs/heads/relevo/old",
				"refs/heads/relevo/",
			},
			"refs/relevo/": {
				"refs/relevo/old/round-2",
				"refs/relevo/Bad.Name/out",
			},
		},
		refOnRemote: map[string]bool{
			"refs/heads/relevo/old": true,
			// refs/relevo/old/round-2 is false (not on remote)
		},
	}
	deps.Refs = fg

	res, err := SweepRefs(ctx, deps, "/repo", false)
	if err != nil {
		t.Fatalf("SweepRefs: %v", err)
	}

	want := []RefOutcome{
		{Ref: "refs/heads/relevo/api", Reason: "live binding"},
		{Ref: "refs/heads/relevo/old", Deleted: true},
		{Ref: "refs/relevo/old/round-2", Reason: "not on any remote-tracking ref"},
	}

	if len(res.Refs) != len(want) {
		t.Fatalf("got %d refs (%+v), want %d (%+v)", len(res.Refs), res.Refs, len(want), want)
	}
	for i := range want {
		if res.Refs[i].Ref != want[i].Ref || res.Refs[i].Deleted != want[i].Deleted || res.Refs[i].Reason != want[i].Reason {
			t.Errorf("[%d] got %+v, want %+v", i, res.Refs[i], want[i])
		}
	}
}

// N6: TestRefLines
func TestRefLines(t *testing.T) {
	t.Parallel()

	outcomes := []RefOutcome{
		{Ref: "refs/heads/relevo/old", Deleted: true},
		{Ref: "refs/heads/relevo/dry", WouldDelete: true},
		{Ref: "refs/heads/relevo/kept", Reason: "not on any remote-tracking ref"},
	}
	got := RefLines(outcomes)
	want := []string{
		"deleted      refs/heads/relevo/old",
		"would delete refs/heads/relevo/dry",
		"kept         refs/heads/relevo/kept (not on any remote-tracking ref)",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

// N7: TestGCDeletesPushedRelevoBranch
func TestGCDeletesPushedRelevoBranch(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	wt := t.TempDir()
	fg := &fakeRefs{
		refOnRemote: map[string]bool{
			"refs/heads/relevo/web": true,
		},
	}
	deps.Refs = fg

	b := store.Binding{
		Name:       "web",
		Repo:       "/repo",
		CWD:        wt,
		Worktree:   wt,
		Branch:     "relevo/web",
		State:      store.StateDone,
		Round:      1,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), deps, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("GC returned %d results, want 1", len(res))
	}
	if len(fg.deleteBranchCalls) != 1 || fg.deleteBranchCalls[0].Dir != "/repo" || fg.deleteBranchCalls[0].Branch != "refs/heads/relevo/web" {
		t.Fatalf("deleteBranchCalls = %v, want [{Dir: /repo, Branch: refs/heads/relevo/web}]", fg.deleteBranchCalls)
	}
	if len(res[0].Refs) != 1 || !res[0].Refs[0].Deleted {
		t.Fatalf("res[0].Refs = %+v, want 1 deleted ref", res[0].Refs)
	}
}

// N8: TestGCKeepsUnpushedBranch
func TestGCKeepsUnpushedBranch(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	wt := t.TempDir()
	fg := &fakeRefs{
		refOnRemote: map[string]bool{
			"refs/heads/relevo/web": false,
		},
	}
	deps.Refs = fg

	b := store.Binding{
		Name:       "web",
		Repo:       "/repo",
		CWD:        wt,
		Worktree:   wt,
		Branch:     "relevo/web",
		State:      store.StateDone,
		Round:      1,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), deps, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("GC returned %d results, want 1", len(res))
	}
	if len(fg.deleteBranchCalls) != 0 {
		t.Fatalf("deleteBranchCalls = %v, want none", fg.deleteBranchCalls)
	}
	if len(res[0].Refs) != 1 || res[0].Refs[0].Reason != "not on any remote-tracking ref" {
		t.Fatalf("res[0].Refs = %+v, want reason 'not on any remote-tracking ref'", res[0].Refs)
	}
}

// N9: TestGCKeptWorktreeTouchesNoRef
func TestGCKeptWorktreeTouchesNoRef(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	wt := t.TempDir()
	fg := &fakeRefs{
		refOnRemote: map[string]bool{
			"refs/heads/relevo/web": true,
		},
	}
	deps.Refs = fg
	deps.Teardown = func(_ context.Context, b store.Binding, _ bool) TeardownOutcome {
		return TeardownOutcome{Kept: b.Worktree, Reason: "uncommitted changes"}
	}

	b := store.Binding{
		Name:       "web",
		Repo:       "/repo",
		CWD:        wt,
		Worktree:   wt,
		Branch:     "relevo/web",
		State:      store.StateDone,
		Round:      1,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), deps, GCOptions{AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("GC returned %d results, want 1", len(res))
	}
	if len(fg.refOnRemoteCalls) != 0 || len(fg.deleteBranchCalls) != 0 || len(fg.deleteRefCalls) != 0 {
		t.Fatalf("expected no git calls, got refOnRemote: %v, deleteBranch: %v, deleteRef: %v",
			fg.refOnRemoteCalls, fg.deleteBranchCalls, fg.deleteRefCalls)
	}
	if len(res[0].Refs) != 0 {
		t.Fatalf("res[0].Refs = %+v, want empty", res[0].Refs)
	}
}

// N10: TestGCDryRunDeletesNoRef
func TestGCDryRunDeletesNoRef(t *testing.T) {
	t.Parallel()

	deps := gcTestDeps(t)
	wt := t.TempDir()
	fg := &fakeRefs{
		refOnRemote: map[string]bool{
			"refs/heads/relevo/web": true,
		},
	}
	deps.Refs = fg

	b := store.Binding{
		Name:       "web",
		Repo:       "/repo",
		CWD:        wt,
		Worktree:   wt,
		Branch:     "relevo/web",
		State:      store.StateDone,
		Round:      1,
		MasterMind: store.Endpoint{PaneID: "w1:p1"},
		Builder:    store.Endpoint{PaneID: "w1:p2"},
	}
	if err := deps.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := GC(context.Background(), deps, GCOptions{DryRun: true, AllMasterMinds: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("GC returned %d results, want 1", len(res))
	}
	if len(fg.deleteBranchCalls) != 0 || len(fg.deleteRefCalls) != 0 {
		t.Fatalf("delete calls on dry run: branch=%v, ref=%v", fg.deleteBranchCalls, fg.deleteRefCalls)
	}
	if len(res[0].Refs) != 1 || !res[0].Refs[0].WouldDelete {
		t.Fatalf("res[0].Refs = %+v, want WouldDelete", res[0].Refs)
	}
}

// N9: bindingRefCandidates proposes the relevo head ref only when the repo
// has it, still proposes it when RefSHA errors, and keeps the refs it
// listed. Mutation: ignore ok and the missing-branch case proposes a
// nonexistent ref.
func TestBindingRefCandidatesSkipsAMissingBranch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cut := store.Binding{Name: "api", Branch: "relevo/api"}
	adopted := store.Binding{Name: "api", Branch: "feature/x", ExistingBranch: true, Builder: store.Endpoint{Mode: store.ModeRemote}}

	t.Run("relevo-cut shape, missing head ref", func(t *testing.T) {
		fg := &fakeRefs{refSHA: map[string]string{}, listRefsResult: []string{"refs/relevo/api/out"}}
		got, err := bindingRefCandidates(ctx, Deps{Refs: fg}, "/repo", cut)
		if err != nil {
			t.Fatalf("bindingRefCandidates: %v", err)
		}
		if want := []string{"refs/relevo/api/out"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("relevo-cut shape, present head ref", func(t *testing.T) {
		fg := &fakeRefs{
			refSHA:         map[string]string{"refs/heads/relevo/api": "tip"},
			listRefsResult: []string{"refs/relevo/api/out"},
		}
		got, err := bindingRefCandidates(ctx, Deps{Refs: fg}, "/repo", cut)
		if err != nil {
			t.Fatalf("bindingRefCandidates: %v", err)
		}
		if want := []string{"refs/heads/relevo/api", "refs/relevo/api/out"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("RefSHA error still proposes the head ref", func(t *testing.T) {
		fg := &fakeRefs{refSHAErr: errors.New("resolve boom"), listRefsResult: []string{"refs/relevo/api/out"}}
		got, err := bindingRefCandidates(ctx, Deps{Refs: fg}, "/repo", cut)
		if err != nil {
			t.Fatalf("bindingRefCandidates: %v", err)
		}
		if want := []string{"refs/heads/relevo/api", "refs/relevo/api/out"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("adopted remote shape, missing head ref", func(t *testing.T) {
		fg := &fakeRefs{refSHA: map[string]string{}, listRefsResult: []string{"refs/relevo/api/out"}}
		got, err := bindingRefCandidates(ctx, Deps{Refs: fg}, "/repo", adopted)
		if err != nil {
			t.Fatalf("bindingRefCandidates: %v", err)
		}
		if want := []string{"refs/relevo/api/out"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}
