package sync

// Enable as join. An enable is a preflight and a pipeline. The preflight
// decides the remote and the token from the machine-local rows and the caller's
// two routes; the pipeline starts the worker, imports every other origin and
// exports this origin's history, leaving a marker while it runs so an enable
// that stops part way resumes rather than starting over.
//
// The two halves are separate because a caller may need the decision without
// the work: the preflight reads the machine-local rows and nothing else, and
// the pipeline is the only part that touches a transport.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ErrEnableStopped reports an enable a disable released before it could turn
// the machine on. It is not a failure of the remote: the machine was told to
// stop, and answering as a success would say sync is on when the mark is off.
var ErrEnableStopped = errors.New("sync: the enable was stopped before it turned the machine on")

// KeyJoin is the marker an enable leaves while its join runs and removes when
// the join finishes. It is machine-local by its namespace, so a second machine
// never sees one. Its presence is what separates an enable that stopped part
// way from a machine that is on: a reader that finds it knows the mark is not
// to be trusted yet.
const KeyJoin = "sync.join"

// JoinProgress is what the join marker holds: when the join began, so a reader
// can tell a long join from a stalled one without asking the remote.
type JoinProgress struct {
	At time.Time `json:"at"`
}

// WriteJoin records a join in progress.
func WriteJoin(local Local, at time.Time) error {
	body, err := json.Marshal(JoinProgress{At: at.UTC()})
	if err != nil {
		return fmt.Errorf("sync: join marker: %w", err)
	}
	if err := local.KVPut(KeyJoin, body); err != nil {
		return fmt.Errorf("sync: join marker: %w", err)
	}
	return nil
}

// ReadJoin returns the join marker and whether one is set. A marker that will
// not parse is a failure rather than an absent one: a join in progress must not
// read as a machine at rest.
func ReadJoin(local Local) (JoinProgress, bool, error) {
	body, ok, err := local.KVGet(KeyJoin)
	if err != nil {
		return JoinProgress{}, false, fmt.Errorf("sync: join marker: %w", err)
	}
	if !ok {
		return JoinProgress{}, false, nil
	}
	var progress JoinProgress
	if err := json.Unmarshal(body, &progress); err != nil {
		return JoinProgress{}, false, fmt.Errorf("sync: join marker: %w", err)
	}
	return progress, true, nil
}

// ClearJoin removes the join marker once the join has finished.
func ClearJoin(local Local) error {
	if err := local.KVDelete(KeyJoin); err != nil {
		return fmt.Errorf("sync: join marker: %w", err)
	}
	return nil
}

// EnableRequest is everything an enable decides on before it touches a remote:
// the machine-local rows, the shared file the origin gate reads, and the
// caller's two routes for the token and the remote.
type EnableRequest struct {
	// Local is the machine-local file the section, the mark and the token live
	// in.
	Local Local
	// Shared is the shared file the origin gate reads.
	Shared *db.DB
	// URL is the --url route, empty when the flag was not passed.
	URL string
	// Token is the token the caller resolved, empty when it resolved none. A
	// token already stored stands in for it.
	Token []byte
	// R2 is what the caller passed for the bucket credentials. A machine that
	// holds them already needs nothing here: the stored ones stand in, field by
	// field, so pointing this machine at another bucket does not reissue the key.
	R2 R2Intake
	// Passphrase is what the caller read in the frame's tail, empty when it read
	// none. It is verified against the remote's key_check before anything is
	// stored, so an enable that was handed the wrong one leaves the machine
	// exactly as it found it rather than pointed at a remote it cannot read.
	Passphrase []byte
	// Join says this enable came from an invite code. The code named the remote
	// and the token, and the bucket credentials arrive in the secrets document
	// rather than on the command line, so the preflight's unconditional no-R2
	// refusal does not apply to it.
	Join bool
	// Yes joins past the quota guard's refusal.
	Yes bool
}

// EnablePlan is what a passing preflight resolved.
type EnablePlan struct {
	// Remote is the remote this enable is pointed at.
	Remote string
	// Token is the token to store, empty when the stored one is used.
	Token []byte
	// R2 is the resolved bucket credentials, complete or the preflight refused.
	R2 R2Secrets
}

