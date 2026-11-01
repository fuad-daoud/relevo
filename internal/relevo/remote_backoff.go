package relevo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
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
}

func (r *remoteBackoff) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// skip reports whether a prefetch against server should not be dialled now.
func (r *remoteBackoff) skip(server string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clock().Before(r.until[server])
}

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
	r.until[server] = r.clock().Add(serverBackoff)
}

// serverUnreachable reports whether a failed read says the server itself did
// not answer. Any other error -- a 404 for one binding, a 401, an unknown
// server -- is a live server's answer about one binding. The client wraps
// transport errors, gateway statuses and the prefetch deadline in
// ErrUnreachable, but its retry sleep can return a bare context error, so a
// deadline counts too.
func serverUnreachable(err error) bool {
	return errors.Is(err, client.ErrUnreachable) || errors.Is(err, context.DeadlineExceeded)
}

// skippedFetch is what a skipped binding reconciles against: an unreachable
// read of its own server, so the apply half stamps and ages the outage exactly
// as it would after a dial, without making one. It is non-nil because a nil
// fetch makes the apply half fetch inline and dial anyway.
func skippedFetch(b store.Binding) *remoteFetch {
	return &remoteFetch{
		Name:   b.Name,
		Server: b.Builder.Server,
		Round:  b.Round,
		Err:    fmt.Errorf("%w: server backed off after a failed prefetch", client.ErrUnreachable),
	}
}

func boundedWait(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
