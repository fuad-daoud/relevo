package relevo

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
)

// TestStatsInputsAssemblesFromDB is cockpit C2b §7: StatsInputs reads the
// windowed rows, the DONE bindings and the availability history from the
// history file, which rt.DB is the shape of here.
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
		History: d,
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
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for a file that reads clean", warnings)
	}
}

// TestStatsInputsCombinesBothOrigins is Round 3 step 1: a display read combines
// every origin's rows, because what a stats panel shows is the window across
// the fleet rather than this machine's slice of it. This machine has imported a
// peer's outage and a peer's slow sample by the time it reads, and both belong
// in History and in the TTFT closure beside its own.
//
// It fails if a read is scoped to the reading machine's own origin: the peer's
// two rows then vanish and History counts one event where it counts two.
func TestStatsInputsCombinesBothOrigins(t *testing.T) {
	t.Parallel()

	d := openTestHistoryDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	const (
		ownOrigin   = "01OWNORIGIN0"
		peerOrigin  = "01PEERORIGIN"
		ownToken    = "opencode/test/model"
		peerToken   = "claude/peer/model"
		ownProvider = "opencode"
		peerProv    = "claude"
	)

	// This machine's own row, written with its own origin as every write is.
	if err := d.InsertAvailabilityEvent(ownOrigin, db.AvailabilityEvent{
		ID: db.NewID(), AtMS: db.UnixMS(now.Add(-time.Hour)), Kind: "rate_limited",
		Provider: ownProvider, Token: ownToken, Source: "planner",
	}); err != nil {
		t.Fatalf("own availability row: %v", err)
	}
	if err := d.InsertLatencySample(ownOrigin, db.LatencySample{
		ID: db.NewID(), AtMS: db.UnixMS(now.Add(-time.Hour)), Token: ownToken,
		Host: "here", TTFTMS: 100,
	}); err != nil {
		t.Fatalf("own latency row: %v", err)
	}

	// The peer's rows, as an import leaves them: another machine's origin on a
	// row this machine holds but does not own.
	if err := d.InsertAvailabilityEvent(peerOrigin, db.AvailabilityEvent{
		ID: db.NewID(), AtMS: db.UnixMS(now.Add(-2 * time.Hour)), Kind: "rate_limited",
		Provider: peerProv, Token: peerToken, Source: "planner",
	}); err != nil {
		t.Fatalf("peer availability row: %v", err)
	}
	if err := d.InsertLatencySample(peerOrigin, db.LatencySample{
		ID: db.NewID(), AtMS: db.UnixMS(now.Add(-2 * time.Hour)), Token: peerToken,
		Host: "peer", TTFTMS: 9000,
	}); err != nil {
		t.Fatalf("peer latency row: %v", err)
	}

	rt := Runtime{
		DB:      d,
		History: d,
		Now:     func() time.Time { return now },
	}

	in, warnings, err := StatsInputs(rt, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("StatsInputs: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for a file that reads clean", warnings)
	}

	// Both origins' events, oldest first, in one History.
	if len(in.History.Events) != 2 {
		t.Fatalf("History has %d events, want 2 (both origins): %+v", len(in.History.Events), in.History.Events)
	}
	if in.History.Events[0].Token != peerToken || in.History.Events[1].Token != ownToken {
		t.Errorf("events = [%s %s], want the peer's older one first", in.History.Events[0].Token, in.History.Events[1].Token)
	}
	// HourCounts is what the panel renders, so the peer's hour must be there.
	if got := availability.HourCounts(in.History, peerProv, availability.RateLimited, time.UTC); len(got) == 0 {
		t.Error("HourCounts has no hour for the peer provider, so the panel would not show it")
	}

	// TTFT is a per-token closure, so each origin's own token answers with its
	// own p50 and neither reads the other's.
	if got, ok := in.TTFT(ownToken); !ok || got != 100 {
		t.Errorf("TTFT(own) = %d, %t, want 100, true", got, ok)
	}
	if got, ok := in.TTFT(peerToken); !ok || got != 9000 {
		t.Errorf("TTFT(peer) = %d, %t, want 9000, true", got, ok)
	}
}

// TestStatsInputsLatencyReadFailureIsAWarning: the history read can fail, and
// when it does StatsInputs warns and carries on. A closed file is the failure --
// it is the one a broken handle produces, and the retired documents' decode
// error is gone with them.
func TestStatsInputsLatencyReadFailureIsAWarning(t *testing.T) {
	t.Parallel()

	good := openTestHistoryDB(t)
	closed := openTestHistoryDB(t)
	if err := closed.Close(); err != nil {
		t.Fatalf("close the history file: %v", err)
	}

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	rt := Runtime{DB: good, History: closed, Now: func() time.Time { return now }}

	_, warnings, err := StatsInputs(rt, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("StatsInputs: %v", err)
	}
	if !slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, "latency") }) {
		t.Errorf("warnings = %v, want one about latency", warnings)
	}
}

// TestStatsInputsEmptyLatencyIsNoWarning: an empty latency table is not an
// error and not a warning -- a fresh install has probed nothing yet.
func TestStatsInputsEmptyLatencyIsNoWarning(t *testing.T) {
	t.Parallel()

	d := openTestHistoryDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	rt := Runtime{DB: d, History: d, Now: func() time.Time { return now }}

	_, warnings, err := StatsInputs(rt, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("StatsInputs: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for empty history tables", warnings)
	}
}
