package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"testing/fstest"
	"time"
)

// errFake is a sentinel Tx callers return to force a rollback in tests.
var errFake = errors.New("fake failure")

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func ptr[T any](v T) *T { return &v }

// embeddedVersion is the highest migration this binary embeds.
func embeddedVersion(t *testing.T) int {
	t.Helper()
	v, err := maxEmbedded(migrationFiles)
	if err != nil {
		t.Fatalf("maxEmbedded: %v", err)
	}
	return v
}

// seedNewerSchema writes a schema_version row above every embedded migration.
func seedNewerSchema(t *testing.T, path string) int {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if _, err := sqlDB.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (99, '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert version 99: %v", err)
	}

	var tables int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	return tables
}

// openSchema opens a read-only database migrated only through the named migrations.
func openSchema(t *testing.T, names ...string) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	fsys := fstest.MapFS{}
	for _, name := range names {
		data, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		fsys["migrations/"+name] = &fstest.MapFile{Data: data}
	}

	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := applyMigrations(sqlDB, fsys); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	d, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func newTestBinding(name string, createdAt time.Time) Binding {
	return Binding{
		Name:         name,
		CWD:          "/home/x/" + name,
		BuilderMode:  "headless",
		CreatedAt:    createdAt,
		IngestSource: IngestLive,
	}
}

func newTestRound(bindingID string, number int, outcome string) Round {
	return Round{
		BindingID: bindingID,
		Number:    number,
		StartedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		Outcome:   outcome,
	}
}

func testRecord(name string) Record {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return Record{
		Owner:     "",
		Name:      name,
		State:     "active",
		Round:     1,
		CWD:       "/tmp/" + name,
		JSON:      `{"name":"` + name + `"}`,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func testEvent(seq int) RecordEvent {
	return RecordEvent{
		Seq:       seq,
		TS:        time.Now().UTC().Truncate(time.Millisecond),
		Round:     1,
		Direction: "to_planner",
		Kind:      "report",
		JSON:      `{"seq":` + strconv.Itoa(seq) + `,"kind":"report","unknown_key":"kept"}`,
	}
}

func recordNames(rs []Record) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func eventSeqs(evs []RecordEvent) []int {
	out := make([]int, len(evs))
	for i, e := range evs {
		out[i] = e.Seq
	}
	return out
}

// seedMirrorRow inserts one mirror binding and one round of it.
func seedMirrorRow(t *testing.T, d *DB, name string, at time.Time, number int) string {
	t.Helper()
	bindingID, err := d.UpsertBinding(newTestBinding(name, at))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	roundID, err := d.UpsertRound(Round{BindingID: bindingID, Number: number, StartedAt: at, Outcome: OutcomeOpen})
	if err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}
	return roundID
}

type pair struct {
	binding string
	number  int
}

func pairsOf(rows []RoundRow) []pair {
	out := make([]pair, len(rows))
	for i, r := range rows {
		out[i] = pair{binding: r.BindingName, number: r.Number}
	}
	sortPairs(out)
	return out
}

func assertPairs(t *testing.T, got []RoundRow, want []pair) {
	t.Helper()
	gotPairs := pairsOf(got)
	sortPairs(want)
	if !reflect.DeepEqual(gotPairs, want) {
		t.Errorf("got %v, want %v", gotPairs, want)
	}
}

func sortPairs(ps []pair) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].binding != ps[j].binding {
			return ps[i].binding < ps[j].binding
		}
		return ps[i].number < ps[j].number
	})
}

// seeded is the fixture seedDB builds: two repos, three bindings (webshop and
// api on repo A -- api archived; docs on repo B), one mastermind, and six rounds
// spanning harnesses, outcomes, gate results, cost bases and two dates.
type seeded struct {
	d          *DB
	repoAID    string
	repoBID    string
	webshopID  string
	apiID      string
	docsID     string
	day1, day2 time.Time
}

