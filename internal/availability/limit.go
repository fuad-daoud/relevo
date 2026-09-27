package availability

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// LimitScanLines is how many trailing lines of a builder's output a decision
// point scans for rate-limit text.
const LimitScanLines = 40

// LimitMatch is one rate-limit pattern match against a builder's output.
type LimitMatch struct {
	Line   string    // the matched line, trimmed, capped at 200 runes
	Until  time.Time // gate end, UTC
	Parsed bool      // Until came from Line, not from the fallback
}

// durationRe matches "resets in 2h48m52s", "try again in 5 min", "resets in
// 1d 5h", "retry after 30s" and captures the duration component run.
var durationRe = regexp.MustCompile(`(?i)(?:resets?|try again|retry)\s+(?:in|after)\s+~?((?:\d+\s*(?:days?|d|hours?|hr|h|minutes?|min|m|seconds?|sec|s)\s*)+)`)

// durationComponentRe pulls one "<number><unit>" component at a time out of the
// captured run above. Units are checked by first letter (h/m/s/d), so the
// alternation only needs to avoid a short form swallowing a longer one.
var durationComponentRe = regexp.MustCompile(`(?i)(\d+)\s*(days?|d|hours?|hr|h|minutes?|min|m|seconds?|sec|s)`)

// clockRe matches "resets 7pm", "resets at 23:30", "resets ~00:26".
var clockRe = regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?~?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?`)

// dateRe matches the absolute-date form codex's weekly limit uses: "try again
// at Oct 19th, 2026 7:14 PM", "resets on October 3, 2026". The capture groups
// are, in order: month, day, year (may be empty), hour, minute and am/pm (each
// may be empty when the line names no time).
var dateRe = regexp.MustCompile(`(?i)(?:try again|resets?)\s+(?:at|on)\s+~?(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:\s*,?\s*(\d{4}))?(?:\s*,?\s*(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?)?`)

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
// when no line matches or patterns is empty. Thinking lines are skipped, because
// a model reasoning about a limit is not hitting one. Never errors.
func MatchLimit(text string, patterns []*regexp.Regexp, now time.Time, fallback time.Duration) (LimitMatch, bool) {
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
		until, parsed := parseReset(capped, now)
		if !parsed {
			until = now.Add(fallback)
		}
		return LimitMatch{Line: capped, Until: until.UTC(), Parsed: parsed}, true
	}
	return LimitMatch{}, false
}

// matchesAny reports whether any pattern matches line.
func matchesAny(line string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

// capLine trims line and caps it at 200 runes.
func capLine(line string) string {
	runes := []rune(strings.TrimSpace(line))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}

// parseReset tries three forms, in this order, on the matched line only: a
// duration ("resets in 2h48m52s"), an absolute date ("try again at Oct 19th,
// 2026 7:14 PM"), and a clock time ("resets 7pm", "resets at 23:30"). A duration
// and a clock time must lie in (now, now+7d]; a date that names a year may lie
// in (now, now+31d], because a full date is hard to misread. Anything else is
// ok=false -- garbage in a line that happened to match the limit pattern must
// not gate a provider indefinitely. The returned time is UTC.
func parseReset(line string, now time.Time) (time.Time, bool) {
	if t, ok := parseDurationReset(line, now); ok {
		return t, true
	}
	if t, ok := parseDateReset(line, now); ok {
		return t, true
	}
	return parseClockReset(line, now)
}

// parseDurationReset handles the "resets in <run>" form.
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
	return windowedReset(now.Add(d), now, limitWindowShort)
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

// parseClockReset handles the clock-time form.
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
		t = t.Add(24 * time.Hour)
	}
	return windowedReset(t, now, limitWindowShort)
}

// parseClockParts parses the optional hour, minute and am/pm groups of the date
// form. An empty hour means the line named no time: midnight, with ok true.
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

// applyMeridiem folds an am/pm suffix into a 24-hour hour. An empty suffix
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

// monthFromName resolves a month name (or its three-letter prefix) to its
// time.Month.
func monthFromName(name string) (time.Month, bool) {
	key := strings.ToLower(name)
	if len(key) > 3 {
		key = key[:3]
	}
	m, ok := monthByName[key]
	return m, ok
}

// windowedReset bounds t to (now, now+window] and returns it in UTC, or ok false
// when it falls outside.
func windowedReset(t, now time.Time, window time.Duration) (time.Time, bool) {
	if !inLimitWindow(t, now, window) {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// limitWindowShort bounds the vaguer reset forms -- a duration and a clock time,
// both easy to misread -- to (now, now+7d].
const limitWindowShort = 7 * 24 * time.Hour

// limitWindowDated bounds a reset that names a full date with a year: a full
// calendar date is hard to misread, so it earns the longer window.
const limitWindowDated = 31 * 24 * time.Hour

// inLimitWindow is parseReset's (now, now+max] bound.
func inLimitWindow(t, now time.Time, max time.Duration) bool {
	return t.After(now) && !t.After(now.Add(max))
}

// LimitPatterns is the harness defaults for token's kind followed by the
// candidate's own limit_patterns, each compiled. When token does not resolve to
// a configured candidate (the candidate was edited or deleted after the binding
// picked it), the harness's patterns alone stand in -- the candidate is gone, so
// it has no patterns of its own. Compiled on each call; decision points fire at
// most once per round, so caching buys nothing.
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
