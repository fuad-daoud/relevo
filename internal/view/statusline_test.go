package view

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestAgeText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input time.Duration
		want  string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h 0m"},
		{27*time.Hour + 4*time.Minute + 30*time.Second, "27h 4m"},
		{-5 * time.Second, "0s"},
	}

	for _, tt := range tests {
		got := AgeText(tt.input)
		if got != tt.want {
			t.Errorf("AgeText(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

var sgrRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripSGR(s string) string {
	return sgrRe.ReplaceAllString(s, "")
}

func width(s string) int {
	return utf8.RuneCountInString(stripSGR(s))
}

func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func TestRenderStatusLineEmpty(t *testing.T) {
	t.Parallel()

	if got := RenderStatusLine(Report{}, baseTime, 80); got != "" {
		t.Errorf("RenderStatusLine(Report{}, baseTime, 80) = %q, want empty", got)
	}
}

// TestRenderPlannerLine is the statusline surface: the first line names the
// planner, an empty name renders nothing, and a narrow terminal cuts the
// visible text to the column budget.
func TestRenderPlannerLine(t *testing.T) {
	t.Parallel()

	got := RenderPlannerLine("architect-14", 80)
	if !strings.Contains(got, "planner architect-14") {
		t.Errorf("RenderPlannerLine(%q, 80) = %q, want it to contain %q", "architect-14", got, "planner architect-14")
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("RenderPlannerLine(%q, 80) = %q, want it to end in a newline", "architect-14", got)
	}

	if got := RenderPlannerLine("", 80); got != "" {
		t.Errorf("RenderPlannerLine(%q, 80) = %q, want empty", "", got)
	}

	narrow := RenderPlannerLine("architect-14", 10)
	visible := strings.TrimSuffix(stripSGR(narrow), "\n")
	if w := utf8.RuneCountInString(visible); w > 10 {
		t.Errorf("RenderPlannerLine(%q, 10) visible text is %d runes, want at most 10: %q", "architect-14", w, visible)
	}
}

// TestRenderStatusLineWaitingFallthrough fixtures.
var wfNow = baseTime

var waitingFallthroughCases = []struct {
	name         string
	binding      BindingStatus
	expectMid    string
	expectStatus string
	expectRight  string
	noSeparator  bool
}{
	{
		name: "report note",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindReport, Note: "unmarked"},
		},
		expectMid:    "r1 · builder on agy",
		expectStatus: "report in",
	},
	{
		name: "question",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindQuestion},
		},
		expectMid:    "r1 · builder on agy",
		expectStatus: "question in",
	},
	{
		name: "answer",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindAnswer},
		},
		expectMid:    "r1 · builder on agy",
		expectStatus: "answered",
	},
	{
		name: "last nil",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      nil,
		},
		expectMid:    "r1 · builder on agy",
		expectStatus: "no plan yet",
		expectRight:  "--",
	},
	{
		name: "empty candidate",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "",
			LastPayload:      &LastEvent{Kind: store.KindPlan},
		},
		expectMid:    "r1 · builder",
		expectStatus: "plan sent",
		noSeparator:  true,
	},
	{
		name: "candidate with no slash",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindPlan},
		},
		expectMid:    "r1 · builder on agy",
		expectStatus: "plan sent",
	},
}

func TestRenderStatusLineWaitingFallthrough(t *testing.T) {
	t.Parallel()

	for _, tt := range waitingFallthroughCases {
		t.Run(tt.name, func(t *testing.T) {
			rep := Report{Bindings: []BindingStatus{tt.binding}}
			out := RenderStatusLine(rep, wfNow, 80)
			lines := splitLines(out)
			if len(lines) != 1 {
				t.Fatalf("got %d lines, want 1", len(lines))
			}
			plain := stripSGR(lines[0])
			if tt.expectMid != "" && !strings.Contains(plain, tt.expectMid) {
				t.Errorf("line %q does not contain mid %q", plain, tt.expectMid)
			}
			if tt.expectStatus != "" && !strings.Contains(plain, tt.expectStatus) {
				t.Errorf("line %q does not contain status %q", plain, tt.expectStatus)
			}
			if tt.expectRight != "" && !strings.Contains(plain, tt.expectRight) {
				t.Errorf("line %q does not contain right %q", plain, tt.expectRight)
			}
			if tt.noSeparator && strings.Contains(plain, "·  ·") {
				t.Errorf("line %q contains empty separator", plain)
			}
		})
	}
}

// TestRenderStatusLineIgnoresBookkeepingLast pins that the statusline reads
// LastPayload, not relevo's own bookkeeping entries (Last): a binding whose
// most recent log entry is a drift note must still show the plan, and the
// age of that plan, not the age of the drift note.
func TestRenderStatusLineIgnoresBookkeepingLast(t *testing.T) {
	t.Parallel()

	now := baseTime
	rep := Report{Bindings: []BindingStatus{
		{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			RoundStart:       now.Add(-12 * time.Minute),
			Last:             &LastEvent{Kind: store.KindDrift, TS: now.Add(-1 * time.Second)},
			LastPayload:      &LastEvent{Kind: store.KindPlan, TS: now.Add(-12 * time.Minute)},
		},
	}}

	out := RenderStatusLine(rep, now, 80)
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	plain := stripSGR(lines[0])
	if !strings.Contains(plain, "plan sent") {
		t.Errorf("line %q does not contain %q", plain, "plan sent")
	}
	if strings.Contains(plain, "drift") {
		t.Errorf("line %q must not mention drift: %q", plain, plain)
	}
	if !strings.HasSuffix(plain, " 12m") {
		t.Errorf("line %q does not have right cell ending in %q", plain, " 12m")
	}
	if strings.Contains(plain, "ACTIVE") {
		t.Errorf("line %q must not contain ACTIVE", plain)
	}
}

