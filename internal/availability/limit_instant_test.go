package availability

import (
	"regexp"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// musePatterns is the muse kind's shipped limit patterns, compiled the same
// way agyPatterns builds agy's.
func musePatterns(t *testing.T) []*regexp.Regexp {
	t.Helper()
	h, ok := harness.Lookup("muse")
	if !ok {
		t.Fatal(`harness.Lookup("muse") not found`)
	}
	patterns := make([]*regexp.Regexp, 0, len(h.LimitPatterns))
	for _, p := range h.LimitPatterns {
		patterns = append(patterns, regexp.MustCompile(p))
	}
	return patterns
}

// museIncidentLine is the quota sentence a failed muse run ends with: the
// 429, the exhausted subscription quota, and the usage window's reset
// instant on one line.
const museIncidentLine = "run ended with Failed: API error 429 [request_id=80df0843-9f2c-4b1e-8a5d-6c7e0f1a2b3c]: Subscription quota exhausted. Your usage window resets at 2026-10-24T13:22:58Z. (rate_limit_error)"

var (
	museIncidentNow   = time.Date(2026, 10, 24, 11, 3, 33, 0, time.UTC)
	museIncidentReset = time.Date(2026, 10, 24, 13, 22, 58, 0, time.UTC)
)

type museInstantCase struct {
	name string
	line string
	now  time.Time
	want time.Time
	ok   bool
}

func museInstantResetCases() []museInstantCase {
	return []museInstantCase{
		{
			name: "future instant with the guard markers",
			line: museIncidentLine,
			now:  museIncidentNow,
			want: museIncidentReset,
			ok:   true,
		},
		{
			name: "offset instant reads as the same moment",
			line: "Subscription quota exhausted. Your usage window resets at 2026-10-24T16:22:58+03:00. (rate_limit_error)",
			now:  museIncidentNow,
			want: museIncidentReset,
			ok:   true,
		},
		{
			name: "fractional instant keeps its sub-second part",
			line: "Subscription quota exhausted. Your usage window resets at 2026-10-24T13:22:58.5Z. (rate_limit_error)",
			now:  museIncidentNow,
			want: time.Date(2026, 10, 24, 13, 22, 58, 500000000, time.UTC),
			ok:   true,
		},
		{
			name: "instant at now is already lifting",
			line: "Subscription quota exhausted. Your usage window resets at 2026-10-24T11:03:33Z. (rate_limit_error)",
			now:  museIncidentNow,
			want: museIncidentNow,
			ok:   true,
		},
		{
			name: "past instant reads as now",
			line: museIncidentLine,
			now:  museIncidentNow.Add(3 * time.Hour),
			want: museIncidentNow.Add(3 * time.Hour),
			ok:   true,
		},
		{
			name: "instant past the dated window refuses",
			line: "Subscription quota exhausted. Your usage window resets at 2026-12-24T13:22:58Z. (rate_limit_error)",
			now:  museIncidentNow,
			ok:   false,
		},
		{
			name: "unguarded ISO timestamp is not a reset",
			line: "deploy finished at 2026-10-24T13:22:58Z, all green",
			now:  museIncidentNow,
			ok:   false,
		},
		{
			name: "guarded line with an impossible date refuses",
			line: "Subscription quota exhausted. Your usage window resets at 2026-13-40T99:99:99Z. (rate_limit_error)",
			now:  museIncidentNow,
			ok:   false,
		},
	}
}

// TestParseInstantResetReadsMuseSentence pins the absolute-instant form: a
// muse-shaped line naming an RFC3339 reset gates at that instant, a lifted
// one reads as now, and anything else refuses.
func TestParseInstantResetReadsMuseSentence(t *testing.T) {
	t.Parallel()

	for _, c := range museInstantResetCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseInstantReset(c.line, c.now)
			if ok != c.ok {
				t.Fatalf("parseInstantReset(%q) ok = %v, want %v", c.line, ok, c.ok)
			}
			if !c.ok {
				return
			}
			if !got.Equal(c.want) {
				t.Errorf("parseInstantReset(%q) = %v, want %v", c.line, got, c.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("parseInstantReset(%q) location = %v, want UTC", c.line, got.Location())
			}
		})
	}
}

// TestInstantPrecedesDurationAndDate pins the precedence: a line naming both
// a prose reset and the muse instant gates at the instant.
func TestInstantPrecedesDurationAndDate(t *testing.T) {
	t.Parallel()

	line := "Subscription quota exhausted, resets in 5m. Your usage window resets at 2026-10-24T13:22:58Z. (rate_limit_error)"
	got, ok := parseReset(line, museIncidentNow)
	if !ok {
		t.Fatal("parseReset ok = false, want the instant parse")
	}
	if !got.Equal(museIncidentReset) {
		t.Errorf("parseReset = %v, want the instant %v, not the 5-minute prose reset", got, museIncidentReset)
	}
}

