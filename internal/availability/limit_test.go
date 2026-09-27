package availability

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

func testNow() time.Time {
	return time.Date(2026, 9, 13, 23, 13, 0, 0, time.FixedZone("EEST", 3*3600))
}

// parseResetNow is the clock every parseReset row is stated against, and
// parseResetLoc its location.
var (
	parseResetNow = testNow()
	parseResetLoc = parseResetNow.Location()
	// parseResetDatedNow is the fixed instant the absolute-date rows are
	// stated against: 2026-09-23 12:00 in the same location as every row.
	parseResetDatedNow = time.Date(2026, 9, 23, 12, 0, 0, 0, parseResetLoc)
)

// parseResetCases is TestParseReset's table: one reset line per row, and the
// instant (or refusal) parseReset reads out of it.
var parseResetCases = []struct {
	name string
	line string
	now  time.Time // zero means the shared parseResetNow
	want time.Time
	ok   bool
}{
	{
		name: "agy fixture duration",
		line: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.",
		want: parseResetNow.Add(2*time.Hour + 48*time.Minute + 52*time.Second),
		ok:   true,
	},
	{
		name: "clock 7pm rolls to tomorrow",
		line: "limit · resets 7pm",
		want: time.Date(2026, 9, 14, 19, 0, 0, 0, parseResetLoc),
		ok:   true,
	},
	{
		name: "clock tilde minute rolls to tomorrow",
		line: "individual quota reached (resets ~00:26)",
		want: time.Date(2026, 9, 14, 0, 26, 0, 0, parseResetLoc),
		ok:   true,
	},
	{
		name: "clock at today",
		line: "Resets at 23:30",
		want: time.Date(2026, 9, 13, 23, 30, 0, 0, parseResetLoc),
		ok:   true,
	},
	{
		name: "long duration within 7 days",
		line: "error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 95h4m16s.",
		want: parseResetNow.Add(95*time.Hour + 4*time.Minute + 16*time.Second),
		ok:   true,
	},
	{
		name: "weekly limit resets in a day and hours",
		line: "Error 429: You have reached your weekly ExamplePass limit. The limit resets in 1d 5h, please try again later.",
		want: parseResetNow.Add(29 * time.Hour),
		ok:   true,
	},
	{
		name: "day component past the short window",
		line: "try again in 8d",
		ok:   false,
	},
	{
		name: "day count that would overflow the duration",
		line: "The limit resets in 123456789012345d.",
		ok:   false,
	},
	{
		name: "hour count that would overflow the duration",
		line: "resets in 99999999999999h",
		ok:   false,
	},
	{
		name: "try again in minutes",
		line: "try again in 5 min",
		want: parseResetNow.Add(5 * time.Minute),
		ok:   true,
	},
	{
		name: "retry after seconds",
		line: "retry after 30s",
		want: parseResetNow.Add(30 * time.Second),
		ok:   true,
	},
	{
		// The real codex weekly-limit sentence; the date form carries a
		// year, so it earns the 31-day window.
		name: "codex date form with a year and a time",
		line: "You've hit your usage limit. … or try again at Oct 19th, 2026 7:14 PM.",
		now:  parseResetDatedNow,
		want: time.Date(2026, 10, 19, 19, 14, 0, 0, parseResetLoc),
		ok:   true,
	},
	{
		name: "date form with a full month name and no time",
		line: "resets on October 3, 2026",
		now:  parseResetDatedNow,
		want: time.Date(2026, 10, 3, 0, 0, 0, 0, parseResetLoc),
		ok:   true,
	},
	{
		// No year: the date is read in now's year and keeps the 7-day
		// window. A trailing "9am" is not an H:MM time, so the day reads
		// as 00:00 -- the earliest reading, which never over-gates.
		name: "date form with no year stays inside the short window",
		line: "try again at Sept 30th 9am",
		now:  parseResetDatedNow,
		want: time.Date(2026, 9, 30, 0, 0, 0, 0, parseResetLoc),
		ok:   true,
	},
	{
		name: "date form with no year past the short window",
		line: "try again at Nov 1st 9am",
		now:  parseResetDatedNow,
		ok:   false,
	},
	{
		name: "date form past the dated window",
		line: "try again at Oct 19th, 2027 7:14 PM",
		now:  parseResetDatedNow,
		ok:   false,
	},
	{
		name: "date form in the past",
		line: "try again at Sep 1st, 2026",
		now:  parseResetDatedNow,
		ok:   false,
	},
	{
		name: "date form on a day that does not exist",
		line: "try again at Feb 30th, 2027",
		now:  parseResetDatedNow,
		ok:   false,
	},
	{
		name: "date form with pm on an hour past 12",
		line: "try again at Oct 19th, 2026 13:14 PM",
		now:  parseResetDatedNow,
		ok:   false,
	},
	{
		name: "no reset time at all",
		line: "You've hit your limit",
		ok:   false,
	},
	{
		name: "duration outside 7 day window",
		line: "try again in 400h",
		ok:   false,
	},
	{
		name: "clock out of range",
		line: "resets 99:99",
		ok:   false,
	},
}

