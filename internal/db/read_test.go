package db

import (
	"errors"
	"testing"
	"time"
)

// TestQueryFilters pins every Filter field's WHERE clause: each row names the
// field it constrains and the exact set of rounds it selects. A nil want means
// the row asserts only the row count.
func TestQueryFilters(t *testing.T) {
	s := seedDB(t)
	a1a2 := []pair{{"webshop", 1}, {"webshop", 2}, {"api", 1}, {"api", 2}}
	api := []pair{{"api", 1}, {"api", 2}}
	cases := []struct {
		name  string
		f     Filter
		want  []pair
		count int
	}{
		{"repo matches origin or common_dir", Filter{Repo: "https://example.test/a.git"}, a1a2, 0},
		{"repo matches common_dir", Filter{Repo: "/home/x/a/.git"}, a1a2, 0},
		{"feature", Filter{Feature: "checkout"}, []pair{{"webshop", 1}, {"webshop", 2}}, 0},
		{"binding name", Filter{Binding: "docs"}, []pair{{"docs", 1}, {"docs", 2}}, 0},
		{"mastermind session", Filter{MasterMind: "sess-1"}, nil, 6},
		{"harness", Filter{Harness: "opencode"}, api, 0},
		{"provider", Filter{Provider: "openrouter"}, api, 0},
		{"model", Filter{Model: "glm"}, api, 0},
		{"candidate", Filter{Candidate: "opencode/openrouter/glm"}, api, 0},
		{"outcome", Filter{Outcome: OutcomeHalted}, []pair{{"api", 1}, {"docs", 2}}, 0},
		{"report outcome", Filter{ReportOutcome: "halted"}, []pair{{"docs", 2}}, 0},
		{"binding state", Filter{State: "needs_you"}, []pair{{"docs", 1}, {"docs", 2}}, 0},
		{"gate result", Filter{GateResult: "fail"}, []pair{{"api", 1}}, 0},
		{"cost basis", Filter{CostBasis: "exact"}, []pair{{"webshop", 1}}, 0},
		{"round number", Filter{Round: 2}, []pair{{"webshop", 2}, {"api", 2}, {"docs", 2}}, 0},
		{"since", Filter{Since: s.day2}, []pair{{"webshop", 2}, {"api", 2}, {"docs", 2}}, 0},
		{"until", Filter{Until: s.day2}, []pair{{"webshop", 1}, {"api", 1}, {"docs", 1}}, 0},
		{"archived only", Filter{Archived: ptr(true)}, api, 0},
		{"live only", Filter{Archived: ptr(false)}, []pair{{"webshop", 1}, {"webshop", 2}, {"docs", 1}, {"docs", 2}}, 0},
		{"limit caps the newest first", Filter{Limit: 2, Newest: true}, nil, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.d.Query(c.f)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if c.want == nil {
				if len(got) != c.count {
					t.Fatalf("got %d rows, want %d", len(got), c.count)
				}
				return
			}
			assertPairs(t, got, c.want)
		})
	}
}

func TestQueryNoFilterReturnsAllNewestFirst(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Newest: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d rows, want 6", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].StartedAt.Before(got[i].StartedAt) {
			t.Errorf("row %d (started %v) is before row %d (started %v); want newest first", i-1, got[i-1].StartedAt, i, got[i].StartedAt)
		}
	}
}

func TestQueryHereUnresolvedIsInvalid(t *testing.T) {
	s := seedDB(t)
	_, err := s.d.Query(Filter{Here: "/some/cwd"})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestQueryReturnsSwitches(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Binding: "webshop", Round: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].Switches != 2 {
		t.Errorf("Switches = %d, want 2", got[0].Switches)
	}
}

// TestQueryRowCarriesTokensDurationAndMode pins the RoundRow columns the
// dashboard's sums need, and that a round with no closed_at has a nil
// DurationMS.
func TestQueryRowCarriesTokensDurationAndMode(t *testing.T) {
	d := openTestDB(t)
	seedTokenRounds(t, d)

	rows, err := d.Query(Filter{Newest: false})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	assertTokenRow(t, rows[0])
	assertOpenRoundRow(t, rows[1])
}

