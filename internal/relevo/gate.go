package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// gateTailLines is how much of the gate's log a failing result carries in
// the payload: enough to see why it failed, not enough to flood it (#132).
const gateTailLines = 5

// gateStep advances the gate for a round whose marker exists. Pure with
// respect to the store: it touches only rt.Runner, the clock and files under
// the binding dir. Returns the updated binding, and either done == false
// (the gate is running; caller returns gating) or done == true with the
// record to attach to the report.
//
//	b.Gate == ""                         -> done, rec == nil          (no gate; zero cost)
//	rt.Runner == nil                     -> done, rec{Result:"error", Note:"no runner"}
//	GateRun == nil                       -> Start; on error rec{Result:"error", Note: err}; else GateRun set, KindGate entry appended, done == false
//	GateRun != nil, Alive                -> if now - StartedAt >= timeout: Kill, rec{Result:"timeout"}; else done == false
//	GateRun != nil, exited               -> code, ok := ExitCode(stream); !ok -> rec{Result:"error", Note:"no exit trailer"}; code == 0 -> "pass"; else "fail"
//
// On done, GateRun is cleared on the returned binding.
func gateStep(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, *store.GateRecord, error) {
	if b.Gate == "" {
		return b, true, nil, nil
	}

	log := rt.Store.GateLogPath(b.Name, b.Round)

	if rt.Runner == nil {
		return b, true, &store.GateRecord{Command: b.Gate, Result: "error", Note: "no runner", LogPath: log}, nil
	}

	if b.GateRun == nil {
		b, rec, err := startGate(ctx, rt, tx, b, 0, "gate started: "+b.Gate)
		if rec != nil {
			return b, true, rec, err
		}
		return b, false, nil, err
	}

	h := spawn.ProcHandle{PID: b.GateRun.PID, StartedAt: time.Unix(b.GateRun.StartedAt, 0)}
	alive, err := rt.Runner.Alive(ctx, h)
	if err != nil {
		// An OS hiccup is not evidence the gate stopped: treat it as alive
		// this tick, the same rule the headless path applies (spec §6).
		slog.Warn("gate liveness check failed; treating as alive", "binding", b.Name, "pid", b.GateRun.PID, "err", err)
		alive = true
	}
	if err == nil && alive {
		// A sighting: this daemon now knows the gate is running (#370, spec
		// §4.2), so its own restart is never blamed for this process later.
		rt.Watched.Mark(b.GateRun.PID, b.GateRun.StartedAt)
	}
	elapsed := rt.Now().Sub(time.Unix(b.GateRun.StartedAt, 0))

	if alive {
		if elapsed >= gateTimeoutFor(b, rt.Policy) {
			if err := rt.Runner.Kill(ctx, h, log); err != nil {
				slog.Warn("gate timeout kill failed", "binding", b.Name, "pid", b.GateRun.PID, "err", err)
			}
			rec := &store.GateRecord{Command: b.GateRun.Command, Result: "timeout", DurationMS: elapsed.Milliseconds(), LogPath: log}
			b.GateRun = nil
			return b, true, rec, nil
		}
		return b, false, nil, nil
	}

	code, ok := rt.Runner.ExitCode(ctx, h, log)
	rec := &store.GateRecord{Command: b.GateRun.Command, LogPath: log, DurationMS: elapsed.Milliseconds()}
	switch {
	case !ok:
		// A gate this daemon never saw alive, whose recorded start predates
		// the daemon, was taken down by the daemon's own restart (#370, spec
		// §4.4): run it once more, as Attempt 1, instead of reporting the
		// interruption as the gate's failure. Attempt 1 is the last one: a
		// second loss reports an error rather than starting a third run.
		if b.GateRun.Attempt == 0 && lostToRestart(rt, b.GateRun.PID, b.GateRun.StartedAt) {
			cmd := b.GateRun.Command
			b.GateRun = nil
			var rrec *store.GateRecord
			var rerr error
			b, rrec, rerr = startGate(ctx, rt, tx, b, 1, "gate restarted (lost to a daemon restart): "+cmd)
			if rrec != nil {
				return b, true, rrec, rerr // the re-run could not start: report it
			}
			return b, false, nil, rerr
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
	b.GateRun = nil
	return b, true, rec, nil
}

// startGate starts the round's gate as a process and records the handle: it is
// gateStep's GateRun == nil body, factored out so that a gate lost to a daemon
// restart is started again exactly as the first run was (#370, spec §4.4).
// attempt is stored on the new GateRun (0 for the round's first run, 1 for the
// one re-run after a loss) and note is the KindGate entry's note. A Start
// failure returns a non-nil GateRecord{Result: "error"} and a nil error,
// exactly as the inlined branch did.
func startGate(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, attempt int, note string) (store.Binding, *store.GateRecord, error) {
	log := rt.Store.GateLogPath(b.Name, b.Round)
	h, err := rt.Runner.Start(ctx, spawn.ProcSpec{
		Dir:        roundTree(rt, b),
		Argv:       []string{"sh", "-c", b.Gate + " 2>&1"},
		LogPath:    log,
		StreamPath: log,
		Scope:      scopeFor(rt, scopeGate, scopeUnitNameFor(scopeGate, b.Owner, b.Name, b.Round, ""), cpuPinText(b)),
	})
	if err != nil {
		return b, &store.GateRecord{Command: b.Gate, Result: "error", Note: err.Error(), LogPath: log}, nil
	}
	// The daemon has now seen this gate alive (#370, spec §4.2): a later tick
	// never judges it lost to a restart.
	rt.Watched.Mark(h.PID, h.StartedAt.Unix())
	b.GateRun = &store.GateRun{PID: h.PID, StartedAt: h.StartedAt.Unix(), Round: b.Round, Command: b.Gate, Attempt: attempt}
	if err := tx.AppendLog(b.Name, store.LogEntry{
		TS:        rt.Now().UTC(),
		Round:     b.Round,
		Direction: store.DirToPlanner,
		Kind:      store.KindGate,
		Path:      log,
		Note:      note,
		Confirmed: true,
	}); err != nil {
		return b, nil, err
	}
	return b, nil, nil
}

// gateTimeoutFor is the timeout that bounds one gate run: the binding's own
// override when set, else the policy default.
func gateTimeoutFor(b store.Binding, pol policy.Policy) time.Duration {
	if b.GateTimeoutMS > 0 {
		return time.Duration(b.GateTimeoutMS) * time.Millisecond
	}
	return pol.GateTimeout()
}

// gateLine is the payload line describing a gate's result. name and round are
// the binding and the round the gate ran for, so the line points the planner
// at `relevo show <name> --round <round> --gate` rather than the log's path
// (P4a round 2 §4.2):
//
//	"Gate: make check -- PASS (exit 0, 1m40s). Output: relevo show webshop --round 1 --gate"
//
// fail adds the tail:
//
//	"Gate: make check -- FAIL (exit 2, 1m40s). Output: relevo show …\n  <last 5 non-empty lines of the log, each indented two spaces>"
//
// timeout: "Gate: make check -- TIMEOUT after 10m0s. Output: relevo show …"
// error:   "Gate: make check -- ERROR: <note>."
func gateLine(name string, round int, rec store.GateRecord, tail []string) string {
	dur := time.Duration(rec.DurationMS) * time.Millisecond
	out := showCommand(name, round, "gate")
	switch rec.Result {
	case "pass":
		return fmt.Sprintf("Gate: %s -- PASS (exit %d, %s). Output: %s", rec.Command, rec.ExitCode, dur, out)
	case "fail":
		var sb strings.Builder
		fmt.Fprintf(&sb, "Gate: %s -- FAIL (exit %d, %s). Output: %s", rec.Command, rec.ExitCode, dur, out)
		for _, l := range tail {
			sb.WriteString("\n  ")
			sb.WriteString(l)
		}
		return sb.String()
	case "timeout":
		return fmt.Sprintf("Gate: %s -- TIMEOUT after %s. Output: %s", rec.Command, dur, out)
	default: // "error"
		return fmt.Sprintf("Gate: %s -- ERROR: %s.", rec.Command, rec.Note)
	}
}

// tailLines returns the last n non-empty lines of the file at path, or nil
// when it cannot be read; never an error (the log is a convenience). Lines
// carrying the rusage trailer are skipped (#313): any scoped spawn prints one
// before its exit trailer, and it is not gate output. A pre-rename log's
// relay-rusage: line is skipped the same way (#292 §1). // name-guard: legacy
func tailLines(read func(string) ([]byte, error), path string, n int) []string {
	data, err := read(path)
	if err != nil {
		return nil
	}
	var nonEmpty []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, spawn.RusageTrailerPrefix) || strings.HasPrefix(l, legacy.RusageTrailer) {
			continue
		}
		nonEmpty = append(nonEmpty, l)
	}
	if len(nonEmpty) > n {
		nonEmpty = nonEmpty[len(nonEmpty)-n:]
	}
	return nonEmpty
}
