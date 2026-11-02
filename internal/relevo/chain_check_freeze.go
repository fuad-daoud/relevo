package relevo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainCheckCouldNotStart is a check run's result when its process never ran
// because the slice is frozen: the host refused the spawn, so the run settles
// without a verdict. It is settled (never advanced again) and never maps to
// the engine, so no red close and no repair round can read it.
const chainCheckCouldNotStart = "could-not-start"

// freezeCheckHoldMax is how many times one check step relaunches into a
// frozen slice before it halts for a human: a suspend/resume thaw lands in
// seconds, so a few immediate retries cover the race; a step still frozen
// after that needs a hand, not another spawn.
const freezeCheckHoldMax = 5

// frozenCheckTailLines bounds the frozen-slice scan: the refusal is written
// where the run died, so its tail holds it.
const frozenCheckTailLines = 20

// frozenCheckReason is the environment wording both check paths halt on: it
// names the slice and the thaw, so a thawed resume retries the right remedy.
func frozenCheckReason(rt Runtime) string {
	if slice := frozenSliceName(rt); slice != "the slice" {
		return fmt.Sprintf("check could not start -- slice %s is frozen; thaw it with `systemctl --user thaw %s`, then `relevo chain --resume`", slice, slice)
	}
	return "check could not start -- the slice is frozen; thaw it, then `relevo chain --resume`"
}

// frozenNoTrailer reports whether rec is the error of a run that died at
// once: no trailer means the host never ran the command, the shape a frozen
// slice leaves behind.
func frozenNoTrailer(rec *store.GateRecord) bool {
	return rec != nil && rec.Result == "error" && rec.Note == gateNoTrailerNote
}

// frozenCheckTail reports whether the run's log carries the frozen-unit
// signature: a dead run proves nothing by itself, while the manager's wording
// proves the host refused the spawn.
func frozenCheckTail(rt Runtime, logPath string) bool {
	tail := tailLines(rt.Store.ReadFile, logPath, frozenCheckTailLines)
	return spawn.IsFrozenUnitError(strings.Join(tail, "\n"))
}

// freezeCheckHolds counts the step's settled could-not-start runs: every
// frozen attempt settles one, so the count bounds the hold.
func freezeCheckHolds(tx *store.Tx, name, step string) (int, error) {
	var holds int
	for run := 1; ; run++ {
		row, err := tx.ChainCheck(name, run)
		if errors.Is(err, store.ErrNotFound) {
			return holds, nil
		}
		if err != nil {
			return 0, err
		}
		if row.Step == step && row.Result == chainCheckCouldNotStart {
			holds++
		}
	}
}

// advanceFrozenCheck settles a frozen run as could-not-start and, under the
// bound, starts the check fresh on a new run: the hold retries the step
// rather than reddening it. Past the bound it reports the typed error the
// tick halts on, starting nothing.
func advanceFrozenCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, row db.ChainCheckRow) (string, string, error) {
	frozen := &EnvironmentError{Reason: frozenCheckReason(rt)}
	row.Result = chainCheckCouldNotStart
	row.Note = frozen.Error()
	if err := chainCheckSealLog(rt, tx, c, row); err != nil {
		return "", row.Log, err
	}
	if err := tx.ChainCheckPut(c.Name, row); err != nil {
		return "", row.Log, err
	}
	holds, err := freezeCheckHolds(tx, c.Name, row.Step)
	if err != nil {
		return "", row.Log, err
	}
	if holds >= freezeCheckHoldMax {
		return "", row.Log, frozen
	}
	next, serr := chainStartCheck(ctx, rt, tx, c, row.Step, row.Command)
	if serr != nil {
		return "", row.Log, serr
	}
	retry, rerr := tx.ChainCheck(c.Name, next)
	if rerr != nil {
		return "", row.Log, rerr
	}
	return "", retry.Log, nil
}

// chainHaltOnCouldNotStart halts the chain when a check advance reports the
// check could not start for environmental reasons, with the same wording as
// a check that could not start: the hold names the freeze and the remedy,
// and no red close reaches the engine. Detection already happened upstream,
// through the classifier, so the halt trusts the type the way a refused start
// does. A non-environment failure keeps the tick's warning.
func chainHaltOnCouldNotStart(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, run int, advanceErr error) (bool, error) {
	var envErr *EnvironmentError
	if !errors.As(advanceErr, &envErr) {
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
	next.Reason = chainCheckStartReason(row.Step, envErr)
	return true, chainTerminalWF(ctx, rt, tx, c, def, before, next, workflow.Event{}, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
}
