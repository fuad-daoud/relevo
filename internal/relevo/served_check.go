package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// maxServedCheckCommand is the longest acceptance command a served check may
// carry. The command runs through `sh -c` in the binding's worktree on the
// server and is stored on the binding, so a bound keeps one unbounded line out
// of both.
const maxServedCheckCommand = 512

// maxServedCheckID is the longest run id a caller may choose. The id is the
// key a repeated request answers from, so it is stored beside the run it names.
const maxServedCheckID = 128

// servedCheckLogCap is how much of a check's log its view carries. The log is
// what a client shows to explain a failure, so the bound keeps one answer from
// growing with whatever the check chose to print.
const servedCheckLogCap = 64 << 10

// ErrCheckRunning reports that the binding already has a check in flight, so a
// second one cannot start until the first ends.
var ErrCheckRunning = errors.New("relevo: a check is already running on this binding")

// ErrNoCheck reports that the binding holds no run under the requested id.
var ErrNoCheck = errors.New("relevo: no such check run on this binding")

// ErrReaderHasNoCheck reports that a reader round has no check: a reader only
// leaves artifacts, so there is nothing for a check to accept and nothing to
// run one against.
var ErrReaderHasNoCheck = errors.New("relevo: a reader round has no check")

// ErrNoWorktree reports that the binding has no tree to run a check in.
var ErrNoWorktree = errors.New("relevo: binding has no worktree to check")

// ErrInvalidCheck reports a check or gate request that cannot be stored or run
// as it stands. It carries the reason, so a caller can say which field to fix.
var ErrInvalidCheck = errors.New("relevo: invalid check request")

// ServedCheckStart starts one check run on a served binding and returns its
// view plus whether this call was the one that started it.
//
// It is idempotent on the run's id: a repeated request naming the run the
// binding already holds answers from that record instead of starting a second
// one, so a client that lost the first answer learns the run's state rather
// than doubling it. A different id while a run is still in flight is
// ErrCheckRunning, and only a settled run may be replaced.
//
// The record stays on the binding after it settles. That is what makes the
// repeat and a later read stable answers, and it is why the binding holds one
// run rather than a history.
func ServedCheckStart(ctx context.Context, rt Runtime, name string, req remote.CreateCheckRequest) (remote.CheckView, bool, error) {
	if err := validServedCheck(req); err != nil {
		return remote.CheckView{}, false, err
	}
	var (
		out     remote.CheckView
		created bool
	)
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		if err := servedCheckRunnable(b); err != nil {
			return err
		}
		if b.CheckRun != nil {
			if b.CheckRun.ID == req.ID {
				out = servedCheckView(rt, *b.CheckRun)
				return nil
			}
			if !b.CheckRun.Settled() {
				return ErrCheckRunning
			}
		}
		run, err := startServedCheck(ctx, rt, b, req)
		if err != nil {
			return err
		}
		b.CheckRun = run
		if err := tx.Save(b); err != nil {
			return err
		}
		out, created = servedCheckView(rt, *run), true
		return nil
	})
	if err != nil {
		return remote.CheckView{}, false, err
	}
	return out, created, nil
}

// ServedCheckGet returns the view of one check run on a served binding. The
// binding holds the run its client last asked for, so an id that does not name
// it is ErrNoCheck rather than a run from an earlier visit.
func ServedCheckGet(rt Runtime, name, id string) (remote.CheckView, error) {
	if id == "" {
		return remote.CheckView{}, ErrNoCheck
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		return remote.CheckView{}, err
	}
	if b.CheckRun == nil || b.CheckRun.ID != id {
		return remote.CheckView{}, ErrNoCheck
	}
	return servedCheckView(rt, *b.CheckRun), nil
}

// ServedSetGate writes the acceptance command and the repair budget onto a
// served binding, so a chain placed before the check route existed still runs
// the gate its own rounds carry.
//
// Both fields are optional and a nil leaves the stored value alone, so a caller
// can move one without naming the other. A reader is refused: its rounds have
// no gate, and one set here would promise a check that never runs.
func ServedSetGate(ctx context.Context, rt Runtime, name string, req remote.SetGateRequest) error {
	if err := validServedGate(req); err != nil {
		return err
	}
	return rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		if b.Shape == store.ShapeReader {
			return ErrReaderHasNoCheck
		}
		if req.Gate != nil {
			b.Gate = *req.Gate
		}
		if req.Regate != nil {
			b.Regate = *req.Regate
		}
		return tx.Save(b)
	})
}

