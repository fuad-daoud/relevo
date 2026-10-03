package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/muesli/termenv"
)

// TestFleetWindowTopLines ports the windowing tests to the fleet table
// (R2.10): it counts lines, not rows, and keeps the cursor row's lines
// fully visible. The two-line NEEDS YOU case is included.
func TestFleetWindowTopLines(t *testing.T) {
	// 10 single-line rows.
	var one []fleetLine
	for i := 0; i < 10; i++ {
		one = append(one, fleetLine{text: fmt.Sprintf("row %d", i), row: i})
	}
	// 10 two-line rows (each row has a question line under it).
	var two []fleetLine
	for i := 0; i < 10; i++ {
		two = append(two, fleetLine{text: fmt.Sprintf("row %d", i), row: i}, fleetLine{text: "  ╰ q", row: i})
	}

	cases := []struct {
		name    string
		lines   []fleetLine
		cursor  int
		top     int
		height  int
		wantTop int
	}{
		{"no limit", one, 0, 0, 0, 0},
		{"fits", one, 3, 0, 5, 0},
		{"cursor below", one, 7, 0, 5, 3},
		{"cursor above", one, 2, 6, 5, 2},
		{"already visible", one, 4, 3, 5, 3},
		{"clamped from past the end", one, 4, 9, 5, 4},
		{"last row", one, 9, 0, 5, 5},
		{"two-line row below", two, 7, 0, 5, 11},
		{"two-line row above", two, 2, 12, 5, 4},
	}
	for _, tc := range cases {
		f := fleetView{cursor: tc.cursor, top: tc.top}
		if got := f.windowTopLines(tc.lines, tc.height); got != tc.wantTop {
			t.Errorf("%s: windowTopLines(cursor %d, top %d, h %d) = %d, want %d",
				tc.name, tc.cursor, tc.top, tc.height, got, tc.wantTop)
		}
	}
}

// TestBodyHeight ports the old bodyRows budget to frame.bodyHeight.
func TestBodyHeight(t *testing.T) {
	env := Env{Height: 24}
	if got := bodyHeight(env); got != 19 {
		t.Errorf("height 24, no error: bodyHeight = %d, want 19", got)
	}
	env.ErrRows = 2
	if got := bodyHeight(env); got != 17 {
		t.Errorf("height 24, two-line error: bodyHeight = %d, want 17", got)
	}
	env = Env{Height: 2}
	if got := bodyHeight(env); got != 0 {
		t.Errorf("height 2: bodyHeight = %d, want 0", got)
	}
	env = Env{Height: 0}
	if got := bodyHeight(env); got != 0 {
		t.Errorf("height 0: bodyHeight = %d, want 0", got)
	}
}

// tenBindings builds a shell at 80 x height showing b00..b{count-1}.
func tenBindings(t *testing.T, height, count int) Model {
	t.Helper()
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: view.Report{Bindings: bindingStatuses(count)}})
	return res.(Model)
}

// bindingStatuses builds Display:"ACTIVE" bindings named b00..b{count-1}.
func bindingStatuses(count int) []view.BindingStatus {
	bs := make([]view.BindingStatus, count)
	for i := range bs {
		bs[i] = view.BindingStatus{Name: fmt.Sprintf("b%02d", i), Display: "ACTIVE"}
	}
	return bs
}

// press sends r as a KeyMsg through Update and returns the updated model.
func press(t *testing.T, m Model, r rune) Model {
	t.Helper()
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return res.(Model)
}

// assertCursorVisible fails t unless the fleet view shows the cursor row for
// name, the header, the keys row, and exactly m.height-1 newlines.
func assertCursorVisible(t *testing.T, m Model, name string) {
	t.Helper()
	view := m.View()
	found := false
	for _, line := range strings.Split(plain(view), "\n") {
		if strings.Contains(line, "▍") && strings.Contains(line, name) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("view must show the cursor row for %s, got:\n%s", name, view)
	}
	if !strings.Contains(plain(view), "relevo") {
		t.Errorf("view must contain the header, got:\n%s", view)
	}
	if !strings.Contains(plain(view), "open") {
		t.Errorf("view must contain the keys row, got:\n%s", view)
	}
	if got := strings.Count(m.View(), "\n"); got != m.height-1 {
		t.Errorf("view must have %d newlines at height %d, got %d:\n%s",
			m.height-1, m.height, got, view)
	}
}

func TestFleetKeepsCursorVisibleWhenScrollingDown(t *testing.T) {
	m := tenBindings(t, 12, 20)
	for i := 1; i <= 12; i++ {
		m = press(t, m, 'j')
		assertCursorVisible(t, m, fmt.Sprintf("b%02d", i))
	}
}

