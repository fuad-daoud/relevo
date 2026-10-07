package relevo

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// VerbRunner runs one sync verb against handles a process already holds.
//
// It is what the owner's OnSyncVerb hook calls and what the cockpit's sync
// actions call, so there is exactly one set of verbs' semantics in the tree: the
// owner hook does not reimplement anything the cockpit does, and neither
// reimplements the CLI's old direct-open path. It runs against the daemon's
// shared and machine-local handles -- the pair already open under the lock --
// so a client asking for a verb never opens a file and never competes for the
// lock the daemon is holding.
//
// Three of the four verbs move a change set, and this build carries no engine
// that could: they refuse with the sync package's one named error rather than
// opening anything. The turn-off still runs whole, because it only writes
// machine-local rows, and status is not a verb at all -- it is a read of the
// same local rows.
//
// A verb is serialized against the daemon's own sync triggers through the same
// guard: one push-then-pull at a time per daemon, whatever asked for it.
type VerbRunner struct {
	// Shared is the daemon's direct shared handle. The verbs that refuse leave
	// it alone, and the turn-off needs no row in it.
	Shared *db.DB
	// Local is the machine-local file beside it, where the settings, the token
	// and the mark live.
	Local relevosync.Local
	// Path is the shared file's path.
	Path string
	// Runner is the daemon's sync runner. Nothing drives it while sync is a
	// stub; it is kept so the wiring the daemon installs stays one value.
	Runner *relevosync.Runner
	// Open builds a remote handle. Nothing opens one while sync is a stub; the
	// seam is kept so the executor has exactly one place a remote is reached
	// from when there is an engine to reach.
	Open relevosync.Opener
	// Serialize runs fn under the daemon's in-flight sync guard. Nil runs it
	// inline, which is what a cockpit wants: it is not the daemon and has no
	// trigger to be serialized against.
	Serialize func(fn func())
	// ClientName is what the remote is told this client is called.
	ClientName string
}

// Run performs one verb and answers with what it did.
//
// The result is always non-nil: a refusal is an answer with a code, and a
// caller that maps codes needs something to map. Only a verb name the executor
// does not know is an error here, because that is a client asking for something
// that does not exist rather than a machine failing to sync.
func (v *VerbRunner) Run(ctx context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult {
	switch verb.Verb {
	case wire.SyncVerbEnable, wire.SyncVerbPush, wire.SyncVerbPull:
		return v.runGuarded(func() *wire.SyncResult { return v.unavailable() })
	case wire.SyncVerbDisable:
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

// unavailable is what the three verbs that would move a change set answer in a
// build with no engine behind them.
//
// They refuse rather than fall back, and the sentence says what is true rather
// than what went wrong: nothing about this machine refused them. The code is the
// invalid-verb class, which the CLI reports as a refusal rather than as a
// defect to file, because a reader who typed a correct command has nothing to
// report.
func (v *VerbRunner) unavailable() *wire.SyncResult {
	return verbRefusal(wire.SyncCodeInvalid, relevosync.ErrSyncUnavailable)
}

// disable runs the turn-off in the order its contract fixes -- mark off, forget
// the token -- against the daemon's own machine-local handle.
//
// There is no final push, because there is no handle to push through: this
// build opens no remote at all, so the attempt is skipped rather than made
// against something that does not exist. The step is still recorded, so the
// order a caller reads back is the order the contract fixes, and a machine that
// leaves does so with every local row intact and still servable.
func (v *VerbRunner) disable(ctx context.Context, verb *wire.SyncVerb) *wire.SyncResult {
	// The section is read only to refuse a machine whose stored settings will
	// not parse before anything else happens. Nothing here reads it again: the
	// turn-off's own rows are the mark, the marker and the token.
	if _, err := relevosync.ReadSettings(v.Local); err != nil {
		return verbRefusal(wire.SyncCodeInvalid, err)
	}

	disabler := &relevosync.Disabler{Local: v.Local, Timeout: verbTimeout(verb)}
	res, err := disabler.Disable(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	// The mark is off, so no later attempt may drive anything an old enable left
	// behind: drop the runner's client now rather than letting it outlive the
	// state that governs it.
	v.dropRunner()
	out := &wire.SyncResult{OK: true, Steps: res.Steps, FinalPush: res.FinalPush}
	if res.FinalPushErr != nil {
		out.Warning = res.FinalPushErr.Error()
	}
	return out
}

// dropRunner forgets the runner's client so the next attempt rebuilds it.
// Enable calls it after success (the stored token or remote may have changed
// under the old handle) and disable calls it after success (the machine is off,
// and a cached client would outlive the mark that governs it).
func (v *VerbRunner) dropRunner() {
	if v.Runner != nil {
		v.Runner.Client = nil
	}
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
