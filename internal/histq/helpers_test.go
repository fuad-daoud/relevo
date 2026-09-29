package histq

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The shared fixture: ten literal rows across three bindings, two repos, two
// features and three builders, in the order the tests index them by, so the
// builders below concatenate to it exactly. Rows 3 and 8 carry an "unknown"
// cost basis; row 5 has no cost, no commits and no duration.
const (
	fxRepoAPI         = "https://github.com/o/api"
	fxRepoWeb         = "https://github.com/o/web"
	fxBuilderAgy      = "agy/antigravity/sonnet"
	fxBuilderClaude   = "claude/remote/opus"
	fxBuilderOpencode = "opencode/local#high"
)

// fxLoc renders day keys in UTC, so the fixture's dates are the test's.
var fxLoc = time.UTC

var parseNow = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func fxDay(day, hour int) time.Time {
	return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC)
}

func fxInt64(v int64) *int64     { return &v }
func fxFloat(v float64) *float64 { return &v }
func fxStr(v string) *string     { return &v }
func fxInt(v int) *int           { return &v }

// fixtureRows is the ten-row fixture, newest-first order deliberately not
// assumed: Group and Apply must work on any order.
func fixtureRows() []db.RoundRow {
	rows := fixtureAPIRows()
	rows = append(rows, fixtureWebRows()...)
	rows = append(rows, fixtureInfraRows()...)
	return append(rows, fixtureAPIRound4()...)
}

func fixtureAPIRows() []db.RoundRow {
	return []db.RoundRow{
		{
			BindingID: "b1", BindingName: "api",
			Repo: fxStr(fxRepoAPI), Feature: fxStr("checkout"), Ticket: fxStr("o/r#607"),
			Number: 1, StartedAt: fxDay(20, 9), ClosedAt: fxClosed(20, 9, 10*time.Minute),
			Outcome:   db.OutcomeReported,
			Candidate: fxStr(fxBuilderAgy), Harness: fxStr("agy"),
			Provider: fxStr("antigravity"), Model: fxStr("sonnet"),
			Mode:    fxStr("pane"),
			Commits: fxInt(2), Tree: fxStr("clean"), GateResult: fxStr("pass"),
			CostUSD: fxFloat(1.00), CostBasis: fxStr("measured"),
			InTokens: fxInt64(100), CacheTokens: fxInt64(200),
			WriteTokens: fxInt64(300), OutTokens: fxInt64(400),
			ReportOutcome: fxStr("done"), Server: fxStr("contabo"),
			DurationMS: fxInt64(600_000),
		},
		{
			BindingID: "b1", BindingName: "api",
			Repo: fxStr(fxRepoAPI), Feature: fxStr("checkout"), Ticket: fxStr("o/r#607"),
			Number: 2, StartedAt: fxDay(20, 8), ClosedAt: fxClosed(20, 8, 20*time.Minute),
			Outcome:   db.OutcomeReported,
			Candidate: fxStr(fxBuilderAgy), Harness: fxStr("agy"),
			Provider: fxStr("antigravity"), Model: fxStr("sonnet"),
			Mode:    fxStr("pane"),
			Commits: fxInt(1), GateResult: fxStr("fail"),
			CostUSD: fxFloat(2.00), CostBasis: fxStr("measured"),
			InTokens:      fxInt64(1000),
			ReportOutcome: fxStr("done"), Server: fxStr("contabo"),
			DurationMS: fxInt64(1_200_000),
		},
		{
			BindingID: "b1", BindingName: "api",
			Repo: fxStr(fxRepoAPI), Feature: fxStr("search"),
			Number: 3, StartedAt: fxDay(20, 10), ClosedAt: fxClosed(20, 10, 30*time.Minute),
			Outcome:   db.OutcomeHalted,
			Candidate: fxStr(fxBuilderClaude), Harness: fxStr("claude"),
			Provider: fxStr("anthropic"), Model: fxStr("opus"),
			Mode:    fxStr("headless"),
			Commits: fxInt(0),
			CostUSD: fxFloat(3.00), CostBasis: fxStr("unknown"),
			InTokens: fxInt64(500), Server: fxStr("contabo"),
			DurationMS: fxInt64(1_800_000),
		},
	}
}

