package ui

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/muesli/termenv"
)

func TestEnteringDetailFetchesPlanTabAndNoOther(t *testing.T) {
	st := store.New(t.TempDir())
	name := "webshop"

	if err := st.Save(newTestBinding(name)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := splitModel(t, 140, 40, view.BindingStatus{Name: name, Round: 2, Display: "ACTIVE"})

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	m = drain(t, m, cmd)

	rv, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("enter must push a round view, got %T", m.top())
	}
	if rv.pane.detail.name != name {
		t.Fatalf("expected detail.name %s, got %s", name, rv.pane.detail.name)
	}
	if rv.pane.detail.active != tabPlan {
		t.Fatalf("expected active tabPlan, got %v", rv.pane.detail.active)
	}
	if !rv.pane.detail.cache[tabPlan].loaded {
		t.Fatal("the plan tab's reply must have landed")
	}
}

func TestSwitchingToUnloadedTabFetchesOnlyThatOne(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.active = tabReport
	rv.pane.tabInFlight = false
	rv.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "x"}

	next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}}, testEnv(mastermindSource{rt}, rv.pane.report, 140, 40))
	got := next.(roundView)
	if got.pane.detail.active != tabDiff {
		t.Fatalf("expected active tabDiff, got %v", got.pane.detail.active)
	}
	if cmd == nil {
		t.Fatal("expected non-nil fetch command for unloaded tab")
	}
	tMsg, ok := cmd().(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", cmd())
	}
	if tMsg.t != tabDiff {
		t.Fatalf("expected tabDiff fetch, got %v", tMsg.t)
	}
}

func TestScrollParkAndRestore(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.active = tabReport
	rv.pane.detail.vp = viewport.New(140, 20)
	rv.pane.detail.cache[tabReport] = tabContent{loaded: true, body: strings.Repeat("report line\n", 50)}
	rv.pane.detail.cache[tabTerminal] = tabContent{loaded: true, body: strings.Repeat("terminal line\n", 50)}
	rv.pane.detail.vp.SetContent(rv.pane.detail.cache[tabReport].body)
	rv.pane.detail.vp.YOffset = 18

	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if rv.pane.detail.active != tabTerminal {
		t.Fatalf("expected active tabTerminal, got %v", rv.pane.detail.active)
	}
	if rv.pane.detail.scroll[tabReport] != 18 {
		t.Fatalf("expected parked scroll for tabReport to be 18, got %d", rv.pane.detail.scroll[tabReport])
	}

	rv.pane.detail.vp.YOffset = 7
	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if rv.pane.detail.active != tabReport {
		t.Fatalf("expected active tabReport, got %v", rv.pane.detail.active)
	}
	if rv.pane.detail.vp.YOffset != 18 {
		t.Fatalf("expected restored YOffset on tabReport to be 18, got %d", rv.pane.detail.vp.YOffset)
	}
	if rv.pane.detail.scroll[tabTerminal] != 7 {
		t.Fatalf("expected parked scroll for tabTerminal to be 7, got %d", rv.pane.detail.scroll[tabTerminal])
	}
}

func TestEmptyContentNotStyledAsError(t *testing.T) {
	c := tabContent{
		loaded: true,
		empty:  "no diff recorded for round 1 — no baseline captured",
	}

	st := styleFor(c)
	if reflect.DeepEqual(st, errorStyle) {
		t.Fatalf("empty content style must not be errorStyle")
	}
	if st.GetForeground() == errorStyle.GetForeground() {
		t.Fatalf("empty content foreground must not match errorStyle foreground")
	}

	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)
	rendered := bodyOf(tabDiff, c, false)
	errRendered := errorStyle.Render(c.empty)
	if rendered == errRendered {
		t.Fatalf("empty prose must NOT be styled with errorStyle")
	}
}

func TestTabErrorDoesNotCorruptOtherTabs(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.vp = viewport.New(80, 20)

	rv.pane.detail.cache[tabDiff] = tabContent{loaded: true, err: errors.New("disk read failed")}
	rv.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "## Successful report content"}

	rv.pane.detail.active = tabDiff
	rv.pane.detail.vp.SetContent(bodyOf(tabDiff, rv.pane.detail.cache[tabDiff], false))
	if !strings.Contains(rv.pane.detail.vp.View(), "error: disk read failed") {
		t.Fatalf("expected error text in diff tab, got %q", rv.pane.detail.vp.View())
	}

	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if !strings.Contains(rv.pane.detail.vp.View(), "Successful report content") {
		t.Fatalf("expected report content in report tab, got %q", rv.pane.detail.vp.View())
	}
}