// TestRenderStatusLineLiveSegment pins the trailing live segment:
// a row whose round is running shows the live figure last in the middle
// cell, before the right cell's clock and state.
func TestRenderStatusLineLiveSegment(t *testing.T) {
	t.Parallel()

	b := BindingStatus{
		Name:             "api",
		Round:            1,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		BuilderName:      "glm-5.3-flash",
		RoundStart:       baseTime.Add(-12 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "builder on glm-5.3-flash · 41k tok") {
		t.Errorf("no live tokens segment: %q", plain)
	}
	if !strings.Contains(plain, "plan sent") {
		t.Errorf("the row's phase must show in its status column: %q", plain)
	}
	if strings.Contains(plain, "$") {
		t.Errorf("must not contain dollar figure: %q", plain)
	}
	if strings.Contains(plain, "live") {
		t.Errorf("must not contain 'live': %q", plain)
	}
	if !strings.HasSuffix(plain, " 12m") {
		t.Errorf("the right cell must survive: %q", plain)
	}
}

// TestRenderStatusLineClosedRoundTokens pins the closed-round segment: a row
// with a closed round shows that round's tokens.
func TestRenderStatusLineClosedRoundTokens(t *testing.T) {
	t.Parallel()

	b := BindingStatus{
		Name:             "api",
		Round:            3,
		Display:          "NEEDS YOU",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		RoundStart:       baseTime.Add(-12 * time.Minute),
		RoundEnd:         baseTime.Add(-2 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-2 * time.Minute), Kind: store.KindReport, Direction: store.DirToPlanner},
		RoundUsage:       &usage.Usage{Tokens: usage.Tokens{In: 2_100_000}},
		Spend:            &usage.Spend{Rounds: 2, Measured: 1.51, Tokens: usage.Tokens{In: 9_000_000}},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "· 2.1M tok") {
		t.Errorf("no round tokens segment: %q", plain)
	}
	if strings.Contains(plain, "9.0M") || strings.Contains(plain, "9M") {
		t.Errorf("must not contain spend tokens: %q", plain)
	}
	if strings.Contains(plain, "$") {
		t.Errorf("must not contain dollar figure: %q", plain)
	}
}

func TestRenderStatusLineLiveWinsOverSpend(t *testing.T) {
	t.Parallel()

	b := BindingStatus{
		Name:             "api",
		Round:            4,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		RoundStart:       baseTime.Add(-12 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		Spend:            &usage.Spend{Rounds: 3, Measured: 1.51, Tokens: usage.Tokens{In: 2_100_000}},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "41k tok") {
		t.Errorf("the live figure must show: %q", plain)
	}
	if strings.Contains(plain, "2.1M") {
		t.Errorf("spend must yield to the live figure, never share a line: %q", plain)
	}
	if strings.Contains(plain, "$") {
		t.Errorf("must not contain dollar figure: %q", plain)
	}
}

// TestRenderStatusLineNarrowDropsUsageFirst pins the placement: the usage
// segment is the last thing in mid, so truncation drops it before the
// waiting verb, and the right cell always survives.
func TestRenderStatusLineNarrowDropsUsageFirst(t *testing.T) {
	t.Parallel()

	b := BindingStatus{
		Name:             "api",
		Round:            1,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		RoundStart:       baseTime.Add(-12 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 40))[0])
	if !strings.HasSuffix(plain, " 12m") {
		t.Errorf("12m must survive the narrow row: %q", plain)
	}
	if strings.Contains(plain, "tok") {
		t.Errorf("the usage segment must be the part truncated: %q", plain)
	}
}

func TestStatusLineWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		columns  int
		override string
		want     int
	}{
		{146, "", 142},
		{146, "0", 146},
		{146, "10", 136},
		{146, "x", 142},
		{146, "-1", 142},
		{0, "", 0},
		{-5, "0", 0},
		{3, "", 1},
	}

	for _, tt := range tests {
		got := StatusLineWidth(tt.columns, tt.override)
		if got != tt.want {
			t.Errorf("StatusLineWidth(%d, %q) = %d, want %d", tt.columns, tt.override, got, tt.want)
		}
	}
}

