package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainCheckGreen passed and chainCheckRed did not; the row keeps the
// runner's own result, so a timeout stays distinguishable from a failure.
const (
	chainCheckGreen = "green"
	chainCheckRed   = "red"
)

// chainCheckStarting is a run's result while its row exists but its process
// is not recorded yet. A row that survives to a tick in this state has no
// process to advance, so the tick settles it as an error.
const chainCheckStarting = "starting"

// chainCheckStopped is the result a run carries when a stop or a done ends
// it: the run must never drive the engine, so the row keeps the word.
const chainCheckStopped = "stopped"

// chainCheckPut is a test seam: nil in production, where the row that fills a
// spawned run's PID is written through tx.ChainCheckPut. A test sets it to
// inject a row-write failure after the process exists, which is the window that
// must not leave an orphan behind.
var chainCheckPut func(tx *store.Tx, name string, row db.ChainCheckRow) error

func putChainCheck(tx *store.Tx, name string, row db.ChainCheckRow) error {
	if chainCheckPut != nil {
		return chainCheckPut(tx, name, row)
	}
	return tx.ChainCheckPut(name, row)
}

// chainStartCheck starts one chain check run in the chain's tree, scoped to
// the gate quota; the check's log streams to the round-file path the row
// records, sealed into a round_file row when the run ends.
//
// The tracking row is written first, as "starting", so a crash between spawn
// and PID fill leaves a settleable row, not an untracked process. A spawn
// that fails marks the row failed; a PID fill that fails kills the process.
func chainStartCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, step, command string) (int, error) {
	if rt.Runner == nil {
		return 0, errors.New("no runner")
	}
	run, err := tx.ChainCheckNextRun(c.Name)
	if err != nil {
		return 0, err
	}
	visit, err := chainCheckVisit(tx, c.Name, step)
	if err != nil {
		return 0, err
	}
	member, round, err := chainCheckLogTarget(tx, c)
	if err != nil {
		return 0, err
	}
	spec := chainCheckSpec(rt, c, run, command, rt.Store.CheckLogPath(member, round, run))
	// Refused before the row is written, and returned typed: the chain halts
	// on the environment's reason instead of recording a red check.
	if err := preflightDisk(rt, spec.Dir); err != nil {
		return 0, err
	}
	row := db.ChainCheckRow{
		Run: run, Step: step, Visit: visit, Command: command,
		Result: chainCheckStarting, Log: spec.LogPath, CreatedAt: rt.Now().UTC(),
	}
	if err := tx.ChainCheckPut(c.Name, row); err != nil {
		return 0, err
	}
	got, rec, _ := startGateProc(ctx, rt, spec, 0)
	if rec != nil {
		row.Result = "error"
		row.Note = rec.Note
		if perr := tx.ChainCheckPut(c.Name, row); perr != nil {
			return 0, perr
		}
		return 0, fmt.Errorf("chain %s check %d: %s", c.Name, run, rec.Note)
	}
	row.PID, row.StartedAt, row.Attempt = got.PID, got.StartedAt, got.Attempt
	row.Result = ""
	if err := putChainCheck(tx, c.Name, row); err != nil {
		// The row cannot name the process just started, so the chain would
		// never learn of it: kill it and report the write failure.
		h := spawn.ProcHandle{PID: got.PID, StartedAt: time.Unix(got.StartedAt, 0)}
		if kerr := rt.Runner.Kill(ctx, h, spec.LogPath); kerr != nil {
			slog.Warn("chain check: kill after a failed row write", "chain", c.Name, "run", run, "err", kerr)
		}
		return 0, err
	}
	return run, nil
}

// chainStartOrAdoptCheck starts a check run unless one is alive: a resume
// re-entering a check step adopts the earlier run rather than starting a
// second one on the same tree.
func chainStartOrAdoptCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, step, command string) (int, error) {
	if run, err := chainCheckLiveRun(ctx, rt, tx, c); err != nil {
		return 0, err
	} else if run > 0 {
		return run, nil
	}
	return chainStartCheck(ctx, rt, tx, c, step, command)
}

// chainCheckLiveRun returns the oldest unsettled run still alive, or 0.
// Runs are contiguous, so the first unsettled one is the only candidate.
func chainCheckLiveRun(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) (int, error) {
	for run := 1; ; run++ {
		row, err := tx.ChainCheck(c.Name, run)
		if errors.Is(err, store.ErrNotFound) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		if chainCheckSettled(row) {
			continue
		}
		if chainCheckRowAlive(ctx, rt, c, row) {
			return run, nil
		}
		return 0, nil
	}
}

