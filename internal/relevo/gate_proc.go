package relevo

import (
	"context"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// gateProcSpec is one process started through the gate runner's mechanics,
// freed of any binding: the tree it runs in, the shell command, the log it
// streams to, its scope unit and CPU pin, and the timeout that bounds it. A
// binding's round gate and a chain's check both start through this.
type gateProcSpec struct {
	Dir      string
	Command  string
	LogPath  string
	UnitName string
	CPUPin   string
	Timeout  time.Duration
}

// startGateProc starts spec's command and records the handle. A Start failure
// returns a non-nil record with Result "error" and a nil error. attempt is
// stored on the run: 0 for a first run, 1 for the single re-run allowed after
// a daemon restart took the first one with it.
func startGateProc(ctx context.Context, rt Runtime, spec gateProcSpec, attempt int) (store.GateRun, *store.GateRecord, error) {
	h, err := rt.Runner.Start(ctx, spawn.ProcSpec{
		Dir:        spec.Dir,
		Argv:       []string{"sh", "-c", spec.Command + " 2>&1"},
		LogPath:    spec.LogPath,
		StreamPath: spec.LogPath,
		Scope:      scopeFor(rt, scopeGate, spec.UnitName, spec.CPUPin),
	})
	if err != nil {
		return store.GateRun{}, &store.GateRecord{Command: spec.Command, Result: "error", Note: err.Error(), LogPath: spec.LogPath}, nil
	}
	// The daemon has now seen this process alive: a later tick never judges it
	// lost to a restart.
	rt.Watched.Mark(h.PID, h.StartedAt.Unix())
	return store.GateRun{PID: h.PID, StartedAt: h.StartedAt.Unix(), Command: spec.Command, Attempt: attempt}, nil, nil
}

// advanceGateProc advances spec's run one tick. next is non-nil only when the
// run must be replaced: the single re-run after a restart loss, carrying
// Attempt 1. Otherwise next is nil and done reports whether the run ended,
// with rec the record to attach. A run still in flight returns done == false
// and rec == nil, so the caller's handle stands.
func advanceGateProc(ctx context.Context, rt Runtime, run store.GateRun, spec gateProcSpec) (next *store.GateRun, done bool, rec *store.GateRecord, err error) {
	h := spawn.ProcHandle{PID: run.PID, StartedAt: time.Unix(run.StartedAt, 0)}
	alive, aerr := rt.Runner.Alive(ctx, h)
	if aerr != nil {
		// An OS hiccup is not evidence the process stopped: treat it as alive
		// this tick, the same rule the headless path applies.
		slog.Warn("gate liveness check failed; treating as alive", "pid", run.PID, "err", aerr)
		alive = true
	}
	if aerr == nil && alive {
		rt.Watched.Mark(run.PID, run.StartedAt)
	}
	elapsed := rt.Now().Sub(time.Unix(run.StartedAt, 0))

	if alive {
		if elapsed >= spec.Timeout {
			if kerr := rt.Runner.Kill(ctx, h, spec.LogPath); kerr != nil {
				slog.Warn("gate timeout kill failed", "pid", run.PID, "err", kerr)
			}
			return nil, true, &store.GateRecord{Command: run.Command, Result: "timeout", DurationMS: elapsed.Milliseconds(), LogPath: spec.LogPath}, nil
		}
		return nil, false, nil, nil
	}

	code, ok := rt.Runner.ExitCode(ctx, h, spec.LogPath)
	rec = &store.GateRecord{Command: run.Command, LogPath: spec.LogPath, DurationMS: elapsed.Milliseconds()}
	switch {
	case !ok:
		// A process this daemon never saw alive, whose recorded start predates
		// the daemon, was taken down by the daemon's own restart: run it once
		// more, as Attempt 1, instead of reporting the interruption as the
		// command's failure. Attempt 1 is the last one: a second loss reports
		// an error rather than starting a third run.
		if run.Attempt == 0 && lostToRestart(rt, run.PID, run.StartedAt) {
			nr, rrec, rerr := startGateProc(ctx, rt, spec, 1)
			if rrec != nil {
				return nil, true, rrec, rerr // the re-run could not start: report it
			}
			return &nr, false, nil, rerr
		}
		rec.Result = "error"
		rec.Note = "no exit trailer"
	case code == 0:
		rec.Result = "pass"
		rec.ExitCode = 0
	default:
		rec.Result = "fail"
		rec.ExitCode = code
	}
	return nil, true, rec, nil
}