func TestShouldDrainStdin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode os.FileMode
		want bool
	}{
		{os.ModeCharDevice, false},
		{os.FileMode(0), true},
		{os.ModeNamedPipe, true},
		{os.ModeCharDevice | os.ModeDevice, false},
	}

	for _, tt := range tests {
		got := ShouldDrainStdin(tt.mode)
		if got != tt.want {
			t.Errorf("ShouldDrainStdin(%v) = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestWaitingReportOutcome(t *testing.T) {
	t.Parallel()

	t.Run("outcome halted", func(t *testing.T) {
		b := BindingStatus{
			LastPayload: &LastEvent{
				Kind:    store.KindReport,
				Outcome: "halted",
			},
		}
		if got := waiting(b); got != "report in · halted" {
			t.Errorf("waiting = %q, want 'report in · halted'", got)
		}
	})

	t.Run("outcome halted with note", func(t *testing.T) {
		b := BindingStatus{
			LastPayload: &LastEvent{
				Kind:    store.KindReport,
				Note:    "unmarked",
				Outcome: "halted",
			},
		}
		if got := waiting(b); got != "report in (unmarked) · halted" {
			t.Errorf("waiting = %q, want 'report in (unmarked) · halted'", got)
		}
	})

	t.Run("outcome done quiet", func(t *testing.T) {
		b := BindingStatus{
			LastPayload: &LastEvent{
				Kind:    store.KindReport,
				Outcome: "done",
			},
		}
		if got := waiting(b); got != "report in" {
			t.Errorf("waiting = %q, want 'report in'", got)
		}
	})
}

// TestRowStatus pins the one status column: Status and Tone come from one
// rule in one order, so no surface has to derive a status again.
// TestRowStatus fixtures.
var rsNow = baseTime

var rowStatusCases = []struct {
	name       string
	binding    BindingStatus
	wantStatus string
	wantTone   string
	wantReason string
}{
	{
		name: "plan sent",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindPlan, Direction: store.DirToBuilder, TS: rsNow.Add(-2 * time.Minute)},
		},
		wantStatus: "plan sent",
		wantTone:   "phase",
	},
	{
		name: "no payload",
		binding: BindingStatus{
			Name:    "api",
			Round:   1,
			Display: "ACTIVE",
		},
		wantStatus: "no plan yet",
		wantTone:   "phase",
	},
	{
		name: "delivered plain report",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindReport, Direction: store.DirToPlanner, Round: 1, TS: rsNow.Add(-2 * time.Minute)},
		},
		wantStatus: "REPORT IN",
		wantTone:   "report",
	},
	{
		name: "delivered report with a note and an outcome",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindReport, Direction: store.DirToPlanner, Note: "unmarked", Outcome: "halted", TS: rsNow.Add(-2 * time.Minute)},
		},
		wantStatus: "REPORT IN · unmarked · halted",
		wantTone:   "report",
	},
	{
		name: "delivered report whose outcome is done",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindReport, Direction: store.DirToPlanner, Outcome: "done", TS: rsNow.Add(-2 * time.Minute)},
		},
		wantStatus: "REPORT IN",
		wantTone:   "report",
	},
	{
		name: "delivered question",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			LastPayload:      &LastEvent{Kind: store.KindQuestion, Direction: store.DirToPlanner, TS: rsNow.Add(-2 * time.Minute)},
		},
		wantStatus: "QUESTION IN",
		wantTone:   "report",
	},
	{
		name: "report still in flight on a live deliverer route",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			PlannerRoute:     "deliverer",
			PlannerRouteLive: true,
			Pending:          &PendingInfo{Round: 1, Kind: store.KindReport},
			LastPayload:      &LastEvent{Kind: store.KindReport, Direction: store.DirToPlanner, TS: rsNow.Add(-5 * time.Second)},
		},
		wantStatus: "report in",
		wantTone:   "phase",
	},
	{
		name: "stalled report",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			PlannerRoute:     "deliverer",
			PlannerRouteLive: true,
			Pending:          &PendingInfo{Round: 1, Kind: store.KindReport},
			LastPayload:      &LastEvent{Kind: store.KindReport, Direction: store.DirToPlanner, TS: rsNow.Add(-2 * time.Minute)},
		},
		wantStatus: "NEEDS YOU",
		wantTone:   "needs",
		wantReason: "report in",
	},
	{
		name:       "paused display",
		binding:    BindingStatus{Name: "api", Round: 1, Display: "PAUSED", BuilderCandidate: "agy"},
		wantStatus: "PAUSED",
		wantTone:   "quiet",
	},
	{
		name:       "held display",
		binding:    BindingStatus{Name: "api", Round: 1, Display: "HELD", BuilderCandidate: "agy"},
		wantStatus: "HELD",
		wantTone:   "held",
	},
	{
		name: "needs you display carries its detail as the reason",
		binding: BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "NEEDS YOU",
			BuilderCandidate: "agy",
			Detail:           "round 1 was open",
		},
		wantStatus: "NEEDS YOU",
		wantTone:   "needs",
		wantReason: "round 1 was open",
	},
}

func TestRowStatus(t *testing.T) {

	for _, tt := range rowStatusCases {
		t.Run(tt.name, func(t *testing.T) {
			rows := StatusLineRows(Report{Bindings: []BindingStatus{tt.binding}}, rsNow)
			if len(rows) != 1 {
				t.Fatalf("len(rows) = %d, want 1", len(rows))
			}
			if rows[0].Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", rows[0].Status, tt.wantStatus)
			}
			if rows[0].Tone != tt.wantTone {
				t.Errorf("Tone = %q, want %q", rows[0].Tone, tt.wantTone)
			}
			if rows[0].Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", rows[0].Reason, tt.wantReason)
			}
		})
	}
}

// TestStatusLineRowActor pins the actor a row names: a builder binding stores
// an empty role (normRole), so its row still names "builder".
func TestStatusLineRowActor(t *testing.T) {
	tests := []struct {
		name string
		role string
		want string
	}{
		{name: "builder stores no role", role: "", want: "builder"},
		{name: "reviewer", role: "reviewer", want: "reviewer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := BindingStatus{Name: "api", Round: 1, Display: "ACTIVE", Role: tt.role}
			rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, baseTime)
			if len(rows) != 1 {
				t.Fatalf("len(rows) = %d, want 1", len(rows))
			}
			if rows[0].Actor != tt.want {
				t.Errorf("Actor = %q, want %q", rows[0].Actor, tt.want)
			}
		})
	}
}

