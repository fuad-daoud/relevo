package relevo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// showFixtureDir is the shared golden fixture ingest's own tests use,
// reused here read-only for the db-backed Show tests (Task 2's plan: seed a
// temp db via ingest.Ingest over it, copying it into a temp dir first, and
// never writing into testdata).
const showFixtureDir = "../ingest/testdata/binding-three-rounds"

// showFixtureBindFiles are the bind.json stand-ins under showFixtureDir
// that belong to other rounds' tests, never copied alongside bind.json.
var showFixtureBindFiles = map[string]bool{
	"bind-legacy.json":   true,
	"bind-round4.json":   true,
	"bind-cwd-gone.json": true,
}

// copyShowFixture copies showFixtureDir into a fresh temp dir so a live
// ingest.DirSource can read it without ever touching testdata.
func copyShowFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	entries, err := os.ReadDir(showFixtureDir)
	if err != nil {
		t.Fatalf("ReadDir fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || showFixtureBindFiles[e.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(showFixtureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	return dst
}

// seedShowDB ingests the golden fixture (live, from a copy) into a fresh
// temp database and returns it.
func seedShowDB(t *testing.T) *db.DB {
	t.Helper()
	dir := copyShowFixture(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := ingest.Ingest(context.Background(), ingest.DirSource(dir), d, ingest.Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	seedRoundMirror(t, d, dir)
	return d
}

// seedArchivedLog writes the fixture's log.jsonl lines as the record's events,
// the medium the database gives them.
func seedArchivedLog(t *testing.T, d *db.DB, recID string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(showFixtureDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("read fixture log.jsonl: %v", err)
	}
	var evs []db.RecordEvent
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		evs = append(evs, db.RecordEvent{Seq: i + 1, JSON: line})
	}
	if err := d.EventReplaceAll(recID, evs); err != nil {
		t.Fatalf("EventReplaceAll: %v", err)
	}
}

// archiveShowFixture copies the golden fixture into a fresh store's
// "fixture" directory -- writing one file per extra basename -> body -- and
// archives it, so the returned store holds exactly one archived record with
// every NNN-* file of the fixture sealed into it.
func archiveShowFixture(t *testing.T, extra map[string]string) *store.Store {
	t.Helper()
	s := store.New(t.TempDir())
	if err := os.MkdirAll(s.Dir("fixture"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries, err := os.ReadDir(showFixtureDir)
	if err != nil {
		t.Fatalf("ReadDir fixture: %v", err)
	}
	// A patch is row-only, so the fixture's own diff and drift become
	// round_file rows rather than files: the archive seals what is on disk, and
	// these two are never there.
	reserved := map[string]int{"001-diff.patch": 1, "002-drift.patch": 2}
	reservedBodies := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || showFixtureBindFiles[e.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(showFixtureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if _, ok := reserved[e.Name()]; ok {
			reservedBodies[e.Name()] = data
			continue
		}
		if err := os.WriteFile(filepath.Join(s.Dir("fixture"), e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	for base, body := range extra {
		if err := os.WriteFile(filepath.Join(s.Dir("fixture"), base), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", base, err)
		}
	}
	bindJSON, err := os.ReadFile(filepath.Join(showFixtureDir, "bind.json"))
	if err != nil {
		t.Fatalf("read fixture bind.json: %v", err)
	}
	sdb, err := s.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	recID, err := sdb.RecordPut(db.Record{Name: "fixture", JSON: string(bindJSON)})
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	seedArchivedLog(t, sdb, recID)
	for base, round := range reserved {
		body, ok := reservedBodies[base]
		if !ok {
			continue
		}
		if err := s.WithLock(func(tx *store.Tx) error {
			return tx.PutRoundFile("fixture", round, filepath.Join(s.Dir("fixture"), base), body)
		}); err != nil {
			t.Fatalf("PutRoundFile %s: %v", base, err)
		}
	}
	if _, err := s.Archive("fixture"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}
	return s
}

// seedShowArchiveDB archives the golden fixture into a record and ingests it
// as an archive source, so the binding it produces carries ArchivedAt.
func seedShowArchiveDB(t *testing.T) *db.DB {
	t.Helper()
	s := archiveShowFixture(t, nil)
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	if _, err := ingest.Ingest(context.Background(), ingest.ArchivedSource(s, archived[0].RecordID), d, ingest.Deps{}); err != nil {
		t.Fatalf("archive Ingest: %v", err)
	}
	seedRoundMirror(t, d, showFixtureDir)
	return d
}

// newShowLiveStore builds a live binding "fixture" with three rounds: round
// 1 done (plan, report, diff), round 2 halted (plan, report, builder log,
// no diff), round 3 the open round (plan only). b.Round is 3, matching
// finishRound's Round++ semantics -- round 3 is the current, not-yet-closed
// round, so "newest completed" must resolve to round 2 (the highest round
// with a report entry), not round 3.
func newShowLiveStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "fixture",
		CWD:   "/work/show-fixture",
		Round: 3,
		State: store.StateNeedsYou,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	write := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(s.PromptPath("fixture", 1), "# Round 1 plan\n")
	write(s.ReportPath("fixture", 1), "# Round 1 report\n")
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("fixture", 1, s.DiffPath("fixture", 1), []byte("diff --git a/x b/x\n"))
	}); err != nil {
		t.Fatalf("PutRoundFile diff: %v", err)
	}
	write(s.PromptPath("fixture", 2), "# Round 2 plan\n")
	write(s.ReportPath("fixture", 2), "# Round 2 report\n")
	write(s.BuilderLogPath("fixture", 2), "round 2 builder log line 1\nround 2 builder log line 2\n")
	write(s.PromptPath("fixture", 3), "# Round 3 plan\n")

	entries := []store.LogEntry{
		{TS: time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 0, 1, 0, time.UTC), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
		{TS: time.Date(2026, 9, 10, 10, 1, 0, 0, time.UTC), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 1, 1, 0, time.UTC), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Outcome: "halted"},
		{TS: time.Date(2026, 9, 10, 10, 2, 0, 0, time.UTC), Round: 3, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
	}
	for _, e := range entries {
		if err := s.AppendLog("fixture", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return s
}

func TestShowLiveDefaultsToNewestCompletedPlan(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Section: ShowPrompt})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !res.Live {
		t.Error("Live = false, want true")
	}
	if res.Round != 2 {
		t.Errorf("Round = %d, want 2 (highest round with a report entry)", res.Round)
	}
	if res.Rounds != 3 {
		t.Errorf("Rounds = %d, want 3 (b.Round)", res.Rounds)
	}
	if res.Missing {
		t.Error("Missing = true, want false")
	}
	if res.Text != "# Round 2 plan\n" {
		t.Errorf("Text = %q, want %q", res.Text, "# Round 2 plan\n")
	}
}

func TestShowLiveRoundReport(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowReport})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("Round = %d, want 1", res.Round)
	}
	if res.Text != "# Round 1 report\n" {
		t.Errorf("Text = %q, want %q", res.Text, "# Round 1 report\n")
	}
}

func TestShowLiveMissingDiffIsMissingNotError(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowDiff})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !res.Missing {
		t.Error("Missing = false, want true (round 2 has no diff file)")
	}
	if res.Text != "" {
		t.Errorf("Text = %q, want empty", res.Text)
	}
}

