package availability

import (
	"regexp"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

// OutageMatch is one provider-outage pattern match against a builder's output:
// the provider said it could not serve the request right now, rather than
// refusing it on quota grounds. It carries the same two reset fields as
// LimitMatch, so a decision point writes one ledger entry either way.
type OutageMatch struct {
	Line   string    // the matched line, trimmed, sanitised, capped at 200 runes
	Until  time.Time // gate end, UTC
	Parsed bool      // Until came from Line, not from the fallback
}

// outagePatterns is the provider-outage family a decision point classifies
// before it calls a no-report exit until-cleared. The patterns live here
// rather than in the harness table because the class is not harness-specific:
// every harness prints a 5xx or the gRPC UNAVAILABLE status when the provider
// itself is down, and a candidate has no outage of its own to declare.
//
// They key on what actually reaches a scan. transcript.LimitLines is the
// channel, and it hands a pattern the harness's own message TEXT -- an error
// event's message, a failed result's result/error text, or a non-JSON line of
// harness stderr -- never a structured status field, so there is no pattern here
// for `"code":503` beside a message: that object never gets scanned.
//
// Nothing here keys on a bare "ERROR" or a bare status word either. A harness
// printing "ERROR" is exactly the genuine crash this classifier must leave
// alone, so every pattern names the status name or the numeric code, and always
// in the shape the harness prints them beside.
var outagePatterns = compileOutage(
	// The gRPC status name Google-shaped providers print, in either case:
	// "UNAVAILABLE (code 503)", "upstream unavailable".
	`\bUNAVAILABLE\b`,
	// The numeric 5xx the same providers print beside it: "code 503",
	// "(code: 500)", "code=502".
	`\bcode[\s:=-]*5\d\d\b`,
	// A status the line states as one: "HTTP 502", "http/1.1 503",
	// "status 500", "status_code: 503", and the same inside a raw JSON line
	// of harness stderr ("status_code":503) -- the one channel that hands a
	// pattern more than the message text.
	`\b(?:http/?[\d.]*\s+|status(?:[\s_-]*code)?[\s"':=-]+)5\d\d\b`,
	// The provider's own words for the class, for a line that names no code.
	`\b(?:service unavailable|internal server error|bad gateway|gateway timeout)\b`,
)

// MatchOutage scans text line by line from the last line backwards and returns
// the first (i.e. most recent) line any outage pattern matches -- the same walk
// and the same last-line-wins rule MatchLimit uses, on the same
// harness-authored scan text a limit scan reads.
//
// Until is parseReset(line, now) when that succeeds and now.Add(fallback)
// otherwise, exactly as for a limit: an outage that names no reset is still a
// transient provider fault, so it gates for the configured default rather than
// until a human clears it. Thinking lines are skipped, because a model
// reasoning about an outage is not hitting one.
//
// ok is false when no line matches, which is the caller's signal that this exit
// is not a provider outage and the existing until-cleared classification stands.
// Never errors, never panics.
func MatchOutage(text string, now time.Time, fallback time.Duration) (OutageMatch, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if transcript.IsThinking(line) {
			continue
		}
		if !matchesAny(line, outagePatterns) {
			continue
		}
		capped := capLine(line)
		until, parsed := parseReset(capped, now)
		if !parsed {
			until = now.Add(fallback)
		}
		return OutageMatch{Line: capped, Until: until.UTC(), Parsed: parsed}, true
	}
	return OutageMatch{}, false
}

// compileOutage compiles the fixed table once, case-insensitively the way the
// harness patterns are written. A pattern that somehow does not compile is
// skipped rather than panicking; the table is a literal, so this cannot fire in
// production, and it keeps the init total.
func compileOutage(patterns ...string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(`(?i)` + p)
		if err != nil {
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}
