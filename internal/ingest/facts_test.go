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

// The next five tests pin the row contract: a round's figures are
// the newest report entry's usage plus every earlier segment of that round
// -- each switch entry that carries a usage, and the PriorTokens a report
// entry carries for a round another machine ran. Entries are built by hand;
// the assertions are db.Round's own fields.

func TestRoundFactsSumsANudgedRoundsSegments(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPrompt},
		// The nudge's outgoing segment, recorded on its switch entry.
		{Round: 1, Kind: store.KindSwitch, Confirmed: true,
			Note:  "nudged builder (ended its turn): resumed session S1",
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}}},
		{Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind,
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}, Cost: usage.Cost{USD: 0.40, Basis: usage.Measured}}},
	}

	r := roundFacts(events, 1)

	checkPtr(t, "InTokens", r.InTokens, int64(300))
	checkPtr(t, "CacheTokens", r.CacheTokens, int64(0))
	checkPtr(t, "WriteTokens", r.WriteTokens, int64(0))
	checkPtr(t, "OutTokens", r.OutTokens, int64(80))
	checkPtr(t, "CostUSD", r.CostUSD, 0.65)
	checkPtr(t, "CostBasis", r.CostBasis, "measured")
}

func TestRoundFactsSumsASwitchedRoundsSegments(t *testing.T) {
	events := []store.LogEntry{
		// Round 1's own nudge: its tokens belong to round 1's row, never to
		// round 2's, however close in the log the entries sit.
		{Round: 1, Kind: store.KindPrompt},
		{Round: 1, Kind: store.KindSwitch, Confirmed: true,
			Note:  "nudged builder (ended its turn): resumed session S1",
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 900, Out: 900}, Cost: usage.Cost{USD: 9.00, Basis: usage.Measured}}},
		{Round: 2, Kind: store.KindPrompt},
		// This round's own mid-round switch: no nudge in its note, a full
		// segment all the same.
		{Round: 2, Kind: store.KindSwitch, Confirmed: true,
			Note:  "switched to agy/test/m: rate-limited",
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}}},
		{Round: 2, Kind: store.KindReport, Direction: store.DirToMasterMind,
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}, Cost: usage.Cost{USD: 0.40, Basis: usage.Measured}}},
	}

	r := roundFacts(events, 2)

	checkPtr(t, "InTokens", r.InTokens, int64(300))
	checkPtr(t, "OutTokens", r.OutTokens, int64(80))
	checkPtr(t, "CostUSD", r.CostUSD, 0.65)
	checkPtr(t, "CostBasis", r.CostBasis, "measured")
}

func TestRoundFactsRemoteRoundCountsPriorTokens(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPrompt},
		// A remote round's report: the client's own usage is the newest
		// segment's bill; PriorTokens carries what another machine ran, as
		// tokens only -- no bill rides the wire, so the row's cost is
		// unknown rather than the priced remainder.
		{Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind,
			Usage:       &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.10, Basis: usage.Measured}},
			PriorTokens: &usage.Tokens{In: 400, Out: 150}},
	}

	r := roundFacts(events, 1)

	checkPtr(t, "InTokens", r.InTokens, int64(500))
	checkPtr(t, "OutTokens", r.OutTokens, int64(200))
	checkPtr(t, "CostUSD", r.CostUSD, 0.0)
	checkPtr(t, "CostBasis", r.CostBasis, "unknown")
}

func TestRoundFactsSingleSegmentUnchanged(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPrompt},
		{Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind,
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}, Cost: usage.Cost{USD: 0.40, Basis: usage.Measured}}},
	}

	r := roundFacts(events, 1)

	checkPtr(t, "InTokens", r.InTokens, int64(200))
	checkPtr(t, "OutTokens", r.OutTokens, int64(30))
	checkPtr(t, "CostUSD", r.CostUSD, 0.40)
	checkPtr(t, "CostBasis", r.CostBasis, "measured")
}

func TestRoundFactsPlanLaneSingleSegmentUnchanged(t *testing.T) {
	planUsage := &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}, Cost: usage.Cost{USD: 0.40, Basis: usage.Measured, Plan: true}}
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPrompt},
		{Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind, Usage: planUsage},
	}

	r := roundFacts(events, 1)

	checkPtr(t, "InTokens", r.InTokens, int64(200))
	checkPtr(t, "OutTokens", r.OutTokens, int64(30))
	checkPtr(t, "CostUSD", r.CostUSD, 0.40)
	checkPtr(t, "CostBasis", r.CostBasis, "measured")
}

func TestRoundFactsPlanLaneWithNudgeSegmentIsUnknown(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPrompt},
		{Round: 1, Kind: store.KindSwitch, Confirmed: true,
			Note:  "nudged builder (ended its turn): resumed session S1",
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}}},
		{Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind,
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}, Cost: usage.Cost{USD: 0.40, Basis: usage.Measured, Plan: true}}},
	}

	r := roundFacts(events, 1)

	checkPtr(t, "InTokens", r.InTokens, int64(300))
	checkPtr(t, "OutTokens", r.OutTokens, int64(80))
	checkPtr(t, "CostUSD", r.CostUSD, 0.0)
	checkPtr(t, "CostBasis", r.CostBasis, "unknown")
}

func TestRoundFactsReportWithoutUsageKeepsNoFigures(t *testing.T) {
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPrompt},
		{Round: 1, Kind: store.KindSwitch, Confirmed: true,
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}, Cost: usage.Cost{USD: 0.25, Basis: usage.Measured}}},
		// A report entry that carries no usage: the row keeps no figures --
		// a round is never given a partial total in place of an absent
		// report segment.
		{Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind},
	}

	r := roundFacts(events, 1)

	if r.InTokens != nil || r.CacheTokens != nil || r.WriteTokens != nil || r.OutTokens != nil {
		t.Errorf("tokens = %v/%v/%v/%v, want no figures at all", r.InTokens, r.CacheTokens, r.WriteTokens, r.OutTokens)
	}
	if r.CostUSD != nil || r.CostBasis != nil {
		t.Errorf("cost = %v/%v, want no figures at all", r.CostUSD, r.CostBasis)
	}
}

func TestRoundFactsStopEntryStampsClosedAt(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	events := []store.LogEntry{
		{Round: 1, Kind: store.KindPrompt, TS: ts.Add(-time.Hour)},
		{Round: 1, Kind: store.KindStop, TS: ts},
		{Round: 1, Kind: store.KindStop, TS: ts.Add(time.Minute)},
	}
	r := roundFacts(events, 1)
	if r.ClosedAt == nil || !r.ClosedAt.Equal(ts) {
		t.Errorf("ClosedAt = %v, want %v", r.ClosedAt, ts)
	}
}
