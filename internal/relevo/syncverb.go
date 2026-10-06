package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
// A verb is serialized against the daemon's own sync triggers through the same
// guard: one push-then-pull at a time per daemon, whatever asked for it.
type VerbRunner struct {
	// Shared is the daemon's direct shared handle: the preflight, the seed copy
	// and the remote's open all read or reach through it.
	Shared *db.DB
	// Local is the machine-local file beside it, where the settings, the token
	// and the mark live.
	Local relevosync.Local
	// Path is the shared file's path, which the remote open needs as a real
	// path and cannot get from a handle that did not open one.
	Path string
	// Runner is the daemon's sync runner, used for push and pull so a verb
	// records the same markers a tick does.
	Runner *relevosync.Runner
	// Open builds a remote handle. Nil selects OpenRemote, which is the daemon's
	// real route; a test sets it to the sync package's fake so an enable reaches
	// no driver and no network.
	//
	// This is the one seam the executor has, and it exists because an enable
	// otherwise calls the driver's constructor directly: the seed decision, the
	// preflight and the mark are all pure functions over the daemon's handles,
	// but the open is a network call, and a test that could not substitute it
	// would be a test that could only be run against a real remote.
	Open relevosync.Opener
	// Serialize runs fn under the daemon's in-flight sync guard. Nil runs it
	// inline, which is what a cockpit wants: it is not the daemon and has no
	// trigger to be serialized against.
	Serialize func(fn func())
	// ClientName is what the remote is told this client is called.
	ClientName string
}

