package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Enabling sync is a four-step decision, and the order is the safety of it:
// check that this database may leave the machine, decide what seeds the remote,
// write the mark, and only then open the handle that will move bytes. Every
// step before the mark is read-only, so a refusal anywhere before it leaves a
// machine exactly as it was.

// The refusals enable can return. They are sentinels so a caller branches on
// the class while the message names the fix.
var (
	// ErrAlreadyEnabled is a machine already syncing. There is no resume: a
	// machine that wants to start over turns sync off first and runs the whole
	// path again, so the seed decision can never be inherited from a run whose
	// inputs have since changed.
	ErrAlreadyEnabled = errors.New("sync: already enabled on this machine")

	// ErrNoToken is an enable with no token on either route. The message names
	// the secret and the two routes and stops there: every line out of enable
	// can end up in a log, so nothing on this path says which route was tried
	// or what either of them held.
	ErrNoToken = errors.New("sync: no " + SecretToken + ": pass --token-stdin or set " + EnvToken)

	// ErrSeedUploadRequired is a database with history meeting a remote that
	// already holds data. Last-push-wins would drop one side of that without a
	// word, so enable writes the seed copy the documented upload path takes and
	// refuses, naming that path.
	ErrSeedUploadRequired = errors.New("sync: this database has history the remote has not seen")

	// ErrNoRemote is an enable that named no remote on either route. A hand-
	// written section is the advanced route, so the refusal names the flag first
	// and the section second rather than sending a first-run reader to a config
	// document to discover where sync keeps its settings.
	ErrNoRemote = errors.New("sync: no remote is configured on this machine: pass --url or set the " + SectionSettings + " section's remote_url")

	// ErrRemoteConflict is a --url that contradicts the remote the section
	// already holds. Enabling would then move this machine's rows somewhere the
	// user did not ask for, so it refuses and names both remotes rather than
	// picking a winner.
	ErrRemoteConflict = errors.New("sync: --url names a remote this machine is not pointed at")
)

// EnvToken is the environment variable an enable reads a token from when
// --token-stdin is not given. It carries the name of the tool that mints these
// tokens, so a shell already exporting one for turso's own CLI does the right
// thing without being told.
const EnvToken = "TURSO_TOKEN"

// TokenSource names the route a token arrived by. It exists so a caller can say
// where it read from without holding the value; it is never formatted into a
// refusal, which is the whole reason it is a name and not a value.
type TokenSource string

const (
	// TokenSourceNone is an intake that resolved nothing.
	TokenSourceNone TokenSource = ""
	// TokenSourceStdin is a token read from standard input.
	TokenSourceStdin TokenSource = "stdin"
	// TokenSourceEnv is a token read from the environment.
	TokenSourceEnv TokenSource = "env"
)

// TokenIntake is the token one enable will store and the two routes it could
// have arrived by. The flag beats the environment, so a script that pipes a
// token cannot be overridden by whatever the environment happens to carry, and
// a flag that was not passed never shadows an environment that was set.
type TokenIntake struct {
	// FromStdin says the caller asked for the token on standard input.
	FromStdin bool
	// Stdin is what standard input held, already read. It is consulted only
	// when FromStdin is set, so an empty pipe is never a reason to ignore a
	// token the environment is carrying.
	Stdin []byte
	// EnvValue is what the environment held under EnvToken.
	EnvValue string
}

// Resolve returns the token to store and the route it came by. A route that
// yields nothing falls through to the next, so a flag asked for and left empty
// still finds the environment's token rather than refusing a machine that has
// one. Neither route yielding anything is ErrNoToken, and that refusal is one
// fixed line whatever was tried: the routes are named, the attempt is not.
func (i TokenIntake) Resolve() ([]byte, TokenSource, error) {
	if i.FromStdin {
		if value := bytes.TrimSpace(i.Stdin); len(value) > 0 {
			return value, TokenSourceStdin, nil
		}
	}
	if value := bytes.TrimSpace([]byte(i.EnvValue)); len(value) > 0 {
		return value, TokenSourceEnv, nil
	}
	return nil, TokenSourceNone, ErrNoToken
}

