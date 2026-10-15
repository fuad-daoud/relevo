package relevo

import (
	"context"
	"sync"
	"time"
)

// prefetchDeadline bounds the tick's first read of a binding from its server.
// The client retries an unreachable server, so without this one dead binding
// costs the tick the client's whole retry budget. It covers only that first
// read: a catch-up download legitimately runs longer. A variable so a test can
// shorten it.
var prefetchDeadline = 5 * time.Second

// serverBackoff is how long ticks leave a server alone after a prefetch against
// it failed, before one binding probes whether it is back.
const serverBackoff = time.Minute

// remoteBackoff is the daemon's memory of servers that failed a prefetch. The
// zero value is ready to use.
type remoteBackoff struct {
	mu sync.Mutex
	// now is the clock the backoff reads. Nil means time.Now.
	now func() time.Time
	// until is when a server that failed may be tried again.
	until map[string]time.Time
	// failed is the servers that failed during the current tick, so the rest of
	// that tick's bindings skip them without waiting on the backoff clock.
	failed map[string]bool
}

func (r *remoteBackoff) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// beginTick forgets which servers failed in the previous tick; the cross-tick
// backoff is untouched.
func (r *remoteBackoff) beginTick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = nil
}

// skip reports whether a prefetch against server should not be dialled now.
func (r *remoteBackoff) skip(server string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed[server] || r.clock().Before(r.until[server])
}

// record notes the outcome of a prefetch that was dialled. A failure arms the
// backoff and skips the server for the rest of the tick; a success ends it.
func (r *remoteBackoff) record(server string, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !failed {
		delete(r.until, server)
		return
	}
	if r.until == nil {
		r.until = map[string]time.Time{}
	}
	if r.failed == nil {
		r.failed = map[string]bool{}
	}
	r.until[server] = r.clock().Add(serverBackoff)
	r.failed[server] = true
}

// skippedFetch is what a skipped binding reconciles against. It is non-nil
// because a nil fetch makes the apply half fetch inline and dial anyway; its
// empty identity never matches a binding, so the apply half leaves the
// binding as it is.
func skippedFetch() *remoteFetch {
	return &remoteFetch{}
}

// boundedWait returns ctx with a deadline of d, or ctx itself when d is not
// positive.
func boundedWait(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
