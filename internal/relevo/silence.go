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

// silenceVerdict reports whether a model has gone silent: no stream byte for
// at least after, while the process tree is known not to be burning CPU. An
// unknown CPU never reads as silent, because a failed read is not evidence.
func silenceVerdict(lastEvent, now time.Time, after time.Duration, cpuBusy, cpuKnown bool) (silent bool, quiet time.Duration) {
	quiet = now.Sub(lastEvent)
	return cpuKnown && !cpuBusy && quiet >= after, quiet
}

// silenceDue reports whether b is a live builder of a kind silence applies to
// in an open round.
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
	started := time.Unix(b.Builder.StartedAt, 0)
	for _, e := range entries {
		if e.Round == b.Round && e.Kind == store.KindSwitch &&
			strings.HasPrefix(e.Note, providerStallNotePrefix) && e.TS.After(started) {
			return nil
		}
	}
	slog.Warn("provider stall", "binding", b.Name, "round", b.Round, "quiet", quiet.Truncate(time.Second))
	return tx.AppendLog(b.Name, store.LogEntry{
		TS: now, Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindSwitch, Confirmed: true,
		Note: fmt.Sprintf("%s no model output for %s while the builder burned no CPU", providerStallNotePrefix, view.AgeText(quiet)),
	})
}
