package consult

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// SpawnTimeout is how long a consult reservation may stay in the spawning state
// before the daemon expires it. It must exceed the worst-case spawn phase (a
// Runner.Start at the client's 30 s timeout) so a slow live spawn is never
// expired from under its owner, and it is how long a crashed ask holds a slot.
const SpawnTimeout = 5 * time.Minute

// Reconcile advances every running consult on one binding by one tick. A
// consult is a process, observed through the Runner: its completion is the
// stream's final message, never a pane's screen.
//
// Preconditions:  the caller holds the state lock and passes its tx.
// Postconditions: each running consult is unchanged, or moved to done or silent
//
//	with exactly one findings entry queued for the transition.
func Reconcile(ctx context.Context, d Deps, tx *store.Tx, b store.Binding) (store.Binding, error) {
	now := d.Now().UTC()

	// Own the slice: the caller compares its copy of the binding against the
	// returned one to decide whether to save, and an in-place write to a
	// shared backing array would change both sides at once.
	b.Consults = append([]store.Consult(nil), b.Consults...)

	for i := range b.Consults {
		// Terminal records are never revisited. Without this guard every tick
		// re-queues findings that were already delivered, which is one
		// notification per poll, forever.
		if b.Consults[i].State == store.ConsultDone || b.Consults[i].State == store.ConsultSilent {
			continue
		}

		if b.Consults[i].State == store.ConsultSpawning {
			if now.Sub(b.Consults[i].SpawnedAt) >= SpawnTimeout {
				var err error
				if b, err = finishConsult(ctx, d, tx, b, i, store.ConsultSilent,
					"spawn did not complete within "+SpawnTimeout.String()); err != nil {
					return b, err
				}
			}
			continue
		}

		if b.Consults[i].Endpoint.Headless() {
			var err error
			if b, err = reconcileHeadless(ctx, d, tx, b, i, now); err != nil {
				return b, err
			}
		}
	}

	return b, nil
}

// reconcileHeadless observes one headless consult: a sighting is recorded so a
// later tick never judges the process lost to this daemon's own restart, and an
// alive process is only watched while a stopped one is read.
func reconcileHeadless(ctx context.Context, d Deps, tx *store.Tx, b store.Binding, i int, now time.Time) (store.Binding, error) {
	c := b.Consults[i]

	if d.Runner == nil {
		// Cannot observe the process; leave the record alone.
		slog.Warn("headless consult but no Runner configured", "binding", b.Name, "consult", c.ID)
		return b, nil
	}

	alive, err := d.Runner.Alive(ctx, handleOf(c.Endpoint))
	if err != nil {
		// An OS hiccup is not evidence the process stopped: treat it as alive
		// this tick.
		slog.Warn("headless consult liveness check failed; treating as alive",
			"binding", b.Name, "consult", c.ID, "pid", c.Endpoint.PID, "err", err)
		alive = true
	}
	if err == nil && alive {
		d.Seen(c.Endpoint.PID, c.Endpoint.StartedAt)
	}

	if alive {
		return reconcileLive(ctx, d, tx, b, i, now)
	}
	return reconcileExited(ctx, d, tx, b, i)
}

// reconcileLive enforces the consult deadline on one process still alive.
func reconcileLive(ctx context.Context, d Deps, tx *store.Tx, b store.Binding, i int, now time.Time) (store.Binding, error) {
	c := b.Consults[i]
	if now.Sub(c.SpawnedAt) < Timeout {
		return b, nil
	}

	if err := d.Runner.Kill(ctx, handleOf(c.Endpoint), c.Endpoint.LogPath); err != nil {
		slog.Warn("headless consult not killed",
			"binding", b.Name, "consult", c.ID, "pid", c.Endpoint.PID, "err", err)
	}
	return finishConsult(ctx, d, tx, b, i, store.ConsultSilent,
		"timed out after "+Timeout.String()+"; process killed")
}

