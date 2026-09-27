package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The agy variables relevo reads. agy rotates ANTIGRAVITY_LS_ADDRESS and
// ANTIGRAVITY_CSRF_TOKEN on every launch, keeps neither in its own process
// environment nor on disk, and injects both only into the commands it runs
// -- so a relevo command running inside agy is the one moment they can be
// captured.
const (
	agyConversationEnv = "ANTIGRAVITY_CONVERSATION_ID"
	agyLSAddressEnv    = "ANTIGRAVITY_LS_ADDRESS"
	agyCSRFTokenEnv    = "ANTIGRAVITY_CSRF_TOKEN"
	agyAgentAPIExeEnv  = "ANTIGRAVITY_AGENTAPI_EXE"
)

// agySecretPrefix is the secret-table name prefix one conversation's
// credentials live under: a conversation 0f0e… is the secret `agy/0f0e…`.
const agySecretPrefix = "agy/"

// agyCredsPruneAfter is how long an unused conversation's credentials are kept
// after the last capture. A conversation that has not touched relevo for a week
// is gone, and its token died with the agy launch that issued it.
const agyCredsPruneAfter = 7 * 24 * time.Hour

// redactedToken is what every rendering of a credential replaces the token
// with, so an accidental %v cannot leak it.
const redactedToken = "<redacted>"

// AgyCreds is one agy conversation's captured agentapi credentials, stored as
// the secret `agy/<conversation_id>` in the machine database. The conversation
// id is the secret's name and the key, not the mastermind record's id: Deliver
// receives only an Endpoint, whose Kind and SessionID are the agy conversation.
type AgyCreds struct {
	ConversationID string    `json:"conversation_id"`
	LSAddress      string    `json:"ls_address"`
	CSRFToken      string    `json:"csrf_token"`
	AgentAPIExe    string    `json:"agentapi_exe"`
	CapturedAt     time.Time `json:"captured_at"`
}

// String renders the credentials with the token redacted, so `%v` and `%+v` on
// an AgyCreds -- a debug print, a wrapped error message -- cannot leak it.
func (c AgyCreds) String() string {
	return fmt.Sprintf("relevo.AgyCreds{conversation_id: %q, ls_address: %q, csrf_token: %s, agentapi_exe: %q, captured_at: %s}",
		c.ConversationID, c.LSAddress, redactedToken, c.AgentAPIExe, c.CapturedAt.Format(time.RFC3339))
}

// GoString is String for %#v, which would otherwise print the struct field by
// field and expose the token.
func (c AgyCreds) GoString() string { return c.String() }

// conversationIDRe is relevo's own copy of the agy conversation id rule: a
// lower-case 8-4-4-4-12 hex UUID. mastermind.Detect carries the same pattern for
// the environment it reads; both are pinned by tests.
var conversationIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validConversationID reports whether conv is an agy conversation id.
func validConversationID(conv string) bool { return conversationIDRe.MatchString(conv) }

// loopbackAgyAddress reports whether addr is a host:port whose host is
// localhost or 127.0.0.1 and whose port is numeric. relevo refuses anything
// else: it would be sending a mastermind's report, which can contain source, to
// whatever host the value names.
func loopbackAgyAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host != "localhost" && host != "127.0.0.1" {
		return false
	}
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}
	return true
}

// SecretStore is the machine database's secret surface (schema v2's `secret`
// table), the store the agy capture, the agy deliverer and the credential
// import run over. Its names are `agy/<conversation id>` for credentials.
type SecretStore interface {
	SecretGet(name string) ([]byte, bool, error)
	SecretPut(name string, value []byte, now time.Time) error
	SecretDelete(name string) error
	SecretNames() ([]string, error)
}

// agySecretName is the secret name one conversation's credentials live under.
func agySecretName(conv string) string { return agySecretPrefix + conv }

// agyEnvValid reports whether the environment carries a capturable agy
// session: a valid conversation id, a loopback address and a non-empty,
// whitespace-free token are all present.
func agyEnvValid(env func(string) string) bool {
	if env == nil {
		return false
	}
	if !validConversationID(env(agyConversationEnv)) {
		return false
	}
	if !loopbackAgyAddress(env(agyLSAddressEnv)) {
		return false
	}
	token := env(agyCSRFTokenEnv)
	return token != "" && !strings.ContainsAny(token, " \t\r\n")
}

// AgyEnvPresent is agyEnvValid, exported: main.go checks it before opening the
// machine database, so a command that runs outside agy opens no database at
// all -- which is what keeps captureAgyEnv free on every verb.
func AgyEnvPresent(env func(string) string) bool { return agyEnvValid(env) }

