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

// blipBackoff is the first armed window after a confirmation: five ticks at the
// 2s interval, so one more probe tells a blip from an outage without holding
// every binding stale for a full minute.
const blipBackoff = 10 * time.Second

// A slow read needs three counted failures spanning 30s: six prefetch bounds,
// fifteen ticks, so a load spike that answers within half a minute never
// declares.
const (
	softConfirmCount = 3
	softConfirmSpan  = 30 * time.Second
)

// A refused dial needs two counted failures spanning one prefetch bound
// across distinct ticks: a dead server answers nothing, so it is not slow.
const (
	hardConfirmCount = 2
	hardConfirmSpan  = 5 * time.Second
)

// remoteBackoff is the daemon's memory of servers that failed a prefetch. The
// zero value is ready to use.
type remoteBackoff struct {
	mu sync.Mutex
	// now is the clock the backoff reads. Nil means time.Now.
	now func() time.Time
	// outages is the consecutive-failure memory per server: a suspicion until
	// count and span confirm, then an armed episode the tick's skips enforce.
	outages map[string]serverOutage
}

// serverOutage is one server's consecutive-failure memory: counted failures
// without backing off until kind and span confirm, then the armed episode the
// tick's skips enforce.
type serverOutage struct {
	// first is the first consecutive failure: the episode's start when count
	// and span confirm it, and the stamp the bindings carry.
	first time.Time
	// last is the most recent consecutive failure: a quiet longer than twice
	// the backoff restarts at suspicion rather than confirming.
	last time.Time
	// opening is the first failure: the episode's account.
	opening error
	// until is the armed episode's end; zero while merely suspected.
	until time.Time
	// count is the consecutive failures counted, one per tick at most.
	count int
	// gen is the tick generation that counted last: later bindings in the
	// same generation reuse that failure without dialling.
	gen uint64
	// soft is whether any counted failure was slow rather than refused, so a
	// mixed run confirms on the slow rule.
	soft bool
}

// backoffReport is what one record call decided: a failure either starts an
// episode, continues one, or starts a suspicion, and a success either ends an
// episode or clears a suspicion quietly.
type backoffReport struct {
	// confirmed is set when this failure's count and span confirm: an episode
	// starts now, at start, and the caller logs it once per server.
	confirmed bool
	// inEpisode is set when this failure lands on an armed episode with recent
	// evidence: the backoff re-arms silently and the caller logs at debug.
	inEpisode bool
	// ended is set when this success closed an armed episode; start, opening
	// and end describe the episode that ended.
	ended bool
	// start is the episode's start: the first failure's time.
	start time.Time
	// opening is the episode's first failure.
	opening error
	// end is when the success cleared the episode, on the backoff's clock.
	end time.Time
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
	return r.clock().Before(r.outages[server].until)
}

// episodeStart reports the armed episode's start for the skipped fetch to
// stamp. A skip means an armed episode, so the start is always set here.
func (r *remoteBackoff) episodeStart(server string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outages[server].first
}

func (r *remoteBackoff) setOutage(server string, st serverOutage) {
	if r.outages == nil {
		r.outages = map[string]serverOutage{}
	}
	r.outages[server] = st
}

// countedIn reports whether gen already counted a failure for server, so a
// later binding in the same tick reuses that failure without dialling.
func (r *remoteBackoff) countedIn(server string, gen uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.outages[server]
	return ok && st.gen == gen
}

// recordFailure counts one failed server read: one per tick generation at
// most. A suspicion without backing off lasts until kind and span confirm --
// slow reads need more failures over longer than refused dials -- and a
// failure on an armed episode with recent evidence re-arms it silently.
//
// One failed read is the noise of a redeploy or a tunnel flap, so a single
// slow probe never declares: the tick reads its bindings one after another,
// which is why only the first failure per generation counts rather than every
// binding's.
func (r *remoteBackoff) recordFailure(server string, err error, gen uint64) backoffReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	st, ok := r.outages[server]
	if ok && now.Sub(st.last) > 2*serverBackoff {
		// The daemon probes every backoff window while bindings live, so a
		// longer quiet means nothing was dialling: the old memory is stale,
		// not evidence, and this failure starts a fresh suspicion.
		ok = false
	}
	if ok && st.gen == gen {
		return backoffReport{}
	}
	if !ok {
		r.setOutage(server, serverOutage{first: now, last: now, opening: err, count: 1, gen: gen, soft: failureSoft(err)})
		return backoffReport{}
	}
	if !st.until.IsZero() {
		st.last = now
		st.gen = gen
		st.count++
		if failureSoft(err) {
			st.soft = true
		}
		st.until = now.Add(serverBackoff)
		r.setOutage(server, st)
		return backoffReport{inEpisode: true, start: st.first, opening: st.opening}
	}
	soft := st.soft || failureSoft(err)
	count := st.count + 1
	span := now.Sub(st.first)
	if (!soft && count >= hardConfirmCount && span >= hardConfirmSpan) ||
		(count >= softConfirmCount && span >= softConfirmSpan) {
		st.last = now
		st.gen = gen
		st.count = count
		st.soft = soft
		st.until = now.Add(blipBackoff)
		r.setOutage(server, st)
		return backoffReport{confirmed: true, start: st.first, opening: st.opening}
	}
	st.last = now
	st.gen = gen
	st.count = count
	st.soft = soft
	r.setOutage(server, st)
	return backoffReport{}
}

// recordSuccess clears one server's consecutive-failure memory. A lone
// suspicion clears quietly; only an armed episode reports its end.
func (r *remoteBackoff) recordSuccess(server string) backoffReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	st, ok := r.outages[server]
	if !ok {
		return backoffReport{}
	}
	delete(r.outages, server)
	if st.until.IsZero() {
		return backoffReport{}
	}
	return backoffReport{ended: true, start: st.first, opening: st.opening, end: now}
}

// failureSoft reports whether an unreachable read was slow rather than
// refused: a deadline or a gateway status means the path answered slowly, so
// it needs the slow rule's longer span before it declares.
func failureSoft(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var gateway *client.GatewayError
	return errors.As(err, &gateway)
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
