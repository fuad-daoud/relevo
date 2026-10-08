package relevo

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// VerbRunner runs one sync verb against handles a process already holds.
//
// It is what the owner's OnSyncVerb hook calls and what the cockpit's sync
// actions call, so there is exactly one set of verbs' semantics in the tree and
// no caller opens the database itself to run one. It runs against the daemon's
// shared and machine-local handles -- the pair already open under the lock --
// so a client asking for a verb never opens a file and never competes for the
// lock the daemon is holding.
//
// Enable runs whole: the preflight decides the remote and the token, the runner
// opens the log transport, and the join bootstraps, imports and reconcile-
// exports before the mark goes on. Push and pull are the two halves of the
// steady exchange, each run alone. Retry clears a breaker latch. The turn-off
// runs whole, deleting the replica the worker owns; status is not a verb at all
// -- it is a read of the same local rows.
//
// A verb is serialized against the daemon's own sync triggers through the same
// guard: one push-then-pull at a time per daemon, whatever asked for it.
type VerbRunner struct {
	// Shared is the daemon's direct shared handle.
	Shared *db.DB
	// Local is the machine-local file beside it, where the settings, the token
	// and the mark live.
	Local relevosync.Local
	// Path is the shared file's path.
	Path string
	// ReplicaPath is the worker's replica file beside the shared one. The
	// turn-off deletes it and the driver files named after it.
	ReplicaPath string
	// Runner is the daemon's sync runner. An enable that finishes leaves the
	// worker it built here, so the daemon holds one transport however many calls
	// it carries.
	Runner *relevosync.Runner
	// Open builds the log transport an enable drives once the preflight has
	// decided the remote. It is the one place a worker is constructed for a
	// verb, so the remote and the token it is built from are the ones just
	// stored.
	Open func(context.Context) (synclog.LogTransport, error)
	// Serialize runs fn under the daemon's in-flight sync guard. Nil runs it
	// inline, which is what a cockpit wants: it is not the daemon and has no
	// trigger to be serialized against.
	Serialize func(fn func())
	// ClientName is what the remote is told this client is called.
	ClientName string

	// joinMu guards joinClient and joinStopped: the one sync a disable must be
	// able to reach before the machine is marked on. An enable publishes its
	// join transport here once the preflight has chosen the remote, because a
	// joined machine reads off for the whole join and a disable arriving
	// mid-join would otherwise find no transport to stop.
	joinMu      sync.Mutex
	joinClient  synclog.LogTransport
	joinStopped bool

	// clientMu guards the runner's Client, the steady worker a queued disable or
	// retry must release before it waits for the slot. It is separate from
	// joinMu because a preempt reads the client outside the slot while the verb
	// inside the slot is the one that replaces it.
	clientMu sync.Mutex
}