func TestParseReset(t *testing.T) {
	t.Parallel()

	for _, c := range parseResetCases {
		t.Run(c.name, func(t *testing.T) {
			n := c.now
			if n.IsZero() {
				n = parseResetNow
			}
			got, ok := parseReset(c.line, n)
			if ok != c.ok {
				t.Fatalf("parseReset(%q) ok = %v, want %v", c.line, ok, c.ok)
			}
			if !c.ok {
				return
			}
			if !got.Equal(c.want) {
				t.Errorf("parseReset(%q) = %v, want %v", c.line, got, c.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("parseReset(%q) location = %v, want UTC", c.line, got.Location())
			}
		})
	}
}

func agyPatterns(t *testing.T) []*regexp.Regexp {
	t.Helper()
	h, ok := harness.Lookup("agy")
	if !ok {
		t.Fatal(`harness.Lookup("agy") not found`)
	}
	patterns := make([]*regexp.Regexp, 0, len(h.LimitPatterns))
	for _, p := range h.LimitPatterns {
		patterns = append(patterns, regexp.MustCompile(p))
	}
	return patterns
}

// codexPatterns is the codex kind's shipped limit patterns, compiled the
// same way agyPatterns builds agy's.
func codexPatterns(t *testing.T) []*regexp.Regexp {
	t.Helper()
	h, ok := harness.Lookup("codex")
	if !ok {
		t.Fatal(`harness.Lookup("codex") not found`)
	}
	patterns := make([]*regexp.Regexp, 0, len(h.LimitPatterns))
	for _, p := range h.LimitPatterns {
		patterns = append(patterns, regexp.MustCompile(p))
	}
	return patterns
}

// opencodePatterns is the opencode kind's shipped limit patterns, compiled the
// same way agyPatterns builds agy's.
func opencodePatterns(t *testing.T) []*regexp.Regexp {
	t.Helper()
	h, ok := harness.Lookup("opencode")
	if !ok {
		t.Fatal(`harness.Lookup("opencode") not found`)
	}
	patterns := make([]*regexp.Regexp, 0, len(h.LimitPatterns))
	for _, p := range h.LimitPatterns {
		patterns = append(patterns, regexp.MustCompile(p))
	}
	return patterns
}

// TestAgyLimitDetectedInRenderedStream: the
// 7 real agy ERROR results render with their result.error line, and a scan of
// those rendered lines finds the 5 real limits and neither of the 2
// non-limit errors. The fixture lives in the transcript package and is read
// by relative path, so both packages scan the same bytes.
func TestAgyLimitDetectedInRenderedStream(t *testing.T) {
	t.Parallel()

	now := testNow()
	raw, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", "agy-errors", "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("fixture has %d lines, want 7", len(lines))
	}
	patterns := agyPatterns(t)
	for i, line := range lines {
		rendered := strings.Join(transcript.Render("agy", []byte(line)), "\n")
		_, ok := MatchLimit(rendered, patterns, now, 0)
		if want := i < 5; ok != want {
			t.Errorf("line %d: rendered stream matches = %v, want %v; rendered:\n%s", i+1, ok, want, rendered)
		}
	}
}