// chainCheckSettled reports whether a row carries a terminal result. A
// "starting" row is not settled: its process is not recorded yet.
func chainCheckSettled(row db.ChainCheckRow) bool {
	return row.Result != "" && row.Result != chainCheckStarting
}

// chainCheckRowAlive reports whether a check row still has a live process or a
// loaded gate scope, either of which means the run is still there.
func chainCheckRowAlive(ctx context.Context, rt Runtime, c db.ChainRow, row db.ChainCheckRow) bool {
	if row.PID != 0 && rt.Runner != nil {
		h := spawn.ProcHandle{PID: row.PID, StartedAt: time.Unix(row.StartedAt, 0)}
		if alive, err := rt.Runner.Alive(ctx, h); err == nil && alive {
			return true
		}
	}
	return scopeRunning(ctx, rt, scopeUnitNameFor(scopeGate, c.Owner, c.Name+"-check", row.Run, ""))
}

// chainKillRunningChecks kills every check run still in flight and records
// each as stopped, so no run survives that a resume could double. A process
// or scope that cannot be ended is returned: the caller must not claim a run
// stopped while something of it still is.
func chainKillRunningChecks(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) error {
	for run := 1; ; run++ {
		row, err := tx.ChainCheck(c.Name, run)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if chainCheckSettled(row) {
			continue
		}
		if err := chainKillCheck(ctx, rt, c, row); err != nil {
			return err
		}
		row.Result = chainCheckStopped
		row.Note = chainCheckStopped
		if err := tx.ChainCheckPut(c.Name, row); err != nil {
			return err
		}
	}
}

// chainKillCheck ends one run: the live process first, then its gate scope.
func chainKillCheck(ctx context.Context, rt Runtime, c db.ChainRow, row db.ChainCheckRow) error {
	if row.PID != 0 && rt.Runner != nil {
		h := spawn.ProcHandle{PID: row.PID, StartedAt: time.Unix(row.StartedAt, 0)}
		alive, err := rt.Runner.Alive(ctx, h)
		if err != nil {
			return err
		}
		if alive {
			if err := rt.Runner.Kill(ctx, h, row.Log); err != nil {
				return err
			}
		}
	}
	unit := scopeUnitNameFor(scopeGate, c.Owner, c.Name+"-check", row.Run, "")
	_, err := endScope(ctx, rt, unit)
	return err
}

// chainAdvanceCheck advances one check run a single tick: green or red once
// ended, "" while it runs. A settled run answers from its row, so a repeated
// tick is a stable read.
func chainAdvanceCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, run int) (result string, logKey string, err error) {
	row, err := tx.ChainCheck(c.Name, run)
	if err != nil {
		return "", "", err
	}
	if row.Result == chainCheckStarting {
		// The row predates its process, so there is nothing to advance:
		// settle it rather than leave the chain waiting on a run that can
		// never close.
		row.Result = "error"
		row.Note = "the run's process was never recorded"
		if err := chainCheckSealLog(rt, tx, c, row); err != nil {
			return "", row.Log, err
		}
		if err := tx.ChainCheckPut(c.Name, row); err != nil {
			return "", row.Log, err
		}
		return chainCheckRed, row.Log, nil
	}
	if row.Result != "" {
		return chainCheckResult(row.Result), row.Log, nil
	}

	spec := chainCheckSpec(rt, c, run, row.Command, row.Log)
	state := store.GateRun{PID: row.PID, StartedAt: row.StartedAt, Attempt: row.Attempt, Command: row.Command}
	next, done, rec, err := advanceGateProc(ctx, rt, state, spec)
	if err != nil {
		return "", row.Log, err
	}
	if next != nil {
		row.PID, row.StartedAt, row.Attempt = next.PID, next.StartedAt, next.Attempt
		if err := tx.ChainCheckPut(c.Name, row); err != nil {
			return "", row.Log, err
		}
		return "", row.Log, nil
	}
	if !done {
		return "", row.Log, nil
	}

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
	return chainCheckResult(row.Result), row.Log, nil
}

// tickChainChecks advances every chain's in-flight check once per tick, one
// per chain: steps run one at a time. A settled check feeds check_closed to
// the engine in the same critical section.
func tickChainChecks(ctx context.Context, rt Runtime) {
	chains, err := rt.Store.Chains()
	if err != nil {
		slog.Warn("chain check tick: list chains", "err", err)
		return
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, c := range chains {
			if err := chainAdvanceOneCheck(ctx, rt, tx, c); err != nil {
				slog.Warn("chain check tick", "chain", c.Name, "err", err)
			}
		}
		return nil
	}); err != nil {
		slog.Warn("chain check tick", "err", err)
	}
}