func TestFleetKeepsCursorVisibleWhenScrollingUp(t *testing.T) {
	m := tenBindings(t, 12, 20)
	for i := 0; i < 12; i++ {
		m = press(t, m, 'j')
	}
	top := fleet(m).top
	for i := 11; i >= 0; i-- {
		m = press(t, m, 'k')
		assertCursorVisible(t, m, fmt.Sprintf("b%02d", i))
		if i == 11 {
			if fleet(m).top != top {
				t.Errorf("after the first k the window must not move: top %d -> %d", top, fleet(m).top)
			}
		}
	}
}

func TestFleetRewindowsOnResize(t *testing.T) {
	m := tenBindings(t, 15, 20)
	for i := 0; i < 7; i++ {
		m = press(t, m, 'j')
	}
	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	m = res.(Model)
	assertCursorVisible(t, m, "b07")
	if got := strings.Count(m.View(), "\n"); got != 7 {
		t.Errorf("after resize to height 8 the view must have 7 newlines, got %d:\n%s", got, m.View())
	}
}

func TestFleetRewindowsWhenBindingRemoved(t *testing.T) {
	m := tenBindings(t, 12, 10)
	for i := 0; i < 9; i++ {
		m = press(t, m, 'j')
	}
	res, _ := m.Update(statusMsg{report: view.Report{Bindings: bindingStatuses(5)}})
	m = res.(Model)
	if got := fleet(m).cursor; got != 4 {
		t.Errorf("cursor must clamp to 4 (b04) when bindings are removed, got %d", got)
	}
	assertCursorVisible(t, m, "b04")
}

func TestCursorFollowsBindingByNameAcrossInsert(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})
	m.now = func() time.Time { return railNow }

	initial := view.Report{Bindings: []view.BindingStatus{
		{Name: "bravo", Display: "ACTIVE"},
		{Name: "charlie", Display: "ACTIVE"},
	}}
	res, _ := m.Update(statusMsg{report: initial})
	m = res.(Model)
	fv := fleet(m)
	if fv.cursor != 0 || fv.sticky != "bravo" {
		t.Fatalf("expected cursor at 0 (bravo), got %d (%s)", fv.cursor, fv.sticky)
	}

	updated := view.Report{Bindings: []view.BindingStatus{
		{Name: "alpha", Display: "ACTIVE"},
		{Name: "bravo", Display: "ACTIVE"},
		{Name: "charlie", Display: "ACTIVE"},
	}}
	res, _ = m.Update(statusMsg{report: updated})
	m = res.(Model)
	fv = fleet(m)
	if fv.cursor != 1 {
		t.Errorf("expected cursor to track 'bravo' to index 1, got %d", fv.cursor)
	}
	if fv.sticky != "bravo" {
		t.Errorf("expected sticky to remain 'bravo', got %s", fv.sticky)
	}
}

func TestCursorClampsWhenBindingRemoved(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})
	m.now = func() time.Time { return railNow }

	res, _ := m.Update(statusMsg{report: view.Report{Bindings: []view.BindingStatus{
		{Name: "alpha", Display: "ACTIVE"},
		{Name: "bravo", Display: "ACTIVE"},
	}}})
	m = res.(Model)

	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = res.(Model)
	if fv := fleet(m); fv.cursor != 1 || fv.sticky != "bravo" {
		t.Fatalf("expected cursor on bravo (1), got %d (%s)", fv.cursor, fv.sticky)
	}

	res, _ = m.Update(statusMsg{report: view.Report{Bindings: []view.BindingStatus{
		{Name: "alpha", Display: "ACTIVE"},
	}}})
	m = res.(Model)
	fv := fleet(m)
	if fv.cursor != 0 {
		t.Errorf("expected cursor clamped to 0, got %d", fv.cursor)
	}
	if fv.sticky != "alpha" {
		t.Errorf("expected sticky re-pointed to 'alpha', got %s", fv.sticky)
	}
}

func TestEmptyBindingsList(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = res.(Model)

	res, _ = m.Update(statusMsg{report: view.Report{Bindings: nil}})
	m = res.(Model)

	if !strings.Contains(m.View(), "no bindings") {
		t.Errorf("expected 'no bindings' in view, got:\n%s", m.View())
	}

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	if len(m.stack) != 1 {
		t.Errorf("expected no push on an empty fleet, depth = %d", len(m.stack))
	}
	if cmd != nil {
		t.Errorf("expected enter on empty list to return nil cmd, got %v", cmd)
	}
}

func TestQuitFromList(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected quit cmd, got nil")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q at depth 1 must return tea.Quit")
	}
}

func TestStateStylesDistinguishable(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	states := []string{"NEEDS YOU", "ACTIVE", "DONE"}
	rendered := make(map[string]string, len(states))
	for _, s := range states {
		rendered[s] = stateStyle(s).Render(s)
	}
	for i, a := range states {
		for _, b := range states[i+1:] {
			if rendered[a] == rendered[b] {
				t.Fatalf("state display styles must be distinguishable:\n%s: %q\n%s: %q", a, rendered[a], b, rendered[b])
			}
		}
	}
}

