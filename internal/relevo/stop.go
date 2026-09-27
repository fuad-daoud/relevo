package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNothingToStop reports a stop on a binding with no round in flight. The
// CLI turns it into a message and exit 0: nothing to stop is an answer, not
// a failure.
var ErrNothingToStop = errors.New("no round is open; nothing to stop")

// DefaultStopGrace is the historical pane wrap-up grace (#138). Pane builders
// were removed in #303; the constant survives only because a stored binding
// may still carry StopGraceMS.
const DefaultStopGrace = 5 * time.Minute

// StopOptions is what a stop request may add. Empty since #303: the pane
// wrap-up (--grace/--now) went with pane builders, and a headless round is
// killed at once.
type StopOptions struct{}

// StopResult is what Stop did.
type StopResult struct {
	Round  int
	Action string        // "killed" | "reaped" | "gone" | "dequeued" | "nothing"
	Grace  time.Duration // always zero since #303; the pane wrap-up is gone
}

// stopAction is what a binding's stop bookkeeping implies should happen next.
type stopAction int

const (
	stopKill    stopAction = iota // a round is open: kill the headless process
	stopDequeue                   // a queued round has no process: drop it from the queue
	stopNothing                   // no round is open
)

// stopDecision says whether a binding has an open round to stop. Pure. A
// local builder is always headless since #303, so an open round is killed at
// once: the pane wrap-up, its grace and its abandonment are gone. A queued
// round is checked first, since it has a zero RoundStartedAt: it is dropped
// from the queue rather than killed (#285, #344).
func stopDecision(b store.Binding, now time.Time) stopAction {
	if !b.QueuedAt.IsZero() {
		return stopDequeue
	}
	if b.RoundStartedAt.IsZero() {
		return stopNothing
	}
	return stopKill
}

// Stop ends an open round on purpose (#138), so the work in it is not lost: a
// headless builder has no stdin to type into, so it is killed now and its
// round is closed without a report (`noreport stopped`). A stop is not a
// failure, so this path never switches builders.
//
// It runs entirely under the state lock (the Done/Pause pattern), with
// refusals first so a refusal changes nothing.
func Stop(ctx context.Context, rt Runtime, name string, opts StopOptions) (StopResult, error) {
	var out StopResult
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		// Refusals first: nothing has changed yet.
		if b.State == store.StateDone {
			return fmt.Errorf("binding %q is done; nothing to stop", name)
		}
		if b.State == store.StatePaused {
			return fmt.Errorf("binding %q is paused; nothing to stop", name)
		}
		if b.Builder.Remote() {
			server := b.Builder.Server
			if rt.Remote == nil {
				return ErrRemoteUnavailable
			}
			who, err := rt.Remote.WhoAmI(ctx, server)
			if err != nil {
				return fmt.Errorf("%s: %w", server, err)
			}
			if !slices.Contains(who.Features, remote.FeatureStop) {
				return fmt.Errorf("server %s predates remote stop (no %q feature); relevo unbind %s to stop the round and drop the binding",
					server, remote.FeatureStop, name)
			}
			view, err := rt.Remote.Stop(ctx, server, name)
			if err != nil {
				var httpErr *client.HTTPError
				if errors.As(err, &httpErr) {
					switch {
					case httpErr.Status == 409 && httpErr.Body.Code == remote.CodeNothingToStop:
						return ErrNothingToStop
					case httpErr.Status == 409 && httpErr.Body.Code == remote.CodeRoundHalted:
						return fmt.Errorf("%s: round %d is halted there: %s", server, b.Round, httpErr.Body.Message)
					case httpErr.Status == 404:
						return fmt.Errorf("%s no longer has binding %q; relevo unbind %s to drop it here", server, name, name)
					}
				}
				if errors.Is(err, client.ErrUnreachable) {
					return fmt.Errorf("%s unreachable: %w", server, err)
				}
				return fmt.Errorf("%s: %w", server, err)
			}

			out.Round = b.Round
			out.Action = view.Stopped
			if out.Action == "" {
				out.Action = "killed"
			}

			// Collect the close now instead of waiting for the next sync. The
			// server has already stopped the round, so a catch-up that cannot
			// finish is not an error: the next sync, pull or wait collects it.
			next, _, oerr := observeRemote(ctx, rt, tx, b)
			if oerr != nil {
				return oerr
			}
			if store.SameBinding(next, b) {
				return nil
			}
			return tx.Save(next)
		}

		out.Round = b.Round

		how := ""
		switch stopDecision(b, rt.Now().UTC()) {
		case stopNothing:
			return ErrNothingToStop
		case stopKill:
			// A stop is not a failure: signal the round's live process, then
			// end the scope a straggler may still hold, and report what
			// actually happened. Ordered so a failed stop leaves the round
			// open and nothing recorded, exactly as `done` does.
			killed, reaped, err := stopOpenRound(ctx, rt, b)
			if err != nil {
				return err
			}
			b.Builder = clearProcess(b.Builder)
			b = abandonSession(b)
			switch {
			case killed:
				how = "killed"
			case reaped:
				how = "reaped"
			default:
				how = "gone"
			}
		case stopDequeue:
			// Nothing to kill: the server's queue is derived from QueuedAt, so
			// clearing it drops the round (#285).
			b.QueuedAt = time.Time{}
			how = "dequeued"
		}

		b, err = closeStopped(ctx, rt, tx, b, how)
		if err != nil {
			return err
		}
		if b.Owner != "" {
			// Same order as markerClose: queueReport first, then the served
			// close, so the closed round's facts are recorded (#344).
			b = closeServedRound(ctx, rt, b)
		}
		b.StalledSince = time.Time{}
		out.Action = how
		return tx.Save(b)
	})
	if err == nil && out.Action == "killed" {
		// The delete runs outside the lock, and its failure only logs: it
		// never changes what Stop did.
		reapAbandoned(ctx, rt, name)
	}
	return out, err
}

