package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// TestWhatAge ports the rail's whatAge assertions to view_fleet.go (R2.10).
func TestWhatAge(t *testing.T) {
	cases := []struct {
		name     string
		b        view.BindingStatus
		wantWhat string
		wantAge  string
	}{
		{
			name: "needs you blocked",
			b: view.BindingStatus{Display: "NEEDS YOU",
				Waiting: &view.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)}},
			wantWhat: "question", wantAge: "2m",
		},
		{
			name:     "needs you detail",
			b:        view.BindingStatus{Display: "NEEDS YOU", Detail: "broken: no space"},
			wantWhat: "broken: no space",
		},
		{
			name:     "needs you bare",
			b:        view.BindingStatus{Display: "NEEDS YOU"},
			wantWhat: "needs you",
		},
		{
			name:     "active working",
			b:        view.BindingStatus{Display: "ACTIVE", BuilderStatus: "working"},
			wantWhat: "working",
		},
		{
			name:     "done",
			b:        view.BindingStatus{Display: "DONE", Last: &view.LastEvent{TS: railNow.Add(-3 * time.Hour)}},
			wantWhat: "done", wantAge: "3h",
		},
		{
			name:     "paused",
			b:        view.BindingStatus{Display: "PAUSED", Last: &view.LastEvent{TS: railNow.Add(-time.Hour), Kind: "pause"}},
			wantWhat: "paused", wantAge: "1h",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotWhat, gotAge := whatAge(tc.b, railNow)
			if gotWhat != tc.wantWhat || gotAge != tc.wantAge {
				t.Errorf("whatAge = (%q, %q), want (%q, %q)", gotWhat, gotAge, tc.wantWhat, tc.wantAge)
			}
		})
	}
}

// TestNowCellCarriesTheStaleLabel is the stale-label port (#135): a NEEDS
// YOU row carries its stale age in the NOW cell.
func TestNowCellCarriesTheStaleLabel(t *testing.T) {
	b := view.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderKind: "agy",
		Stale:   "stale 4h 0m",
		Waiting: &view.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)},
	}
	if got := nowCell(b, railNow); !strings.Contains(got, "· stale 4h 0m") {
		t.Errorf("nowCell = %q, want it to carry %q", got, "· stale 4h 0m")
	}
}