// CaptureAgyCreds persists the calling agy session's agentapi credentials as
// the secret agy/<conversation_id> and reports whether it wrote. Running inside
// agy is the only place the address and token exist, so every verb calls this
// once before dispatch: an agy mastermind runs relevo constantly (send, wait,
// pull, status), and the secret is therefore fresh after every agy restart as
// soon as the mastermind next touches relevo.
//
// It writes nothing and returns false, nil unless a valid conversation id, a
// loopback address and a non-empty, whitespace-free token are all present, and
// skips the write when the stored credentials already carry the same address,
// token and exe. It never returns an error that contains the token: an error
// names the secret only.
func CaptureAgyCreds(env func(string) string, secrets SecretStore, now time.Time) (bool, error) {
	if secrets == nil || !agyEnvValid(env) {
		return false, nil
	}

	conv := env(agyConversationEnv)
	creds := AgyCreds{
		ConversationID: conv,
		LSAddress:      env(agyLSAddressEnv),
		CSRFToken:      env(agyCSRFTokenEnv),
		AgentAPIExe:    env(agyAgentAPIExeEnv),
		CapturedAt:     now,
	}

	if prev, err := ReadAgyCreds(secrets, conv); err == nil &&
		prev.LSAddress == creds.LSAddress &&
		prev.CSRFToken == creds.CSRFToken &&
		prev.AgentAPIExe == creds.AgentAPIExe {
		return false, nil
	}

	raw, err := json.Marshal(creds)
	if err != nil {
		return false, fmt.Errorf("agy credentials: encode: %w", err)
	}
	name := agySecretName(conv)
	if err := secrets.SecretPut(name, raw, now); err != nil {
		return false, fmt.Errorf("agy credentials: write %s: %w", name, err)
	}

	pruneAgyCreds(secrets, now)
	return true, nil
}

// ImportAgyCreds adopts the pre-database credential files, if any: a present
// <dir>/<conversation>.json is put to the secret agy/<conversation> and only
// then removed, and <dir> is removed when it is left empty.
// The order is put, then remove, never the reverse, so a crash between them leaves the file and the next run imports it
// again. The row wins over a file that is still there, and a malformed file
// fails loudly and stays where it is. It is a no-op with a nil store or dir.
func ImportAgyCreds(secrets SecretStore, dir string) error {
	if secrets == nil || dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agy credentials: read %s: %w", dir, err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		conv := strings.TrimSuffix(name, ".json")
		key := agySecretName(conv)
		path := filepath.Join(dir, name)

		if _, ok, err := secrets.SecretGet(key); err != nil {
			return fmt.Errorf("agy credentials: read %s: %w", key, err)
		} else if ok {
			// The row is the record now: the file is left where it is.
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("agy credentials: read %s: %w", path, err)
		}
		if !json.Valid(raw) {
			return fmt.Errorf("agy credentials: decode %s: invalid JSON", path)
		}
		if err := secrets.SecretPut(key, raw, time.Now().UTC()); err != nil {
			return fmt.Errorf("agy credentials: write %s: %w", key, err)
		}
		if err := os.Remove(path); err != nil {
			slog.Warn("agy credentials: could not remove imported file", "secret", key, "path", path, "err", err)
		}
	}

	_ = os.Remove(dir)
	return nil
}

// ReadAgyCreds reads and validates one conversation's credentials. A missing
// secret returns an error wrapping os.ErrNotExist; the caller turns any error
// into "no credentials yet", so the distinction only matters to a reader.
func ReadAgyCreds(secrets SecretStore, conv string) (AgyCreds, error) {
	name := agySecretName(conv)
	if secrets == nil {
		return AgyCreds{}, fmt.Errorf("agy credentials: read %s: %w", name, os.ErrNotExist)
	}
	raw, ok, err := secrets.SecretGet(name)
	if err != nil {
		return AgyCreds{}, fmt.Errorf("agy credentials: read %s: %w", name, err)
	}
	if !ok {
		return AgyCreds{}, fmt.Errorf("agy credentials: read %s: %w", name, os.ErrNotExist)
	}
	var creds AgyCreds
	if err := json.Unmarshal(raw, &creds); err != nil {
		return AgyCreds{}, fmt.Errorf("agy credentials: decode %s: %w", name, err)
	}
	return creds, nil
}

// pruneAgyCreds deletes every other agy credential whose capture is older than
// agyCredsPruneAfter. Best effort by design: it runs on the capture path, which
// must never fail a command, so every error is dropped.
func pruneAgyCreds(secrets SecretStore, now time.Time) {
	names, err := secrets.SecretNames()
	if err != nil {
		return
	}
	for _, name := range names {
		if !strings.HasPrefix(name, agySecretPrefix) {
			continue
		}
		raw, ok, err := secrets.SecretGet(name)
		if err != nil || !ok {
			continue
		}
		var creds AgyCreds
		if err := json.Unmarshal(raw, &creds); err != nil {
			continue
		}
		if !creds.CapturedAt.IsZero() && now.Sub(creds.CapturedAt) > agyCredsPruneAfter {
			_ = secrets.SecretDelete(name)
		}
	}
}
