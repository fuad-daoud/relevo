package relevo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestNormalizeSubdir(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"a plain segment", "src/api", "src/api"},
		{"git's trailing slash dropped", "src/", "src"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSubdir(tc.in)
			if err != nil {
				t.Fatalf("NormalizeSubdir(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeSubdir(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	refusals := []struct{ name, in string }{
		{"absolute", "/abs"},
		{"parent segment", "a/../b"},
		{"a dot segment", "a/./b"},
		{"the root itself", "/"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSubdir(tc.in)
			if err == nil {
				t.Fatalf("NormalizeSubdir(%q) = %q, want a refusal", tc.in, got)
			}
		})
	}
}

// TestRoundDir is the join itself: the tree plus its subdirectory, and the
// tree alone when the binding has none. Returning CWD as-is is the mutation
// every joined test below catches too.
func TestRoundDir(t *testing.T) {
	t.Parallel()

	under := store.Binding{Name: "a", CWD: "/trees/api", Subdir: "src/api"}
	if got, want := RoundDir(under), "/trees/api/src/api"; got != want {
		t.Errorf("RoundDir = %q, want %q", got, want)
	}
	plain := store.Binding{Name: "a", CWD: "/trees/api"}
	if got, want := RoundDir(plain), "/trees/api"; got != want {
		t.Errorf("RoundDir without a subdir = %q, want the tree itself", got)
	}
}

// TestRoundTreeKeepsTheRootUntouched pins the root case: no subdirectory, the
// tree itself, unchanged. A join that turned an empty prefix into "./" would
// leave a different string.
func TestRoundTreeKeepsTheRootUntouched(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := store.Binding{Name: "a", CWD: "/tree"}
	if got := roundTree(rt, b); got != "/tree" {
		t.Errorf("roundTree = %q, want the tree itself", got)
	}
}

// TestReaderRoundRunsInTheScratchSubdirectory pins the reader side of the
// round's directory: the throwaway worktree plus the same subdirectory. A
// join left on the writer side only would put the scratch round at the
// worktree's root.
func TestReaderRoundRunsInTheScratchSubdirectory(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := store.Binding{Name: "rdr", CWD: "/tree", Shape: store.ShapeReader, Round: 3, Subdir: "src/api"}
	want := filepath.Join(rt.Store.ScratchWorktreePath("rdr", 3), "src", "api")
	if got := roundTree(rt, b); got != want {
		t.Errorf("roundTree = %q, want %q", got, want)
	}
}

// TestGateRunsInTheRoundDirectory pins the gate's directory on the same rule:
// the gate process of a bind started below the root runs in the tree plus its
// subdirectory. A spec that named b.CWD would check the round's files at the
// tree root instead.
func TestGateRunsInTheRoundDirectory(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	fr := newFakeRunner()
	rt.Runner = fr
	b.Subdir = "src/api"
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, _, _, err := gateStep(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("gateStep: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	if got, want := fr.specs[0].Dir, filepath.Join(b.CWD, b.Subdir); got != want {
		t.Errorf("gate Dir = %q, want the round's directory %q", got, want)
	}
}