func TestShowLiveLogFiltersRound(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowLog})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(res.Events) != 2 {
		t.Fatalf("len(Events) = %d, want 2", len(res.Events))
	}
	for _, e := range res.Events {
		if e.Round != 1 {
			t.Errorf("Events has round %d, want only round 1", e.Round)
		}
	}
}

func TestShowLiveTranscriptReadsBuilderLog(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowTranscript})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := "round 2 builder log line 1\nround 2 builder log line 2\n"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

// TestShowLiveRoundsIsHighestPlanned pins Task 0(b): for a live binding,
// Rounds must be the highest round number with a plan log entry, not
// b.Round -- b.Round is the *next* round once a round has closed
// (finishRound does Round++), so a binding sitting idle after round 3
// closed (Round: 4, no round-4 plan sent yet) must still report Rounds ==
// 3, not 4.
func TestShowLiveRoundsIsHighestPlanned(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "idle",
		CWD:   "/work/idle",
		Round: 4,
		State: store.StateDone,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(s.PromptPath("idle", 3), []byte("# Round 3 plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	entries := []store.LogEntry{
		{TS: time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 0, 1, 0, time.UTC), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
		{TS: time.Date(2026, 9, 10, 10, 1, 0, 0, time.UTC), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 1, 1, 0, time.UTC), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
		{TS: time.Date(2026, 9, 10, 10, 2, 0, 0, time.UTC), Round: 3, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 2, 1, 0, time.UTC), Round: 3, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
	}
	for _, e := range entries {
		if err := s.AppendLog("idle", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	rt := Runtime{Store: s}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "idle", Section: ShowPrompt})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Rounds != 3 {
		t.Errorf("Rounds = %d, want 3 (highest round with a plan entry, not b.Round == 4)", res.Rounds)
	}
	if res.Round != 3 {
		t.Errorf("Round = %d, want 3", res.Round)
	}
}

func TestShowLiveNoCompletedRound(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "openonly",
		CWD:   "/work/openonly",
		Round: 1,
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(s.PromptPath("openonly", 1), []byte("# plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	entry := store.LogEntry{TS: time.Now(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true}
	if err := s.AppendLog("openonly", entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	rt := Runtime{Store: s}
	_, err := Show(context.Background(), rt, ShowOptions{Name: "openonly", Section: ShowPrompt})
	if !errors.Is(err, ErrNoCompletedRound) {
		t.Errorf("err = %v, want ErrNoCompletedRound", err)
	}
}

func TestShowDBFallsBackWhenNotLive(t *testing.T) {
	t.Parallel()

	d := seedShowDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Section: ShowPrompt})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Live {
		t.Error("Live = true, want false")
	}
	if res.Round != 3 {
		t.Errorf("Round = %d, want 3 (highest round with outcome != open)", res.Round)
	}
	if res.Rounds != 3 {
		t.Errorf("Rounds = %d, want 3", res.Rounds)
	}
	want := "# Round 3 plan\n\nFixture plan text for round 3.\n"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

func TestShowDBTranscriptFromRows(t *testing.T) {
	t.Parallel()

	d := seedShowDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowTranscript})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := "round 2 builder log line 1\nround 2 builder log line 2\nround 2 builder log line 3"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

// TestShowDBSkipsOpenRoundForNewestCompleted pins the db-path completion
// rule -- "the highest round with outcome != open" -- against a binding
// whose highest round is still open: round 2's own plan/log entries would
// make it the numerically newest round, but with no report, no exit and an
// active state it derives to OutcomeOpen (internal/ingest/outcome.go), so
// the newest *completed* round must be round 1.
func TestShowDBSkipsOpenRoundForNewestCompleted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bind := `{
  "name": "openhead",
  "cwd": "/work/openhead",
  "round": 2,
  "state": "active",
  "created_at": "2026-09-10T10:00:00.000Z"
}`
	log := `{"ts":"2026-09-10T10:00:00.000Z","round":1,"direction":"to_builder","kind":"plan","confirmed":true}
{"ts":"2026-09-10T10:00:01.000Z","round":1,"direction":"to_planner","kind":"report","confirmed":true,"outcome":"done"}
{"ts":"2026-09-10T10:01:00.000Z","round":2,"direction":"to_builder","kind":"plan","confirmed":true}
`
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(bind), 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatalf("write log.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001-plan.md"), []byte("# round 1 plan\n"), 0o644); err != nil {
		t.Fatalf("write 001-plan.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001-report.md"), []byte("# round 1 report\n"), 0o644); err != nil {
		t.Fatalf("write 001-report.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "002-plan.md"), []byte("# round 2 plan\n"), 0o644); err != nil {
		t.Fatalf("write 002-plan.md: %v", err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := ingest.Ingest(context.Background(), ingest.DirSource(dir), d, ingest.Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	seedRoundMirror(t, d, dir)

	rt := Runtime{Store: store.New(t.TempDir()), DB: d}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "openhead", Section: ShowPrompt})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("Round = %d, want 1 (round 2 is still open)", res.Round)
	}
	if res.Text != "# round 1 plan\n" {
		t.Errorf("Text = %q, want %q", res.Text, "# round 1 plan\n")
	}
}

func TestShowDBLogFromEvents(t *testing.T) {
	t.Parallel()

	d := seedShowDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowLog})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(res.Events) != 4 {
		t.Fatalf("len(Events) = %d, want 4 (pick, plan, diff, report)", len(res.Events))
	}
	for _, e := range res.Events {
		if e.Round != 1 {
			t.Errorf("Events has round %d, want only round 1", e.Round)
		}
	}
	if res.Events[3].Kind != store.KindReport || res.Events[3].Outcome != "done" {
		t.Errorf("Events[3] = %+v, want the round-1 report entry (outcome done)", res.Events[3])
	}
}

