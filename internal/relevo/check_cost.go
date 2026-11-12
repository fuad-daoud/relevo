package relevo

import (
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// foldChainCheckCost folds a settled chain check's measurement into its
// member's round sample: the member the check ran against, the round the
// check log keys to, and the trailer the advance read. An unset seam, a
// missing trailer, or a member the store no longer names folds nothing, so
// the settle never fails because of it.
func foldChainCheckCost(rt Runtime, tx *store.Tx, c db.ChainRow, r *store.Rusage) {
	if r == nil || rt.FoldCheckCost == nil {
		return
	}
	member, round, err := chainCheckLogTarget(tx, c)
	if err != nil {
		return
	}
	b, err := tx.Load(member)
	if err != nil {
		return
	}
	rt.FoldCheckCost(b, round, r)
}

// foldServedCheckCost folds a settled served check's measurement into its
// round's sample. A binding with no serve facts records nothing, the way the
// close itself skips one.
func foldServedCheckCost(rt Runtime, b store.Binding, r *store.Rusage) {
	if r == nil || rt.FoldCheckCost == nil || b.Serve == nil {
		return
	}
	rt.FoldCheckCost(b, b.Serve.ClosedRound, r)
}