func TestResizeReflowsViewportWithoutLosingActiveTab(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.active = tabDiff
	rv.pane.detail.vp = viewport.New(80, 20)

	next, _ := rv.Update(tea.WindowSizeMsg{Width: 100, Height: 60}, Env{Width: 100, Height: 60, Now: railNow, Report: rv.pane.report})
	got := next.(roundView)

	if got.pane.detail.active != tabDiff {
		t.Fatalf("expected active tab to remain tabDiff, got %v", got.pane.detail.active)
	}
	if got.pane.detail.vp.Width != got.pane.contentWidth() {
		t.Fatalf("expected vp.Width %d (contentWidth), got %d", got.pane.contentWidth(), got.pane.detail.vp.Width)
	}
	if got.pane.detail.vp.Height != got.pane.viewportHeight() {
		t.Fatalf("expected vp.Height %d, got %d", got.pane.viewportHeight(), got.pane.detail.vp.Height)
	}
}

func TestPanicOnShrinkingContent(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 3, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.active = tabDiff
	rv.pane.detail.vp = viewport.New(80, 20)
	rv.pane.detail.vp.SetContent(strings.Repeat("diff line\n", 200))
	rv.pane.detail.vp.SetYOffset(120)

	rv = roundMsg(rv, tabMsg{
		name:  "webshop",
		round: rv.pane.detail.round,
		t:     tabDiff,
		content: tabContent{
			loaded: true,
			body:   "line 1\nline 2\nline 3",
		},
	})

	view := rv.Body(testEnv(mastermindSource{rt}, rv.pane.report, 80, 24), 80, 20)
	if view == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestSwitchTabBackIntoInvalidatedTabNoPanic(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 3, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.active = tabDiff
	rv.pane.detail.vp = viewport.New(80, 20)
	rv.pane.detail.vp.SetContent(strings.Repeat("diff line\n", 200))
	rv.pane.detail.vp.SetYOffset(120)

	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	rv.pane.detail.cache[tabDiff] = tabContent{}
	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})

	if view := rv.Body(testEnv(mastermindSource{rt}, rv.pane.report, 80, 24), 80, 20); view == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestRefreshActiveTabPreservesLiveScroll(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 3, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.active = tabTerminal
	rv.pane.detail.follow = false
	rv.pane.detail.vp = viewport.New(80, 20)
	rv.pane.detail.vp.SetContent(strings.Repeat("line\n", 100))
	rv.pane.detail.vp.SetYOffset(42)

	rv = roundMsg(rv, tabMsg{
		name:  "webshop",
		round: rv.pane.detail.round,
		t:     tabTerminal,
		content: tabContent{
			loaded: true,
			body:   strings.Repeat("line\n", 100),
		},
	})

	if rv.pane.detail.vp.YOffset != 42 {
		t.Fatalf("scroll discarded by a refresh of the active tab: YOffset = %d, want 42", rv.pane.detail.vp.YOffset)
	}
}

func TestInvalidationResetsParkedOffset(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ts := time.Now()
	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: "webshop", Round: 3, Display: "ACTIVE", Last: &view.LastEvent{TS: ts, Round: 3}},
	}}
	rv := newTestRound(t, rt, rep, "webshop", 0)
	rv.pane.detail.active = tabTerminal
	rv.pane.detail.lastLogTS = ts
	rv.pane.detail.scroll[tabDiff] = 85
	rv.pane.detail.scroll[tabReport] = 40
	rv.pane.detail.scroll[tabLog] = 15
	rv.pane.detail.scroll[tabTerminal] = 10

	newRep := view.Report{Bindings: []view.BindingStatus{
		{Name: "webshop", Round: 3, Display: "ACTIVE", Last: &view.LastEvent{TS: ts.Add(5 * time.Second), Round: 3}},
	}}
	next, _ := rv.Update(statusMsg{report: newRep}, testEnv(mastermindSource{rt}, newRep, 140, 40))
	got := next.(roundView)

	if got.pane.detail.scroll[tabDiff] != 0 || got.pane.detail.scroll[tabReport] != 0 || got.pane.detail.scroll[tabLog] != 0 {
		t.Errorf("file-backed parked offsets must reset: %+v", got.pane.detail.scroll)
	}
	if got.pane.detail.scroll[tabTerminal] != 10 {
		t.Errorf("expected tabTerminal scroll preserved, got %d", got.pane.detail.scroll[tabTerminal])
	}
}

