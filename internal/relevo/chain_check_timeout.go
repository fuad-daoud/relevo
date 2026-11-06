package relevo

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// timeoutCheckHoldMax bounds how many consecutive settled timeouts one step
// tolerates: the first may be a slow host, so it settles and retries once,
// while the second names the limit instead of buying repair.
const timeoutCheckHoldMax = 2

// chainTimeoutError is the typed hold a second consecutive timeout reports,
// so the tick halts on identity rather than matching the limit text again.
type chainTimeoutError struct {
	Step  string
	Holds int
}

func (e *chainTimeoutError) Error() string {
	return fmt.Sprintf("check %s timed out %d times in a row", e.Step, e.Holds)
}

// timeoutCheckHolds counts the step's consecutive settled timeouts, walking
// from newest to oldest: a could-not-start neither counts nor ends the walk,
// because a freeze between two timeouts changes nothing about them, while any
// other settled result ends it. Unsettled rows are skipped, so an in-flight
// retry never breaks the count.
func timeoutCheckHolds(tx *store.Tx, name, step string) (int, error) {
	var rows []db.ChainCheckRow
	for run := 1; ; run++ {
		row, err := tx.ChainCheck(name, run)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return 0, err
		}
		if row.Step != step {
			continue
		}
		rows = append(rows, row)
	}
	var holds int
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if row.Result == "timeout" {
			holds++
			continue
		}
		if row.Result == chainCheckCouldNotStart {
			continue
		}
		if !chainCheckSettled(row) {
			continue
		}
		break
	}
	return holds, nil
}

// advanceHoldAwaitingRun points the workflow's awaited check at the hold's
// retry run, so the retry's green close matches and the chain continues. A
// hold is not an engine transition, so the write carries no event and records
// no trace row. A stateless chain carries no workflow to advance.
func advanceHoldAwaitingRun(rt Runtime, tx *store.Tx, c db.ChainRow, run int) error {
	if len(c.WorkflowJSON) == 0 {
		return nil
	}
	def, err := chain.WorkflowDef(c)
	if err != nil {
		return err
	}
	before, err := chain.WorkflowState(c)
	if err != nil {
		return err
	}
	next := cloneFlowState(before)
	next.Awaiting.Run = run
	return chainSaveFlow(rt, tx, c, def, before, next, workflow.Event{}, workflow.Action{}, "")
}

// advanceTimeoutCheck settles a timed-out run with its runner result and,
// under the bound, starts the check fresh on a new run: the hold retries the
// step rather than reddening it. Past the bound it reports the typed error
// the tick halts on, starting nothing. The walk runs only here, once per
// settled timeout, never on an ordinary tick.
func advanceTimeoutCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, row db.ChainCheckRow, rec *store.GateRecord) (string, string, error) {
	row.Result = rec.Result
	row.ExitCode = rec.ExitCode
	row.DurationMS = rec.DurationMS
	row.Note = rec.Note
	if err := chainCheckSealLog(rt, tx, c, row); err != nil {
		return "", row.Log, err
	}
	if err := tx.ChainCheckPut(c.Name, row); err != nil {
		return "", row.Log, err
	}
	holds, err := timeoutCheckHolds(tx, c.Name, row.Step)
	if err != nil {
		return "", row.Log, err
	}
	if holds >= timeoutCheckHoldMax {
		return "", row.Log, &chainTimeoutError{Step: row.Step, Holds: holds}
	}
	next, serr := chainStartCheck(ctx, rt, tx, c, row.Step, row.Command)
	if serr != nil {
		return "", row.Log, serr
	}
	retry, rerr := tx.ChainCheck(c.Name, next)
	if rerr != nil {
		return "", row.Log, rerr
	}
	if err := advanceHoldAwaitingRun(rt, tx, c, next); err != nil {
		return "", row.Log, err
	}
	return "", retry.Log, nil
}

// chainHaltOnTimeout halts the chain when a check advance reports a second
// consecutive timeout, with no red close and no repair: the halt flows
// through the terminal writer so the newest timeout's wording names the
// limit. A non-timeout failure keeps the tick's warning.
func chainHaltOnTimeout(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, run int, advanceErr error) (bool, error) {
	var timeoutErr *chainTimeoutError
	if !errors.As(advanceErr, &timeoutErr) {
		return false, nil
	}
	row, err := tx.ChainCheck(c.Name, run)
	if err != nil {
		return false, err
	}
	def, err := chain.WorkflowDef(c)
	if err != nil {
		return false, err
	}
	before, err := chain.WorkflowState(c)
	if err != nil {
		return false, err
	}
	next := cloneFlowState(before)
	next.Status = workflow.StatusHalted
	next.Reason = fmt.Sprintf("check %s timed out %d times in a row", row.Step, timeoutErr.Holds)
	return true, chainTerminalWF(ctx, rt, tx, c, def, before, next, workflow.Event{}, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
}
