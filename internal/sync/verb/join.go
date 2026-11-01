package verb

// The passphrase half of an enable: stage what the worker is built from, check
// the passphrase against the remote's key_check, and only then let the enable
// import anything.
//
// The order is the whole point: a wrong passphrase is refused before the
// machine holds a remote or token it cannot use, because a machine pointed at
// somebody else's remote acts on its next attempt in a way the operator never
// asked for. The worker is built from the machine-local rows, so the remote and
// the token must be staged before one opens, and every staged row is put back
// byte-for-byte when the check fails.
//
// A --join needs this and a plain enable may need it: a joiner cannot unlock
// without a key, while a plain enable on an unlocked machine has nothing to
// verify.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// errNoPassphrase is a join that arrived without one: a joiner that cannot
// unlock would import every entry and hold none, reading as joined while
// sharing nothing. Refusing here costs one command; discovering it later costs
// a sync that looks fine and is not. It is one fixed line, because the
// passphrase never appears in a message.
var errNoPassphrase = errors.New("sync: enable --join: no passphrase: pass --passphrase-stdin or set RELEVO_SYNC_PASSPHRASE")

// verified is an enable that has staged its rows, opened a worker against them
// and stored the key the passphrase derived. The snapshot is kept because the
// enable can still fail after this point, and a failure there has to leave the
// machine no worse than a join that was never attempted.
type verified struct {
	// snapshot puts the staged rows back.
	snapshot relevosync.JoinSnapshot
	// transport is the worker the passphrase was checked against.
	transport synclog.LogTransport
	// opened says whether this enable built the transport, because only one it
	// opened may be closed here.
	opened bool
	// bucketless says the worker that answered was started with no bucket
	// stored, which is what the enable answers for before the import runs: the
	// import fetches `$ref` bodies, so it cannot drive a worker with none.
	bucketless bool
	// unstage puts the staged rows back. It is safe to call more than once: the
	// snapshot is a set of bytes, not a consumption.
	unstage func()
}

// stageAndVerify stages what the worker is built from, opens it, and stores the
// key the passphrase derives.
//
// required decides whether a missing passphrase is a refusal: a --join needs
// one, while a plain enable handed none has nothing to verify and goes on as it
// always did.
//
// A plain enable stages its bucket credentials too, because its worker opens
// from the stored set and on a fresh machine the only set is the one this
// enable carries; a join's arrive in the fleet's secrets document instead.
func (v *Runner) stageAndVerify(ctx context.Context, remote string, token, passphrase []byte, required bool, r2 relevosync.R2Intake) (*verified, error) {
	if required && len(bytes.TrimSpace(passphrase)) == 0 {
		return nil, errNoPassphrase
	}
	if !required && len(bytes.TrimSpace(passphrase)) == 0 {
		return nil, nil
	}
	on, err := relevosync.Enabled(v.Local)
	if err != nil {
		return nil, err
	}
	if on {
		return nil, relevosync.ErrAlreadyEnabled
	}
	snapshot, err := relevosync.SnapshotJoin(v.Local)
	if err != nil {
		return nil, err
	}
	out := &verified{snapshot: snapshot, unstage: func() { v.unstageJoin(snapshot) }}
	if err := v.stageRows(remote, token); err != nil {
		v.unstageJoin(snapshot)
		return nil, err
	}
	if !required {
		if err := v.stageR2(r2); err != nil {
			v.unstageJoin(snapshot)
			return nil, err
		}
	}
	transport, opened, bucketless, err := v.openJoinTransport(ctx, required)
	if err != nil {
		v.unstageJoin(snapshot)
		return nil, err
	}
	out.transport, out.opened, out.bucketless = transport, opened, bucketless
	ring, err := relevosync.VerifyJoinKey(ctx, v.Local, transport, passphrase)
	if err != nil {
		v.unstageJoin(snapshot)
		if opened {
			closeTransport(transport)
		}
		return nil, err
	}
	if err := relevosync.SetKeys(v.Local, ring, time.Now()); err != nil {
		v.unstageJoin(snapshot)
		closeTransport(transport)
		return nil, err
	}
	// The fleet's secrets document is applied before the enable goes on, over
	// the worker that exists. A joiner's R2 credentials are in that document and
	// a peer's `$ref` row can sit above the entries that carry them, so waiting
	// for the import's own walk is what stalls the origin on its first body. The
	// walk pulls entries only -- it fetches no body -- so it runs on the worker
	// it was handed, bucket stored or not. A plain enable's machine already
	// holds its credentials, so only a join pays for it.
	if required {
		if err := relevosync.PrepassSecrets(v.Shared, v.Local, transport); err != nil {
			v.unstageJoin(snapshot)
			// Clear, then close -- and close only the worker this enable opened
			// (out.opened), because closeTransport reaches the runner's steady
			// holder too when openTransport answered with it: this path closes
			// a worker it may not have built, on a publication a later disable
			// would then find already gone.
			v.clearJoin(transport)
			if opened {
				closeTransport(transport)
			}
			return nil, err
		}
	}
	return out, nil
}