func TestNonRoundKeyedTabsAccepted(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"
	b := newTestBinding(name)
	b.Round = 4
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tests := []struct {
		name string
		key  string
		tab  tab
	}{
		{name: "log", key: "5", tab: tabLog},
		{name: "terminal", key: "3", tab: tabTerminal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: name, Round: 4, Display: "ACTIVE"}}}, name, 0)
			rv.pane.detail.round = 3
			rv.pane.detail.active = tabReport
			rv.pane.tabInFlight = false

			next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)}, testEnv(mastermindSource{rt}, rv.pane.report, 140, 40))
			got := next.(roundView)
			if cmd == nil {
				t.Fatalf("%s: expected non-nil cmd from switchTab", tc.name)
			}
			got = roundMsg(got, cmd())
			if !got.pane.detail.cache[tc.tab].loaded {
				t.Fatalf("%s reply discarded: tab stays on %q forever", tc.name, bodyOf(tc.tab, got.pane.detail.cache[tc.tab], false))
			}
		})
	}
}

func TestFiveTabsLoadContentEndToEnd(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"
	b := newTestBinding(name)
	b.Round = 4
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: name, Round: 4, Display: "ACTIVE"}}}, name, 0)
	rv.pane.detail.round = 3
	rv.pane.detail.active = tabReport
	rv.pane.tabInFlight = false

	tabKeys := []struct {
		key string
		t   tab
	}{
		{"1", tabPlan},
		{"2", tabReport},
		{"3", tabTerminal},
		{"4", tabDiff},
		{"5", tabLog},
	}
	for _, tk := range tabKeys {
		rv.pane.detail.cache[tk.t] = tabContent{}
		rv.pane.tabInFlight = false
		next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tk.key)}, testEnv(mastermindSource{rt}, rv.pane.report, 140, 40))
		rv = next.(roundView)
		if cmd == nil {
			t.Fatalf("tab %v: expected non-nil cmd from switchTab", tk.t)
		}
		rv = roundMsg(rv, cmd())
		rv.pane.tabInFlight = false

		if !rv.pane.detail.cache[tk.t].loaded {
			t.Fatalf("tab %v reply discarded: cache not loaded", tk.t)
		}
	}
}

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"vt", "a\x0bb", "a\uFFFDb"},
		{"ff", "a\x0cb", "a\uFFFDb"},
		{"crlf", "a\r\nb", "a\nb"},
		{"lone cr", "a\rb", "ab"},
		{"tab", "a\tb", "a    b"},
		{"esc sequence", "a\x1b[2Jb", "a\uFFFD[2Jb"},
		{"null and ack", "a\x00\x06b", "a\uFFFD\uFFFDb"},
		{"invalid utf8", "a\xffb", "a\uFFFDb"},
		{"c1 nel", "a\u0085b", "a\uFFFDb"},
		{"unicode unchanged", "héllo — ✓", "héllo — ✓"},
		{"newline unchanged", "line1\nline2", "line1\nline2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeText(tc.in); got != tc.want {
				t.Fatalf("sanitizeText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBodyOfControlBytesKeepLineCount(t *testing.T) {
	raw := "line 1\nmiddle \x0b \x0c \x1b[2J controls\nline 3"

	cases := []struct {
		name     string
		tab      tab
		headless bool
	}{
		{"transcript", tabTerminal, true},
		{"log", tabLog, false},
		{"diff", tabDiff, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := bodyOf(tc.tab, tabContent{loaded: true, body: raw}, tc.headless)
			wrapped := wrapBody(rendered, 80)
			stripped := stripANSI(wrapped)

			for _, r := range stripped {
				if r < 0x20 && r != '\n' {
					t.Fatalf("%s: result contains control rune below 0x20: %q (%#x)", tc.name, r, r)
				}
			}

			lines := strings.Split(stripped, "\n")
			if len(lines) != 3 {
				t.Fatalf("%s: expected 3 lines, got %d: %q", tc.name, len(lines), stripped)
			}
		})
	}
}
