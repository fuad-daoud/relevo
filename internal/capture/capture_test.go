package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestRoundDiff_NilGit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	d := Deps{Store: s}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = RoundDiff(ctx, d, tx, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if res.Available {
		t.Fatal("expected Available=false with nil Git")
	}
	if line := DiffLine(res, CommitResult{}, "", "webshop", 1); line != "" {
		t.Fatalf("expected empty line for nil Git, got %q", line)
	}
}

func TestRoundDiff_ErrNotRepo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeErr: git.ErrNotRepo}
	d := Deps{Store: s, Git: fg}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = RoundDiff(ctx, d, tx, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if res.Available {
		t.Fatal("expected Available=false for ErrNotRepo")
	}
	if line := DiffLine(res, CommitResult{}, "", "webshop", 1); line != "" {
		t.Fatalf("expected empty line for ErrNotRepo, got %q", line)
	}
}

func TestRoundDiff_GitFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeErr: errors.New("boom: git broken")}
	d := Deps{Store: s, Git: fg}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = RoundDiff(ctx, d, tx, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if res.Available {
		t.Fatal("expected Available=false for git failure")
	}
	if res.Reason != "boom: git broken" {
		t.Fatalf("unexpected Reason: %q", res.Reason)
	}
	wantLine := "Diff: unavailable (boom: git broken)"
	if line := DiffLine(res, CommitResult{}, "", "webshop", 1); line != wantLine {
		t.Fatalf("got %q, want %q", line, wantLine)
	}
}

func TestRoundDiff_EmptyDiff(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		diffResult:     git.Diff{Stat: git.Stat{FilesChanged: 0}},
	}
	d := Deps{Store: s, Git: fg}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "tree-start"}

	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = RoundDiff(ctx, d, tx, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !res.Available {
		t.Fatal("expected Available=true for empty diff")
	}
	if res.Path != "" {
		t.Fatalf("expected empty Path, got %q", res.Path)
	}
	if _, err := os.Stat(s.DiffPath("webshop", 1)); !os.IsNotExist(err) {
		t.Fatalf("expected no patch file written, got err %v", err)
	}
	if _, err := s.ReadFile(s.DiffPath("webshop", 1)); err == nil {
		t.Fatal("expected no patch row stored, got nil err")
	}
	wantLine := "Diff: no file changes"
	if line := DiffLine(res, CommitResult{}, "", "webshop", 1); line != wantLine {
		t.Fatalf("got %q, want %q", line, wantLine)
	}
}

func TestRoundDiff_TruncatedDiff(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		diffResult: git.Diff{
			Stat:      git.Stat{FilesChanged: 312, Insertions: 48120, Deletions: 9033},
			Truncated: true,
		},
	}
	d := Deps{Store: s, Git: fg}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "tree-start"}

	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = RoundDiff(ctx, d, tx, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !res.Available {
		t.Fatal("expected Available=true for truncated diff")
	}
	if !res.Truncated {
		t.Fatal("expected Truncated=true")
	}
	if res.Path != "" {
		t.Fatalf("expected empty Path, got %q", res.Path)
	}
	if _, err := os.Stat(s.DiffPath("webshop", 1)); !os.IsNotExist(err) {
		t.Fatalf("expected no patch file written, got err %v", err)
	}
	if _, err := s.ReadFile(s.DiffPath("webshop", 1)); err == nil {
		t.Fatal("expected no patch row stored, got nil err")
	}
	wantLine := "Diff: 312 files, +48120 -9033 (patch omitted, over the 4 MiB cap)"
	if line := DiffLine(res, CommitResult{}, "", "webshop", 1); line != wantLine {
		t.Fatalf("got %q, want %q", line, wantLine)
	}
}