func seedTokenRounds(t *testing.T, d *DB) {
	t.Helper()
	server := "contabo"
	bindingID, err := d.UpsertBinding(Binding{
		Name: "remote-run", CWD: "/home/x/remote", BuilderMode: "headless",
		Server: &server, CreatedAt: time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC),
		IngestSource: IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	started := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	closed := started.Add(27 * time.Minute)
	in, cache, write, out := int64(1000), int64(2000), int64(3000), int64(4000)
	mode, report := "remote", "done"
	if _, err := d.UpsertRound(Round{
		BindingID: bindingID, Number: 1, StartedAt: started, ClosedAt: &closed,
		Outcome: OutcomeReported, InTokens: &in, CacheTokens: &cache,
		WriteTokens: &write, OutTokens: &out, Mode: &mode, ReportOutcome: &report,
	}); err != nil {
		t.Fatalf("UpsertRound 1: %v", err)
	}
	if _, err := d.UpsertRound(Round{
		BindingID: bindingID, Number: 2, StartedAt: started.Add(24 * time.Hour),
		Outcome: OutcomeOpen,
	}); err != nil {
		t.Fatalf("UpsertRound 2: %v", err)
	}
}

func assertTokenRow(t *testing.T, got RoundRow) {
	t.Helper()
	if got.Number != 1 {
		t.Fatalf("rows[0].Number = %d, want 1 (oldest first)", got.Number)
	}
	for _, c := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"InTokens", got.InTokens, 1000},
		{"CacheTokens", got.CacheTokens, 2000},
		{"WriteTokens", got.WriteTokens, 3000},
		{"OutTokens", got.OutTokens, 4000},
		{"DurationMS", got.DurationMS, 27 * 60 * 1000},
	} {
		switch {
		case c.got == nil:
			t.Errorf("%s = nil, want %d", c.name, c.want)
		case *c.got != c.want:
			t.Errorf("%s = %d, want %d", c.name, *c.got, c.want)
		}
	}
	if got.ReportOutcome == nil || *got.ReportOutcome != "done" {
		t.Errorf("ReportOutcome = %v, want done", got.ReportOutcome)
	}
	if got.Mode == nil || *got.Mode != "remote" {
		t.Errorf("Mode = %v, want remote", got.Mode)
	}
	if got.Server == nil || *got.Server != "contabo" {
		t.Errorf("Server = %v, want contabo", got.Server)
	}
}

func assertOpenRoundRow(t *testing.T, open RoundRow) {
	t.Helper()
	if open.Number != 2 {
		t.Fatalf("rows[1].Number = %d, want 2", open.Number)
	}
	if open.ClosedAt != nil || open.DurationMS != nil {
		t.Errorf("open round: ClosedAt = %v, DurationMS = %v; want both nil", open.ClosedAt, open.DurationMS)
	}
	if open.InTokens != nil || open.ReportOutcome != nil || open.Mode != nil {
		t.Errorf("open round: InTokens = %v, ReportOutcome = %v, Mode = %v; want all nil",
			open.InTokens, open.ReportOutcome, open.Mode)
	}
}

func TestBindingsNewestActivityFirst(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Bindings(Filter{})
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].LastActivity.Before(got[i].LastActivity) {
			t.Errorf("row %d (activity %v) is before row %d (activity %v); want newest first",
				i-1, got[i-1].LastActivity, i, got[i].LastActivity)
		}
	}
	for _, br := range got {
		if br.Rounds != 2 {
			t.Errorf("binding %s has %d rounds, want 2", br.Name, br.Rounds)
		}
	}
}

