package availability

import (
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

var transportPatterns = compileOutage(
	`\bECONNRESET\b`,
	`\bsocket connection was closed\b`,
	`\bstream ended without finish_reason\b`,
	`\bConnection lost while reading the response\b`,
	`\bStream error occurred\b`,
	`\bprovider\.transport\b`,
)

// MatchTransport returns the most recent line a transport pattern matches. A
// line an outage or credit pattern also matches is not transport. Limit
// exclusion is the caller's: it orders the limit check first.
func MatchTransport(text string) (line string, ok bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if transcript.IsThinking(l) || !matchesAny(l, transportPatterns) {
			continue
		}
		if _, out := MatchOutage(l, time.Time{}, 0); out {
			return "", false
		}
		if _, cr := MatchCredit(l); cr {
			return "", false
		}
		return capLine(l), true
	}
	return "", false
}
