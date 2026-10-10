package sync

// The steady pipeline runs while sync is on: it drains this machine's outbox
// into the log and reads every other origin's entries back. It is the one body
// both triggers share, so the seal path and the idle window cannot drift into
// two orders or two marker sets.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// errNoSync is what an attempt with nothing to drive reports. Both triggers ask
// Enabled first, so arriving here means one of them forgot to.
var errNoSync = errors.New("sync: no client and no local file to drive")

// errStepTimedOut is what a step that ran past its bound reports. The worker is
// cancelled with it, so the next attempt starts a fresh process rather than
// driving the one that stopped answering.
var errStepTimedOut = errors.New("sync: a step ran past its deadline")

// StepTimeout bounds one data step of the steady pipeline. A step that outlives
// it is a worker that stopped answering rather than a slow remote, so the
// worker is cancelled and the step reported instead of holding the next tick.
// It sits just below the pipe client's transfer-sized call bound, so the
// deliberate cancel is taken first and the step is released as a stop rather
// than left for the transport to time out and count as a death.
const StepTimeout = 9 * time.Minute

// SteadyImportDeadline bounds one attempt's import loop. It sits under the
// 5-minute idle window so two attempts never overlap: an attempt still
// importing when the next window opens would otherwise drive the one worker
// from two triggers. A stop at this bound is not an error; the next tick
// continues from the marks this one moved.
const SteadyImportDeadline = 4 * time.Minute

// SteadyResult is what one attempt moved, in the shape the trigger logs it.
type SteadyResult struct {
	// Exported is how many of this machine's entries the attempt handed the
	// log.
	Exported int
	// Applied is how many of another origin's entries the attempt wrote.
	Applied int
	// Err is the first failure the attempt hit, nil on success.
	Err error
	// Attention is whether Err is one a human has to act on.
	Attention bool
	// Trouble is what the attempt's import reported besides applied entries:
	// an origin a newer writer held, a batch a refusal dropped, a sequence gap.
	Trouble Trouble
}

// On reports whether this machine's sync is turned on as the local marker says.
// A machine that never turned sync on has nothing to poll the network for.
//
// It reads only the machine-local marker, never the client: a tick asks this
// before it takes the sync slot, while a verb may be replacing the transport
// under that slot, and the marker is the field neither writes.
//
// An unreadable marker reads as on: a caller has already established that there
// is a machine-local file, and skipping on a transient read error would quietly
// stop syncing a machine that is meant to be syncing.
func (r *Runner) On() bool {
	if r == nil || r.Local == nil {
		return false
	}
	state, err := ReadState(r.Local)
	if err != nil {
		slog.Warn("sync: read the enabled marker", "err", err)
		return true
	}
	return state.Enabled
}

// SyncOnce runs one steady attempt and records its outcome in the local
// markers. Every path leaves a tick marker behind, so a machine that gave up
// says so rather than looking idle.
func (r *Runner) SyncOnce(ctx context.Context, shared *db.DB) SteadyResult {
	if !r.Enabled() || shared == nil {
		return SteadyResult{Err: errNoSync}
	}
	start := time.Now()
	out := r.attempt(ctx, shared)
	r.record(out)
	r.RecordAttempt(start, out)
	return out
}

// attempt is the pipeline without the marker writing. The order is the
// exchange's: export first, so the log learns what this machine wrote, then
// import, so the file learns what the others wrote.
func (r *Runner) attempt(ctx context.Context, shared *db.DB) SteadyResult {
	t := NewStopTransport(r.Client)
	defer r.Track(t)()
	out := r.exchange(ctx, shared, t)
	r.countBytes(ctx, t)
	return out
}

// exchange is the export then the import over one attempt's transport.
func (r *Runner) exchange(ctx context.Context, shared *db.DB, t *StopTransport) SteadyResult {
	var out SteadyResult
	if err := within(ctx, t, r.stepTimeout(), func() error {
		res, err := synclog.NewExporter(shared, t).Export()
		out.Exported = res.Appended
		return err
	}); err != nil {
		return r.failed(out, err)
	}
	imported, err := r.drainImport(ctx, shared, t)
	if err != nil {
		return r.failed(out, err)
	}
	out.Applied = imported.Applied
	out.Trouble = troubleFrom(imported)
	return out
}

// Track records t as the work in flight until the returned release runs, so
// Stop can end it from outside the sync slot that work holds.
func (r *Runner) Track(t *StopTransport) (release func()) {
	if r == nil {
		return func() {}
	}
	r.active.Store(t)
	return func() { r.active.CompareAndSwap(t, nil) }
}

// Stop ends the work in flight, if there is any: a steady attempt or a push or
// pull verb. A disable or retry calls it before it waits for the sync slot, so
// it does not wait out work that can run for a whole data-step bound.
func (r *Runner) Stop() {
	if r == nil {
		return
	}
	if t := r.active.Load(); t != nil {
		t.Stop()
	}
}

