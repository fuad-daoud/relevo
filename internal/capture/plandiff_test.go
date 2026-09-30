package capture

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestPlanDiffWritesThePatchAndComparesStartWithEnd pins the happy path: the
// diff runs from the spec's From to its End, and the patch lands at the spec's
// round_file key, never on disk.
func TestPlanDiffWritesThePatchAndComparesStartWithEnd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{diffResult: git.Diff{
		Stat:  git.Stat{FilesChanged: 3, Insertions: 40, Deletions: 2},
		Patch: []byte("--- a/x\n+++ b/x\n@@ ...\n"),
	}}
	d := Deps{Store: s, Git: fg}
	if err := s.Save(store.Binding{Name: "webshop", CWD: "/repo", Round: 1}); err != nil {
		t.Fatal(err)
	}

	path := s.PlanDiffPath("webshop", 1)
	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = PlanDiff(ctx, d, tx, PlanDiffSpec{
			Name: "webshop", Round: 1, Dir: "/repo", From: "start-1", End: "end-9", Path: path,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if !res.Available || res.Path != path {
		t.Fatalf("result = %+v, want Available at %q", res, path)
	}
	if res.Stat.FilesChanged != 3 {
		t.Errorf("Stat = %+v, want the diff's stat", res.Stat)
	}
	if fg.lastDiffFrom != "start-1" || fg.lastDiffTo != "end-9" {
		t.Errorf("DiffTrees(%q, %q), want the spec's (start-1, end-9)", fg.lastDiffFrom, fg.lastDiffTo)
	}
	if fg.snapshotCalls != 0 {
		t.Errorf("SnapshotTree called %d times, want 0: PlanDiff takes its trees", fg.snapshotCalls)
	}
	if _, err := s.ReadFile(path); err != nil {
		t.Fatalf("round_file %s missing: %v", path, err)
	}
	data, err := s.ReadFile(path)
	if err != nil || string(data) != "--- a/x\n+++ b/x\n@@ ...\n" {
		t.Fatalf("stored patch = %q (err %v), want the diff's patch", data, err)
	}
}

// TestPlanDiffReportsUnavailableWithoutBothEnds pins the guard: an empty From
// or End is unavailable with the same reason RoundDiff uses, and no git call is
// made at all.
func TestPlanDiffReportsUnavailableWithoutBothEnds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{}
	d := Deps{Store: s, Git: fg}

	for _, tc := range []struct{ name, from, end string }{
		{"no from", "", "end"},
		{"no end", "start", ""},
		{"neither", "", ""},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var res DiffResult
			if err := s.WithLock(func(tx *store.Tx) error {
				res = PlanDiff(ctx, d, tx, PlanDiffSpec{Name: "webshop", Round: 1, Dir: "/repo", From: tc.from, End: tc.end, Path: "/p"})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if res.Available {
				t.Errorf("result = %+v, want Available=false", res)
			}
			if res.Reason != "no baseline" {
				t.Errorf("Reason = %q, want %q", res.Reason, "no baseline")
			}
		})
	}
	if fg.diffCalls != 0 {
		t.Errorf("DiffTrees called %d times, want 0 with no baseline", fg.diffCalls)
	}
}

// TestPlanDiffIsSilentWithoutGit pins the no-git case: no git means nothing to
// say, exactly as RoundDiff's zero result is.
func TestPlanDiffIsSilentWithoutGit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	var res DiffResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = PlanDiff(ctx, Deps{Store: s}, tx, PlanDiffSpec{Name: "webshop", Round: 1, Dir: "/repo", From: "a", End: "b", Path: "/p"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if res.Available || res.Reason != "" {
		t.Errorf("result = %+v, want a silent unavailable", res)
	}
}

// TestPlanDiffRefusesAMissingPathOrRecord pins PlanDiff's two non-git failure
// landings: a diff with no round_file key to store it at, and a record the
// round_file row cannot be written for.
func TestPlanDiffRefusesAMissingPathOrRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	if err := s.Save(store.Binding{Name: "webshop", CWD: "/repo", Round: 1}); err != nil {
		t.Fatal(err)
	}
	d := Deps{Store: s, Git: &fakeGit{diffResult: git.Diff{Stat: git.Stat{FilesChanged: 1}, Patch: []byte("p")}}}

	run := func(spec PlanDiffSpec) DiffResult {
		var res DiffResult
		if err := s.WithLock(func(tx *store.Tx) error {
			res = PlanDiff(ctx, d, tx, spec)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := run(PlanDiffSpec{Name: "webshop", Round: 1, Dir: "/repo", From: "a", End: "b"}); res.Available || res.Reason != "no path" {
		t.Errorf("no path: %+v, want Available=false with the no-path reason", res)
	}
	if res := run(PlanDiffSpec{Name: "ghost", Round: 1, Dir: "/repo", From: "a", End: "b", Path: s.PlanDiffPath("webshop", 1)}); res.Available || res.Reason == "" {
		t.Errorf("missing record: %+v, want Available=false with a reason", res)
	}
}

// TestPlanDiffNeverFailsTheCaller pins the no-error contract on its other
// outcomes: a git failure carries a reason, an empty diff and a truncated one
// are Available with no patch, and a missing record is a reason rather than a
// panic.
func TestPlanDiffNeverFailsTheCaller(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	if err := s.Save(store.Binding{Name: "webshop", CWD: "/repo", Round: 1}); err != nil {
		t.Fatal(err)
	}
	spec := PlanDiffSpec{Name: "webshop", Round: 1, Dir: "/repo", From: "a", End: "b", Path: s.PlanDiffPath("webshop", 1)}

	call := func(d Deps) DiffResult {
		var res DiffResult
		if err := s.WithLock(func(tx *store.Tx) error {
			res = PlanDiff(ctx, d, tx, spec)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := call(Deps{Store: s, Git: &fakeGit{diffErr: git.ErrNotRepo}}); res.Available || res.Reason != "" {
		t.Errorf("not a repo: %+v, want a silent unavailable", res)
	}
	if res := call(Deps{Store: s, Git: &fakeGit{diffErr: errors.New("boom")}}); res.Available || res.Reason != "boom" {
		t.Errorf("git failure: %+v, want Available=false with the brief reason", res)
	}
	if res := call(Deps{Store: s, Git: &fakeGit{diffResult: git.Diff{Stat: git.Stat{FilesChanged: 0}}}}); !res.Available || res.Path != "" {
		t.Errorf("empty diff: %+v, want Available with no patch", res)
	}
	if res := call(Deps{Store: s, Git: &fakeGit{diffResult: git.Diff{Stat: git.Stat{FilesChanged: 2}, Truncated: true}}}); !res.Available || !res.Truncated || res.Path != "" {
		t.Errorf("truncated: %+v, want Available+Truncated with no patch", res)
	}
	if res := call(Deps{Store: s, Git: &fakeGit{diffResult: git.Diff{Stat: git.Stat{FilesChanged: 2}, Patch: []byte("p")}}}); !res.Available || res.Path != spec.Path {
		t.Errorf("normal: %+v, want Available at %q", res, spec.Path)
	}
}