// TestRenderStatusLineOneStatusClockLast pins the row's three parts: the middle
// names the actor, the status column holds the row's one status, and the clock
// is right-aligned last. Only the status column carries the status word, and
// the status and the clock hold their columns on every row, so neither moves
// when another row's text changes length.
func TestRenderStatusLineOneStatusClockLast(t *testing.T) {
	now := baseTime
	rep := Report{Bindings: []BindingStatus{
		{
			Name:             "planner-policy",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy/google/gemini-3.8-flash-high",
			RoundStart:       now.Add(-7 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-7 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		},
		{
			Name:             "leaves1",
			Round:            2,
			Display:          "ACTIVE",
			BuilderCandidate: "agy/google/gemini-3.8-flash-high",
			RoundStart:       now.Add(-33 * time.Minute),
			RoundEnd:         now.Add(-31 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-31 * time.Minute), Round: 2, Kind: store.KindReport, Direction: store.DirToPlanner},
		},
		{
			Name:             "proc",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy/google/gemini-3.8-flash-high",
			RoundStart:       now.Add(-21 * time.Minute),
			RoundEnd:         now.Add(-20 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-20 * time.Minute), Round: 1, Kind: store.KindReport, Direction: store.DirToPlanner, Outcome: "halted"},
		},
	}}

	rows := StatusLineRows(rep, now)
	lines := splitLines(RenderStatusLine(rep, now, 140))
	if len(lines) != len(rows) {
		t.Fatalf("got %d lines and %d rows, want one line per row", len(lines), len(rows))
	}

	statusCol, clockEnd := -1, -1
	for i, row := range rows {
		plain := stripSGR(lines[i])
		t.Logf("row %d: %q", i, plain)

		if got := strings.Count(strings.ToLower(plain), strings.ToLower(row.Status)); got != 1 {
			t.Errorf("line %d %q carries its status %q %d times, want exactly once", i, plain, row.Status, got)
		}
		if !strings.HasSuffix(plain, row.Clock) {
			t.Errorf("line %d %q does not end with its clock %q", i, plain, row.Clock)
		}
		if end := utf8.RuneCountInString(plain); clockEnd < 0 {
			clockEnd = end
		} else if end != clockEnd {
			t.Errorf("line %d ends at column %d, want %d, so the clock's last rune never moves", i, end, clockEnd)
		}
		if col := strings.Index(plain, row.Status); col < 0 {
			t.Errorf("line %d %q does not carry its status %q at all", i, plain, row.Status)
		} else if statusCol < 0 {
			statusCol = col
		} else if col != statusCol {
			t.Errorf("line %d status starts at column %d, want %d", i, col, statusCol)
		}
		if !strings.Contains(plain, "builder on agy") {
			t.Errorf("line %d %q does not name the actor and the harness in the middle", i, plain)
		}
	}
}

func statuslineFixture(now time.Time) Report {
	return Report{
		Bindings: []BindingStatus{
			{
				Name:             "api",
				Round:            3,
				Display:          "ACTIVE",
				BuilderCandidate: "agy/google/gemini-3.8-flash-high",
				RoundStart:       now.Add(-12 * time.Minute),
				LastPayload: &LastEvent{
					TS:        now.Add(-12 * time.Minute),
					Kind:      store.KindPlan,
					Direction: store.DirToBuilder,
				},
			},
			{
				Name:             "client",
				Round:            1,
				Display:          "NEEDS YOU",
				BuilderCandidate: "opencode/openrouter/z-ai/glm-5.3-flash",
				BuilderName:      "glm-5.3-flash",
				RoundStart:       now.Add(-4 * time.Minute),
				LastPayload: &LastEvent{
					TS:   now.Add(-4 * time.Minute),
					Kind: store.KindPlan,
				},
			},
			{
				Name:             "docs",
				Round:            2,
				Display:          "PAUSED",
				BuilderCandidate: "agy/google/gemini-3.8-flash-high",
				RoundStart:       now.Add(-5 * time.Minute),
				RoundEnd:         now.Add(-5*time.Minute + 23*time.Second),
				LastPayload: &LastEvent{
					TS:        now.Add(-23 * time.Second),
					Kind:      store.KindReport,
					Direction: store.DirToPlanner,
				},
			},
		},
	}
}

func TestRenderStatusLineAt80(t *testing.T) {
	t.Parallel()

	out := RenderStatusLine(statuslineFixture(baseTime), baseTime, 80)
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		if w := width(line); w != 80 {
			t.Errorf("line %d width = %d, want 80; line = %q", i, w, stripSGR(line))
		}
	}

	plain0 := stripSGR(lines[0])
	if !strings.HasPrefix(plain0, "○ api     r3 · builder on agy") {
		t.Errorf("line 0 prefix mismatch: %q", plain0)
	}
	if !strings.HasSuffix(plain0, "plan sent  12m") {
		t.Errorf("line 0 suffix mismatch: %q", plain0)
	}
	if strings.Contains(plain0, "ACTIVE") {
		t.Errorf("line 0 must not contain ACTIVE: %q", plain0)
	}

	plain1 := stripSGR(lines[1])
	if !strings.HasPrefix(plain1, "● client  r1 · builder on glm-5.3-flash · plan sent") {
		t.Errorf("line 1 prefix mismatch: %q", plain1)
	}
	if !strings.HasSuffix(plain1, "NEEDS YOU   4m") {
		t.Errorf("line 1 suffix mismatch: %q", plain1)
	}

	plain2 := stripSGR(lines[2])
	if !strings.HasPrefix(plain2, "○ docs    r2 · builder on agy") {
		t.Errorf("line 2 prefix mismatch: %q", plain2)
	}
	if !strings.HasSuffix(plain2, "PAUSED     23s") {
		t.Errorf("line 2 suffix mismatch: %q", plain2)
	}
}

func TestRenderStatusLineTruncatesAt40(t *testing.T) {
	t.Parallel()

	out := RenderStatusLine(statuslineFixture(baseTime), baseTime, 40)
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		if w := width(line); w != 40 {
			t.Errorf("line %d width = %d, want 40; line = %q", i, w, stripSGR(line))
		}
	}

	plain2 := stripSGR(lines[2])
	if !strings.Contains(plain2, "…") {
		t.Errorf("line 2 expected to contain '…': %q", plain2)
	}

	suffixes := []string{"plan sent  12m", "NEEDS YOU   4m", "PAUSED     23s"}
	for i, line := range lines {
		plain := stripSGR(line)
		if !strings.HasSuffix(plain, suffixes[i]) {
			t.Errorf("line %d suffix mismatch: %q, want suffix %q", i, plain, suffixes[i])
		}
	}
}