// TestCandidateText pins the ON cell's naming rule in the one place it
// lives: the model part is everything after the second '/'.
func TestCandidateText(t *testing.T) {
	cases := map[string]string{
		"cline-pass/deepseek-v4.1-flash#high": "deepseek-v4.1-flash#high",
		"agy":                                 "agy",
		"":                                    "-",
	}
	for in, want := range cases {
		if got := candidateText(view.BindingStatus{BuilderCandidate: in}); got != want {
			t.Errorf("candidateText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSpendText pins the SPEND cell (A2): money only. Measured and estimated
// dollars sum, a '~' marks a partly estimated sum, a plan lane reads "plan",
// and anything under a cent reads "<$0.01".
func TestSpendText(t *testing.T) {
	cases := []struct {
		name string
		s    usage.Spend
		want string
	}{
		{"measured only", usage.Spend{Measured: 9.40}, "$9.40"},
		{"measured and estimated", usage.Spend{Measured: 1.23, Estimated: 0.40}, "~$1.63"},
		{"estimated only", usage.Spend{Estimated: 0.02}, "~$0.02"},
		{"plan lane", usage.Spend{Rounds: 2, Plan: 2}, "plan"},
		{"measured beats a plan lane", usage.Spend{Measured: 0.10, Plan: 1}, "$0.10"},
		{"under a cent", usage.Spend{Measured: 0.004}, "<$0.01"},
		{"under a cent, estimated", usage.Spend{Estimated: 0.004}, "~<$0.01"},
		{"nothing measured", usage.Spend{Rounds: 3, Unknown: 2}, ""},
		{"zero", usage.Spend{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := spendText(tc.s); got != tc.want {
				t.Errorf("spendText(%+v) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}

// TestFleetBodyHeader pins A1: the table's first body line is the header, and
// TestFleetBodyHeader pins D4: the table's first list lines are section lines,
// and neither the loading nor the empty body has one.
func TestFleetBodyHeader(t *testing.T) {
	rows := []view.BindingStatus{{Name: "webshop", Round: 4, Display: "ACTIVE", BuilderStatus: "working"}}
	loaded := Env{Loaded: true, Now: railNow, Report: view.Report{Bindings: rows}, Width: 140, Height: 40}
	f := newFleetView(true)

	body := stripANSI(f.Body(loaded, 140, 40))
	if !strings.Contains(body, "working") {
		t.Errorf("body must contain the working section line, got:\n%s", body)
	}
	if !strings.Contains(body, "webshop") {
		t.Errorf("body must contain the binding row, got:\n%s", body)
	}

	loading := loaded
	loading.Loaded = false
	if body := stripANSI(f.Body(loading, 140, 40)); strings.Contains(body, "working") {
		t.Errorf("the loading body must have no section line: %q", body)
	}
	empty := loaded
	empty.Report = view.Report{}
	if body := stripANSI(f.Body(empty, 140, 40)); strings.Contains(body, "working") {
		t.Errorf("the empty body must have no section line: %q", body)
	}
}

// TestFleetHeaderSurvivesNarrowing pins D4: at 80 columns, name and now survive.
func TestFleetHeaderSurvivesNarrowing(t *testing.T) {
	b := view.BindingStatus{
		Name: "webshop", Round: 4, Display: "ACTIVE", BuilderStatus: "working",
		BuilderCandidate: "cline/deepseek", MasterMindName: "architect-1",
		Spend: &usage.Spend{Measured: 1.23},
	}
	line := stripANSI(fleetRowLine(b, groupWorking, false, railNow, 80))
	if !strings.Contains(line, "webshop") {
		t.Errorf("name must survive at 80, got %q", line)
	}
	if !strings.Contains(line, "working") {
		t.Errorf("now must survive at 80, got %q", line)
	}
}

// TestNeedsYouGrammarIsShared pins A3: the header and the fleet's context
// line read "1 needs you" for one and "N need you" for more, from the one
// helper, so they cannot drift.
func TestNeedsYouGrammarIsShared(t *testing.T) {
	one := splitModel(t, 140, 40, view.BindingStatus{Name: "webshop", Round: 1, Display: "NEEDS YOU"})
	if h := stripANSI(one.headerView(one.env())); !strings.Contains(h, "● 1 needs you") {
		t.Errorf("header = %q, want the singular", h)
	}
	left, _ := fleet(one).Context(one.env())
	if !strings.Contains(stripANSI(left), "1 needs you") {
		t.Errorf("context = %q, want the singular", stripANSI(left))
	}

	two := splitModel(t, 140, 40,
		view.BindingStatus{Name: "webshop", Round: 1, Display: "NEEDS YOU"},
		view.BindingStatus{Name: "docs", Round: 1, Display: "NEEDS YOU"},
	)
	if h := stripANSI(two.headerView(two.env())); !strings.Contains(h, "● 2 need you") {
		t.Errorf("header = %q, want the plural", h)
	}
	left, _ = fleet(two).Context(two.env())
	if !strings.Contains(stripANSI(left), "2 need you") {
		t.Errorf("context = %q, want the plural", stripANSI(left))
	}
}

// TestFleetFilter pins A4: '/' opens a capturing input, the typed text keeps
// only matching rows, the selection survives, enter keeps the filter, and
// esc clears it.
func TestFleetFilter(t *testing.T) {
	rows := []view.BindingStatus{
		{Name: "webshop", Round: 4, Display: "ACTIVE", CWD: "/home/x/web"},
		{Name: "docs", Round: 1, Display: "DONE"},
		{Name: "api", Round: 2, Display: "ACTIVE"},
	}
	env := Env{Loaded: true, Now: railNow, Report: view.Report{Bindings: rows}, Width: 140, Height: 40, StatusAt: railNow}
	f := newFleetView(true)
	f.showDone = true
	key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

	// Put the selection on "docs" first: it must survive the filter.
	all := f.rows(env)
	for i, b := range all {
		if b.Name == "docs" {
			f.cursor, f.sticky = i, b.Key()
		}
	}
	if got := f.rows(env)[f.cursor].Name; got != "docs" {
		t.Fatalf("cursor on %q, want docs", got)
	}

	// '/' opens the input; it captures every key while open.
	next, _ := f.Update(key('/'), env)
	f = next.(fleetView)
	if !f.Capturing() {
		t.Fatal("the filter input must capture keys while open")
	}

	// Type "do": only docs matches, and it stays selected.
	for _, r := range "do" {
		next, _ = f.Update(key(r), env)
		f = next.(fleetView)
	}
	if got := f.rows(env); len(got) != 1 || got[0].Name != "docs" {
		t.Fatalf("filtered rows = %v, want [docs]", got)
	}
	if got := f.rows(env)[f.cursor].Name; got != "docs" {
		t.Errorf("the selection must survive the filter, cursor on %q", got)
	}
	if _, right := f.Context(env); !strings.Contains(stripANSI(right), `filter "do"`) {
		t.Errorf("the context line must name the filter, right = %q", stripANSI(right))
	}

	// enter keeps the filter and closes the input.
	next, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	f = next.(fleetView)
	if f.Capturing() {
		t.Error("enter must close the filter input")
	}
	if got := f.rows(env); len(got) != 1 || got[0].Name != "docs" {
		t.Errorf("enter must keep the filter, rows = %v", got)
	}

	// esc at the fleet, not capturing, clears it.
	next, _ = f.Update(tea.KeyMsg{Type: tea.KeyEsc}, env)
	f = next.(fleetView)
	if got := f.rows(env); len(got) != len(rows) {
		t.Errorf("esc must clear the filter, rows = %v", got)
	}
	if _, right := f.Context(env); strings.Contains(stripANSI(right), "filter") {
		t.Errorf("the cleared filter must leave the context line, right = %q", stripANSI(right))
	}

	// esc while the input is open clears it and closes.
	next, _ = f.Update(key('/'), env)
	f = next.(fleetView)
	next, _ = f.Update(key('w'), env)
	f = next.(fleetView)
	if len(f.rows(env)) != 1 {
		t.Fatalf("typing must filter live, rows = %v", f.rows(env))
	}
	next, _ = f.Update(tea.KeyMsg{Type: tea.KeyEsc}, env)
	f = next.(fleetView)
	if f.Capturing() {
		t.Error("esc must close the filter input")
	}
	if got := f.rows(env); len(got) != len(rows) {
		t.Errorf("esc must clear the filter, rows = %v", got)
	}
}

// TestFleetFilterMatchesEveryShownField pins A4's field list: key, actor,
// candidate, mastermind, repo and state all match, case-insensitively.
func TestFleetFilterMatchesEveryShownField(t *testing.T) {
	b := view.BindingStatus{
		Name: "webshop", Round: 4, Display: "PAUSED", Role: "reviewer",
		BuilderCandidate: "cline-pass/deepseek-v4.1-flash#high",
		MasterMindName:   "architect-1", CWD: "/home/x/relevo",
	}
	for _, q := range []string{"WEBSHOP", "reviewer", "deepseek", "architect-1", "relevo", "paused"} {
		if !fleetRowMatches(b, q) {
			t.Errorf("fleetRowMatches(%q) = false, want true", q)
		}
	}
	if fleetRowMatches(b, "zzz") {
		t.Error("an unmatched text must keep nothing")
	}
}

// TestFleetColumnsAndNarrowDropOrder pins D4: the new drop order mastermind first,
// then spend, then candidate.
func TestFleetColumnsAndNarrowDropOrder(t *testing.T) {
	b := view.BindingStatus{
		Name: "webshop", Round: 4, Display: "ACTIVE", BuilderStatus: "working",
		BuilderCandidate: "cline-pass/deepseek-v4.1-flash#high",
		MasterMindName:   "architect-1",
		Spend:            &usage.Spend{Measured: 1.23},
	}
	cases := []struct {
		width     int
		wantPlan  bool
		wantSpend bool
		wantCand  bool
	}{
		{132, true, true, true},
		{90, false, true, true},
		{70, false, false, true},
		{50, false, false, false},
	}
	for _, tc := range cases {
		cand, spend, plan := fleetRowPlan(tc.width)
		if plan != tc.wantPlan || spend != tc.wantSpend || cand != tc.wantCand {
			t.Errorf("width %d: plan=%v spend=%v cand=%v, want plan=%v spend=%v cand=%v",
				tc.width, plan, spend, cand, tc.wantPlan, tc.wantSpend, tc.wantCand)
		}
		line := stripANSI(fleetRowLine(b, groupWorking, false, railNow, tc.width))
		if got := strings.Contains(line, "architect-1"); got != tc.wantPlan {
			t.Errorf("width %d: mastermind present = %v, want %v: %s", tc.width, got, tc.wantPlan, line)
		}
		if got := strings.Contains(line, "$1.23"); got != tc.wantSpend {
			t.Errorf("width %d: spend present = %v, want %v: %s", tc.width, got, tc.wantSpend, line)
		}
		if got := strings.Contains(line, "deepseek-v4.1-flash"); got != tc.wantCand {
			t.Errorf("width %d: the ON cell present = %v, want %v: %s", tc.width, got, tc.wantCand, line)
		}
	}
}

// TestFleetNeedsYouSecondLine pins §2.3 and D4: a NEEDS YOU row with a Waiting
// line gets a second line with the quote glyph "╰ ".
func TestFleetNeedsYouSecondLine(t *testing.T) {
	b := view.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		Waiting: &view.Waiting{Cause: "blocked", Line: "which database should r4 use?"},
	}
	f := newFleetView(true)
	env := Env{Loaded: true, Now: railNow, Report: view.Report{Bindings: []view.BindingStatus{b}}, Width: 140, Height: 40}
	lines := f.fleetListLines(env, 140)
	if len(lines) != 4 {
		t.Fatalf("%d lines, want 4 (section + row + question + blank)", len(lines))
	}
	if !strings.Contains(stripANSI(lines[2].text), "╰ which database should r4 use?") {
		t.Errorf("question line = %q", stripANSI(lines[2].text))
	}
	if lines[2].row != 0 {
		t.Errorf("the question line belongs to row 0, got %d", lines[2].row)
	}

	// No Waiting line: no second line.
	b.Waiting = &view.Waiting{Cause: "blocked"}
	env.Report = view.Report{Bindings: []view.BindingStatus{b}}
	if got := len(f.fleetListLines(env, 140)); got != 3 {
		t.Errorf("without a Waiting line: %d lines, want 3", got)
	}
}
