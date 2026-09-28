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
const AskNote = `This repository has not answered whether relevo should be its MasterMind.
Before doing anything else, ask the human which of these three options they want:
  relevo mastermind enable          -- this session only
  relevo mastermind enable --repo   -- this repository from now on
  relevo mastermind disable --repo  -- never in this repository
Then run the command they choose. Do not act on anything else until they answer.`

// ConsentText is what a session is told for its repo's answer. rec is nil when
// the repo answered yes but the session has no record yet: the guide goes
// alone, and the harness registers before it hands anything off.
func ConsentText(state Consent, rec *Record) string {
	switch state {
	case ConsentYes:
		if rec == nil {
			return Guide()
		}
		return hookContext(*rec) + "\n\n" + Guide()
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
