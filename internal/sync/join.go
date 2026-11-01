package sync

// Enable as join: a preflight deciding the remote, then a pipeline importing
// every other origin and exporting this origin's history behind a marker a
// stopped enable resumes from. The halves are separate so the decision reads
// only machine-local rows while only the pipeline touches a transport.

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

// ErrEnableStopped reports an enable a disable released before it turned the
// machine on: answering success would say sync is on when the mark is off.
var ErrEnableStopped = errors.New("sync: the enable was stopped before it turned the machine on")

// KeyJoin is the marker an enable leaves while its join runs and removes when
// the join finishes. It is machine-local by its namespace, so a second machine
// never sees one: a reader that finds it knows the mark is not to be trusted yet.
const KeyJoin = "sync.join"

// JoinProgress is what the join marker holds: when the join began, so a reader
// tells a long join from a stalled one without asking the remote.
type JoinProgress struct {
	At time.Time `json:"at"`
}

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

// ReadJoin returns the join marker; one that will not parse is a failure rather
// than absent, so a join in progress never reads as a machine at rest.
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

func ClearJoin(local Local) error {
	if err := local.KVDelete(KeyJoin); err != nil {
		return fmt.Errorf("sync: join marker: %w", err)
	}
	return nil
}

// EnableRequest is what an enable decides on before it touches a remote: the
// machine-local rows, the shared file the origin gate reads, and the caller's
// two routes for the token and the remote.
type EnableRequest struct {
	// Local is the machine-local file holding the section, the mark and the token.
	Local Local
	// Shared is the shared file the origin gate reads.
	Shared *db.DB
	// URL is the --url route, empty when the flag was not passed.
	URL string
	// Token is the token the caller resolved, empty when it resolved none; a
	// stored token stands in for it.
	Token []byte
	// R2 is the bucket credentials the caller passed; stored ones stand in field
	// by field, so pointing at another bucket does not reissue the key.
	R2 R2Intake
	// Passphrase is what the caller read in the frame's tail, empty when it read
	// none. It is verified against the remote's key_check before anything is
	// stored, so a wrong one leaves the machine as found rather than pointed at
	// a remote it cannot read.
	Passphrase []byte
	// Join says this enable came from an invite code, whose bucket credentials
	// arrive in the secrets document rather than on the command line, so the
	// preflight's unconditional no-R2 refusal does not apply to it.
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
	// R2 is the resolved bucket credentials, complete or the preflight refused it.
	R2 R2Secrets
}

// Preflight runs the enable's checks in their fixed order and returns the plan
// a passing run works from. The order is the contract: already-on refuses first
// so an enable never re-decides a running machine; the token is checked before
// the remote because no remote can supply it; the section-versus-flag conflict
// is checked before the origin gate because it is this machine's own rows while
// the gate reads the shared file and costs most. Reordering would name a fix the
// user does not owe yet, or read the shared file for a machine with no token.
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
		return EnablePlan{}, &RemoteConflictError{URL: r.URL, Stored: settings.RemoteURL}
	}
	if r.Shared == nil {
		return EnablePlan{}, fmt.Errorf("sync: enable: no shared database: %w", db.ErrInvalid)
	}
	// R2 is checked after the origin gate rather than with the token: a machine
	// whose file cannot be shared moves no body either way, and the gate is the
	// check that costs. The refusal names the flags that clear it.
	//
	// A join resolves this leg leniently, and only this leg: a code carries no
	// bucket credentials, which arrive in the secrets document the importer
	// applies before it fetches any body. So only the absence of a set is let
	// through; flags that were passed meet the same endpoint rules as a plain
	// enable's.
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

// EnableResult is what one enable moved, in the shape the caller reports.
type EnableResult struct {
	Remote string
	// Resumed is whether a join was already in progress when this run began.
	Resumed bool
	// Imported is what the other origins' entries did to this file.
	Imported synclog.ImportResult
	// Appended is how many entries the reconcile wrote, across Batches appends.
	Appended int
	// Batches is the reconcile's transport append count.
	Batches int
	// Guarded is whether the join was estimated against a bucket; an estimate
	// that is not Known means no machine had published its log size.
	Guarded  bool
	Estimate JoinEstimate
	// Stats is the worker's last reading the runner counts on from.
	Stats synclog.Stats
	// Locked is whether the reconcile was held because this machine has no key
	// while its own origin signs; the join marker then stays until unlock sends
	// this origin's history.
	Locked bool
	// Adopted is the identity an adoption took, empty for every other enable;
	// the machine is off then, by design: see Adoption.
	Adopted string
}