// validServedCheck refuses a check request that cannot be stored or run: a
// missing or oversized id, or a missing or oversized command.
func validServedCheck(req remote.CreateCheckRequest) error {
	if req.ID == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidCheck)
	}
	if len(req.ID) > maxServedCheckID {
		return fmt.Errorf("%w: id must be at most %d bytes", ErrInvalidCheck, maxServedCheckID)
	}
	if req.Command == "" {
		return fmt.Errorf("%w: command is required", ErrInvalidCheck)
	}
	if len(req.Command) > maxServedCheckCommand {
		return fmt.Errorf("%w: command must be at most %d bytes", ErrInvalidCheck, maxServedCheckCommand)
	}
	return nil
}

// validServedGate refuses a gate update that names nothing to write, an
// oversized command, or a negative repair budget. A negative budget has no
// meaning as a count of rounds and would be stored as one.
func validServedGate(req remote.SetGateRequest) error {
	if req.Gate == nil && req.Regate == nil {
		return fmt.Errorf("%w: gate or regate is required", ErrInvalidCheck)
	}
	if req.Gate != nil && len(*req.Gate) > maxServedCheckCommand {
		return fmt.Errorf("%w: gate must be at most %d bytes", ErrInvalidCheck, maxServedCheckCommand)
	}
	if req.Regate != nil && *req.Regate < 0 {
		return fmt.Errorf("%w: regate must not be negative", ErrInvalidCheck)
	}
	return nil
}

// servedCheckRunnable refuses a check the binding cannot run: a reader round has
// no check, and a binding with no worktree has no tree to run one in.
func servedCheckRunnable(b store.Binding) error {
	if b.Shape == store.ShapeReader {
		return ErrReaderHasNoCheck
	}
	if b.Worktree == "" {
		return ErrNoWorktree
	}
	return nil
}

// startServedCheck begins req's process and returns the record to store. A
// command that cannot be started is recorded as an errored run rather than
// refused, so the client reads the failure from the same run it asked for
// instead of seeing an error and no record of what was tried.
func startServedCheck(ctx context.Context, rt Runtime, b store.Binding, req remote.CreateCheckRequest) (*store.CheckRun, error) {
	run := store.CheckRun{
		ID:      req.ID,
		Command: req.Command,
		Step:    req.Step,
		Round:   b.Round,
		LogPath: rt.Store.ServedCheckLogPath(b.Name, b.Round),
	}
	started, rec, err := startGateProc(ctx, rt, servedCheckSpec(rt, b, run), 0)
	if rec != nil {
		applyServedCheckRecord(&run, rec)
		return &run, err
	}
	if err != nil {
		return nil, err
	}
	run.PID = started.PID
	run.StartedAt = started.StartedAt
	run.Attempt = started.Attempt
	return &run, nil
}

// servedCheckStep advances the binding's stored check run one tick through the
// same gate-runner mechanics a round's gate and a chain's check use. changed is
// false when the tick found nothing to record, so a caller knows whether it
// must save.
//
// A run lost to a daemon restart is started once more as Attempt 1, the
// allowance the gate's own runner makes; a second loss settles as an error
// rather than a third process.
func servedCheckStep(ctx context.Context, rt Runtime, b store.Binding) (store.Binding, bool) {
	if b.CheckRun == nil || b.CheckRun.Settled() {
		return b, false
	}
	run := *b.CheckRun
	spec := servedCheckSpec(rt, b, run)

	next, done, rec, err := advanceGateProc(ctx, rt, servedCheckGateRun(run), spec)
	switch {
	case err != nil:
		// Settling the run is what keeps the binding out of check_running: a
		// step that returned early would leave the next POST refused forever.
		slog.Warn("served check advance failed", "binding", b.Name, "err", err)
		run.Result, run.Note = "error", err.Error()
		b.CheckRun = &run
		return b, true
	case next != nil:
		run.PID, run.StartedAt, run.Attempt = next.PID, next.StartedAt, next.Attempt
		b.CheckRun = &run
		return b, true
	case !done:
		return b, false
	}
	if rec != nil {
		applyServedCheckRecord(&run, rec)
	}
	b.CheckRun = &run
	return b, true
}