// Run performs one verb and answers with what it did.
//
// The result is always non-nil: a refusal is an answer with a code, and a
// caller that maps codes needs something to map. Only a verb name the executor
// does not know is an error here, because that is a client asking for something
// that does not exist rather than a machine failing to sync.
func (v *VerbRunner) Run(ctx context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult {
	switch verb.Verb {
	case wire.SyncVerbEnable:
		return v.runGuarded(func() *wire.SyncResult { return v.enable(ctx, verb, token) })
	case wire.SyncVerbPush:
		return v.runGuarded(func() *wire.SyncResult { return v.push(ctx) })
	case wire.SyncVerbPull:
		return v.runGuarded(func() *wire.SyncResult { return v.pull(ctx) })
	case wire.SyncVerbRetry:
		// A retry is sent exactly when a call has stopped answering, so it
		// releases that call before it queues: the slot it is clearing is held
		// by the very call it must not wait out.
		v.preempt()
		return v.runGuarded(func() *wire.SyncResult { return v.retry() })
	case wire.SyncVerbProbe:
		return v.runGuarded(func() *wire.SyncResult { return v.probe(ctx) })
	case wire.SyncVerbDisable:
		// A disable must not queue behind a call that can run for the whole
		// data-call bound: a joined machine reads off for the whole join, and a
		// pull or a steady tick against a blackholed remote holds the slot just
		// as long. The preempt releases whatever is in flight, so the disable
		// runs in seconds rather than waiting a bound out.
		v.preempt()
		return v.runGuarded(func() *wire.SyncResult { return v.disable(ctx, verb) })
	default:
		return &wire.SyncResult{
			OK:      false,
			Code:    wire.SyncCodeInvalid,
			Message: "sync: no such verb",
		}
	}
}

// runGuarded runs fn under the daemon's serialization when one is installed.
//
// The guard is the daemon's own: a write and a trigger must not interleave,
// because both record the same markers and both hold the same local file.
// queueSync drops a trigger that arrives while another is in flight because a
// tick is cheap to lose; a verb does not take that path -- a caller asked for it
// explicitly, so it waits its turn rather than being answered "nothing
// happened".
func (v *VerbRunner) runGuarded(fn func() *wire.SyncResult) *wire.SyncResult {
	if v.Serialize == nil {
		return fn()
	}
	var out *wire.SyncResult
	v.Serialize(func() { out = fn() })
	if out == nil {
		return &wire.SyncResult{
			OK:      false,
			Code:    wire.SyncCodeInternal,
			Message: "sync: the verb did not run",
		}
	}
	return out
}

// publishJoin records the transport an enable's join drives, so a disable that
// arrives before the join finishes reaches it while the machine is still off.
func (v *VerbRunner) publishJoin(t synclog.LogTransport) {
	v.joinMu.Lock()
	v.joinClient = t
	v.joinMu.Unlock()
}

// clearJoin forgets the join transport once the enable that published it has
// settled, so a later disable does not try to stop a worker already gone.
func (v *VerbRunner) clearJoin(t synclog.LogTransport) {
	v.joinMu.Lock()
	if v.joinClient == t {
		v.joinClient = nil
	}
	v.joinMu.Unlock()
}

// joinTransport is the transport an enable's join is driving, or nil.
func (v *VerbRunner) joinTransport() synclog.LogTransport {
	v.joinMu.Lock()
	defer v.joinMu.Unlock()
	return v.joinClient
}

// joinWasStopped reports whether a disable released the join this runner is
// driving, which is what keeps the released enable from marking the machine on.
func (v *VerbRunner) joinWasStopped() bool {
	v.joinMu.Lock()
	defer v.joinMu.Unlock()
	return v.joinStopped
}

// beginJoin clears a stop a previous disable left, so a fresh enable is not
// held back by -- or kept off because of -- a stop aimed at an earlier join.
func (v *VerbRunner) beginJoin() {
	v.joinMu.Lock()
	v.joinStopped = false
	v.joinMu.Unlock()
}

// preempt releases whatever transport this runner has in flight, so a disable or
// a retry does not queue behind a call that can run for the whole data-call
// bound. It reaches two transports: the join an enable is driving, and the
// runner's steady worker, which is what a push, pull or steady tick drives. It
// marks a join stopped before cancelling it, so an enable the release lets
// finish its last step still leaves the mark off; a runner with neither in
// flight is left alone, which keeps every other verb on the daemon's ordinary
// serialization.
func (v *VerbRunner) preempt() {
	v.joinMu.Lock()
	join := v.joinClient
	if join != nil {
		v.joinStopped = true
	}
	v.joinMu.Unlock()

	steady := v.steadyTransport()
	for _, transport := range []synclog.LogTransport{join, steady} {
		if cancel := cancelTransport(transport); cancel != nil {
			cancel()
		}
	}
}

// steadyTransport is the worker the runner drives steady calls through, which is
// the transport a queued disable or retry must release before it waits for the
// slot. A runner with no worker reports nil, and cancelTransport then has
// nothing to cancel.
func (v *VerbRunner) steadyTransport() synclog.LogTransport {
	if v.Runner == nil {
		return nil
	}
	v.clientMu.Lock()
	defer v.clientMu.Unlock()
	return v.Runner.Client
}

// setClient records the worker the runner drives steady calls through, under the
// same lock a preempt reads it with. A runner with no holder is left alone.
func (v *VerbRunner) setClient(transport synclog.LogTransport) {
	if v.Runner == nil {
		return
	}
	v.clientMu.Lock()
	v.Runner.Client = transport
	v.clientMu.Unlock()
}

// push drains this machine's outbox into the log: the export half of the steady
// exchange, run alone for a caller who asked for it. It appends only rows this
// installation owns, so an imported row never travels back.
func (v *VerbRunner) push(ctx context.Context) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	res, err := synclog.NewExporter(v.Shared, transport).Export()
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	return &wire.SyncResult{OK: true, Applied: res.Appended > 0}
}

// pull reads every origin but this one and applies it: the import half of the
// steady exchange, run alone. A batch this machine cannot act on is dropped by
// the importer rather than failing the call, so one unreadable entry cannot
// stop every other origin.
func (v *VerbRunner) pull(ctx context.Context) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	res, err := synclog.NewImporter(v.Shared, transport).Import()
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	return &wire.SyncResult{OK: true, Applied: res.Applied > 0}
}

// retry clears a latched breaker and drops the worker, so the next attempt
// tries a fresh process rather than the one that stopped answering.
func (v *VerbRunner) retry() *wire.SyncResult {
	if v.Runner == nil {
		return verbRefusal(wire.SyncCodeInvalid,
			fmt.Errorf("sync: retry: no runner is installed: %w", db.ErrInvalid))
	}
	if err := v.Runner.Retry(); err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	return &wire.SyncResult{OK: true}
}

