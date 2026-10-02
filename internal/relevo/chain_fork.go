package relevo

import (
	"context"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainFlowFork runs the fork action: it starts each child workflow in order
// under the parent's lock, cutting each child's branch from the parent writer's tip.
// If any child fails to start, earlier children are rolled back and the parent halts.
func chainFlowFork(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action) error {
	var created []childCreated
	for _, spec := range act.Children {
		ch, err := chainStartChild(ctx, rt, tx, c, spec)
		if err != nil {
			chainRollbackChildren(ctx, rt, tx, c.Repo, created)
			next.Status = workflow.StatusHalted
			next.Reason = fmt.Sprintf("child %s could not start: %v", c.Name+"."+spec.Key, err)
			return chainTerminalWF(ctx, rt, tx, c, def, before, *next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
		}
		created = append(created, ch)
	}

	closing := ev.Member
	if closing == "" {
		closing = c.Builder
	}
	return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, closing)
}

// chainRollbackChildren removes what earlier started children created when a
// later child fails to start: worktree, branch, rows, members, and chain directories.
func chainRollbackChildren(ctx context.Context, rt Runtime, tx *store.Tx, repo string, children []childCreated) {
	for i := len(children) - 1; i >= 0; i-- {
		ch := children[i]
		chainRollback(ctx, rt, repo, ch.worktree, ch.branch)
		_ = tx.ChainDelete(ch.name)
		for _, m := range ch.members {
			_ = tx.Delete(m)
		}
		_ = os.RemoveAll(rt.Store.ChainDir(ch.name))
	}
}
