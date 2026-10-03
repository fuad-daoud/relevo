package availability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

// Credit exhaustion is not a resetting rate limit. A builder that ran out of
// credits gates until a human tops the account up and clears it: no reset the
// line names and no configured fallback can stand in for that, because neither
// is a promise the provider made about when the balance returns.
//
// These tests pin the classifier's contract at the availability level: the
// phrases that make the class, the shapes that must stay out of it, and the
// guarantee that no reset the line carries can turn the class back into a
// timed gate.

// TestMatchCreditPhrases is the class's own table. Every row is a phrase a
// provider can print when the account is out of credit and must match; the
// second half are near misses that must not, so the classifier cannot grow
// into "every quota line is a credit line".
//
// All five seed phrases appear: the three the issue names (requires more
// credits, credits exhausted, can only afford) and the two the opencode
// harness table already carried as limit patterns (insufficient credits /
// quota). The latter two are pinned verbatim against LimitPatterns further
// down, so a change to the harness table cannot silently drop the class.
func TestMatchCreditPhrases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		ok   bool
	}{
		// The real observation, verbatim: opencode-errors row 2.
		{"the real 402 fixture", "Error: This request requires more credits, or fewer max_tokens. You requested up to 131072 tokens, but can only afford 34217.", true},
		{"requires more credits", "Error: this request requires more credits", true},
		{"credits exhausted", "Error: credits exhausted for this account", true},
		{"can only afford", "Error: the request is larger than this account can only afford", true},
		{"insufficient credits", "Error 402: insufficient credits, add funds to continue", true},
		{"insufficient quota", "Error 429: insufficient quota for this key", true},
		{"codex out of credit", "You have insufficient credits to access the Anthropic API", true},
		{"uppercase CREDITS EXHAUSTED", "Error: CREDITS EXHAUSTED", true},
		{"embedded in a JSON message", `{"error":{"type":"provider.quota","message":"credits exhausted, top up to continue","status":402}}`, true},

		// Near misses. A resetting quota line, a provider fault and a crash all
		// stay out of the class: they have a clock, or no clock but no balance.
		{"a resetting weekly limit", "Error 429: You have reached your weekly ExamplePass limit. The limit resets in 23m, please try again later.", false},
		{"a provider outage", "API error (attempt 1): UNAVAILABLE (code 503): No capacity available for model gemini-3.8-flash-high on the server", false},
		{"a crash", "panic: runtime error: invalid memory address or nil pointer dereference", false},
		{"an ordinary 429", "Error 429: too many requests", false},
		{"an empty tail", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line, ok := MatchCredit(c.text)
			if ok != c.ok {
				t.Fatalf("MatchCredit(%q) ok = %v, want %v", c.text, ok, c.ok)
			}
			if !ok {
				if line != "" {
					t.Errorf("line = %q, want empty on no match", line)
				}
				return
			}
			if line == "" {
				t.Error("line is empty, want the matched line kept as the gate note")
			}
			if strings.TrimSpace(line) != line {
				t.Errorf("line = %q, want it trimmed", line)
			}
		})
	}
}

// TestMatchCreditIgnoresAResetLikeTail is the heart of the contract: a credit
// line that happens to carry a reset, a clock or a date is still the credit
// class. Each row is timed by MatchLimit -- the test asserts that, so it fails
// loudly if a future parser change stops reading the reset -- and every one of
// them is classified credit, which is what stops the timed mapping from
// applying to this class at all.
func TestMatchCreditIgnoresAResetLikeTail(t *testing.T) {
	t.Parallel()

	// Each row also matches one of the harness's own credit patterns, so
	// MatchLimit really does reach it today -- which is what makes the row
	// prove anything. A row built from a seed phrase with no harness pattern
	// would never have been timed, so it would pass here for the wrong reason.
	cases := []struct {
		name string
		text string
	}{
		{"a duration reset", "Error: this request requires more credits; the limit resets in 23m"},
		{"a clock reset", "Error: insufficient credits, resets at 23:30"},
		{"an absolute date", "Error: insufficient credits, try again at Oct 19th, 2026 7:14 PM"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line, ok := MatchCredit(c.text)
			if !ok {
				t.Fatalf("MatchCredit(%q) found nothing, want the credit class", c.text)
			}
			if !strings.Contains(line, "credits") {
				t.Errorf("line = %q, want the matched credit line", line)
			}

			// The same line IS timed by MatchLimit, which is exactly why the
			// credit class cannot be served by that path.
			if _, limitOK := MatchLimit(c.text, opencodePatterns(t), baseTime, time.Hour); !limitOK {
				t.Fatalf("MatchLimit stopped matching %q, so this row no longer proves the timed mapping was reachable", c.text)
			}
		})
	}
}