func TestBindingNewestByName(t *testing.T) {
	s := seedDB(t)
	d := s.d

	dup, err := d.UpsertBinding(Binding{
		Name: "webshop", CWD: "/home/x/webshop2", BuilderMode: "headless",
		CreatedAt: s.day2.Add(24 * time.Hour), IngestSource: IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding dup: %v", err)
	}

	br, ok, err := d.Binding("webshop")
	if err != nil {
		t.Fatalf("Binding: %v", err)
	}
	if !ok {
		t.Fatal("Binding not found")
	}
	if br.ID != dup {
		t.Errorf("Binding returned id %q, want the newest %q", br.ID, dup)
	}
}

func TestRoundsAscending(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Rounds(s.webshopID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rounds, want 2", len(got))
	}
	if got[0].Number != 1 || got[1].Number != 2 {
		t.Errorf("round numbers = [%d %d], want [1 2]", got[0].Number, got[1].Number)
	}
}

func TestArtifactMissingIsFalse(t *testing.T) {
	s := seedDB(t)
	_, ok, err := s.d.Artifact("nonexistent-round", ArtifactPlan)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if ok {
		t.Error("Artifact found for a nonexistent round, want not found")
	}
}

func TestTranscriptPaging(t *testing.T) {
	d := openTestDB(t)
	var recs []TranscriptRecord
	for i := 0; i < 5; i++ {
		recs = append(recs, TranscriptRecord{Seq: i, RecordJSON: "{}", Rendered: "line"})
	}
	if _, err := d.AppendTranscript(OwnerMasterMind, "sess-1", recs); err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}

	got, err := d.Transcript(OwnerMasterMind, "sess-1", 2, 2)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].Seq != 2 || got[1].Seq != 3 {
		t.Errorf("seqs = [%d %d], want [2 3]", got[0].Seq, got[1].Seq)
	}
}

func TestEventsRoundZeroIsAll(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	round1ID, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeReported))
	if err != nil {
		t.Fatalf("UpsertRound 1: %v", err)
	}
	round2ID, err := d.UpsertRound(newTestRound(bindingID, 2, OutcomeOpen))
	if err != nil {
		t.Fatalf("UpsertRound 2: %v", err)
	}

	evs := []Event{
		{BindingID: bindingID, RoundID: &round1ID, Seq: 1, TS: time.Now(), Kind: "send", Direction: "mastermind_to_builder", EntryJSON: "{}"},
		{BindingID: bindingID, RoundID: &round2ID, Seq: 2, TS: time.Now(), Kind: "send", Direction: "mastermind_to_builder", EntryJSON: "{}"},
	}
	if _, err := d.AppendEvents(bindingID, evs); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	all, err := d.Events(bindingID, 0)
	if err != nil {
		t.Fatalf("Events(0): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Events(0) got %d, want 2", len(all))
	}

	round1Only, err := d.Events(bindingID, 1)
	if err != nil {
		t.Fatalf("Events(1): %v", err)
	}
	if len(round1Only) != 1 || round1Only[0].Seq != 1 {
		t.Fatalf("Events(1) = %+v, want one event with seq 1", round1Only)
	}
}

func TestStatsCounts(t *testing.T) {
	s := seedDB(t)
	stats, err := s.d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if want := embeddedVersion(t); stats.Version != want {
		t.Errorf("Version = %d, want %d", stats.Version, want)
	}
	if stats.Rows["binding"] != 3 {
		t.Errorf("Rows[binding] = %d, want 3", stats.Rows["binding"])
	}
	if stats.Rows["round"] != 6 {
		t.Errorf("Rows[round] = %d, want 6", stats.Rows["round"])
	}
	if stats.Rows["repo"] != 2 {
		t.Errorf("Rows[repo] = %d, want 2", stats.Rows["repo"])
	}
	if stats.NewestRound == nil || !stats.NewestRound.Equal(s.day2) {
		t.Errorf("NewestRound = %v, want %v", stats.NewestRound, s.day2)
	}
	if stats.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", stats.SizeBytes)
	}
}

