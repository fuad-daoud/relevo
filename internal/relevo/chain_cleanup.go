package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainStartResult refreshes the started row and reports the start line's
// check and cleanup: the commands the check and cleanup steps run, with the
// tier that supplied each and the cleanup's when word.
func chainStartResult(tx *store.Tx, prep chainPreparedWorkflow, row db.ChainRow, stored []store.Binding) ChainResult {
	if current, cerr := tx.Chain(prep.opts.Name); cerr == nil {
		row = current
	}
	check, source := chain.CheckReport(prep.plan.def, prep.plan.gateRes, prep.opts.Params)
	cleanup, cleanupSource, cleanupAfter := chain.CleanupReport(prep.plan.def,
		prep.plan.cleanupRes.Command, prep.plan.cleanupRes.Source, prep.plan.cleanupRes.After, prep.opts.Params)
	return ChainResult{Chain: row, Members: stored, Plans: len(prep.plan.bodies),
		Check: check, CheckSource: source,
		Cleanup: cleanup, CleanupSource: cleanupSource, CleanupAfter: cleanupAfter}
}

// isCleanupStep reports whether the stored definition's step carries a
// cleanup command. The chain_check table holds both checks and cleanups, so
// the step's kind tells a run apart when the definition is present.
func isCleanupStep(def workflow.Definition, step string) bool {
	s, ok := def.Steps[step]
	if !ok {
		return false
	}
	return s.Cleanup != ""
}

// hasCleanupCommand reports whether any step renders a cleanup command, so a
// client can tell a chain that runs a cleanup from one that prunes to the
// shape an old server reads.
func hasCleanupCommand(def workflow.Definition) bool {
	for _, s := range def.Steps {
		if s.Cleanup == "" {
			continue
		}
		if workflow.RenderParams(def, s.Cleanup) != "" {
			return true
		}
	}
	return false
}

// chainCleanupStartFailWarning is the trace warning for a cleanup the chain
// owner could not start. A cleanup never halts a chain, so the step closes
// done and the start failure has to survive in the trace beside that close.
func chainCleanupStartFailWarning(err error) string {
	return fmt.Sprintf("cleanup skipped: the chain owner could not start it: %v", err)
}

// chainCleanupWarning is the start-line and trace warning naming the server
// that cannot run the chain's cleanup. The chain runs without cleanup and is
// never refused for it.
func chainCleanupWarning(server string) string {
	return fmt.Sprintf("server %s cannot run cleanup; running without it", server)
}

// chainCleanupSkippedWarning is the trace warning for a placed writer's
// cleanup. Only the chain owner can run a cleanup in its worktree, so a
// missing runner warns instead of halting: the local engine has no tree for
// the remote writer's cleanup, and a halt would end a chain that could
// otherwise finish.
func chainCleanupSkippedWarning(server string) string {
	if server == "" {
		return "cleanup skipped on a placed writer: only the chain owner runs cleanup in its worktree"
	}
	return fmt.Sprintf("cleanup skipped on placed writer on server %s: only the chain owner runs cleanup in its worktree", server)
}

// chainStartCleanup starts one cleanup run in the chain's tree, reusing the
// check runner spec: the gate quota, the worktree and the round's
// environment. A cleanup has no binding, so it takes the policy timeout and
// no CPU pin, exactly as a check does.
func chainStartCleanup(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, step, command string) (int, error) {
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
	spec := chainCheckSpec(rt, c, run, command, rt.Store.CleanupLogPath(member, round, run))
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
	got, rec, serr := startGateProc(ctx, rt, spec, 0)
	if serr != nil {
		row.Result = chainCheckCouldNotStart
		row.Note = serr.Error()
		if perr := tx.ChainCheckPut(c.Name, row); perr != nil {
			return 0, perr
		}
		return 0, serr
	}
	if rec != nil {
		row.Result = "error"
		row.Note = rec.Note
		if perr := tx.ChainCheckPut(c.Name, row); perr != nil {
			return 0, perr
		}
		return 0, fmt.Errorf("chain %s cleanup %d: %s", c.Name, run, rec.Note)
	}
	row.PID, row.StartedAt, row.Attempt = got.PID, got.StartedAt, got.Attempt
	row.Result = ""
	if err := putChainCheck(tx, c.Name, row); err != nil {
		h := spawn.ProcHandle{PID: got.PID, StartedAt: time.Unix(got.StartedAt, 0)}
		if kerr := rt.Runner.Kill(ctx, h, spec.LogPath); kerr != nil {
			slog.Warn("chain cleanup: kill after a failed row write", "chain", c.Name, "run", run, "err", kerr)
		}
		return 0, err
	}
	return run, nil
}

