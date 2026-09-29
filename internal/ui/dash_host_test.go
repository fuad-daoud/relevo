package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
	"github.com/fuad-daoud/relevo/internal/view"
)

// dashHostModel is splitModel with a database behind the source, so the
// `:rounds` command line can open the rounds view. The db is empty: what
// these tests read is the host's wiring, not the rows.
func dashHostModel(t *testing.T, width, height int, opts Options, rows ...view.BindingStatus) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st, DB: d}
	opts.Interval = time.Second
	m := newModel(context.Background(), mastermindSource{rt}, opts)
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: view.Report{Bindings: rows}})
	return res.(Model)
}

// typeLine types a command line and presses enter, returning the model.
func typeLine(t *testing.T, m Model, line string) Model {
	t.Helper()
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = res.(Model)
	for _, r := range line {
		res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = res.(Model)
	}
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	return drain(t, m, cmd)
}

// TestCmdRoundsEntersAndFleetLeaves: the `d` key is gone (X5); `:rounds`
// opens the rounds view and `:fleet` returns to the fleet.
func TestCmdRoundsEntersAndFleetLeaves(t *testing.T) {
	m := dashHostModel(t, 140, 40, Options{}, threeRows()...)

	m = typeLine(t, m, "rounds")
	if _, ok := m.top().(roundsView); !ok {
		t.Fatalf(":rounds must open the rounds view, got %T", m.top())
	}

	m = typeLine(t, m, "fleet")
	if _, ok := m.top().(fleetView); !ok {
		t.Fatalf(":fleet must return to the fleet, got %T", m.top())
	}
}

// TestCmdRoundsWithoutDBNotices: `:rounds` with no database is the same
// refusal the dashboard once showed, and the fleet stays.
func TestCmdRoundsWithoutDBNotices(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m = typeLine(t, m, "rounds")
	if _, ok := m.top().(fleetView); !ok {
		t.Errorf("no database: top = %T, want the fleet", m.top())
	}
	if !strings.Contains(m.notice, "no database") {
		t.Errorf("no database: notice = %q", m.notice)
	}
}

// TestJumpFromDashPushesLiveRound: a JumpMsg naming a live row resolves to
// a roundOpenMsg and a pushed round view.
func TestJumpFromDashPushesLiveRound(t *testing.T) {
	rows := []view.BindingStatus{{Name: "persist", Round: 3, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working"}}
	m := dashHostModel(t, 140, 40, Options{}, rows...)
	rv, _, err := newRoundsView(m.env(), "", "")
	if err != nil {
		t.Fatalf("newRoundsView: %v", err)
	}
	m.stack = append(m.stack, rv)

	res, cmd := m.Update(dash.JumpMsg{BindingName: "persist", BindingID: "b1", Round: 1})
	m = res.(Model)
	m = drain(t, m, cmd)

	got, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("openRound must push a round view, got %T", m.top())
	}
	if got.pane.detail.name != "persist" || got.pane.detail.round != 1 {
		t.Errorf("detail = %s r%d, want persist r1", got.pane.detail.name, got.pane.detail.round)
	}
}

// TestJumpFromDashArchivedUsesDatabase replaces the old scope-all jump
// (X3): a jump to a binding that is not live resolves through
// relevo.Bindings and pushes an archived round view.
func TestJumpFromDashArchivedUsesDatabase(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusLoaded = true
	rv, _, err := newRoundsView(m.env(), "", "")
	if err != nil {
		t.Fatalf("newRoundsView: %v", err)
	}
	m.stack = append(m.stack, rv)

	res, cmd := m.Update(dash.JumpMsg{BindingName: h.Name, BindingID: h.ID, Round: 2})
	m = res.(Model)
	m = drain(t, m, cmd)

	got, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("openRound must push a round view, got %T", m.top())
	}
	if got.pane.detail.live {
		t.Error("detail.live = true for an archived jump")
	}
	if got.pane.detail.name != h.Name {
		t.Errorf("detail.name = %q, want %q", got.pane.detail.name, h.Name)
	}
}

// TestOptionsStartRounds: --dashboard start becomes Options.Start = "rounds".
func TestOptionsStartRounds(t *testing.T) {
	rows := threeRows()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st, DB: d}}, Options{Interval: time.Second, Start: "rounds"})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	res, cmd := m.Update(statusMsg{report: view.Report{Bindings: rows}})
	m = res.(Model)
	m = drain(t, m, cmd)

	if _, ok := m.top().(roundsView); !ok {
		t.Fatalf("Options.Start = rounds must open the rounds view after the first status, got %T", m.top())
	}
	if !m.started {
		t.Error("the start command must set started")
	}
}

// TestOptionsStartRoundsWithoutDBNotices: the start command with no
// database leaves the fleet and notices.
func TestOptionsStartRoundsWithoutDBNotices(t *testing.T) {
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Start: "rounds"})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	res, cmd := m.Update(statusMsg{report: view.Report{Bindings: threeRows()}})
	m = res.(Model)
	m = drain(t, m, cmd)

	if _, ok := m.top().(fleetView); !ok {
		t.Errorf("no database: top = %T, want the fleet", m.top())
	}
	if !strings.Contains(m.notice, "no database") {
		t.Errorf("notice = %q, want the no-database text", m.notice)
	}
}

