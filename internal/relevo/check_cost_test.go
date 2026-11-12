package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// foldCall is one FoldCheckCost call the stub observed.
type foldCall struct {
	binding store.Binding
	round   int
	rusage  *store.Rusage
}

// settleChainCheck starts a chain check, exits it clean, and advances it to
// its settle, through a stub fold seam. A non-nil trailer is what the fake
// runner reports for the check's process.
func settleChainCheck(t *testing.T, trailer *spawn.ProcRusage) ([]foldCall, string) {
	t.Helper()
	rt, fr, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 0)
	if trailer != nil {
		fr.setRusage(pid, *trailer)
	}
	var calls []foldCall
	rt.FoldCheckCost = func(b store.Binding, closedRound int, r *store.Rusage) {
		calls = append(calls, foldCall{binding: b, round: closedRound, rusage: r})
	}
	result, _ := chainAdvanceCheckForTest(t, rt, c, run)
	return calls, result
}

// TestFoldHelpersSkipWithoutSeamMemberOrServe pins the fold helpers'
// guards: an unset seam folds nothing, a chain with no member to key to
// folds nothing, a member the store no longer names folds nothing, and a
// served binding with no serve facts folds nothing.
func TestFoldHelpersSkipWithoutSeamMemberOrServe(t *testing.T) {
	t.Parallel()

	t.Run("no seam folds nothing", func(t *testing.T) {
		t.Parallel()
		rt, _, c := chainCheckFixture(t)
		if rt.FoldCheckCost != nil {
			t.Fatal("fixture FoldCheckCost set, want the local nil")
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			foldChainCheckCost(rt, tx, c, &store.Rusage{BurstBytes: int64ptr(3 << 30)})
			foldServedCheckCost(rt, store.Binding{Name: "shop"}, &store.Rusage{BurstBytes: int64ptr(3 << 30)})
			return nil
		}); err != nil {
			t.Fatalf("WithLock: %v", err)
		}
	})

	t.Run("no member folds nothing", func(t *testing.T) {
		t.Parallel()
		rt, _, _ := chainCheckFixture(t)
		var calls []foldCall
		rt.FoldCheckCost = func(b store.Binding, closedRound int, r *store.Rusage) {
			calls = append(calls, foldCall{binding: b, round: closedRound, rusage: r})
		}
		bare := db.ChainRow{ID: db.NewID(), Name: "bare", Status: "running", Phase: "build", Step: "building", Plan: 1, Plans: 1}
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			if err := tx.ChainPut(bare); err != nil {
				return err
			}
			foldChainCheckCost(rt, tx, bare, &store.Rusage{BurstBytes: int64ptr(3 << 30)})
			return nil
		}); err != nil {
			t.Fatalf("WithLock: %v", err)
		}
		if len(calls) != 0 {
			t.Fatalf("fold calls = %d, want none for a memberless chain", len(calls))
		}
	})

	t.Run("gone member folds nothing", func(t *testing.T) {
		t.Parallel()
		rt, _, c := chainCheckFixture(t)
		var calls []foldCall
		rt.FoldCheckCost = func(b store.Binding, closedRound int, r *store.Rusage) {
			calls = append(calls, foldCall{binding: b, round: closedRound, rusage: r})
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			if err := tx.Delete(c.Builder); err != nil {
				return err
			}
			foldChainCheckCost(rt, tx, c, &store.Rusage{BurstBytes: int64ptr(3 << 30)})
			return nil
		}); err != nil {
			t.Fatalf("WithLock: %v", err)
		}
		if len(calls) != 0 {
			t.Fatalf("fold calls = %d, want none for a deleted member", len(calls))
		}
	})

	t.Run("no serve facts folds nothing", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		var calls []foldCall
		rt.FoldCheckCost = func(b store.Binding, closedRound int, r *store.Rusage) {
			calls = append(calls, foldCall{binding: b, round: closedRound, rusage: r})
		}
		foldServedCheckCost(rt, store.Binding{Name: "local"}, &store.Rusage{BurstBytes: int64ptr(3 << 30)})
		if len(calls) != 0 {
			t.Fatalf("fold calls = %d, want none without serve facts", len(calls))
		}
	})
}

func int64ptr(v int64) *int64 { return &v }