func TestRenderStatusLineZeroColumnsIs80(t *testing.T) {
	t.Parallel()

	out0 := RenderStatusLine(statuslineFixture(baseTime), baseTime, 0)
	out80 := RenderStatusLine(statuslineFixture(baseTime), baseTime, 80)
	if out0 != out80 {
		t.Errorf("output for columns 0 does not match columns 80:\nout0:\n%s\nout80:\n%s", out0, out80)
	}
}

func TestRenderStatusLineUnpaddedWhenTooNarrow(t *testing.T) {
	t.Parallel()

	out := RenderStatusLine(statuslineFixture(baseTime), baseTime, 20)
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	plain0 := stripSGR(lines[0])
	want := "○ api  r3 · builder on agy · plan sent · 12m"
	if plain0 != want {
		t.Errorf("line 0 = %q, want %q", plain0, want)
	}
}

func TestRenderStatusLineColours(t *testing.T) {
	t.Parallel()

	out := RenderStatusLine(statuslineFixture(baseTime), baseTime, 80)
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	if !strings.Contains(lines[0], "\x1b[38;5;245m○\x1b[0m") {
		t.Errorf("line 0 missing dim dot: %q", lines[0])
	}
	if strings.Contains(lines[0], "\x1b[38;5;42m") {
		t.Errorf("line 0 must not contain active display colour: %q", lines[0])
	}
	if strings.Contains(lines[0], "ACTIVE") {
		t.Errorf("line 0 must not contain ACTIVE: %q", lines[0])
	}

	if !strings.Contains(lines[1], "\x1b[1;38;5;214m●\x1b[0m") {
		t.Errorf("line 1 missing needs you dot: %q", lines[1])
	}
	if !strings.Contains(lines[1], "\x1b[1;38;5;214mNEEDS YOU\x1b[0m") {
		t.Errorf("line 1 missing needs you display colour: %q", lines[1])
	}

	if strings.Count(lines[2], "\x1b[") != 2 {
		t.Errorf("line 2 should contain exactly 2 escape sequences (the dot only), got %d: %q", strings.Count(lines[2], "\x1b["), lines[2])
	}
}

func TestRoundClock(t *testing.T) {
	t.Parallel()

	start := baseTime.Add(-10 * time.Minute)
	end := baseTime.Add(-3 * time.Minute)

	// Zero start -> "--"
	zero := BindingStatus{}
	if got := roundClock(zero, baseTime); got != "--" {
		t.Errorf("roundClock(zero) = %q, want %q", got, "--")
	}

	// Open -> now - start, and advancing now advances it
	open := BindingStatus{RoundStart: start}
	now1 := baseTime
	now2 := baseTime.Add(5 * time.Minute)
	got1 := roundClock(open, now1)
	got2 := roundClock(open, now2)
	if got1 != "10m" {
		t.Errorf("roundClock(open, now1) = %q, want '10m'", got1)
	}
	if got2 != "15m" {
		t.Errorf("roundClock(open, now2) = %q, want '15m'", got2)
	}
	if got1 == got2 {
		t.Errorf("advancing now must advance open round clock: %q vs %q", got1, got2)
	}

	// Closed -> end - start, and two different now values give the same string
	closed := BindingStatus{RoundStart: start, RoundEnd: end}
	c1 := roundClock(closed, now1)
	c2 := roundClock(closed, now2)
	if c1 != "7m" {
		t.Errorf("roundClock(closed, now1) = %q, want '7m'", c1)
	}
	if c1 != c2 {
		t.Errorf("different now values must give same string for closed round: %q vs %q", c1, c2)
	}
}

func TestRenderStatusLineRemoteServer(t *testing.T) {
	t.Parallel()

	b := BindingStatus{
		Name:             "api",
		Round:            1,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		BuilderName:      "glm-5.3-flash",
		Server:           "contabo",
		RoundStart:       baseTime.Add(-10 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-10 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
	}
	out := RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120)
	plain := stripSGR(splitLines(out)[0])
	if !strings.Contains(plain, "r1 · builder on glm-5.3-flash@contabo") {
		t.Errorf("expected glm-5.3-flash@contabo, got: %q", plain)
	}

	b.Server = ""
	outLocal := RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120)
	plainLocal := stripSGR(splitLines(outLocal)[0])
	if !strings.Contains(plainLocal, "r1 · builder on glm-5.3-flash") {
		t.Errorf("expected glm-5.3-flash, got: %q", plainLocal)
	}
}