// TestOpencodeLimitDetectedInRenderedStream: the two real provider failures --
// a weekly 429 and an out-of-credits 402 -- render with their error.message
// line, and a scan of the rendered stream finds both, while a provider error
// that is not a limit renders and matches nothing. The first row's exact
// rendered text is pinned so a renderer change fails here instead of silently
// dropping the signal. The fixture lives in the transcript package and is read
// by relative path, so both packages scan the same bytes.
func TestOpencodeLimitDetectedInRenderedStream(t *testing.T) {
	t.Parallel()

	now := testNow()
	const fallback = time.Hour
	raw, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", "opencode-errors", "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("fixture has %d lines, want 3", len(lines))
	}
	patterns := opencodePatterns(t)
	want := []struct {
		ok     bool
		parsed bool
		until  time.Time
	}{
		{true, true, now.Add(23 * time.Minute)},
		{true, false, now.Add(fallback)},
		{false, false, time.Time{}},
	}
	for i, line := range lines {
		rendered := strings.Join(transcript.Render("opencode", []byte(line)), "\n")
		if i == 0 {
			const wantRendered = "  ⎿ error: Error 429: You have reached your weekly ExamplePass limit. The limit resets in 23m, please try again later."
			if rendered != wantRendered {
				t.Errorf("row 1 rendered = %q, want %q", rendered, wantRendered)
			}
		}
		m, ok := MatchLimit(rendered, patterns, now, fallback)
		if ok != want[i].ok {
			t.Errorf("row %d: matches = %v, want %v; rendered:\n%s", i+1, ok, want[i].ok, rendered)
			continue
		}
		if !ok {
			continue
		}
		if m.Parsed != want[i].parsed {
			t.Errorf("row %d: Parsed = %v, want %v", i+1, m.Parsed, want[i].parsed)
		}
		if !m.Until.Equal(want[i].until) {
			t.Errorf("row %d: Until = %v, want %v", i+1, m.Until, want[i].until)
		}
	}
}

func TestMatchLimitLastLineWins(t *testing.T) {
	t.Parallel()

	text := "individual quota reached: first\njust chatting\nindividual quota reached: last"
	got, ok := MatchLimit(text, agyPatterns(t), testNow(), time.Hour)
	if !ok {
		t.Fatal("MatchLimit ok = false, want true")
	}
	if want := "individual quota reached: last"; got.Line != want {
		t.Errorf("Line = %q, want %q", got.Line, want)
	}
}

func TestMatchLimitAgyFixtureParsesReset(t *testing.T) {
	t.Parallel()

	now := testNow()
	text := "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s."
	got, ok := MatchLimit(text, agyPatterns(t), now, time.Hour)
	if !ok {
		t.Fatal("MatchLimit ok = false, want true")
	}
	if !got.Parsed {
		t.Error("Parsed = false, want true")
	}
	want := now.Add(2*time.Hour + 48*time.Minute + 52*time.Second)
	if !got.Until.Equal(want) {
		t.Errorf("Until = %v, want %v", got.Until, want)
	}
}

func TestMatchLimitNoResetUsesFallback(t *testing.T) {
	t.Parallel()

	now := testNow()
	fallback := time.Hour
	got, ok := MatchLimit("individual quota reached", agyPatterns(t), now, fallback)
	if !ok {
		t.Fatal("MatchLimit ok = false, want true")
	}
	if got.Parsed {
		t.Error("Parsed = true, want false")
	}
	if want := now.Add(fallback); !got.Until.Equal(want) {
		t.Errorf("Until = %v, want %v", got.Until, want)
	}
}

func TestMatchLimitCodexDateParsesReset(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.FixedZone("EEST", 3*3600))
	text := "You've hit your usage limit. … or try again at Oct 19th, 2026 7:14 PM."
	got, ok := MatchLimit(text, codexPatterns(t), now, time.Hour)
	if !ok {
		t.Fatal("MatchLimit ok = false, want true")
	}
	if !got.Parsed {
		t.Error("Parsed = false, want true")
	}
	want := time.Date(2026, 10, 19, 19, 14, 0, 0, now.Location())
	if !got.Until.Equal(want) {
		t.Errorf("Until = %v, want %v", got.Until, want)
	}
}

func TestMatchLimitNoLineMatches(t *testing.T) {
	t.Parallel()

	text := "starting\nboom: out of tokens\nrelevo-exit:3"
	if _, ok := MatchLimit(text, agyPatterns(t), testNow(), time.Hour); ok {
		t.Error("ok = true, want false")
	}
}

func TestMatchLimitNilPatternsNeverMatch(t *testing.T) {
	t.Parallel()

	text := "Individual quota reached. Resets in 2h48m52s."
	if _, ok := MatchLimit(text, nil, testNow(), time.Hour); ok {
		t.Error("ok = true, want false")
	}
}

func TestMatchLimitCapsMatchedLineAt200Runes(t *testing.T) {
	t.Parallel()

	filler := ""
	for len(filler) < 300 {
		filler += "x"
	}
	got, ok := MatchLimit("individual quota reached "+filler, agyPatterns(t), testNow(), time.Hour)
	if !ok {
		t.Fatal("MatchLimit ok = false, want true")
	}
	if n := len([]rune(got.Line)); n != 200 {
		t.Errorf("len([]rune(Line)) = %d, want 200", n)
	}
}
