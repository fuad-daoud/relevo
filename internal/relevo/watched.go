package relevo

import (
	"sync"
	"time"
)

// watchKey names one process this daemon has seen alive: the pid alone is
// not enough, because a reused pid is a different process. startedAt is
// Unix seconds, exactly as store.Endpoint.StartedAt and
// store.GateRun.StartedAt record it.
type watchKey struct {
	pid       int
	startedAt int64
}

// Watched is the daemon's in-memory set of the processes it has seen alive.
// It answers the one question lostToRestart needs: was this process already
// known to be running to the daemon that is judging it?
//
// A nil *Watched is valid and has seen nothing, so a CLI one-shot, which
// never creates one, is judged as having seen nothing. It is deliberately
// never persisted: a daemon that has just started has, by definition, seen
// nothing.
type Watched struct {
	mu   sync.Mutex
	seen map[watchKey]struct{}
}

// NewWatched returns an empty Watched. Only `relevo daemon` creates one.
func NewWatched() *Watched {
	return &Watched{seen: map[watchKey]struct{}{}}
}

// Mark records that pid, started when startedAt says, was seen alive. It is
// nil-safe and ignores a pid that cannot be a real process (pid <= 0) and a
// start time that was never recorded (startedAt == 0), so a call site may
// Mark unconditionally on a liveness observation it trusts.
func (w *Watched) Mark(pid int, startedAt int64) {
	if w == nil || pid <= 0 || startedAt == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[watchKey]struct{}{}
	}
	w.seen[watchKey{pid: pid, startedAt: startedAt}] = struct{}{}
}

// Seen reports whether Mark recorded exactly this process. It is nil-safe
// and returns false on a nil *Watched: a daemon with no set has seen
// nothing.
func (w *Watched) Seen(pid int, startedAt int64) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.seen[watchKey{pid: pid, startedAt: startedAt}]
	return ok
}

// lostToRestart is the one predicate for "this daemon never saw it alive,
// and it started before this daemon did, so this daemon's own restart must
// have taken it down". It is true only when every clause holds:
//   - rt.StartedAt is non-zero, so only the daemon ever answers true and a
//     CLI one-shot never does;
//   - startedAt was recorded;
//   - the process started before this daemon;
//   - Watched has not seen it. A nil Watched has seen nothing.
func lostToRestart(rt Runtime, pid int, startedAt int64) bool {
	if rt.StartedAt.IsZero() || startedAt == 0 {
		return false
	}
	if !time.Unix(startedAt, 0).Before(rt.StartedAt) {
		return false
	}
	return !rt.Watched.Seen(pid, startedAt)
}