// TestRenderStatusLineOnFallback pins the middle's " on " segment: it names
// the candidate's short name when the row carries one, falls back to the
// harness when the set no longer holds the token, and disappears entirely
// when the row has no candidate.
func TestRenderStatusLineOnFallback(t *testing.T) {
	t.Parallel()

	b := BindingStatus{
		Name:             "api",
		Round:            1,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		LastPayload:      &LastEvent{TS: baseTime.Add(-10 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "r1 · builder on opencode") {
		t.Errorf("a retired token must fall back to the harness: %q", plain)
	}

	b.Server = "contabo"
	plain = stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "r1 · builder on opencode@contabo") {
		t.Errorf("a retired token on a remote runner must name the server: %q", plain)
	}

	b.Server = ""
	b.BuilderCandidate = ""
	plain = stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if strings.Contains(plain, " on ") {
		t.Errorf("a row with no candidate must not name what it runs on: %q", plain)
	}
}

func TestRoundTokensAddsPrior(t *testing.T) {
	t.Parallel()

	// 1. open round with live 41k plus prior 100k gives "141k tok"
	b1 := BindingStatus{
		RoundEnd: time.Time{}, // open
		LiveUsage: &usage.Usage{
			Samples: 1,
			Tokens:  usage.Tokens{In: 41_000},
		},
		RoundPriorTokens: usage.Tokens{In: 100_000},
	}
	if got := roundTokens(b1); got != "141k tok" {
		t.Errorf("open roundTokens = %q, want %q", got, "141k tok")
	}

	// 2. closed with 2.1M plus 0.4M gives "2.5M tok"
	b2 := BindingStatus{
		RoundEnd: baseTime, // closed
		RoundUsage: &usage.Usage{
			Tokens: usage.Tokens{In: 2_100_000},
		},
		RoundPriorTokens: usage.Tokens{In: 400_000},
	}
	if got := roundTokens(b2); got != "2.5M tok" {
		t.Errorf("closed roundTokens = %q, want %q", got, "2.5M tok")
	}

	// 3. no live samples but prior 100k gives "100k tok"
	b3 := BindingStatus{
		RoundEnd:         time.Time{}, // open
		LiveUsage:        &usage.Usage{Samples: 0},
		RoundPriorTokens: usage.Tokens{In: 100_000},
	}
	if got := roundTokens(b3); got != "100k tok" {
		t.Errorf("no live samples roundTokens = %q, want %q", got, "100k tok")
	}
}

func TestStatusLineRows(t *testing.T) {
	t.Parallel()

	now := baseTime

	t.Run("NEEDS YOU row with a report LastPayload", func(t *testing.T) {
		b := BindingStatus{
			Name:             "worker",
			Round:            2,
			Display:          "NEEDS YOU",
			BuilderCandidate: "claude/model",
			RoundStart:       now.Add(-5 * time.Minute),
			RoundEnd:         now.Add(-2 * time.Minute),
			LastPayload: &LastEvent{
				Kind: store.KindReport,
				Note: "halted",
				TS:   now.Add(-2 * time.Minute),
			},
			PlannerRoute: "deliverer",
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		row := rows[0]
		if row.Name != "worker" || row.Round != 2 || row.Display != "NEEDS YOU" {
			t.Errorf("row = %+v", row)
		}
		if row.Waiting != "report in (halted)" {
			t.Errorf("Waiting = %q, want 'report in (halted)'", row.Waiting)
		}
		if row.Clock != "3m" {
			t.Errorf("Clock = %q, want '3m'", row.Clock)
		}
		if row.LastKind != "report" {
			t.Errorf("LastKind = %q, want 'report'", row.LastKind)
		}
		expectedTS := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)
		if row.LastTS != expectedTS {
			t.Errorf("LastTS = %q, want %q", row.LastTS, expectedTS)
		}
		if row.Route != "deliverer" {
			t.Errorf("Route = %q, want 'deliverer'", row.Route)
		}
		if row.Actor != "builder" {
			t.Errorf("Actor = %q, want 'builder'", row.Actor)
		}
		if row.Status != "NEEDS YOU" || row.Tone != "needs" {
			t.Errorf("Status/Tone = %q/%q, want 'NEEDS YOU'/'needs'", row.Status, row.Tone)
		}
		if row.Reason != "report in (halted)" {
			t.Errorf("Reason = %q, want 'report in (halted)'", row.Reason)
		}
	})

}

func TestStatusLineRowsTokens(t *testing.T) {
	t.Parallel()

	now := baseTime

	t.Run("open round with LiveUsage samples -> tokens = <n> tok", func(t *testing.T) {
		b := BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
			RoundStart:       now.Add(-12 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-12 * time.Minute), Kind: store.KindPlan},
			LiveUsage: &usage.Usage{
				Tokens:  usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000},
				Samples: 1,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].Tokens != "41k tok" {
			t.Errorf("Tokens = %q, want '41k tok'", rows[0].Tokens)
		}
	})

	t.Run("closed round with RoundUsage -> its tokens", func(t *testing.T) {
		b := BindingStatus{
			Name:       "api",
			Round:      1,
			RoundStart: now.Add(-10 * time.Minute),
			RoundEnd:   now.Add(-5 * time.Minute),
			RoundUsage: &usage.Usage{
				Tokens: usage.Tokens{In: 20_000, Out: 5_000},
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].Tokens != "25k tok" {
			t.Errorf("Tokens = %q, want '25k tok'", rows[0].Tokens)
		}
	})

}

func TestStatusLineRowsRemote(t *testing.T) {
	t.Parallel()

	now := baseTime

	t.Run("remote row (Server set) -> harness <h>@<server>", func(t *testing.T) {
		b := BindingStatus{
			Name:             "api",
			Round:            1,
			BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
			Server:           "contabo",
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].Harness != "opencode@contabo" {
			t.Errorf("Harness = %q, want 'opencode@contabo'", rows[0].Harness)
		}
		if rows[0].Actor != "builder" {
			t.Errorf("Actor = %q, want 'builder'", rows[0].Actor)
		}
	})

	t.Run("no RoundStart -> clock --", func(t *testing.T) {
		b := BindingStatus{
			Name:  "api",
			Round: 1,
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].Clock != "--" {
			t.Errorf("Clock = %q, want '--'", rows[0].Clock)
		}
	})

	t.Run("no payload -> last_kind \"\", last_ts \"\"", func(t *testing.T) {
		b := BindingStatus{
			Name:        "api",
			Round:       1,
			LastPayload: nil,
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].LastKind != "" {
			t.Errorf("LastKind = %q, want empty", rows[0].LastKind)
		}
		if rows[0].LastTS != "" {
			t.Errorf("LastTS = %q, want empty", rows[0].LastTS)
		}
		if rows[0].Status != "no plan yet" || rows[0].Tone != "phase" {
			t.Errorf("Status/Tone = %q/%q, want 'no plan yet'/'phase'", rows[0].Status, rows[0].Tone)
		}
	})

}