func TestRoundDiff_NormalDiff(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 7, Insertions: 214, Deletions: 38},
			Patch: []byte("--- a/file\n+++ b/file\n@@ ...\n"),
		},
	}
	d := Deps{Store: s, Git: fg}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 2, RoundBaselineTree: "tree-start"}

	// Must save binding so s.Dir("webshop") exists for writing the patch
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = RoundDiff(ctx, d, tx, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !res.Available {
		t.Fatal("expected Available=true")
	}
	expectedPath := s.DiffPath("webshop", 2)
	if res.Path != expectedPath {
		t.Fatalf("got path %q, want %q", res.Path, expectedPath)
	}

	if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
		t.Fatalf("expected no patch file on disk, got err %v", err)
	}

	data, err := s.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("read stored patch: %v", err)
	}
	if string(data) != "--- a/file\n+++ b/file\n@@ ...\n" {
		t.Fatalf("unexpected patch content: %s", string(data))
	}

	wantLine := "Diff: relevo show webshop --round 2 --diff (7 files, +214 -38)"
	if line := DiffLine(res, CommitResult{}, "", "webshop", 2); line != wantLine {
		t.Fatalf("got %q, want %q", line, wantLine)
	}

	// ReadDiff
	patch, ok, err := ReadDiff(s, "webshop", 2)
	if err != nil || !ok {
		t.Fatalf("ReadDiff failed: ok=%v, err=%v", ok, err)
	}
	if string(patch) != string(data) {
		t.Fatalf("ReadDiff mismatch: %s", string(patch))
	}

	// ReadDiff for round without patch
	patch, ok, err = ReadDiff(s, "webshop", 1)
	if err != nil || ok || patch != nil {
		t.Fatalf("expected nil, false, nil for round 1, got %v, %v, %v", patch, ok, err)
	}

	// ReadDiff for missing binding
	_, _, err = ReadDiff(s, "nonexistent", 1)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing binding, got %v", err)
	}
}

func TestBaseline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	b := store.Binding{Name: "webshop", CWD: "/repo"}
	newDeps := func(g Git) Deps {
		return Deps{Store: s, Git: g}
	}

	tree, head := Baseline(ctx, newDeps(&fakeGit{snapshotTreeID: "tree-base", headCommitID: "head-base"}), b)
	if tree != "tree-base" || head != "head-base" {
		t.Fatalf("got (%q, %q), want (tree-base, head-base)", tree, head)
	}

	if tree, head := Baseline(ctx, newDeps(nil), b); tree != "" || head != "" {
		t.Fatalf("nil git: got (%q, %q), want both empty", tree, head)
	}

	fgSnap := &fakeGit{snapshotTreeErr: errors.New("fail"), headCommitID: "head-base"}
	if tree, head := Baseline(ctx, newDeps(fgSnap), b); tree != "" || head != "" {
		t.Fatalf("snapshot failure: got (%q, %q), want both empty", tree, head)
	}
	if fgSnap.headCalls != 0 {
		t.Fatalf("HeadCommit called %d times after a failed snapshot, want 0", fgSnap.headCalls)
	}

	if tree, head := Baseline(ctx, newDeps(&fakeGit{snapshotTreeID: "tree-base", headCommitErr: errors.New("unborn")}), b); tree != "tree-base" || head != "" {
		t.Fatalf("head failure: got (%q, %q), want (tree-base, \"\")", tree, head)
	}
}

func TestRoundDiff_EmptyBaselineSkipsSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeID: "tree-end"}
	d := Deps{Store: s, Git: fg}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: ""}

	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = RoundDiff(ctx, d, tx, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if res.Available {
		t.Fatal("expected Available=false for empty baseline")
	}
	if res.Reason != "no baseline" {
		t.Fatalf("got Reason %q, want %q", res.Reason, "no baseline")
	}
	if fg.snapshotCalls != 0 {
		t.Fatalf("expected 0 SnapshotTree calls, got %d", fg.snapshotCalls)
	}
}

// roundDiffEndTreeCase is one scripted RoundDiff outcome for the EndTree
// contract. The table lives at package level so its closures do not count
// against the test function's length.
type roundDiffEndTreeCase struct {
	name               string
	git                func() *fakeGit
	nilGit             bool
	baseline           string
	wantEndTree        string
	wantAvailable      bool
	wantReasonNonEmpty bool
	assertCalls        func(t *testing.T, fg *fakeGit)
}

