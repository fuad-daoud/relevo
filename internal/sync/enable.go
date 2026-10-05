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

// SeedCase names which of the three situations an enable found. The three want
// opposite things from the remote, so the case is decided before any handle is
// opened rather than discovered afterwards.
type SeedCase string

const (
	// SeedEmptyCloud is a database with history meeting a remote that holds
	// nothing: the first push is the seed and nothing is ever fetched.
	SeedEmptyCloud SeedCase = "empty_cloud"
	// SeedExistingDB is a database with history meeting a remote that already
	// holds data. History on both sides is the only case that cannot proceed on
	// its own, because last-push-wins drops one of them silently.
	SeedExistingDB SeedCase = "existing_db"
	// SeedNewMachine is a machine holding no history at all: the remote is the
	// only source, so the open bootstraps and the state is pulled in.
	SeedNewMachine SeedCase = "new_machine"
)

// SeedInput is everything the seed decision turns on. It is three facts rather
// than a database handle so the decision is a pure function a test can drive
// without a remote or a file.
type SeedInput struct {
	// LocalHasHistory is whether this machine's shared database holds rows a
	// push would carry.
	LocalHasHistory bool
	// CloudEmpty is whether the remote holds nothing yet.
	CloudEmpty bool
	// SeedUploaded reports that the documented Turso upload already ran against
	// the seed copy a previous enable wrote, so the history on both sides is
	// this machine's own rather than two histories that disagree.
	SeedUploaded bool
}

// SeedDecision is what the seed matrix decided: the open it wants and the calls
// that follow it. Every arm sets Pull when Bootstrap is false, which is the
// driver's own rule -- an open that skipped the bootstrap owes the caller a
// pull -- so no decision in this file can be the false-then-forgets-the-pull
// one.
type SeedDecision struct {
	// Case is which of the three situations this is.
	Case SeedCase
	// Bootstrap is what the open's BootstrapIfEmpty must carry. It is a plain
	// bool because the decision is total: no case here wants the pointer left
	// unset, so the driver's own default never decides for us.
	Bootstrap bool
	// Pull is whether an explicit pull follows the open.
	Pull bool
	// Push is whether a push follows the open.
	Push bool
	// NeedsUpload is whether the case cannot proceed until the seed copy has
	// been uploaded through the documented path.
	NeedsUpload bool
}

// DecideSeed is the whole seed matrix. A machine with no history bootstraps,
// because the remote is the only source of anything it will ever hold. A
// machine with history and an empty remote pushes, because that push is the
// seed and there is nothing to fetch. History on both sides is the one case
// that refuses, and it refuses until the upload has happened.
func DecideSeed(in SeedInput) SeedDecision {
	switch {
	case !in.LocalHasHistory:
		// The pull is made explicit even though the bootstrap already performs
		// one: a remote that changes between the open and the mark must not
		// leave this machine a round behind with no call that says so.
		return SeedDecision{Case: SeedNewMachine, Bootstrap: true, Pull: true}
	case in.CloudEmpty:
		// A pull against an empty remote costs one round trip and changes
		// nothing, and it is what an open that skipped the bootstrap owes.
		return SeedDecision{Case: SeedEmptyCloud, Bootstrap: false, Pull: true, Push: true}
	case in.SeedUploaded:
		// The upload put this database's history on the remote, so the push that
		// follows carries only what changed since.
		return SeedDecision{Case: SeedExistingDB, Bootstrap: false, Pull: true, Push: true}
	default:
		return SeedDecision{Case: SeedExistingDB, Bootstrap: false, Pull: true, NeedsUpload: true}
	}
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
	// SeedPath is where that copy is written.
	SeedPath string
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
		return EnableResult{}, ErrAlreadyEnabled
	}

	token, _, err := e.Intake.Resolve()
	if err != nil {
		return EnableResult{}, err
	}

	settings, err := e.resolveRemote()
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
		if e.SeedCopy == nil {
			return EnableResult{Case: decision.Case}, ErrSeedUploadRequired
		}
		if err := e.SeedCopy(e.SeedPath); err != nil {
			return EnableResult{Case: decision.Case}, fmt.Errorf("sync: enable: seed copy: %w", err)
		}
		return EnableResult{Case: decision.Case, Seed: e.SeedPath}, fmt.Errorf(
			"%w; upload %s with `turso db import %s`, then re-run this verb with --seed-uploaded",
			ErrSeedUploadRequired, e.SeedPath, e.SeedPath)
	}

	if err := SetToken(e.Local, token, e.now()); err != nil {
		return EnableResult{Case: decision.Case}, err
	}
	if err := MarkEnabled(e.Local, true, e.now()); err != nil {
		return EnableResult{Case: decision.Case}, err
	}

	client, err := e.openClient(ctx, decision, settings, token)
	if err != nil {
		return EnableResult{Case: decision.Case, RemoteURL: settings.RemoteURL}, err
	}
	out := EnableResult{Case: decision.Case, RemoteURL: settings.RemoteURL}
	out.Applied = e.runSeed(ctx, client, decision)
	return out, nil
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
// A --url that contradicts the stored remote refuses rather than overwriting.
// Silently repointing a machine at a second remote would move its rows to a
// database the user did not name, and the refusal names both so the reader can
// see which is which.
func (e *Enabler) resolveRemote() (Settings, error) {
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
	case settings.RemoteURL == "":
		settings.RemoteURL = flagged
		if err := PutSettings(e.Local, settings, e.now()); err != nil {
			return Settings{}, err
		}
		return settings, nil
	case settings.RemoteURL == flagged:
		return settings, nil
	default:
		return Settings{}, fmt.Errorf("%w: the %s section holds %q, --url named %q",
			ErrRemoteConflict, SectionSettings, settings.RemoteURL, flagged)
	}
}

// runSeed makes the calls the decision asked for, in push-then-pull order so
// the pull has the fewest unpushed local changes to roll back and replay. It
// returns whether the pull applied anything.
func (e *Enabler) runSeed(ctx context.Context, client SyncClient, decision SeedDecision) bool {
	ctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()

	if decision.Push {
		if err := client.Push(ctx); err != nil {
			return false
		}
	}
	if !decision.Pull {
		return false
	}
	applied, err := client.Pull(ctx)
	if err != nil {
		return false
	}
	return applied
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
