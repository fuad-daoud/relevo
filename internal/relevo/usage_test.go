package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// closeRound drives the ordinary close: report file, done marker, idle
// builder, one reconcile. Returns the report entry.
func closeRound(t *testing.T, rt Runtime, b store.Binding) store.LogEntry {
	t.Helper()
	if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath(b.Name, b.Round))
	next, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// reconcile (reconcile_test.go) mirrors Reconcile's own contract -- "does
	// NOT persist anything: ... the caller must tx.Save it" -- by returning
	// the updated binding without saving it, exactly as daemon.go's tick
	// does before its own tx.Save. Every other caller of this helper only
	// inspects the returned value in memory; this is the first to also
	// assert on the persisted store, so it does the daemon's save itself.
	if err := rt.Store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindReport && e.Round == b.Round {
			return e
		}
	}
	t.Fatal("no report entry")
	return store.LogEntry{}
}

func TestRoundCloseRecordsUsage(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Git = &fakeGit{snapshotTreeID: "t", diffResult: git.Diff{}}
	fu := &fakeUsage{samples: []usage.Sample{{Provider: "test", Model: "m", Tokens: usage.Tokens{In: 10, Out: 2}, USD: 0.5, HasCost: true}}}
	rt.Usage = fu
	// The round was sent at baseTime; close it 90 s later.
	rt.Now = func() time.Time { return baseTime.Add(90 * time.Second) }

	e := closeRound(t, rt, b)
	if e.Usage == nil {
		t.Fatal("report entry has no usage")
	}
	if e.Usage.Cost.Basis != usage.Measured || e.Usage.Cost.USD != 0.5 || e.Usage.Tokens != (usage.Tokens{In: 10, Out: 2}) {
		t.Errorf("usage = %+v", e.Usage)
	}
	if e.Usage.DurationMS != 90_000 {
		t.Errorf("DurationMS = %d, want 90000 (RoundStartedAt to close)", e.Usage.DurationMS)
	}
	if e.Usage.Harness != "agy" {
		t.Errorf("Harness = %q, want the builder's kind", e.Usage.Harness)
	}
	if len(fu.sources) != 1 {
		t.Fatalf("reader called %d times, want 1", len(fu.sources))
	}
	src := fu.sources[0]
	if src.Harness != "agy" || src.Mode != usage.ModeHeadless || src.Provider != "test" || src.Model != "m" {
		t.Errorf("source = %+v", src)
	}
	if !src.Start.Equal(baseTime) || !src.End.Equal(baseTime.Add(90*time.Second)) {
		t.Errorf("window = %v..%v", src.Start, src.End)
	}
}

func TestRoundCloseWithNoReaderIsUnknownAndStillCloses(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Usage = nil
	e := closeRound(t, rt, b)
	if e.Usage == nil || e.Usage.Cost.Basis != usage.Unknown || e.Usage.Note != "no reader" {
		t.Errorf("usage = %+v, want unknown/no reader", e.Usage)
	}
	got, _ := rt.Store.Load(b.Name)
	if got.Round != b.Round+1 || !got.RoundStartedAt.IsZero() {
		t.Errorf("round did not close normally: %+v", got)
	}
}

func TestRoundCloseReaderNoteIsUnknown(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Usage = &fakeUsage{note: "no stream"}
	e := closeRound(t, rt, b)
	if e.Usage.Cost.Basis != usage.Unknown || e.Usage.Note != "no stream" {
		t.Errorf("usage = %+v", e.Usage)
	}
}

func TestRoundCloseReaderTimeoutStillCloses(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Usage = &fakeUsage{block: true}
	done := make(chan store.LogEntry, 1)
	go func() { done <- closeRound(t, rt, b) }()
	select {
	case e := <-done:
		if e.Usage == nil || e.Usage.Cost.Basis != usage.Unknown {
			t.Errorf("usage = %+v", e.Usage)
		}
	case <-time.After(usageDeadline + 5*time.Second):
		t.Fatal("round close hung on the reader")
	}
}

func TestRoundSourceHeadlessAndPlan(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	b.Builder.Mode = store.ModeHeadless
	b.Worktree = "/wt"
	src := roundSource(rt, b, baseTime, baseTime.Add(time.Minute))
	if src.Mode != usage.ModeHeadless || src.StreamPath != rt.Store.RunnerStreamPath(b.Name, b.Round) {
		t.Errorf("headless source = %+v", src)
	}
	if src.Worktree != "/wt" {
		t.Errorf("Worktree = %q", src.Worktree)
	}
	if src.Plan {
		t.Error("test candidates carry no plan flag")
	}
	b.BuilderCandidate = "" // adopted builder
	src = roundSource(rt, b, baseTime, baseTime)
	if src.Provider != "" || src.Model != "" {
		t.Errorf("adopted builder must have no candidate provider/model: %+v", src)
	}
}

func TestRecordUsageFoldsWithRuntimePrices(t *testing.T) {
	t.Parallel()

	rt := Runtime{Now: func() time.Time { return baseTime }}
	rt.Usage = &fakeUsage{samples: []usage.Sample{{Provider: "test", Model: "m", Tokens: usage.Tokens{In: 1_000_000}}}}
	rt.Prices = usage.Prices{Models: map[string]usage.ModelPrice{"test/m": {In: 2}}}
	u := recordUsage(context.Background(), rt, usage.Source{Harness: "claude", Mode: usage.ModeHeadless, Start: baseTime, End: baseTime.Add(time.Second)})
	if u.Cost.Basis != usage.Estimated || u.Cost.USD != 2 {
		t.Errorf("usage = %+v, want estimated $2", u)
	}
	if u.DurationMS != 1000 || u.Harness != "claude" {
		t.Errorf("duration/harness = %d/%q", u.DurationMS, u.Harness)
	}
}

// TestRecordUsageAttachesStepStats pins that a round's step figures come
// from its builder stream at close (#323, #324): the stream's step and
// tool-call counts land on the Usage even when no reader is wired, and a
// stream that cannot be read leaves them zero without failing the round.
func TestRecordUsageAttachesStepStats(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "001-builder.jsonl")
	lines := []string{
		`{"type":"step_start","timestamp":1000}`,
		`{"type":"text","timestamp":1200}`,
		`{"type":"step_finish","timestamp":2000}`,
		`{"type":"step_start","timestamp":3000}`,
		`{"type":"tool_use","timestamp":3400}`,
		`{"type":"step_finish","timestamp":5000}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{} // rt.Usage is nil: the step figures do not need a reader
	u := recordUsage(context.Background(), rt, usage.Source{Harness: "opencode", StreamPath: path})
	if u.Steps != 2 || u.ToolCalls != 1 {
		t.Errorf("Steps/ToolCalls = %d/%d, want 2/1", u.Steps, u.ToolCalls)
	}

	missing := recordUsage(context.Background(), rt,
		usage.Source{Harness: "opencode", StreamPath: filepath.Join(t.TempDir(), "nope.jsonl")})
	if missing.Steps != 0 || missing.ToolCalls != 0 || missing.StepP50MS != 0 || missing.FirstOutputP50MS != 0 {
		t.Errorf("a missing stream must leave the step fields zero: %+v", missing)
	}
}