// stageRows writes the remote and the token into the machine-local rows. It is
// the two rows the worker is opened from and nothing else: the bucket
// credentials a plain enable takes are written by the enable itself later.
func (v *Runner) stageRows(remote string, token []byte) error {
	now := time.Now()
	settings, err := relevosync.ReadSettings(v.Local)
	if err != nil {
		return err
	}
	settings.RemoteURL = remote
	if err := relevosync.PutSettings(v.Local, settings, now); err != nil {
		return err
	}
	return relevosync.SetToken(v.Local, token, now)
}

// stageR2 stores the bucket credentials a plain enable resolves from its flags
// over the stored set. An incomplete set is refused here, as the enable itself
// would refuse it, before a worker is opened that could not be built.
func (v *Runner) stageR2(r2 relevosync.R2Intake) error {
	stored, err := relevosync.ReadR2(v.Local)
	if err != nil {
		return err
	}
	r2.Stored = stored
	resolved, err := r2.R2Resolve()
	if err != nil {
		return err
	}
	return relevosync.SetR2(v.Local, resolved, time.Now())
}

// unstageJoin puts the staged rows back.
//
// It runs on every path that opened a worker to check the passphrase and got no
// key back, the only way a machine ends up worse off than it was. When the
// restore itself fails, the log says the machine may not be entirely as it was,
// which an operator has to hear.
func (v *Runner) unstageJoin(snapshot relevosync.JoinSnapshot) {
	if err := snapshot.Restore(v.Local, time.Now()); err != nil {
		slog.Warn("sync: enable: the staged rows could not be restored", "err", err)
	}
}

// openJoinTransport opens the worker the passphrase is checked against, wrapped
// in the stop a join drives so a disable arriving mid-join still reaches it.
//
// joining decides the opener: a joiner's stored rows hold no bucket yet -- the
// credentials arrive in the fleet's secrets document, which only the worker is
// opened to read -- so the wiring can install an opener that starts the worker
// with none instead of refusing. Every other enable opens through Open.
// bucketless is the opener's answer about the worker it built, which the rest
// of the enable must answer for.
func (v *Runner) openJoinTransport(ctx context.Context, joining bool) (synclog.LogTransport, bool, bool, error) {
	var transport synclog.LogTransport
	var err error
	var bucketless bool
	if joining && v.OpenNoBucket != nil {
		transport, bucketless, err = v.OpenNoBucket(ctx)
	} else {
		transport, err = v.openTransport(ctx)
	}
	if err != nil {
		return nil, false, false, err
	}
	stop := relevosync.NewStopTransport(transport)
	// Published before the check, because the machine reads off from here until
	// the enable settles and a disable has to be able to reach the worker.
	v.publishJoin(stop)
	return stop, (joining && v.OpenNoBucket != nil) || v.Open != nil, bucketless, nil
}

// enableJoin runs a join that arrived with a code.
//
// The passphrase is required here, and the enable it hands off to resolves the
// bucket credentials leniently: a code has nowhere to carry them and they arrive
// in the secrets document, but bucket flags the joiner passed are honored.
func (v *Runner) enableJoin(ctx context.Context, verb *wire.SyncVerb, token, passphrase []byte, r2 relevosync.R2Intake) *wire.SyncResult {
	v.beginJoin()
	done, err := v.stageAndVerify(ctx, verb.RemoteURL, token, passphrase, true, r2)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	return v.enablePipeline(ctx, verb, token, r2, true, done)
}

// refusedForGood says an enable failed on a rule that re-running cannot clear
// on its own, as opposed to a transport or import failure the next enable
// resumes from. Only the first kind puts the staged rows back.
func refusedForGood(err error, enabler *relevosync.Enabler) bool {
	return enabler.PreflightFailed() ||
		errors.Is(err, relevosync.ErrNoR2AfterJoin) ||
		errors.Is(err, relevosync.ErrJoinOverQuota)
}