// chainAdvanceOneCheck advances the chain's oldest unsettled check and feeds
// its end to the engine. Runs are contiguous, so the list stops at the first
// unrecorded run. A terminal chain, and one whose engine is not waiting on a
// check, are skipped before any row lookup.
//
// A chain with no workflow never started a check, so it is left alone; one
// with no check row may still await a placed writer's check, retried here.
func chainAdvanceOneCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) error {
	if len(c.WorkflowJSON) == 0 {
		return nil
	}
	if chainFlowTerminal(workflow.Status(c.Status)) {
		return nil
	}
	// A server chain is driven by the server's daemon: advancing its checks
	// from an unpulled mirror would 404 and halt the mirror on every tick.
	if chain.OnServer(c) {
		return nil
	}
	st, err := chain.WorkflowState(c)
	if err != nil {
		return err
	}
	if st.Awaiting.Step == "" || st.Awaiting.Member != "" {
		return nil
	}
	for run := 1; ; run++ {
		row, err := tx.ChainCheck(c.Name, run)
		if errors.Is(err, store.ErrNotFound) {
			return chainTickPlacedCheck(ctx, rt, tx, c)
		}
		if err != nil {
			return err
		}
		if chainCheckSettled(row) {
			continue
		}
		result, logKey, err := chainAdvanceCheck(ctx, rt, tx, c, run)
		if err != nil {
			return err
		}
		if result == "" {
			return nil
		}
		before, err := chain.WorkflowState(c)
		if err != nil {
			return err
		}
		ev := workflow.Event{
			Kind: workflow.EventCheckClosed, Step: row.Step, Run: run,
			Result: result, Log: logKey,
			RepeatRed: chainRepeatRedCheck(rt, c, before, row.Step, logKey),
		}
		if halted, err := housekeep.ChainHaltOnEnvironment(ctx, HousekeepDeps(rt), tx, c, run, ev); halted || err != nil {
			return err
		}
		return chainAdvance(ctx, rt, tx, c, ev)
	}
}

// chainTickPlacedCheck advances a placed writer's check run: with a run id
// it polls GetCheck for the awaited run; without one it answers from the
// writer's newest closed round's gate record once pulled.
func chainTickPlacedCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) error {
	if len(c.WorkflowJSON) == 0 || c.Status != string(chain.StatusRunning) {
		return nil
	}
	st, err := chain.WorkflowState(c)
	if err != nil {
		return err
	}
	// Only a check awaiting is answered here: a run step has a member.
	if st.Awaiting.Step == "" || st.Awaiting.Member != "" {
		return nil
	}
	def, err := chain.WorkflowDef(c)
	if err != nil {
		return err
	}
	step, ok := def.Steps[st.Awaiting.Step]
	if !ok || step.Check == "" {
		return nil
	}
	member, err := chainFlowWriterMember(tx, c)
	if err != nil {
		return err
	}
	b, lerr := tx.Load(member)
	if lerr != nil || !b.Builder.Remote() {
		return nil
	}
	if st.Awaiting.Run == 0 {
		act := workflow.Action{Kind: workflow.ActionRunCheck, Step: st.Awaiting.Step, Command: step.Check}
		return chainFlowPullCheck(ctx, rt, tx, c, def, st, &st, workflow.Event{}, act, member)
	}
	if rt.Remote == nil {
		return nil
	}
	view, err := rt.Remote.GetCheck(ctx, b.Builder.Server, b.Name, chainCheckID(st.Awaiting.Run))
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return chainPlacedCheckMiss(ctx, rt, tx, c, def, st, b, step)
		}
		return nil
	}
	if view.Result == "" {
		return nil
	}
	logTarget, round, err := chainCheckLogTarget(tx, c)
	if err != nil {
		return err
	}
	logKey := rt.Store.CheckLogPath(logTarget, round, st.Awaiting.Run)
	if err := tx.PutRoundFile(logTarget, round, logKey, []byte(partialCheckNote(view, b.Builder.Server)+view.LogTail)); err != nil {
		return err
	}
	saved, err := tx.Chain(c.Name)
	if err != nil {
		return err
	}
	before, err := chain.WorkflowState(saved)
	if err != nil {
		return err
	}
	return chainAdvance(ctx, rt, tx, saved, workflow.Event{
		Kind:      workflow.EventCheckClosed,
		Step:      st.Awaiting.Step,
		Run:       st.Awaiting.Run,
		Result:    chainCheckResult(view.Result),
		Log:       logKey,
		RepeatRed: chainRepeatRedCheck(rt, saved, before, st.Awaiting.Step, logKey),
	})
}

