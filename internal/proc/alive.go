//go:build unix

package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// Alive reports whether the handle's process exists, is not a zombie, and
// started within a second of when the handle says. A missing pid is (false,
// nil); only ps itself failing to run is an error.
func (r *Runner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	if h.PID <= 0 {
		return false, nil
	}
	started, state, err := psInfo(ctx, h.PID)
	if errors.Is(err, errNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return procFact{started: started, state: state}.alive(h, true), nil
}

// AliveBatch answers the same question Alive does for many handles at once, with
// one ps fork instead of one per handle: a status refresh probes every headless
// row it paints, and the per-row fork is the cost. The result is identical to
// calling Alive on each handle in turn -- the same pid, state and start-time
// rules, applied to one ps reading instead of many.
//
// Only ps itself failing to run is an error. A pid ps does not list is absent
// from the map, which reads as not alive, exactly as errNoProcess does for the
// single-handle path; the caller decides what an absent key means.
func (r *Runner) AliveBatch(ctx context.Context, handles []spawn.ProcHandle) (map[int]spawn.AliveFact, error) {
	out := make(map[int]spawn.AliveFact, len(handles))
	if len(handles) == 0 {
		return out, nil
	}
	pids := make([]string, 0, len(handles))
	seen := make(map[int]bool, len(handles))
	for _, h := range handles {
		if h.PID <= 0 || seen[h.PID] {
			continue
		}
		seen[h.PID] = true
		pids = append(pids, strconv.Itoa(h.PID))
	}
	if len(pids) == 0 {
		return out, nil
	}
	raw, err := exec.CommandContext(ctx, "ps", "-o", "pid=", "-o", "stat=", "-o", "lstart=", "-p", strings.Join(pids, ",")).Output()
	if err != nil {
		var exit *exec.ExitError
		errors.As(err, &exit) // nil when ps never ran (e.g. not on PATH)
		if classifyPS(ctx.Err(), exit, raw) == psNoProcess {
			return out, nil
		}
		return nil, fmt.Errorf("proc: ps: %w", err)
	}
	facts, err := parsePSFacts(raw)
	if err != nil {
		return nil, err
	}
	for pid, f := range facts {
		out[pid] = spawn.AliveFact{StartedAt: f.started, State: f.state}
	}
	return out, nil
}

// procFact is one process as ps reported it: its start time and its state. It
// is what Alive compares a handle against, shared by the single-handle and the
// batch path so both apply the same rules to the same fields.
type procFact struct {
	started time.Time
	state   string
}

// alive answers Alive for one handle against an already-read fact, so the batch
// path cannot drift from the single one: the pid-reuse window, the zombie rule
// and the missing-process rule are one implementation.
func (f procFact) alive(h spawn.ProcHandle, found bool) bool {
	if !found {
		return false
	}
	if strings.HasPrefix(f.state, "Z") {
		return false
	}
	diff := f.started.Sub(h.StartedAt)
	if diff < 0 {
		diff = -diff
	}
	return diff <= time.Second
}

// parsePSFacts reads the pid/stat/lstart triples AliveBatch asked ps for. A
// pid ps omitted is simply absent, so a missing process and a reused one stay
// distinguishable: absent means not alive, and Alive is the only caller that
// turns a fact into an answer.
func parsePSFacts(out []byte) (map[int]procFact, error) {
	facts := map[int]procFact{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 7 {
			return nil, fmt.Errorf("proc: unexpected ps output %q", strings.TrimSpace(line))
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("proc: parse ps pid %q: %w", fields[0], err)
		}
		started, err := time.ParseInLocation(psLayout, strings.Join(fields[2:7], " "), time.Local)
		if err != nil {
			return nil, fmt.Errorf("proc: parse ps start time %q: %w", strings.Join(fields[2:7], " "), err)
		}
		facts[pid] = procFact{started: started, state: fields[1]}
	}
	return facts, nil
}

// ExitCode reads the trailer the supervisor appended, if it is the stream's last
// line. A kill recorded for this handle returns ok=false regardless of what the
// stream ends with.
