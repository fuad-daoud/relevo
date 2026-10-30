package resolve

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
)

func TestPolicyWarningsNoneWhenConsistent(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	got := PolicyWarnings(set, pol)
	if len(got) != 0 {
		t.Fatalf("PolicyWarnings = %+v, want empty", got)
	}
}

func TestPolicyWarningsNoneWhenNoOrder(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)

	got := PolicyWarnings(set, policy.Policy{})
	if len(got) != 0 {
		t.Fatalf("PolicyWarnings = %+v, want empty", got)
	}
}

func TestPolicyWarningsMatrix(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := policy.Policy{Order: map[string][]string{
		"builder":  {testAgyRef, "claude/test/nope"},
		"reviewer": {testAgyRef},
	}}

	want := []PolicyWarning{
		{Role: "builder", Index: 1, Token: "claude/test/nope", Text: `order.builder[1] "claude/test/nope" is not a configured candidate`},
		{Role: "builder", Index: -1, Token: testClaudeRef, Text: `builder: claude-m serves the actor but is not in order.builder`},
		{Role: "builder", Index: -1, Token: testOpencodeRef, Text: `builder: m serves the actor but is not in order.builder`},
		{Role: "reviewer", Index: 0, Token: testAgyRef, Text: `order.reviewer[0] "agy-m" does not serve reviewer (its roles: [builder])`},
		{Role: "reviewer", Index: -1, Token: testClaudeRef, Text: `reviewer: claude-m serves the actor but is not in order.reviewer`},
	}

	got := PolicyWarnings(set, pol)
	if len(got) != len(want) {
		t.Fatalf("PolicyWarnings = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("warning[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFormatPolicyOrderWithGatedFirst(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	until := baseTime.Add(10 * time.Minute)
	gates := []availability.Gate{{Token: testAgyRef, Kind: availability.SpawnFailed, Until: until}}

	got := FormatPolicy(set, pol, gates, availability.History{}, baseTime, time.UTC)

	want := strings.Join([]string{
		"builder  (order set in config policy)",
		"  1  agy-m     order     spawn failed " + availability.GateUntilText(until),
		"  2  claude-m  order     <- would pick",
		"  3  m         unlisted",
		"librarian  (no order set)",
		"  no candidate serves this actor",
		"reviewer  (no order set)",
		"  1  claude-m  sole      <- would pick",
		"researcher  (no order set)",
		"  no candidate serves this actor",
		"",
		"warnings",
		"  builder: m serves the actor but is not in order.builder",
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPolicyNoOrderTwoServeRefuses(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)

	got := FormatPolicy(set, policy.Policy{}, nil, availability.History{}, baseTime, time.UTC)

	want := strings.Join([]string{
		"builder  (no order set)",
		"  1  agy-m",
		"  2  claude-m",
		"  3  m",
		"  would refuse: 3 candidates serve builder and no order is set",
		"librarian  (no order set)",
		"  no candidate serves this actor",
		"reviewer  (no order set)",
		"  1  claude-m  sole      <- would pick",
		"researcher  (no order set)",
		"  no candidate serves this actor",
		`no policy configured; set one with relevo config set policy (see README "Policy")`,
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPolicyAllGated(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)
	gates := []availability.Gate{limit(testAgyRef), limit(testClaudeRef), limit(testOpencodeRef)}

	got := FormatPolicy(set, pol, gates, availability.History{}, baseTime, time.UTC)

	want := strings.Join([]string{
		"builder  (order set in config policy)",
		"  1  agy-m     order     rate-limited until cleared",
		"  2  claude-m  order     rate-limited until cleared",
		"  3  m         order     rate-limited until cleared",
		"  would refuse: every candidate serving builder is gated",
		"librarian  (no order set)",
		"  no candidate serves this actor",
		"reviewer  (no order set)",
		"  1  claude-m  sole      rate-limited until cleared",
		"  would refuse: every candidate serving reviewer is gated",
		"researcher  (no order set)",
		"  no candidate serves this actor",
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPolicyEmptySet(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, "[]")

	got := FormatPolicy(set, orderOf("builder", testAgyRef), nil, availability.History{}, baseTime, time.UTC)

	want := "no candidates configured; set one with relevo config set candidates (see README \"Candidates\")\n"
	if got != want {
		t.Errorf("FormatPolicy = %q, want %q", got, want)
	}
}

// testClaudeOnlyJSON has only the claude candidate, serving both builder
// and reviewer, so a role with an order set still resolves to a sole pick.
const testClaudeOnlyJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]}
]`

func TestFormatPolicySoleWithOrder(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testClaudeOnlyJSON)
	pol := orderOf("reviewer", testClaudeRef)

	got := FormatPolicy(set, pol, nil, availability.History{}, baseTime, time.UTC)

	want := strings.Join([]string{
		"builder  (no order set)",
		"  1  m  sole      <- would pick",
		"librarian  (no order set)",
		"  no candidate serves this actor",
		"reviewer  (order set in config policy)",
		"  1  m  sole      <- would pick",
		"researcher  (no order set)",
		"  no candidate serves this actor",
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

// wantHourRuler renders formatHistory's hour ruler line, matching its exact
// column widths, so tests build the expected history block from counts
// rather than hand-counted spacing.
func wantHourRuler() string {
	var labels [24]string
	for h := range labels {
		labels[h] = fmt.Sprintf("%02d", h)
	}
	return strings.Repeat(" ", 27) + strings.Join(labels[:], " ")
}

// wantHourRow renders one formatHistory data row from a provider, kind and
// a [24]int of hour counts, matching formatHistory's exact cell format, so
// tests state counts, not spacing.
func wantHourRow(provider string, kind availability.Kind, counts [24]int) string {
	row := fmt.Sprintf("  %-10s %-13s", provider, availability.GateKindText(kind))
	for _, n := range counts {
		cell := "."
		if n != 0 {
			cell = strconv.Itoa(n)
		}
		row += fmt.Sprintf(" %2s", cell)
	}
	return row
}

func TestFormatPolicyPeakColumn(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	now := time.Date(2026, 9, 11, 21, 15, 0, 0, time.UTC)

	hist := availability.History{Events: []availability.Event{
		{At: time.Date(2026, 9, 11, 20, 10, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 11, 22, 5, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 11, 21, 0, 0, 0, time.UTC).AddDate(0, 0, -31), Kind: availability.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 11, 21, 30, 0, 0, time.UTC), Kind: availability.SpawnFailed, Provider: "test"},
	}}.Prune(now)

	got := FormatPolicy(set, pol, nil, hist, now, time.UTC)

	var rlCounts, sfCounts [24]int
	rlCounts[20], rlCounts[21], rlCounts[22] = 1, 1, 1
	sfCounts[21] = 1

	want := strings.Join([]string{
		"builder  (order set in config policy)",
		"  1  agy-m     order     limited 3x around 21:00 (30d)  <- would pick",
		"  2  claude-m  order     limited 3x around 21:00 (30d)",
		"  3  m         unlisted  limited 3x around 21:00 (30d)",
		"librarian  (no order set)",
		"  no candidate serves this actor",
		"reviewer  (no order set)",
		"  1  claude-m  sole      limited 3x around 21:00 (30d)  <- would pick",
		"researcher  (no order set)",
		"  no candidate serves this actor",
		"",
		"warnings",
		"  builder: m serves the actor but is not in order.builder",
		"",
		"history (30d, local hours)",
		wantHourRuler(),
		wantHourRow("test", availability.RateLimited, rlCounts),
		wantHourRow("test", availability.SpawnFailed, sfCounts),
	}, "\n") + "\n"

	if got != want {
		t.Errorf("FormatPolicy =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatPolicyPeakWrapsMidnight(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	now := time.Date(2026, 9, 11, 23, 50, 0, 0, time.UTC)

	hist := availability.History{Events: []availability.Event{
		{At: time.Date(2026, 9, 11, 23, 30, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "test"},
		{At: time.Date(2026, 9, 12, 0, 20, 0, 0, time.UTC), Kind: availability.RateLimited, Provider: "test"},
	}}.Prune(now)

	got := FormatPolicy(set, pol, nil, hist, now, time.UTC)

	want := "limited 2x around 23:00 (30d)"
	if !strings.Contains(got, want) {
		t.Errorf("FormatPolicy =\n%s\nwant a row containing %q", got, want)
	}
}

func TestFormatPolicyNoHistoryNoBlock(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)

	got := FormatPolicy(set, pol, nil, availability.History{}, baseTime, time.UTC)

	if strings.Contains(got, "limited") {
		t.Errorf("FormatPolicy with empty history contains %q:\n%s", "limited", got)
	}
	if strings.Contains(got, "history (") {
		t.Errorf("FormatPolicy with empty history contains %q:\n%s", "history (", got)
	}
}

func TestFormatPolicyGateAndPeakOrder(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	now := time.Date(2026, 9, 11, 21, 0, 0, 0, time.UTC)
	until := now.Add(10 * time.Minute)

	gates := []availability.Gate{{Token: testAgyRef, Kind: availability.SpawnFailed, Until: until}}
	hist := availability.History{Events: []availability.Event{
		{At: now, Kind: availability.RateLimited, Provider: "test"},
	}}

	got := FormatPolicy(set, pol, gates, hist, now, time.UTC)

	wantTail := "limited 1x around 21:00 (30d); spawn failed " + availability.GateUntilText(until)
	if !strings.Contains(got, wantTail) {
		t.Errorf("FormatPolicy =\n%s\nwant a row containing %q", got, wantTail)
	}
}

func TestFormatPolicyRepeatedGateRendersOnce(t *testing.T) {
	t.Parallel()

	// Three `relevo gate <token>` runs without a `--clear` between leave
	// three live ledger entries on one token. The row says it once.
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	gates := []availability.Gate{
		{Token: testAgyRef, Kind: availability.RateLimited},
		{Token: testAgyRef, Kind: availability.RateLimited},
		{Token: testAgyRef, Kind: availability.RateLimited},
	}

	got := FormatPolicy(set, pol, gates, availability.History{}, baseTime, time.UTC)

	wantRow := "  1  agy-m     order     rate-limited until cleared\n"
	if !strings.Contains(got, wantRow) {
		t.Errorf("FormatPolicy =\n%s\nwant a row exactly %q", got, wantRow)
	}
	if strings.Contains(got, "until cleared; rate-limited") {
		t.Errorf("gate text repeated:\n%s", got)
	}
}

func TestAllGatedErrorNamesEachGateOnce(t *testing.T) {
	t.Parallel()

	// The refusal text goes through the same renderer as the pick line.
	set := candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)
	gates := []availability.Gate{
		{Token: testAgyRef, Kind: availability.RateLimited},
		{Token: testAgyRef, Kind: availability.RateLimited},
	}

	_, err := Resolve(set, policy.Policy{}, gates, "", "builder")
	if !errors.Is(err, ErrAllGated) {
		t.Fatalf("got %v, want ErrAllGated", err)
	}
	if n := strings.Count(err.Error(), "rate-limited until cleared"); n != 1 {
		t.Errorf("gate text appears %d times in %q, want 1", n, err.Error())
	}
}

func TestRoleRefusalsNoOrder(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)

	got := RoleRefusals(set, policy.Policy{}, nil)

	want := []RoleRefusal{{
		Role:    "builder",
		Text:    "3 candidates serve builder and no order is set",
		NoOrder: true,
		Serving: []string{testAgyRef, testClaudeRef, testOpencodeRef},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RoleRefusals = %+v, want %+v", got, want)
	}
}

func TestRoleRefusalsNoneWhenOrderedOrSole(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	if got := RoleRefusals(set, pol, nil); len(got) != 0 {
		t.Errorf("RoleRefusals with order = %+v, want none (reviewer is sole, researcher unserved)", got)
	}
}

func TestRoleRefusalsAllGated(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)
	gates := []availability.Gate{limit(testAgyRef), limit(testClaudeRef), limit(testOpencodeRef)}

	got := RoleRefusals(set, pol, gates)

	want := []RoleRefusal{
		{Role: "builder", Text: "every candidate serving builder is gated",
			Serving: []string{testAgyRef, testClaudeRef, testOpencodeRef}, Gated: []string{"test"}},
		{Role: "reviewer", Text: "every candidate serving reviewer is gated",
			Serving: []string{testClaudeRef}, Gated: []string{"test"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RoleRefusals = %+v, want %+v", got, want)
	}
}

func TestRoleRefusalsEmptySet(t *testing.T) {
	t.Parallel()

	if got := RoleRefusals(nil, policy.Policy{}, nil); got != nil {
		t.Errorf("RoleRefusals(nil) = %+v, want nil", got)
	}
}

// clearedEvent is a Cleared event recording a block of d that ended at at.
func clearedEvent(provider string, at time.Time, d time.Duration) availability.Event {
	return availability.Event{
		At:       at,
		Kind:     availability.Cleared,
		Provider: provider,
		Source:   "planner",
		Since:    at.Add(-d),
	}
}

// TestFormatHistoryBlockedFor: a provider with clears gains both a `cleared`
// row in the hourly grid and a "blocked for" row summarising them.
// The even-count case pins the lower middle as the median, so it is always
// a duration that was observed rather than an average of two.
func TestFormatHistoryBlockedFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		clears  []availability.Event
		blocked string
	}{
		{
			name: "odd count",
			clears: []availability.Event{
				clearedEvent("cline-pass", baseTime.Add(time.Hour), time.Hour),
				clearedEvent("cline-pass", baseTime.Add(7*time.Hour), 5*time.Hour),
				clearedEvent("cline-pass", baseTime.Add(100*time.Hour), 72*time.Hour),
			},
			blocked: "blocked for (30d, gates cleared by hand)\n" +
				"  cline-pass 3 clears  median 5h00m  longest 3d00h\n",
		},
		{
			name: "even count takes the lower middle",
			clears: []availability.Event{
				clearedEvent("cline-pass", baseTime.Add(time.Hour), time.Hour),
				clearedEvent("cline-pass", baseTime.Add(6*time.Hour), 5*time.Hour),
			},
			blocked: "blocked for (30d, gates cleared by hand)\n" +
				"  cline-pass 2 clears  median 1h00m  longest 5h00m\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hist := availability.History{}.Append(availability.Event{
				At: baseTime, Kind: availability.RateLimited, Provider: "cline-pass", Source: "planner",
			})
			for _, ev := range tt.clears {
				hist = hist.Append(ev)
			}
			// test is gated but never cleared: it belongs in the grid and
			// must stay out of the block.
			hist = hist.Append(availability.Event{
				At: baseTime, Kind: availability.RateLimited, Provider: "test", Source: "planner",
			})

			got := formatHistory(hist, time.UTC)

			if !strings.Contains(got, "  cline-pass cleared") {
				t.Errorf("formatHistory has no cleared grid row for cline-pass:\n%s", got)
			}
			if !strings.HasSuffix(got, tt.blocked) {
				t.Errorf("formatHistory =\n%s\nwant it to end with:\n%s", got, tt.blocked)
			}
		})
	}
}

// TestBlockedText pins blockedText's flooring at each of the three scales.
func TestBlockedText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0m"},
		{45 * time.Minute, "45m"},
		{5*time.Hour + 2*time.Minute, "5h02m"},
		{52 * time.Hour, "2d04h"},
		{72 * time.Hour, "3d00h"},
	}

	for _, tt := range tests {
		if got := blockedText(tt.d); got != tt.want {
			t.Errorf("blockedText(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// A provider name in the history block is a column of a history row, and a
// history row can have arrived from another installation. So the byte ranges a
// terminal acts on -- C0 (ESC among them), DEL and C1, and the newline that
// would start a row of its own -- are dropped on the way out, and the row itself
// is untouched: this changes what a block prints, not what the file holds.
func TestFormatHistoryDropsControlBytesFromAProvider(t *testing.T) {
	t.Parallel()

	const hostile = "open\u001b[31mcode\u007f\u0085\u0007/é\ncline-pass  rate_limited 99"
	hist := availability.History{Events: []availability.Event{
		{At: baseTime, Kind: availability.RateLimited, Provider: hostile, Source: "planner"},
	}}
	clean := availability.History{Events: []availability.Event{
		{At: baseTime, Kind: availability.RateLimited, Provider: "open", Source: "planner"},
	}}

	got := formatHistory(hist, time.UTC)

	if r := controlRunes(got); r != "" {
		t.Fatalf("formatHistory left %q in the block, want every control byte dropped", r)
	}
	var counts [24]int
	counts[baseTime.Hour()] = 1
	if !strings.Contains(got, wantHourRow("open[31mcode/écline-pass  rate_limited 99", availability.RateLimited, counts)) {
		t.Errorf("formatHistory =\n%s\nwant the printable part of the provider kept", got)
	}
	// One row in, one row out: the provider could not start a line of its own,
	// so the block has exactly the shape the clean provider's has.
	if want, have := len(strings.Split(formatHistory(clean, time.UTC), "\n")), len(strings.Split(got, "\n")); have != want {
		t.Errorf("formatHistory of a hostile provider printed %d lines, want the clean provider's %d", have, want)
	}
	// A cleared event for the same provider puts the name in the blocked-for
	// summary too, which is a second place the same string is drawn.
	cleared := availability.History{}.Append(availability.Event{
		At: baseTime, Kind: availability.RateLimited, Provider: hostile, Source: "planner",
	})
	cleared = cleared.Append(clearedEvent(hostile, baseTime.Add(time.Hour), time.Hour))
	blocked := formatHistory(cleared, time.UTC)
	if r := controlRunes(blocked); r != "" {
		t.Fatalf("the blocked-for summary left %q in the block, want every control byte dropped", r)
	}
	if !strings.Contains(blocked, "  open[31mcode/écline-pass  rate_limited 99 1 clear ") {
		t.Errorf("formatHistory =\n%s\nwant the printable part of the provider kept in the summary", blocked)
	}
}

// A clean provider, and one with printable non-ASCII in it, render exactly as
// they did before: sanitizing is not allowed to change a block a peer did not
// try to break.
func TestFormatHistoryLeavesACleanProviderByteIdentical(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"cline-pass", "claude/订阅-m", "тест"} {
		hist := availability.History{Events: []availability.Event{
			{At: baseTime, Kind: availability.RateLimited, Provider: provider, Source: "planner"},
		}}
		want := formatHistory(availability.History{Events: []availability.Event{
			{At: baseTime, Kind: availability.RateLimited, Provider: provider, Source: "planner"},
		}}, time.UTC)
		if got := formatHistory(hist, time.UTC); got != want {
			t.Errorf("formatHistory of %q =\n%s\nwant\n%s", provider, got, want)
		}
		if !strings.Contains(want, provider) {
			t.Errorf("formatHistory of %q dropped the provider itself:\n%s", provider, want)
		}
	}
}

// controlRunes is every byte a terminal would act on rather than print, as one
// string, so a case names what leaked rather than counting bytes. The block's own
// line breaks are excluded: they are what separates rows, and a provider that
// could contribute one is caught by the line count instead.
func controlRunes(s string) string {
	var bad []string
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			bad = append(bad, fmt.Sprintf("%U", r))
		}
	}
	return strings.Join(bad, " ")
}
