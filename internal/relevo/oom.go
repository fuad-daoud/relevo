package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
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

// oomKilled reports an explicit oom-kill result; an unknown result counts as
// not oom-killed, so detection can only ever take today's path.
func oomKilled(ctx context.Context, rt Runtime, b store.Binding) bool {
	if rt.Scope == nil {
		return false
	}
	prober, ok := rt.Runner.(spawn.ScopeResultProber)
	if !ok {
		return false
	}
	result, err := prober.ScopeResult(ctx, scopeUnitName(b))
	if err != nil {
		slog.Debug("scope result probe failed; treating as not oom-killed",
			"binding", b.Name, "round", b.Round, "err", err)
		return false
	}
	return result == scopeResultOOM
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

func requeueOOM(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, now time.Time) (store.Binding, error) {
	b.RoundOOMKills++
	b = abandonSession(b)
	b.Builder.PID, b.Builder.StartedAt = 0, 0
	b.Builder = clearProcess(b.Builder)
	b.StalledSince = time.Time{}

	if b.RoundOOMKills >= oomMaxKills {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: builder killed by systemd-oomd (host out of memory) %d times this round; free memory, then relevo send --name %s --file <plan> again",
			b.Name, b.RoundOOMKills, b.Name))
	}

	running := 1
	if b.Owner == "" {
		n, err := localRunning(tx, b.Name)
		if err != nil {
			return b, err
		}
		running = n + 1
	}

	// Queue at the head (same as the served lost path): use RoundStartedAt so
	// this round is first in line.
	b.QueuedAt = b.RoundStartedAt
	if b.QueuedAt.IsZero() {
		b.QueuedAt = now
	}
	b.RoundStartedAt = time.Time{}
	b.OOMRequeue = &store.OOMRequeue{At: now, Running: running}

	var note string
	if b.Owner == "" {
		note = fmt.Sprintf("re-queued (builder killed by systemd-oomd: host out of memory; %d local round(s) were running)", running)
	} else {
		note = "re-queued (builder killed by systemd-oomd: host out of memory)"
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

// oomAdmissible reports whether a local oom-queued round may be re-admitted:
// the host ran out of memory with this many rounds running, so the round waits
// until fewer are running.
func oomAdmissible(b store.Binding, running int, now time.Time) bool {
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

// admitOOMQueued runs with no lock held and admits at most one oom-queued round
// per tick: a just-started builder's memory grows only after admission.
func admitOOMQueued(ctx context.Context, rt Runtime, bindings []store.Binding) {
	now := rt.Now().UTC()

	running := 0
	for _, b := range bindings {
		if runsLocally(b) {
			running++
		}
	}

	var best *store.Binding
	for i := range bindings {
		b := &bindings[i]
		if !oomAdmissible(*b, running, now) {
			continue
		}
		if best == nil || b.QueuedAt.Before(best.QueuedAt) {
			best = b
		}
	}
	if best == nil {
		return
	}

	if err := Admit(ctx, rt, best.Name); err != nil {
		if errors.Is(err, ErrNotQueued) {
			slog.Debug("oom admit: round already started by another writer", "binding", best.Name)
			return
		}
		slog.Error("oom admit failed", "binding", best.Name, "err", err)
	}
}

// oomNote is the prompt addition for a round that was interrupted by an oom
// kill. It tells the builder what happened and to run git status and git diff
// first.
func oomNote(t time.Time) string {
	return fmt.Sprintf(oomNoteFormat, t.UTC().Format("2006-01-02T15:04:05Z"))
}

const oomNoteFormat = `This round was interrupted at %s because the host ran out of memory and systemd-oomd killed the builder process. The working tree may already hold partial edits from an earlier attempt at this same plan, and those edits are your own work, not someone else's. Run "git status" and "git diff" first, keep whatever is correct, and finish the plan.`