// TestClockReadsInstantYearAsHour pins the misread the instant form preempts:
// the clock form reads the instant's year as hour 20, so without the instant
// form ahead of it the sentence would gate at 20:00 local.
func TestClockReadsInstantYearAsHour(t *testing.T) {
	t.Parallel()

	now := museIncidentNow.In(time.FixedZone("EEST", 3*3600))
	got, ok := parseClockReset(museIncidentLine, now)
	if !ok {
		t.Fatal("parseClockReset ok = false, want the year-as-hour misread")
	}
	want := time.Date(2026, 10, 24, 20, 0, 0, 0, now.Location())
	if !got.Equal(want) {
		t.Errorf("parseClockReset = %v, want %v", got, want)
	}
	if !got.Equal(time.Date(2026, 10, 24, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("parseClockReset = %v, want 17:00Z: 20:00 local is three hours past the true reset", got)
	}
}

// TestMatchLimitMuseInstantReplaysIncidentReset pins the end-to-end replay:
// the incident sentence matches a shipped muse pattern and gates at the
// sentence's own instant rather than the fallback.
func TestMatchLimitMuseInstantReplaysIncidentReset(t *testing.T) {
	t.Parallel()

	fallback := time.Hour
	got, ok := MatchLimitWithReset(museIncidentLine, musePatterns(t), museIncidentNow, fallback, 0)
	if !ok {
		t.Fatal("MatchLimitWithReset ok = false, want true")
	}
	if !got.Parsed {
		t.Errorf("Parsed = false, want true (Until %v is the %v fallback)", got.Until, fallback)
	}
	if !got.Until.Equal(museIncidentReset) {
		t.Errorf("Until = %v, want the sentence instant %v", got.Until, museIncidentReset)
	}
	if got.Line != museIncidentLine {
		t.Errorf("Line = %q, want the incident sentence", got.Line)
	}
	if n := len([]rune(got.Line)); n > 200 {
		t.Errorf("len([]rune(Line)) = %d, want the capped line at most 200", n)
	}
}

// TestMatchLimitWithResetStructuredBeatsText pins the settled precedence: a
// sane structured instant wins over whatever the matched text line names,
// and gates at that instant with no reset sentence in the text at all.
func TestMatchLimitWithResetStructuredBeatsText(t *testing.T) {
	t.Parallel()

	now := museIncidentNow
	line := "individual quota reached"
	got, ok := MatchLimitWithReset(line, agyPatterns(t), now, time.Hour, museIncidentReset.UnixMilli())
	if !ok {
		t.Fatal("MatchLimitWithReset ok = false, want true")
	}
	if !got.Parsed {
		t.Error("Parsed = false, want true: a sane structured reset is not the fallback")
	}
	want := museIncidentReset
	if !want.After(now) || want.After(now.Add(limitWindowDated)) {
		t.Fatalf("fixture instant %v is outside the test now's dated window", want)
	}
	if !got.Until.Equal(want) {
		t.Errorf("Until = %v, want the structured instant %v", got.Until, want)
	}
	if got.Line != line {
		t.Errorf("Line = %q, want the matched text line", got.Line)
	}
}

// TestMatchLimitWithResetIgnoresInsaneStructured pins the sanity bound: zero,
// a lifted instant and one past the dated window leave the text to decide,
// exactly as a zero resetMS does.
func TestMatchLimitWithResetIgnoresInsaneStructured(t *testing.T) {
	t.Parallel()

	now := museIncidentNow
	line := "Subscription quota exhausted, resets in 5m. (rate_limit_error)"
	want := now.Add(5 * time.Minute)
	for _, c := range []struct {
		name string
		ms   int64
	}{
		{"zero is absent", 0},
		{"lifted instant falls through", museIncidentNow.Add(-time.Minute).UnixMilli()},
		{"instant past the dated window falls through", museIncidentNow.Add(limitWindowDated + time.Hour).UnixMilli()},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, ok := MatchLimitWithReset(line, musePatterns(t), now, time.Hour, c.ms)
			if !ok {
				t.Fatal("MatchLimitWithReset ok = false, want true")
			}
			if !got.Parsed {
				t.Error("Parsed = false, want true: the text names its own reset")
			}
			if !got.Until.Equal(want) {
				t.Errorf("Until = %v, want the text reset %v", got.Until, want)
			}
		})
	}
}

// TestMatchLimitPastInstantExpiresImmediately pins the automatic path's
// clamp: a sentence whose instant already lifted gates until now -- the gate
// expires immediately -- instead of the fallback.
func TestMatchLimitPastInstantExpiresImmediately(t *testing.T) {
	t.Parallel()

	now := museIncidentNow.Add(3 * time.Hour)
	got, ok := MatchLimitWithReset(museIncidentLine, musePatterns(t), now, time.Hour, 0)
	if !ok {
		t.Fatal("MatchLimitWithReset ok = false, want true")
	}
	if !got.Parsed {
		t.Error("Parsed = false, want true: the lifted instant still parses")
	}
	if !got.Until.Equal(now) {
		t.Errorf("Until = %v, want now %v: a lifted reset must not gate", got.Until, now)
	}
}

// TestMatchLimitWithResetInsaneStructuredKeepsFallback pins the last leg: an
// insane structured instant beside a line naming no reset still gates at the
// fallback rather than inventing an expiry.
func TestMatchLimitWithResetInsaneStructuredKeepsFallback(t *testing.T) {
	t.Parallel()

	now := museIncidentNow
	fallback := time.Hour
	got, ok := MatchLimitWithReset("Subscription quota exhausted. (rate_limit_error)", musePatterns(t), now, fallback, 0)
	if !ok {
		t.Fatal("MatchLimitWithReset ok = false, want true")
	}
	if got.Parsed {
		t.Error("Parsed = true, want false")
	}
	if want := now.Add(fallback); !got.Until.Equal(want) {
		t.Errorf("Until = %v, want the %v fallback", got.Until, fallback)
	}
}