// Preflight runs the enable's checks in their fixed order and returns the plan
// a passing run works from.
//
// The order is the contract. A machine already on refuses before anything else,
// so an enable never re-decides a machine that is running. The token is checked
// before the remote because the token is the one input no remote can supply.
// The section-versus-flag conflict is checked before the origin gate because it
// is a fact about this machine's own rows, while the gate reads the shared file
// and is the most expensive check. A caller that reordered them would let a
// later check name a fix the user does not owe yet, or start reading the shared
// file for a machine that has no token at all.
func (r EnableRequest) Preflight() (EnablePlan, error) {
	on, err := Enabled(r.Local)
	if err != nil {
		return EnablePlan{}, err
	}
	if on {
		return EnablePlan{}, ErrAlreadyEnabled
	}
	if len(bytes.TrimSpace(r.Token)) == 0 {
		if _, stored, err := ReadToken(r.Local); err != nil {
			return EnablePlan{}, err
		} else if !stored {
			return EnablePlan{}, ErrNoToken
		}
	}
	settings, err := ReadSettings(r.Local)
	if err != nil {
		return EnablePlan{}, err
	}
	remote := r.URL
	if remote == "" {
		remote = settings.RemoteURL
	}
	if remote == "" {
		return EnablePlan{}, ErrNoRemote
	}
	if r.URL != "" && settings.RemoteURL != "" && r.URL != settings.RemoteURL {
		return EnablePlan{}, ErrRemoteConflict
	}
	if r.Shared == nil {
		return EnablePlan{}, fmt.Errorf("sync: enable: no shared database: %w", db.ErrInvalid)
	}
	// R2 is checked after the origin gate rather than with the token: a machine
	// whose file cannot be shared is not going to move a body either way, and the
	// gate is the check that costs. The refusal names the flags that clear it,
	// which is what a user who was expecting a working enable needs to read.
	//
	// A join resolves this leg leniently, and only this leg. A code carries no
	// bucket credentials because there is nowhere on a command line to put them
	// that they would not outlive; they arrive in the secrets document, which the
	// importer applies before it fetches any body. So a join that was given no
	// complete set is not incomplete when this runs -- it is complete in a
	// different place -- and only the absence of a set is let through. Flags
	// that were passed are held to the same endpoint rules as a plain enable's.
	stored, err := ReadR2(r.Local)
	if err != nil {
		return EnablePlan{}, err
	}
	r2, err := R2Intake{
		Endpoint: r.R2.Endpoint,
		Bucket:   r.R2.Bucket,
		KeyID:    r.R2.KeyID,
		Secret:   r.R2.Secret,
		Stored:   stored,
	}.R2Resolve()
	if err != nil && (!r.Join || !errors.Is(err, ErrNoR2)) {
		return EnablePlan{}, err
	}
	if err := db.EnablePreflight(r.Shared).Err(); err != nil {
		return EnablePlan{}, err
	}
	return EnablePlan{Remote: remote, Token: r.Token, R2: r2}, nil
}

// EnableResult is what one enable moved.
type EnableResult struct {
	// Remote is the remote the enable stored.
	Remote string
	// Resumed is whether a join was already in progress when this run began.
	Resumed bool
	// Imported is what the other origins' entries did to this file.
	Imported synclog.ImportResult
	// Appended is how many entries the reconcile wrote, across Batches appends.
	Appended int
	// Batches is how many transport appends the reconcile made.
	Batches int
	// Guarded is whether the join was estimated against a bucket, and Estimate
	// what it found. An estimate that is not Known means no machine had
	// published its log size.
	Guarded  bool
	Estimate JoinEstimate
	// Stats is the worker's last reading, which the runner counts on from.
	Stats synclog.Stats
	// Locked is whether the reconcile was held because this machine has no key
	// while its own origin signs. The join marker then stays, and unlock sends
	// this origin's history.
	Locked bool
}

// Enabler drives one enable: the preflight, the store, the transport and the
// join.
type Enabler struct {
	// Request is the preflight's input.
	Request EnableRequest
	// Open builds the transport after the preflight has decided the remote.
	// Nil means the transport was already built and is in Transport.
	Open func() (synclog.LogTransport, error)
	// Transport is the log this enable drives. A run that builds one through
	// Open leaves it here, so the wiring that installed Open can hand the same
	// worker to whatever drives sync next.
	Transport synclog.LogTransport
	// Blobs builds the in-process bucket store the join guard reads. Nil skips
	// the guard and the body repair.
	Blobs func(R2Secrets) blobstore.BlobStore
	// present is the bodies the bucket holds under this machine's origin, read
	// once before the join, or nil when there is no bucket to read.
	present map[string]bool
	// last is the worker's totals as of the last count.
	last synclog.Stats
	// Stopped reports whether the enable has been released by a disable since
	// it began. A join released that way must not turn the machine on when it
	// settles: the machine was told to stop, so the mark stays off even when
	// the pipeline had already reached its last step before the stop landed.
	// Nil means nothing can stop the run mid-flight.
	Stopped func() bool
	// Now stamps the join marker and the enabled mark. Nil means time.Now.
	Now func() time.Time
	// preflightFailed is set when Enable stopped at the preflight.
	preflightFailed bool
}