// enablePipeline runs the ordinary enable, which is what imports the fleet and
// turns the machine on.
//
// joining relaxes the preflight's bucket check: a join's credentials may arrive
// in the secrets document, so only flags actually passed are stored.
func (v *Runner) enablePipeline(ctx context.Context, verb *wire.SyncVerb, token []byte, r2 relevosync.R2Intake, joining bool, staged *verified) *wire.SyncResult {
	v.beginJoin()
	var opened synclog.LogTransport
	if staged != nil {
		opened = staged.transport
	}
	if joining && staged != nil {
		if err := v.settleImportBucket(r2, staged); err != nil {
			staged.unstage()
			return Refusal(Classify(err), err)
		}
		opened = staged.transport
	}
	enabler := &relevosync.Enabler{
		Request: relevosync.EnableRequest{
			Local:  v.Local,
			Shared: v.Shared,
			URL:    verb.RemoteURL,
			Token:  token,
			R2:     r2,
			Join:   joining,
			Yes:    verb.Yes,
		},
		Blobs:   v.Blobs,
		Stopped: v.joinWasStopped,
		Now:     time.Now,
		Adopt:   v.adoption(verb),
	}
	// A transport this enable already opened is handed over rather than built
	// again: the passphrase was verified against it, and a second worker pointed
	// at the same remote would be a second thing to tear down on a failure.
	if opened != nil {
		enabler.Transport = opened
	} else {
		enabler.Open = func() (synclog.LogTransport, error) {
			transport, err := v.openTransport(ctx)
			if err != nil {
				return transport, err
			}
			stop := relevosync.NewStopTransport(transport)
			v.publishJoin(stop)
			return stop, nil
		}
	}
	res, err := enabler.Enable()
	v.clearJoin(enabler.Transport)
	if err != nil {
		if enabler.Transport != nil {
			closeTransport(enabler.Transport)
		}
		if staged != nil && refusedForGood(err, enabler) {
			staged.unstage()
		}
		return Refusal(Classify(err), err)
	}
	if res.Adopted != "" {
		// The worker was opened under the identity the machine just left; the
		// daemon builds its own, as the adopted one, when it restarts.
		closeTransport(enabler.Transport)
		return &wire.SyncResult{OK: true, RemoteURL: res.Remote, TokenPresent: true, Warning: adoptedWarning(res.Adopted)}
	}
	client := enabler.Transport
	if stop, ok := client.(*relevosync.StopTransport); ok {
		client = stop.Inner()
	}
	v.setClient(client)
	if v.Runner != nil {
		v.Runner.SeedStats(res.Stats)
	}
	return &wire.SyncResult{OK: true, Applied: true, RemoteURL: res.Remote, TokenPresent: true, Warning: enableWarning(res)}
}

// settleImportBucket decides what a join's import runs on.
//
// A verify worker opened with no bucket cannot carry the import, which fetches
// `$ref` bodies: the stored set after the secrets pre-pass is merged with the
// flags the joiner passed -- a flag beats the stored value it would fill -- and
// neither side completing it is the fleet granting none, refused before the
// enable opens a worker no bucket could carry. A merged set that is complete
// ends the question: a verify worker started with none is closed and the enable
// builds a fresh one through Open, now that the rows name a bucket. One opened
// with a bucket keeps driving.
//
// The refusal error is returned; the caller puts the staged rows back.
//
// Ordering matters on the way out: the transport is cleared from the join's
// publication before it is closed, so a disable arriving mid-join reaches a
// worker that is still there and never drives one already gone.
func (v *Runner) settleImportBucket(r2 relevosync.R2Intake, staged *verified) error {
	stored, err := relevosync.ReadR2(v.Local)
	if err != nil {
		releaseStagedWorker(v, staged)
		return err
	}
	resolve := relevosync.R2Intake{
		Endpoint: r2.Endpoint,
		Bucket:   r2.Bucket,
		KeyID:    r2.KeyID,
		Secret:   r2.Secret,
		Stored:   stored,
	}
	if _, resolveErr := resolve.R2Resolve(); resolveErr != nil {
		releaseStagedWorker(v, staged)
		if errors.Is(resolveErr, relevosync.ErrNoR2) {
			return relevosync.ErrNoR2AfterJoin
		}
		return resolveErr
	}
	if staged.bucketless {
		releaseStagedWorker(v, staged)
	}
	return nil
}

func releaseStagedWorker(v *Runner, staged *verified) {
	v.clearJoin(staged.transport)
	closeTransport(staged.transport)
	staged.transport = nil
}