func TestShowDBArchivedHeaderFacts(t *testing.T) {
	t.Parallel()

	d := seedShowArchiveDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Section: ShowPrompt})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !res.Archived {
		t.Error("Archived = false, want true")
	}
	if res.ArchivedAt.IsZero() {
		t.Error("ArchivedAt is zero, want the archived record's stamp")
	}
}

func TestShowRoundOutOfRange(t *testing.T) {
	t.Parallel()

	d := seedShowDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	_, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 99, Section: ShowPrompt})
	if err == nil {
		t.Fatal("Show: want an error for a round out of range")
	}
	want := "round 99: binding has 3 rounds"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestShowUnknownBinding(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: store.New(t.TempDir())}

	_, err := Show(context.Background(), rt, ShowOptions{Name: "nope", Section: ShowPrompt})
	if err == nil {
		t.Fatal("Show: want an error for an unknown binding")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want it to wrap store.ErrNotFound", err)
	}
}

// TestShowArchivedReadsSealedRoundFiles pins Show's archived step: an archived
// binding is answered from its sealed round files and its record's log, with
// no mirror at all. Mutation: drop Show's archived step and Show returns
// not-found (DB is nil).
func TestShowArchivedReadsSealedRoundFiles(t *testing.T) {
	t.Parallel()

	s := archiveShowFixture(t, map[string]string{"001-gate.log": "gate output\n"})
	rt := Runtime{Store: s}

	// Every expected text is the fixture file's own bytes.
	fixture := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(showFixtureDir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return string(data)
	}

	cases := []struct {
		name    string
		round   int
		section ShowSection
		want    string
		missing bool
	}{
		{name: "plan round 1", round: 1, section: ShowPrompt, want: fixture("001-plan.md")},
		{name: "report round 2", round: 2, section: ShowReport, want: fixture("002-report.md")},
		{name: "diff round 1", round: 1, section: ShowDiff, want: fixture("001-diff.patch")},
		{name: "drift round 2", round: 2, section: ShowDrift, want: fixture("002-drift.patch")},
		{name: "transcript round 2", round: 2, section: ShowTranscript, want: fixture("002-builder.log")},
		{name: "gate round 1", round: 1, section: ShowGate, want: "gate output\n"},
		{name: "diff round 2 is missing", round: 2, section: ShowDiff, want: "", missing: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: tc.round, Section: tc.section})
			if err != nil {
				t.Fatalf("Show: %v", err)
			}
			if res.Text != tc.want {
				t.Errorf("Text = %q, want %q", res.Text, tc.want)
			}
			if res.Missing != tc.missing {
				t.Errorf("Missing = %v, want %v", res.Missing, tc.missing)
			}
			if res.Live {
				t.Error("Live = true, want false")
			}
			if !res.Archived {
				t.Error("Archived = false, want true")
			}
			if res.ArchivedAt.IsZero() {
				t.Error("ArchivedAt is zero, want the record's stamp")
			}
		})
	}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowLog})
	if err != nil {
		t.Fatalf("Show log: %v", err)
	}
	if len(res.Events) == 0 {
		t.Fatal("len(Events) = 0, want at least one round-1 event")
	}
	for _, e := range res.Events {
		if e.Round != 1 {
			t.Errorf("Events has round %d, want only round 1", e.Round)
		}
	}
	if res.Live || !res.Archived || res.ArchivedAt.IsZero() {
		t.Errorf("log result Live = %v, Archived = %v, ArchivedAt = %v, want false, true, non-zero",
			res.Live, res.Archived, res.ArchivedAt)
	}
}

