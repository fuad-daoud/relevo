package availability

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

// creditPatterns is the credit-exhaustion family a decision point classifies
// before it calls MatchLimit. The class lives here rather than only in the
// harness table because it is not harness-specific: every provider refuses a
// request it cannot pay for in its own words, and a candidate has no balance of
// its own to declare.
//
// Two of these are the harness's own opencode limit patterns -- insufficient
// credits/quota and requires more credits -- restated here so the class is
// decided without consulting the candidate set, and so it can be extended
// without editing a harness table that other paths also read. The rest are the
// provider sentences the class is known to arrive in. Keeping the two tables
// separate is deliberate: a limit pattern is timed, a credit pattern is not, so
// a shared entry could only ever be one or the other.
//
// They key on what actually reaches a scan. transcript.LimitLines is the
// channel, and it hands a pattern the harness's own message TEXT -- an error
// event's message, a failed result's error text, or a non-JSON line of harness
// stderr -- so a pattern here matches a sentence, never a structured field.
var creditPatterns = compileOutage(
	// The two patterns internal/harness already carried for opencode, verbatim
	// in substance. Insufficient quota is included with credits: a provider
	// that says the balance will not cover the request has said the balance is
	// short, whether or not it uses the word credit.
	`\binsufficient (credits?|quota)\b`,
	`\brequires more credits\b`,
	// The provider sentences that name the balance being spent down rather
	// than a quota window being reached. The seed for this class named all
	// three of these; the last two had no pattern anywhere in the tree.
	`\bcredits exhausted\b`,
	`\bcan only afford\b`,
)

// MatchCredit scans text line by line from the last line backwards and returns
// the most recent line any credit pattern matches -- the same walk and the same
// last-line-wins rule MatchLimit and MatchOutage use, on the same
// harness-authored scan text a limit scan reads. Thinking lines are skipped,
// because a model reasoning about an empty balance is not an empty balance.
//
// It takes no time and returns no time, which is the whole point. A credit
// exhaustion is not a window that rolls over: it ends when a human tops the
// account up and clears the gate. The reset a line may name describes the
// limit that ran out, not the top-up that has not happened, so reading it as
// the gate end would reopen the candidate on a clock no provider promised.
// The caller writes the zero Until that means "until cleared" and lets the
// existing ledger, projection and clear path carry it.
//
// ok is false when no line matches, which is the caller's signal that this exit
// is not a credit exhaustion and every existing timed path stands. Never
// errors, never panics.
func MatchCredit(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if transcript.IsThinking(line) {
			continue
		}
		if !matchesAny(line, creditPatterns) {
			continue
		}
		return capLine(line), true
	}
	return "", false
}
