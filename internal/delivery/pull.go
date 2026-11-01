package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Pull claims the oldest claimable payload for name with route. A caller that
// printed one specific payload uses PullMatching instead.
func Pull(ctx context.Context, st *store.Store, name, route string) (text string, found bool, err error) {
	return pullMatching(ctx, st, name, route, nil)
}

// PullMatching claims the oldest claimable payload match accepts, scanning past
// the claimable entries it does not; rejected entries stay pending and
// unconfirmed, so a read never reorders the queue. match answers "did this
// read print that entry?"; nil accepts every entry, which is what Pull passes.
func PullMatching(ctx context.Context, st *store.Store, name, route string, match func(store.LogEntry) bool) (text string, found bool, err error) {
	return pullMatching(ctx, st, name, route, match)
}

// busyRetryDelays is retryBusy's backoff on db.ErrBusy: a short wait, then two longer ones.
var busyRetryDelays = []time.Duration{250 * time.Millisecond, time.Second, 2 * time.Second}

// retryBusy retries fn on db.ErrBusy with the next delay; any other error
// returns at once. sleep is injectable for tests; nil means time.Sleep.
func retryBusy(ctx context.Context, delays []time.Duration, sleep func(time.Duration), fn func() error) error {
	if sleep == nil {
		sleep = time.Sleep
	}

	err := fn()
	for i := 0; errors.Is(err, db.ErrBusy) && i < len(delays); i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sleep(delays[i])
		err = fn()
	}
	return err
}

// pullMatching confirms the oldest claimable entry match accepts, without
// pushing anything; a rejecting caller confirms nothing. The text is PushText
// for the entry, and the scan, match and confirm run inside one lock on the
// log just read, retried on busy, so decision and confirm cannot come apart.
func pullMatching(ctx context.Context, st *store.Store, name, route string, match func(store.LogEntry) bool) (text string, found bool, err error) {
	var entry store.LogEntry

	// Same critical section as DeliverPending: only one claimant may win.
	err = retryBusy(ctx, busyRetryDelays, nil, func() error {
		return st.WithLock(func(tx *store.Tx) error {
			// Log order, passing rejected entries for the first accepted one.
			pending, err := tx.ClaimableForMasterMindThrough(name, 0)
			if err != nil {
				return err
			}
			for _, p := range pending {
				if match != nil && !match(p.Entry) {
					continue
				}
				if err := tx.ConfirmIndex(name, p.Idx, route); err != nil {
					return err
				}

				entry, found = p.Entry, true

				return nil
			}
			return nil
		})
	})
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}

	// File reads happen outside the lock: no file I/O under the state lock.
	text, _ = PushText(entry, BindingFor(st, name), st.ReadFile)
	return text, true, nil
}

// Delivered is one entry a through-pull confirmed with its PushText: splitting
// confirmation from concatenation lets a caller with one output budget decide
// per entry without re-reading an already-confirmed queue.
type Delivered struct {
	Entry store.LogEntry
	Text  string
}

// PullPendingThroughEntries confirms every pending payload of name at or below
// round (every round when round <= 0) without pushing, ordered with ReportLast.
// The list-and-confirm runs in one lock, retried on busy; file reads happen
// outside it.
func PullPendingThroughEntries(ctx context.Context, st *store.Store, name, route string, round int) ([]Delivered, error) {
	var pending []store.PendingEntry

	err := retryBusy(ctx, busyRetryDelays, nil, func() error {
		return st.WithLock(func(tx *store.Tx) error {
			entries, err := tx.ClaimableForMasterMindThrough(name, round)
			if err != nil {
				return err
			}
			for _, p := range entries {
				if err := tx.ConfirmIndex(name, p.Idx, route); err != nil {
					return err
				}
			}

			pending = entries

			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}

	// File reads happen outside the lock.
	binding := BindingFor(st, name)
	out := make([]Delivered, 0, len(pending))
	for _, p := range pending {
		text, _ := PushText(p.Entry, binding, st.ReadFile)
		out = append(out, Delivered{Entry: p.Entry, Text: text})
	}
	return ReportLast(out), nil
}

// EarlierHeader introduces an entry delivered before the waited round, so an
// older payload is neither dropped nor mistaken for the newest text. The path
// is dropped when there is none: a halt carries no artifact.
func EarlierHeader(round int, path string) string {
	if path == "" {
		return fmt.Sprintf("── round %d: not delivered earlier ──\n", round)
	}
	return fmt.Sprintf("── round %d: not delivered earlier (%s) ──\n", round, path)
}

// HaltHeader introduces a halt collected with the waited report: the round
// stopping, not a report gone undelivered.
func HaltHeader(round int) string {
	return fmt.Sprintf("── round %d: halted ──\n", round)
}

// EntryHeader is the label for an entry before the waited round's text: the
// halt label for that round's halt, the not-delivered-earlier label otherwise.
func EntryHeader(e store.LogEntry, waitedRound int) string {
	if e.Kind == store.KindHalt && e.Round == waitedRound {
		return HaltHeader(e.Round)
	}
	return EarlierHeader(e.Round, e.Path)
}

// ReportLast moves the newest round's report entries last: log order files a
// closing halt after its report, which would demote the report under a header.
// Entries of that round keep their order; a halt-only round comes back
// untouched.
func ReportLast(delivered []Delivered) []Delivered {
	if len(delivered) < 2 {
		return delivered
	}
	waited := 0
	for _, d := range delivered {
		if d.Entry.Round > waited {
			waited = d.Entry.Round
		}
	}

	reports := make([]Delivered, 0, len(delivered))
	out := make([]Delivered, 0, len(delivered))
	for _, d := range delivered {
		if d.Entry.Round == waited && d.Entry.Kind == store.KindReport {
			reports = append(reports, d)
			continue
		}
		out = append(out, d)
	}
	if len(reports) == 0 {
		return delivered
	}
	return append(out, reports...)
}

// JoinDelivered renders one whole-result string, waited round last, earlier
// entries each under their round's header. It renders the order it is given,
// so callers order with ReportLast first.
func JoinDelivered(delivered []Delivered) string {
	if len(delivered) == 1 {
		return delivered[0].Text
	}

	waited := delivered[len(delivered)-1].Entry.Round
	var b strings.Builder
	for _, d := range delivered[:len(delivered)-1] {
		b.WriteString(EntryHeader(d.Entry, waited))
		b.WriteString(d.Text)
		b.WriteString("\n\n")
	}
	b.WriteString(delivered[len(delivered)-1].Text)
	return b.String()
}

// PullPendingThrough is PullPendingThroughEntries plus the one-reader
// concatenation: the waited round's text last, earlier ones each under a header.
func PullPendingThrough(ctx context.Context, st *store.Store, name, route string, round int) (text string, found bool, err error) {
	delivered, err := PullPendingThroughEntries(ctx, st, name, route, round)
	if err != nil {
		return "", false, err
	}
	if len(delivered) == 0 {
		return "", false, nil
	}
	return JoinDelivered(delivered), true, nil
}
