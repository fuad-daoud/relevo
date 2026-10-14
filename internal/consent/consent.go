// Package consent carries the two consent documents: a repository's answer to
// "may relevo register this repo's sessions as MasterMinds?", keyed by remote
// URL, and a session's own answer to the same question, keyed by harness kind
// and session id.
//
// The two are separate documents rather than one document with two kinds of key
// because their backing stores differ -- a repository answer is a column on a
// row in the shared file, a session answer is a row in the machine-local file --
// and because their validity rules differ with them: a repository key is a
// non-empty remote URL, a session key is a non-empty kind and a non-empty id.
// Per-key export and applied state then needs no key-kind dispatch, so the two
// sets of keys can never be read as one another's.
//
// The package exists at all because internal/db cannot implement a document: a
// document is a synclog type, synclog imports internal/db, and a method on
// *db.DB cannot return one without the cycle.
package consent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// value is the document's value for one key: the answer, and when it was given.
//
// Both documents carry the same shape, because both carry the same thing. They
// are separate documents for the reasons in the package comment, not for the
// sake of the value they hold.
type value struct {
	Answer db.Consent `json:"answer"`
	At     time.Time  `json:"at"`
}

// encode renders one answer as a document value.
func encode(c db.Consent, at time.Time) (json.RawMessage, error) {
	raw, err := json.Marshal(value{Answer: c, At: at})
	if err != nil {
		return nil, fmt.Errorf("consent: encode %q: %w", c, err)
	}
	return raw, nil
}

// decodeValue reads one arriving answer and refuses every shape this build has
// no reader for.
//
// Each refusal answers its own attack. An answer outside yes and no is a value
// no code here can act on, and storing it would put a row before every reader
// that treats it as a decision. An absent or unparseable time is a value whose
// order against everything else is unknown, and a document orders its keys by
// when they were written, so accepting one would let a peer place a key at a
// moment of its own choosing. An unknown field is a peer's rule this build has
// not read, and ignoring it would apply the answer on the wrong terms -- the
// same refusal the log itself makes on a body that carries a field it does not
// know. Unset is not accepted as a value either: clearing is a tombstone, and a
// key the document no longer holds says that without a value that says it.
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
	// Unset is storable in a row -- it is what a cleared answer leaves behind --
	// but it is not a value any document carries. Clearing is a tombstone, and a
	// tombstone is the key's absence; an "unset" value would be a third state
	// beside the answer and the absence, and every reader would need a rule for
	// it. Refusing it here means a peer cannot write an answer a person never
	// gave and have it read back as one they did.
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

// validURL is the repository document's key rule: a non-empty remote URL, held
// to what a URL is and nothing more. The rule is deliberately not a scheme
// allowlist -- git speaks several schemes and normalisation already decided
// which form this is -- and deliberately not a length cap on the value, because
// a URL longer than any real one is refused by the store rather than here, and
// a second place to disagree about it is a second thing to keep in step.
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
