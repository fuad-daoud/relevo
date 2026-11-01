package sync

// The steady pipeline drains this machine's outbox into the log and reads every
// other origin's entries back. It is the one body both triggers share, so the
// seal path and the idle window cannot drift into two orders or marker sets.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// errNoSync is what an attempt with nothing to drive reports; both triggers ask
// Enabled first, so arriving here means one of them forgot to.
var errNoSync = errors.New("sync: no client and no local file to drive")

// errStepTimedOut is what a step past its bound reports; the worker is cancelled
// with it, so the next attempt starts fresh rather than driving one that stopped
// answering.
var errStepTimedOut = errors.New("sync: a step ran past its deadline")

// StepTimeout bounds one data step of the steady pipeline. A step past it is a
// worker that stopped answering rather than a slow remote, so the worker is
// cancelled instead of holding the next tick. It sits just below the pipe
// client's transfer-sized call bound, so the deliberate cancel wins and the step
// releases as a stop rather than timing out as a death.
const StepTimeout = 9 * time.Minute

// SteadyImportDeadline bounds one attempt's import loop. It sits under the
// 5-minute idle window so two attempts never overlap on the one worker. A stop
// at this bound is not an error; the next tick continues from the marks moved.
const SteadyImportDeadline = 4 * time.Minute

// SteadyResult is what one attempt moved, in the shape the trigger logs.
type SteadyResult struct {
	// Exported is how many of this machine's entries the attempt handed the log.
	Exported int
	// Applied is how many of another origin's entries the attempt wrote.
	Applied int
	// Err is the first failure the attempt hit, nil on success.
	Err error
	// Attention is whether Err is one a human has to act on.
	Attention bool
	// Trouble is what the attempt's import reported besides applied entries: a
	// held origin, a dropped batch, a sequence gap.
	Trouble Trouble
}

// On reports whether this machine's sync is on as the local marker says; a
// machine that never turned sync on has nothing to poll for. It reads only the
// machine-local marker, never the client: a tick asks this before taking the sync
// slot while a verb may be replacing the transport under it. An unreadable
// marker reads as on, so a transient read error never quietly stops a syncing
// machine.
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

// SyncOnce runs one steady attempt and records its outcome in the local markers;
// every path leaves a tick marker, so a machine that gave up says so rather than
// looking idle.
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

// attempt is the pipeline without the marker writing: export first so the log
// learns what this machine wrote, then import. The capture runs before the
// export, so an edit to a hook script or native agent file on disk reaches the
// peers with this attempt rather than the next.
func (r *Runner) attempt(ctx context.Context, shared *db.DB) SteadyResult {
	CaptureFiles(shared)
	t := NewStopTransport(r.Client)
	defer r.Track(t)()
	out := r.exchange(ctx, shared, t)
	r.countBytes(ctx, t)
	return out
}

// exchange is the export then the import over one attempt's transport. The marks
// are published after the import, not inside it: what a publish reports is where
// this machine has got to, only true once the import has finished moving them.
func (r *Runner) exchange(ctx context.Context, shared *db.DB, t *StopTransport) SteadyResult {
	var out SteadyResult
	r.recordRemoteKDF(ctx, t)
	// An adopted identity checks the claim before either half runs: another
	// machine holding the token means two machines would append to one log.
	if err := r.identityGate(ctx, t); err != nil {
		return r.failed(out, err)
	}
	if err := r.pendingAdoptGate(shared); err != nil {
		out.Trouble.Held = append(out.Trouble.Held, err.Error())
		return out
	}
	// The stale stop reads the two local epoch facts and nothing else: a machine
	// whose held newest epoch is below the recorded remote epoch neither exports
	// nor imports until its owner unlocks with the new passphrase, because a
	// stale exporter appends entries current readers refuse, and a stale importer
	// would apply them.
	if err := r.epochGate(); err != nil {
		if !errors.Is(err, synclog.ErrEpochMoved) && !errors.Is(err, ErrEpochReplaced) {
			// An unreadable key is this machine's own failure: the attempt reports
			// it failed rather than running either half unverified.
			return r.failed(out, err)
		}
		out.Trouble.Held = append(out.Trouble.Held, err.Error())
		return out
	}
	exported, locked, err := r.exportStep(ctx, shared, t)
	if err != nil {
		return r.failed(out, err)
	}
	out.Exported = exported
	// The extension vouching for this origin's confirmed tail goes out before the
	// import reads the cuts, so this attempt's own reads see the fence believed in.
	PublishOwnCut(ctx, shared, r.Local, t)
	imported, err := r.drainImport(ctx, shared, t)
	// The settles run after the import: what they render is the config the
	// import wrote, only true once committed.
	SettleFiles(shared)
	if err != nil {
		return r.failed(out, err)
	}
	out.Applied = imported.Applied
	out.Trouble = TroubleFrom(imported)
	if locked {
		out.Trouble.Held = append(out.Trouble.Held, synclog.ErrLockedSignedOrigin.Error())
	}
	r.publishMarks(shared, t, imported.Moved())
	now := time.Now().UTC()
	r.publishTraffic(t, now)
	compacted := r.compact(shared, t, now)
	r.publishSize(shared.Origin(), t, compacted, out.Exported > 0, now)
	r.measureStorage(t, now)
	return out
}