// PreflightFailed reports whether the last Enable stopped at its preflight,
// before it stored or opened anything.
func (e *Enabler) PreflightFailed() bool { return e.preflightFailed }

// Enable runs the whole path: preflight, store, transport, join, then the mark.
//
// The mark is the last write and the only one that says the machine is on, so
// an enable that stops before the join finishes leaves a machine that reads off
// with a join marker behind it. That is exactly the state a second enable
// resumes from.
func (e *Enabler) Enable() (EnableResult, error) {
	plan, err := e.Request.Preflight()
	if err != nil {
		e.preflightFailed = true
		return EnableResult{}, err
	}
	now := e.now()
	if err := e.store(plan, now); err != nil {
		return EnableResult{}, err
	}
	// A plain enable estimates against a bucket it already holds, so the guard
	// runs before the transport opens and a machine that would not fit refuses
	// without having imported anything. A join holds none: its credentials
	// arrive in the secrets document during the import, so its guard runs after
	// it, in join, against what the document brought. Estimating before that
	// would read an empty credential set as an empty bucket and wave the join
	// through on a bucket nobody can name.
	var est JoinEstimate
	var guarded bool
	if !e.Request.Join {
		est, guarded, err = e.guard(plan, now)
		if err != nil {
			return EnableResult{Remote: plan.Remote, Guarded: guarded, Estimate: est}, err
		}
		if e.present, err = e.ownBodies(plan); err != nil {
			return EnableResult{Remote: plan.Remote, Guarded: guarded, Estimate: est}, err
		}
	}
	transport, err := e.transport()
	if err != nil {
		return EnableResult{}, err
	}
	res, err := e.join(transport, now)
	res.Remote = plan.Remote
	res.Guarded, res.Estimate = guarded, est
	if err != nil {
		return res, err
	}
	// A join a disable released must not turn the machine on when it settles:
	// the stop may land after the pipeline's last step, and the mark is what a
	// reader trusts, so it is withheld rather than written and cleared again.
	if e.Stopped != nil && e.Stopped() {
		return res, ErrEnableStopped
	}
	if err := MarkEnabled(e.Request.Local, true, now); err != nil {
		return res, err
	}
	return res, nil
}

// store writes the remote, and the token and R2 credentials when the caller
// brought them. Every row written here is what the worker is built from, so they
// are all written before anything opens a remote: an enable that stopped part
// way leaves a machine still pointed at the right place with whatever it managed
// to store, rather than one whose worker would open against nothing.
func (e *Enabler) store(plan EnablePlan, now time.Time) error {
	settings, err := ReadSettings(e.Request.Local)
	if err != nil {
		return err
	}
	settings.RemoteURL = plan.Remote
	if err := PutSettings(e.Request.Local, settings, now); err != nil {
		return err
	}
	// A join's plan carries no bucket credentials, because a code has nowhere to
	// put them and they arrive in the secrets document instead. Storing an empty
	// set would be storing a refusal, so an absent one is left alone; a plain
	// enable's plan is complete by the time it gets here, so this is the join
	// path and nothing else.
	//
	// A resolved set equal to what the machine already holds carries nothing a
	// person typed into this enable, so nothing is written here: a write would
	// stamp and signal the secrets document, and a joiner whose credential rows
	// were applied a moment ago must not answer the fleet with its own stamps.
	if plan.R2.Complete() {
		stored, err := ReadR2(e.Request.Local)
		if err != nil {
			return err
		}
		if stored != plan.R2 {
			if err := SetR2(e.Request.Local, plan.R2, now); err != nil {
				return err
			}
		}
	}
	if len(bytes.TrimSpace(plan.Token)) == 0 {
		return nil
	}
	return SetToken(e.Request.Local, plan.Token, now)
}

