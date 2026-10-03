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

// Pull claims the oldest claimable mastermind payload for name with route,
// unconditionally, and returns the text it carried. It is the in-process
// reader's own claim: a caller that has printed one specific payload uses
// PullMatching instead, so a read that printed something else cannot consume it.
func Pull(ctx context.Context, st *store.Store, name, route string) (text string, found bool, err error) {
	return pullMatching(ctx, st, name, route, nil)
}

// PullMatching is Pull's conditional form: it claims the oldest claimable
// mastermind payload for name with route only when match accepts that entry,
// and answers found false without confirming anything when it does not. The
// claimable scan is unchanged, so an entry a push route already admitted is
// still not claimable here and an older undelivered entry still comes first --
// a read may confirm a payload, never reorder or bypass the queue.
//
// match is the caller's answer to "did this read print that entry?", so a
// reader that printed one section claims only the payload that section is.
// A nil match accepts every entry, which is what Pull passes.
func PullMatching(ctx context.Context, st *store.Store, name, route string, match func(store.LogEntry) bool) (text string, found bool, err error) {
	return pullMatching(ctx, st, name, route, match)
}

// busyRetryDelays is the backoff retryBusy sleeps between attempts when a
// store transaction could not proceed because another process held the
// database (db.ErrBusy): a short first wait, then two longer ones.
var busyRetryDelays = []time.Duration{250 * time.Millisecond, time.Second, 2 * time.Second}

// retryBusy calls fn; while it returns an error that wraps db.ErrBusy and
// delays remain, it sleeps the next delay and calls fn again. A non-busy error
// returns at once, and so does the last error once the delays run out. When
// ctx is done before a sleep it returns ctx.Err(). sleep is injectable for
// tests; nil means time.Sleep.
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

// pullMatching returns the oldest claimable entry's text for name and marks it
// delivered with route, WITHOUT pushing anything -- but only when match accepts
// that entry. A nil match claims it unconditionally, which is what the removed
// pull verb did and what the helper `relevo wait` calls once its round has
// ended: the CLI prints the result to stdout and the mastermind reads it as
// tool output.
//
// The text is PushText(entry, <name's binding>, st.ReadFile): the stored payload (origin
// line first) plus a blank line plus the report file's text, capped at
// MaxPushBytes. found is false when nothing is pending and when match rejects
// the entry, so a rejecting caller confirms nothing and reads no text.
//
// The lock and confirm step is wrapped in retryBusy: other relevo processes
// and the daemon hold the database concurrently, so a busy begin is retried
// with a short backoff rather than failing the delivery outright. The match
// decision runs inside the lock, on the entry the scan just returned, so what
// is decided and what is confirmed cannot come apart.
func pullMatching(ctx context.Context, st *store.Store, name, route string, match func(store.LogEntry) bool) (text string, found bool, err error) {
	var entry store.LogEntry

	// Same critical section as DeliverPending: the daemon may be delivering
	// this very payload right now, and only one of us may claim it.
	err = retryBusy(ctx, busyRetryDelays, nil, func() error {
		return st.WithLock(func(tx *store.Tx) error {
			pending, idx, ok, err := tx.ClaimableForMasterMind(name)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			if match != nil && !match(pending) {
				return nil
			}
			if err := tx.ConfirmIndex(name, idx, route); err != nil {
				return err
			}

			entry, found = pending, true

			return nil
		})
	})
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}

	// The file read happens outside the lock: no file I/O under the state
	// lock.
	text, _ = PushText(entry, BindingFor(st, name), st.ReadFile)
	return text, true, nil
}

// Delivered is one pending entry a through-pull confirmed, with the exact text
// that entry carries: Text is PushText(e, <name's binding>, st.ReadFile), the
// same string PullPendingThrough concatenates. Splitting the confirmation from
// the concatenation is what lets a caller that has one output budget for the
// whole result decide per entry -- deliver it, cut it, or point at it -- without
// re-reading a queue whose entries are already confirmed.
type Delivered struct {
	Entry store.LogEntry
	Text  string
}

// PullPendingThroughEntries returns one Delivered per pending mastermind
// payload of name whose round is at most round -- every round when round <= 0
// -- confirming each with route and WITHOUT pushing anything, in log order so
// the waited round is last. An empty slice with a nil error means nothing was
// pending and nothing was confirmed.
//
// The list and confirm step runs in ONE lock, wrapped in retryBusy: a busy
// database is retried with a short backoff, and nothing is confirmed unless
// the whole lock body succeeded. The file reads happen outside the lock.
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

	// The file reads happen outside the lock: no file I/O under the state
	// lock, as pullPending does.
	binding := BindingFor(st, name)
	out := make([]Delivered, 0, len(pending))
	for _, p := range pending {
		text, _ := PushText(p.Entry, binding, st.ReadFile)
		out = append(out, Delivered{Entry: p.Entry, Text: text})
	}
	return out, nil
}

// JoinDelivered renders one whole-result string from the per-entry deliveries,
// waited round last: the single entry's text when there is one, and every
// earlier entry under a header naming its round otherwise. It is the shape
// PullPendingThrough returns and the shape WaitResult.Payload carries, so a
// caller that reads the result whole and a caller that reads it per entry agree
// on what the entries were.
func JoinDelivered(delivered []Delivered) string {
	if len(delivered) == 1 {
		return delivered[0].Text
	}

	var b strings.Builder
	for _, d := range delivered[:len(delivered)-1] {
		fmt.Fprintf(&b, "── round %d: not delivered earlier (%s) ──\n", d.Entry.Round, d.Entry.Path)
		b.WriteString(d.Text)
		b.WriteString("\n\n")
	}
	b.WriteString(delivered[len(delivered)-1].Text)
	return b.String()
}

// PullPendingThrough returns the text for every pending mastermind payload of name
// whose round is at most round -- every round when round <= 0 -- and marks each
// delivered with route, WITHOUT pushing anything. The waited round's text is
// last; every earlier one is prefixed with a header naming its round, so an
// older undelivered payload is neither dropped nor mistaken for the newest
// text.
//
// It is PullPendingThroughEntries plus the concatenation a reader of one whole
// result wants: the split form is what a caller with its own per-entry output
// budget uses, and both confirm the same entries in the same order.
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