func fixtureWebRows() []db.RoundRow {
	return []db.RoundRow{
		{
			BindingID: "b2", BindingName: "web",
			Repo: fxStr(fxRepoWeb), Feature: fxStr("search"), Ticket: fxStr("#42"),
			Number: 1, StartedAt: fxDay(19, 9), ClosedAt: fxClosed(19, 9, 40*time.Minute),
			Outcome:   db.OutcomeReported,
			Candidate: fxStr(fxBuilderClaude), Harness: fxStr("claude"),
			Provider: fxStr("anthropic"), Model: fxStr("opus"),
			Mode:    fxStr("headless"),
			Commits: fxInt(3), GateResult: fxStr("pass"),
			CostUSD: fxFloat(0.50), CostBasis: fxStr("measured"),
			InTokens: fxInt64(1000), CacheTokens: fxInt64(500),
			WriteTokens: fxInt64(500), OutTokens: fxInt64(0),
			ReportOutcome: fxStr("deferred"), Server: fxStr("contabo"),
			DurationMS: fxInt64(2_400_000),
		},
		{
			BindingID: "b2", BindingName: "web",
			Repo: fxStr(fxRepoWeb), Feature: fxStr("search"),
			Number: 2, StartedAt: fxDay(20, 11),
			Outcome:   db.OutcomeOpen,
			Candidate: fxStr(fxBuilderOpencode), Harness: fxStr("opencode"),
			Provider: fxStr("openai"), Model: fxStr("gpt"),
			Mode:   fxStr("remote"),
			Server: fxStr("contabo"),
		},
		{
			BindingID: "b2", BindingName: "web",
			Repo: fxStr(fxRepoWeb), Feature: fxStr("checkout"),
			Number: 3, StartedAt: fxDay(19, 10), ClosedAt: fxClosed(19, 10, 50*time.Minute),
			Outcome:   db.OutcomeExited,
			Candidate: fxStr(fxBuilderOpencode), Harness: fxStr("opencode"),
			Provider: fxStr("openai"), Model: fxStr("gpt"),
			Mode:    fxStr("remote"),
			Commits: fxInt(1),
			CostUSD: fxFloat(0.25), CostBasis: fxStr("estimated"),
			InTokens: fxInt64(3000), Server: fxStr("local"),
			DurationMS: fxInt64(3_000_000),
		},
	}
}

func fixtureInfraRows() []db.RoundRow {
	return []db.RoundRow{
		{
			BindingID: "b3", BindingName: "infra",
			Repo: fxStr(fxRepoAPI), Feature: fxStr("checkout"),
			Number: 1, StartedAt: fxDay(19, 11), ClosedAt: fxClosed(19, 11, 60*time.Minute),
			Outcome:   db.OutcomeReported,
			Candidate: fxStr(fxBuilderAgy), Harness: fxStr("agy"),
			Provider: fxStr("antigravity"), Model: fxStr("sonnet"),
			Mode:    fxStr("pane"),
			Commits: fxInt(4), Tree: fxStr("dirty"), GateResult: fxStr("timeout"),
			CostUSD: fxFloat(4.00), CostBasis: fxStr("measured"),
			InTokens: fxInt64(4000), ReportOutcome: fxStr("done"), Server: fxStr("local"),
			DurationMS: fxInt64(3_600_000),
		},
		{
			BindingID: "b3", BindingName: "infra",
			Repo: fxStr(fxRepoAPI), Feature: fxStr("search"),
			Number: 2, StartedAt: fxDay(20, 12), ClosedAt: fxClosed(20, 12, 70*time.Minute),
			Outcome:   db.OutcomeHalted,
			Candidate: fxStr(fxBuilderOpencode), Harness: fxStr("opencode"),
			Provider: fxStr("openai"), Model: fxStr("gpt"),
			Mode:    fxStr("remote"),
			Commits: fxInt(2),
			CostUSD: fxFloat(1.50), CostBasis: fxStr("unknown"),
			InTokens: fxInt64(1500), Server: fxStr("local"),
			DurationMS: fxInt64(4_200_000),
		},
		{
			BindingID: "b3", BindingName: "infra",
			Repo: fxStr(fxRepoWeb), Feature: fxStr("checkout"),
			Number: 3, StartedAt: fxDay(20, 13), ClosedAt: fxClosed(20, 13, 80*time.Minute),
			Outcome:   db.OutcomeReported,
			Candidate: fxStr(fxBuilderClaude), Harness: fxStr("claude"),
			Provider: fxStr("anthropic"), Model: fxStr("opus"),
			Mode:    fxStr("headless"),
			Commits: fxInt(5), GateResult: fxStr("pass"),
			CostUSD: fxFloat(3.50), CostBasis: fxStr("measured"),
			InTokens: fxInt64(2500), ReportOutcome: fxStr("done"), Server: fxStr("local"),
			DurationMS: fxInt64(4_800_000),
		},
	}
}

func fixtureAPIRound4() []db.RoundRow {
	return []db.RoundRow{
		{
			BindingID: "b1", BindingName: "api",
			Repo: fxStr(fxRepoWeb), Feature: fxStr("search"), Ticket: fxStr("#42"),
			Number: 4, StartedAt: fxDay(19, 12), ClosedAt: fxClosed(19, 12, 90*time.Minute),
			Outcome:   db.OutcomeReported,
			Candidate: fxStr(fxBuilderAgy), Harness: fxStr("agy"),
			Provider: fxStr("antigravity"), Model: fxStr("sonnet"),
			Mode:    fxStr("pane"),
			Commits: fxInt(0), GateResult: fxStr("error"),
			CostUSD: fxFloat(0.75), CostBasis: fxStr("estimated"),
			InTokens: fxInt64(750), ReportOutcome: fxStr("halted"), Server: fxStr("local"),
			DurationMS: fxInt64(5_400_000),
		},
	}
}

func fxClosed(day, hour int, d time.Duration) *time.Time {
	t := fxDay(day, hour).Add(d)
	return &t
}

func eqStr(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}
