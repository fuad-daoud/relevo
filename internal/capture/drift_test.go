package capture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// driftCase is one scripted Drift outcome. The table lives at package level so
// its closures do not count against the test function's length.
type driftCase struct {
	name               string
	nilGit             bool
	git                func() *fakeGit
	binding            store.Binding
	baseline           string
	setupStore         func(t *testing.T, s *store.Store, b store.Binding)
	wantAvailable      bool
	wantStat           git.Stat
	wantTruncated      bool
	wantPath           bool // whether Path should match DriftPath
	wantReasonNonEmpty bool
	wantReason         string
	assertCalls        func(t *testing.T, fg *fakeGit)
}

var driftCases = []driftCase{
	// 1. d.Git == nil, b.RoundClosedTree == "", or baseline == ""
	{
		name:   "1a. d.Git == nil -> Available: false, no Reason, no git call",
		nilGit: true,
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "tree-closed",
		},
		baseline:           "tree-base",
		wantAvailable:      false,
		wantReasonNonEmpty: false,
	},
	{
		name: "1b. b.RoundClosedTree == \"\" -> Available: false, no Reason, 0 DiffTrees calls",
		git: func() *fakeGit {
			return &fakeGit{}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "",
		},
		baseline:           "tree-base",
		wantAvailable:      false,
		wantReasonNonEmpty: false,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 0 {
				t.Fatalf("expected 0 DiffTrees calls, got %d", fg.diffCalls)
			}
		},
	},
	{
		name: "1c. baseline == \"\" -> Available: false, no Reason, 0 DiffTrees calls",
		git: func() *fakeGit {
			return &fakeGit{}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "tree-closed",
		},
		baseline:           "",
		wantAvailable:      false,
		wantReasonNonEmpty: false,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 0 {
				t.Fatalf("expected 0 DiffTrees calls, got %d", fg.diffCalls)
			}
		},
	},
	// 2. baseline == b.RoundClosedTree -> Available: true, zero Stat, 0 DiffTrees calls
	{
		name: "2. baseline == b.RoundClosedTree -> Available: true, zero Stat, 0 DiffTrees calls",
		git: func() *fakeGit {
			return &fakeGit{}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "same-tree",
		},
		baseline:           "same-tree",
		wantAvailable:      true,
		wantStat:           git.Stat{},
		wantReasonNonEmpty: false,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 0 {
				t.Fatalf("expected 0 DiffTrees calls, got %d", fg.diffCalls)
			}
		},
	},
	// 3. DiffTrees returns git.ErrNotRepo -> Available: false, empty Reason
	{
		name: "3. DiffTrees returns ErrNotRepo -> Available: false, empty Reason",
		git: func() *fakeGit {
			return &fakeGit{diffErr: git.ErrNotRepo}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "tree-closed",
		},
		baseline:           "tree-base",
		wantAvailable:      false,
		wantReasonNonEmpty: false,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 1 {
				t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
			}
		},
	},
	// 4. DiffTrees returns any other error -> Available: false, Reason: brief(err)
	{
		name: "4. DiffTrees returns any other error -> Available: false, Reason: brief(err)",
		git: func() *fakeGit {
			return &fakeGit{diffErr: errors.New("boom: git diff error\nextra line")}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "tree-closed",
		},
		baseline:           "tree-base",
		wantAvailable:      false,
		wantReasonNonEmpty: true,
		wantReason:         "boom: git diff error",
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 1 {
				t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
			}
		},
	},
	// 5. empty stat -> Available: true, Stat set, no Path
	{
		name: "5. empty stat -> Available: true, Stat set, no Path",
		git: func() *fakeGit {
			return &fakeGit{
				diffResult: git.Diff{
					Stat: git.Stat{FilesChanged: 0},
				},
			}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "tree-closed",
		},
		baseline:      "tree-base",
		wantAvailable: true,
		wantStat:      git.Stat{FilesChanged: 0},
		wantPath:      false,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 1 {
				t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
			}
		},
	},
	// 6. truncated -> Available: true, Stat exact, Truncated: true, no Path
	{
		name: "6. truncated -> Available: true, Stat exact, Truncated: true, no Path",
		git: func() *fakeGit {
			return &fakeGit{
				diffResult: git.Diff{
					Stat:      git.Stat{FilesChanged: 50, Insertions: 500, Deletions: 100},
					Truncated: true,
				},
			}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "tree-closed",
		},
		baseline:      "tree-base",
		wantAvailable: true,
		wantStat:      git.Stat{FilesChanged: 50, Insertions: 500, Deletions: 100},
		wantTruncated: true,
		wantPath:      false,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 1 {
				t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
			}
		},
	},
	// 7. otherwise write patch to DriftPath(b.Name, b.Round) -> Available: true, Path set
	{
		name: "7. normal diff -> Available: true, Stat exact, Path set",
		git: func() *fakeGit {
			return &fakeGit{
				diffResult: git.Diff{
					Stat:  git.Stat{FilesChanged: 3, Insertions: 40, Deletions: 2},
					Patch: []byte("--- a/f\n+++ b/f\n@@ ...\n"),
				},
			}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           5,
			RoundClosedTree: "tree-closed",
		},
		baseline: "tree-base",
		setupStore: func(t *testing.T, s *store.Store, b store.Binding) {
			if err := s.Save(b); err != nil {
				t.Fatal(err)
			}
		},
		wantAvailable: true,
		wantStat:      git.Stat{FilesChanged: 3, Insertions: 40, Deletions: 2},
		wantPath:      true,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 1 {
				t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
			}
		},
	},
	// 8. WriteFile failure -> Available: false, Reason: brief(err)
	{
		name: "8. WriteFile failure -> Available: false, Reason: brief(err)",
		git: func() *fakeGit {
			return &fakeGit{
				diffResult: git.Diff{
					Stat:  git.Stat{FilesChanged: 1, Insertions: 1, Deletions: 0},
					Patch: []byte("patch content"),
				},
			}
		},
		binding: store.Binding{
			Name:            "webshop",
			CWD:             "/repo",
			Round:           2,
			RoundClosedTree: "tree-closed",
		},
		baseline: "tree-base",
		setupStore: func(t *testing.T, s *store.Store, b store.Binding) {
			// Create a file where the binding directory would be so os.WriteFile fails
			dirPath := s.Dir(b.Name)
			if err := os.WriteFile(dirPath, []byte("not a directory"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		wantAvailable:      false,
		wantReasonNonEmpty: true,
		assertCalls: func(t *testing.T, fg *fakeGit) {
			if fg.diffCalls != 1 {
				t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
			}
		},
	},
}

func TestDrift_Cases(t *testing.T) {
	t.Parallel()

	for _, tc := range driftCases {
		t.Run(tc.name, func(t *testing.T) {
			runDriftCase(t, tc)
		})
	}
}

// runDriftCase builds one driftCases row's world, calls Drift under the store
// lock, and checks every field the row pins.
func runDriftCase(t *testing.T, tc driftCase) {
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

	if tc.setupStore != nil {
		tc.setupStore(t, s, tc.binding)
	}

	var res DriftResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = Drift(ctx, d, tx, tc.binding, tc.baseline)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if res.Available != tc.wantAvailable {
		t.Fatalf("Available = %v, want %v", res.Available, tc.wantAvailable)
	}
	if res.Stat != tc.wantStat {
		t.Fatalf("Stat = %+v, want %+v", res.Stat, tc.wantStat)
	}
	if res.Truncated != tc.wantTruncated {
		t.Fatalf("Truncated = %v, want %v", res.Truncated, tc.wantTruncated)
	}
	if tc.wantPath {
		wantPath := s.DriftPath(tc.binding.Name, tc.binding.Round)
		if res.Path != wantPath {
			t.Fatalf("Path = %q, want %q", res.Path, wantPath)
		}
		data, err := s.ReadFile(wantPath)
		if err != nil {
			t.Fatalf("failed to read written drift patch: %v", err)
		}
		if string(data) != "--- a/f\n+++ b/f\n@@ ...\n" {
			t.Fatalf("unexpected drift patch content: %s", string(data))
		}
		if _, err := os.Stat(wantPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("os.Stat(%s) = %v, want ErrNotExist (no file on disk)", wantPath, err)
		}
	} else if res.Path != "" {
		t.Fatalf("Path = %q, want empty", res.Path)
	}
	if tc.wantReasonNonEmpty && res.Reason == "" {
		t.Fatal("expected non-empty Reason")
	}
	if !tc.wantReasonNonEmpty && res.Reason != "" {
		t.Fatalf("expected empty Reason, got %q", res.Reason)
	}
	if tc.wantReason != "" && res.Reason != tc.wantReason {
		t.Fatalf("Reason = %q, want %q", res.Reason, tc.wantReason)
	}
	if tc.assertCalls != nil && fg != nil {
		tc.assertCalls(t, fg)
	}
}

func TestDrift_DiffTreesArgOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 1, Insertions: 2, Deletions: 0},
			Patch: []byte("patch"),
		},
	}
	d := Deps{Store: s, Git: fg}
	b := store.Binding{
		Name:            "webshop",
		CWD:             "/repo",
		Round:           3,
		RoundClosedTree: "tree-round-2-closed",
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	baseline := "tree-round-3-opening"
	var res DriftResult
	if err := s.WithLock(func(tx *store.Tx) error {
		res = Drift(ctx, d, tx, b, baseline)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !res.Available {
		t.Fatalf("expected Available=true, got Reason=%q", res.Reason)
	}

	// Diff direction must be: DiffTrees(ctx, b.CWD, b.RoundClosedTree, baseline)
	if fg.lastDiffDir != b.CWD {
		t.Fatalf("DiffTrees dir = %q, want %q", fg.lastDiffDir, b.CWD)
	}
	if fg.lastDiffFrom != b.RoundClosedTree {
		t.Fatalf("DiffTrees from = %q, want %q (b.RoundClosedTree)", fg.lastDiffFrom, b.RoundClosedTree)
	}
	if fg.lastDiffTo != baseline {
		t.Fatalf("DiffTrees to = %q, want %q (baseline)", fg.lastDiffTo, baseline)
	}
}

// driftRendererCase is one DriftSummary/DriftLine golden.
type driftRendererCase struct {
	name        string
	res         DriftResult
	round       int
	wantSummary string
	wantLine    string
}

var driftRendererCases = []driftRendererCase{
	{
		name:        "unavailable with empty reason",
		res:         DriftResult{Available: false, Reason: ""},
		round:       5,
		wantSummary: "unavailable",
		wantLine:    "",
	},
	{
		name:        "unavailable with reason",
		res:         DriftResult{Available: false, Reason: "boom: git broken"},
		round:       5,
		wantSummary: "unavailable: boom: git broken",
		wantLine:    "drift: unavailable (boom: git broken)",
	},
	{
		name:        "empty stat / equal trees / no drift",
		res:         DriftResult{Available: true, Stat: git.Stat{FilesChanged: 0}},
		round:       5,
		wantSummary: "no drift",
		wantLine:    "",
	},
	{
		name: "multi-file drift with patch",
		res: DriftResult{
			Available: true,
			Path:      "/home/u/.local/state/relevo/webshop/005-drift.patch",
			Stat:      git.Stat{FilesChanged: 3, Insertions: 40, Deletions: 2},
		},
		round:       5,
		wantSummary: "3 files, +40 -2",
		wantLine: "drift: 3 files, +40 -2 between round 4's report and this send\n" +
			"       relevo show webshop --round 5 --drift",
	},
	{
		name: "single file drift with patch",
		res: DriftResult{
			Available: true,
			Path:      "/home/u/.local/state/relevo/webshop/002-drift.patch",
			Stat:      git.Stat{FilesChanged: 1, Insertions: 12, Deletions: 3},
		},
		round:       2,
		wantSummary: "1 file, +12 -3",
		wantLine: "drift: 1 file, +12 -3 between round 1's report and this send\n" +
			"       relevo show webshop --round 2 --drift",
	},
	{
		name: "truncated diff",
		res: DriftResult{
			Available: true,
			Stat:      git.Stat{FilesChanged: 312, Insertions: 48120, Deletions: 9033},
			Truncated: true,
		},
		round:       5,
		wantSummary: "truncated",
		wantLine: "drift: 312 files, +48120 -9033 between round 4's report and this send\n" +
			"       (patch omitted, over the 4 MiB cap)",
	},
}

func TestDriftRenderers_Goldens(t *testing.T) {
	t.Parallel()

	for _, tc := range driftRendererCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DriftSummary(tc.res); got != tc.wantSummary {
				t.Errorf("DriftSummary = %q, want %q", got, tc.wantSummary)
			}
			if got := DriftLine(tc.res, "webshop", tc.round); got != tc.wantLine {
				t.Errorf("DriftLine = %q, want %q", got, tc.wantLine)
			}
		})
	}
}

