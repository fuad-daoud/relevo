package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrAuthRefused is the one failure a retry cannot fix: the remote rejected this
// installation's token, so every later attempt is refused the same way until a
// new token is minted. It is the whole difference between a machine that is
// behind and a machine a human has to unblock.
var ErrAuthRefused = errors.New("sync: the remote refused this installation's token")

// errNoSync is what an attempt with nothing to drive returns. It is a
// programming error rather than a remote failure: both triggers ask Enabled
// first, so arriving here means one of them forgot to.
var errNoSync = errors.New("sync: no client and no local file to drive")

// DefaultTimeout bounds one push-then-pull. The bound is load-bearing: it is
// what keeps a blackholed network off the seal path and out of a tick, so a
// remote that never answers costs one bounded wait rather than a hung round.
const DefaultTimeout = 20 * time.Second

// KeyStats is the snapshot of what the remote last reported, kept where the sync
// view will read it. The statusline never reads this key: it reads one marker at
// a time and formats a token.
const KeyStats = "sync.stats"

// authRefusedMessage is what the attention marker says for a refused token. It
// is fixed text rather than the remote's own words, because the message is
// rendered in a statusline and travels in a log line, and a body the remote
// chose has no business in either.
const authRefusedMessage = "the remote refused this installation's token"

// Runner drives one bounded push-then-pull and records the outcome in the
// machine-local markers. It never builds a client: a caller hands one in, and a
// nil client is a machine that has no sync to run at all.
type Runner struct {
	// Client is the remote seam. Nil means sync is off, and an attempt refuses
	// rather than inventing a handle.
	Client SyncClient
	// Local is the machine-local kv the markers are written through, so a
	// marker can never reach the file that leaves this machine.
	Local db.KV
	// Now stamps the markers. Nil means time.Now.
	Now func() time.Time
	// Timeout bounds one attempt. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Outcome is what one attempt did, in the shape the markers record it.
type Outcome struct {
	// OK is whether push, pull and stats all finished without error.
	OK bool
	// Applied is whether the pull rebased remote changes onto the local set.
	Applied bool
	// Stats is what the remote reported. It is only meaningful when Measured.
	Stats Stats
	// Measured is whether stats answered, and so whether the backlog and the
	// snapshot were refreshed rather than left at the last measurement.
	Measured bool
	// Err is the first failure the attempt hit.
	Err error
	// Attention is whether Err is one a human has to act on.
	Attention bool
}

// Enabled reports whether there is anything to drive. A machine with no client
// or no local file has no sync, and both triggers skip it entirely rather than
// queueing work that can only fail.
func (r *Runner) Enabled() bool {
	return r != nil && r.Client != nil && r.Local != nil
}

// Ensure reports whether there is a client to drive, building one through
// open the first time. It is how a runner that starts clientless becomes usable
// without a restart: the mark decides, and the open only runs for a machine
// whose mark is on.
//
// Nil-receiver safe, like Enabled: a missing runner has nothing to ensure.
// A machine whose mark is off reports false without opening anything; a mark
// that cannot be read is treated as on, mirroring On. Dropping a stale client
// is the callers' job (enable and disable both do it): with a preset client
// this check short-circuits before reading the mark, which keeps every
// existing caller on today's path.
//
// The open runs under DefaultTimeout on top of the caller's context, so a
// blackholed dial costs one bounded wait wherever Ensure is called from —
// including a seal path that must stay cheap. Callers pass an open that
// validates locally (settings, token) before any dial, so a refusal is fast
// and a dial only happens for a machine that has something to reach with.
//
// One instance belongs to one driver at a time: the daemon's verb guard and
// its in-flight drop mean two Ensures never overlap on the same instance, and
// the cockpit's instance is never shared. Sharing one Runner across drivers
// without that serialization would race the Client swap below.
func (r *Runner) Ensure(ctx context.Context, open func(context.Context) (SyncClient, error)) bool {
	if r == nil {
		return false
	}
	if r.Enabled() {
		return true
	}
	if r.Local == nil || open == nil {
		return false
	}
	if state, err := ReadState(r.Local); err == nil && !state.Enabled {
		return false
	}
	octx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	c, err := open(octx)
	if err != nil {
		return false
	}
	r.Client = c
	return true
}

// On reports whether this machine's sync is turned on as the local marker says.
// A machine that never turned sync on has nothing to poll the network for, so
// it is skipped rather than driven once a window against a marker reading off.
//
// An unreadable marker reads as on: Enabled has already established that there
// is a client and a local file, and skipping on a transient read error would
// quietly stop syncing a machine that is meant to be syncing.
func (r *Runner) On() bool {
	if !r.Enabled() {
		return false
	}
	state, err := ReadState(r.Local)
	if err != nil {
		slog.Warn("sync: read the enabled marker", "err", err)
		return true
	}
	return state.Enabled
}

// SyncOnce is one bounded push-then-pull with the markers written for whatever
// came out of it. It is the single body both triggers share, so the seal path
// and the idle window cannot drift into two different orders or two different
// marker sets.
//
// Push goes first: a pull that ran first would have every unpushed local change
// to roll back and replay, and pushing first leaves that replay set as small as
// it can be.
//
// Every call runs under one timeout, so a stalled remote costs the same bounded
// wait whichever call stalled. Nothing is swallowed -- the attempt returns the
// error it hit -- but it never returns without a marker, because a machine that
// gave up has to say so rather than look idle.
func (r *Runner) SyncOnce(ctx context.Context) Outcome {
	if !r.Enabled() {
		return Outcome{Err: errNoSync}
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	out := r.attempt(ctx)
	r.record(out)
	return out
}

// attempt is the push-then-pull with no marker writing: it reports what happened
// and leaves the recording to SyncOnce, which owns it on every path so that no
// outcome can be produced without one.
func (r *Runner) attempt(ctx context.Context) Outcome {
	var out Outcome
	if err := r.Client.Push(ctx); err != nil {
		return r.failed(out, err)
	}
	applied, err := r.Client.Pull(ctx)
	if err != nil {
		return r.failed(out, err)
	}
	out.Applied = applied

	stats, err := r.Client.Stats(ctx)
	if err != nil {
		// The change set already moved, but the attempt cannot be called good
		// without knowing how far behind the machine still is, so it lands as
		// behind and the next attempt measures again.
		return r.failed(out, fmt.Errorf("sync: stats after push and pull: %w", err))
	}
	out.OK = true
	out.Stats = stats
	out.Measured = true
	return out
}

// failed is the outcome every error path takes: not OK, the error kept, and
// Attention set only for the refusal a human has to unblock.
func (r *Runner) failed(out Outcome, err error) Outcome {
	out.OK = false
	out.Stats = Stats{}
	out.Measured = false
	out.Err = err
	out.Attention = errors.Is(err, ErrAuthRefused)
	return out
}

// record writes the markers for one attempt. Every outcome goes through here,
// so no attempt can finish without leaving a trace behind.
//
// A failure does not overwrite the backlog or the snapshot: it never measured
// either, and writing a zero there would report a change set nobody asked
// about. The tick marker and the attention marker are the two that decide the
// token, and both are written on every path.
//
// A marker that cannot be written is logged rather than folded into the outcome:
// the outcome describes the sync, and the next attempt repeats the same
// measurement, so a lost marker costs one window rather than a wrong answer.
func (r *Runner) record(out Outcome) {
	now := r.now().UTC()
	r.put(KeyLastTick, tick{At: now, OK: out.OK})
	if out.Attention {
		r.put(KeyAttention, attention{At: now, Message: authRefusedMessage})
	} else {
		r.delete(KeyAttention)
	}
	if !out.Measured {
		return
	}
	r.put(KeyBacklog, out.Stats.CdcOperations)
	r.put(KeyStats, out.Stats)
}

func (r *Runner) put(key string, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		slog.Warn("sync: encode a marker", "key", key, "err", err)
		return
	}
	if err := r.Local.KVPut(key, body); err != nil {
		slog.Warn("sync: write a marker", "key", key, "err", err)
	}
}

func (r *Runner) delete(key string) {
	if err := r.Local.KVDelete(key); err != nil {
		slog.Warn("sync: delete a marker", "key", key, "err", err)
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultTimeout
}