// TestShowArchivedWinsOverTheMirror pins the precedence of Show's archived
// step over the database: the record's sealed gate log answers where showDB
// on its own reports Missing (it has no gate row).
func TestShowArchivedWinsOverTheMirror(t *testing.T) {
	t.Parallel()

	s := archiveShowFixture(t, map[string]string{"001-gate.log": "gate output\n"})
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := ingest.Ingest(context.Background(), ingest.ArchivedSource(s, archived[0].RecordID), d, ingest.Deps{}); err != nil {
		t.Fatalf("archive Ingest: %v", err)
	}

	rt := Runtime{Store: s, DB: d}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowGate})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing {
		t.Error("Missing = true, want the sealed gate log (showDB alone reports Missing)")
	}
	if res.Text != "gate output\n" {
		t.Errorf("Text = %q, want %q", res.Text, "gate output\n")
	}
}

// TestShowArchivedDefaultsToNewestCompletedRound pins Show's archived step to
// showLive's default-round rule: with no --round, the newest completed round
// is the highest KindReport entry round -- round 2 in the fixture -- not the
// highest planned round (3).
func TestShowArchivedDefaultsToNewestCompletedRound(t *testing.T) {
	t.Parallel()

	s := archiveShowFixture(t, nil)
	rt := Runtime{Store: s}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Section: ShowPrompt})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Round != 2 {
		t.Errorf("Round = %d, want 2 (highest round with a report entry)", res.Round)
	}
	want, err := os.ReadFile(filepath.Join(showFixtureDir, "002-plan.md"))
	if err != nil {
		t.Fatalf("read fixture 002-plan.md: %v", err)
	}
	if res.Text != string(want) {
		t.Errorf("Text = %q, want %q", res.Text, string(want))
	}
}

