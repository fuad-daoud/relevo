package relevo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// roundReq is the request a round's start makes: the binding it spawns for,
// its name as key, and the tree the process will run in.
func roundReq(b *store.Binding) procRequest {
	return procRequest{kind: procKindRound, key: b.Name, tree: "/repo", binding: b}
}

func TestPrepareProcCreatesRoundDir(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()
	b := store.Binding{Name: "webshop", Round: 1}

	dir, env, cleanup, err := prepareProc(context.Background(), rt, roundReq(&b))
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	want := filepath.Join(rt.TmpRoot, "rounds", "webshop-001")
	if dir != want {
		t.Errorf("tmpDir = %q, want %q", dir, want)
	}
	if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
		t.Fatalf("the round's temp dir %s does not exist: %v", dir, serr)
	}
	if env != nil {
		t.Errorf("env = %v, want none", env)
	}
	cleanup()
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Errorf("the cleanup left %s behind", dir)
	}
}

// TestPrepareProcNoRootIsNoop pins the no-tmp-root contract: nothing is
// returned to set on the spec, nothing errors, and the cleanup is safe to
// call anyway -- container mode and every test runtime land here.
func TestPrepareProcNoRootIsNoop(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := store.Binding{Name: "webshop", Round: 1}

	dir, env, cleanup, err := prepareProc(context.Background(), rt, roundReq(&b))
	if err != nil {
		t.Fatalf("prepareProc with no TmpRoot: %v", err)
	}
	if dir != "" {
		t.Errorf("tmpDir = %q, want empty", dir)
	}
	if env != nil {
		t.Errorf("env = %v, want nil", env)
	}
	cleanup()
}

// TestPrepareProcCheckKindCreatesNothing pins this plan's half of the seam:
// checks are requests like rounds but the only thing prepared today is a
// round's directory, so a check start still runs on the inherited temp dir.
func TestPrepareProcCheckKindCreatesNothing(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()

	dir, env, cleanup, err := prepareProc(context.Background(), rt, procRequest{
		kind: procKindCheck, key: "check-1", tree: "/repo",
	})
	if err != nil {
		t.Fatalf("prepareProc(check): %v", err)
	}
	if dir != "" || env != nil {
		t.Errorf("dir/env = %q/%v, want nothing for a check", dir, env)
	}
	cleanup()
	if entries, lerr := os.ReadDir(filepath.Join(rt.TmpRoot, "rounds")); lerr == nil && len(entries) > 0 {
		t.Errorf("a check prepared %d round dirs", len(entries))
	}
}

// TestPrepareProcRereadsTheRoundDir pins reuse: a relaunch passes the same
// binding and gets the same directory back, not a fresh-named one, which is
// what lets a resume keep pointing TMPDIR at the round's dir.
func TestPrepareProcRereadsTheRoundDir(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()
	b := store.Binding{Name: "webshop", Round: 4}

	first, _, _, err := prepareProc(context.Background(), rt, roundReq(&b))
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(first, "scratch"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, _, _, err := prepareProc(context.Background(), rt, roundReq(&b))
	if err != nil {
		t.Fatalf("prepareProc again: %v", err)
	}
	if second != first {
		t.Errorf("relaunch dir = %q, want the first dir %q", second, first)
	}
	if _, serr := os.Stat(filepath.Join(second, "scratch")); serr != nil {
		t.Errorf("EnsureTmpEntry disturbed the round dir: %v", serr)
	}
}

// TestRoundTmpRelPadsLikeScratchPaths pins the naming rule against the
// store's own per-round worktree path: same zero-padding, so one round's
// worktree and one round's temp dir carry the same NNN without a second
// format to keep in step.
func TestRoundTmpRelPadsLikeScratchPaths(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	for _, tc := range []struct {
		name  string
		round int
	}{{"api", 7}, {"webshop", 1}, {"x", 42}, {"y", 1000}} {
		want := filepath.Join("rounds", filepath.Base(st.ScratchWorktreePath(tc.name, tc.round)))
		if got := roundTmpRel(tc.name, tc.round); got != want {
			t.Errorf("roundTmpRel(%q, %d) = %q, want %q", tc.name, tc.round, got, want)
		}
	}
}

// TestRemoveRoundTmpWithoutRootRemovesNothing pins the pairing with
// prepareProc: a runtime that named no tmp root never made a directory, and
// asking it to remove one touches nothing.
func TestRemoveRoundTmpWithoutRootRemovesNothing(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	removeRoundTmp(rt, store.Binding{Name: "webshop", Round: 1}, 1)
}

// TestRemoveRoundTmpMissingDirIsSilent pins the route-sharing shape: the
// close and a kill on the same route both reach this hook, and the second
// removal of an already-gone directory is success, not a warning.
func TestRemoveRoundTmpMissingDirIsSilent(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()
	b := store.Binding{Name: "webshop", Round: 1}

	dir, _, _, err := prepareProc(context.Background(), rt, roundReq(&b))
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	removeRoundTmp(rt, b, 1)
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Fatalf("the first removal left %s behind", dir)
	}
	removeRoundTmp(rt, b, 1)
}
