package sync

// The machine-local half of an enable: stored token, remote-pointing section, and
// on-mark. All live beside the shared database so none leaves with a change set,
// shared by turn-off, status and editor rather than a package of their own.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

// The refusals the enable surface answers with, sentinels branching on class while
// the message names the fix.
var (
	// ErrAlreadyEnabled is a machine already syncing. A stopped-before-join machine
	// is off, so a second enable resumes it; a machine that is on refuses until
	// turned off, because the mark is never overridden.
	ErrAlreadyEnabled = errors.New("sync: already enabled on this machine")

	// ErrNoToken is an intake with no token on either route, naming secret and
	// routes and nothing tried or held: every line here can end up in a log.
	ErrNoToken = errors.New("sync: no " + SecretToken + ": pass --token-stdin or set " + EnvToken)

	// ErrNoRemote is an enable naming no remote on either route, flag first and
	// section second: the section is the advanced route, not a first-run reader's.
	ErrNoRemote = errors.New("sync: no remote is configured on this machine: pass --url or set the " + SectionSettings + " section's remote_url")

	// ErrRemoteConflict is a --url contradicting the stored remote: moving rows
	// elsewhere unasked refuses, naming both remotes rather than picking a winner.
	ErrRemoteConflict = errors.New("sync: --url names a remote this machine is not pointed at")
)

// RemoteConflictError is ErrRemoteConflict carrying the two remotes named.
type RemoteConflictError struct {
	// URL is the --url value and Stored the section's remote_url.
	URL, Stored string
}

func (e *RemoteConflictError) Error() string {
	return fmt.Sprintf("%v: --url is %s but this machine's stored remote_url is %s", ErrRemoteConflict, e.URL, e.Stored)
}

func (e *RemoteConflictError) Unwrap() error { return ErrRemoteConflict }

// EnvToken is the environment variable a token is read from when --token-stdin is
// not given; named for the minting tool, so a shell exporting one for turso's CLI
// does the right thing unasked.
const EnvToken = "TURSO_TOKEN"

// TokenSource names the route a token arrived by, so a caller says where without
// holding the value; never formatted into a refusal, hence a name not a value.
type TokenSource string

const (
	// TokenSourceNone is an intake that resolved nothing.
	TokenSourceNone TokenSource = ""
	// TokenSourceStdin is a token read from standard input.
	TokenSourceStdin TokenSource = "stdin"
	// TokenSourceEnv is a token read from the environment.
	TokenSourceEnv TokenSource = "env"
)

// TokenIntake is the token an enable would store and its two arrival routes: flag
// beats environment, so a piped token wins and an unpassed flag never shadows a
// set environment.
type TokenIntake struct {
	// FromStdin says the caller asked for the token on standard input.
	FromStdin bool
	// Stdin is what standard input held, already read, consulted only when FromStdin
	// is set, so an empty pipe never ignores an environment-carried token.
	Stdin []byte
	// EnvValue is what the environment held under EnvToken.
	EnvValue string
}

// Resolve returns the token to store and the route it came by. An empty route
// falls through to the next, so an empty flag still finds the environment's token.
// Neither yielding anything is ErrNoToken, one fixed line: routes named, attempt not.
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

// R2Intake is what an enable was given for R2 and the stored fallback. The flag
// beats stored per field, so repointing keeps the old secret: non-secret flags
// describe where, and only where changed.
type R2Intake struct {
	// Endpoint, Bucket and KeyID are the flags, empty when they were not passed.
	Endpoint string
	Bucket   string
	KeyID    string
	// Secret is what --r2-secret-stdin or EnvR2Secret resolved to, empty when
	// neither carried anything.
	Secret []byte
	// Stored is what the machine already holds, read by the caller to keep this rule
	// a pure function testable without a database.
	Stored R2Secrets
}

// R2Resolve returns the four credentials an enable would store: supplied wins,
// unpassed falls back to stored. Nothing resolving is ErrNoR2, refused here rather
// than at first upload so the enable refuses before importing anything.
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

// SectionSettings is the config section this machine's sync settings live under,
// machine-local by table: the name is the only thing settings and section agree on.
const SectionSettings = "sync"

// ReadSettings returns this machine's sync section, or zero Settings when none: an
// absent section reads as off with no remote, which is exactly what it is.
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

// MarkEnabled writes the machine-local mark that sync is on or off. Two rows carry
// the one fact in one transaction -- section and sync.enabled marker -- because
// writing one without the other drifts into off-yet-pushing or pushing-with-nothing.
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

// PutSettings writes the sync section on its own, without the enabled marker: the
// advanced route storing a remote before the mark, so a later refusal leaves the
// machine pointed right with sync still off.
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

// encodeSettings is the one place a Settings becomes a section body, so both writers
// agree on the on-disk shape.
func encodeSettings(settings Settings) ([]byte, error) {
	body, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("sync: encode the %s section: %w", SectionSettings, err)
	}
	return body, nil
}