// Enabler runs one enable: the already-on read, the token, the preflight, the
// seed decision, the mark, and the open. Every input it cannot answer for
// itself is a field, so the whole path is drivable with no remote and no file.
type Enabler struct {
	// Local is the machine-local file: the token, the sync section and the
	// enabled mark all live there and nowhere else.
	Local Local
	// Preflight is the set of checks the database must pass before its rows may
	// leave this machine. Nil refuses, because a check that was skipped is a
	// check that passed.
	Preflight func() db.Preflight
	// LocalHasHistory reports whether this machine holds rows worth seeding a
	// remote with. Nil refuses.
	LocalHasHistory func() (bool, error)
	// CloudEmpty reports whether the remote holds nothing yet. It is handed the
	// settings this enable resolved and the token to reach the remote with,
	// because the enable has resolved the token and not yet stored it: storing
	// it before the seed decision would leave a credential behind on an enable
	// that goes on to refuse. The settings are handed rather than read back
	// because the caller cannot know the remote --url named until the enable has
	// resolved it. Nil refuses -- the seed matrix cannot be entered without the
	// answer, and guessing would push this machine's history over a remote that
	// already had some.
	CloudEmpty func(context.Context, Settings, []byte) (bool, error)
	// SeedCopy writes the copy the documented upload path takes. Nil means the
	// existing-history case has nothing to hand that path and refuses for it.
	SeedCopy func(path string) error
	// SeedExists reports whether the seed copy is already on disk. It is what
	// makes a second seed demand idempotent: a refusal that already wrote the copy
	// leaves it there, so an enable that runs again finds it and names the same
	// file rather than failing on a path that is not empty.
	//
	// Nil reports false, which is the driver's own behaviour: the copy is written.
	SeedExists func(string) (bool, error)
	// SeedPath is where that copy is written.
	SeedPath string
	// Backfill records the rows this machine wrote before capture was turned on
	// into the change set. It runs at the moment the file becomes a member, after
	// the seed decision and before the first push, because that is the only
	// moment at which a row written before capture can still be carried by the
	// push that follows: no pragma placement recovers a row that is already in
	// the database and in no change set.
	//
	// Nil runs no backfill, which is what a caller with no file to walk takes.
	// A failure is reported rather than swallowed -- a backfill that did not run
	// leaves rows the push cannot carry, and saying so is the difference between
	// a machine that knows it is behind and one that reports success forever.
	Backfill func(context.Context) (BackfillResult, error)
	// Open builds the handle the decision asked for. Nil refuses.
	Open func(context.Context, OpenConfig) (SyncClient, error)
	// Intake is the token this enable will store and the routes it may have
	// arrived by. The caller fills it from the flag and the environment, so
	// this path never reads either itself.
	Intake TokenIntake
	// SeedUploaded reports that the documented upload already ran, which is the
	// only thing that turns the existing-history case from a refusal into an
	// enable.
	SeedUploaded bool
	// RemoteURL is the remote --url named, empty when the flag was not passed.
	// An empty value is not a request to use whatever is stored: the enable
	// still needs a remote from somewhere, and ResolveRemote is where a machine
	// with neither is refused.
	RemoteURL string
	// Now stamps the mark. Nil means time.Now.
	Now func() time.Time
	// Timeout bounds the probe and the open. Zero means DefaultTimeout.
	Timeout time.Duration
}

// EnableResult is what one enable did, in the shape a caller reports it.
type EnableResult struct {
	// Case is the seed decision's case, including on the refusal paths, so a
	// caller can say what it found even when it could not act on it.
	Case SeedCase
	// Seed is the seed copy that was written, empty when none was.
	Seed string
	// RemoteURL is the remote this machine is pointed at now, whether --url
	// named it or the section already held it. It is on the result rather than
	// left to the caller to re-read so that what a caller reports is the remote
	// the enable actually opened, not the one the section held a moment earlier.
	RemoteURL string
	// Applied is whether the pull rebased anything.
	Applied bool
	// Backfill is what the pre-capture backfill recorded, zero when it did not
	// run.
	Backfill BackfillResult
}

