package verb

import (
	"log/slog"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// ExportDebounce is the shortest gap between the starts of two attempts that
// an outbox entry or a freshen hint may cause. Every machine pulls an attempt's
// changes as whole pages, and a page that changed in several attempts between
// two pulls travels once, so fewer, larger batches cost far less per entry.
// Five minutes trades how soon a peer sees a routine write for that saving; a
// write that cannot wait -- a gate, a handover, a takeover -- goes through
// SyncNow, which skips this gap. A freshen shares it
// because a peer exports no faster than this, so pulling sooner finds little.
const ExportDebounce = 5 * time.Minute

// OwedDebounce is the gap while a locked join's owed history is still going
// out. Each attempt sends one slice of it and gives the slot back, so the long
// export gap would stretch a join's send twentyfold; a join is one-off, and
// its batches are full ones.
const OwedDebounce = 15 * time.Second

// HotWindow is the idle pull window while another machine is writing. One
// attempt is a handful of calls to the remote, so 30 s is 2,880 attempts a day
// at most, and only for the minutes after something arrived; an idle machine
// never pays it.
const HotWindow = 30 * time.Second

// HotSince is how long an attempt that applied rows keeps the window hot. A
// machine whose peer just wrote is likely to see more soon; five minutes
// without a row is the cold window's own length, so a quiet peer costs nothing
// extra.
const HotSince = 5 * time.Minute

// ColdWindow is the idle pull window otherwise. The window exists to catch
// edits no round produced -- a config row, an installation touch, a confirm --
// so polling the network any faster on an idle machine would buy nothing and
// cost a round latency each time.
const ColdWindow = 5 * time.Minute

// Liveness is the state the liveness triggers share with the attempt that
// holds the slot. The engine holds it under the same lock as the slot flag it
// qualifies.
type Liveness struct {
	// Dirty is set by a trigger that found the slot held: the attempt in
	// flight may have read the outbox before the write that caused it, so the
	// slot's holder runs once more when it ends.
	Dirty bool
	// StartedAt is when the last attempt began. The debounce and the freshen
	// throttle measure from the start, so a slow attempt does not delay the
	// next one by its own length.
	StartedAt time.Time
	// AppliedAt is when the last attempt that applied rows ended. Zero means
	// no attempt has applied anything since this engine started.
	AppliedAt time.Time
	// OnSince is when this engine first saw sync on in the current on-period.
	// A machine that just came on has a peer that is likely active and no
	// history in this process, and AppliedAt cannot say so until an attempt
	// has applied something.
	OnSince time.Time
}

// BreakerDue reports whether the machine's breaker lets a call run now. An
// unreadable breaker reads as due: the per-call gate inside the pipeline still
// refuses, and skipping here on a transient read error would quietly stop a
// machine that is meant to be syncing.
func BreakerDue(runner *relevosync.Runner) bool {
	if runner == nil || runner.Local == nil {
		return true
	}
	due, err := relevosync.NewBreaker(runner.Local).Due()
	if err != nil {
		slog.Warn("sync: read the breaker", "err", err)
		return true
	}
	return due
}

// PullWindow is the idle gap in force at now.
func (l *Liveness) PullWindow(now time.Time) time.Duration {
	if WarmSince(l.AppliedAt, now) || WarmSince(l.OnSince, now) {
		return HotWindow
	}
	return ColdWindow
}

func WarmSince(at, now time.Time) bool {
	return !at.IsZero() && now.Sub(at) < HotSince
}
