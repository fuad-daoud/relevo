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
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
}

// EnablePlan is what a passing preflight resolved.
type EnablePlan struct {
	// Remote is the remote this enable is pointed at.
	Remote string
	// Token is the token to store, empty when the stored one is used.
	Token []byte
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
	if err := db.EnablePreflight(r.Shared).Err(); err != nil {
		return EnablePlan{}, err
	}
	return EnablePlan{Remote: remote, Token: r.Token}, nil
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
	// Stopped reports whether the enable has been released by a disable since
	// it began. A join released that way must not turn the machine on when it
	// settles: the machine was told to stop, so the mark stays off even when
	// the pipeline had already reached its last step before the stop landed.
	// Nil means nothing can stop the run mid-flight.
	Stopped func() bool
	// Now stamps the join marker and the enabled mark. Nil means time.Now.
	Now func() time.Time
}

// Enable runs the whole path: preflight, store, transport, join, then the mark.
//
// The mark is the last write and the only one that says the machine is on, so
// an enable that stops before the join finishes leaves a machine that reads off
// with a join marker behind it. That is exactly the state a second enable
// resumes from.
func (e *Enabler) Enable() (EnableResult, error) {
	plan, err := e.Request.Preflight()
	if err != nil {
		return EnableResult{}, err
	}
	now := e.now()
	if err := e.store(plan, now); err != nil {
		return EnableResult{}, err
	}
	transport, err := e.transport()
	if err != nil {
		return EnableResult{}, err
	}
	res, err := e.join(transport, now)
	res.Remote = plan.Remote
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

// store writes the remote and, when the caller brought one, the token. Both
// rows are what the worker is built from, so they are written before anything
// opens a remote.
func (e *Enabler) store(plan EnablePlan, now time.Time) error {
	settings, err := ReadSettings(e.Request.Local)
	if err != nil {
		return err
	}
	settings.RemoteURL = plan.Remote
	if err := PutSettings(e.Request.Local, settings, now); err != nil {
		return err
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
// this origin's history in bounded chunks, then clear the marker.
//
// The first transport call comes before the marker and is what starts the
// worker and bootstraps the replica. A remote that refuses the log therefore
// leaves no join in progress behind, and its refusal is the first thing the
// caller reads.
func (e *Enabler) join(transport synclog.LogTransport, now time.Time) (EnableResult, error) {
	var res EnableResult
	if _, err := transport.Stats(); err != nil {
		return res, fmt.Errorf("sync: enable: open the log: %w", err)
	}
	_, resumed, err := ReadJoin(e.Request.Local)
	if err != nil {
		return res, err
	}
	res.Resumed = resumed
	if !resumed {
		if err := WriteJoin(e.Request.Local, now); err != nil {
			return res, err
		}
	}
	imported, err := e.drainImport(transport)
	res.Imported = imported
	if err != nil {
		return res, fmt.Errorf("sync: enable: import: %w", err)
	}
	appended, batches, err := e.reconcile(transport)
	res.Appended, res.Batches = appended, batches
	if err != nil {
		return res, err
	}
	if err := ClearJoin(e.Request.Local); err != nil {
		return res, err
	}
	return res, nil
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
func (e *Enabler) drainImport(transport synclog.LogTransport) (synclog.ImportResult, error) {
	deadline := e.now().Add(JoinImportDeadline)
	return drainImport(func() (synclog.ImportResult, error) {
		return synclog.NewImporter(e.Request.Shared, transport).Import()
	}, deadline, e.now)
}

// reconcile exports this origin's history one bounded chunk at a time. Each
// chunk compares against head, so a run that stops part way leaves the
// converged rows out of the next one: the resumption is head's, and no cursor
// of our own can drift from it.
func (e *Enabler) reconcile(transport synclog.LogTransport) (int, int, error) {
	reconciler := synclog.NewReconciler(e.Request.Shared, transport)
	var appended, batches int
	for {
		one, err := reconciler.ReconcileBatch()
		if err != nil {
			return appended, batches, fmt.Errorf("sync: enable: reconcile: %w", err)
		}
		if one.Batches == 0 {
			return appended, batches, nil
		}
		appended += one.Upserts + one.Deletes
		batches += one.Batches
	}
}

func (e *Enabler) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
