package housekeep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	// oomCooldown is the pause before a local round is re-admitted: the memory
	// freed by the kill takes a moment to be reclaimed, so an immediate
	// relaunch would meet the same pressure.
	oomCooldown = 60 * time.Second

	// oomMaxKills caps the re-queues in one round: pressure that never ends
	// must not loop forever.
	oomMaxKills = 3

	// scopeResultOOM is the systemd Result value for an oom-killed scope.
	scopeResultOOM = "oom-kill"
)

// OOMKilled reads the killed round's scope result from the user journal and
// reports the memory peak it reached, or (0, false) when there is nothing to
// report. It probes only a scoped round whose process start time is known,
// because only entries at or after that start belong to this attempt; anything
// else -- no scope, no probe, a probe error, a non-oom result -- counts as not
// oom-killed.
func OOMKilled(ctx context.Context, deps Deps, b store.Binding) (int64, bool) {
	if deps.Scope == nil || b.Builder.StartedAt == 0 {
		return 0, false
	}
	prober := deps.ScopeProber
	if prober == nil {
		return 0, false
	}
	since := time.Unix(b.Builder.StartedAt, 0).UTC()
	res, err := prober.ScopeResult(ctx, deps.UnitName(b), since)
	if err != nil {
		slog.Debug("scope result probe failed; treating as not oom-killed",
			"binding", b.Name, "round", b.Round, "err", err)
		return 0, false
	}
	return res.PeakBytes, res.Result == scopeResultOOM
}

// OOMWords is how every surface of an oom kill names it: a scoped cgroup was
// killed by the kernel's out-of-memory killer, with the journal's peak when it
// reported one.
func OOMWords(peak int64) string {
	if peak <= 0 {
		return "killed: out of memory (peak unknown)"
	}
	return fmt.Sprintf("killed: out of memory (peak %.1f GiB)", float64(peak)/(1<<30))
}

func runsLocally(b store.Binding) bool {
	return b.Owner == "" && !b.Builder.Remote() && b.Builder.Headless() && b.Builder.PID != 0
}

func localRunning(tx *store.Tx, self string) (int, error) {
	bindings, err := tx.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range bindings {
		if b.Name == self {
			continue
		}
		if runsLocally(b) {
			n++
		}
	}
	return n, nil
}

// RequeueOOM re-queues a local round whose builder the oom killer took, on the
// same candidate with no switch and no exclusion.
func RequeueOOM(ctx context.Context, deps Deps, tx *store.Tx, b store.Binding, peak int64, now time.Time) (store.Binding, error) {
	b.RoundOOMKills++
	b = deps.Abandon(b)
	b.Builder.PID, b.Builder.StartedAt = 0, 0
	b.Builder = deps.ClearProcess(b.Builder)
	b.StalledSince = time.Time{}

	if b.RoundOOMKills >= oomMaxKills {
		return deps.Halt(ctx, tx, b, fmt.Sprintf(
			"%s: builder %s %d times this round; free memory, then relevo send --name %s --file <plan> again",
			b.Name, OOMWords(peak), b.RoundOOMKills, b.Name))
	}

	running := 1
	if b.Owner == "" {
		n, err := localRunning(tx, b.Name)
		if err != nil {
			return b, err
		}
		running = n + 1
	}

	// Queue at the head (the same as the served lost path): RoundStartedAt is
	// what puts this round first in line.
	b.QueuedAt = b.RoundStartedAt
	if b.QueuedAt.IsZero() {
		b.QueuedAt = now
	}
	b.RoundStartedAt = time.Time{}
	b.OOMRequeue = &store.OOMRequeue{At: now, Running: running, PeakBytes: peak}

	var note string
	if b.Owner == "" {
		note = fmt.Sprintf("re-queued (builder %s; %d local round(s) were running)", OOMWords(peak), running)
	} else {
		note = fmt.Sprintf("re-queued (builder %s)", OOMWords(peak))
	}
	if err := tx.AppendLog(b.Name, store.LogEntry{
		TS:        now,
		Round:     b.Round,
		Direction: store.DirToMasterMind,
		Kind:      store.KindQueue,
		Confirmed: true,
		Note:      note,
	}); err != nil {
		return b, err
	}
	slog.Info("headless builder re-queued after an oom kill",
		"binding", b.Name, "round", b.Round, "running", running)
	return b, nil
}

// OOMAdmissible reports whether a local oom-queued round may be re-admitted:
// the host ran out of memory with this many rounds running, so the round waits
// until fewer are running.
func OOMAdmissible(b store.Binding, running int, now time.Time) bool {
	if b.Owner != "" {
		return false
	}
	if b.QueuedAt.IsZero() {
		return false
	}
	if b.OOMRequeue == nil {
		return false
	}
	if b.State != store.StateActive {
		return false
	}
	if now.Sub(b.OOMRequeue.At) < oomCooldown {
		return false
	}
	if running >= b.OOMRequeue.Running {
		return false
	}
	return true
}

// AdmitOOMQueued runs with no lock held and admits at most one oom-queued round
// per tick: a just-started builder's memory grows only after admission.
func AdmitOOMQueued(ctx context.Context, deps Deps, bindings []store.Binding) {
	now := deps.Now().UTC()

	running := 0
	for _, b := range bindings {
		if runsLocally(b) {
			running++
		}
	}

	var best *store.Binding
	for i := range bindings {
		b := &bindings[i]
		if !OOMAdmissible(*b, running, now) {
			continue
		}
		if best == nil || b.QueuedAt.Before(best.QueuedAt) {
			best = b
		}
	}
	if best == nil {
		return
	}

	if err := deps.Admit(ctx, best.Name); err != nil {
		if errors.Is(err, deps.NotQueued) {
			slog.Debug("oom admit: round already started by another writer", "binding", best.Name)
			return
		}
		slog.Error("oom admit failed", "binding", best.Name, "err", err)
	}
}

// OOMNote is the prompt addition for a round that was interrupted by an oom
// kill. It tells the builder what happened and to run git status and git diff
// first.
func OOMNote(t time.Time) string {
	return fmt.Sprintf(oomNoteFormat, t.UTC().Format("2006-01-02T15:04:05Z"))
}

// The cause is the round's own cgroup scope, ended by the kernel's OOM killer:
// a scope hit its memory limit. It is never a host-wide kill, and naming
// systemd-oomd sent readers looking at the wrong layer.
const oomNoteFormat = `This round was interrupted at %s because the builder's cgroup scope ran out of memory and the kernel's out-of-memory killer ended the builder process. The working tree may already hold partial edits from an earlier attempt at this same plan, and those edits are your own work, not someone else's. Run "git status" and "git diff" first, keep whatever is correct, and finish the plan.`