// TestColonInsideRoundsFilterIsForwarded pins §5.2 rule 4 over rule 5: while
// the rounds filter (the dashboard's / editor) is open, the view captures
// every key, so ':' must reach the filter and must not open the command
// line. This is the test the R2.13 mutation swaps rules for.
func TestColonInsideRoundsFilterIsForwarded(t *testing.T) {
	m := dashHostModel(t, 140, 40, Options{}, threeRows()...)
	m = typeLine(t, m, "rounds")

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = res.(Model)
	if !m.top().Capturing() {
		t.Fatal("/ must open the dashboard filter (the view must capture)")
	}

	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = res.(Model)
	if m.cmd.open {
		t.Error(": must be forwarded to the capturing rounds filter, not open the command line")
	}
}

// TestRoundsSavePrefsOnChange: a query and a sort change in the rounds view
// are persisted through prefMsg.
func TestRoundsSavePrefsOnChange(t *testing.T) {
	ps := testPrefsStore(t)
	m := dashHostModel(t, 140, 40, Options{Prefs: ps}, threeRows()...)
	m = typeLine(t, m, "rounds")

	// Sort: s changes the screen's sort key, and the prefMsg must save.
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = res.(Model)
	m = drain(t, m, cmd)
	gotSort := loadPrefs(ps).DashboardSort
	if gotSort == "" {
		t.Fatal("s did not persist a sort key")
	}

	// Query: / then text then enter.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = res.(Model)
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("harness:agy")})
	m = res.(Model)
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	m = drain(t, m, cmd)
	if got := loadPrefs(ps).Dashboard; got != "harness:agy" {
		t.Errorf("saved Dashboard = %q, want harness:agy", got)
	}
}

func TestRoundsContextSummary(t *testing.T) {
	m := goldenRoundsModel(t, 160, 40)
	rv, ok := m.top().(roundsView)
	if !ok {
		t.Fatalf("top is %T, want roundsView", m.top())
	}
	left, right := rv.Context(m.env())
	leftPlain := stripANSI(left)
	if !strings.Contains(leftPlain, "3 rounds") {
		t.Errorf("left = %q, want '3 rounds'", leftPlain)
	}
	if strings.Contains(leftPlain, "$") {
		t.Errorf("left = %q contains $", leftPlain)
	}
	rightPlain := stripANSI(right)
	if !strings.Contains(rightPlain, "sort newest") {
		t.Errorf("right = %q, want 'sort newest'", rightPlain)
	}

	// After b, the right has by and binding
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = drain(t, res.(Model), cmd)
	rv = m.top().(roundsView)
	_, right = rv.Context(m.env())
	rightPlain = stripANSI(right)
	if !strings.Contains(rightPlain, "by") || !strings.Contains(rightPlain, "binding") {
		t.Errorf("after b: right = %q, want 'by' and 'binding'", rightPlain)
	}
}

func TestRoundsRunningFromReport(t *testing.T) {
	m := goldenRoundsModel(t, 160, 40)
	rows := dashRows()
	for i := range rows {
		if rows[i].BindingName == "persist" && rows[i].Number == 5 {
			rows[i].Outcome = db.OutcomeOpen
		}
	}
	res, _ := m.Update(dash.RowsMsg{Rows: rows, At: railNow})
	m = res.(Model)

	b := view.BindingStatus{
		Name:          "persist",
		Round:         5,
		Display:       "ACTIVE",
		BuilderStatus: "working",
	}
	res, cmd := m.Update(statusMsg{report: view.Report{Bindings: []view.BindingStatus{b}}})
	m = drain(t, res.(Model), cmd)

	body := m.View()
	bodyPlain := stripANSI(body)
	if !strings.Contains(bodyPlain, "running") {
		t.Errorf("body does not contain 'running':\n%s", bodyPlain)
	}
}

func TestRoundsContextFitsAt100(t *testing.T) {
	m := goldenRoundsModel(t, 100, 40)
	rows := append([]db.RoundRow(nil), dashRows()...)
	pStr := func(s string) *string { return &s }
	pInt64 := func(n int64) *int64 { return &n }
	extraOutcomes := []struct {
		outcome string
		report  *string
	}{
		{db.OutcomeReported, pStr("done")},
		{db.OutcomeHalted, nil},
		{db.OutcomeReported, pStr("blocked")},
		{db.OutcomeExited, nil},
		{db.OutcomeSwitched, nil},
	}
	for i, eo := range extraOutcomes {
		rows = append(rows, db.RoundRow{
			BindingID:     "b-extra",
			BindingName:   "extra",
			Number:        10 + i,
			StartedAt:     railNow.Add(-time.Duration(i+1) * time.Hour),
			Outcome:       eo.outcome,
			ReportOutcome: eo.report,
			InTokens:      pInt64(500_000),
		})
	}
	res, _ := m.Update(dash.RowsMsg{Rows: rows, At: railNow})
	m = res.(Model)

	rv, ok := m.top().(roundsView)
	if !ok {
		t.Fatalf("top is %T, want roundsView", m.top())
	}
	left, right := rv.Context(m.env())
	if w := lipgloss.Width(left) + lipgloss.Width(right); w > 100 {
		t.Errorf("total width = %d > 100 (left=%d, right=%d)", w, lipgloss.Width(left), lipgloss.Width(right))
	}
	rightPlain := stripANSI(right)
	if !strings.Contains(rightPlain, "sort newest") {
		t.Errorf("right = %q, want 'sort newest'", rightPlain)
	}
}
