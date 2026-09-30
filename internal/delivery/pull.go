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

// Pull is pullPending for in-process readers: the cockpit's round view calls it
// with route "tui" when it opens a binding whose report is ready for the human
// mastermind, so the text it returns is shown and the entry is marked delivered
// to the TUI rather than to `relevo wait`; `relevo show`'s live branch calls it
// with route "show" and discards the text, so a plain read confirms the payload
// instead of leaving it to be pushed afterwards.
func Pull(ctx context.Context, st *store.Store, name, route string) (text string, found bool, err error) {
	return pullPending(ctx, st, name, route)
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

// pullPending returns the oldest pending entry's text for name and marks it
// delivered with route, WITHOUT pushing anything. It is what the removed pull
// verb did, and the helper `relevo wait` calls once its round has ended: the
// CLI prints the result to stdout and the mastermind reads it as tool output.
//
// The text is PushText(entry, <name's binding>, st.ReadFile): the stored payload (origin
// line first) plus a blank line plus the report file's text, capped at
// MaxPushBytes. found is false when nothing is pending.
//
// The lock and confirm step is wrapped in retryBusy: other relevo processes
// and the daemon hold the database concurrently, so a busy begin is retried
// with a short backoff rather than failing the delivery outright.
func pullPending(ctx context.Context, st *store.Store, name, route string) (text string, found bool, err error) {
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

// PullPendingThrough returns the text for every pending mastermind payload of name
// whose round is at most round -- every round when round <= 0 -- and marks each
// delivered with route, WITHOUT pushing anything. The waited round's text is
// last; every earlier one is prefixed with a header naming its round, so an
// older undelivered payload is neither dropped nor mistaken for the newest
// text.
//
// The list and confirm step runs in ONE lock, wrapped in retryBusy: a busy
// database is retried with a short backoff, and nothing is confirmed unless
// the whole lock body succeeded. The file reads happen outside the lock, as in
// pullPending.
func PullPendingThrough(ctx context.Context, st *store.Store, name, route string, round int) (text string, found bool, err error) {
	var pending []store.PendingEntry

	err = retryBusy(ctx, busyRetryDelays, nil, func() error {
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
		return "", false, err
	}
	if len(pending) == 0 {
		return "", false, nil
	}

	// The file reads happen outside the lock: no file I/O under the state
	// lock, as pullPending does.
	if len(pending) == 1 {
		text, _ = PushText(pending[0].Entry, BindingFor(st, name), st.ReadFile)
		return text, true, nil
	}

	var b strings.Builder
	for _, p := range pending[:len(pending)-1] {
		fmt.Fprintf(&b, "── round %d: not delivered earlier (%s) ──\n", p.Entry.Round, p.Entry.Path)
		earlier, _ := PushText(p.Entry, BindingFor(st, name), st.ReadFile)
		b.WriteString(earlier)
		b.WriteString("\n\n")
	}
	last, _ := PushText(pending[len(pending)-1].Entry, BindingFor(st, name), st.ReadFile)
	b.WriteString(last)
	return b.String(), true, nil
}