// drainImport reads the other origins page by page, each Import under the step
// bound, until a run moves no mark or the attempt's own deadline passes.
func (r *Runner) drainImport(ctx context.Context, shared *db.DB, t *StopTransport) (synclog.ImportResult, error) {
	deadline := time.Now().Add(SteadyImportDeadline)
	return drainImport(func() (synclog.ImportResult, error) {
		var res synclog.ImportResult
		err := within(ctx, t, r.stepTimeout(), func() error {
			var ierr error
			res, ierr = synclog.NewImporter(shared, t).Import()
			return ierr
		})
		return res, err
	}, deadline, time.Now)
}

// troubleFrom is the importer's report as the marker stores it: each trouble in
// the importer's own sentence, so a status surface names the installation and
// the sequence a reader has to act on without re-deriving either.
func troubleFrom(res synclog.ImportResult) Trouble {
	var t Trouble
	for _, held := range res.Held {
		t.Held = append(t.Held, held.String())
	}
	for _, drop := range res.Dropped {
		t.Dropped = append(t.Dropped, drop.String())
	}
	for _, stall := range res.Stalled {
		t.Held = append(t.Held, stall.String())
	}
	for _, gap := range res.Gaps {
		t.Gaps = append(t.Gaps, gap.String())
	}
	return t
}

// failed is the result every error path takes: the counts already moved are
// kept, the error is reported, and Attention names the one a human must
// unblock.
func (r *Runner) failed(out SteadyResult, err error) SteadyResult {
	out.Err = err
	out.Attention = errors.Is(err, ErrAuthRefused)
	return out
}

// within runs one step and gives up on it after the bound. A step that outlives
// the bound is a worker that stopped answering: the step's transport is
// stopped, which cancels the call it was waiting on and refuses every call
// after it, and the step is reported as timed out. This waits for the step to
// come back, so no call a previous step left behind runs beside the next one.
func within(ctx context.Context, t *StopTransport, bound time.Duration, step func() error) error {
	done := make(chan error, 1)
	go func() { done <- step() }()

	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Stop()
		<-done
		return ctx.Err()
	case <-timer.C:
		t.Stop()
		<-done
		return errStepTimedOut
	}
}

// cancelWorker stops the worker a transport is driving, when it can. Killing
// the process a call waits on is what releases a step that ran past its bound;
// a transport with no worker behind it has nothing to stop.
func cancelWorker(client synclog.LogTransport) {
	if c, ok := client.(interface{ Cancel() }); ok {
		c.Cancel()
	}
}

// stepTimeout is the bound one step runs under: the runner's own when a test
// shortened it, the package default otherwise.
func (r *Runner) stepTimeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return StepTimeout
}

// record writes the markers one attempt leaves. The tick marker goes on every
// path, so a machine that gave up says so; the backlog, the trouble report and
// the exchange times go on only once the attempt finished, because those are
// the facts a completed attempt measured. The latch belongs to the breaker and
// is never touched here.
func (r *Runner) record(out SteadyResult) {
	now := time.Now().UTC()
	r.put(KeyLastTick, tick{At: now, OK: out.Err == nil})
	if out.Err != nil {
		return
	}
	r.put(KeyBacklog, int64(0))
	r.putTrouble(out.Trouble)
	r.put(KeyTimes, Times{Export: now, Import: now})
}

// maxDroppedReports caps the cumulative dropped list. It is a report a reader
// scrolls, not a log: the newest reports are the ones still worth acting on, so
// a machine that drops for a long time keeps the tail rather than growing a
// marker without bound.
const maxDroppedReports = 50

// putTrouble writes the attempt's report, carrying dropped reports forward. A
// dropped batch has already had its origin's mark moved past it, so it is never
// offered again and only a reader can act on it; a later clean attempt must not
// erase it. Held and gap reports are per-run because they recur while they are
// true, so they are written as this attempt found them.
func (r *Runner) putTrouble(t Trouble) {
	var prior Trouble
	if err := marker(r.Local, KeyTrouble, &prior); err != nil {
		slog.Warn("sync: read the trouble marker", "err", err)
	} else {
		t.Dropped = capDropped(append(prior.Dropped, t.Dropped...))
	}
	r.put(KeyTrouble, t)
}

// capDropped keeps the newest dropped reports, dropping the oldest when the
// list grows past the cap.
func capDropped(list []string) []string {
	if len(list) <= maxDroppedReports {
		return list
	}
	return list[len(list)-maxDroppedReports:]
}

// put writes one marker, logging a failure rather than folding it into the
// result: the next attempt repeats the same measurement, so a lost marker costs
// one window rather than a wrong answer.
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