var roundDiffEndTreeCases = []roundDiffEndTreeCase{
	{
		name: "successful snapshot, successful diff -> EndTree set, Available true",
		git: func() *fakeGit {
			return &fakeGit{
				snapshotTreeID: "tree-end",
				diffResult: git.Diff{
					Stat: git.Stat{FilesChanged: 1, Insertions: 2, Deletions: 1},
				},
			}
		},
		baseline:           "tree-start",
		wantEndTree:        "tree-end",
		wantAvailable:      true,
		wantReasonNonEmpty: false,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.snapshotCalls != 1 {
				t.Fatalf("expected 1 SnapshotTree call, got %d", fg.snapshotCalls)
			}
			if fg.diffCalls != 1 {
				t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
			}
		},
	},
	{
		name: "successful snapshot, DiffTrees fails -> EndTree still set, Available false with a Reason",
		git: func() *fakeGit {
			return &fakeGit{
				snapshotTreeID: "tree-end",
				diffErr:        errors.New("boom: diff failed"),
			}
		},
		baseline:           "tree-start",
		wantEndTree:        "tree-end",
		wantAvailable:      false,
		wantReasonNonEmpty: true,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.snapshotCalls != 1 {
				t.Fatalf("expected 1 SnapshotTree call, got %d", fg.snapshotCalls)
			}
			if fg.diffCalls != 1 {
				t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
			}
		},
	},
	{
		name:               "d.Git == nil -> EndTree empty",
		nilGit:             true,
		baseline:           "tree-start",
		wantEndTree:        "",
		wantAvailable:      false,
		wantReasonNonEmpty: false,
	},
	{
		name: "b.RoundBaselineTree == \"\" -> EndTree empty, and no snapshot was attempted",
		git: func() *fakeGit {
			return &fakeGit{snapshotTreeID: "tree-end"}
		},
		baseline:           "",
		wantEndTree:        "",
		wantAvailable:      false,
		wantReasonNonEmpty: true,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.snapshotCalls != 0 {
				t.Fatalf("expected 0 SnapshotTree calls, got %d", fg.snapshotCalls)
			}
		},
	},
	{
		name: "SnapshotTree returns git.ErrNotRepo -> EndTree empty",
		git: func() *fakeGit {
			return &fakeGit{snapshotTreeErr: git.ErrNotRepo}
		},
		baseline:           "tree-start",
		wantEndTree:        "",
		wantAvailable:      false,
		wantReasonNonEmpty: false,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.snapshotCalls != 1 {
				t.Fatalf("expected 1 SnapshotTree call, got %d", fg.snapshotCalls)
			}
		},
	},
}

func TestRoundDiff_EndTree(t *testing.T) {
	t.Parallel()

	for _, tc := range roundDiffEndTreeCases {
		t.Run(tc.name, func(t *testing.T) {
			runRoundDiffEndTree(t, tc)
		})
	}
}

// runRoundDiffEndTree builds one roundDiffEndTreeCases row's world, calls
// RoundDiff under the store lock, and checks every field the row pins.
func runRoundDiffEndTree(t *testing.T, tc roundDiffEndTreeCase) {
	t.Helper()
	ctx := context.Background()
	s := store.New(t.TempDir())
	var fg *fakeGit
	var g Git
	if !tc.nilGit {
		if tc.git != nil {
			fg = tc.git()
		} else {
			fg = &fakeGit{}
		}
		g = fg
	}
	d := Deps{Store: s, Git: g}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: tc.baseline}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = RoundDiff(ctx, d, tx, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if res.EndTree != tc.wantEndTree {
		t.Fatalf("EndTree = %q, want %q", res.EndTree, tc.wantEndTree)
	}
	if res.Available != tc.wantAvailable {
		t.Fatalf("Available = %v, want %v", res.Available, tc.wantAvailable)
	}
	if tc.wantReasonNonEmpty && res.Reason == "" {
		t.Fatal("expected non-empty Reason")
	}
	if !tc.wantReasonNonEmpty && res.Reason != "" {
		t.Fatalf("expected empty Reason, got %q", res.Reason)
	}
	if tc.assertCalls != nil && fg != nil {
		tc.assertCalls(t, fg)
	}
}

// commitFactsCase is one scripted CommitFacts outcome. The table lives at
// package level so its expectations and closures do not count against the test
// function's length.
type commitFactsCase struct {
	name    string
	git     func() *fakeGit
	binding store.Binding
	want    CommitResult
	check   func(t *testing.T, fg *fakeGit)
}