func TestReadDrift(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())

	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 5}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	patchPath := s.DriftPath("webshop", 5)
	patchContent := "--- a/file\n+++ b/file\n@@ ...\n"
	if err := os.WriteFile(patchPath, []byte(patchContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Present
	data, ok, err := ReadDrift(s, "webshop", 5)
	if err != nil {
		t.Fatalf("ReadDrift present returned err: %v", err)
	}
	if !ok {
		t.Fatal("ReadDrift present returned ok=false")
	}
	if string(data) != patchContent {
		t.Fatalf("ReadDrift present got %q, want %q", string(data), patchContent)
	}

	// 2. Absent (round has no drift file)
	data, ok, err = ReadDrift(s, "webshop", 4)
	if err != nil {
		t.Fatalf("ReadDrift absent returned err: %v", err)
	}
	if ok {
		t.Fatal("ReadDrift absent returned ok=true")
	}
	if data != nil {
		t.Fatalf("ReadDrift absent returned non-nil data: %v", data)
	}

	// 3. Unknown binding
	_, _, err = ReadDrift(s, "nonexistent", 5)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ReadDrift unknown binding returned err %v, want ErrNotFound", err)
	}

	// 4. Wrapped read error (e.g. drift path is an unreadable directory)
	unreadableDir := s.DriftPath("webshop", 6)
	if err := os.Mkdir(unreadableDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err = ReadDrift(s, "webshop", 6)
	if err == nil {
		t.Fatal("expected error reading directory as file")
	}
	if filepath.Base(unreadableDir) != "006-drift.patch" {
		t.Fatalf("unexpected path: %s", unreadableDir)
	}
}
