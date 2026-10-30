package relevo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

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
