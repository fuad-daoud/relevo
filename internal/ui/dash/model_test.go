package dash

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
)

var testNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func p[T any](v T) *T { return &v }

// testRows is the r1 fixture shape hand-built, so every test reads the same
// three rounds: two claude/anthropic/sonnet rounds on persist, one
// agy/antigravity/claude-sonnet-4-6 round on api, with the columns the
// grid and the sums read.
func testRows() []db.RoundRow {
	return []db.RoundRow{
		{
			BindingID: "b1", BindingName: "persist",
			Repo: p("git@github.com:x/persist.git"), Feature: p("auth"),
			Number: 5, StartedAt: time.Date(2026, 9, 20, 22, 1, 0, 0, time.UTC),
			Outcome:   db.OutcomeReported,
			Candidate: p("claude/anthropic/sonnet"),
			Harness:   p("claude"), Provider: p("anthropic"), Model: p("sonnet"),
			Commits: p(1), Tree: p("clean"), GateResult: p("pass"),
			InTokens: p(int64(1_000_000)), OutTokens: p(int64(200_000)),
			CostUSD: p(0.42), CostBasis: p("measured"),
			ReportOutcome: p("done"), Mode: p("pane"),
			DurationMS: p(int64(27 * 60_000)),
		},
		{
			BindingID: "b1", BindingName: "persist",
			Repo: p("git@github.com:x/persist.git"), Feature: p("auth"),
			Number: 4, StartedAt: time.Date(2026, 9, 19, 9, 30, 0, 0, time.UTC),
			Outcome:   db.OutcomeHalted,
			Candidate: p("claude/anthropic/sonnet"),
			Harness:   p("claude"), Provider: p("anthropic"), Model: p("sonnet"),
			Commits: p(0), Tree: p("dirty"), GateResult: p("fail"),
			InTokens: p(int64(400_000)), CacheTokens: p(int64(100_000)),
			CostUSD: p(1.10), CostBasis: p("measured"),
			ReportOutcome: p("halted"), Mode: p("pane"),
			DurationMS: p(int64(12 * 60_000)),
		},
		{
			BindingID: "b2", BindingName: "api",
			Repo:   p("git@github.com:x/api.git"),
			Number: 2, StartedAt: time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC),
			Outcome:   db.OutcomeReported,
			Candidate: p("agy/antigravity/claude-sonnet-4-6"),
			Harness:   p("agy"), Provider: p("antigravity"), Model: p("claude-sonnet-4-6"),
			Commits: p(3), Tree: p("clean"), GateResult: p("pass"),
			InTokens: p(int64(2_000_000)),
			CostUSD:  p(9.10), CostBasis: p("unknown"),
			ReportOutcome: p("done"), Mode: p("headless"),
		},
	}
}

// newTestModel is New with the fake fetch Task 1 calls for: literal rows,
// no database. Width 160 so every column is on.
func newTestModel(t *testing.T, queryText string) Model {
	t.Helper()
	m := New(nil, time.UTC, func() time.Time { return testNow }, queryText, "")
	m.fetchFn = func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
		return testRows(), nil
	}
	m.SetSize(160, 40)
	return m
}

