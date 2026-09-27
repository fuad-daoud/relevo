package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestLogLineWithoutUsageIsTodaysFormat(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	e := store.LogEntry{TS: ts, Round: 4, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/p/004-report.md", Note: "scraped"}
	want := ts.Local().Format("2006-01-02 15:04:05") + "  round 4   to_planner report    /p/004-report.md scraped"
	if got := LogLine(e); got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}
	if strings.Contains(LogLine(e), "\n") {
		t.Error("no second line without usage")
	}
}

func TestLogLineWithUsageAddsSecondLine(t *testing.T) {
	t.Parallel()

	e := store.LogEntry{TS: time.Now(), Round: 4, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/p/004-report.md",
		Usage: &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 60_000,
			Tokens: usage.Tokens{In: 100, Out: 10}, Cost: usage.Cost{USD: 0.5, Basis: usage.Measured}, Samples: 1}}
	got := LogLine(e)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("want two lines, got %q", got)
	}
	if !strings.HasPrefix(lines[1], strings.Repeat(" ", 21)+"⎿ ") {
		t.Errorf("second line must be indented under the round column: %q", lines[1])
	}
	if !strings.HasSuffix(lines[1], usage.Line(*e.Usage)) {
		t.Errorf("second line must end with usage.Line: %q", lines[1])
	}
}

func TestLogLineLateSuffix(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	base := store.LogEntry{TS: ts, Round: 4, Direction: store.DirToBuilder, Kind: store.KindPrompt, Path: "/p/004-report.md", Note: "nudge"}
	notLate := base
	notLate.Late = false
	late := base
	late.Late = true

	wantNotLate := ts.Local().Format("2006-01-02 15:04:05") + "  round 4   to_runner  prompt    /p/004-report.md nudge"
	if got := LogLine(notLate); got != wantNotLate {
		t.Errorf("\n got  %q\n want %q", got, wantNotLate)
	}

	gotLate := LogLine(late)
	wantLate := wantNotLate + " late"
	if gotLate != wantLate {
		t.Errorf("\n got  %q\n want %q", gotLate, wantLate)
	}
	if !strings.HasSuffix(gotLate, "nudge late") {
		t.Errorf("expected suffix %q, got %q", "nudge late", gotLate)
	}
}

func TestLogLineOutcomeAndFlagged(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	e := store.LogEntry{
		TS:        ts,
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindReport,
		Path:      "/p/001-report.md",
		Note:      "noreport",
		Outcome:   "halted",
		Flagged:   2,
		Late:      true,
	}
	got := LogLine(e)
	want := ts.Local().Format("2006-01-02 15:04:05") + "  round 1   to_planner report    /p/001-report.md noreport outcome=halted flagged=2 late"
	if got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}
}

func TestLogLineClassify(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	e := store.LogEntry{
		TS:        ts,
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindReport,
		Path:      "/p/001-report.md",
		Flagged:   3,
		FlaggedBy: "both",
		Classify:  &store.ClassifyRecord{Max: 0.94, Partial: true},
		Late:      true,
	}
	got := LogLine(e)
	wantSuffix := "flagged=3 by=both p=0.94 partial late"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("got %q, want suffix %q", got, wantSuffix)
	}

	eTimeout := store.LogEntry{
		TS:        ts,
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindReport,
		Path:      "/p/001-report.md",
		Flagged:   3,
		FlaggedBy: "regex",
		Classify:  &store.ClassifyRecord{Note: "classify: timeout"},
	}
	gotTimeout := LogLine(eTimeout)
	if strings.Contains(gotTimeout, "p=") {
		t.Errorf("got %q, want no p=", gotTimeout)
	}
}

