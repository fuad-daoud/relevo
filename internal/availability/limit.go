package availability

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// LimitScanLines is how many trailing lines of a builder's output a decision
// point scans for rate-limit text.
const LimitScanLines = 40

// LimitMatch is one rate-limit pattern match against a builder's output.
type LimitMatch struct {
	Line   string    // the matched line, trimmed, capped at 200 runes
	Until  time.Time // gate end, UTC
	Parsed bool      // Until came from the stream's own reset rather than the fallback
}

// durationRe matches "resets in 2h48m52s" and captures the duration component
// run.
var durationRe = regexp.MustCompile(`(?i)(?:resets?|try again|retry)\s+(?:in|after)\s+~?((?:\d+\s*(?:days?|d|hours?|hr|h|minutes?|min|m|seconds?|sec|s)\s*)+)`)

// durationComponentRe pulls one "<number><unit>" component at a time out of the
// captured run above. Units are checked by first letter (h/m/s/d), so the
// alternation only needs to keep a short form from swallowing a longer one.
var durationComponentRe = regexp.MustCompile(`(?i)(\d+)\s*(days?|d|hours?|hr|h|minutes?|min|m|seconds?|sec|s)`)

// clockRe matches "resets 7pm", "resets at 23:30", "resets ~00:26".
var clockRe = regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?~?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?`)

// dateRe matches the absolute-date form codex's weekly limit uses: "try again
// at Oct 19th, 2026 7:14 PM". The capture groups are, in order: month, day,
// year (may be empty), hour, minute and am/pm (each may be empty when the line
// names no time).
var dateRe = regexp.MustCompile(`(?i)(?:try again|resets?)\s+(?:at|on)\s+~?(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:\s*,?\s*(\d{4}))?(?:\s*,?\s*(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?)?`)

// instantRe matches an absolute RFC3339 reset instant, the form muse's quota
// sentence names: "Your usage window resets at 2026-10-24T13:22:58Z".
var instantRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})`)

// instantGuardRe keeps the instant form to muse-shaped lines, so another
// provider's prose timestamp cannot set a gate: only a line carrying one of
// the markers from muse's own limit rows is read as a muse reset.
var instantGuardRe = regexp.MustCompile(`(?i)usage window|quota|rate_limit_error|API error 429`)

// monthByName maps a month's lower-cased 3-letter prefix to its time.Month, so
// both "sept" and "september" resolve through "sep".
var monthByName = map[string]time.Month{
	"jan": time.January,
	"feb": time.February,
	"mar": time.March,
	"apr": time.April,
	"may": time.May,
	"jun": time.June,
	"jul": time.July,
	"aug": time.August,
	"sep": time.September,
	"oct": time.October,
	"nov": time.November,
	"dec": time.December,
}

// MatchLimit scans text line by line from the last line backwards and returns
// the first (i.e. most recent) line any pattern matches. Until is
// parseReset(line, now) when that succeeds, else now.Add(fallback). ok is false
// when no line matches or patterns is empty. Thinking lines are skipped,
// because a model reasoning about a limit is not hitting one. Never errors.
func MatchLimit(text string, patterns []*regexp.Regexp, now time.Time, fallback time.Duration) (LimitMatch, bool) {
	return MatchLimitWithReset(text, patterns, now, fallback, 0)
}

// MatchLimitWithReset is MatchLimit with a structured resets_at_ms threaded
// in: the millisecond instant a muse stream already carried beside the text
// (a failed run's quota, else an allowance window). When it is sane it wins
// over whatever the matched line names -- Until is that instant and Parsed is
// true -- and when it is absent or outside the dated window the text decides
// exactly as MatchLimit. Never errors.
func MatchLimitWithReset(text string, patterns []*regexp.Regexp, now time.Time, fallback time.Duration, resetMS int64) (LimitMatch, bool) {
	if len(patterns) == 0 {
		return LimitMatch{}, false
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if transcript.IsThinking(line) {
			continue
		}
		if !matchesAny(line, patterns) {
			continue
		}
		capped := capLine(line)
		if until, ok := structuredReset(resetMS, now); ok {
			return LimitMatch{Line: capped, Until: until, Parsed: true}, true
		}
		until, parsed := parseReset(capped, now)
		if !parsed {
			until = now.Add(fallback)
		}
		return LimitMatch{Line: capped, Until: until.UTC(), Parsed: parsed}, true
	}
	return LimitMatch{}, false
}

// structuredReset reads a structured resets_at_ms beside the matched text.
// It is sane -- and the gate's Until -- when it names an instant inside the
// dated window, the bound a reset that states its own day earns; zero (no
// field), a lifted instant and one further out than the window do not parse,
// so the text decides instead. The returned time is UTC.
func structuredReset(ms int64, now time.Time) (time.Time, bool) {
	if ms == 0 {
		return time.Time{}, false
	}
	return windowedReset(time.UnixMilli(ms).UTC(), now, limitWindowDated)
}

