package relevo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// FollowLog streams a binding's new log entries to emit as they appear, polling
// every interval. after is the last Seq the caller has already seen (0 for
// every entry). It returns nil when the binding reaches DONE or is removed --
// both mean there is nothing left to follow -- and ctx.Err() when ctx is
// cancelled. DONE is checked AFTER draining, so the entries that closed the
// round are emitted before the loop stops.
func FollowLog(ctx context.Context, rt Runtime, name string, after int, interval time.Duration, emit func(store.LogEntry)) error {
	if name == "" {
		return errors.New("follow log: empty binding name")
	}
	if interval <= 0 {
		return fmt.Errorf("follow log: interval must be positive, got %s", interval)
	}

	for {
		b, err := rt.Store.Load(name)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		entries, err := rt.Store.ReadLogAfter(name, after)
		if err != nil {
			return err
		}
		for _, e := range entries {
			emit(e)
			after = e.Seq
		}

		if b.State == store.StateDone {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