// transport returns the log this enable drives, building it once when the
// caller installed an opener and keeping it so the wiring can reuse the worker.
func (e *Enabler) transport() (synclog.LogTransport, error) {
	if e.Transport != nil {
		return e.Transport, nil
	}
	if e.Open == nil {
		return nil, fmt.Errorf("sync: enable: no log transport: %w", db.ErrInvalid)
	}
	transport, err := e.Open()
	if err != nil {
		return nil, err
	}
	e.Transport = transport
	return transport, nil
}

// join runs the pipeline: open the log, import every other origin, reconcile
// this origin's history in bounded chunks, publish the marks, then clear the
// marker.
//
// The marks are published only once the reconcile has succeeded, because they
// say how far this machine has got and a join that stopped earlier has not got
// there. Each step counts the worker's bytes as it goes, so a worker that dies
// mid-join loses at most one batch of counts.
//
// The first transport call comes before the marker and is what starts the
// worker and bootstraps the replica. A remote that refuses the log therefore
// leaves no join in progress behind, and its refusal is the first thing the
// caller reads.
func (e *Enabler) join(transport synclog.LogTransport, now time.Time) (EnableResult, error) {
	var res EnableResult
	defer func() { res.Stats = e.last }()
	opened, err := transport.Stats()
	if err != nil {
		return res, fmt.Errorf("sync: enable: open the log: %w", err)
	}
	recordStats(e.Request.Local, &e.last, opened)
	e.recordRemoteKDF(transport)
	_, resumed, err := ReadJoin(e.Request.Local)
	if err != nil {
		return res, err
	}
	res.Resumed = resumed
	if !resumed {
		if err := WriteJoin(e.Request.Local, now); err != nil {
			return res, err
		}
		// The new peer has seen none of the doc keys this machine exported to
		// another log, so the next export sends every one again. Local write
		// stamps go too, before the import, so shared keys apply over the seed.
		//
		// The secrets document's bookkeeping is kept over the clear. The
		// pre-pass that filled it read it off the fleet's own entries on the
		// log this enable is joining, and a machine whose held values are the
		// fleet's answers the fleet with nothing: an apply of a peer's entry is
		// this machine taking one, not this machine writing one.
		kept, err := readDocSecretsBookkeeping(e.Request.Local)
		if err != nil {
			return res, err
		}
		if err := e.Request.Local.ClearDocExportState(); err != nil {
			return res, err
		}
		if err := restoreDocSecretsBookkeeping(e.Request.Local, kept); err != nil {
			return res, err
		}
	}
	imported, err := e.drainImport(transport)
	res.Imported = imported
	if err != nil {
		return res, fmt.Errorf("sync: enable: import: %w", err)
	}
	// The document has been applied, so a join knows whether its credentials
	// arrived. The completeness check comes first because a guard run without a
	// bucket would read an empty credential set as an empty bucket, and the
	// join would be waved through on a bucket nobody can name.
	if err := e.checkJoinR2(); err != nil {
		return res, err
	}
	if e.Request.Join {
		est, guarded, err := e.guardJoined()
		res.Guarded, res.Estimate = guarded, est
		if err != nil {
			return res, err
		}
		if e.present, err = e.ownBodiesJoined(); err != nil {
			return res, err
		}
	}
	appended, batches, err := e.reconcile(transport)
	res.Appended, res.Batches = appended, batches
	res.Locked = errors.Is(err, synclog.ErrLockedSignedOrigin)
	if err != nil && !res.Locked {
		return res, err
	}
	if err := e.publishMarks(transport); err != nil {
		return res, err
	}
	// A locked machine still turns on, or unlock -- which needs sync on -- could
	// never run. The marker stays because this origin's history is still owed:
	// it keeps the outbox, and FinishJoin sends the history once there is a key.
	if res.Locked {
		return res, nil
	}
	if err := ClearJoin(e.Request.Local); err != nil {
		return res, err
	}
	return res, nil
}

// FinishJoin sends what a locked join left owed: it reconciles this origin's
// history, signed now that there is a key, publishes the marks and clears the
// join marker. A machine with no join marker has nothing owed.
func (e *Enabler) FinishJoin(transport synclog.LogTransport) (int, error) {
	if _, joining, err := ReadJoin(e.Request.Local); err != nil || !joining {
		return 0, err
	}
	appended, _, err := e.reconcile(transport)
	if err != nil {
		return appended, err
	}
	if err := e.publishMarks(transport); err != nil {
		return appended, err
	}
	return appended, ClearJoin(e.Request.Local)
}