// Enable runs the enable path in order. Each step refuses before the next one
// starts, and the only write before the mark is the token: everything else the
// path touches before it is a read.
func (e *Enabler) Enable(ctx context.Context) (EnableResult, error) {
	state, err := ReadState(e.Local)
	if err != nil {
		return EnableResult{}, err
	}
	if state.Enabled {
		seeding, err := ReadSeeding(e.Local)
		if err != nil {
			return EnableResult{}, err
		}
		if !seeding {
			return EnableResult{}, ErrAlreadyEnabled
		}
	}

	token, _, err := e.Intake.Resolve()
	if err != nil {
		return EnableResult{}, err
	}

	settings, err := e.resolveRemote(state.Enabled)
	if err != nil {
		return EnableResult{}, err
	}

	if e.Preflight == nil {
		return EnableResult{}, fmt.Errorf("sync: enable: no preflight to run: %w", db.ErrInvalid)
	}
	if pre := e.Preflight(); !pre.OK() {
		return EnableResult{}, pre.Err()
	}

	hasHistory, err := e.localHasHistory()
	if err != nil {
		return EnableResult{}, err
	}
	cloudEmpty, err := e.cloudEmpty(ctx, settings, token)
	if err != nil {
		return EnableResult{}, err
	}
	decision := DecideSeed(SeedInput{
		LocalHasHistory: hasHistory,
		CloudEmpty:      cloudEmpty,
		SeedUploaded:    e.SeedUploaded,
	})

	if decision.NeedsUpload {
		return EnableResult{Case: decision.Case, Seed: e.SeedPath}, e.demandSeedUpload()
	}

	if err := MarkSeeding(e.Local, e.now()); err != nil {
		return EnableResult{Case: decision.Case}, err
	}
	if err := SetToken(e.Local, token, e.now()); err != nil {
		return EnableResult{Case: decision.Case}, err
	}
	if err := MarkEnabled(e.Local, true, e.now()); err != nil {
		return EnableResult{Case: decision.Case}, err
	}

	client, err := e.openClient(ctx, decision, settings, token)
	if err != nil {
		// The window stays open: the mark is on and no round ever ran, which is
		// the state this marker exists to describe. The next enable re-runs it.
		return EnableResult{Case: decision.Case, RemoteURL: settings.RemoteURL}, err
	}

	// The open above is what turned this file into a member, so this is the first
	// moment at which a row this machine wrote before capture was on can still be
	// carried. It runs before the seed round because the seed round is the first
	// push, and a row recorded after that push waits for the next one.
	backfilled, err := e.runBackfill(ctx)
	if err != nil {
		return EnableResult{Case: decision.Case, RemoteURL: settings.RemoteURL}, err
	}
	return e.finishSeed(ctx, client, decision, settings, backfilled)
}

// finishSeed runs the seed round the decision asked for and closes the window
// around its outcome. Split out of Enable for the function-length gate; the
// order below is the contract, not an arrangement.
func (e *Enabler) finishSeed(ctx context.Context, client SyncClient, decision SeedDecision,
	settings Settings, backfilled BackfillResult) (EnableResult, error) {
	out := EnableResult{Case: decision.Case, RemoteURL: settings.RemoteURL, Backfill: backfilled}
	applied, err := e.runSeed(ctx, client, decision)
	if err != nil {
		// The mark is written but the seed is not, so this machine reads as
		// half-enabled and re-running the verb is what completes it. That is the
		// recoverable answer; answering OK over a seed that did not land is what
		// leaves a caller believing the rows are on the remote.
		return EnableResult{Case: decision.Case, RemoteURL: settings.RemoteURL}, err
	}
	out.Applied = applied
	// The round ran, so the window closes. A round that ran and failed to move
	// anything is still a round: the machine is on, its seed decision was made,
	// and the driver is the thing to report against on the next tick.
	ClearSeeding(e.Local)
	return out, nil
}

// runBackfill records the rows this machine wrote before capture was on. A
// missing backfill is not a failure: the enable's own decision is still a seed
// decision and the pass is only there to close a gap the driver cannot close
// itself.
func (e *Enabler) runBackfill(ctx context.Context) (BackfillResult, error) {
	if e.Backfill == nil {
		return BackfillResult{}, nil
	}
	return e.Backfill(ctx)
}

