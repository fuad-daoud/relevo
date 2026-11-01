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

	"github.com/fuad-daoud/relevo/internal/sanitize"
)

// The agy variables relevo reads. agy rotates them on every launch and
// injects both only into the commands it runs, so inside agy is the one
// moment they can be captured.
const (
	agyConversationEnv = "ANTIGRAVITY_CONVERSATION_ID"
	agyLSAddressEnv    = "ANTIGRAVITY_LS_ADDRESS"
	agyCSRFTokenEnv    = "ANTIGRAVITY_CSRF_TOKEN"
	agyAgentAPIExeEnv  = "ANTIGRAVITY_AGENTAPI_EXE"
)

// agySecretPrefix is the secret-table prefix: conversation 0f0e… lives as `agy/0f0e…`.
const agySecretPrefix = "agy/"

// agyCredsPruneAfter is how long unused credentials are kept: a conversation
// silent for a week is gone, and its token died with the agy launch that issued it.
const agyCredsPruneAfter = 7 * 24 * time.Hour

// redactedToken replaces the token in every rendering, so an accidental %v cannot leak it.
const redactedToken = sanitize.Redacted

// AgyCreds is one agy conversation's captured agentapi credentials. The
// conversation id is the secret's name and key, not the mastermind record's id.
type AgyCreds struct {
	ConversationID string    `json:"conversation_id"`
	LSAddress      string    `json:"ls_address"`
	CSRFToken      string    `json:"csrf_token"`
	AgentAPIExe    string    `json:"agentapi_exe"`
	CapturedAt     time.Time `json:"captured_at"`
}

// String redacts the token, so `%v` and `%+v` cannot leak it.
func (c AgyCreds) String() string {
	return fmt.Sprintf("relevo.AgyCreds{conversation_id: %q, ls_address: %q, csrf_token: %s, agentapi_exe: %q, captured_at: %s}",
		c.ConversationID, c.LSAddress, redactedToken, c.AgentAPIExe, c.CapturedAt.Format(time.RFC3339))
}

// GoString is String for %#v, which would otherwise expose the token field by field.
func (c AgyCreds) GoString() string { return c.String() }

// conversationIDRe is relevo's own copy of the agy conversation id rule,
// matching mastermind.Detect's; both are pinned by tests.
var conversationIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validConversationID(conv string) bool { return conversationIDRe.MatchString(conv) }

// loopbackAgyAddress refuses anything but a numeric-port localhost host:port:
// a report can contain source, so it must never go to whatever host a value names.
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

// SecretStore is the machine database's secret surface the agy capture,
// deliverer and credential import run over.
type SecretStore interface {
	SecretGet(name string) ([]byte, bool, error)
	SecretPut(name string, value []byte, now time.Time) error
	SecretDelete(name string) error
	SecretNames() ([]string, error)
}

func agySecretName(conv string) string { return agySecretPrefix + conv }

// agyEnvValid reports whether the environment carries a capturable agy session.
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

// AgyEnvPresent is agyEnvValid, exported so a command outside agy opens no
// database at all -- which keeps capture free on every verb.
func AgyEnvPresent(env func(string) string) bool { return agyEnvValid(env) }

// CaptureAgyCreds persists the calling agy session's credentials and reports
// whether it wrote. Inside agy is the only place they exist, so every verb
// calls this once before dispatch and the secret is fresh after every restart.
// It skips the write when the stored credentials match, and no error it
// returns ever contains the token.
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

// ImportAgyCreds adopts the pre-database credential files: put, then remove,
// never the reverse, so a crash between them leaves the file for the next run.
// The row wins over a leftover file; a malformed file fails loudly and stays.
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

// ReadAgyCreds reads one conversation's credentials; a missing secret wraps
// os.ErrNotExist, which the caller folds into "no credentials yet".
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

// pruneAgyCreds deletes other credentials older than agyCredsPruneAfter. Best
// effort: it runs on the capture path, which must never fail a command.
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