// feed runs Init's fetch and delivers its message, as the host's loop does.
func feed(t *testing.T, m Model) Model {
	t.Helper()
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned no command")
	}
	res, _ := m.Update(cmd())
	return res
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func special(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func TestInitFetchesAndGroups(t *testing.T) {
	m := feed(t, newTestModel(t, "by:candidate"))
	if m.groups == nil {
		t.Fatal("by:candidate: groups = nil, want the regrouped buckets")
	}
	if len(m.groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2 (claude, agy)", len(m.groups))
	}
	if m.tiles.Rounds != 3 {
		t.Errorf("tiles.Rounds = %d, want 3", m.tiles.Rounds)
	}
	if m.tiles.Bindings != 2 {
		t.Errorf("tiles.Bindings = %d, want 2", m.tiles.Bindings)
	}
	if m.fetching {
		t.Error("fetching = true after a reply")
	}
	if !m.lastFetch.Equal(testNow) {
		t.Errorf("lastFetch = %v, want %v", m.lastFetch, testNow)
	}
}

func TestBCyclesAxes(t *testing.T) {
	m := newTestModel(t, "")
	if m.query.By != histq.AxisNone {
		t.Fatalf("By = %q, want none", m.query.By)
	}
	res, _ := m.Update(key("b"))
	m = res
	if m.query.By != histq.AxisBinding {
		t.Errorf("first b: By = %q, want binding", m.query.By)
	}
	res, _ = m.Update(key("b"))
	m = res
	if m.query.By != histq.AxisRepo {
		t.Errorf("second b: By = %q, want repo", m.query.By)
	}
	m = newTestModel(t, "")
	for i := 0; i < len(histq.Axes()); i++ {
		res, _ := m.Update(key("b"))
		m = res
	}
	if m.query.By != histq.AxisNone {
		t.Errorf("%d b presses: By = %q, want none", len(histq.Axes()), m.query.By)
	}
}

func TestEnterTogglesGroup(t *testing.T) {
	m := feed(t, newTestModel(t, "by:candidate"))
	lines := m.visible()
	if len(lines) == 0 || lines[0].kind != lineGroup {
		t.Fatalf("first visible line is not a group: %+v", lines)
	}
	k := lines[0].group.Key
	before := len(lines)
	res, _ := m.Update(special(tea.KeyEnter))
	m = res
	if !m.expanded[k] {
		t.Errorf("enter on group %q did not expand it", k)
	}
	after := m.visible()
	if len(after) <= before {
		t.Errorf("expanding added no lines: %d -> %d", before, len(after))
	}
	// And again collapses it.
	res, _ = m.Update(special(tea.KeyEnter))
	m = res
	if m.expanded[k] {
		t.Errorf("second enter did not collapse %q", k)
	}
	if got := len(m.visible()); got != before {
		t.Errorf("collapsed to %d lines, want %d", got, before)
	}
}

func TestEnterOnRoundEmitsJump(t *testing.T) {
	m := feed(t, newTestModel(t, ""))
	// Sorted by started desc: persist r5 is first.
	_, cmd := m.Update(special(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("enter on a round row returned no command")
	}
	msg, ok := cmd().(JumpMsg)
	if !ok {
		t.Fatalf("command returned %T, want JumpMsg", cmd())
	}
	if msg.BindingName != "persist" || msg.BindingID != "b1" || msg.Round != 5 {
		t.Errorf("JumpMsg = %+v, want persist/b1/r5", msg)
	}
}

func TestSlashEditApplyAndError(t *testing.T) {
	m := feed(t, newTestModel(t, ""))

	res, _ := m.Update(key("/"))
	m = res
	if !m.editing {
		t.Fatal("/ did not open the editor")
	}
	if m.input.Value() != "" {
		t.Errorf("editor prefill = %q, want the applied text (empty)", m.input.Value())
	}

	res, _ = m.Update(key("harness:agy"))
	m = res
	res, cmd := m.Update(special(tea.KeyEnter))
	m = res
	if m.editing {
		t.Error("enter on a valid query left the editor open")
	}
	if m.parseErr != "" {
		t.Errorf("parseErr = %q, want none", m.parseErr)
	}
	if m.query.Filter.Harness != "agy" {
		t.Errorf("Filter.Harness = %q, want agy", m.query.Filter.Harness)
	}
	if m.QueryText() != "harness:agy" {
		t.Errorf("QueryText = %q, want harness:agy", m.QueryText())
	}
	if cmd == nil {
		t.Fatal("applying a query returned no fetch command")
	}
	if _, ok := cmd().(RowsMsg); !ok {
		t.Errorf("apply command returned %T, want RowsMsg", cmd())
	}

	// A bad query: the error goes inline, the previous query and its rows
	// stay, and nothing is fetched. The editor is prefilled with the
	// applied text, so clear it first (ctrl+u), as a human would.
	res, _ = m.Update(key("/"))
	m = res
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = res
	res, _ = m.Update(key("bogus:1"))
	m = res
	res, cmd = m.Update(special(tea.KeyEnter))
	m = res
	if !m.editing {
		t.Error("a parse error must leave the editor open")
	}
	if m.parseErr == "" {
		t.Fatal("bogus:1: parseErr is empty")
	}
	if m.query.Filter.Harness != "agy" {
		t.Errorf("parse error changed the query: Harness = %q", m.query.Filter.Harness)
	}
	if cmd != nil {
		t.Error("a parse error must not fetch")
	}

	// esc cancels, clearing the error.
	res, _ = m.Update(special(tea.KeyEsc))
	m = res
	if m.editing || m.parseErr != "" {
		t.Errorf("esc: editing %v parseErr %q, want false/empty", m.editing, m.parseErr)
	}
}

func TestSortCyclesAndFlips(t *testing.T) {
	m := newTestModel(t, "")
	if m.SortKey() != "started" {
		t.Errorf("SortKey = %q, want started", m.SortKey())
	}
	for _, want := range []string{"tokens", "duration", "commits", "started"} {
		res, _ := m.Update(key("s"))
		m = res
		if m.SortKey() != want {
			t.Errorf("s: SortKey = %q, want %q", m.SortKey(), want)
		}
	}
	if !m.sortDesc {
		t.Error("sortDesc defaults to true (newest/biggest first)")
	}
	res, _ := m.Update(key("S"))
	m = res
	if m.sortDesc {
		t.Error("S did not flip the direction")
	}

	// The sort is real: by tokens descending api's 2.0M leads;
	// by tokens ascending persist r4 (500k).
	desc := feed(t, newTestModel(t, ""))
	desc.sortKey, desc.sortDesc = "tokens", true
	if got := desc.visible()[0].row.BindingName; got != "api" {
		t.Errorf("tokens desc: first row %q, want api", got)
	}
	asc := feed(t, newTestModel(t, ""))
	asc.sortKey, asc.sortDesc = "tokens", false
	if got := asc.visible()[0].row.Number; got != 4 {
		t.Errorf("tokens asc: first row r%d, want r4", got)
	}

	// Grouped, s cycles the group keys: the default is tokens, so the first
	// press lands on rounds.
	g := feed(t, newTestModel(t, "by:candidate"))
	if g.groupedLevel() != true {
		t.Fatal("grouped model's cursor is not on a group line")
	}
	res, _ = g.Update(key("s"))
	g = res
	if g.SortKey() != "rounds" {
		t.Errorf("grouped first s: SortKey = %q, want rounds", g.SortKey())
	}
	res, _ = g.Update(key("s"))
	g = res
	if g.SortKey() != "halted" {
		t.Errorf("grouped second s: SortKey = %q, want halted", g.SortKey())
	}
}

func TestCursorBoundsAndExpansion(t *testing.T) {
	m := feed(t, newTestModel(t, ""))
	if len(m.visible()) != 6 {
		t.Fatalf("flat: %d visible lines, want 6", len(m.visible()))
	}
	res, _ := m.Update(special(tea.KeyUp))
	m = res
	if m.cursor != 1 {
		t.Errorf("up at the top: cursor = %d, want 1", m.cursor)
	}
	res, _ = m.Update(special(tea.KeyEnd))
	m = res
	if m.cursor != 5 {
		t.Errorf("end: cursor = %d, want 5", m.cursor)
	}
	res, _ = m.Update(special(tea.KeyDown))
	m = res
	if m.cursor != 5 {
		t.Errorf("down at the bottom: cursor = %d, want 5", m.cursor)
	}
	res, _ = m.Update(special(tea.KeyHome))
	m = res
	if m.cursor != 1 {
		t.Errorf("home: cursor = %d, want 1", m.cursor)
	}
	res, _ = m.Update(special(tea.KeyPgDown))
	m = res
	if m.cursor != 5 {
		t.Errorf("pgdown: cursor = %d, want 5", m.cursor)
	}
	res, _ = m.Update(special(tea.KeyPgUp))
	m = res
	if m.cursor != 1 {
		t.Errorf("pgup: cursor = %d, want 1", m.cursor)
	}

	g := feed(t, newTestModel(t, "by:candidate"))
	if len(g.visible()) != 2 {
		t.Fatalf("grouped: %d visible lines, want 2 groups", len(g.visible()))
	}
	g.cursor = 1
	res, _ = g.Update(special(tea.KeyPgDown))
	g = res
	if g.cursor != 1 {
		t.Errorf("pgdown clamps to the last group: cursor = %d, want 1", g.cursor)
	}
}

func TestShouldRefreshEvery10s(t *testing.T) {
	m := newTestModel(t, "")
	if !m.ShouldRefresh(testNow) {
		t.Error("a never-fetched screen should refresh")
	}
	m.lastFetch = testNow.Add(-9 * time.Second)
	if m.ShouldRefresh(testNow) {
		t.Error("9s since the last fetch: should not refresh")
	}
	m.lastFetch = testNow.Add(-10 * time.Second)
	if !m.ShouldRefresh(testNow) {
		t.Error("10s since the last fetch: should refresh")
	}
	m.fetching = true
	m.lastFetch = time.Time{}
	if m.ShouldRefresh(testNow) {
		t.Error("a fetch in flight blocks the next one")
	}
}