// TestFleetThreeStates ports the rail's three list states (R2.10): loading
// before the first status, an unavailable status after only a failure, and
// "no bindings" after a successful empty report.
func TestFleetThreeStates(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = res.(Model)
	if !strings.Contains(m.View(), "loading…") {
		t.Errorf("fresh model must render 'loading…', got:\n%s", m.View())
	}
	if strings.Contains(m.View(), "no bindings") {
		t.Errorf("fresh model must NOT render 'no bindings', got:\n%s", m.View())
	}

	res, _ = m.Update(statusMsg{err: errors.New("harness connection refused")})
	m = res.(Model)
	if !strings.Contains(m.View(), "harness connection refused") {
		t.Errorf("a failing statusMsg must render the error block, got:\n%s", m.View())
	}
	if strings.Contains(m.View(), "no bindings") {
		t.Errorf("a failing statusMsg before any success must NOT render 'no bindings', got:\n%s", m.View())
	}

	res, _ = m.Update(statusMsg{report: view.Report{Bindings: nil}})
	m = res.(Model)
	if !strings.Contains(m.View(), "no bindings") {
		t.Errorf("successful empty report must render 'no bindings', got:\n%s", m.View())
	}

	b := view.BindingStatus{Name: "webshop", Round: 1, Display: "ACTIVE"}
	res, _ = m.Update(statusMsg{report: view.Report{Bindings: []view.BindingStatus{b}}})
	m = res.(Model)
	if !strings.Contains(m.View(), "webshop") {
		t.Fatalf("expected 'webshop' in the fleet, got:\n%s", m.View())
	}

	res, _ = m.Update(statusMsg{err: errors.New("intermittent failure")})
	m = res.(Model)
	if !strings.Contains(m.View(), "webshop") {
		t.Errorf("a failing poll after a successful one must keep the last good fleet, got:\n%s", m.View())
	}
	if strings.Contains(m.View(), "cannot reach the daemon") || strings.Contains(m.View(), "no bindings") {
		t.Errorf("a failing poll after a successful one must not revert, got:\n%s", m.View())
	}
}

// TestRenderErrorAndErrorBlock ports the error-block tests to the frame
// (R2.10).
func TestRenderErrorAndErrorBlock(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	// 1. A three-line error renders as three lines, none wider than width.
	threeLineErr := errors.New("error line one\nerror line two\nerror line three")
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m = res.(Model)
	res, _ = m.Update(statusMsg{err: threeLineErr})
	m = res.(Model)
	rendered := m.View()

	errRendered := renderError(threeLineErr, 60)
	errLines := strings.Split(errRendered, "\n")
	if len(errLines) != 3 {
		t.Fatalf("expected 3 error lines, got %d: %q", len(errLines), errLines)
	}
	for i, l := range errLines {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("error line %d width %d > 60: %q", i, w, l)
		}
		if !strings.Contains(rendered, l) {
			t.Errorf("expected view to contain error line %q", l)
		}
	}

	// 2. A multi-line error renders in full, its last line included.
	multiLineMsg := "client protocol 22 is newer than server protocol 20; restart the daemon\n" +
		"before using this command. Stop the old process to use the new version.\n" +
		"Stopping exits running processes.\n" +
		"Run `relevo daemon --stop`, then restart relevo with the\n" +
		"same socket override."
	res, _ = m.Update(statusMsg{err: errors.New(multiLineMsg)})
	m = res.(Model)
	if !strings.Contains(m.View(), "same socket override") {
		t.Errorf("expected the last line of a multi-line error, got:\n%s", m.View())
	}

	// 3. More than maxErrorLines is capped and ends with "…".
	tenLineMsg := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10"
	cappedLines := strings.Split(renderError(errors.New(tenLineMsg), 80), "\n")
	if len(cappedLines) != maxErrorLines {
		t.Fatalf("expected %d lines, got %d", maxErrorLines, len(cappedLines))
	}
	if !strings.HasSuffix(cappedLines[len(cappedLines)-1], "…") {
		t.Fatalf("expected capped error to end with '…', got %q", cappedLines[len(cappedLines)-1])
	}

	// 4. The keys row carries the marker, not the error text.
	f := stripANSI(m.keysView(m.env()))
	if !strings.Contains(f, "! refresh failed (retrying)") {
		t.Errorf("keys row must contain '! refresh failed (retrying)', got %q", f)
	}
	if strings.Contains(f, "client protocol") {
		t.Errorf("keys row must NOT contain error text, got %q", f)
	}

	// 5. A nil error adds no error block and no marker.
	res, _ = m.Update(statusMsg{report: view.Report{}})
	m = res.(Model)
	if strings.Contains(m.View(), "refresh failed") || strings.Contains(m.View(), "client protocol") {
		t.Errorf("nil err must not add an error block or marker, got:\n%s", m.View())
	}
}