// TestStatusLineRowOn pins row.On: the candidate's short name when the set
// holds the token, its harness when it does not, the server suffix for a
// remote runner, and empty when the row has no candidate at all.
func TestStatusLineRowOn(t *testing.T) {
	t.Parallel()

	now := baseTime
	tests := []struct {
		name string
		b    BindingStatus
		want string
	}{
		{
			name: "named local",
			b:    BindingStatus{BuilderCandidate: "opencode/cline-pass/glm-5.3-flash", BuilderName: "glm-5.3-flash"},
			want: "glm-5.3-flash",
		},
		{
			name: "named remote",
			b:    BindingStatus{BuilderCandidate: "opencode/cline-pass/glm-5.3-flash", BuilderName: "glm-5.3-flash", Server: "contabo"},
			want: "glm-5.3-flash@contabo",
		},
		{
			name: "retired token",
			b:    BindingStatus{BuilderCandidate: "opencode/cline-pass/glm-5.3-flash"},
			want: "opencode",
		},
		{
			name: "no candidate",
			b:    BindingStatus{},
			want: "",
		},
		{
			name: "no candidate on a server",
			b:    BindingStatus{Server: "contabo"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := StatusLineRows(Report{Bindings: []BindingStatus{tt.b}}, now)
			if len(rows) != 1 {
				t.Fatalf("len(rows) = %d, want 1", len(rows))
			}
			if rows[0].On != tt.want {
				t.Errorf("On = %q, want %q", rows[0].On, tt.want)
			}
		})
	}
}

func TestStatusLineRowsEmptyDoc(t *testing.T) {
	t.Parallel()

	now := baseTime

	t.Run("empty report -> [] (not nil) once wrapped in StatusLineDoc and marshalled", func(t *testing.T) {
		rows := StatusLineRows(Report{}, now)
		if rows == nil {
			t.Fatal("StatusLineRows returned nil slice, want non-nil empty slice")
		}
		doc := StatusLineDoc{
			Planner: nil,
			Now:     now.UTC(),
			Rows:    rows,
		}
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		s := string(data)
		if !strings.Contains(s, `"planner":null`) {
			t.Errorf("json %q does not contain '\"planner\":null'", s)
		}
		if !strings.Contains(s, `"rows":[]`) {
			t.Errorf("json %q does not contain '\"rows\":[]'", s)
		}
	})

}

func TestStatusLineRowsPending(t *testing.T) {
	t.Parallel()

	now := baseTime

	t.Run("delivered report (Pending nil) -> needs_you false, report_in true, report_round = its round", func(t *testing.T) {
		b := BindingStatus{
			Name:    "worker",
			Round:   4,
			Display: "ACTIVE",
			LastPayload: &LastEvent{
				Round:     3,
				Kind:      store.KindReport,
				Direction: store.DirToPlanner,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want false (the report reached the chat)", rows[0].NeedsYou)
		}
		if !rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want true", rows[0].ReportIn)
		}
		if rows[0].ReportRound != 3 {
			t.Errorf("ReportRound = %d, want 3", rows[0].ReportRound)
		}
		if rows[0].Actor != "builder" {
			t.Errorf("Actor = %q, want 'builder'", rows[0].Actor)
		}
		if rows[0].Status != "REPORT IN" || rows[0].Tone != "report" {
			t.Errorf("Status/Tone = %q/%q, want 'REPORT IN'/'report'", rows[0].Status, rows[0].Tone)
		}
		if rows[0].Reason != "" {
			t.Errorf("Reason = %q, want empty (nothing to explain)", rows[0].Reason)
		}
	})

	t.Run("pending report, deliverer live, 5s old -> needs_you false, report_in false", func(t *testing.T) {
		b := BindingStatus{
			Name:             "worker",
			Round:            4,
			Display:          "ACTIVE",
			PlannerRoute:     "deliverer",
			PlannerRouteLive: true,
			Pending:          &PendingInfo{Round: 3, Kind: store.KindReport},
			LastPayload: &LastEvent{
				Round:     3,
				Kind:      store.KindReport,
				Direction: store.DirToPlanner,
				TS:        now.Add(-5 * time.Second),
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want false (the push has 60s to land)", rows[0].NeedsYou)
		}
		if rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want false (still pending)", rows[0].ReportIn)
		}
	})

}

func TestStatusLineRowsStalled(t *testing.T) {
	t.Parallel()

	now := baseTime

	t.Run("pending report, deliverer live, 61s old -> needs_you true", func(t *testing.T) {
		b := BindingStatus{
			Name:             "worker",
			Round:            4,
			Display:          "ACTIVE",
			PlannerRoute:     "deliverer",
			PlannerRouteLive: true,
			Pending:          &PendingInfo{Round: 3, Kind: store.KindReport},
			LastPayload: &LastEvent{
				Round:     3,
				Kind:      store.KindReport,
				Direction: store.DirToPlanner,
				TS:        now.Add(-61 * time.Second),
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if !rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want true (waited longer than PendingNeedsYouAfter)", rows[0].NeedsYou)
		}
		if rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want false (still pending)", rows[0].ReportIn)
		}
	})

}

func TestStatusLineRowsPull(t *testing.T) {
	t.Parallel()

	now := baseTime

	t.Run("pending report, route pull -> needs_you true at once", func(t *testing.T) {
		b := BindingStatus{
			Name:         "worker",
			Round:        4,
			Display:      "ACTIVE",
			PlannerRoute: "pull",
			Pending:      &PendingInfo{Round: 3, Kind: store.KindReport},
			LastPayload: &LastEvent{
				Round:     3,
				Kind:      store.KindReport,
				Direction: store.DirToPlanner,
				TS:        now,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if !rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want true (a pull route cannot be pushed to)", rows[0].NeedsYou)
		}
		if rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want false (still pending)", rows[0].ReportIn)
		}
	})

}

