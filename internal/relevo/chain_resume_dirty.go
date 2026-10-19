package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainResumeDirtyRecovery re-evaluates the close of a round the chain halted
// on for leaving work uncommitted. Re-entering the step would re-send the
// plan over work that is already done, so once the cause is gone the round's
// own close is fed back and the chain advances; while it is not, the resume
// refuses with the halt's own advice. The git seam cannot test ancestry, so a
// commit is told by HEAD having moved off the round's baseline.
func chainResumeDirtyRecovery(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, st workflow.State) (*workflow.Event, error) {
	if st.Status != workflow.StatusHalted || st.Awaiting.Member == "" || st.Awaiting.Round <= 0 || st.Awaiting.Step != st.At {
		return nil, nil
	}
	reason := st.Reason
	if reason == "" {
		reason = c.Reason
	}
	if !chainHaltedOverUncommittedWork(reason) {
		return nil, nil
	}
	member, err := chainFlowMemberName(tx, c, st.Awaiting.Member)
	if err != nil {
		return nil, err
	}
	b, err := tx.Load(member)
	if err != nil {
		return nil, err
	}
	if !chainDirtyGateApplies(rt, b) {
		return nil, nil
	}
	dirty, err := rt.Git.Dirty(ctx, b.CWD)
	if err != nil {
		return nil, refuse("chain %s is halted over uncommitted work and its tree could not be read (%v): %s", c.Name, err, reason)
	}
	if dirty {
		return nil, refuse("chain %s is still halted over uncommitted work: %s", c.Name, reason)
	}
	head, err := rt.Git.HeadCommit(ctx, b.CWD)
	if err != nil {
		return nil, refuse("chain %s is halted over uncommitted work and HEAD could not be read (%v): %s", c.Name, err, reason)
	}
	if b.RoundClosedBaselineHead == "" {
		return nil, refuse("chain %s is still halted over uncommitted work, and the round's starting commit is unknown: %s", c.Name, reason)
	}
	if head == "" || head == b.RoundClosedBaselineHead {
		return nil, refuse("chain %s is still halted over uncommitted work, and nothing has been committed since the round began: %s", c.Name, reason)
	}
	return chainResumeRoundClose(ctx, rt, tx, c, member, st.Awaiting.Round)
}