// TestMatchCreditLastLineWins pins the same backwards walk MatchLimit and
// MatchOutage follow: the most recent credit line is the one reported.
func TestMatchCreditLastLineWins(t *testing.T) {
	t.Parallel()

	text := strings.Join([]string{
		"Error 402: insufficient credits, add funds to continue",
		"retrying the request",
		"Error 402: requires more credits, top up the account",
	}, "\n")

	line, ok := MatchCredit(text)
	if !ok {
		t.Fatal("MatchCredit found nothing, want the most recent credit line")
	}
	if want := "Error 402: requires more credits, top up the account"; line != want {
		t.Errorf("line = %q, want the last one %q", line, want)
	}
}

// TestMatchCreditSkipsThinkingLines pins that a model reasoning about running
// out of credit is not running out of credit -- the rule every scan here
// follows.
func TestMatchCreditSkipsThinkingLines(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		"[thinking]If the account requires more credits the request will fail.",
		"thinking: I should note credits exhausted is different from a rate limit",
	} {
		if !transcript.IsThinking(line) {
			continue // the pinned wording changed; nothing to assert about it
		}
		if got, ok := MatchCredit(line); ok {
			t.Errorf("MatchCredit claimed the thinking line %q", got)
		}
	}
}

// TestMatchCreditCapsTheNote pins the gate note's length, so a provider that
// prints a paragraph alongside the credit error cannot put an unbounded string
// in the ledger and in every status line that renders it.
func TestMatchCreditCapsTheNote(t *testing.T) {
	t.Parallel()

	long := "Error: this request requires more credits. " + strings.Repeat("top up the account. ", 200)
	line, ok := MatchCredit(long)
	if !ok {
		t.Fatal("MatchCredit found nothing")
	}
	if n := len([]rune(line)); n != 200 {
		t.Errorf("note length = %d runes, want the 200-rune cap", n)
	}
	if !strings.HasPrefix(line, "Error: this request requires more credits.") {
		t.Errorf("note = %q, want it trimmed and capped from the front", line)
	}
}

// TestMatchCreditReadsTheRealOpencodeFixture proves the scan channel actually
// delivers the credit text: the raw fixture row is put through the very writer
// the decision points read (transcript.LimitLines) and the classifier must
// still match. Without this the table above could pass on strings no harness
// ever prints.
func TestMatchCreditReadsTheRealOpencodeFixture(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", "opencode-errors", "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	scanned := ""
	for _, line := range lines {
		scanned += strings.Join(transcript.LimitLines("opencode", []byte(line)), "\n") + "\n"
	}

	line, ok := MatchCredit(scanned)
	if !ok {
		t.Fatalf("MatchCredit found nothing in the rendered fixture:\n%s", scanned)
	}
	if !strings.Contains(line, "requires more credits") {
		t.Errorf("line = %q, want the 402 credit row", line)
	}

	// The row alone must match, so the match above came from the credit row and
	// not from the 429 beside it.
	only := strings.Join(transcript.LimitLines("opencode", []byte(lines[1])), "\n")
	if _, ok := MatchCredit(only); !ok {
		t.Errorf("MatchCredit missed the 402 row alone: %q", only)
	}
}

