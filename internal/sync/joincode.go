package sync

// The join code: the one line a syncing machine prints, the only thing a new
// machine needs to point at the same remote. Fixed prefix, version, and base64url
// body, pastable anywhere without escaping: remote and bearer only, every other
// secret arriving through the signed log -- one credential, not the whole machine.
// Every function here is pure, parsing from the string alone with no network, so
// a malformed code refuses before dialing.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/sanitize"

	"github.com/fuad-daoud/relevo/internal/db"
)

const (
	// JoinCodeVersion is the code shape this build reads, in the prefix not the body,
	// so an unknown version refuses without decoding: a changed layout is never guessed.
	JoinCodeVersion = "v1"

	// JoinCodePrefix is what every code starts with, version included, so a v2 code
	// refuses here rather than reaching the decoder.
	JoinCodePrefix = "relevo-join-" + JoinCodeVersion + "-"

	// joinCodeFamily is the prefix without the version, distinguishing a wrong version
	// from not-a-code: only the first can be a version mistake, so they refuse
	// differently.
	joinCodeFamily = "relevo-join-"
)

// ErrJoinCode is any code this build will not read -- not a code, unknown version,
// undecodable body, unknown field -- saying which, so a holder knows whether to ask
// for a fresh one.
var ErrJoinCode = errors.New("sync: that is not a join code this build reads")

// ErrInviteSyncOff is an invite on a machine that is not syncing: no remote to name,
// so the refusal names the verb that would give it one rather than printing empty.
var ErrInviteSyncOff = errors.New("sync: this machine is not syncing: run `relevo db sync enable` first")

// JoinCode is what a code carries: remote and bearer only. A third field changes
// the contract, so the decoder refuses unknown fields rather than misreading one.
type JoinCode struct {
	// URL is the remote a joiner syncs with.
	URL string `json:"url"`
	// Token is the bearer reaching the remote.
	Token string `json:"token"`
}

// NewJoinCode builds the one line a syncing machine prints; an empty remote or
// bearer refuses rather than encodes, since one without the other points nowhere.
func NewJoinCode(remote string, token []byte) (string, error) {
	if strings.TrimSpace(remote) == "" {
		return "", fmt.Errorf("sync: join code: no remote: %w", db.ErrInvalid)
	}
	if len(bytes.TrimSpace(token)) == 0 {
		return "", fmt.Errorf("sync: join code: no %s: %w", SecretToken, db.ErrInvalid)
	}
	body, err := json.Marshal(JoinCode{URL: remote, Token: string(token)})
	if err != nil {
		return "", fmt.Errorf("sync: join code: encode: %w", err)
	}
	// The unpadded URL alphabet, not the padded standard one: a hand-copied code
	// mangles '=' and '+' first.
	return JoinCodePrefix + base64.RawURLEncoding.EncodeToString(body), nil
}

// ParseJoinCode reads a code back into remote and bearer, the whole of what
// `--join <code>` does before dialing. Surrounding whitespace trims as copy-paste
// residue; anything else refuses: an unknown version says so before decoding, since
// a body from another version may hold other-meaning fields no guess may resolve.
func ParseJoinCode(code string) (JoinCode, error) {
	trimmed := strings.TrimSpace(code)
	if !strings.HasPrefix(trimmed, joinCodeFamily) {
		return JoinCode{}, fmt.Errorf("%w: it does not start with %s", ErrJoinCode, JoinCodePrefix)
	}
	rest := strings.TrimPrefix(trimmed, joinCodeFamily)
	version, encoded, ok := strings.Cut(rest, "-")
	if !ok || encoded == "" {
		return JoinCode{}, fmt.Errorf("%w: it names no version", ErrJoinCode)
	}
	if version != JoinCodeVersion {
		return JoinCode{}, fmt.Errorf("%w: it is version %s and this build reads %s", ErrJoinCode, shownVersion(version), JoinCodeVersion)
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return JoinCode{}, fmt.Errorf("%w: its body is not base64url", ErrJoinCode)
	}
	// The decoder is closed: an unknown field is refused rather than ignored, so
	// extra never reads as absent under a versioned envelope.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var out JoinCode
	if err := dec.Decode(&out); err != nil {
		return JoinCode{}, fmt.Errorf("%w: its body is not a %s code", ErrJoinCode, JoinCodeVersion)
	}
	if strings.TrimSpace(out.URL) == "" || strings.TrimSpace(out.Token) == "" {
		return JoinCode{}, fmt.Errorf("%w: it names no remote or no %s", ErrJoinCode, SecretToken)
	}
	return out, nil
}

// maxShownVersion bounds how much of a refusal-quoted version segment shows.
const maxShownVersion = 16

// shownVersion renders the unreadable version segment for a terminal: cut short and
// made inert, newlines replaced, since the segment is attacker-chosen text.
func shownVersion(version string) string {
	suffix := ""
	if len(version) > maxShownVersion {
		version, suffix = version[:maxShownVersion], "..."
	}
	return strings.ReplaceAll(sanitize.Text(version), "\n", "\uFFFD") + suffix
}
