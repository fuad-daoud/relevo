package relevo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// interruptedNoteFormat is the relaunch prompt's note: a constant text with
// the interruption time interpolated. It tells the builder three things:
// the round was interrupted by a relevo daemon restart at T; the working
// tree may already hold partial edits from an earlier attempt at this same
// plan, and those edits are its own; and it should run git status and git
// diff first, keep what is correct and finish the plan.
const interruptedNoteFormat = `This round was interrupted at %s by a relevo daemon restart. The working tree may already hold partial edits from an earlier attempt at this same plan, and those edits are your own work, not someone else's. Run "git status" and "git diff" first, keep whatever is correct, and finish the plan.`

// interruptedNote renders interruptedNoteFormat for the daemon restart at t.
// t is rendered as UTC RFC 3339, the same shape the relaunch log entry uses.
func interruptedNote(t time.Time) string {
	return fmt.Sprintf(interruptedNoteFormat, t.UTC().Format(time.RFC3339))
}

// ErrStopFailed reports that relevo marked a binding done or unbound but
// could not stop its headless process. The state change stands;
// the pid stays on the endpoint (done) or in the result (unbind) so the
// human can find the process.
var ErrStopFailed = errors.New("could not stop the builder process")

// stopProcess kills a headless endpoint's live process, if it has one. It
// returns the pid it addressed -- 0 when there was nothing to stop -- and
// Kill's error. A binding with no headless process, or one between rounds, is
// a no-op. Runner nil with a pid recorded is an error: relevo cannot say the
// process is stopped.
func stopProcess(ctx context.Context, rt Runtime, b store.Binding, why string) (int, error) {
	if !b.Builder.Headless() || b.Builder.PID == 0 {
		return 0, nil
	}
	if rt.Runner == nil {
		return b.Builder.PID, spawn.ErrRunnerUnavailable
	}
	if err := rt.Runner.Kill(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); err != nil {
		return b.Builder.PID, err
	}
	// The kill took the round's process, so its temp dir goes with it: a
	// later start recreates it under the same name, which is what makes
	// removing on a kill safe for a round that resumes.
	housekeep.RemoveRoundTmp(HousekeepDeps(rt), b, b.Round)
	return b.Builder.PID, nil
}

// statusTailLines is how much of the log `relevo status` shows under a
// headless builder line.
const statusTailLines = 3

// headlessStatus is what `relevo status` says about a headless endpoint: the
// status word and the process details. Idle between rounds; otherwise a live
// Alive check, then, for an exited process, the trailer's code. No Runner
// means relevo cannot say. A live process whose stream has gone quiet
// (binding.StalledSince) reads "stalled <age>" in place of "working".
//
// detail false builds no tail: the read is a whole-file logTail or a 64 KiB
// window per row per tick, and the fleet path paints none of it. The flag
// reaches here rather than the call site so the read is never made and then
// thrown away.
//
// alive is the report's batched liveness reading, so a refresh probes ps once
// rather than once per row. A nil map is the single-row path, which probes its
// one handle directly.
func headlessStatus(ctx context.Context, rt Runtime, b store.Binding, detail bool, alive map[spawn.ProcHandle]aliveAnswer) (string, *view.HeadlessInfo) {
	e := b.Builder
	info := &view.HeadlessInfo{PID: e.PID, LogPath: e.LogPath}
	if e.StartedAt != 0 {
		info.StartedAt = time.Unix(e.StartedAt, 0)
	}
	// The tail is read before the pid and runner checks, not after them: a
	// binding keeps its LogPath after its builder exits (reconcileHeadless
	// zeroes the pid and leaves the log), so a row with no process is still a
	// row whose log a human reads to find out why it stopped. Skipping the
	// read here would drop that tail from the surfaces that show it.
	if detail && e.LogPath != "" {
		if tail := builderTail(rt, b, statusTailLines); tail != "" {
			info.Tail = strings.Split(tail, "\n")
		}
	}
	if e.PID == 0 {
		return "idle", info
	}
	if rt.Runner == nil {
		return "unknown", info
	}
	h := handleOf(e)
	running, known := builderAlive(ctx, rt, h, alive)
	if !known {
		return "unknown", info
	}
	if !running {
		return exitedStatus(ctx, rt, b, info), info
	}
	// The working label comes from the progress labels, so a live
	// headless builder can also read "exploring <age>".
	working, _ := view.LabelsOf(b, rt.Now())
	if working = silenceLabel(rt, b, rt.Now(), working); working != "" {
		return working, info
	}
	return "working", info
}

// exitedStatus is the word and the code for a process that is no longer there:
// the trailer's code when the supervisor wrote one, else "unknown". It holds
// the exit read so the tail read and the exit read stay legible next to each
// other.
func exitedStatus(ctx context.Context, rt Runtime, b store.Binding, info *view.HeadlessInfo) string {
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); ok {
		info.ExitCode = strconv.Itoa(code)
		return "exited " + info.ExitCode
	}
	info.ExitCode = "unknown"
	return "exited"
}

// builderAlive is the report's batched liveness answer for h, or a direct probe
// when the report carries none. It reports ok=false when the process could not
// be probed at all, which is how "unknown" stays distinct from "exited": a ps
// that could not run says nothing about the process, and the row says so.
func builderAlive(ctx context.Context, rt Runtime, h spawn.ProcHandle, alive map[spawn.ProcHandle]aliveAnswer) (bool, bool) {
	if got, ok := alive[h]; ok {
		return got.alive, got.known
	}
	got, err := rt.Runner.Alive(ctx, h)
	if err != nil {
		return false, false
	}
	return got, true
}