var commitFactsCases = []commitFactsCase{
	{
		name:    "nil git",
		git:     func() *fakeGit { return nil },
		binding: store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineHead: "head-start"},
		want:    CommitResult{},
	},
	{
		name:    "no baseline head",
		git:     func() *fakeGit { return &fakeGit{headCommitID: "h"} },
		binding: store.Binding{Name: "webshop", CWD: "/repo", Round: 1},
		want:    CommitResult{Reason: "no baseline"},
		check: func(t *testing.T, fg *fakeGit) {
			if fg.headCalls != 0 || fg.revListCalls != 0 || fg.dirtyCalls != 0 {
				t.Fatalf("git was called without a baseline: %+v", fg)
			}
		},
	},
	{
		name:    "head fails",
		git:     func() *fakeGit { return &fakeGit{headCommitErr: errors.New("boom: head")} },
		binding: store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineHead: "head-start"},
		want:    CommitResult{Reason: "head: boom: head"},
		check: func(t *testing.T, fg *fakeGit) {
			if fg.revListCalls != 0 || fg.dirtyCalls != 0 {
				t.Fatalf("sequence did not stop at the first failure: %+v", fg)
			}
		},
	},
	{
		name:    "rev-list fails",
		git:     func() *fakeGit { return &fakeGit{headCommitID: "head-end", revListErr: errors.New("boom: rev-list")} },
		binding: store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineHead: "head-start"},
		want:    CommitResult{Reason: "rev-list: boom: rev-list"},
		check: func(t *testing.T, fg *fakeGit) {
			if fg.dirtyCalls != 0 {
				t.Fatalf("Dirty called after rev-list failed: %+v", fg)
			}
		},
	},
	{
		name: "dirty check fails",
		git: func() *fakeGit {
			return &fakeGit{headCommitID: "head-end", revListCount: 2, dirtyErr: errors.New("boom: status")}
		},
		binding: store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineHead: "head-start"},
		want:    CommitResult{Reason: "dirty check: boom: status"},
	},
	{
		name:    "not a repository is silent",
		git:     func() *fakeGit { return &fakeGit{headCommitErr: fmt.Errorf("%w: nope", git.ErrNotRepo)} },
		binding: store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineHead: "head-start"},
		want:    CommitResult{},
	},
	{
		name:    "all succeed",
		git:     func() *fakeGit { return &fakeGit{headCommitID: "head-end", revListCount: 3, dirtyResult: true} },
		binding: store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineHead: "head-start"},
		want:    CommitResult{Known: true, Commits: 3, Dirty: true},
		check: func(t *testing.T, fg *fakeGit) {
			if fg.lastRevListDir != "/repo" || fg.lastRevListFrom != "head-start" || fg.lastRevListTo != "head-end" {
				t.Fatalf("rev-list range: dir=%q from=%q to=%q", fg.lastRevListDir, fg.lastRevListFrom, fg.lastRevListTo)
			}
			if fg.lastDirtyDir != "/repo" {
				t.Fatalf("Dirty dir = %q, want /repo", fg.lastDirtyDir)
			}
		},
	},
}

func TestCommitFacts(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	for _, tc := range commitFactsCases {
		t.Run(tc.name, func(t *testing.T) {
			var fg *fakeGit
			var g Git
			if tc.git != nil {
				fg = tc.git()
				if fg != nil {
					g = fg
				}
			}
			got := CommitFacts(context.Background(), Deps{Store: s, Git: g}, tc.binding)
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if tc.check != nil && fg != nil {
				tc.check(t, fg)
			}
		})
	}
}