// Enabler drives one enable: preflight, store, transport and join.
type Enabler struct {
	// Request is the preflight's input.
	Request EnableRequest
	// Open builds the transport after the preflight has decided the remote.
	// Nil means the transport was already built and is in Transport.
	Open func() (synclog.LogTransport, error)
	// Transport is the log this enable drives; one built through Open is left
	// here so the wiring that installed Open hands the same worker onward.
	Transport synclog.LogTransport
	// Blobs builds the bucket store the join guard reads. Nil skips the guard
	// and the body repair.
	Blobs func(R2Secrets) blobstore.BlobStore
	// present is the bodies the bucket holds under this machine's origin, read
	// once before the join, or nil when there is no bucket to read.
	present map[string]bool
	// last is the worker's totals as of the last count.
	last synclog.Stats
	// Stopped reports whether a disable released the enable since it began; a
	// released join must not turn the machine on when it settles, so the mark
	// stays off even past the pipeline's last step. Nil means nothing can stop
	// the run mid-flight.
	Stopped func() bool
	// Now stamps the join and enabled marks. Nil means time.Now.
	Now func() time.Time
	// Adopt makes this enable an adoption, continuing as another installation.
	// Nil is every other enable.
	Adopt *Adoption
	// adopting is set once the claim is held, stopping the join's import from
	// capturing local files under the identity about to be left.
	adopting bool
	// resumed is whether the join marker was already set when joinImport ran.
	resumed bool
	// preflightFailed is set when Enable stopped at the preflight.
	preflightFailed bool
}

// PreflightFailed reports whether the last Enable stopped at its preflight,
// before storing or opening anything.
func (e *Enabler) PreflightFailed() bool { return e.preflightFailed }

// Enable runs the whole path: preflight, store, transport, join, then the mark.
// The mark is the last write and the only one that says the machine is on, so a
// stop before the join finishes reads off with a join marker a second enable
// resumes from.
func (e *Enabler) Enable() (EnableResult, error) {
	if err := e.checkAdoptPending(); err != nil {
		return EnableResult{}, err
	}
	plan, err := e.Request.Preflight()
	if err != nil {
		e.preflightFailed = true
		return EnableResult{}, err
	}
	now := e.now()
	if e.Adopt != nil {
		return e.enableAdopt(plan, now)
	}
	if err := e.store(plan, now); err != nil {
		return EnableResult{}, err
	}
	// A plain enable estimates against a bucket it already holds, so the guard
	// runs before the transport opens. A join holds none until the import brings
	// the secrets document, so its guard runs after, in join; estimating before
	// that would read an empty credential set as an empty bucket and wave the
	// join through on a bucket nobody can name.
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
	// A join a disable released must not turn the machine on when it settles: the
	// stop may land after the pipeline's last step, and the mark is what a reader
	// trusts, so it is withheld rather than written and cleared again.
	if e.Stopped != nil && e.Stopped() {
		return res, ErrEnableStopped
	}
	if err := MarkEnabled(e.Request.Local, true, now); err != nil {
		return res, err
	}
	return res, nil
}

