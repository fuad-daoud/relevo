package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

func TestStatusMsgUnchangedTSNoFetch(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	name := "webshop"
	ts := time.Now().Truncate(time.Second)

	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: name, Round: 2, Display: "ACTIVE", Last: &view.LastEvent{TS: ts, Round: 2}},
	}}
	rv := newTestRound(t, rt, rep, name, 0)
	rv.pane.detail.active = tabReport
	rv.pane.detail.lastLogTS = ts
	rv.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "initial report"}
	rv.pane.tabInFlight = false

	next, cmd := rv.Update(statusMsg{report: rep}, testEnv(mastermindSource{rt}, rep, 140, 40))
	got := next.(roundView)

	if cmd != nil {
		t.Fatalf("expected nil cmd for unchanged TS, got %v", cmd)
	}
	if !got.pane.detail.cache[tabReport].loaded {
		t.Fatal("expected report cache to remain loaded when TS unchanged")
	}
}

func TestStatusMsgNewerTSClearsFileCachesPreservesTerminal(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"
	if err := st.Save(newTestBinding(name)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ts := time.Now().Truncate(time.Second)
	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: name, Round: 2, Display: "ACTIVE", Last: &view.LastEvent{TS: ts, Round: 2}},
	}}
	rv := newTestRound(t, rt, rep, name, 0)
	rv.pane.detail.active = tabReport
	rv.pane.detail.lastLogTS = ts
	rv.pane.detail.vp = viewport.New(80, 20)
	rv.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "cached report"}
	rv.pane.detail.cache[tabDiff] = tabContent{loaded: true, body: "cached diff"}
	rv.pane.detail.cache[tabLog] = tabContent{loaded: true, body: "cached log"}
	rv.pane.detail.cache[tabTerminal] = tabContent{loaded: true, body: "live terminal"}
	rv.pane.tabInFlight = false

	newTS := ts.Add(10 * time.Second)
	newRep := view.Report{Bindings: []view.BindingStatus{
		{Name: name, Round: 4, Display: "ACTIVE", Last: &view.LastEvent{TS: newTS, Round: 4}},
	}}
	next, cmd := rv.Update(statusMsg{report: newRep}, testEnv(mastermindSource{rt}, newRep, 140, 40))
	got := next.(roundView)

	for _, tb := range []tab{tabReport, tabDiff, tabLog} {
		if got.pane.detail.cache[tb].loaded {
			t.Errorf("tab %v cache should be cleared on new TS", tb)
		}
	}
	if !got.pane.detail.cache[tabTerminal].loaded || got.pane.detail.cache[tabTerminal].body != "live terminal" {
		t.Error("tabTerminal cache must be untouched by log invalidation")
	}
	if got.pane.detail.round != 3 {
		t.Errorf("fallback Round-1 when PlanRound is 0: got %d", got.pane.detail.round)
	}
	if !got.pane.detail.lastLogTS.Equal(newTS) {
		t.Errorf("expected lastLogTS to update to %v, got %v", newTS, got.pane.detail.lastLogTS)
	}
	if cmd == nil {
		t.Fatal("expected refetch cmd for active tab after invalidation")
	}
	tMsg, ok := cmd().(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg from refetch cmd, got %T", cmd())
	}
	if tMsg.t != tabReport {
		t.Fatalf("expected refetch of active tab (tabReport), got %v", tMsg.t)
	}

	rep2 := view.Report{Bindings: []view.BindingStatus{
		{Name: name, Round: 4, PlanRound: 4, Display: "ACTIVE", Last: &view.LastEvent{TS: newTS.Add(10 * time.Second), Round: 4}},
	}}
	next2, _ := got.Update(statusMsg{report: rep2}, testEnv(mastermindSource{rt}, rep2, 140, 40))
	got2 := next2.(roundView)
	if got2.pane.detail.round != 4 {
		t.Errorf("expected detail.round to update to 4, got %d", got2.pane.detail.round)
	}
}