// probe is the test-connection verb: it drives the same worker a verb does and
// reads its stats, which moves no change set in this file. It is a verb rather
// than a direct call so the cockpit reaches it over the owner socket like every
// other action, and the daemon's one worker is the one that answers.
func (v *VerbRunner) probe(ctx context.Context) *wire.SyncResult {
	if err := v.Probe(ctx); err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	return &wire.SyncResult{OK: true}
}

// Probe asks the log whether it answers: it drives the same worker a verb does
// and reads its stats, which moves no change set in this file.
func (v *VerbRunner) Probe(ctx context.Context) error {
	transport, err := v.drive(ctx)
	if err != nil {
		return err
	}
	_, err = transport.Stats()
	return err
}

// drive returns the log a one-shot drives. A worker an enable left on the
// runner is reused, so a push or pull after an enable drives the same process;
// a machine whose runner holds only the placeholder opens one through the
// wiring's opener and keeps it, so the next call and the tick share it.
func (v *VerbRunner) drive(ctx context.Context) (synclog.LogTransport, error) {
	if v.Shared == nil {
		return nil, fmt.Errorf("sync: no shared database: %w", db.ErrInvalid)
	}
	if client := v.steadyTransport(); client != nil && transportReady(client) {
		return client, nil
	}
	transport, err := v.openTransport(ctx)
	if err != nil {
		return nil, err
	}
	v.setClient(transport)
	return transport, nil
}

// transportReady reports whether a transport names a remote. A supervisor built
// before an enable refuses until one did, so a caller can tell it from the
// worker an enable left behind; a transport that cannot answer is taken as
// ready, because only the placeholder knows it has nothing behind it.
func transportReady(transport synclog.LogTransport) bool {
	r, ok := transport.(interface{ Ready() bool })
	return !ok || r.Ready()
}