// TestChainCheckSettleFoldsTheTrailer pins that a chain check settling with
// a rusage trailer folds it through the seam: the member, the round the
// check log keys to, and the trailer. Without a trailer the seam stays
// quiet, and the settle itself is unchanged.
func TestChainCheckSettleFoldsTheTrailer(t *testing.T) {
	t.Parallel()

	base, burst := int64(100<<20), int64(300<<20)
	t.Run("trailer folds the member round and rusage", func(t *testing.T) {
		t.Parallel()
		calls, result := settleChainCheck(t, &spawn.ProcRusage{CPUMS: 1200, BaseBytes: &base, BurstBytes: &burst})
		if result != chainCheckGreen {
			t.Fatalf("result = %q, want green", result)
		}
		if len(calls) != 1 {
			t.Fatalf("fold calls = %d, want 1", len(calls))
		}
		got := calls[0]
		if got.binding.Name != "shop" {
			t.Errorf("fold binding = %q, want the checked member shop", got.binding.Name)
		}
		if got.round != 1 {
			t.Errorf("fold round = %d, want the member's newest closed round 1", got.round)
		}
		if got.rusage == nil || got.rusage.CPUMS != 1200 || got.rusage.BaseBytes == nil || *got.rusage.BaseBytes != base || got.rusage.BurstBytes == nil || *got.rusage.BurstBytes != burst {
			t.Errorf("fold rusage = %+v, want the trailer's measurement", got.rusage)
		}
	})

	t.Run("no trailer folds nothing", func(t *testing.T) {
		t.Parallel()
		calls, result := settleChainCheck(t, nil)
		if result != chainCheckGreen {
			t.Fatalf("result = %q, want green", result)
		}
		if len(calls) != 0 {
			t.Fatalf("fold calls = %d, want none without a trailer", len(calls))
		}
	})
}

// settleServedCheck posts a served check, exits it clean, and ticks it to
// its settle, through a stub fold seam. A non-nil trailer is what the fake
// runner reports for the check's process.
func settleServedCheck(t *testing.T, trailer *spawn.ProcRusage) ([]foldCall, store.CheckRun) {
	t.Helper()
	rt, b, fr := servedCheckEnv(t)
	b.Serve.ClosedRound = 4
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	var calls []foldCall
	rt.FoldCheckCost = func(m store.Binding, closedRound int, r *store.Rusage) {
		calls = append(calls, foldCall{binding: m, round: closedRound, rusage: r})
	}
	startCheck(t, rt, remote.CreateCheckRequest{ID: "c1", Command: "make check"})
	pid := fr.handles[len(fr.handles)-1].PID
	fr.script(pid, false)
	fr.exit(pid, 0)
	if trailer != nil {
		fr.setRusage(pid, *trailer)
	}
	tickServedChecks(context.Background(), rt)
	return calls, tickCheck(t, rt)
}

// TestServedCheckSettleFoldsTheTrailer pins that a served check settling
// with a rusage trailer folds it through the seam with the binding and the
// binding's closed round. Without a trailer the seam stays quiet, and the
// settle itself is unchanged.
func TestServedCheckSettleFoldsTheTrailer(t *testing.T) {
	t.Parallel()

	base, burst := int64(100<<20), int64(300<<20)
	t.Run("trailer folds the binding closed round and rusage", func(t *testing.T) {
		t.Parallel()
		calls, got := settleServedCheck(t, &spawn.ProcRusage{CPUMS: 1200, BaseBytes: &base, BurstBytes: &burst})
		if got.Result != "pass" {
			t.Fatalf("Result = %q, want pass", got.Result)
		}
		if len(calls) != 1 {
			t.Fatalf("fold calls = %d, want 1", len(calls))
		}
		fold := calls[0]
		if fold.binding.Name != "webshop" {
			t.Errorf("fold binding = %q, want webshop", fold.binding.Name)
		}
		if fold.round != 4 {
			t.Errorf("fold round = %d, want the binding's closed round 4", fold.round)
		}
		if fold.rusage == nil || fold.rusage.CPUMS != 1200 || fold.rusage.BaseBytes == nil || *fold.rusage.BaseBytes != base || fold.rusage.BurstBytes == nil || *fold.rusage.BurstBytes != burst {
			t.Errorf("fold rusage = %+v, want the trailer's measurement", fold.rusage)
		}
	})

	t.Run("no trailer folds nothing", func(t *testing.T) {
		t.Parallel()
		calls, got := settleServedCheck(t, nil)
		if got.Result != "pass" {
			t.Fatalf("Result = %q, want pass", got.Result)
		}
		if len(calls) != 0 {
			t.Fatalf("fold calls = %d, want none without a trailer", len(calls))
		}
	})
}
