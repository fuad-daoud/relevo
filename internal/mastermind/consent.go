package mastermind

import "github.com/fuad-daoud/relevo/internal/db"

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
// the guide alone, and no stays silent.
func ConsentText(state Consent, rec *Record) string {
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

// HookConsent is the Claude hook's answer for a consent text: an empty text
// answers with an empty object, so Claude Code adds no context and the session
// starts bare.
func HookConsent(text string) []byte {
	if text == "" {
		return []byte("{}\n")
	}
	return encodeHookContext(text)
}