// ResetFromReason reports the reset instant a gate reason's own text names, and
// whether it named one at all. It is parseReset applied to an operator- or
// provider-supplied --reason rather than to a builder's output, so the same
// wording that ends a gate when the daemon sees it also ends a gate a human
// records by hand: a reason reading "RESOURCE_EXHAUSTED 429: ... Resets in
// 51m30s" expires at that reset instead of staying until cleared.
//
// ok is false when the reason names no reset this parser trusts -- no reset at
// all, or one outside the window a vaguer form may claim -- and the caller must
// then keep the gate until cleared rather than inventing an expiry no provider
// ever stated. The returned time is UTC.
func ResetFromReason(reason string, now time.Time) (time.Time, bool) {
	return parseReset(sanitize.Text(reason), now)
}

func matchesAny(line string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

func capLine(line string) string {
	runes := []rune(sanitize.Text(strings.TrimSpace(line)))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}

// parseReset tries four forms, in this order, on the matched line only: an
// absolute instant ("resets at 2026-10-24T13:22:58Z"), a duration ("resets in
// 2h48m52s"), an absolute date ("try again at Oct 19th, 2026 7:14 PM"), and a
// clock time ("resets 7pm", "resets at 23:30"). The instant leads because the
// clock form otherwise reads the instant's year as an hour ("2026" as 20:00).
// An instant and a full date may lie in (now, now+31d]; a date naming no year
// and a clock time are the vaguer forms and keep the 7-day window, because
// "7pm" without a day is the one a line can most easily be misread out of.
// Anything outside its window is ok=false -- garbage in a line that happened
// to match the limit pattern must not gate a provider indefinitely. The two
// exceptions read as a reset lifting now: an instant at or before now, and a
// clock time that just passed within clockResetGrace, so a gate recorded at
// its own reset time gates for zero time. The returned time is UTC.
func parseReset(line string, now time.Time) (time.Time, bool) {
	if t, ok := parseInstantReset(line, now); ok {
		return t, true
	}
	if t, ok := parseDurationReset(line, now); ok {
		return t, true
	}
	if t, ok := parseDateReset(line, now); ok {
		return t, true
	}
	return parseClockReset(line, now)
}

// parseInstantReset handles the absolute-instant form, guarded to muse-shaped
// lines: another provider's prose timestamp must not set a gate. A future
// instant earns the dated window; one at or before now is a reset that already
// lifted and reads as now, so the gate it feeds expires immediately.
func parseInstantReset(line string, now time.Time) (time.Time, bool) {
	if !instantGuardRe.MatchString(line) {
		return time.Time{}, false
	}
	m := instantRe.FindString(line)
	if m == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, m)
	if err != nil {
		return time.Time{}, false
	}
	if !t.After(now) {
		return now.UTC(), true
	}
	return windowedReset(t, now, limitWindowDated)
}

func parseDurationReset(line string, now time.Time) (time.Time, bool) {
	m := durationRe.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, false
	}
	var d time.Duration
	for _, c := range durationComponentRe.FindAllStringSubmatch(m[1], -1) {
		n, err := strconv.Atoi(c[1])
		if err != nil {
			continue
		}
		var unit time.Duration
		switch u := strings.ToLower(c[2]); u[0] {
		case 'd':
			unit = 24 * time.Hour
		case 'h':
			unit = time.Hour
		case 'm':
			unit = time.Minute
		case 's':
			unit = time.Second
		default:
			continue
		}
		// Refuse a count no window could accept before multiplying it: a
		// large number wraps int64 nanoseconds into a small value that
		// could land inside the window and gate on a nonsense reset.
		if n > int(limitWindowDated/unit) {
			return time.Time{}, false
		}
		d += time.Duration(n) * unit
		if d > limitWindowDated {
			return time.Time{}, false
		}
	}
	// A duration earns the dated window, not the short one: a monthly
	// subscription limit states its own reset in days, and "16d 5h" is that
	// statement rather than a misread. The overflow guard above already
	// refused a count no window could accept, so nothing here gates a provider
	// on a wrapped nanosecond count.
	return windowedReset(now.Add(d), now, limitWindowDated)
}

// parseDateReset handles the absolute-date form, which may or may not name a
// year and a time of day.
func parseDateReset(line string, now time.Time) (time.Time, bool) {
	m := dateRe.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, false
	}
	month, ok := monthFromName(m[1])
	if !ok {
		return time.Time{}, false
	}
	day, err := strconv.Atoi(m[2])
	if err != nil || day < 1 || day > 31 {
		return time.Time{}, false
	}
	hour, minute, ok := parseClockParts(m[4], m[5], m[6])
	if !ok {
		return time.Time{}, false
	}
	return dateReset(now, month, day, m[3], hour, minute)
}

// dateReset builds the reset instant for the date form. A named year earns the
// longer window; a yearless date that has already passed rolls to next year.
func dateReset(now time.Time, month time.Month, day int, yearStr string, hour, minute int) (time.Time, bool) {
	loc := now.Location() // codex prints the machine's local time
	if yearStr != "" {
		year, err := strconv.Atoi(yearStr)
		if err != nil {
			return time.Time{}, false
		}
		t := time.Date(year, month, day, hour, minute, 0, 0, loc)
		if t.Day() != day {
			return time.Time{}, false
		}
		return windowedReset(t, now, limitWindowDated)
	}

	t := time.Date(now.Year(), month, day, hour, minute, 0, 0, loc)
	if t.Day() != day {
		return time.Time{}, false
	}
	if !t.After(now) {
		t = time.Date(now.Year()+1, month, day, hour, minute, 0, 0, loc)
		if t.Day() != day {
			return time.Time{}, false
		}
	}
	return windowedReset(t, now, limitWindowShort)
}