func TestStatusLineRowsNeedsYou(t *testing.T) {
	t.Parallel()

	now := baseTime

	t.Run("NEEDS YOU display with a plan payload -> needs_you true, report_in false, report_round 0", func(t *testing.T) {
		b := BindingStatus{
			Name:    "worker",
			Round:   2,
			Display: "NEEDS YOU",
			LastPayload: &LastEvent{
				Round:     2,
				Kind:      store.KindPlan,
				Direction: store.DirToBuilder,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if !rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want true", rows[0].NeedsYou)
		}
		if rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want false (no to-planner payload)", rows[0].ReportIn)
		}
		if rows[0].ReportRound != 0 {
			t.Errorf("ReportRound = %d, want 0", rows[0].ReportRound)
		}
	})

	t.Run("ACTIVE + plan to builder -> false, 0", func(t *testing.T) {
		b := BindingStatus{
			Name:    "worker",
			Round:   1,
			Display: "ACTIVE",
			LastPayload: &LastEvent{
				Round:     1,
				Kind:      store.KindPlan,
				Direction: store.DirToBuilder,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want false", rows[0].NeedsYou)
		}
		if rows[0].ReportRound != 0 {
			t.Errorf("ReportRound = %d, want 0", rows[0].ReportRound)
		}
	})
}

// TestRenderStatusLineSharesTheRowRule is the Claude Code line is
// rendered from StatusLineRows, so its text carries exactly the row's shown
// round (report_round when > 0, else round) and the row's word -- no word for
// ACTIVE, REPORT IN for a delivered report, NEEDS YOU for a stalled pending or
// a NEEDS YOU display.
// TestRenderStatusLineSharesTheRowRule fixtures.
var srrNow = baseTime
var srrRep = Report{Bindings: []BindingStatus{
	{
		Name:             "active",
		Round:            2,
		Display:          "ACTIVE",
		BuilderCandidate: "agy",
		RoundStart:       srrNow.Add(-3 * time.Minute),
		LastPayload:      &LastEvent{TS: srrNow.Add(-3 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
	},
	{
		Name:             "delivered",
		Round:            5,
		Display:          "ACTIVE",
		BuilderCandidate: "agy",
		RoundStart:       srrNow.Add(-4 * time.Minute),
		LastPayload:      &LastEvent{TS: srrNow.Add(-4 * time.Minute), Round: 4, Kind: store.KindReport, Direction: store.DirToPlanner},
	},
	{
		Name:             "stalled",
		Round:            7,
		Display:          "ACTIVE",
		BuilderCandidate: "agy",
		PlannerRoute:     "deliverer",
		PlannerRouteLive: true,
		Pending:          &PendingInfo{Round: 6, Kind: store.KindReport},
		RoundStart:       srrNow.Add(-7 * time.Minute),
		LastPayload:      &LastEvent{TS: srrNow.Add(-2 * time.Minute), Round: 6, Kind: store.KindReport, Direction: store.DirToPlanner},
	},
	{
		Name:             "stuck",
		Round:            3,
		Display:          "NEEDS YOU",
		BuilderCandidate: "agy",
		RoundStart:       srrNow.Add(-2 * time.Minute),
		LastPayload:      &LastEvent{TS: srrNow.Add(-2 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
	},
	{
		Name:             "paused",
		Round:            6,
		Display:          "PAUSED",
		BuilderCandidate: "agy",
		RoundStart:       srrNow.Add(-6 * time.Minute),
		LastPayload:      &LastEvent{TS: srrNow.Add(-1 * time.Minute), Round: 5, Kind: store.KindReport, Direction: store.DirToPlanner},
	},
}}
var srrCases = []struct {
	i     int
	round int
	word  string
	dot   string
}{
	{0, 2, "", "○"},
	{1, 4, "REPORT IN", "○"},
	{2, 6, "NEEDS YOU", "●"},
	{3, 3, "NEEDS YOU", "●"},
	{4, 5, "PAUSED", "○"},
}

func TestRenderStatusLineSharesTheRowRule(t *testing.T) {
	t.Parallel()

	rows := StatusLineRows(srrRep, srrNow)
	lines := splitLines(RenderStatusLine(srrRep, srrNow, 120))
	if len(rows) != len(lines) {
		t.Fatalf("got %d rows and %d lines, want one line per row", len(rows), len(lines))
	}

	for _, tc := range srrCases {
		row := rows[tc.i]
		shown := row.Round
		if row.ReportRound > 0 {
			shown = row.ReportRound
		}
		if shown != tc.round {
			t.Errorf("row %d shown round = %d, want %d", tc.i, shown, tc.round)
		}
		plain := stripSGR(lines[tc.i])
		if !strings.Contains(plain, "r"+strconv.Itoa(tc.round)) {
			t.Errorf("line %d %q does not carry the row's shown round r%d", tc.i, plain, tc.round)
		}
		if tc.word == "" {
			for _, absent := range []string{"ACTIVE", "REPORT IN", "NEEDS YOU"} {
				if strings.Contains(plain, absent) {
					t.Errorf("line %d %q must carry no word, found %q", tc.i, plain, absent)
				}
			}
		} else if !strings.Contains(plain, tc.word) {
			t.Errorf("line %d %q does not carry the row's word %q", tc.i, plain, tc.word)
		}
		if !strings.HasPrefix(plain, tc.dot+" ") {
			t.Errorf("line %d %q does not start with the %s dot", tc.i, plain, tc.dot)
		}
	}
}
