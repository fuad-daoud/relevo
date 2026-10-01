package relevo

import (
	"context"
	"fmt"
	"strings"
	"time"

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

	spec := gateProcSpec{
		Dir:      roundTree(rt, b),
		Command:  b.Gate,
		LogPath:  rt.Store.GateLogPath(b.Name, b.Round),
		UnitName: scopeUnitNameFor(scopeGate, b.Owner, b.Name, b.Round, ""),
		CPUPin:   cpuPinText(b),
		Timeout:  gateTimeoutFor(b, rt.Policy),
	}

	if rt.Runner == nil {
		return b, true, &store.GateRecord{Command: b.Gate, Result: "error", Note: "no runner", LogPath: spec.LogPath}, nil
	}

	if b.GateRun == nil {
		run, rec, err := startGateProc(ctx, rt, spec, 0)
		if rec != nil {
			return b, true, rec, err
		}
		run.Round = b.Round
		b.GateRun = &run
		if err := appendGateLog(rt, tx, b, "gate started: "+b.Gate); err != nil {
			return b, false, nil, err
		}
		return b, false, nil, nil
	}

	next, done, rec, err := advanceGateProc(ctx, rt, *b.GateRun, spec)
	if next != nil {
		next.Round = b.Round
		b.GateRun = next
		if err := appendGateLog(rt, tx, b, "gate restarted (lost to a daemon restart): "+spec.Command); err != nil {
			return b, false, nil, err
		}
		return b, false, nil, nil
	}
	if done {
		b.GateRun = nil
	}
	return b, done, rec, err
}

// appendGateLog records a gate run's start on the binding's log, so the
// mastermind can see when a gate began or was re-run after a restart.
func appendGateLog(rt Runtime, tx *store.Tx, b store.Binding, note string) error {
	return tx.AppendLog(b.Name, store.LogEntry{
		TS:        rt.Now().UTC(),
		Round:     b.Round,
		Direction: store.DirToMasterMind,
		Kind:      store.KindGate,
		Path:      rt.Store.GateLogPath(b.Name, b.Round),
		Note:      note,
		Confirmed: true,
	})
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
// the binding and the round the gate ran for, so the line points the mastermind
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
// before its exit trailer, and it is not gate output.
func tailLines(read func(string) ([]byte, error), path string, n int) []string {
	data, err := read(path)
	if err != nil {
		return nil
	}
	var nonEmpty []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, spawn.RusageTrailerPrefix) {
			continue
		}
		nonEmpty = append(nonEmpty, l)
	}
	if len(nonEmpty) > n {
		nonEmpty = nonEmpty[len(nonEmpty)-n:]
	}
	return nonEmpty
}