// applyServedCheckRecord copies a finished run's outcome onto the stored run.
func applyServedCheckRecord(run *store.CheckRun, rec *store.GateRecord) {
	run.Result = rec.Result
	run.ExitCode = rec.ExitCode
	run.DurationMS = rec.DurationMS
	run.Note = rec.Note
	if rec.LogPath != "" {
		run.LogPath = rec.LogPath
	}
}

// servedCheckSpec is the gate-runner spec for one served check run: the
// binding's own worktree, a gate scope named after the binding and the round,
// and the policy's gate timeout. The timeout comes from policy and not from the
// binding because a check is the client's own acceptance command, not the
// round's stored gate.
func servedCheckSpec(rt Runtime, b store.Binding, run store.CheckRun) gateProcSpec {
	return gateProcSpec{
		Dir:      b.Worktree,
		Command:  run.Command,
		LogPath:  run.LogPath,
		UnitName: scopeUnitNameFor(scopeGate, b.Owner, b.Name+"-check", run.Round, ""),
		Timeout:  rt.Policy.GateTimeout(),
	}
}

// servedCheckGateRun is the stored run as the gate runner's own advance sees
// it: the process handle, the round and the attempt, and nothing else. The
// stored run's id and step do not belong to the mechanics, so they do not
// travel.
func servedCheckGateRun(r store.CheckRun) store.GateRun {
	return store.GateRun{
		PID:       r.PID,
		StartedAt: r.StartedAt,
		Round:     r.Round,
		Command:   r.Command,
		Attempt:   r.Attempt,
	}
}

// servedCheckView is the wire view of one stored run.
func servedCheckView(rt Runtime, run store.CheckRun) remote.CheckView {
	tail, cut := servedCheckLogTail(rt.Store.ReadFile, run.LogPath)
	return remote.CheckView{
		ID:           run.ID,
		Command:      run.Command,
		Step:         run.Step,
		Result:       run.Result,
		ExitCode:     run.ExitCode,
		DurationMS:   run.DurationMS,
		Note:         run.Note,
		LogTail:      tail,
		LogTruncated: cut,
	}
}

// servedCheckLogTail is the last servedCheckLogCap bytes of the log at path,
// and whether the cut was made. The end is kept: a failed check says why in its
// last lines, and a prefix would hide exactly that. A log that cannot be read
// has no tail, which is not a failure -- the run's result is the record and the
// log is a convenience beside it.
func servedCheckLogTail(read func(string) ([]byte, error), path string) (string, bool) {
	if path == "" {
		return "", false
	}
	data, err := read(path)
	if err != nil {
		return "", false
	}
	if len(data) > servedCheckLogCap {
		return string(data[len(data)-servedCheckLogCap:]), true
	}
	return string(data), false
}

// tickServedChecks advances every served binding's check run once per tick, one
// check per binding: a check is a single process against one tree, so a binding
// never has two to advance. It rides the daemon tick, the one that advances a
// round's gate, so a check keeps moving across the ticks between a round's
// marker and the next round.
func tickServedChecks(ctx context.Context, rt Runtime) {
	bindings, err := rt.Store.List()
	if err != nil {
		slog.Warn("served check tick: list bindings", "err", err)
		return
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, listed := range bindings {
			tickServedCheck(ctx, rt, tx, listed.Name)
		}
		return nil
	}); err != nil {
		slog.Warn("served check tick", "err", err)
	}
}

// tickServedCheck advances one binding's run. Each binding settles on its own,
// so one refusal never holds the rest of the sweep back.
func tickServedCheck(ctx context.Context, rt Runtime, tx *store.Tx, name string) {
	b, err := tx.Load(name)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		slog.Warn("served check tick", "binding", name, "err", err)
		return
	}
	if b.Serve == nil || b.CheckRun == nil {
		return
	}
	next, changed := servedCheckStep(ctx, rt, b)
	if !changed {
		return
	}
	if err := tx.Save(next); err != nil {
		slog.Warn("served check tick: save", "binding", name, "err", err)
	}
}
