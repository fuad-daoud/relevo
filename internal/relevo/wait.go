// Package relevo wait: relevo wait's polling loop and exit classification, per
// docs/specs/2026-09-14-wait-and-waiting-on-you-design.md §3.3-4.7.
package relevo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// WaitResult is what `relevo wait` reports once it stops polling.
type WaitResult struct {
	Code int    // one of the Wait* exit constants below; meaningful only when Done
	Line string // stdout line: report path, "-" (noreport), or the Waiting line
	Done bool   // false while the round is open and nothing needs a human

	// Payload is the pending entry's text Wait delivered for the round it
	// stopped on (§4.1): the same text `relevo wait` printed. "" when nothing
	// was pending, when --peek suppressed the delivery, or on an exit that
	// delivers nothing (124 and 4).
	Payload string
	// DeliverErr is a read or confirm failure while delivering Payload. It
	// never changes the exit code (§6): the CLI prints it to stderr.
	DeliverErr error

	// Round is the round Wait waited on for the binding it returns, so the
	// CLI can name it when a delivery fails. 0 when not applicable: a
	// timeout or a gone binding.
	Round int
}

const (
	// WaitClosed is the exit code for a round that closed with a marked report.
	WaitClosed = 0
	// WaitUnmarked is the exit code for a round that closed without a marked
	// report (unmarked, scraped, or noreport).
	WaitUnmarked = 2
	// WaitNeedsYou is the exit code for a binding view.WaitingOn classified as
	// needing a human.
	WaitNeedsYou = 3
	// WaitGone is the exit code for a binding that is DONE, or was unbound
	// while Wait was polling it.
	WaitGone = 4
	// WaitHalted is the exit code for a round that closed and its report's
	// Outcome is halted or blocked (#133).
	WaitHalted = 5
	// WaitNotStarted is the exit code for a round that has no plan entry:
	// nothing is in flight, so waiting for it can only time out (#253).
	WaitNotStarted = 6
	// WaitTimeout is the exit code for a wait whose --timeout elapsed.
	WaitTimeout = 124
)

// lastReportEntry returns the last to_planner/report log entry for round, if one
// exists.
func lastReportEntry(entries []store.LogEntry, round int) (store.LogEntry, bool) {
	var last store.LogEntry
	found := false
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToMasterMind && e.Kind == store.KindReport {
			last, found = e, true
		}
	}
	return last, found
}

// DefaultWaitRound is the round `relevo wait` waits on when --round is not
// given: the highest round among to_builder/prompt entries (a nudge is not a
// send, so entries noted nudgeNote are excluded, as HasPromptEntry excludes
// them); b.Round when there is none (spec §4.5, decision 7).
func DefaultWaitRound(b store.Binding, entries []store.LogEntry) int {
	round := 0
	for _, e := range entries {
		if e.Direction == store.DirToBuilder && store.IsPromptKind(e.Kind) && e.Note != nudgeNote && e.Round > round {
			round = e.Round
		}
	}
	if round == 0 {
		return b.Round
	}
	return round
}

// WaitOutcome classifies one binding's round into a WaitResult, per spec
// §4.6. Pure apart from questionOf. The report entry for round is checked
// before State == done and before view.WaitingOn, so an earlier round's close is
// reported regardless of what the binding is doing now.
func WaitOutcome(b store.Binding, entries []store.LogEntry, round int, questionOf func(name string, round int) string) WaitResult {
	if e, ok := lastReportEntry(entries, round); ok {
		code := WaitClosed
		// a marked round that halted is 5, not 0; an unmarked round that halted is
		// also 5 -- the mastermind has to read why either way.
		if e.Outcome == reporttail.OutcomeHalted || e.Outcome == reporttail.OutcomeBlocked {
			code = WaitHalted
		} else if e.Note != "" {
			code = WaitUnmarked
		}
		line := e.Path
		if e.Note == "noreport" {
			line = "-"
		}
		return WaitResult{Code: code, Line: line, Done: true}
	}

	if b.State == store.StateDone {
		return WaitResult{Code: WaitGone, Done: true}
	}

	if w, ok := view.WaitingOn(b, entries, questionOf); ok {
		return WaitResult{Code: WaitNeedsYou, Line: w.Line, Done: true}
	}

	// Nothing in flight: the round was never sent, so no later poll can see
	// it close. A nudge is not a send, so HasPromptEntry excludes it, as
	// DefaultWaitRound does.
	if !HasPromptEntry(entries, round) {
		return WaitResult{
			Code: WaitNotStarted,
			Line: fmt.Sprintf("round %d was never sent to %s's runner", round, b.Name),
			Done: true,
		}
	}

	return WaitResult{}
}