// mirrorLines returns body's complete lines the way the retired round
// transcript read yielded them (internal/ingest readAppendOnly): everything
// up to the final newline, split on '\n'. A file with no trailing newline
// has no complete line and yields none.
func mirrorLines(body []byte) [][]byte {
	end := bytes.LastIndexByte(body, '\n')
	if end < 0 {
		return nil
	}
	return bytes.Split(body[:end], []byte{'\n'})
}

// mirrorTranscriptRecords builds round n's builder transcript rows exactly as
// ingest built them before D3b: the builder stream (NNN-builder.jsonl) when
// the fixture has one, else the builder log (NNN-builder.log) (D3b plan §3
// fence item 3).
func mirrorTranscriptRecords(t *testing.T, dir string, n int, builderKind string) []db.TranscriptRecord {
	t.Helper()

	streamName := fmt.Sprintf("%03d-builder.jsonl", n)
	stream, err := os.ReadFile(filepath.Join(dir, streamName))
	if err == nil {
		lines := mirrorLines(stream)
		recs := make([]db.TranscriptRecord, 0, len(lines))
		r := transcript.NewRenderer()
		for i, line := range lines {
			rec := db.TranscriptRecord{Seq: i, RecordJSON: string(line)}
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 && trimmed[0] == '{' {
				rec.Rendered = strings.Join(r.Render(builderKind, line), "\n")
			}
			recs = append(recs, rec)
		}
		return recs
	}
	if !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", streamName, err)
	}

	logName := fmt.Sprintf("%03d-builder.log", n)
	logBody, err := os.ReadFile(filepath.Join(dir, logName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", logName, err)
	}
	lines := mirrorLines(logBody)
	recs := make([]db.TranscriptRecord, 0, len(lines))
	for i, line := range lines {
		recs = append(recs, db.TranscriptRecord{Seq: i, Rendered: string(line)})
	}
	return recs
}

