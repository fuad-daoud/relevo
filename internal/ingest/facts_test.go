package ingest

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestRoundFactsFromFixtureRound1(t *testing.T) {
	ts := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return tm
	}

	events := []store.LogEntry{
		{TS: ts("2026-09-10T10:00:00Z"), Round: 1, Kind: store.KindPick},
		{TS: ts("2026-09-10T10:00:01Z"), Round: 1, Kind: store.KindPrompt, Tier: "high"},
		{TS: ts("2026-09-10T10:00:02Z"), Round: 1, Kind: store.KindDiff, Commits: 2, Tree: "clean"},
		{
			TS: ts("2026-09-10T10:00:03Z"), Round: 1, Kind: store.KindReport, Outcome: "done",
			Gate: &store.GateRecord{Command: "make check", Result: "pass", ExitCode: 0, DurationMS: 900, LogPath: "001-gate.log"},
			Usage: &usage.Usage{
				Harness: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash",
				Tokens:  usage.Tokens{In: 1000, CacheRead: 5000, CacheWrite: 0, Out: 200},
				Cost:    usage.Cost{USD: 0.12, Basis: usage.Measured},
				Samples: 1,
			},
		},
		// Round 2's entries must not leak into round 1's facts.
		{TS: ts("2026-09-10T10:01:00Z"), Round: 2, Kind: store.KindPrompt, Tier: "low"},
	}

	r := roundFacts(events, 1)

	if !r.StartedAt.Equal(ts("2026-09-10T10:00:01Z")) {
		t.Errorf("StartedAt = %v, want the plan entry's ts", r.StartedAt)
	}
	if r.ClosedAt == nil || !r.ClosedAt.Equal(ts("2026-09-10T10:00:03Z")) {
		t.Errorf("ClosedAt = %v, want the report entry's ts", r.ClosedAt)
	}
	checkPtr(t, "Tier", r.Tier, "high")
	checkPtr(t, "Commits", r.Commits, 2)
	checkPtr(t, "Tree", r.Tree, "clean")
	checkPtr(t, "GateResult", r.GateResult, "pass")
	checkPtr(t, "GateExit", r.GateExit, 0)
	checkPtr(t, "GateDurationMS", r.GateDurationMS, int64(900))
	checkPtr(t, "InTokens", r.InTokens, int64(1000))
	checkPtr(t, "CacheTokens", r.CacheTokens, int64(5000))
	checkPtr(t, "WriteTokens", r.WriteTokens, int64(0))
	checkPtr(t, "OutTokens", r.OutTokens, int64(200))
	checkPtr(t, "CostUSD", r.CostUSD, 0.12)
	checkPtr(t, "CostBasis", r.CostBasis, "measured")
	checkPtr(t, "ReportOutcome", r.ReportOutcome, "done")
}

func checkPtr[T comparable](t *testing.T, field string, got *T, want T) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %v", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %v, want %v", field, *got, want)
	}
}