func TestLogLineTierOnPlanOnly(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	plan := store.LogEntry{
		TS:        ts,
		Round:     1,
		Direction: store.DirToBuilder,
		Kind:      store.KindPrompt,
		Path:      "/p/001-plan.md",
		Tier:      "edit",
	}
	gotPlan := LogLine(plan)
	if !strings.HasSuffix(gotPlan, " tier=edit") {
		t.Errorf("expected suffix %q on plan line, got %q", " tier=edit", gotPlan)
	}

	report := store.LogEntry{
		TS:        ts,
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindReport,
		Path:      "/p/001-report.md",
		Tier:      "edit",
	}
	gotReport := LogLine(report)
	if strings.Contains(gotReport, "tier=") {
		t.Errorf("report entry must not print tier: %q", gotReport)
	}
}

// TestLogLineGateSuffix pins #132: a report entry with a Gate record appends
// " gate=<Result>"; an entry with none carries no such suffix.
func TestLogLineGateSuffix(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	withGate := store.LogEntry{
		TS: ts, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: "/p/001-report.md", Gate: &store.GateRecord{Result: "fail"},
	}
	got := LogLine(withGate)
	if !strings.HasSuffix(got, " gate=fail") {
		t.Errorf("expected suffix %q, got %q", " gate=fail", got)
	}

	noGate := withGate
	noGate.Gate = nil
	got2 := LogLine(noGate)
	if strings.Contains(got2, "gate=") {
		t.Errorf("no gate= expected without a Gate record: %q", got2)
	}
}

// TestLogLineSessionSuffix pins #147: a report entry with a BuilderSession
// appends " session=<kind>:<id8>" (the id cut to eight characters); an entry
// with none carries no suffix.
func TestLogLineSessionSuffix(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	withSession := store.LogEntry{
		TS: ts, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path:           "/p/001-report.md",
		BuilderSession: &store.BuilderSession{Kind: "claude", ID: "0123456789abcdef"},
	}
	if got := LogLine(withSession); !strings.HasSuffix(got, " session=claude:01234567") {
		t.Errorf("expected suffix %q, got %q", " session=claude:01234567", got)
	}

	short := withSession
	short.BuilderSession = &store.BuilderSession{Kind: "agy", ID: "conv-1"}
	if got := LogLine(short); !strings.HasSuffix(got, " session=agy:conv-1") {
		t.Errorf("expected suffix %q, got %q", " session=agy:conv-1", got)
	}

	noSession := withSession
	noSession.BuilderSession = nil
	if got := LogLine(noSession); strings.Contains(got, "session=") {
		t.Errorf("no session= expected without a BuilderSession: %q", got)
	}
}

// TestShortCPU pins #299: a round's CPU time at seconds resolution, from
// tenths below a minute up through minutes and hours. The 8550 row is the
// contract's own correction: 8550 ms is 8.55 s, an exact tie, and half-up
// (ms+50)/100 gives 86 tenths -> "8.6s".
func TestShortCPU(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ms   int64
		want string
	}{
		{0, ""},
		{-1, ""},
		{50, "0.1s"},
		{499, "0.5s"},
		{8550, "8.6s"},
		{19723, "19.7s"},
		{59_949, "59.9s"},
		{59_999, "1m00s"},
		{60_000, "1m00s"},
		{80_000, "1m20s"},
		{3_599_000, "59m59s"},
		{3_600_000, "1h00m"},
		{7_500_000, "2h05m"},
	}
	for _, c := range cases {
		if got := shortCPU(c.ms); got != c.want {
			t.Errorf("shortCPU(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}

// TestLogLineRusageUsesSecondsResolution pins #299's rendering: the report
// line carries the cpu time at seconds resolution rather than
// usage.ShortDuration's "<1m", and shortBytes still renders peak memory.
func TestLogLineRusageUsesSecondsResolution(t *testing.T) {
	t.Parallel()

	e := store.LogEntry{
		Kind:   store.KindReport,
		Rusage: &store.Rusage{CPUMS: 8550, PeakMemBytes: 341 << 20},
	}
	got := LogLine(e)
	want := "cpu 8.6s peak 358MB"
	if !strings.Contains(got, want) {
		t.Errorf("LogLine(%+v) = %q, want it to contain %q", e, got, want)
	}
}
