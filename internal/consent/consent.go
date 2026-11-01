// Package consent carries the two consent documents, repository answers by
// remote URL and session answers by kind and id. It exists because a *db.DB
// method cannot return a synclog document without an import cycle.
package consent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// value is one key's answer and when it was given; both documents carry it,
// and stay separate because their stores and key rules differ, so neither's
// keys can be read as the other's.
type value struct {
	Answer db.Consent `json:"answer"`
	At     time.Time  `json:"at"`
}

func encode(c db.Consent, at time.Time) (json.RawMessage, error) {
	raw, err := json.Marshal(value{Answer: c, At: at})
	if err != nil {
		return nil, fmt.Errorf("consent: encode %q: %w", c, err)
	}
	return raw, nil
}

// decodeValue reads one arriving answer and refuses every shape this build has
// no reader for. An answer outside yes and no is a value no code here can act
// on, and storing it would put a row before every reader that treats it as a
// decision. An absent or unparseable time is a value whose order is unknown, so
// accepting one would let a peer place a key at a moment of its own choosing.
// An unknown field is a peer's rule this build has not read, and ignoring it
// would apply the answer on the wrong terms -- the same refusal the log makes
// on a body carrying a field it does not know. Unset is not a value either:
// clearing is a tombstone.
func decodeValue(doc, key string, raw json.RawMessage) (value, error) {
	var v value
	if len(bytes.TrimSpace(raw)) == 0 {
		return v, fmt.Errorf("%s %s: value is empty: %w", doc, key, db.ErrInvalid)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return v, fmt.Errorf("%s %s: value is not a JSON object: %w: %w", doc, key, db.ErrInvalid, err)
	}
	allowed := map[string]bool{"answer": true, "at": true}
	for name := range fields {
		if !allowed[name] {
			return v, fmt.Errorf("%s %s: value carries unknown field %q: %w", doc, key, name, db.ErrInvalid)
		}
	}
	answerText, hasAnswer := fields["answer"]
	atText, hasAt := fields["at"]
	if !hasAnswer || !hasAt {
		return v, fmt.Errorf("%s %s: value must carry an answer and a time: %w", doc, key, db.ErrInvalid)
	}
	if err := json.Unmarshal(answerText, &v.Answer); err != nil {
		return v, fmt.Errorf("%s %s: answer is not text: %w: %w", doc, key, db.ErrInvalid, err)
	}
	// Unset is storable in a row, but it is not a value any document carries:
	// it would be a third state beside the answer and the absence, and a peer
	// could write an answer a person never gave and have it read back as one
	// they did.
	if v.Answer == db.ConsentUnset {
		return v, fmt.Errorf("%s %s: unset is a tombstone, not a value: %w", doc, key, db.ErrInvalid)
	}
	if err := v.Answer.Valid(); err != nil {
		return v, fmt.Errorf("%s %s: %w", doc, key, err)
	}
	if err := json.Unmarshal(atText, &v.At); err != nil {
		return v, fmt.Errorf("%s %s: the time is not a timestamp: %w: %w", doc, key, db.ErrInvalid, err)
	}
	if v.At.IsZero() {
		return v, fmt.Errorf("%s %s: the time is zero: %w", doc, key, db.ErrInvalid)
	}
	return v, nil
}

// validURL is the repository document's key rule: a non-empty remote URL. The
// rule is deliberately not a scheme allowlist -- git speaks several schemes --
// and deliberately not a length cap, because the store refuses an overlong URL
// and a second limit here is a second thing to keep in step.
func validURL(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("%s: the key is not a remote URL: %w", db.ConsentDoc, db.ErrInvalid)
	}
	return nil
}

// sessionKey splits a session consent key into its kind and id, refusing every
// shape that does not name exactly one session.
//
// The separator is a slash because a kind is a harness name and an id is a
// session name, neither of which carries one; refusing a second slash is what
// keeps "claude/a/b" from being read as kind "claude" and id "a/b", which is a
// session this machine has never seen.
func sessionKey(key string) (kind, sessionID string, err error) {
	kind, sessionID, found := strings.Cut(key, "/")
	if !found {
		return "", "", fmt.Errorf("%s: the key %q names no kind and id: %w", db.SessionConsentDoc, key, db.ErrInvalid)
	}
	if kind == "" || sessionID == "" {
		return "", "", fmt.Errorf("%s: the key %q has an empty kind or id: %w", db.SessionConsentDoc, key, db.ErrInvalid)
	}
	if strings.Contains(sessionID, "/") {
		return "", "", fmt.Errorf("%s: the key %q names more than one id: %w", db.SessionConsentDoc, key, db.ErrInvalid)
	}
	return kind, sessionID, nil
}