// resolveRemote decides which remote this enable will use, and stores a --url
// the section does not already hold.
//
// The store happens here rather than at the mark so that a refusal further down
// -- a preflight, a seed case, an unreachable remote -- leaves a machine with
// the remote it was pointed at and sync still off. The remote is not a
// credential and is not a decision the enable made, so writing it is not the
// same kind of step as the token: the mark is what makes the machine sync, and
// nothing before it does.
//
// A --url that contradicts the stored remote refuses rather than overwriting,
// but only while this machine is on. Silently repointing a machine that is
// syncing at a second remote would move its rows to a database the user did not
// name, and the refusal names both so the reader can see which is which.
//
// A machine that is off is a different case, and it is the one the seed detour
// strands people in. The documented upload creates a *new* cloud database rather
// than filling the one --url names, so the remote to sync with changes and the
// section still holds the old one. There is no sequence of verbs that reaches the
// new remote while the contradiction refusal stands: disable leaves the stored
// remote in place by design, so the next enable refuses, and the reader is left
// with a machine that cannot be pointed anywhere. An off machine has no push in
// flight and no handle open, so a --url naming a different remote is not a silent
// repoint -- it is the enable being told where to go, which is what the flag is.
func (e *Enabler) resolveRemote(enabled bool) (Settings, error) {
	settings, err := ReadSettings(e.Local)
	if err != nil {
		return Settings{}, err
	}
	flagged := strings.TrimSpace(e.RemoteURL)
	switch {
	case flagged == "" && settings.RemoteURL == "":
		return Settings{}, ErrNoRemote
	case flagged == "":
		return settings, nil
	case !validRemoteURL(flagged):
		return Settings{}, fmt.Errorf("sync: --url is not a remote URL: %w", db.ErrInvalid)
	case settings.RemoteURL == flagged:
		return settings, nil
	case settings.RemoteURL == "":
		settings.RemoteURL = flagged
		if err := PutSettings(e.Local, settings, e.now()); err != nil {
			return Settings{}, err
		}
		return settings, nil
	case enabled:
		return Settings{}, fmt.Errorf("%w: the %s section holds %q, --url named %q",
			ErrRemoteConflict, SectionSettings, settings.RemoteURL, flagged)
	default:
		// This machine is off and the flag is repointing it. One write, and not the
		// case the refusal above is about.
		settings.RemoteURL = flagged
		if err := PutSettings(e.Local, settings, e.now()); err != nil {
			return Settings{}, err
		}
		return settings, nil
	}
}

// runSeed makes the calls the decision asked for, in push-then-pull order so
// the pull has the fewest unpushed local changes to roll back and replay. It
// returns whether the pull applied anything, and the first failure it hit:
// a seed that did not land must be reported, never recorded as done.
func (e *Enabler) runSeed(ctx context.Context, client SyncClient, decision SeedDecision) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()

	if decision.Push {
		if err := client.Push(ctx); err != nil {
			return false, err
		}
	}
	if !decision.Pull {
		return false, nil
	}
	applied, err := client.Pull(ctx)
	if err != nil {
		return false, err
	}
	return applied, nil
}

// openClient builds the handle the decision asked for, with the token the
// enable resolved put into the config the opener is given. The opener therefore
// holds no credential of its own: the value travels from the resolve, through
// this one call, into the driver, and nowhere else.
//
// A missing opener is a refusal rather than a nil client: a client that was never
// built cannot be pushed or pulled through, and the mark is already written by
// then, so the machine must be told rather than left looking enabled and idle.
func (e *Enabler) openClient(ctx context.Context, decision SeedDecision, settings Settings, token []byte) (SyncClient, error) {
	if e.Open == nil {
		return nil, fmt.Errorf("sync: enable: no opener for the remote: %w", db.ErrInvalid)
	}
	return e.Open(ctx, OpenConfig{
		BootstrapIfEmpty: decision.Bootstrap,
		RemoteURL:        settings.RemoteURL,
		Namespace:        settings.Namespace,
		AuthToken:        token,
		// The enable is the only open that moves a whole database, so it is the
		// only one that bounds a single driver call. The first push after this
		// open carries this machine's entire unpushed change set and the first
		// pull can carry the remote's entire state, and a driver call asked to do
		// all of that at once is the shape whose failure this side cannot catch.
		// See DriverVersion for what that failure is and why the version is not
		// moved instead.
		PullBytesThreshold:      SeedPullBytes,
		PushOperationsThreshold: SeedPushOperations,
	})
}

