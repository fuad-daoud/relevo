package sync

// The join code: the one line a machine that already syncs prints, and the only
// thing a new machine needs before it can point itself at the same remote.
//
// It is a fixed prefix, a version, and a base64url body -- nothing else -- so a
// reader can tell what a string is before it decodes it, and so a code can be
// pasted through a terminal, a chat window or a shell history without a single
// character needing an escape. The body carries the remote and the bearer
// credential and nothing more: the passphrase, the bucket credentials and every
// other secret arrive through the signed log, so a code copied into a ticket is
// one credential rather than the whole machine.
//
// Every function here is pure. Parsing a code is a function of the string alone
// and reaches no network, which is what lets an enable refuse a malformed code
// before it dials anything.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
)

const (
	// JoinCodeVersion is the code shape this build reads. It is part of the
	// prefix rather than a field in the body, so a reader can refuse a version it
	// does not know without decoding anything: a body whose layout changed is not
	// something to guess at.
	JoinCodeVersion = "v1"

	// JoinCodePrefix is what every code starts with. The version is part of it, so
	// a v2 code does not share a prefix with this one and is refused here rather
	// than reaching the decoder.
	JoinCodePrefix = "relevo-join-" + JoinCodeVersion + "-"

	// joinCodeFamily is the prefix without the version. It is what distinguishes
	// "this is a join code in some version" from "this is not a join code", and
	// the two are refused differently because only one of them can be a version
	// mistake.
	joinCodeFamily = "relevo-join-"
)

// ErrJoinCode is any code this build will not read: one that is not a code, one
// in a version it does not know, one whose body does not decode, and one carrying
// a field it does not know. The message says which, because the four have
// different causes and a reader holding a code copied from somewhere else needs
// to know whether to ask for a fresh one.
var ErrJoinCode = errors.New("sync: that is not a join code this build reads")

// ErrInviteSyncOff is an invite on a machine that is not syncing. There is no
// remote to name in a code from such a machine, so the refusal names the verb
// that would give it one rather than printing an empty code.
var ErrInviteSyncOff = errors.New("sync: this machine is not syncing: run `relevo db sync enable` first")

// JoinCode is what a code carries. The two fields are the whole of it: where the
// remote is, and the bearer credential that reaches it. A third field is a change
// to the contract, and the decoder refuses unknown fields precisely so one added
// later cannot be read as something this build means by one of these.
type JoinCode struct {
	// URL is the remote a joiner syncs with.
	URL string `json:"url"`
	// Token is the bearer credential that reaches it.
	Token string `json:"token"`
}

// NewJoinCode builds the one line a machine that already syncs prints.
//
// A remote or a token that is empty is refused rather than encoded: a code
// carrying one without the other cannot point a joiner anywhere, and printing it
// would hand the reader a line that fails on the machine it is meant for.
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
	// The unpadded URL alphabet, not the padded standard one: a code is meant to
	// be copied by hand, and '=' and '+' are the two characters a paste is most
	// likely to mangle.
	return JoinCodePrefix + base64.RawURLEncoding.EncodeToString(body), nil
}

// ParseJoinCode reads a code back into the remote and the token. It is the whole
// of what `--join <code>` does before anything is dialed.
//
// Surrounding whitespace is trimmed rather than refused, because a code arrives
// by copy-paste and the newline a terminal adds is not part of what the writer
// meant. Anything else that does not match is refused: a version this build does
// not read says so rather than reaching the decoder, because a body written for
// another version may hold fields with other meanings and guessing at them is how
// a joiner ends up pointed somewhere its operator did not choose.
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
		return JoinCode{}, fmt.Errorf("%w: it is version %s and this build reads %s", ErrJoinCode, version, JoinCodeVersion)
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return JoinCode{}, fmt.Errorf("%w: its body is not base64url", ErrJoinCode)
	}
	// The decoder is closed: a field this build does not know is refused rather
	// than ignored. Ignoring it would let a code carrying something extra be read
	// as though it had not, which is the one behaviour a versioned envelope exists
	// to prevent.
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