// parseClockReset handles the clock-time form. A clock time in the past
// within clockResetGrace reads as a reset lifting now; further back it rolls
// to tomorrow.
func parseClockReset(line string, now time.Time) (time.Time, bool) {
	m := clockRe.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil {
		return time.Time{}, false
	}
	minute := 0
	if m[2] != "" {
		if minute, err = strconv.Atoi(m[2]); err != nil {
			return time.Time{}, false
		}
	}
	hour, ok := applyMeridiem(hour, m[3])
	if !ok || minute < 0 || minute > 59 {
		return time.Time{}, false
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !t.After(now) {
		// A clock time within the grace in the past is a reset that just
		// lifted: the gate this feeds ends at now, gating for zero time,
		// instead of a day away. Further back the line is stale, and the
		// roll to tomorrow stands.
		if now.Sub(t) <= clockResetGrace {
			return now.UTC(), true
		}
		t = t.Add(24 * time.Hour)
	}
	return windowedReset(t, now, limitWindowShort)
}

// parseClockParts parses the date form's optional time groups, defaulting a
// missing time to midnight.
func parseClockParts(hh, mm, ampm string) (hour, minute int, ok bool) {
	if hh == "" {
		return 0, 0, true
	}
	hour, err := strconv.Atoi(hh)
	if err != nil {
		return 0, 0, false
	}
	if minute, err = strconv.Atoi(mm); err != nil || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	hour, ok = applyMeridiem(hour, ampm)
	if !ok {
		return 0, 0, false
	}
	return hour, minute, true
}

// applyMeridiem folds an am/pm suffix into a 24-hour hour; an empty suffix
// requires an already-24-hour hour.
func applyMeridiem(hour int, ampm string) (int, bool) {
	switch strings.ToLower(ampm) {
	case "am":
		if hour < 1 || hour > 12 {
			return 0, false
		}
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour < 1 || hour > 12 {
			return 0, false
		}
		if hour != 12 {
			hour += 12
		}
	default:
		if hour < 0 || hour > 23 {
			return 0, false
		}
	}
	return hour, true
}

func monthFromName(name string) (time.Month, bool) {
	key := strings.ToLower(name)
	if len(key) > 3 {
		key = key[:3]
	}
	m, ok := monthByName[key]
	return m, ok
}

func windowedReset(t, now time.Time, window time.Duration) (time.Time, bool) {
	if !inLimitWindow(t, now, window) {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// limitWindowShort bounds the vaguer reset forms -- a clock time, and a date
// naming no year -- to (now, now+7d], because neither says which day it means.
const limitWindowShort = 7 * 24 * time.Hour

// limitWindowDated bounds the reset forms that carry their own day: a duration
// ("16d 5h") and a full calendar date with a year. Both say how far out they
// are, so a monthly quota may state a reset two weeks out without the parser
// discarding what it just read.
const limitWindowDated = 31 * 24 * time.Hour

// clockResetGrace is how long past a clock-time reset the parser still reads
// the reset as lifting now rather than rolling to tomorrow. A limit whose
// reset just passed is lifting as the scan reads it, so gating the provider
// until the same clock time tomorrow would hide a usable provider for a day;
// an hour outlasts the gap between a provider printing the line and relevo
// scanning it. A clock time further back than that is a stale line, and the
// roll to tomorrow stands.
const clockResetGrace = time.Hour

func inLimitWindow(t, now time.Time, max time.Duration) bool {
	return t.After(now) && !t.After(now.Add(max))
}

// LimitPatterns is the harness defaults for token's kind followed by the
// candidate's own limit_patterns, each compiled. When token does not resolve to
// a configured candidate (the candidate was edited or deleted after the binding
// picked it), the harness's patterns alone stand in -- the candidate is gone,
// so it has no patterns of its own. Compiled on each call; decision points fire
// at most once per round, so caching buys nothing.
//
// A candidate pattern that fails to compile here cannot happen -- the set's
// Load already refused the file -- so a pattern that somehow doesn't compile is
// skipped defensively rather than panicking.
func LimitPatterns(d Deps, token string) []*regexp.Regexp {
	if d.Candidates == nil {
		return nil
	}
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return nil
	}

	var raw []string
	c, err := d.Candidates.Lookup(ref)
	if err != nil {
		// The candidate is gone: fall back to the harness's own patterns.
		if h, ok := harness.Lookup(ref.Harness); ok {
			raw = append(raw, h.LimitPatterns...)
		}
	} else {
		if h, ok := harness.Lookup(c.Harness); ok {
			raw = append(raw, h.LimitPatterns...)
		}
		raw = append(raw, c.LimitPatterns...)
	}

	var compiled []*regexp.Regexp
	for _, p := range raw {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}