// seedRoundMirror inserts, directly, the round mirror rows ingest wrote
// before D3b and no longer writes: the plain round-file artifacts (plan,
// report, diff, drift) and each round's builder transcript, read from the
// fixture's own files exactly as ingest built them (D3b plan §3 fence items
// 1 and 3). Show's database fallback (showDB, show.go) still reads those rows
// for a binding with no live files and no archived record, so the db-backed
// show tests seed them by hand after ingest.Ingest.
func seedRoundMirror(t *testing.T, d *db.DB, dir string) {
	t.Helper()

	var bind struct {
		Name    string `json:"name"`
		Builder struct {
			Kind string `json:"kind"`
		} `json:"builder"`
	}
	bindJSON, err := os.ReadFile(filepath.Join(dir, "bind.json"))
	if err != nil {
		t.Fatalf("read bind.json: %v", err)
	}
	if err := json.Unmarshal(bindJSON, &bind); err != nil {
		t.Fatalf("parse bind.json: %v", err)
	}

	row, found, err := d.Binding(bind.Name)
	if err != nil || !found {
		t.Fatalf("Binding(%s): found=%v err=%v", bind.Name, found, err)
	}
	rounds, err := d.Rounds(row.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}

	type artifactSeed struct {
		roundID, kind, text, sha string
		size                     int64
	}
	type transcriptSeed struct {
		roundID string
		recs    []db.TranscriptRecord
	}

	roundFiles := []struct{ base, kind string }{
		{"%03d-plan.md", db.ArtifactPrompt},
		{"%03d-report.md", db.ArtifactReport},
		{"%03d-diff.patch", db.ArtifactDiff},
		{"%03d-drift.patch", db.ArtifactDrift},
	}

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var artifacts []artifactSeed
	var transcripts []transcriptSeed
	for _, r := range rounds {
		for _, a := range roundFiles {
			body, rerr := os.ReadFile(filepath.Join(dir, fmt.Sprintf(a.base, r.Number)))
			if rerr != nil {
				if os.IsNotExist(rerr) {
					continue
				}
				t.Fatalf("read %s: %v", a.base, rerr)
			}
			sum := sha256.Sum256(body)
			artifacts = append(artifacts, artifactSeed{
				roundID: r.ID,
				kind:    a.kind,
				text:    string(body),
				size:    int64(len(body)),
				sha:     fmt.Sprintf("%x", sum),
			})
		}
		transcripts = append(transcripts, transcriptSeed{
			roundID: r.ID,
			recs:    mirrorTranscriptRecords(t, dir, r.Number, bind.Builder.Kind),
		})
	}

	if err := d.Tx(func(tx *db.Tx) error {
		for _, a := range artifacts {
			if err := tx.UpsertArtifact(db.Artifact{
				RoundID:    a.roundID,
				Kind:       a.kind,
				Text:       a.text,
				Bytes:      a.size,
				SHA256:     a.sha,
				CapturedAt: now,
			}); err != nil {
				return err
			}
		}
		for _, ts := range transcripts {
			if len(ts.recs) == 0 {
				continue
			}
			if _, err := tx.AppendTranscript(db.OwnerRound, ts.roundID, ts.recs); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed round mirror: %v", err)
	}
}

// N8: a live round with a stream and no NNN-builder.log renders the stream,
// and the archived variant renders the sealed stream the same way.
func TestShowTranscriptRendersTheStreamWithoutALog(t *testing.T) {
	t.Parallel()

	stream := "live stream line one\nlive stream line two\n"

	t.Run("live", func(t *testing.T) {
		s := store.New(t.TempDir())
		b := store.Binding{Name: "fixture", CWD: "/work/fixture", Round: 1, State: store.StateActive}
		if err := s.Save(b); err != nil {
			t.Fatalf("Save: %v", err)
		}
		entry := store.LogEntry{TS: time.Now(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true}
		if err := s.AppendLog("fixture", entry); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
		if err := os.WriteFile(s.BuilderStreamPath("fixture", 1), []byte(stream), 0o644); err != nil {
			t.Fatalf("write stream: %v", err)
		}

		res, err := Show(context.Background(), Runtime{Store: s}, ShowOptions{Name: "fixture", Round: 1, Section: ShowTranscript})
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if res.Missing || res.Text != stream {
			t.Errorf("Missing = %v, Text = %q, want the rendered stream %q", res.Missing, res.Text, stream)
		}
	})

	t.Run("archived", func(t *testing.T) {
		s := archiveShowFixture(t, map[string]string{"003-builder.jsonl": stream})
		res, err := Show(context.Background(), Runtime{Store: s}, ShowOptions{Name: "fixture", Round: 3, Section: ShowTranscript})
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if res.Missing || res.Text != stream {
			t.Errorf("Missing = %v, Text = %q, want the rendered sealed stream %q", res.Missing, res.Text, stream)
		}
		if !res.Archived {
			t.Error("Archived = false, want true")
		}
	})
}

// TestFindingsRound pins the pure resolver behind `show --findings`: a
// requested round within upper is taken as is, one above upper is refused with
// the plan-round count, and no --round scans newest-first for the round that
// holds the consult's findings, ErrNoFindings when none does.
func TestFindingsRound(t *testing.T) {
	t.Parallel()

	existsErr := errors.New("exists failed")
	holds := func(rounds ...int) func(int) (bool, error) {
		return func(round int) (bool, error) {
			for _, r := range rounds {
				if r == round {
					return true, nil
				}
			}
			return false, nil
		}
	}

	cases := []struct {
		name      string
		requested int
		upper     int
		exists    func(int) (bool, error)
		want      int
		wantErr   string
		wantNone  bool
	}{
		{name: "requested round within upper", requested: 1, upper: 1, exists: holds(), want: 1},
		{name: "requested round above upper", requested: 2, upper: 1, exists: holds(), wantErr: "round 2: binding has 1 rounds"},
		{name: "newest held round wins", requested: 0, upper: 2, exists: holds(1), want: 1},
		{name: "newest of two held rounds wins", requested: 0, upper: 2, exists: holds(1, 2), want: 2},
		{name: "no round holds the findings", requested: 0, upper: 2, exists: holds(), wantNone: true},
		{name: "exists error propagates", requested: 0, upper: 2, exists: func(int) (bool, error) { return false, existsErr }, wantErr: "exists failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			round, err := findingsRound(tc.requested, tc.upper, tc.exists)
			if tc.wantNone {
				if !errors.Is(err, ErrNoFindings) {
					t.Fatalf("findingsRound(%d, %d) err = %v, want ErrNoFindings", tc.requested, tc.upper, err)
				}
				return
			}
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("findingsRound(%d, %d) err = %v, want %q", tc.requested, tc.upper, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("findingsRound(%d, %d): %v", tc.requested, tc.upper, err)
			}
			if round != tc.want {
				t.Errorf("findingsRound(%d, %d) = %d, want %d", tc.requested, tc.upper, round, tc.want)
			}
		})
	}
}

// TestShowLiveFindingsOnBindingWithNoRounds pins #593 on the live path: a
// consult is recorded on the binding's current round, which has no plan entry
// yet, so `show --findings <id>` must read it with and without --round.
func TestShowLiveFindingsOnBindingWithNoRounds(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "consultonly",
		CWD:   "/work/consultonly",
		Round: 1,
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	const id = "abc"
	const want = "# Plan\n\nWrite the plan as findings.\n"
	if err := os.WriteFile(s.FindingsPath("consultonly", 1, id), []byte(want), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	rt := Runtime{Store: s}
	for _, round := range []int{1, 0} {
		res, err := Show(context.Background(), rt, ShowOptions{
			Name: "consultonly", Round: round, Section: ShowFindings, FindingsID: id,
		})
		if err != nil {
			t.Fatalf("Show(round %d): %v", round, err)
		}
		if res.Missing {
			t.Errorf("Show(round %d): Missing = true, want the findings text", round)
		}
		if res.Text != want {
			t.Errorf("Show(round %d): Text = %q, want %q", round, res.Text, want)
		}
		if res.Round != 1 {
			t.Errorf("Show(round %d): Round = %d, want 1", round, res.Round)
		}
		if res.Rounds != 0 {
			t.Errorf("Show(round %d): Rounds = %d, want the plan-round count 0", round, res.Rounds)
		}
	}
}

// TestShowLiveFindingsUnknownConsult: no round holds the consult's findings,
// so `show --findings` with no --round reports ErrNoFindings rather than
// scanning into a bound error.
func TestShowLiveFindingsUnknownConsult(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "consultonly",
		CWD:   "/work/consultonly",
		Round: 1,
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(s.FindingsPath("consultonly", 1, "abc"), []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	rt := Runtime{Store: s}
	_, err := Show(context.Background(), rt, ShowOptions{
		Name: "consultonly", Round: 0, Section: ShowFindings, FindingsID: "zzz",
	})
	if !errors.Is(err, ErrNoFindings) {
		t.Fatalf("err = %v, want ErrNoFindings", err)
	}
	if !strings.Contains(err.Error(), "zzz") || !strings.Contains(err.Error(), "consultonly") {
		t.Errorf("err = %v, want it to name consult zzz on consultonly", err)
	}
}

// TestShowArchivedFindingsOnBindingWithNoRounds is the archived analogue of
// TestShowLiveFindingsOnBindingWithNoRounds: an archived record whose round 1
// has no plan entry still answers a findings read, sealed or scanned.
func TestShowArchivedFindingsOnBindingWithNoRounds(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "archivedconsult",
		CWD:   "/work/archivedconsult",
		Round: 1,
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	const id = "abc"
	const want = "# Plan\n\nArchived consult findings.\n"
	if err := os.WriteFile(s.FindingsPath("archivedconsult", 1, id), []byte(want), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}
	if _, err := s.Archive("archivedconsult"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	rt := Runtime{Store: s}
	for _, round := range []int{1, 0} {
		res, err := Show(context.Background(), rt, ShowOptions{
			Name: "archivedconsult", Round: round, Section: ShowFindings, FindingsID: id,
		})
		if err != nil {
			t.Fatalf("Show(round %d): %v", round, err)
		}
		if !res.Archived {
			t.Errorf("Show(round %d): Archived = false, want true", round)
		}
		if res.Missing {
			t.Errorf("Show(round %d): Missing = true, want the findings text", round)
		}
		if res.Text != want {
			t.Errorf("Show(round %d): Text = %q, want %q", round, res.Text, want)
		}
		if res.Round != 1 {
			t.Errorf("Show(round %d): Round = %d, want 1", round, res.Round)
		}
	}
}

// seedShowClaimStore is the routeRuntime seed the two claim tests share: a
// live binding with one pending mastermind-bound report entry and the report
// file the section prints.
func seedShowClaimStore(t *testing.T, rt Runtime) {
	t.Helper()
	seedPending(t, rt, "webshop", testClaimMasterMind, "opencode")
	if err := os.MkdirAll(rt.Store.OutDir("webshop"), 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("# round 1 report\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

// TestShowLiveClaimsPendingMasterMindPayload pins #673: a plain (non-peek)
// live read claims the oldest pending mastermind-bound payload through the
// same delivery.Pull the cockpit calls with "tui", stamped with route "show",
// and discards the pulled text -- the requested section is still what Show
// returns.
func TestShowLiveClaimsPendingMasterMindPayload(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedShowClaimStore(t, rt)

	res, err := Show(context.Background(), rt, ShowOptions{Name: "webshop", Section: ShowReport})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Text != "# round 1 report\n" {
		t.Errorf("Text = %q, want the report section", res.Text)
	}

	// The claim consumed the entry and stamped it with show's route.
	if _, found, err := delivery.Pull(context.Background(), rt.Store, "webshop", "probe"); err != nil || found {
		t.Errorf("Pull after a plain show = found %v, err %v; want nothing pending", found, err)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	route := ""
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Confirmed {
			route = e.Route
		}
	}
	if route != "show" {
		t.Errorf("confirmed route = %q, want %q", route, "show")
	}
}

// TestShowLivePeekLeavesPendingMasterMindPayload pins #673's --peek contract: a
// peeked live read still prints the section but claims nothing, so the payload
// is left for the route that pushes it.
func TestShowLivePeekLeavesPendingMasterMindPayload(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedShowClaimStore(t, rt)

	res, err := Show(context.Background(), rt, ShowOptions{Name: "webshop", Section: ShowReport, Peek: true})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Text != "# round 1 report\n" {
		t.Errorf("Text = %q, want the report section", res.Text)
	}

	if _, found, err := delivery.Pull(context.Background(), rt.Store, "webshop", "tui"); err != nil || !found {
		t.Errorf("Pull after a peeked show = found %v, err %v; want the payload still pending", found, err)
	}
}

// TestShowDoesNotClaimAdmittedPayload pins the read side of the admitted state:
// an entry a push route already admitted is not claimable, so a plain show
// prints the requested section but claims nothing -- the deliverer's own
// read-back is the only thing that may confirm it.
func TestShowDoesNotClaimAdmittedPayload(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedShowClaimStore(t, rt)
	if err := rt.Store.AdmitIndex("webshop", 0); err != nil {
		t.Fatalf("AdmitIndex: %v", err)
	}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "webshop", Section: ShowReport})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Text != "# round 1 report\n" {
		t.Errorf("Text = %q, want the report section", res.Text)
	}

	if _, found, err := delivery.Pull(context.Background(), rt.Store, "webshop", "probe"); err != nil || found {
		t.Errorf("Pull after an admitted show = found %v, err %v; want nothing claimable", found, err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction != store.DirToMasterMind {
			continue
		}
		if e.AdmittedAt == nil {
			t.Error("the entry lost its admit marker")
		}
		if e.Confirmed {
			t.Error("an admitted entry must not be confirmed by a plain show")
		}
	}
}
