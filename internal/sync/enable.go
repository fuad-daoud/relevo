package sync

// The machine-local half of an enable: the token it stores, the section it
// points at a remote, and the mark that says the machine is on. Every one of
// them lives in the file beside the shared database, so none can leave with a
// change set. The turn-off, the status read and the editor all read and write
// exactly them, which is why they share a file with the enable rather than a
// package of their own.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

// The refusals the enable surface answers with. They are sentinels so a caller
// branches on the class while the message names the fix.
var (
	// ErrAlreadyEnabled is a machine already syncing. An enable that stopped
	// before the join finished left the machine off, so a second enable resumes
	// it rather than inheriting a half-join; a machine that is on refuses until
	// it is turned off, because the mark is the one thing an enable never
	// overrides.
	ErrAlreadyEnabled = errors.New("sync: already enabled on this machine")

	// ErrNoToken is an intake with no token on either route. The message names
	// the secret and the two routes and stops there: every line out of this
	// path can end up in a log, so nothing here says which route was tried or
	// what either of them held.
	ErrNoToken = errors.New("sync: no " + SecretToken + ": pass --token-stdin or set " + EnvToken)

	// ErrNoRemote is an enable that named no remote on either route. A hand-
	// written section is the advanced route, so the refusal names the flag first
	// and the section second rather than sending a first-run reader to a config
	// document to discover where sync keeps its settings.
	ErrNoRemote = errors.New("sync: no remote is configured on this machine: pass --url or set the " + SectionSettings + " section's remote_url")

	// ErrRemoteConflict is a --url that contradicts the remote the section
	// already holds. Pointing this machine somewhere else would move its rows to
	// a database the user did not ask for, so it refuses and names both remotes
	// rather than picking a winner.
	ErrRemoteConflict = errors.New("sync: --url names a remote this machine is not pointed at")
)

// EnvToken is the environment variable a token is read from when --token-stdin
// is not given. It carries the name of the tool that mints these tokens, so a
// shell already exporting one for turso's own CLI does the right thing without
// being told.
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

// TokenIntake is the token an enable would store and the two routes it could
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

// R2Intake is what an enable was given for R2, and the stored credentials it
// falls back to. The flag beats the stored value per field, so a machine
// pointed at a new bucket keeps its old secret rather than being asked for it
// again -- the three non-secret flags describe where, and only where changed.
type R2Intake struct {
	// Endpoint, Bucket and KeyID are the flags, empty when they were not passed.
	Endpoint string
	Bucket   string
	KeyID    string
	// Secret is what --r2-secret-stdin or EnvR2Secret resolved to, empty when
	// neither carried anything.
	Secret []byte
	// Stored is what the machine already holds, read by the caller so this rule
	// stays a pure function of its input and can be tested without a database.
	Stored R2Secrets
}

// R2Resolve returns the four credentials an enable would store.
//
// A field the caller supplied wins, and a field it did not is taken from what is
// already stored. Nothing resolving is ErrNoR2, which is the refusal an enable
// stops on: a machine that cannot name a bucket cannot move a body, and the
// check is here rather than at the first upload so the enable refuses before it
// has imported anything.
func (i R2Intake) R2Resolve() (R2Secrets, error) {
	out := R2Secrets{
		Endpoint: i.Endpoint,
		Bucket:   i.Bucket,
		KeyID:    i.KeyID,
		Secret:   string(bytes.TrimSpace(i.Secret)),
	}
	if out.Endpoint == "" {
		out.Endpoint = i.Stored.Endpoint
	}
	if out.Bucket == "" {
		out.Bucket = i.Stored.Bucket
	}
	if out.KeyID == "" {
		out.KeyID = i.Stored.KeyID
	}
	if out.Secret == "" {
		out.Secret = i.Stored.Secret
	}
	if !out.Complete() {
		return R2Secrets{}, ErrNoR2
	}
	if err := blobstore.CheckEndpoint(out.Endpoint); err != nil {
		return R2Secrets{}, fmt.Errorf("sync: r2.endpoint: %w: %w", err, db.ErrInvalid)
	}
	return out, nil
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
// advanced route: what stores a remote before the mark, because a remote is not
// a decision anything made and a refusal after it must leave the machine
// pointed at the right remote with sync still off.
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
