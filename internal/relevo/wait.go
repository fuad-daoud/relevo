// Package relevo wait: relevo wait's polling loop and exit classification, per
// docs/specs/2026-09-14-wait-and-waiting-on-you-design.md §3.3-4.7.
package relevo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
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
	// Delivered is Payload's per-entry form: one record per entry the delivery
	// confirmed, waited round last, each carrying the text that entry alone
	// carries. Payload is these concatenated, so a caller that prints the
	// result whole reads Delivered and a caller with an output budget of its
	// own (#907) can decide per entry which ones fit. Empty whenever Payload
	// is.
	Delivered []delivery.Delivered
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

// unmarkedNote reports whether a report entry's note says the round closed
// without its completion marker, which is what WaitUnmarked (2) means.
//
// A note is only ever that claim when it carries one of the no-marker tokens
// themselves: "unmarked" (the builder exited after its report but never wrote
// the marker), "scraped" (the body is a terminal capture, not the builder's
// own file), or "noreport" (the marker is there but no report). Notes are
// space-joined, so each token is matched as a whole word and a join like
// "noreport stopped" still reads as unmarked.
//
// Every other note an annotated marked close carries -- "gate=<result>",
// "stopped", "scope=ok", "escaped", "uncommitted work at refs/relevo/...",
// "consumed by chain ..." -- describes a close that did happen, so a
// non-empty note alone is never enough to call a round unmarked.
func unmarkedNote(note string) bool {
	for _, tok := range strings.Fields(note) {
		switch tok {
		case "unmarked", noteScraped, "noreport":
			return true
		}
	}
	return false
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
		} else if unmarkedNote(e.Note) {
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

// waitReader covers the store reads wait needs: loading a binding and reading its log.
type waitReader interface {
	Load(name string) (store.Binding, error)
	ReadLog(name string) ([]store.LogEntry, error)
}

var _ waitReader = (*store.Store)(nil)

// WaitOptions configures Wait.
type WaitOptions struct {
	Names    []string      // one name, or several for --any; len >= 1
	Round    int           // 0 = DefaultWaitRound per binding, resolved once at start
	Timeout  time.Duration // > 0; the CLI defaults 10m
	Interval time.Duration // poll period; the CLI passes 1s; tests pass something small
	Peek     bool          // --peek: report the outcome only, deliver nothing

	reader waitReader
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
	stderr io.Writer
}

// waitDeliverable reports whether code is an exit on which `relevo wait`
// prints the pending payload (§4.1): every exit except the timeout (124) and a
// done or unbound binding (4).
func waitDeliverable(code int) bool {
	return code != WaitTimeout && code != WaitGone
}

func initialWaitRounds(rdr waitReader, opts WaitOptions) (map[string]int, error) {
	rounds := make(map[string]int, len(opts.Names))
	for _, n := range opts.Names {
		b, err := rdr.Load(n)
		if err != nil {
			return nil, err
		}
		entries, err := rdr.ReadLog(n)
		if err != nil {
			return nil, err
		}
		round := opts.Round
		if round == 0 {
			round = DefaultWaitRound(b, entries)
		}
		rounds[n] = round
	}
	return rounds, nil
}

// waitRegistration is one `relevo wait` process's hold on the bindings it is
// polling, in the store the status path reads. Its zero value is a
// registration that holds nothing, so a Runtime with no wait store needs no
// branch at the call site.
type waitRegistration struct {
	store     delivery.WaitClaimStore
	names     []string
	pid       int
	startedAt time.Time
}

// registerWait writes a registration for names, and returns the handle that
// removes it again.
//
// Every write is best-effort and its failure is swallowed: this records who is
// polling, and the wait's actual job is to collect the payload. A store that
// refuses the row must not stop the wait -- the status row falls back to its
// grace, which is the direction that still escalates a payload nobody collects.
func registerWait(rt Runtime, names []string, now time.Time) waitRegistration {
	reg := waitRegistration{store: rt.Waits, names: names, pid: os.Getpid(), startedAt: now}
	reg.refresh(now)
	return reg
}

// refresh re-stamps every held binding, so a poll that is running stays live.
// StartedAt is the first refresh's clock and never moves: it is when this
// process began polling, which is what it says, not when the row was last
// touched.
func (r waitRegistration) refresh(now time.Time) {
	if r.store == nil {
		return
	}
	for _, n := range r.names {
		_ = r.store.Write(delivery.WaitClaim{
			Name: n, PID: r.pid, StartedAt: r.startedAt, SeenAt: now,
		}, now)
	}
}

// release drops the registration. It is deferred for the whole wait, so every
// exit path -- a delivered round, a timeout, an error -- leaves no row behind
// that would read as a live wait.
func (r waitRegistration) release() {
	if r.store == nil {
		return
	}
	for _, n := range r.names {
		_ = r.store.Remove(n, r.pid)
	}
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

	retryer := newWaitRetryer(rt, opts)
	rounds, err := initialWaitRounds(retryer.reader, opts)
	if err != nil {
		return "", WaitResult{}, err
	}

	qf := questionFirstLine(rt)
	start := retryer.now()

	// The registration is held for the whole poll loop and dropped on the way
	// out, including on every error return, so a crashed or timed-out wait
	// cannot leave a row that reads as live. It is written from the clock read
	// the loop already makes, so registering costs no extra read: a wait that
	// resolves on its first pass still holds a registration, because the write
	// happens before the loop and the release after it.
	registration := registerWait(rt, opts.Names, start)
	defer registration.release()

	for {
		// A remote binding's closed round is collected here too (spec §2.2):
		// with no daemon running, this is the only place `relevo wait` would
		// otherwise see the round's state go stale. Advisory: a sync error
		// does not stop the poll, since the loop below re-reads the store
		// either way.
		_, _, _ = SyncRemoteUnlessDaemon(ctx, rt)

		for _, n := range opts.Names {
			b, err := retryer.load(ctx, n)
			if errors.Is(err, store.ErrNotFound) {
				return n, WaitResult{Code: WaitGone, Done: true}, nil
			}
			if err != nil {
				return n, WaitResult{}, err
			}
			entries, err := retryer.readLog(ctx, n)
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
					delivered, derr := delivery.PullPendingThroughEntries(ctx, rt.Store, n, "wait", rounds[n])
					if derr != nil {
						r.DeliverErr = derr
					} else if len(delivered) > 0 {
						r.Delivered = delivered
						r.Payload = delivery.JoinDelivered(delivered)
					}
				}
				r.Round = rounds[n]
				return n, r, nil
			}
		}

		// One clock read per pass serves both the timeout check and the
		// registration: the same instant answers "has this wait run long
		// enough to give up" and "is this wait still polling". A wait that is
		// running therefore keeps its row fresh, and one that died between
		// passes ages out on its own without anything having to clean it up.
		now := retryer.now()
		registration.refresh(now)

		if now.Sub(start) >= opts.Timeout {
			return "", WaitResult{Code: WaitTimeout, Done: true}, nil
		}

		if err := retryer.sleep(ctx, opts.Interval); err != nil {
			return "", WaitResult{}, err
		}
	}
}

// WaitOwned lists Store.List() filtered to masterMindID, excluding StateDone,
// and calls Wait with those names.
func WaitOwned(ctx context.Context, rt Runtime, masterMindID string, round int, timeout, interval time.Duration) (string, WaitResult, error) {
	if rt.Store == nil {
		return "", WaitResult{}, errors.New("wait: store is required")
	}
	all, err := rt.Store.List()
	if err != nil {
		return "", WaitResult{}, err
	}
	var names []string
	for _, b := range all {
		if b.MasterMindID == masterMindID && b.State != store.StateDone {
			names = append(names, b.Name)
		}
	}
	if len(names) == 0 {
		return "", WaitResult{}, fmt.Errorf("no active bindings for mastermind %s", masterMindID)
	}
	return Wait(ctx, rt, WaitOptions{
		Names:    names,
		Round:    round,
		Timeout:  timeout,
		Interval: interval,
	})
}