func TestRecentEvents(t *testing.T) {
	d := openTestDB(t)
	since := seedRecentEvents(t, d)

	got, err := d.RecentEvents(since, 0)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3 (only those since `since`)", len(got))
	}
	want := []struct {
		kind, binding string
	}{
		{"switch", "webshop"},
		{"plan", "webshop"},
		{"report", "atlas"},
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].BindingName != w.binding {
			t.Errorf("got[%d] = %+v, want %s on %s", i, got[i], w.kind, w.binding)
		}
	}

	if got[0].Round == nil || *got[0].Round != 2 {
		t.Errorf("got[0].Round = %v, want 2", got[0].Round)
	}
	if got[2].Round == nil || *got[2].Round != 1 {
		t.Errorf("got[2].Round = %v, want 1", got[2].Round)
	}
	if got[2].Tokens == nil || *got[2].Tokens != 1000 {
		t.Errorf("got[2].Tokens = %v, want 1000 (the four counters summed)", got[2].Tokens)
	}
	if got[2].DurationMS == nil || *got[2].DurationMS != 600_000 {
		t.Errorf("got[2].DurationMS = %v, want 600000", got[2].DurationMS)
	}
	if got[0].DurationMS != nil || got[1].DurationMS != nil {
		t.Errorf("open rounds: DurationMS = %v, %v; want both nil", got[0].DurationMS, got[1].DurationMS)
	}

	limited, err := d.RecentEvents(since, 2)
	if err != nil {
		t.Fatalf("RecentEvents(limit 2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("len(limited) = %d, want 2", len(limited))
	}
}

func seedRecentEvents(t *testing.T, d *DB) time.Time {
	t.Helper()
	now := time.Now().Truncate(time.Millisecond)

	binding1ID, err := d.UpsertBinding(newTestBinding("atlas", now.Add(-time.Hour)))
	if err != nil {
		t.Fatalf("UpsertBinding 1: %v", err)
	}
	binding2ID, err := d.UpsertBinding(newTestBinding("webshop", now.Add(-time.Hour)))
	if err != nil {
		t.Fatalf("UpsertBinding 2: %v", err)
	}

	r1 := newTestRound(binding1ID, 1, OutcomeReported)
	r1.StartedAt = now.Add(-30 * time.Minute)
	ct := now.Add(-20 * time.Minute)
	r1.ClosedAt = &ct
	in, cache, write, out := int64(100), int64(200), int64(300), int64(400)
	r1.InTokens, r1.CacheTokens, r1.WriteTokens, r1.OutTokens = &in, &cache, &write, &out
	round1ID, err := d.UpsertRound(r1)
	if err != nil {
		t.Fatalf("UpsertRound 1: %v", err)
	}

	r2 := newTestRound(binding2ID, 2, OutcomeOpen)
	r2.StartedAt = now.Add(-10 * time.Minute)
	round2ID, err := d.UpsertRound(r2)
	if err != nil {
		t.Fatalf("UpsertRound 2: %v", err)
	}

	evs1 := []Event{
		{BindingID: binding1ID, RoundID: &round1ID, Seq: 1, TS: now.Add(-20 * time.Minute), Kind: "plan", Direction: "mastermind_to_builder", EntryJSON: "{}"},
		{BindingID: binding1ID, RoundID: &round1ID, Seq: 2, TS: now.Add(-10 * time.Minute), Kind: "report", Direction: "builder_to_planner", EntryJSON: `{"outcome":"done"}`},
	}
	if _, err := d.AppendEvents(binding1ID, evs1); err != nil {
		t.Fatalf("AppendEvents 1: %v", err)
	}
	evs2 := []Event{
		{BindingID: binding2ID, RoundID: &round2ID, Seq: 1, TS: now.Add(-5 * time.Minute), Kind: "plan", Direction: "mastermind_to_builder", EntryJSON: "{}"},
		{BindingID: binding2ID, RoundID: &round2ID, Seq: 2, TS: now.Add(-2 * time.Minute), Kind: "switch", Direction: "system", EntryJSON: "{}"},
	}
	if _, err := d.AppendEvents(binding2ID, evs2); err != nil {
		t.Fatalf("AppendEvents 2: %v", err)
	}

	return now.Add(-15 * time.Minute)
}
