package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

const providerStallNotePrefix = "provider stall:"

// silenceCPUFloor is the CPU a builder's process tree must burn between two
// samples to count as busy: 2 CPU-seconds in clock ticks (100 Hz). opencode
// idles at under 1 percent of a core, while a test suite or `go build` burns
// whole cores.
const silenceCPUFloor = 2 * 100

// silenceKinds are the builder kinds whose silence is told apart from a stall.
var silenceKinds = map[string]bool{"opencode": true}

// silenceVerdict reports whether a model has gone silent. An unknown CPU
// never reads as silent, because a failed read is not evidence.
func silenceVerdict(lastEvent, now time.Time, after time.Duration, cpuBusy, cpuKnown bool) (silent bool, quiet time.Duration) {
	quiet = now.Sub(lastEvent)
	return cpuKnown && !cpuBusy && quiet >= after, quiet
}

func silenceDue(b store.Binding) bool {
	return silenceKinds[b.Builder.Kind] && b.Builder.PID != 0 && !b.RoundStartedAt.IsZero()
}

// silenceOf is the verdict for a binding at now. CPUTicksAt is the last time
// the tree burned CPU, so the tree is busy while that is within the window.
// The last event is the stream file's mtime, which counts any bytes the
// process wrote, stderr included.
func silenceOf(rt Runtime, b store.Binding, now time.Time) (bool, time.Duration) {
	if !silenceDue(b) || b.Progress == nil || b.Progress.CPUTicksAt.IsZero() {
		return false, 0
	}
	after := rt.Policy.SilentAfter()
	busy := now.Sub(b.Progress.CPUTicksAt) < after
	return silenceVerdict(streamLastActivity(rt, b), now, after, busy, true)
}

// silenceLabel is the status word for a silent model, or "". A stalled label
// outranks it; it replaces exploring and working.
func silenceLabel(rt Runtime, b store.Binding, now time.Time, working string) string {
	if strings.HasPrefix(working, "stalled") {
		return working
	}
	if silent, quiet := silenceOf(rt, b, now); silent {
		return fmt.Sprintf("model silent for %d min", int(quiet/time.Minute))
	}
	return working
}

// noteProviderStall logs one provider stall line per silence episode: an
// episode is a builder process, and the log already holds the marker.
func noteProviderStall(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, now time.Time) error {
	silent, quiet := silenceOf(rt, b, now)
	if !silent {
		return nil
	}
	if stalledSince(b, entries) {
		return nil
	}
	slog.Warn("provider stall", "binding", b.Name, "round", b.Round, "quiet", quiet.Truncate(time.Second))
	return tx.AppendLog(b.Name, store.LogEntry{
		TS: now, Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindSwitch, Confirmed: true,
		Note: fmt.Sprintf("%s no model output for %s while the builder burned no CPU", providerStallNotePrefix, view.AgeText(quiet)),
	})
}

// actOnSilence acts on a silent model. The first silence in a session aborts
// the process; the exit path then resumes the same session. A second incident
// in the session, or a session with no id, switches candidate like a gate.
func actOnSilence(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, now time.Time) (store.Binding, bool, error) {
	silent, quiet := silenceOf(rt, b, now)
	if !silent {
		return b, false, nil
	}
	sess := b.Builder.StreamSessionID
	if sess != "" && sameSessionResumes(entries, b.Round, sess) == 0 {
		if err := noteProviderStall(ctx, rt, tx, b, entries, now); err != nil {
			return b, false, err
		}
		if _, err := stopProcess(ctx, rt, b, "provider stall"); err != nil {
			slog.Warn("provider stall abort failed", "binding", b.Name, "round", b.Round, "err", err)
			return b, false, nil
		}
		return b, true, nil
	}
	b.RoundExcluded = appendUnique(b.RoundExcluded, b.BuilderCandidate)
	reason := fmt.Sprintf("%s no stream event for %d min", providerStallNotePrefix, int(quiet/time.Minute))
	next, err := switchBuilder(ctx, rt, tx, b, reason, true, false)
	return next, true, err
}