// chainAdvanceCleanup advances one cleanup run and maps any settled result to
// done: a cleanup never reports green or red, so review and repair seeds that
// read the gate never see it. It returns the sealed log with the run's exit
// code and duration, which the trace renders beside the step.
func chainAdvanceCleanup(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, run int) (result, logKey string, exitCode int, durationMS int64, err error) {
	result, logKey, err = chainAdvanceCheck(ctx, rt, tx, c, run)
	if err != nil || result == "" {
		return "", logKey, 0, 0, err
	}
	row, rerr := tx.ChainCheck(c.Name, run)
	if rerr != nil {
		return "", logKey, 0, 0, rerr
	}
	return "done", logKey, row.ExitCode, row.DurationMS, nil
}

// chainStartOrAdoptCleanup starts a cleanup run unless one is alive: a resume
// re-entering a cleanup step adopts the earlier run rather than starting a
// second one beside it on the same tree.
func chainStartOrAdoptCleanup(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, step, command string) (int, error) {
	if run, err := chainCheckLiveRun(ctx, rt, tx, c); err != nil {
		return 0, err
	} else if run > 0 {
		return run, nil
	}
	return chainStartCleanup(ctx, rt, tx, c, step, command)
}

// chainAdvanceOneCleanup advances the chain's oldest unsettled cleanup run
// and feeds its end to the engine as done. A cleanup never feeds the
// repeat-red rule and never counts toward a regate budget: its done status
// matches no red arm. An advance failure warns and still advances to done,
// so a cleanup never halts the chain.
func chainAdvanceOneCleanup(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, run int) error {
	row, err := tx.ChainCheck(c.Name, run)
	if err != nil {
		return err
	}
	step := row.Step
	result, logKey, exitCode, durationMS, aerr := chainAdvanceCleanup(ctx, rt, tx, c, run)
	if aerr != nil {
		slog.Warn("chain cleanup: advance failed, closing as done", "chain", c.Name, "step", step, "run", run, "err", aerr)
		return chainAdvance(ctx, rt, tx, c, workflow.Event{
			Kind: workflow.EventCheckClosed, Step: step, Run: run,
			Result: "done", Log: logKey, ExitCode: exitCode, DurationMS: durationMS,
			Reason: fmt.Sprintf("cleanup %s run %d did not finish: %v", step, run, aerr),
		})
	}
	if result == "" {
		return nil
	}
	return chainAdvance(ctx, rt, tx, c, workflow.Event{
		Kind: workflow.EventCheckClosed, Step: step, Run: run,
		Result: "done", Log: logKey, ExitCode: exitCode, DurationMS: durationMS,
	})
}

// chainTickPlacedCleanup skips a placed writer's awaiting cleanup with a
// trace warning, reporting whether it handled the tick. A non-cleanup step
// is left for the check path.
func chainTickPlacedCleanup(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, st workflow.State) (bool, error) {
	if !isCleanupStep(def, st.Awaiting.Step) {
		return false, nil
	}
	member, err := chainFlowWriterMember(tx, c)
	if err != nil {
		return true, err
	}
	b, lerr := tx.Load(member)
	server := ""
	if lerr == nil {
		server = b.Builder.Server
	}
	step := def.Steps[st.Awaiting.Step]
	act := workflow.Action{Kind: workflow.ActionRunCheck, Step: st.Awaiting.Step, Command: workflow.RenderParams(def, step.Cleanup)}
	return true, chainSkipCleanup(ctx, rt, tx, c, def, st, &st, workflow.Event{}, act, member, chainCleanupSkippedWarning(server))
}

// chainSkipCleanup closes a cleanup as done without running it and carries
// the caller's warning into the trace: the engine advances past the step's
// done edge, and the trace row names why the cleanup did not run -- a placed
// writer only the chain owner could run for, or a cleanup whose start failed.
func chainSkipCleanup(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action, member, warning string) error {
	next.Awaiting.Run = 0
	if err := chainSaveFlow(rt, tx, c, def, before, *next, ev, act, member); err != nil {
		return err
	}
	saved, err := tx.Chain(c.Name)
	if err != nil {
		return err
	}
	return chainAdvance(ctx, rt, tx, saved, workflow.Event{
		Kind: workflow.EventCheckClosed, Step: act.Step, Run: 0,
		Result: "done", Log: "", Reason: warning,
	})
}