// exportStep runs the exporter under the attempt bound; locked signed origins
// are tolerated so the import half still runs.
func (r *Runner) exportStep(ctx context.Context, shared *db.DB, t *StopTransport) (int, bool, error) {
	var exported int
	err := within(ctx, t, r.stepTimeout(), func() error {
		key, err := SigningKey(r.Local)
		if err != nil {
			return err
		}
		exp := synclog.NewExporter(shared, t)
		exp.SetKey(key)
		BindDocs(exp, shared)
		res, err := exp.Export()
		exported = res.Appended
		return err
	})
	if err != nil {
		if errors.Is(err, synclog.ErrLockedSignedOrigin) {
			return 0, true, nil
		}
		return 0, false, err
	}
	return exported, false, nil
}

// identityGate is CheckAdoptedClaim under the attempt's step bound.
func (r *Runner) identityGate(ctx context.Context, t *StopTransport) error {
	if r.Local == nil {
		return nil
	}
	return within(ctx, t, r.stepTimeout(), func() error {
		return CheckAdoptedClaim(ctx, r.Local, t)
	})
}

// pendingAdoptGate stops steady attempts while an adoption is unfinished, when
// file and database disagree about identity and neither half is safe.
func (r *Runner) pendingAdoptGate(shared *db.DB) error {
	pending, ok, err := ReadAdoptPending(shared, r.Local)
	if err != nil || !ok {
		return err
	}
	return fmt.Errorf("adoption of %s is unfinished: run relevo db sync enable --as %s", pending.ID, pending.ID)
}

// marksEvery is the shortest gap between two publishes of this machine's marks.
// A publish is a Turso write and a push; the throttle is safe because a mark only
// grows, so a fallen-behind publish only makes another machine's floor more cautious.
const marksEvery = 15 * time.Minute

// publishMarks tells the log how far this machine has applied of every origin,
// at most once every marksEvery and only after an import that moved a mark. A
// failed publish is logged, not folded into the result: the exchange succeeded,
// and the next attempt republishes anyway.
func (r *Runner) publishMarks(shared *db.DB, t *StopTransport, moved bool) {
	if !moved || r.Local == nil {
		return
	}
	now := time.Now().UTC()
	if !r.marksDue(now) {
		return
	}
	marks, err := shared.ImportMarks()
	if err != nil {
		slog.Warn("sync: read this machine's import marks", "err", err)
		return
	}
	if err := t.PublishMarks(marks, now); err != nil {
		slog.Warn("sync: publish this machine's import marks", "err", err)
		return
	}
	r.put(KeyMarksPublished, now)
}

// marksDue reports whether a publish is due: due when the marker is absent, and
// again once marksEvery has passed. An unparsable marker reads as due: the worst
// it costs is one publish the throttle would have skipped.
func (r *Runner) marksDue(now time.Time) bool {
	var last time.Time
	if err := marker(r.Local, KeyMarksPublished, &last); err != nil {
		slog.Warn("sync: read the marks marker", "err", err)
		return true
	}
	return last.IsZero() || now.Sub(last) >= marksEvery
}

// Track records t as the work in flight until the returned release runs, so Stop
// can end it from outside the sync slot.
func (r *Runner) Track(t *StopTransport) (release func()) {
	if r == nil {
		return func() {}
	}
	r.active.Store(t)
	return func() { r.active.CompareAndSwap(t, nil) }
}

// Stop ends the work in flight, if any. A disable or retry calls it before waiting
// for the sync slot, so it never waits out a whole data-step bound.
func (r *Runner) Stop() {
	if r == nil {
		return
	}
	if t := r.active.Load(); t != nil {
		t.Stop()
	}
}

// drainImport reads the other origins page by page, each Import under the step
// bound, until a run moves no mark or the attempt's deadline passes.
func (r *Runner) drainImport(ctx context.Context, shared *db.DB, t *StopTransport) (synclog.ImportResult, error) {
	deadline := time.Now().Add(SteadyImportDeadline)
	return drainImport(func() (synclog.ImportResult, error) {
		var res synclog.ImportResult
		err := within(ctx, t, r.stepTimeout(), func() error {
			if err := ReplayRefusedPass(shared, r.Local, t); err != nil {
				slog.Warn("sync: replay refused pass", "err", err)
			}
			imp := synclog.NewImporter(shared, t)
			if ierr := BindVerifier(imp, r.Local); ierr != nil {
				return ierr
			}
			BindDocs(imp, shared)
			var ierr error
			res, ierr = imp.Import()
			return ierr
		})
		return res, err
	}, deadline, time.Now)
}