// store writes the remote, and the token and R2 credentials when the caller
// brought them. Every row here is what the worker is built from, so all are
// written before anything opens a remote: a stopped enable leaves a machine
// still pointed at the right place rather than one whose worker opens nothing.
func (e *Enabler) store(plan EnablePlan, now time.Time) error {
	settings, err := ReadSettings(e.Request.Local)
	if err != nil {
		return err
	}
	settings.RemoteURL = plan.Remote
	if err := PutSettings(e.Request.Local, settings, now); err != nil {
		return err
	}
	// A join's plan carries no bucket credentials, which arrive in the secrets
	// document instead: storing an empty set would store a refusal, so an absent
	// one is left alone. A resolved set equal to what the machine already holds
	// is written nowhere either: a write would stamp and signal the secrets
	// document, and a joiner whose rows were just applied must not answer the
	// fleet with its own stamps.
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
// caller installed an opener and keeping it for the wiring to reuse.
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

// join runs the pipeline: import every other origin, reconcile this origin's
// history, publish the marks, then clear the marker. The marks go out only once
// the reconcile has succeeded, because they say how far this machine has got.
// Each step counts the worker's bytes, so a worker that dies mid-join loses at
// most one batch of counts. The first transport call comes before the marker and
// starts the worker, so a refusing remote leaves no join in progress behind.
func (e *Enabler) join(transport synclog.LogTransport, now time.Time) (res EnableResult, _ error) {
	// The result is named so the deferred reading lands in what the caller gets:
	// the runner is seeded from it, and a zero would fold the worker's whole
	// cumulative total into the month a second time.
	defer func() { res.Stats = e.last }()
	imported, err := e.joinImport(transport, now)
	res.Resumed = e.resumed
	res.Imported = imported
	if err != nil {
		return res, fmt.Errorf("sync: enable: import: %w", err)
	}
	// The document has been applied, so a join knows whether its credentials
	// arrived; the completeness check comes first because a guard without a
	// bucket would read an empty credential set as an empty bucket.
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
	// never run. The marker stays while this origin's history is owed; FinishJoin
	// sends it once there is a key.
	if res.Locked {
		return res, nil
	}
	if err := ClearJoin(e.Request.Local); err != nil {
		return res, err
	}
	return res, nil
}

// FinishJoin sends what a locked join left owed, signed now that there is a key.
// A machine with no join marker has nothing owed.
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

// JoinImportDeadline bounds the join's import loop, which runs long by design
// on a large remote. A backlog that will not finish stops the join at the import
// and lets it reconcile and enable, with the rest draining on the steady ticks
// that follow. A stop at this bound is not an error: the marks moved so far
// stand and the next run resumes from them.
const JoinImportDeadline = 30 * time.Minute

// drainImport reads the other origins page by page until a run moves no mark or
// the join's own deadline passes. The secrets pre-pass runs before the import,
// and only for a join whose R2 credentials are not stored: a peer's log
// interleaves rows and documents in sequence order, so a `$ref` row above the
// fleet's `doc:secrets` entry fetches before those credentials are applied and
// stalls on the first body with no bucket. The pre-pass applies that one
// document first and moves no mark, so the import below meets the same entries
// as redeliveries. A plain enable already holds the credentials and pays nothing.
func (e *Enabler) drainImport(transport synclog.LogTransport) (synclog.ImportResult, error) {
	// The join clears every doc's write stamps and export state, so the capture
	// runs first and the re-emit already carries the files edited before it.
	if !e.adopting {
		CaptureFiles(e.Request.Shared)
	}
	if err := e.prepassSecrets(transport); err != nil {
		return synclog.ImportResult{}, err
	}
	deadline := e.now().Add(JoinImportDeadline)
	res, err := drainImport(func() (synclog.ImportResult, error) {
		imp := synclog.NewImporter(e.Request.Shared, transport)
		if err := BindVerifier(imp, e.Request.Local); err != nil {
			return synclog.ImportResult{}, err
		}
		BindDocs(imp, e.Request.Shared)
		imp.AfterBatch = func() { e.count(transport) }
		res, ierr := imp.Import()
		e.count(transport)
		return res, ierr
	}, deadline, e.now)
	// The settle runs after the drain: what it renders is the config the drain
	// wrote, only true once committed.
	SettleFiles(e.Request.Shared)
	return res, err
}

// count folds the worker's totals into this month's row; an untakeable reading
// is skipped because the next one carries the same totals.
func (e *Enabler) count(transport synclog.LogTransport) {
	if cur, err := transport.Stats(); err == nil {
		recordStats(e.Request.Local, &e.last, cur)
	}
}

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
// chunk compares against head, so the resumption is head's and no cursor of our
// own can drift from it.
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

// recordRemoteKDF is the join's hello learning whether the remote holds a key.
func (e *Enabler) recordRemoteKDF(transport synclog.LogTransport) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	RecordRemoteKDF(ctx, e.Request.Local, transport)
}
