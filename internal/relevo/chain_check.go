package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainCheckGreen is a check that passed and chainCheckRed one that did not;
// the engine reads these words, while the row keeps the runner's own result,
// so a timeout stays distinguishable from a plain failure.
const (
	chainCheckGreen = "green"
	chainCheckRed   = "red"
)

// chainStartCheck starts one chain check run: the command runs through the gate
// runner's mechanics in the chain's tree, scoped to the gate quota, and a
// chain_check row records it with the visit this run is for its step. The
// returned run number is the row's key.
//
// The run streams its log to the check's round-file path, recorded on the row;
// chainAdvanceCheck seals that file into a round_file row when the run ends.
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
	got, rec, _ := startGateProc(ctx, rt, spec, 0)
	if rec != nil {
		return 0, fmt.Errorf("chain %s check %d: %s", c.Name, run, rec.Note)
	}
	row := db.ChainCheckRow{
		Run: run, Step: step, Visit: visit, Command: command,
		PID: got.PID, StartedAt: got.StartedAt, Attempt: got.Attempt,
		Log: spec.LogPath, CreatedAt: rt.Now().UTC(),
	}
	if err := tx.ChainCheckPut(c.Name, row); err != nil {
		return 0, err
	}
	return run, nil
}

// chainAdvanceCheck advances one chain check run a single tick. result is green
// or red once the run ended and "" while it still runs; logKey is the
// round-file key the check's log is sealed at. A run found already settled
// answers from its row, so a repeated tick is a stable read.
func chainAdvanceCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, run int) (result string, logKey string, err error) {
	row, err := tx.ChainCheck(c.Name, run)
	if err != nil {
		return "", "", err
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
// check per chain: a chain's steps run one at a time, so at most one check is
// in flight. A settled check is fed back to the engine as check_closed in the
// same critical section.
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

// chainAdvanceOneCheck advances the chain's oldest unsettled check, if it has
// one, and feeds its end to the engine. Runs are allocated contiguously, so the
// row list stops at the first run the chain has not recorded. A chain with no
// workflow never started a check, so it is left alone. A chain with no check row
// at all may still be waiting on a placed writer's check, which this tick
// re-tries.
func chainAdvanceOneCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) error {
	if len(c.WorkflowJSON) == 0 {
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
		if row.Result != "" {
			continue
		}
		result, logKey, err := chainAdvanceCheck(ctx, rt, tx, c, run)
		if err != nil {
			return err
		}
		if result == "" {
			return nil
		}
		return chainAdvance(ctx, rt, tx, c, workflow.Event{
			Kind: workflow.EventCheckClosed, Step: row.Step, Run: run,
			Result: result, Log: logKey,
		})
	}
}

// chainTickPlacedCheck re-tries a placed writer's check that was left awaiting
// when its round's gate record had not been pulled yet. A running workflow chain
// that awaits a check step with no run of its own, on a writer member placed on a
// server, answers from the writer's newest closed round's gate record once the
// pull has installed it; until then it stays where it is. Every other chain is
// left alone, so a local check (which has its own row) is never touched here.
func chainTickPlacedCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) error {
	if len(c.WorkflowJSON) == 0 || c.Status != string(chain.StatusRunning) {
		return nil
	}
	st, err := chainWorkflowState(c)
	if err != nil {
		return err
	}
	// Only a check awaiting is answered here: a run step has a member, and a
	// check already given a run has nothing left to look for.
	if st.Awaiting.Step == "" || st.Awaiting.Member != "" || st.Awaiting.Run != 0 {
		return nil
	}
	def, err := chainWorkflowDef(c)
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
	act := workflow.Action{Kind: workflow.ActionRunCheck, Step: st.Awaiting.Step, Command: step.Check}
	return chainFlowPullCheck(ctx, rt, tx, c, def, st, &st, workflow.Event{}, act, member)
}

// chainCheckSpec is the gate-runner spec for one chain check: the chain's tree,
// the gate scope unit, and the policy's gate timeout. A check has no binding,
// so it takes the policy timeout and no CPU pin.
func chainCheckSpec(rt Runtime, c db.ChainRow, run int, command, logPath string) gateProcSpec {
	return gateProcSpec{
		Dir:      c.Worktree,
		Command:  command,
		LogPath:  logPath,
		UnitName: scopeUnitNameFor(scopeGate, c.Owner, c.Name+"-check", run, ""),
		Timeout:  rt.Policy.GateTimeout(),
	}
}

// chainCheckVisit is the visit a run is for its step: one plus the runs already
// recorded for it.
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

// chainCheckLogTarget names the member and round a chain's check log is keyed
// to: the chain's writer binding and its newest closed round. A chain with no
// writer uses its first member.
func chainCheckLogTarget(tx *store.Tx, c db.ChainRow) (string, int, error) {
	member := c.Builder
	if member == "" {
		if members := chainMembersOf(c); len(members) > 0 {
			member = members[0]
		}
	}
	if member == "" {
		return "", 0, fmt.Errorf("chain %s has no member to hold a check log", c.Name)
	}
	round := memberNewestClosedRound(tx, member)
	if round < 1 {
		round = 1
	}
	return member, round, nil
}

// chainCheckSealLog writes the run's streamed log into its round_file row and
// removes the on-disk copy: the row is the record, exactly as a round's diff
// is. A run that wrote nothing seals an empty row.
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

// chainCheckResult maps the runner's result word onto the engine's: a pass is
// green and every settled non-pass, a failure, a timeout or an error, is red.
func chainCheckResult(raw string) string {
	if raw == "pass" {
		return chainCheckGreen
	}
	return chainCheckRed
}