// TroubleFrom is the importer's report as the marker stores it, each trouble in
// the importer's own sentence naming the installation and sequence to act on.
func TroubleFrom(res synclog.ImportResult) Trouble {
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

// failed is the result every error path takes: counts already moved are kept,
// and Attention names the one a human must unblock.
func (r *Runner) failed(out SteadyResult, err error) SteadyResult {
	out.Err = err
	out.Attention = errors.Is(err, ErrAuthRefused)
	return out
}

// within runs one step and gives up on it after the bound. A step past it is a
// worker that stopped answering: its transport is stopped, cancelling the waited
// call and refusing later ones, and the step reports timed out. This waits for
// the step to return, so no leftover call runs beside the next one.
func within(ctx context.Context, t *StopTransport, bound time.Duration, step func() error) error {
	timer := time.NewTimer(bound)
	defer timer.Stop()
	return withinUntil(ctx, t, timer.C, step)
}

// withinUntil runs one step until expired receives or ctx is done, split from
// within so a test can fire the bound without relying on clocks.
func withinUntil(ctx context.Context, t *StopTransport, expired <-chan time.Time, step func() error) error {
	done := make(chan error, 1)
	go func() { done <- step() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Stop()
		<-done
		return ctx.Err()
	case <-expired:
		t.Stop()
		<-done
		return errStepTimedOut
	}
}

// cancelWorker stops the worker a transport is driving, when it can: killing the
// waited-on process is what releases a step past its bound.
func cancelWorker(client synclog.LogTransport) {
	if c, ok := client.(interface{ Cancel() }); ok {
		c.Cancel()
	}
}

// stepTimeout is the bound one step runs under: the runner's own when a test
// shortened it, else the package default.
func (r *Runner) stepTimeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return StepTimeout
}

// record writes the markers one attempt leaves. The tick marker goes on every
// path so a machine that gave up says so; backlog, trouble and times go on only
// once finished, because those are what a completed attempt measured. The latch
// belongs to the breaker and is never touched here.
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

// maxDroppedReports caps the cumulative dropped list, keeping the tail: the
// newest reports are the ones still worth acting on.
const maxDroppedReports = 50

// putTrouble writes the attempt's report through RecordTrouble, carrying dropped
// reports forward.
func (r *Runner) putTrouble(t Trouble) {
	RecordTrouble(r.Local, t)
}

// RecordTrouble writes one import's report to the trouble marker, carrying dropped
// reports forward: a dropped batch moved its origin's mark past it, so it is never
// offered again and a clean import must not erase it. Held and gap reports are
// per-run because they recur while true. The steady attempt and the one-shot pull
// share it, so a status read agrees with whichever ran last.
func RecordTrouble(kv db.KV, t Trouble) {
	var prior Trouble
	if err := marker(kv, KeyTrouble, &prior); err != nil {
		slog.Warn("sync: read the trouble marker", "err", err)
	} else {
		t.Dropped = capDropped(append(prior.Dropped, t.Dropped...))
	}
	body, err := json.Marshal(t)
	if err != nil {
		slog.Warn("sync: encode a marker", "key", KeyTrouble, "err", err)
		return
	}
	if err := kv.KVPut(KeyTrouble, body); err != nil {
		slog.Warn("sync: write a marker", "key", KeyTrouble, "err", err)
	}
}

// capDropped keeps the newest dropped reports past the cap.
func capDropped(list []string) []string {
	if len(list) <= maxDroppedReports {
		return list
	}
	return list[len(list)-maxDroppedReports:]
}

// put writes one marker, logging a failure rather than folding it into the result:
// the next attempt repeats the measurement, so a lost marker costs one window.
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

// recordRemoteKDF is the exchange's hello learning whether the remote holds a key,
// under the step bound so a silent remote costs one step.
func (r *Runner) recordRemoteKDF(ctx context.Context, t *StopTransport) {
	ctx, cancel := context.WithTimeout(ctx, r.stepTimeout())
	defer cancel()
	RecordRemoteKDF(ctx, r.Local, t)
}

// epochGate is the stale stop the exchange runs before both halves: ErrEpochMoved
// when the keyring's newest epoch sits below the recorded remote epoch,
// ErrEpochReplaced on a matching epoch with a mismatched check, nil otherwise. A
// locked machine never trips it, holding no key; an unrecorded epoch leaves the
// gate open, as a fact an unreachable remote left in place.
func (r *Runner) epochGate() error {
	ring, ok, err := heldKeyring(r.Local)
	if err != nil || !ok {
		return err
	}
	remote, ok, err := remoteEpoch(r.Local)
	if err != nil || !ok {
		return err
	}
	if ring.Epoch < remote {
		return synclog.ErrEpochMoved
	}
	if ring.Epoch == remote && checkMismatch(r.Local) {
		return ErrEpochReplaced
	}
	return nil
}

// heldKeyring reads the machine's keyring over a kv handle, ok false when it holds
// no key: the read SigningKey does, over the whole ring rather than one row.
func heldKeyring(kv db.KV) (Keyring, bool, error) {
	l, isDB := kv.(Local)
	if !isDB || l == nil {
		return Keyring{}, false, nil
	}
	return ReadKeyring(l)
}