// reconcileExited reads one stopped consult. The exit trailer is read before
// the stream's final message: a process that wrote no trailer was killed before
// it finished, so its text is partial and is never delivered as findings.
func reconcileExited(ctx context.Context, d Deps, tx *store.Tx, b store.Binding, i int) (store.Binding, error) {
	c := b.Consults[i]

	code, ok := d.Runner.ExitCode(ctx, handleOf(c.Endpoint), c.Endpoint.LogPath)
	if !ok {
		note := "ended without an exit trailer (killed before it finished); partial output: " + c.Endpoint.LogPath
		if d.LostToRestart(c.Endpoint.PID, c.Endpoint.StartedAt) {
			note = "lost to a daemon restart before it finished (no exit trailer); partial output: " + c.Endpoint.LogPath
		}
		return finishConsult(ctx, d, tx, b, i, store.ConsultSilent, note)
	}

	stream, _ := d.Store.ReadFile(c.Endpoint.LogPath)
	text := transcript.FinalText(c.Endpoint.Kind, stream)
	if text == "" {
		return finishConsult(ctx, d, tx, b, i, store.ConsultSilent,
			fmt.Sprintf("process exited (code %s) with no final message; see %s", strconv.Itoa(code), c.Endpoint.LogPath))
	}

	if err := tx.PutRoundFile(b.Name, c.Round, c.FindingsPath, []byte(text+"\n")); err != nil {
		return finishConsult(ctx, d, tx, b, i, store.ConsultSilent, "could not record findings: "+brief(err))
	}
	return finishConsult(ctx, d, tx, b, i, store.ConsultDone, "")
}

// finishConsult queues one planner-bound entry and marks the record terminal.
// Queue rather than a direct append is what makes consults inherit the
// anti-clobber rule, held notifications and `relevo wait` without new delivery
// code.
func finishConsult(ctx context.Context, d Deps, tx *store.Tx, b store.Binding, i int, state store.ConsultState, note string) (store.Binding, error) {
	c := b.Consults[i]

	entry := store.LogEntry{
		TS:        d.Now().UTC(),
		Round:     c.Round,
		Direction: store.DirToPlanner,
		Kind:      store.KindFindings,
		Note:      note,
		Usage:     d.Usage(ctx, b, c, d.Now().UTC()),
	}

	if state == store.ConsultDone {
		entry.Path = c.FindingsPath
		entry.Payload = fmt.Sprintf("Findings from %s consult %s: %s", c.Role, c.ID, delivery.FindingsCommand(b.Name, c.Round, c.ID))
	} else {
		// No Path: a silent consult wrote no file, and pointing at one that
		// does not exist would send the planner to read nothing. The note is
		// arbitrary text, so trim one trailing period: the payload supplies
		// its own, and a doubled period reads as a typo.
		entry.Payload = fmt.Sprintf("Consult %s (%s) wrote no findings: %s.",
			c.ID, c.Role, strings.TrimSuffix(note, "."))
	}

	if c.Role == VerifyRole {
		applyVerdict(d, &entry, &b, c, state)
	}

	if err := delivery.Queue(ctx, d.Delivery, tx, b.Name, entry); err != nil {
		return b, err
	}

	b.Consults[i].State = state
	b.Consults[i].Note = note

	if c.Role == VerifyRole {
		removeVerifyWorktree(ctx, d, b, c.Round)
	}

	return b, nil
}

// applyVerdict records the round-close reviewer's verdict on the findings entry
// and on the binding. A reviewer that produced no findings is delivered as
// unstructured, with the reason kept in the payload.
func applyVerdict(d Deps, entry *store.LogEntry, b *store.Binding, c store.Consult, state store.ConsultState) {
	if state != store.ConsultDone {
		b.LastVerdict = &store.Verdict{Round: c.Round, Verdict: verdictUnstructured}
		return
	}

	body, err := d.Store.ReadFile(c.FindingsPath)
	if err != nil {
		body = nil
	}
	verdict, reasons := parseVerdict(body)
	entry.Verdict = verdict
	entry.Reasons = reasons
	entry.Payload = fmt.Sprintf("relevo: round %d · verdict %s · %d reasons · %s",
		c.Round, verdict, len(reasons), delivery.FindingsCommand(b.Name, c.Round, c.ID))
	b.LastVerdict = &store.Verdict{
		Round:    c.Round,
		Verdict:  verdict,
		Reasons:  reasons,
		Findings: c.FindingsPath,
	}
}

// handleOf is the endpoint's stored process fields as the Runner's handle.
// StartedAt is Unix seconds on the endpoint.
func handleOf(e store.Endpoint) spawn.ProcHandle {
	return spawn.ProcHandle{PID: e.PID, StartedAt: time.Unix(e.StartedAt, 0)}
}