func TestDiffTextWithCommitFacts(t *testing.T) {
	t.Parallel()

	normal := DiffResult{Available: true, Path: "/p/007-diff.patch", Stat: git.Stat{FilesChanged: 6, Insertions: 120, Deletions: 30}}
	empty := DiffResult{Available: true}
	truncated := DiffResult{Available: true, Truncated: true, Stat: git.Stat{FilesChanged: 312, Insertions: 48120, Deletions: 9033}}
	unavailable := DiffResult{Available: false, Reason: "no baseline"}
	silent := DiffResult{Available: false}

	cases := []struct {
		name        string
		res         DiffResult
		facts       CommitResult
		branch      string
		wantSummary string
		wantLine    string
	}{
		{"commits clean with branch", normal, CommitResult{Known: true, Commits: 3}, "relevo/api-auth",
			"6 files, +120 -30; 3 commits, clean",
			"Diff: relevo show webshop --round 7 --diff (6 files, +120 -30) -- 3 commits on relevo/api-auth, tree clean"},
		{"one commit singular", normal, CommitResult{Known: true, Commits: 1}, "relevo/api-auth",
			"6 files, +120 -30; 1 commit, clean",
			"Diff: relevo show webshop --round 7 --diff (6 files, +120 -30) -- 1 commit on relevo/api-auth, tree clean"},
		{"commits dirty with branch", normal, CommitResult{Known: true, Commits: 3, Dirty: true}, "relevo/api-auth",
			"6 files, +120 -30; 3 commits, dirty",
			"Diff: relevo show webshop --round 7 --diff (6 files, +120 -30) -- 3 commits on relevo/api-auth, tree dirty"},
		{"commits clean without branch", normal, CommitResult{Known: true, Commits: 3}, "",
			"6 files, +120 -30; 3 commits, clean",
			"Diff: relevo show webshop --round 7 --diff (6 files, +120 -30) -- 3 commits, tree clean"},
		{"no commits dirty", normal, CommitResult{Known: true, Commits: 0, Dirty: true}, "relevo/api-auth",
			"6 files, +120 -30; no commits, dirty",
			"Diff: relevo show webshop --round 7 --diff (6 files, +120 -30) -- no commits; changes are uncommitted in the worktree"},
		{"no commits clean", normal, CommitResult{Known: true}, "relevo/api-auth",
			"6 files, +120 -30; no commits, clean",
			"Diff: relevo show webshop --round 7 --diff (6 files, +120 -30) -- no commits, tree clean"},
		{"unknown with reason", normal, CommitResult{Reason: "rev-list: boom"}, "relevo/api-auth",
			"6 files, +120 -30; commits unknown (rev-list: boom)",
			"Diff: relevo show webshop --round 7 --diff (6 files, +120 -30) -- commits unknown (rev-list: boom)"},
		{"unknown silent", normal, CommitResult{}, "relevo/api-auth",
			"6 files, +120 -30",
			"Diff: relevo show webshop --round 7 --diff (6 files, +120 -30)"},
		{"empty diff gains nothing", empty, CommitResult{Known: true, Commits: 2}, "relevo/api-auth",
			"no changes",
			"Diff: no file changes"},
		{"truncated keeps the clause", truncated, CommitResult{Known: true, Dirty: true}, "",
			"truncated; no commits, dirty",
			"Diff: 312 files, +48120 -9033 (patch omitted, over the 4 MiB cap) -- no commits; changes are uncommitted in the worktree"},
		{"unavailable keeps the clause", unavailable, CommitResult{Known: true, Commits: 2}, "relevo/api-auth",
			"unavailable: no baseline; 2 commits, clean",
			"Diff: unavailable (no baseline) -- 2 commits on relevo/api-auth, tree clean"},
		{"unavailable and unknown", unavailable, CommitResult{Reason: "no baseline"}, "",
			"unavailable: no baseline; commits unknown (no baseline)",
			"Diff: unavailable (no baseline) -- commits unknown (no baseline)"},
		{"silent diff stays silent", silent, CommitResult{Known: true, Commits: 2}, "relevo/api-auth",
			"unavailable; 2 commits, clean",
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DiffSummary(tc.res, tc.facts); got != tc.wantSummary {
				t.Errorf("DiffSummary = %q, want %q", got, tc.wantSummary)
			}
			if got := DiffLine(tc.res, tc.facts, tc.branch, "webshop", 7); got != tc.wantLine {
				t.Errorf("DiffLine = %q, want %q", got, tc.wantLine)
			}
		})
	}
}

