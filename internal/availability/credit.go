package availability

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

// creditPatterns is the credit-exhaustion family a decision point classifies
// before it calls MatchLimit. The class is not harness-specific: every provider
// refuses a request it cannot pay for in its own words. Two of the patterns
// restate the harness's own opencode limit patterns, so the class is decided
// without consulting the candidate set; the two copies must agree. Keeping the
// two tables separate is deliberate: a limit pattern is timed, a credit
// pattern is not.
//
// They key on what actually reaches a scan. transcript.LimitLines is the
// channel, and it hands a pattern the harness's own message TEXT, so a pattern
// here matches a sentence, never a structured field.
var creditPatterns = compileOutage(
	// Quota counts as credit: a balance that will not cover the request is
	// short.
	`\binsufficient (credits?|quota)\b`,
	`\brequires more credits\b`,
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