func seedDB(t *testing.T) seeded {
	t.Helper()
	d := openTestDB(t)

	day1 := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)

	repoAID := upsertRepo(t, d, Repo{
		OriginURL: ptr("https://example.test/a.git"),
		CommonDir: ptr("/home/x/a/.git"),
		FirstSeen: day1,
	})
	repoBID := upsertRepo(t, d, Repo{OriginURL: ptr("https://example.test/b.git"), FirstSeen: day1})

	mastermindID, err := d.UpsertMasterMind(MasterMind{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: day1, LastSeen: day2})
	if err != nil {
		t.Fatalf("UpsertMasterMind: %v", err)
	}

	webshopID := upsertBinding(t, d, Binding{
		Name: "webshop", RepoID: &repoAID, MasterMindID: &mastermindID, Feature: ptr("checkout"),
		Ticket: ptr("o/r#607"),
		CWD:    "/home/x/webshop", BuilderMode: "headless", CreatedAt: day1, IngestSource: IngestLive,
	})
	apiID := upsertBinding(t, d, Binding{
		Name: "api", RepoID: &repoAID, MasterMindID: &mastermindID, Ticket: ptr("#607"),
		CWD: "/home/x/api", BuilderMode: "headless", CreatedAt: day1,
		ArchivedAt: ptr(day2), ArchivePath: ptr("/archive/api.tar.gz"), IngestSource: IngestArchive,
	})
	docsID := upsertBinding(t, d, Binding{
		Name: "docs", RepoID: &repoBID, MasterMindID: &mastermindID, FinalState: ptr("needs_you"),
		Ticket: ptr("#42"),
		CWD:    "/home/x/docs", BuilderMode: "pane", CreatedAt: day1, IngestSource: IngestLive,
	})

	for _, r := range seedRounds(webshopID, apiID, docsID, day1, day2) {
		if _, err := d.UpsertRound(r); err != nil {
			t.Fatalf("UpsertRound %+v: %v", r, err)
		}
	}

	return seeded{d: d, repoAID: repoAID, repoBID: repoBID, webshopID: webshopID, apiID: apiID, docsID: docsID, day1: day1, day2: day2}
}

func seedRounds(webshopID, apiID, docsID string, day1, day2 time.Time) []Round {
	return []Round{
		{
			BindingID: webshopID, Number: 1, StartedAt: day1, Outcome: OutcomeReported,
			Candidate: ptr("claude/anthropic/sonnet"), Harness: ptr("agy"),
			Provider: ptr("anthropic"), Model: ptr("sonnet"),
			CostBasis: ptr("exact"), CostUSD: ptr(1.5),
			Switches: 2,
		},
		{
			BindingID: webshopID, Number: 2, StartedAt: day2, Outcome: OutcomeOpen,
			Candidate: ptr("claude/anthropic/sonnet"), Harness: ptr("agy"),
			Provider: ptr("anthropic"), Model: ptr("sonnet"),
		},
		{
			BindingID: apiID, Number: 1, StartedAt: day1, Outcome: OutcomeHalted,
			Candidate: ptr("opencode/openrouter/glm"), Harness: ptr("opencode"),
			Provider: ptr("openrouter"), Model: ptr("glm"),
			GateResult: ptr("fail"), CostBasis: ptr("estimated"), CostUSD: ptr(0.2),
		},
		{
			BindingID: apiID, Number: 2, StartedAt: day2, Outcome: OutcomeReported,
			Candidate: ptr("opencode/openrouter/glm"), Harness: ptr("opencode"),
			Provider: ptr("openrouter"), Model: ptr("glm"),
			GateResult: ptr("pass"),
		},
		{
			BindingID: docsID, Number: 1, StartedAt: day1, Outcome: OutcomeReported,
			Candidate: ptr("claude/anthropic/sonnet"), Harness: ptr("agy"),
			Provider: ptr("anthropic"), Model: ptr("sonnet"), ReportOutcome: ptr("done"),
		},
		{
			BindingID: docsID, Number: 2, StartedAt: day2, Outcome: OutcomeHalted,
			Candidate: ptr("claude/anthropic/sonnet"), Harness: ptr("agy"),
			Provider: ptr("anthropic"), Model: ptr("sonnet"), ReportOutcome: ptr("halted"),
		},
	}
}

func upsertRepo(t *testing.T, d *DB, r Repo) string {
	t.Helper()
	id, err := d.UpsertRepo(r)
	if err != nil {
		t.Fatalf("UpsertRepo %+v: %v", r, err)
	}
	return id
}

func upsertBinding(t *testing.T, d *DB, b Binding) string {
	t.Helper()
	id, err := d.UpsertBinding(b)
	if err != nil {
		t.Fatalf("UpsertBinding %+v: %v", b, err)
	}
	return id
}

func isCrockford(c byte) bool {
	for i := 0; i < len(crockford); i++ {
		if crockford[i] == c {
			return true
		}
	}
	return false
}