func TestScrollPreservedAcrossStatusMsgWithoutInvalidation(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	name := "webshop"
	ts := time.Now().Truncate(time.Second)

	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: name, Round: 2, Display: "ACTIVE", Last: &view.LastEvent{TS: ts, Round: 2}},
	}}
	rv := newTestRound(t, rt, rep, name, 0)
	rv.pane.detail.active = tabReport
	rv.pane.detail.lastLogTS = ts
	rv.pane.detail.vp = viewport.New(80, 20)
	rv.pane.detail.vp.SetContent(strings.Repeat("line\n", 100))
	rv.pane.detail.vp.YOffset = 33

	next, _ := rv.Update(statusMsg{report: rep}, testEnv(mastermindSource{rt}, rep, 140, 40))
	got := next.(roundView)

	if got.pane.detail.vp.YOffset != 33 {
		t.Errorf("expected scroll offset 33 preserved, got %d", got.pane.detail.vp.YOffset)
	}
}

func TestTerminalTabPollsOnEveryTickWhenVisible(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"
	if err := st.Save(newTestBinding(name)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rep := view.Report{Bindings: []view.BindingStatus{{Name: name, Round: 2, Display: "ACTIVE"}}}
	rv := newTestRound(t, rt, rep, name, 0)
	rv.pane.detail.active = tabTerminal
	rv.pane.detail.vp = viewport.New(80, 20)
	rv.pane.tabInFlight = false

	next, cmd := rv.Update(tickMsg(time.Now()), testEnv(mastermindSource{rt}, rep, 140, 40))
	rv = next.(roundView)
	if cmd == nil {
		t.Fatal("terminal tab must issue a fetch on every tick while visible")
	}
	if _, ok := cmd().(tabMsg); !ok {
		t.Error("the tick's command must be the terminal tab's fetch")
	}

	// A cached file-backed tab issues none.
	rv.pane.detail.active = tabReport
	rv.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "report"}
	rv.pane.tabInFlight = false
	_, cmd2 := rv.Update(tickMsg(time.Now()), testEnv(mastermindSource{rt}, rep, 140, 40))
	if cmd2 != nil {
		if _, ok := cmd2().(tabMsg); ok {
			t.Error("file-backed cached tab must NOT issue fetch on tick")
		}
	}
}

func TestStatusMsgBindingVanishesPopsToListWithNote(t *testing.T) {
	m := splitModel(t, 140, 40, view.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"})
	v, _ := newRoundView(m.env(), "webshop", 0)
	m.stack = append(m.stack, v)

	emptyRep := view.Report{Bindings: []view.BindingStatus{{Name: "other-binding", Display: "ACTIVE"}}}
	res, cmd := m.Update(statusMsg{report: emptyRep})
	m = res.(Model)
	m = drain(t, m, cmd)

	if len(m.stack) != 1 {
		t.Fatalf("the round view must pop when its binding vanishes, depth = %d", len(m.stack))
	}
	if !strings.Contains(m.notice, "webshop is gone") {
		t.Fatalf("expected 'webshop is gone' notice, got %q", m.notice)
	}
	if n := strings.Count(m.notice, "is gone"); n != 1 {
		t.Errorf("notice must carry exactly one \"is gone\", got %d: %q", n, m.notice)
	}
	if !strings.Contains(stripANSI(m.keysView(m.env())), "webshop is gone") {
		t.Fatalf("expected the keys row to show the notice, got %q", stripANSI(m.keysView(m.env())))
	}

	// A later statusMsg must NOT clear the notice.
	res, _ = m.Update(statusMsg{report: emptyRep})
	m = res.(Model)
	if !strings.Contains(m.notice, "webshop is gone") {
		t.Fatalf("notice must survive a subsequent statusMsg, got %q", m.notice)
	}

	// A keypress clears it.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = res.(Model)
	if m.notice != "" {
		t.Fatalf("notice must be cleared on keypress, got %q", m.notice)
	}
}