// localHasHistory reports whether this machine holds rows worth seeding with.
func (e *Enabler) localHasHistory() (bool, error) {
	if e.LocalHasHistory == nil {
		return false, fmt.Errorf("sync: enable: no way to read this machine's history: %w", db.ErrInvalid)
	}
	return e.LocalHasHistory()
}

// cloudEmpty reports whether the remote holds nothing yet, with the settings
// the enable resolved and the token it resolved to reach it with.
func (e *Enabler) cloudEmpty(ctx context.Context, settings Settings, token []byte) (bool, error) {
	if e.CloudEmpty == nil {
		return false, fmt.Errorf("sync: enable: no way to read the remote: %w", db.ErrInvalid)
	}
	return e.CloudEmpty(ctx, settings, token)
}

func (e *Enabler) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Enabler) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return DefaultTimeout
}

// SectionSettings is the config section this machine's sync settings live
// under. The section is machine-local by the table it is stored in, so the name
// is the only thing the settings and the section have to agree about.
const SectionSettings = "sync"

// ReadSettings returns this machine's sync section, or a zero Settings when the
// machine has none. An absent section is not an error: a machine that never
// turned sync on has no section, and reads as off with no remote, which is
// exactly what it is.
func ReadSettings(l Local) (Settings, error) {
	body, ok, err := l.ConfigGet(SectionSettings)
	if err != nil {
		return Settings{}, fmt.Errorf("sync: read the %s section: %w", SectionSettings, err)
	}
	if !ok {
		return Settings{}, nil
	}
	return ParseSettings(body)
}

// MarkEnabled writes the machine-local mark that sync is on or off. Two rows
// carry the one fact and both are written in the same transaction: the sync
// section, which is the record a reader opens, and the sync.enabled marker,
// which is the one the idle tick and the statusline read without parsing a
// section. Writing one without the other is the drift that leaves a machine
// that reads as off still pushing, or one that pushes with nothing to say so.
func MarkEnabled(l Local, enabled bool, now time.Time) error {
	settings, err := ReadSettings(l)
	if err != nil {
		return err
	}
	settings.Enabled = enabled
	body, err := encodeSettings(settings)
	if err != nil {
		return err
	}
	marker, err := json.Marshal(enabled)
	if err != nil {
		return fmt.Errorf("sync: encode the %s marker: %w", KeyEnabled, err)
	}
	if err := l.Tx(func(t *db.Tx) error {
		if err := t.ConfigPut(SectionSettings, body, now.UTC()); err != nil {
			return fmt.Errorf("sync: write the %s section: %w", SectionSettings, err)
		}
		if err := t.KVPut(KeyEnabled, marker); err != nil {
			return fmt.Errorf("sync: write the %s marker: %w", KeyEnabled, err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// PutSettings writes the sync section on its own, without the enabled marker.
// The two rows MarkEnabled writes together carry the mark, so this is the
// advanced route: what enable uses to store a --url before the mark, because a
// remote is not a decision the enable made and a refusal after it must leave
// the machine pointed at the right remote with sync still off.
func PutSettings(l Local, settings Settings, now time.Time) error {
	body, err := encodeSettings(settings)
	if err != nil {
		return err
	}
	if err := l.Tx(func(t *db.Tx) error {
		if err := t.ConfigPut(SectionSettings, body, now.UTC()); err != nil {
			return fmt.Errorf("sync: write the %s section: %w", SectionSettings, err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// encodeSettings is the one place a Settings becomes a section body, so the two
// writers cannot disagree about what a section looks like on disk.
func encodeSettings(settings Settings) ([]byte, error) {
	body, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("sync: encode the %s section: %w", SectionSettings, err)
	}
	return body, nil
}