// WaitOptions configures Wait.
type WaitOptions struct {
	Names    []string      // one name, or several for --any; len >= 1
	Round    int           // 0 = DefaultWaitRound per binding, resolved once at start
	Timeout  time.Duration // > 0; the CLI defaults 10m
	Interval time.Duration // poll period; the CLI passes 1s; tests pass something small
	Peek     bool          // --peek: report the outcome only, deliver nothing
}

// waitDeliverable reports whether code is an exit on which `relevo wait`
// prints the pending payload (§4.1): every exit except the timeout (124) and a
// done or unbound binding (4).
func waitDeliverable(code int) bool {
	return code != WaitTimeout && code != WaitGone
}

// Wait polls the store until one of opts.Names closes its
// round, needs a human, or is gone, or opts.Timeout elapses, per spec §4.7.
// On an exit that delivers (every code but WaitTimeout and WaitGone) it then
// prints the binding's oldest pending payload for the mastermind (§4.1): the
// returned WaitResult carries the text in Payload and marks the entry
// delivered with route "wait", unless opts.Peek suppresses that. A delivery
// failure is returned in DeliverErr and never changes Code (§6).
//
// Every name is loaded once up front, so a name that does not exist is an
// error before the loop starts (exit 1 from cmd/relevo); a name that
// disappears mid-wait is WaitGone instead. The first pass always runs before
// any sleep, so a round that already closed returns immediately.
func Wait(ctx context.Context, rt Runtime, opts WaitOptions) (name string, res WaitResult, err error) {
	if len(opts.Names) == 0 {
		return "", WaitResult{}, fmt.Errorf("wait: at least one name is required")
	}
	if opts.Timeout <= 0 {
		return "", WaitResult{}, fmt.Errorf("wait: timeout must be positive")
	}
	if opts.Interval <= 0 {
		return "", WaitResult{}, fmt.Errorf("wait: interval must be positive")
	}

	rounds := make(map[string]int, len(opts.Names))
	for _, n := range opts.Names {
		b, err := rt.Store.Load(n)
		if err != nil {
			return "", WaitResult{}, err
		}
		entries, err := rt.Store.ReadLog(n)
		if err != nil {
			return "", WaitResult{}, err
		}
		round := opts.Round
		if round == 0 {
			round = DefaultWaitRound(b, entries)
		}
		rounds[n] = round
	}

	qf := questionFirstLine(rt)
	start := rt.Now()

	for {
		// A remote binding's closed round is collected here too (spec §2.2):
		// with no daemon running, this is the only place `relevo wait` would
		// otherwise see the round's state go stale. Advisory: a sync error
		// does not stop the poll, since the loop below re-reads the store
		// either way.
		_, _, _ = SyncRemoteUnlessDaemon(ctx, rt)

		for _, n := range opts.Names {
			b, err := rt.Store.Load(n)
			if errors.Is(err, store.ErrNotFound) {
				return n, WaitResult{Code: WaitGone, Done: true}, nil
			}
			if err != nil {
				return n, WaitResult{}, err
			}
			entries, err := rt.Store.ReadLog(n)
			if err != nil {
				return n, WaitResult{}, err
			}
			if r := WaitOutcome(b, entries, rounds[n], qf); r.Done {
				// §4.1: every exit except the timeout and a gone binding
				// prints the pending payload, after the outcome line, and
				// marks it delivered with route "wait". --peek reports the
				// outcome only. A delivery failure never changes the exit
				// code (§6); the CLI prints DeliverErr to stderr.
				if waitDeliverable(r.Code) && !opts.Peek {
					text, found, derr := delivery.PullPendingThrough(ctx, rt.Store, n, "wait", rounds[n])
					if derr != nil {
						r.DeliverErr = derr
					} else if found {
						r.Payload = text
					}
				}
				r.Round = rounds[n]
				return n, r, nil
			}
		}

		if rt.Now().Sub(start) >= opts.Timeout {
			return "", WaitResult{Code: WaitTimeout, Done: true}, nil
		}

		select {
		case <-ctx.Done():
			return "", WaitResult{}, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}