// enable runs one join through the sync package: the preflight decides the
// remote and the token, the runner opens the log transport, and the pipeline
// bootstraps, imports every other origin and reconcile-exports this origin's
// history before the mark goes on.
//
// The transport is opened after the preflight and only for a run that passed
// it, so a machine with no token, no remote or a failing origin gate never
// starts a worker. A run that fails closes the transport it opened, because a
// worker left behind holds the replica open against the next one; a run that
// finishes leaves it on the runner, where the daemon can drive it again.
func (v *VerbRunner) enable(ctx context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult {
	v.beginJoin()
	opened := false
	enabler := &relevosync.Enabler{
		Request: relevosync.EnableRequest{
			Local:  v.Local,
			Shared: v.Shared,
			URL:    verb.RemoteURL,
			Token:  token,
		},
		Open: func() (synclog.LogTransport, error) {
			transport, err := v.openTransport(ctx)
			if err == nil {
				// Published before the join, because a joined machine reads
				// off until the join finishes and a disable has to reach the
				// transport while the join is still in flight.
				v.publishJoin(transport)
				opened = v.Open != nil
			}
			return transport, err
		},
		Stopped: v.joinWasStopped,
		Now:     time.Now,
	}
	res, err := enabler.Enable()
	v.clearJoin(enabler.Transport)
	if err != nil {
		// Only a transport this enable opened is closed: one taken from the
		// runner belongs to whatever built it and must not be torn down here.
		if opened {
			closeTransport(enabler.Transport)
		}
		return verbRefusal(verbClassify(err), err)
	}
	v.setClient(enabler.Transport)
	return &wire.SyncResult{OK: true, Applied: true, RemoteURL: res.Remote, TokenPresent: true}
}

// openTransport returns the log an enable drives: the opener the wiring
// installed, or a client the runner already holds. A runner with neither has no
// way to reach a remote, so the enable refuses rather than opening something of
// its own.
func (v *VerbRunner) openTransport(ctx context.Context) (synclog.LogTransport, error) {
	if v.Open != nil {
		return v.Open(ctx)
	}
	if client := v.steadyTransport(); client != nil {
		return client, nil
	}
	return nil, fmt.Errorf("sync: no log transport is installed: %w", db.ErrInvalid)
}

// closeTransport releases a transport that can release itself, so a worker a
// failed enable started does not outlive it. A transport with no close holds no
// process and is left to its owner.
func closeTransport(transport synclog.LogTransport) {
	_ = closeTransportErr(transport)
}

// closeTransportErr is closeTransport with its failure, so a caller that reports
// what it did can say whether the close worked.
func closeTransportErr(transport synclog.LogTransport) error {
	if transport == nil {
		return nil
	}
	if closer, ok := transport.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// disable runs the turn-off in the order its contract fixes, against the
// daemon's own machine-local handle.
//
// The final export drives the worker the stored rows build, and only while the
// machine is on: a machine that is off has nothing to hand over, so the step is
// recorded and skipped. The worker is stopped before its replica goes, and the
// client is dropped last so a later attempt rebuilds it rather than driving a
// state that is gone.
func (v *VerbRunner) disable(ctx context.Context, verb *wire.SyncVerb) *wire.SyncResult {
	// The section is read only to refuse a machine whose stored settings will
	// not parse before anything else happens. Nothing here reads it again: the
	// turn-off's own rows are the mark, the marker and the token.
	if _, err := relevosync.ReadSettings(v.Local); err != nil {
		return verbRefusal(wire.SyncCodeInvalid, err)
	}

	transport := v.disableTransport(ctx)
	disabler := &relevosync.Disabler{
		Local:       v.Local,
		FinalExport: exportThrough(v.Shared, transport),
		Stop:        stopTransport(transport),
		ReplicaPath: v.ReplicaPath,
		Drop:        v.dropClient,
		Cancel:      cancelTransport(transport),
		Timeout:     verbTimeout(verb),
	}
	res, err := disabler.Disable(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	out := &wire.SyncResult{OK: true, Steps: res.Steps, FinalPush: res.FinalPush}
	if res.FinalPushErr != nil {
		out.Warning = res.FinalPushErr.Error()
	}
	return out
}

// disableTransport is the log the turn-off's final export drives: the worker
// the machine's stored rows build, or nothing when sync is off or no worker can
// be built, because a machine with nothing behind it has nothing to hand over.
func (v *VerbRunner) disableTransport(ctx context.Context) synclog.LogTransport {
	// A join in flight is reached first, whatever the enabled mark says: the
	// joined machine reads off for the whole join, so the gate below would
	// find no transport and leave the join's worker running.
	if t := v.joinTransport(); t != nil {
		return t
	}
	on, err := relevosync.Enabled(v.Local)
	if err != nil || !on {
		return nil
	}
	if v.Runner != nil && v.Runner.Client != nil && transportReady(v.Runner.Client) {
		return v.Runner.Client
	}
	transport, err := v.openTransport(ctx)
	if err != nil {
		return nil
	}
	return transport
}

// exportThrough adapts one log export to the turn-off's single best-effort
// call. A machine with no shared file or no transport yields nil, which the
// turn-off reads as nothing to hand over through.
func exportThrough(shared *db.DB, transport synclog.LogTransport) func(context.Context) error {
	if shared == nil || transport == nil {
		return nil
	}
	return func(context.Context) error {
		_, err := synclog.NewExporter(shared, transport).Export()
		return err
	}
}

// stopTransport stops the worker behind a transport, when there is one.
func stopTransport(transport synclog.LogTransport) func() error {
	if transport == nil {
		return nil
	}
	return func() error { return closeTransportErr(transport) }
}

// cancelTransport releases a data call a transport has in flight, when it can.
// The supervisor's Cancel kills the worker without taking its call lock, so a
// turn-off issued while a pull is hung returns at once and the released call
// settles as a deliberate stop rather than a counted death.
func cancelTransport(transport synclog.LogTransport) func() {
	canceller, ok := transport.(interface{ Cancel() })
	if !ok {
		return nil
	}
	return canceller.Cancel
}

// dropClient forgets the runner's client so the next attempt rebuilds it. The
// turn-off calls it last: the machine is off, and a cached client would outlive
// the mark that governs it.
func (v *VerbRunner) dropClient() error {
	v.setClient(nil)
	return nil
}

// verbTimeout is the bound a verb's network work runs under. Zero selects the
// package default, which is the runner's own bound: a machine that cannot reach
// its remote must be able to stop syncing without waiting on a network that is
// not answering.
func verbTimeout(verb *wire.SyncVerb) time.Duration {
	if verb.TimeoutMS > 0 {
		return time.Duration(verb.TimeoutMS) * time.Millisecond
	}
	return relevosync.DefaultTimeout
}

// refusal is a failed verb as the owner answers it: the class a caller maps and
// the message it shows. Both come from the same error the sync package raised,
// so a refusal a caller can act on keeps the wording the package gave it.
func verbRefusal(code string, err error) *wire.SyncResult {
	return &wire.SyncResult{OK: false, Code: code, Message: err.Error()}
}

// OwnerVerb is the owner.Server hook that makes a verb run with this daemon's
// handles. It is a method rather than a closure so the daemon, a test and the
// re-exec'd image all install exactly the same function.
func (v *VerbRunner) OwnerVerb(ctx context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult {
	return v.Run(ctx, verb, token)
}

// VerbRefusal turns a result into the error a caller classifies, so the client
// side of the surface maps codes without importing the sync package's sentinels.
func VerbRefusal(res *wire.SyncResult) error { return client.VerbError(res) }
