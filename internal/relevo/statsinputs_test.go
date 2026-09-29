package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestStatsInputsAssemblesFromDB is cockpit C2b §7: StatsInputs reads the
// windowed rows, the DONE bindings and the availability history from rt.DB,
// and a latency read failure is a warning, never an error.
func TestStatsInputsAssemblesFromDB(t *testing.T) {
	t.Parallel()

	d := openTestHistoryDB(t)

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)

	doneID, err := d.UpsertBinding(db.Binding{
		Name: "landed", CWD: "/work/landed", BuilderMode: "pane",
		FinalState: ptr("done"), CreatedAt: now, IngestSource: db.IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding landed: %v", err)
	}
	openID, err := d.UpsertBinding(db.Binding{
		Name: "open", CWD: "/work/open", BuilderMode: "pane",
		CreatedAt: now, IngestSource: db.IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding open: %v", err)
	}

	tok := "opencode/test/model"
	if _, err := d.UpsertRound(db.Round{
		BindingID: doneID, Number: 1, StartedAt: now.Add(-time.Hour),
		Outcome: db.OutcomeReported, Candidate: ptr(tok),
	}); err != nil {
		t.Fatalf("UpsertRound in-window: %v", err)
	}
	// Older than the window: StatsInputs must not return it.
	if _, err := d.UpsertRound(db.Round{
		BindingID: openID, Number: 1, StartedAt: now.Add(-30 * 24 * time.Hour),
		Outcome: db.OutcomeReported,
	}); err != nil {
		t.Fatalf("UpsertRound out-of-window: %v", err)
	}

	rt := Runtime{
		DB:      d,
		Latency: badJSONKV{},
		Now:     func() time.Time { return now },
	}

	in, warnings, err := StatsInputs(rt, since)
	if err != nil {
		t.Fatalf("StatsInputs: %v", err)
	}
	if len(in.Rows) != 1 {
		t.Errorf("Rows = %d, want 1 (windowed to since)", len(in.Rows))
	}
	if !in.Landed[doneID] {
		t.Errorf("Landed[%q] = false, want true (a DONE binding)", doneID)
	}
	if in.Landed[openID] {
		t.Errorf("Landed[%q] = true, want false (the binding is not DONE)", openID)
	}
	if in.Since != since {
		t.Errorf("Since = %v, want %v", in.Since, since)
	}
	if in.Until != now {
		t.Errorf("Until = %v, want rt.Now() %v", in.Until, now)
	}
	if in.Loc != time.Local {
		t.Errorf("Loc = %v, want time.Local", in.Loc)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "latency") {
		t.Errorf("warnings = %v, want one about latency", warnings)
	}
}

// TestStatsInputsEmptyLatencyIsNoWarning: an absent latency record is not an
// error and not a warning -- a fresh install has probed nothing yet.
func TestStatsInputsEmptyLatencyIsNoWarning(t *testing.T) {
	t.Parallel()

	d := openTestHistoryDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	rt := Runtime{DB: d, Latency: testGateKV(t), Now: func() time.Time { return now }}

	_, warnings, err := StatsInputs(rt, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("StatsInputs: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for an empty latency record", warnings)
	}
}