// stopOpenRound stops what an open headless round still has running: its
// recorded process when that is alive, then its scope when that is still
// loaded, which is where a straggler the runner abandoned holds on. It reports
// what it did -- killed for a live process signalled, reaped for a scope ended
// -- so the close can say what happened instead of claiming a kill that never
// was. An error means the round may still have something running and the
// caller leaves the round open.
func stopOpenRound(ctx context.Context, rt Runtime, b store.Binding) (killed, reaped bool, err error) {
	if b.Builder.PID != 0 {
		if rt.Runner == nil {
			return false, false, spawn.ErrRunnerUnavailable
		}
		alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
		if err != nil {
			return false, false, fmt.Errorf("binding %q: check previous process %d: %w", b.Name, b.Builder.PID, err)
		}
		if alive {
			if _, err := stopProcess(ctx, rt, b.Builder, "stop"); err != nil {
				return false, false, err
			}
			killed = true
		}
	}
	reaped, err = endScope(ctx, rt, scopeUnitName(b))
	if err != nil {
		return killed, false, err
	}
	return killed, reaped, nil
}

// stopPayload is the report payload and note a stopped close writes, local or
// remote. how names the close ("killed", "reaped", "gone" or "dequeued"),
// where is "" for a local stop and " on <server>" for a remote one, and
// haveReport says whether a report file was on disk. Pure.
func stopPayload(how, name string, round int, where string, haveReport bool) (payload, note string) {
	if haveReport {
		return fmt.Sprintf("The runner was stopped (%s) for round %d%s. Report: %s", how, round, where, showCommand(name, round, "report")), "stopped"
	}
	return fmt.Sprintf("The runner was stopped (%s) for round %d%s; no report was written.", how, round, where), "noreport stopped"
}

// closeStopped closes an open round whose builder was stopped (#138): the
// report is queued if one is on disk, and the round closes without the
// switch a builder's own exit-without-report would trigger -- a stop is not
// a failure. how names the close for the log ("killed", "reaped", "gone" or
// "dequeued").
//
// queueReport does the round advance and clears the stop bookkeeping, so the
// entry appended after it is filed under the round that was stopped.
func closeStopped(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, how string) (store.Binding, error) {
	stoppedRound := b.Round
	reportPath, serr := writeReaderSummary(rt, b)
	if serr != nil {
		slog.Warn("reader summary not written", "binding", b.Name, "round", stoppedRound, "err", serr)
	}

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}

	haveReport := false
	if _, err := os.Stat(reportPath); err == nil {
		haveReport = true
	}
	payload, note := stopPayload(how, b.Name, stoppedRound, "", haveReport)

	next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, note, nil, nil, nil, nil)
	if err != nil {
		return b, err
	}

	if err := tx.AppendLog(b.Name, store.LogEntry{
		TS: rt.Now().UTC(), Round: stoppedRound, Direction: store.DirToPlanner,
		Kind: store.KindStop, Note: "stopped/" + how, Confirmed: true,
	}); err != nil {
		return next, err
	}
	return next, nil
}