// chainPlacedCheckMiss answers a GetCheck that found nothing under the awaited
// run id. A bare miss is not a loss: the tick may read between the create
// being asked for and its answer arriving, or the server may still be starting
// the run. Halting there ended chains on checks about to answer.
//
// A loss is proved, not assumed: the binding holds a settled run under a
// different id for this same step. Everything else keeps awaiting; the chain's
// own timeouts end a check that truly never answers.
func chainPlacedCheckMiss(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, st workflow.State, b store.Binding, step workflow.Step) error {
	if !chainCheckRunLost(ctx, rt, b, st, step) {
		return nil
	}
	before := st
	st.Status = workflow.StatusHalted
	st.Reason = fmt.Sprintf("check %s: %s", st.Awaiting.Step, "the server holds a different run for it")
	return chainTerminalWF(ctx, rt, tx, c, def, before, st, workflow.Event{}, workflow.Action{Kind: workflow.ActionHalt, Reason: st.Reason})
}

// chainCheckRunLost reports a settled run under an id other than the awaited
// one, for the awaited step. Holding nothing, or the awaited run itself, is
// not a loss.
func chainCheckRunLost(ctx context.Context, rt Runtime, b store.Binding, st workflow.State, step workflow.Step) bool {
	if rt.Remote == nil {
		return false
	}
	// The binding holds one run, so the only other id worth asking about is the
	// one this machine would mint next. Anything else is not a run of this step.
	view, err := rt.Remote.GetCheck(ctx, b.Builder.Server, b.Name, chainCheckID(st.Awaiting.Run+1))
	if err != nil {
		return false
	}
	return view.Result != "" && view.Step == st.Awaiting.Step
}

// chainCheckSpec is the gate-runner spec for one chain check. A check has no
// binding, so it takes the policy timeout and no CPU pin.
func chainCheckSpec(rt Runtime, c db.ChainRow, run int, command, logPath string) gateProcSpec {
	return gateProcSpec{
		Dir:      c.Worktree,
		Command:  command,
		LogPath:  logPath,
		UnitName: scopeUnitNameFor(scopeGate, c.Owner, c.Name+"-check", run, ""),
		Timeout:  rt.Policy.GateTimeout(),
	}
}

func chainCheckVisit(tx *store.Tx, name, step string) (int, error) {
	visit := 1
	for run := 1; ; run++ {
		row, err := tx.ChainCheck(name, run)
		if errors.Is(err, store.ErrNotFound) {
			return visit, nil
		}
		if err != nil {
			return 0, err
		}
		if row.Step == step {
			visit++
		}
	}
}

// chainCheckLogTarget names the member and round a check log is keyed to. A
// chain with no writer uses its first member.
func chainCheckLogTarget(tx *store.Tx, c db.ChainRow) (string, int, error) {
	member := c.Builder
	if member == "" {
		if members := view.MembersOf(c); len(members) > 0 {
			member = members[0]
		}
	}
	if member == "" {
		return "", 0, fmt.Errorf("chain %s has no member to hold a check log", c.Name)
	}
	round := chain.NewestClosedRound(tx, member)
	if round < 1 {
		round = 1
	}
	return member, round, nil
}

// chainCheckSealLog writes the run's streamed log into its round_file row and
// removes the on-disk copy: the row is the record. A run that wrote nothing
// seals an empty row.
func chainCheckSealLog(rt Runtime, tx *store.Tx, c db.ChainRow, row db.ChainCheckRow) error {
	body, err := rt.Store.ReadFile(row.Log)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		body = nil
	default:
		return err
	}
	member, round, ok := rt.Store.CheckLogTarget(row.Log)
	if !ok {
		return fmt.Errorf("chain check: cannot resolve the check log path %s", row.Log)
	}
	if err := tx.PutRoundFile(member, round, row.Log, body); err != nil {
		return err
	}
	if err := os.Remove(row.Log); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("chain check: could not remove the sealed log", "chain", c.Name, "path", row.Log, "err", err)
	}
	return nil
}

// chainCheckResult maps the runner's word onto the engine's: pass is green,
// every other settled result red.
func chainCheckResult(raw string) string {
	if raw == "pass" {
		return chainCheckGreen
	}
	return chainCheckRed
}

// partialCheckNote heads a stored check log whose served tail was cut, so it
// never reads as the whole run. It names the server and the run, not a path:
// a server's layout is not for its clients.
func partialCheckNote(view remote.CheckView, server string) string {
	if !view.LogTruncated {
		return ""
	}
	return fmt.Sprintf("partial: the last %d KiB of the check log; the full log is check run %s on server %s\n", servedCheckLogCap/1024, view.ID, server)
}