// JoinImportDeadline bounds the join's import loop. The join reads every other
// origin's history, which on a large remote is many pages, so the loop runs
// long by design; the bound exists so a backlog that will not finish stops the
// join at the import and lets it reconcile and enable, with the remaining
// origins draining on the steady ticks that follow. A stop at this bound is not
// an error: the marks moved so far stand and the next run resumes from them.
const JoinImportDeadline = 30 * time.Minute

// drainImport reads the other origins page by page until a run moves no mark or
// the join's own deadline passes.
//
// The secrets pre-pass runs before the import, and only for a join whose R2
// credentials are not stored. That is the one document a joiner cannot reach by
// ordinary ordering: a peer's log interleaves its rows and its documents in
// sequence order, so a `$ref` row written above the fleet's `doc:secrets` entry
// is fetched before those credentials have been applied, and the origin stalls
// on the first body it has no bucket to fetch. The pre-pass applies that one
// document first and moves no mark, so the import below reaches the same entries
// as redeliveries. A plain enable already holds the credentials, so it has
// nothing to unblock and pays nothing for the walk.
func (e *Enabler) drainImport(transport synclog.LogTransport) (synclog.ImportResult, error) {
	// The join clears every doc's write stamps and export state, so the files
	// a person edited before it would otherwise never be exported. The capture
	// runs first so the re-emit already carries them.
	CaptureFiles(e.Request.Shared)
	if err := e.prepassSecrets(transport); err != nil {
		return synclog.ImportResult{}, err
	}
	deadline := e.now().Add(JoinImportDeadline)
	res, err := drainImport(func() (synclog.ImportResult, error) {
		key, err := SigningKey(e.Request.Local)
		if err != nil {
			return synclog.ImportResult{}, err
		}
		imp := synclog.NewImporter(e.Request.Shared, transport)
		imp.SetKey(key)
		BindDocs(imp, e.Request.Shared)
		imp.AfterBatch = func() { e.count(transport) }
		res, ierr := imp.Import()
		e.count(transport)
		return res, ierr
	}, deadline, e.now)
	// The settle runs after the drain, not inside it: what it renders is the
	// config the drain wrote, which is only true once it has committed.
	SettleFiles(e.Request.Shared)
	return res, err
}

// count folds the worker's totals into this month's row. A reading that cannot
// be taken is skipped: the next one carries the same totals.
func (e *Enabler) count(transport synclog.LogTransport) {
	if cur, err := transport.Stats(); err == nil {
		recordStats(e.Request.Local, &e.last, cur)
	}
}

// publishMarks tells the log how far this machine has applied of every origin.
func (e *Enabler) publishMarks(transport synclog.LogTransport) error {
	marks, err := e.Request.Shared.ImportMarks()
	if err != nil {
		return fmt.Errorf("sync: enable: read the import marks: %w", err)
	}
	if err := transport.PublishMarks(marks, e.now().UTC()); err != nil {
		return fmt.Errorf("sync: enable: publish the marks: %w", err)
	}
	e.count(transport)
	return nil
}

// reconcile exports this origin's history one bounded chunk at a time. Each
// chunk compares against head, so a run that stops part way leaves the
// converged rows out of the next one: the resumption is head's, and no cursor
// of our own can drift from it.
func (e *Enabler) reconcile(transport synclog.LogTransport) (int, int, error) {
	key, err := SigningKey(e.Request.Local)
	if err != nil {
		return 0, 0, fmt.Errorf("sync: enable: %w", err)
	}
	reconciler := synclog.NewReconciler(e.Request.Shared, transport)
	reconciler.SetKey(key)
	BindDocs(reconciler, e.Request.Shared)
	if e.present != nil {
		reconciler.RepairBodies(e.present)
	}
	var appended, batches int
	for {
		one, err := reconciler.ReconcileBatch()
		if err != nil {
			return appended, batches, fmt.Errorf("sync: enable: reconcile: %w", err)
		}
		if !one.More() {
			return appended, batches, nil
		}
		appended += one.Upserts + one.Deletes
		batches += one.Batches
		e.count(transport)
	}
}

func (e *Enabler) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// recordRemoteKDF is the join's hello: it learns whether the remote holds a key.
func (e *Enabler) recordRemoteKDF(transport synclog.LogTransport) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	RecordRemoteKDF(ctx, e.Request.Local, transport)
}