// TestDiffLineFromNoteMatchesDiffLine checks DiffLineFromNote against
// DiffLine's own output for the same res/facts pair, over the note shapes
// where the wire facts (Note, Commits, Tree) carry everything DiffLine
// needs: an available diff always has a res.Path DiffLineFromNote never
// sees, so those cases are deliberately not compared here (see the
// function's doc comment).
func TestDiffLineFromNoteMatchesDiffLine(t *testing.T) {
	t.Parallel()

	unavailable := DiffResult{Available: false, Reason: "no baseline"}
	empty := DiffResult{Available: true}
	silent := DiffResult{Available: false}
	branch := "relevo/api-auth"

	cases := []struct {
		name  string
		res   DiffResult
		facts CommitResult
		tree  string
	}{
		// Mutation target: drop the trailing commitClause append for the
		// unavailable case and this stops matching DiffLine, which appends
		// it too.
		{"unavailable keeps the clause", unavailable, CommitResult{Known: true, Commits: 2}, "clean"},
		// Mutation target: treat "no changes" like the default case (append
		// a clause) and this starts differing from DiffLine, which returns
		// early with no clause on an empty diff.
		{"empty diff gains nothing", empty, CommitResult{Known: true, Commits: 2}, "clean"},
		// Mutation target: drop the "base == \"unavailable\"" short-circuit
		// and this returns a non-empty line instead of "", unlike DiffLine
		// on a result with no reason.
		{"silent diff stays silent", silent, CommitResult{Known: true, Commits: 2}, "clean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note := DiffSummary(tc.res, tc.facts)
			want := DiffLine(tc.res, tc.facts, branch, "webshop", 7)
			if got := DiffLineFromNote(note, tc.facts.Commits, tc.tree, branch); got != want {
				t.Fatalf("DiffLineFromNote(%q, %d, %q, %q) = %q, want %q (DiffLine's own output for the same facts)",
					note, tc.facts.Commits, tc.tree, branch, got, want)
			}
		})
	}
}

// TestPathsLine pins the payload line for a changed_paths mismatch: its exact
// wording, and both plural forms formatFiles renders.
func TestPathsLine(t *testing.T) {
	t.Parallel()

	if got, want := PathsLine(0, 24), "Paths: the report's changed_paths lists 0, the diff has 24 files -- check the diff, not the list"; got != want {
		t.Errorf("PathsLine(0, 24) = %q, want %q", got, want)
	}
	if got, want := PathsLine(2, 1), "Paths: the report's changed_paths lists 2, the diff has 1 file -- check the diff, not the list"; got != want {
		t.Errorf("PathsLine(2, 1) = %q, want %q", got, want)
	}
	if !strings.HasSuffix(PathsLine(2, 1), "1 file -- check the diff, not the list") {
		t.Errorf("PathsLine(2, 1) = %q, want it to end '1 file -- check the diff, not the list'", PathsLine(2, 1))
	}
}

// TestPathsLineFromNote pins the reading of the clause out of a KindDiff
// note, including the position the note leaves it in and the no-clause cases
// that must stay silent.
func TestPathsLineFromNote(t *testing.T) {
	t.Parallel()

	joined := "24 files, +1 -2; 1 commit on relevo/x, tree clean paths: report 0, diff 24"
	if got, want := PathsLineFromNote(joined), PathsLine(0, 24); got != want {
		t.Errorf("PathsLineFromNote(%q) = %q, want %q", joined, got, want)
	}
	if got := PathsLineFromNote("3 files, +1 -1"); got != "" {
		t.Errorf("PathsLineFromNote(%q) = %q, want \"\"", "3 files, +1 -1", got)
	}
	if got := PathsLineFromNote(""); got != "" {
		t.Errorf("PathsLineFromNote(\"\") = %q, want \"\"", got)
	}
}

// TestPathsClauseReMatchesReconcileFormat pins pathsClauseRe against the exact
// format reconcile renders the clause with, "paths: report %d, diff %d": if
// either side changes shape, the two counts stop being read back.
func TestPathsClauseReMatchesReconcileFormat(t *testing.T) {
	t.Parallel()

	// The literal below must stay identical to the format string reconcile
	// passes when it appends the mismatch clause.
	note := "24 files, +1 -2; 1 commit on relevo/x, tree clean " +
		fmt.Sprintf("paths: report %d, diff %d", 2, 3)
	if got, want := PathsLineFromNote(note), PathsLine(2, 3); got != want {
		t.Fatalf("PathsLineFromNote(%q) = %q, want %q", note, got, want)
	}
}