// seedPath is where an existing-history enable writes the copy the documented
// upload path takes: beside the shared file, in a directory only this user
// reaches. The name is fixed so a refusal naming it names the same file every
// time.
func (v *VerbRunner) seedPath() string {
	return filepath.Join(filepath.Dir(v.Path), "relevo-seed.db")
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
	case wire.SyncVerbDisable:
		return v.runGuarded(func() *wire.SyncResult { return v.disable(ctx, verb, token) })
	case wire.SyncVerbPush, wire.SyncVerbPull:
		return v.runGuarded(func() *wire.SyncResult { return v.pushPull(ctx, verb) })
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
// The guard is the daemon's own: a push-then-pull from a verb and one from a
// seal or an idle tick must not interleave, because both record the same markers
// and both hold the same remote handle. queueSync drops a trigger that arrives
// while another is in flight because a tick is cheap to lose; a verb does not
// take that path -- a caller asked for it explicitly, so it waits its turn
// rather than being answered "nothing happened".
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

// enable runs the enable path with the daemon's own handles: the already-on
// read, the token it was handed, the preflight, the seed decision, the mark and
// the open. Every input it cannot answer for itself is filled from the daemon's
// pair, which is the whole of what "the daemon does the work" means.
//
// The stored remote is read here rather than taken from the request: the section
// is a row in the machine-local file the daemon already has open, so re-reading
// it is both cheaper and more correct than trusting a body a client sent. The
// request's --url is what a caller may add, and the Enabler refuses a --url that
// contradicts the stored one, exactly as it does for a CLI one-shot.
func (v *VerbRunner) enable(ctx context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult {
	// The section is read here only to fail a machine whose stored settings
	// will not parse before anything else happens. The Enabler reads the section
	// again itself when it resolves the remote, so this is not a second source
	// of truth and cannot disagree with it.
	if _, err := relevosync.ReadSettings(v.Local); err != nil {
		return verbRefusal(wire.SyncCodeInvalid, err)
	}

	enabler := &relevosync.Enabler{
		Local:           v.Local,
		Preflight:       func() db.Preflight { return db.EnablePreflight(v.Shared) },
		LocalHasHistory: func() (bool, error) { return db.HasSharedHistory(v.Shared) },
		CloudEmpty:      v.cloudEmpty(token),
		SeedCopy:        func(path string) error { return v.Shared.SeedCopy(path) },
		SeedExists:      v.seedExists,
		SeedPath:        v.seedPathFor(verb),
		Open:            v.opener(token),
		// The token arrived by the framed request rather than by the enable
		// reading stdin or the environment itself: the client's route already
		// resolved it and resolved it deliberately, so re-reading either here
		// would decide the credential twice.
		Intake:       relevosync.TokenIntake{FromStdin: true, Stdin: token},
		SeedUploaded: verb.SeedUploaded,
		RemoteURL:    verb.RemoteURL,
		Timeout:      verbTimeout(verb),
	}

	res, err := enabler.Enable(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	// The stored token or remote may have changed under any previously cached
	// client, so the next attempt rebuilds rather than reusing it. One extra
	// dial per enable is cheaper than a lifecycle for the old handle.
	v.dropRunner()
	return &wire.SyncResult{
		OK:           true,
		RemoteURL:    res.RemoteURL,
		SeedCase:     string(res.Case),
		Seed:         res.Seed,
		Applied:      res.Applied,
		TokenPresent: true,
	}
}

// disable runs the turn-off in the order its contract fixes -- best-effort
// bounded final push, mark off, delete the token, close -- against the daemon's
// own handles.
//
// The final push handle is opened only when there is something to push through,
// something to push with, and something in this file for a push to carry. A
// machine with no token or no remote has nothing a final push could do, so the
// turn-off does not open one and does not pay a dial to find out; a remote that
// will not open is the strongest reason of all to stop syncing, so the turn-off
// continues without a handle rather than refusing and leaving the machine pushing
// at a dead remote; and a file the driver never joined has nothing to carry, so
// asking the remote for a handle to push an empty change set over it costs a dial
// and can only fail. The last of those three is the machine whose enable died
// before its first open, and it is the one that most needs this verb to complete
// with no handle and no network at all.
func (v *VerbRunner) disable(ctx context.Context, verb *wire.SyncVerb, token []byte) *wire.SyncResult {
	settings, err := relevosync.ReadSettings(v.Local)
	if err != nil {
		return verbRefusal(wire.SyncCodeInvalid, err)
	}
	if len(token) == 0 {
		// The caller shipped none, which is the normal case: disable runs against
		// what this machine already stored rather than against a credential it
		// was handed.
		token, _ = v.storedToken()
	}

	disabler := &relevosync.Disabler{Local: v.Local, Timeout: verbTimeout(verb)}
	// A file the driver never joined has nothing a final push could carry, and
	// asking anyway costs a dial to find out -- on a machine whose enable died
	// before its first open, which is exactly the machine that most needs its
	// turn-off to complete. The membership read is this file's own schema and
	// reaches no remote, so the turn-off below runs with no handle and no
	// network at all.
	if len(token) > 0 && settings.RemoteURL != "" && v.joined() {
		handle, oerr := v.openRemote(ctx, v.openConfig(settings, token, true))
		if oerr != nil {
			fmtStderr("relevo db sync disable: the final push will be skipped: %v", oerr)
		} else {
			disabler.Client = handle
			disabler.Close = func() error { return nil }
		}
	}

	res, err := disabler.Disable(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	// The mark is off, so no later attempt may drive the old client: drop it
	// now rather than letting it outlive the state that governs it.
	v.dropRunner()
	out := &wire.SyncResult{OK: true, Steps: res.Steps, FinalPush: res.FinalPush}
	if res.FinalPushErr != nil {
		out.Warning = res.FinalPushErr.Error()
	}
	return out
}

// ensureRunner reports the runner push and pull drive, building its client
// the first time through memberOpener. A machine whose mark is off, or whose
// open fails, has no runner and gets false with nothing opened and nothing
// cached. Callers hold the verb guard across the whole verb, so two Ensures
// never overlap on one VerbRunner; the cockpit's instance is never shared.
func (v *VerbRunner) ensureRunner(ctx context.Context) (*relevosync.Runner, bool) {
	if v.Runner == nil {
		v.Runner = &relevosync.Runner{Local: v.Local}
	}
	if !v.Runner.Ensure(ctx, v.memberOpener()) {
		return nil, false
	}
	return v.Runner, true
}

// dropRunner forgets the runner's client so the next attempt rebuilds it.
// Enable calls it after success (the stored token or remote may have changed
// under the old handle) and disable calls it after success (the machine is
// off, and a cached client would outlive the mark that governs it).
func (v *VerbRunner) dropRunner() {
	if v.Runner != nil {
		v.Runner.Client = nil
	}
}

// memberOpener opens the member a push or pull drives: the stored settings
// and token, validated locally before any dial, over the role-gated member
// open. A machine with no remote or no token fails here, fast and without a
// dial, which is what keeps a refusal off the network.
func (v *VerbRunner) memberOpener() func(context.Context) (relevosync.SyncClient, error) {
	return func(ctx context.Context) (relevosync.SyncClient, error) {
		settings, err := relevosync.ReadSettings(v.Local)
		if err != nil {
			return nil, err
		}
		token, ok := v.storedToken()
		if !ok {
			return nil, errors.New("sync: no token stored on this machine")
		}
		return v.openRemote(ctx, v.openConfig(settings, token, false))
	}
}

// pushPull runs one bounded push-then-pull through the daemon's runner, so a
// verb and a tick write the same markers and read as one machine.
//
// It is SyncOnce rather than a single direction because the runner's order is
// what the daemon has always done and a verb must not be a second order: push
// first, then pull, then the stats that say how far behind this machine still
// is. A caller that asked for push learns what landed either way.
func (v *VerbRunner) pushPull(ctx context.Context, verb *wire.SyncVerb) *wire.SyncResult {
	// A machine with no remote or no token has nothing to reach and nothing to
	// authenticate with, and saying which half is absent is the whole difference
	// between a refusal the user can act on and one they have to guess at. That
	// check comes before the runner's, because a runner with no client reports
	// the same absence in words that name neither the remote nor the token.
	if err := v.checkReachable(); err != nil {
		return verbRefusal(wire.SyncCodeInvalid, err)
	}
	r, ok := v.ensureRunner(ctx)
	if !ok {
		return &wire.SyncResult{
			OK:      false,
			Code:    wire.SyncCodeInvalid,
			Message: "sync: this machine has no remote handle open",
		}
	}
	out := r.SyncOnce(ctx)
	if out.Err != nil {
		// The runner has already recorded the failure in the markers; the verb
		// reports the same class the tick would, so a caller and the statusline
		// read one answer.
		code := wire.SyncCodeRemoteUnreachable
		if out.Attention {
			code = wire.SyncCodeAuthRefused
		}
		return &wire.SyncResult{OK: false, Code: code, Message: out.Err.Error()}
	}
	return &wire.SyncResult{OK: true, Applied: out.Applied}
}

// joined reports whether the daemon's shared file carries the sync driver's
// marker tables, which is the only evidence available that an enable ever got as
// far as joining this file to a remote.
//
// It answers true when it cannot tell, so a file this cannot read is treated as
// joined and the turn-off still attempts its final push. That is the direction
// that pushes a machine's rows rather than the one that silently drops them.
func (v *VerbRunner) joined() bool {
	if v.Path == "" {
		return true
	}
	ok, err := relevosync.HasSyncMarker(v.Path)
	if err != nil {
		slog.Warn("relevo db sync: read the sync marker tables", "err", err)
		return true
	}
	return ok
}

// checkReachable refuses when there is no remote or no token to reach it with.
//
// It names which half is missing rather than reporting that something is
// missing, because a caller who is told "no remote" and a caller who is told
// "no turso.token" have two different fixes and one question to answer. It is
// the same rule the CLI applied before the verbs moved here, kept word for word
// so a message a user has already seen does not change under them.
func (v *VerbRunner) checkReachable() error {
	settings, err := relevosync.ReadSettings(v.Local)
	if err != nil {
		return err
	}
	_, hasToken := v.storedToken()
	switch {
	case settings.RemoteURL == "" && !hasToken:
		return fmt.Errorf("sync: no remote and no %s on this machine", relevosync.SecretToken)
	case settings.RemoteURL == "":
		return fmt.Errorf("sync: no remote is configured on this machine; set the sync section's remote_url")
	case !hasToken:
		return fmt.Errorf("sync: no %s on this machine", relevosync.SecretToken)
	}
	return nil
}

// storedToken is the token in the machine-local file, and the one a verb runs
// with when the caller shipped none.
//
// Only enable carries a token on the wire, because only enable has a route that
// can produce one -- the other three run against what this machine already
// stored. That is what keeps the credential off the wire for the three verbs
// that do not need it: there is one reader of the secret in the tree, and it is
// the process that already owns the file.
//
// The value is a local, passed straight into an open config. It is never
// formatted into an error, never logged and never returned in a result.
func (v *VerbRunner) storedToken() ([]byte, bool) {
	value, ok, err := relevosync.ReadToken(v.Local)
	if err != nil || !ok {
		return nil, false
	}
	return value, true
}

// seedExists reports whether the seed copy is already on disk.
//
// It is what makes a second seed demand idempotent. A first enable that refuses
// for the upload leaves its copy behind, so an enable that runs again -- because
// the machine was wedged, or because the reader re-ran the command -- finds the
// same file rather than refusing to overwrite it. A stat answers it; nothing here
// reads the copy.
func (v *VerbRunner) seedExists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// seedPathFor is the path a seed copy lands on, defaulting to the daemon's own
// beside the shared file.
func (v *VerbRunner) seedPathFor(verb *wire.SyncVerb) string {
	if verb.SeedPath != "" {
		return verb.SeedPath
	}
	return v.seedPath()
}

// openRemote is the daemon's route to a handle, going through the injected
// opener when a test installed one. Every path here goes through it rather than
// naming OpenRemote, so a fixture can substitute the fake everywhere and no verb
// can quietly reach a network a test did not mean to.
func (v *VerbRunner) openRemote(ctx context.Context, cfg relevosync.OpenConfig) (relevosync.SyncClient, error) {
	if v.Open != nil {
		return v.Open(ctx, cfg)
	}
	return relevosync.OpenRemote(ctx, cfg)
}

// openConfig is the open the settings and the handed token describe. bootstrap
// is the caller's decision: an enable's seed matrix has already made it, and a
// push or pull must never take the remote's initial state.
//
// Every open it builds names the live file as a member, never as a bare one: the
// only caller is the turn-off's final push, and by then an enable has joined this
// file to the remote. A file that is not a member refuses there rather than being
// made into one, because a turn-off has no business turning a file into a member.
func (v *VerbRunner) openConfig(settings relevosync.Settings, token []byte, bootstrap bool) relevosync.OpenConfig {
	return relevosync.OpenConfig{
		Role:             relevosync.OpenMember,
		Path:             v.Path,
		RemoteURL:        settings.RemoteURL,
		Namespace:        settings.Namespace,
		ClientName:       v.ClientName,
		AuthToken:        token,
		BootstrapIfEmpty: bootstrap,
	}
}

// opener builds the handle an enable opens. It fills in what only this machine
// knows and leaves the bootstrap decision and the token exactly as the enable
// path put them, so neither is decided twice.
//
// The token is a parameter here rather than something this type holds, and it is
// passed straight into the open config: no field on any long-lived value ever
// carries it, so there is nothing for a later log line or error to format.
func (v *VerbRunner) opener(token []byte) relevosync.Opener {
	open := v.Open
	if open == nil {
		open = relevosync.OpenRemote
	}
	return func(ctx context.Context, cfg relevosync.OpenConfig) (relevosync.SyncClient, error) {
		if cfg.RemoteURL == "" {
			return nil, errors.New("sync: enable: no remote is configured on this machine")
		}
		// This is the enable's own open and the only one allowed to create sync
		// membership: the seed matrix has already decided whether the remote's
		// state may be taken or this machine's history is the seed, so creating
		// the membership is the point rather than an accident of the open.
		cfg.Role = relevosync.OpenSeed
		cfg.Path = v.Path
		cfg.ClientName = v.ClientName
		cfg.AuthToken = token
		return open(ctx, cfg)
	}
}

// cloudEmpty reports whether the remote holds nothing yet.
//
// It answers by opening the remote against a file of its own and asking whether
// a pull brought anything back: a remote with nothing in it applies no changes,
// and a remote with something in it applies at least one. The pull is the only
// question the driver's own surface answers about a remote's contents, so it is
// the whole of the probe -- and it runs before anything is marked, so a refusal
// here leaves the machine untouched.
//
// The file is a throwaway, and that is the whole safety of this probe. A pull
// rewrites the file it was given: pointed at this machine's live database with a
// remote holding nothing, it applied the remote's emptiness over the history and
// left a skeleton where the database had been. The live path is therefore never
// named here at all, and the probe's answer is taken on a file created for the
// question and deleted after it.
//
// The token arrives as an argument rather than out of the local file because the
// enable has resolved it and deliberately not stored it yet.
func (v *VerbRunner) cloudEmpty(token []byte) func(context.Context, relevosync.Settings, []byte) (bool, error) {
	return func(ctx context.Context, st relevosync.Settings, _ []byte) (bool, error) {
		if st.RemoteURL == "" {
			return false, errors.New("sync: enable: no remote is configured on this machine; pass --url")
		}
		scratch, err := relevosync.NewThrowaway(v.Path)
		if err != nil {
			return false, err
		}
		defer scratch.Release()

		handle, err := v.openRemote(ctx, v.probeConfig(st, token, scratch.Path))
		if err != nil {
			return false, err
		}
		applied, err := handle.Pull(ctx)
		if err != nil {
			return false, err
		}
		return !applied, nil
	}
}

// probeConfig is the open the emptiness probe makes: the settings and token the
// enable resolved, against the throwaway file rather than the live one.
//
// It is a separate builder from openConfig on purpose. openConfig names the live
// path, and a probe that reused it would put the live database back in reach of
// the one call whose whole job is to ask the remote a question -- which is how
// the live file was destroyed. The two are separate so the probe cannot be
// handed the live path by a later edit that changes only one of them.
func (v *VerbRunner) probeConfig(settings relevosync.Settings, token []byte, scratch string) relevosync.OpenConfig {
	return relevosync.OpenConfig{
		// The role is named here rather than left for the opener to fill in,
		// because this builder is where the file is decided: it names a
		// throwaway, and a throwaway is the one file whose membership the driver
		// may create without asking. An open that named no role refuses at the
		// gate, and a probe is the one call on this path whose refusal would stop
		// an enable the user typed correctly.
		Role:      relevosync.OpenScratch,
		Path:      scratch,
		RemoteURL: settings.RemoteURL,
		// Namespace and client name are the live ones, so the probe asks the
		// remote the question a real open would ask rather than a question about
		// some other namespace.
		Namespace:  settings.Namespace,
		ClientName: v.ClientName,
		AuthToken:  token,
		// Bootstrap stays off for the same reason it always was: the question is
		// what a pull brings back, so nothing may be taken before it is asked.
		BootstrapIfEmpty: false,
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

// classify maps a sync package error onto the closed set of refusal codes. The
// mapping is by sentinel rather than by message, so a wording change cannot
// change which code a caller maps and no remote-chosen body can name a class.
func verbClassify(err error) string {
	switch {
	case errors.Is(err, relevosync.ErrNoToken):
		return wire.SyncCodeNoToken
	case errors.Is(err, relevosync.ErrNoRemote):
		return wire.SyncCodeNoRemote
	case errors.Is(err, relevosync.ErrAlreadyEnabled):
		return wire.SyncCodeAlreadyEnabled
	case errors.Is(err, relevosync.ErrRemoteConflict):
		return wire.SyncCodeRemoteConflict
	case errors.Is(err, relevosync.ErrSeedUploadRequired):
		return wire.SyncCodeSeedUploadRequired
	case errors.Is(err, relevosync.ErrAuthRefused):
		return wire.SyncCodeAuthRefused
	case errors.Is(err, relevosync.ErrNotSynced):
		// The open's role gate refused, so this file is not a member of a sync.
		// It is a refusal rather than an internal failure because a reader can
		// act on it: the fix is to turn sync on, which is a command. Classifying
		// it as internal would point the reader at `relevo bugreport` for
		// something they did right.
		//
		// The same sentinel answers an open that named no role at all, which is
		// a defect in this tree rather than on the reader's machine. It is still
		// not internal: an unset role reaches the reader with a path and a
		// sentence saying membership was not created, and no exit code makes that
		// sentence actionable. The refusal class is the honest one either way,
		// and the message is what says which of the two happened.
		return wire.SyncCodeInvalid
	case errors.Is(err, db.ErrPreflightRefused):
		return wire.SyncCodePreflightRefused
	case errors.Is(err, db.ErrLocked):
		return wire.SyncCodeRemoteUnreachable
	case errors.Is(err, db.ErrInvalid):
		return wire.SyncCodeInvalid
	}
	return wire.SyncCodeInternal
}

// fmtStderr writes one advisory line from the daemon. A verb's own refusal is
// returned in the result and rendered by the caller; this is the one message
// the daemon itself says, because the process it runs in is the one that knows
// the remote would not open. It is a var so a test can capture it.
var fmtStderr = func(format string, args ...any) {
	slog.Warn(fmt.Sprintf(format, args...))
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