// TestCreditPatternsLeaveOutagePatternsAlone is the both-directions guard at
// the availability level. Credit fixtures must never be an outage, and outage
// fixtures must never be credit: the two classes gate differently, so a line
// claimed by both would gate on whichever ran first.
//
// It also pins the opencode harness table still carries its two credit
// patterns verbatim, so the class cannot be dropped from LimitPatterns while
// the credit classifier keeps matching it here.
func TestCreditPatternsLeaveOutagePatternsAlone(t *testing.T) {
	t.Parallel()

	creditLines := []string{
		"Error 402: insufficient credits, add funds to continue",
		"Error: this request requires more credits",
		"Error: credits exhausted for this account",
		"Error: this request exceeds what the key can only afford",
		"Error 429: insufficient quota for this key",
	}
	for _, line := range creditLines {
		if _, ok := MatchOutage(line, baseTime, time.Hour); ok {
			t.Errorf("MatchOutage claimed the credit line %q, want no match", line)
		}
		if _, ok := MatchCredit(line); !ok {
			t.Errorf("MatchCredit stopped matching the credit line %q", line)
		}
	}

	// Every shape TestMatchOutage's own table calls an outage is not credit.
	for _, line := range []string{
		"API error (attempt 1): UNAVAILABLE (code 503): No capacity available for model gemini-3.8-flash-high on the server",
		"API error: UNAVAILABLE",
		"the upstream is unavailable",
		"API error (attempt 4): code 503 from the upstream",
		"backend returned (code: 500)",
		"backend returned code=502",
		"backend returned code503",
		"POST /v1/messages -> HTTP 502 Bad Gateway",
		"the request came back with status 500",
		`{"status_code":503,"message":"upstream"}`,
		"the endpoint answered service unavailable",
		"the model endpoint reported internal server error",
		"proxy said bad gateway",
		"upstream gateway timeout",
	} {
		if got, ok := MatchCredit(line); ok {
			t.Errorf("MatchCredit claimed the outage line %q (%q), want no match", line, got)
		}
	}

	got := LimitPatterns(testDeps(t), testOpencodeRef)
	var raw []string
	for _, re := range got {
		raw = append(raw, re.String())
	}
	for _, want := range []string{"(?i)insufficient (credits|quota)", "(?i)requires more credits"} {
		found := false
		for _, s := range raw {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("LimitPatterns lost the credit pattern %q; got %v", want, raw)
		}
	}
}

// TestCreditGateClearsWithTheExistingPath is the manual-clear contract: a gate
// written with a zero Until is rendered as "until cleared" and cleared by
// exactly the existing clear path, with no credit-specific one added.
//
// This is what makes the zero Until usable rather than merely recorded -- a
// gate nothing can clear would strand the candidate for good.
func TestCreditGateClearsWithTheExistingPath(t *testing.T) {
	t.Parallel()

	d := testDeps(t)
	line, ok := MatchCredit("Error 402: insufficient credits, add funds to continue")
	if !ok {
		t.Fatal("MatchCredit found nothing")
	}
	entry := Entry{
		Kind:    RateLimited,
		Subject: "test",
		At:      baseTime,
		Note:    line + " -- top up the account to clear",
		Source:  "relevo",
		Binding: "webshop",
	}
	if !entry.Until.IsZero() {
		t.Fatalf("Until = %v, want the zero value that means until cleared", entry.Until)
	}
	if err := AppendEntryLocked(d, entry); err != nil {
		t.Fatalf("AppendEntryLocked: %v", err)
	}

	// Rendered, it reads as until cleared -- not as a time.
	if got, want := GateUntilText(entry.Until), "until cleared"; got != want {
		t.Errorf("GateUntilText = %q, want %q", got, want)
	}

	// And it stays live: the projection still reports it a month on, because a
	// zero Until is never expired by time alone. Only the clear below lifts it.
	monthLater := baseTime.Add(30 * 24 * time.Hour)
	d.Now = func() time.Time { return monthLater }
	gates := LedgerGates(d, []string{testOpencodeRef})
	if len(gates) != 1 {
		t.Fatalf("gates = %+v, want the credit gate still live a month later: a zero Until never expires", gates)
	}
	if gates[0].Until.IsZero() != true {
		t.Errorf("gate Until = %v, want the zero value that renders as until cleared", gates[0].Until)
	}
	d.Now = func() time.Time { return baseTime }

	// The existing clear path resolves and clears it, unchanged.
	provider, removed, err := Available(d, "test", ClearedByMasterMind)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1: the existing clear path must clear a credit gate unchanged", removed)
	}
	if provider != "test" {
		t.Errorf("provider = %q, want %q", provider, "test")
	}
	if gates := LedgerGates(d, []string{testOpencodeRef}); len(gates) != 0 {
		t.Errorf("gates = %+v, want none after the existing clear path ran", gates)
	}
}
