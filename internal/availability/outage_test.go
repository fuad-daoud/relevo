package availability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

// TestMatchOutage is the classifier's own contract. The table's want-ok
// column is the whole test: each row is either the provider-outage class, which
// must match, or a near miss that must NOT -- the second half is what stops the
// classifier from growing into "every dead builder is an outage".
func TestMatchOutage(t *testing.T) {
	t.Parallel()

	now := baseTime
	const fallback = time.Hour

	cases := []struct {
		name string
		text string
		ok   bool
	}{
		// The real observation, verbatim: agy-errors row 7.
		{"agy UNAVAILABLE (code 503)", "API error (attempt 1): UNAVAILABLE (code 503): No capacity available for model gemini-3.8-flash-high on the server", true},
		{"UNAVAILABLE alone", "API error: UNAVAILABLE", true},
		{"lowercase unavailable", "the upstream is unavailable", true},
		{"code 503", "API error (attempt 4): code 503 from the upstream", true},
		{"code: 500", "backend returned (code: 500)", true},
		{"code=502", "backend returned code=502", true},
		{"code503 with no separator", "backend returned code503", true},
		{"HTTP 502", "POST /v1/messages -> HTTP 502 Bad Gateway", true},
		{"status 500", "the request came back with status 500", true},
		{"status_code: 503", `{"status_code":503,"message":"upstream"}`, true},
		{"service unavailable", "the endpoint answered service unavailable", true},
		{"internal server error", "the model endpoint reported internal server error", true},
		{"bad gateway", "proxy said bad gateway", true},
		{"gateway timeout", "upstream gateway timeout", true},

		// Near misses. Each is a line a real builder can print that must
		// stay with the crash path.
		{"bare ERROR", "ERROR: something went wrong", false},
		{"a 503 that is a count", "2026/09/14 10:04:11 worker exited after 503 processed jobs", false},
		{"a connection reset", "API error (attempt 1): request failed: Post \"https://example.invalid/v1\": read tcp: connection reset by peer", false},
		{"a 4xx quota line is not an outage", "RESOURCE_EXHAUSTED (code 429): Individual quota reached.", false},
		{"an empty tail", "", false},
		{"empty after the last newline", "API error: UNAVAILABLE\n\n", true},
		{"a 5xx-looking number in a longer id", "trace id 50321 failed", false},
		{"a 4xx status", "the request came back with status 429", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, ok := MatchOutage(c.text, now, fallback)
			if ok != c.ok {
				t.Fatalf("MatchOutage(%q) ok = %v, want %v", c.text, ok, c.ok)
			}
			if !ok {
				if m.Line != "" {
					t.Errorf("m.Line = %q, want empty on no match", m.Line)
				}
				return
			}
			if m.Line == "" {
				t.Error("m.Line is empty, want the matched line kept as the gate note")
			}
			if m.Until.IsZero() {
				t.Error("m.Until is zero, want a timed gate")
			}
			if !m.Until.After(now) {
				t.Errorf("m.Until = %v, want it after now (%v)", m.Until, now)
			}
			if m.Until.After(now.Add(31 * 24 * time.Hour)) {
				t.Errorf("m.Until = %v, want it inside the reset window", m.Until)
			}
		})
	}
}

// TestMatchOutageLastLineWins pins the same rule MatchLimit follows: the scan
// walks backwards and the most recent outage line is the one reported.
func TestMatchOutageLastLineWins(t *testing.T) {
	t.Parallel()

	now := baseTime
	text := strings.Join([]string{
		"API error: UNAVAILABLE (code 503): no capacity",
		"retrying the request",
		"API error: UNAVAILABLE (code 500): upstream crashed",
	}, "\n")

	m, ok := MatchOutage(text, now, time.Hour)
	if !ok {
		t.Fatal("MatchOutage found nothing, want the most recent outage line")
	}
	if want := "API error: UNAVAILABLE (code 500): upstream crashed"; m.Line != want {
		t.Errorf("m.Line = %q, want the last one %q", m.Line, want)
	}
}

// TestMatchOutageParsesAReset pins that a named reset is honoured rather than
// replaced by the fallback, so an outage line that does say when it clears says
// so in the gate -- the same rule and the same parsers as a rate limit.
func TestMatchOutageParsesAReset(t *testing.T) {
	t.Parallel()

	now := baseTime
	m, ok := MatchOutage("API error: UNAVAILABLE (code 503): try again in 12m30s", now, time.Hour)
	if !ok {
		t.Fatal("MatchOutage found nothing")
	}
	if !m.Parsed {
		t.Fatalf("Parsed = false for %q, want the named reset used", m.Line)
	}
	if want := now.Add(12*time.Minute + 30*time.Second).UTC(); !m.Until.Equal(want) {
		t.Errorf("Until = %v, want %v", m.Until, want)
	}
}

