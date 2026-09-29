package mastermind

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Consent is db.Consent, re-exported so a caller passes one answer type and
// the rendering below stays testable without a database handle.
type Consent = db.Consent

const (
	ConsentUnset = db.ConsentUnset
	ConsentYes   = db.ConsentYes
	ConsentNo    = db.ConsentNo
)

// AskNote is the unset answer's text. A SessionStart hook cannot prompt, so
// the note asks the model to put the question to the human and then run the
// command that records the answer. The commands it names are the CLI's; a test
// pins them against a rename.
const AskNote = `Before anything else, ask the human whether relevo should be this repository's MasterMind. If you have an interactive question or choice tool, use it; otherwise ask in text. Offer exactly these three options:
  relevo mastermind enable          -- this session only
  relevo mastermind enable --repo   -- this repository from now on
  relevo mastermind disable --repo  -- never in this repository
Wait for their answer, then run the command they choose. Do not work on anything else first.`

// ConsentText is what a session is told for its repo's answer and record. A
// session with a record is always briefed, whatever the repo says: an explicit
// `enable` answered for that session. Without a record, unset asks, yes briefs
// the guide alone, and no stays silent. A `no` answer hides any record: the
// session is not briefed even when one exists.
func ConsentText(state Consent, rec *Record) string {
	if state == ConsentNo {
		return ""
	}
	if rec != nil {
		return hookContext(*rec) + "\n\n" + Guide()
	}
	switch state {
	case ConsentYes:
		return Guide()
	case ConsentUnset:
		return AskNote
	default:
		return ""
	}
}

// EffectiveConsent is the answer that governs a session: its own answer when it
// has one, otherwise the repository's. A session `no` therefore hides a `yes`
// repository, and a session `yes` registers even in an unset or `no` one.
func EffectiveConsent(session, repo Consent) Consent {
	if session != ConsentUnset {
		return session
	}
	return repo
}

// The status tokens: the compact state a session is told so a change can be
// noticed. StatusMasterMind's token is "mastermind:<id>:<name>"; a rename is a
// token change.
const (
	StatusNone          = "none"
	StatusAsk           = "ask"
	statusMasterMindPre = "mastermind:"
)

// StatusToken is the token for a session's effective answer and its record.
// A record always names a mastermind; otherwise unset is the open question and
// anything else is "none". A caller with an effective yes must register first
// (as the hooks do), so a granted session carries its record's token.
func StatusToken(effective Consent, rec *Record) string {
	if rec != nil {
		return statusMasterMindPre + rec.ID + ":" + rec.Name
	}
	if effective == ConsentUnset {
		return StatusAsk
	}
	return StatusNone
}

// StatusMasterMindParts splits a mastermind token into its id and name. ok is
// false for any other token.
func StatusMasterMindParts(token string) (id, name string, ok bool) {
	rest, found := strings.CutPrefix(token, statusMasterMindPre)
	if !found {
		return "", "", false
	}
	id, name, found = strings.Cut(rest, ":")
	if !found || id == "" || name == "" {
		return "", "", false
	}
	return id, name, true
}

// StatusNotice is the text a session is handed when its status token changes,
// so a session told "ask" learns it was answered, and one told it is a
// MasterMind learns it is not (or was renamed) without waiting for the next
// request's context. Pure: no database, no clock. No change is empty.
func StatusNotice(prev, next string, rec *Record) string {
	if prev == next {
		return ""
	}

	if prevID, prevName, prevOK := StatusMasterMindParts(prev); prevOK {
		if nextID, nextName, nextOK := StatusMasterMindParts(next); nextOK {
			if prevID == nextID && prevName != nextName {
				return fmt.Sprintf("Status change: this session's relevo MasterMind is now named %s.", nextName)
			}
		}
	}

	switch {
	case next == StatusAsk:
		return AskNote
	case next == StatusNone:
		switch {
		case strings.HasPrefix(prev, statusMasterMindPre):
			return "Status change: this session is no longer a relevo MasterMind. Stop acting as one: no bind, send, wait, or done."
		case prev == StatusAsk:
			return "Status change: the consent question was answered no. Do not ask again; this session is not a relevo MasterMind."
		}
	case strings.HasPrefix(next, statusMasterMindPre):
		return ConsentText(ConsentYes, rec)
	}
	return ""
}

// HookConsent is the Claude hook's answer for a consent text: an empty text
// answers with an empty object, so Claude Code adds no context and the session
// starts bare.
func HookConsent(text string) []byte {
	return HookConsentFor(HookEventSessionStart, text)
}

// HookConsentFor is HookConsent for an arbitrary hook event, so a
// UserPromptSubmit notice carries its own hookEventName.
func HookConsentFor(event, text string) []byte {
	if text == "" {
		return []byte("{}\n")
	}
	return encodeHookContext(event, text)
}
