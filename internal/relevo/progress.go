package relevo

import (
	"context"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
)

// signals is one tick's read of a binding's progress sources. A source
// that cannot be read is absent for that sample: sampleSignals leaves it at its
// zero value rather than inventing one, because a failed read is not evidence
// that a builder stopped.
type signals struct {
	// tree is the working tree's fingerprint; "" when unavailable (no git, or
	// no cwd to run it in).
	tree string
	// outputAt is a headless builder's streamLastActivity.
	outputAt time.Time
	// cpuTicks is the builder's process-tree CPU, in clock ticks; cpuOK is
	// false when it could not be read.
	cpuTicks uint64
	cpuOK    bool
}

// sampleSignals reads a binding's progress sources for one tick. It is
// read-only and best-effort: every failure is simply an absent signal.
func sampleSignals(ctx context.Context, rt Runtime, b store.Binding) signals {
	var s signals

	if rt.Git != nil && b.CWD != "" {
		if fp, err := rt.Git.TreeFingerprint(ctx, housekeep.RoundTree(HousekeepDeps(rt), b)); err == nil {
			s.tree = fp
		}
	}

	// A headless builder's liveness signal is the stream file's mtime.
	s.outputAt = streamLastActivity(rt, b)

	if silenceDue(b) {
		read := processTreeCPUTicks
		if rt.CPUTicks != nil {
			read = rt.CPUTicks
		}
		s.cpuTicks, s.cpuOK = read(b.Builder.PID)
	}

	return s
}

// progressStep advances a binding's progress clock by one tick.
//
// Precondition: the round is open (RoundStartedAt non-zero). The first call
// records the current signals and starts the clock from the round's start; a
// call inside policy.json's progress_interval_ms returns the binding unchanged.
//
// It sets and clears two labels:
//   - StalledSince when no signal has moved for stall_after_ms;
//   - ExploringSince when the output or screen has moved while the tree has
//     not for explore_after_ms, and the builder is not stalled.
func progressStep(rt Runtime, b store.Binding, now time.Time, s signals) store.Binding {
	if b.RoundStartedAt.IsZero() {
		return b
	}

	if b.Progress == nil {
		b.Progress = &store.Progress{
			SampledAt: now,
			Tree:      s.tree,
			TreeAt:    b.RoundStartedAt,
			OutputAt:  b.RoundStartedAt,
		}
	} else if now.Sub(b.Progress.SampledAt) < rt.Policy.ProgressInterval() {
		return b // not yet
	} else {
		b.Progress.SampledAt = now
	}

	p := b.Progress
	if s.tree != "" && s.tree != p.Tree {
		p.Tree, p.TreeAt = s.tree, now
	}
	if s.outputAt.After(p.OutputAt) {
		p.OutputAt = s.outputAt
	}

	stepCPU(p, s, now)

	// No signal has ever been readable: record the sample and set no label,
	// because the round's start is not evidence of a hung builder on its own.
	if !anySignal(b, p) {
		return b
	}

	last := p.TreeAt
	if p.OutputAt.After(last) {
		last = p.OutputAt
	}

	if now.Sub(last) >= rt.Policy.StallAfter() {
		if b.StalledSince.IsZero() {
			b.StalledSince = last
			slog.Warn("builder stalled", "binding", b.Name, "round", b.Round, "quiet", now.Sub(last).Truncate(time.Second))
		}
	} else {
		b.StalledSince = time.Time{}
	}

	if b.StalledSince.IsZero() && p.OutputAt.After(p.TreeAt) && now.Sub(p.TreeAt) >= exploreThreshold(rt, p) {
		if b.ExploringSince.IsZero() {
			b.ExploringSince = p.TreeAt
		}
	} else {
		b.ExploringSince = time.Time{}
	}

	return b
}

// stepCPU keeps CPUTicksAt at the last sample where the tree burned more than
// silenceCPUFloor. A tree whose ticks fell had a child exit, which burned CPU
// too. An unreadable sample clears it, so silence is never judged on stale CPU.
func stepCPU(p *store.Progress, s signals, now time.Time) {
	switch {
	case !s.cpuOK:
		p.CPUTicks, p.CPUTicksAt = 0, time.Time{}
	case p.CPUTicksAt.IsZero() || s.cpuTicks < p.CPUTicks || s.cpuTicks-p.CPUTicks > silenceCPUFloor:
		p.CPUTicks, p.CPUTicksAt = s.cpuTicks, now
	default:
		p.CPUTicks = s.cpuTicks
	}
}

// resumedIdleAfter is how long a resumed session may leave the tree alone
// before it is labelled exploring: about one full make check plus a retry,
// since a 3 h round polled for 188 min.
const resumedIdleAfter = 10 * time.Minute

// exploreThreshold is how long the tree may sit still while output moves
// before the exploring label is set.
func exploreThreshold(rt Runtime, p *store.Progress) time.Duration {
	if p.ResumedAt.IsZero() {
		return rt.Policy.ExploreAfter()
	}
	return min(rt.Policy.ExploreAfter(), resumedIdleAfter)
}

// anySignal reports whether at least one progress source was ever sampled
// non-empty. A headless stream's mtime counts, which is why it is compared
// against the round's start rather than tested for zero.
func anySignal(b store.Binding, p *store.Progress) bool {
	if p.Tree != "" || p.Output != "" {
		return true
	}
	return p.OutputAt.After(b.RoundStartedAt)
}