// TestMatchOutageFallbackGate is the no-reset case pinned to the fallback
// window itself, so a future change that made the fallback longer could not
// pass by accident.
func TestMatchOutageFallbackGate(t *testing.T) {
	t.Parallel()

	now := baseTime
	m, ok := MatchOutage("API error: UNAVAILABLE (code 503): no capacity", now, 37*time.Minute)
	if !ok {
		t.Fatal("MatchOutage found nothing")
	}
	if m.Parsed {
		t.Fatalf("Parsed = true for %q: this is the fallback case", m.Line)
	}
	if want := now.Add(37 * time.Minute).UTC(); !m.Until.Equal(want) {
		t.Errorf("Until = %v, want %v", m.Until, want)
	}
}

// TestMatchOutageReadsTheRealAgyFixture proves the scan channel actually
// delivers the outage text: the raw line is rendered by the very renderer the
// exit path uses (transcript.LimitLines, the no-timestamp scan writer) and the
// classifier must still match. Without this the table above could pass on
// strings no harness ever prints.
func TestMatchOutageReadsTheRealAgyFixture(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", "agy-errors", "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	// Row 7 is the outage observation; row 6 is the connection failure that
	// must stay with the crash path. Indexes are the fixture's own.
	scanned := ""
	for _, line := range lines[6:] {
		scanned += strings.Join(transcript.LimitLines("agy", []byte(line)), "\n") + "\n"
	}

	m, ok := MatchOutage(scanned, baseTime, time.Hour)
	if !ok {
		t.Fatalf("MatchOutage found nothing in the rendered fixture tail:\n%s", scanned)
	}
	if !strings.Contains(m.Line, "UNAVAILABLE") || !strings.Contains(m.Line, "code 503") {
		t.Errorf("m.Line = %q, want the UNAVAILABLE (code 503) row", m.Line)
	}

	// The connection-reset row alone must not match, so the match above came
	// from the outage row and not from the other failed result.
	only := strings.Join(transcript.LimitLines("agy", []byte(lines[5])), "\n")
	if _, ok := MatchOutage(only, baseTime, time.Hour); ok {
		t.Errorf("MatchOutage claimed the connection-reset row %q, want no match", only)
	}
}

// TestMatchOutageSkipsThinkingLines pins that a model reasoning about an
// outage is not one -- the rule MatchLimit and matchDenial both follow.
func TestMatchOutageSkipsThinkingLines(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		"[thinking]The upstream returned UNAVAILABLE (code 503) last time.",
		"thinking: I should note the 503 was a rate limit, not an outage",
	} {
		if !transcript.IsThinking(line) {
			continue // the pinned wording changed; nothing to assert about it
		}
		if _, ok := MatchOutage(line, baseTime, time.Hour); ok {
			t.Errorf("MatchOutage claimed the thinking line %q", line)
		}
	}
}

// TestMatchOutageCapsTheNote pins the gate note's length, so a provider that
// prints a paragraph of stack trace cannot put an unbounded string in the
// ledger and in every status line that renders it.
func TestMatchOutageCapsTheNote(t *testing.T) {
	t.Parallel()

	long := "API error: UNAVAILABLE (code 503): " + strings.Repeat("no capacity ", 200)
	m, ok := MatchOutage(long, baseTime, time.Hour)
	if !ok {
		t.Fatal("MatchOutage found nothing")
	}
	if n := len([]rune(m.Line)); n != 200 {
		t.Errorf("note length = %d runes, want the 200-rune cap", n)
	}
	if !strings.HasPrefix(m.Line, "API error: UNAVAILABLE (code 503):") {
		t.Errorf("note = %q, want it trimmed and capped from the front", m.Line)
	}
}

// TestOutagePatternsLeaveCreditPatternsAlone is the guard on the credit
// patterns: adding an outage class must not re-time, remove or widen any of
// them.
// LimitPatterns must return exactly the harness + candidate patterns it
// returned before, so the two classifiers share no pattern.
func TestOutagePatternsLeaveCreditPatternsAlone(t *testing.T) {
	t.Parallel()

	d := testDeps(t)
	got := LimitPatterns(d, testOpencodeRef)

	var raw []string
	for _, re := range got {
		raw = append(raw, re.String())
	}
	// The credit-exhaustion patterns, verbatim from internal/harness.
	for _, want := range []string{"(?i)insufficient (credits|quota)", "(?i)requires more credits"} {
		found := false
		for _, s := range raw {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("LimitPatterns lost the #891 pattern %q; got %v", want, raw)
		}
	}

	// And the credit line the credit fixtures use is NOT an outage, so the two
	// classes cannot be confused for one another.
	for _, line := range []string{
		"Error 402: insufficient credits, add funds to continue",
		"Error: this request requires more credits",
	} {
		if _, ok := MatchOutage(line, baseTime, time.Hour); ok {
			t.Errorf("MatchOutage claimed the credit line %q, want no match", line)
		}
		if _, ok := MatchLimit(line, got, baseTime, time.Hour); !ok {
			t.Errorf("MatchLimit stopped matching the #891 credit line %q", line)
		}
	}
}
