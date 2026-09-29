package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

func extractBatch(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return []tea.Cmd(batch)
	}
	return []tea.Cmd{cmd}
}

func hasStatusMsg(batch []tea.Cmd) bool {
	// batch[0] is tick. Subsequent commands are fetches.
	for i := 1; i < len(batch); i++ {
		msg := batch[i]()
		if _, ok := msg.(statusMsg); ok {
			return true
		}
	}
	return false
}

func hasTabMsg(batch []tea.Cmd) bool {
	for i := 1; i < len(batch); i++ {
		msg := batch[i]()
		if _, ok := msg.(tabMsg); ok {
			return true
		}
	}
	return false
}

func TestSingleFlightStatusInFlightBlocksSecondFetch(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})

	m.statusInFlight = true

	res, cmd := m.Update(tickMsg(time.Now()))
	updated := res.(Model)

	if !updated.statusInFlight {
		t.Error("expected statusInFlight to remain true")
	}

	batch := extractBatch(cmd)
	if hasStatusMsg(batch) {
		t.Error("tickMsg while statusInFlight must not issue fetchStatus")
	}
}

func TestSingleFlightTabInFlightStillIssuesStatus(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})

	m.statusInFlight = false

	res, cmd := m.Update(tickMsg(time.Now()))
	updated := res.(Model)

	if !updated.statusInFlight {
		t.Error("expected statusInFlight to become true")
	}

	batch := extractBatch(cmd)
	if !hasStatusMsg(batch) {
		t.Fatal("tickMsg while statusInFlight is clear MUST issue fetchStatus")
	}
}

// TestSingleFlightTabInFlightBlocksSecondTabFetch is the round view's own
// fetch guard (the second of the two guards): a tick while tabInFlight is
// set must not issue a second tab fetch.
func TestSingleFlightTabInFlightBlocksSecondTabFetch(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.active = tabTerminal
	rv.pane.tabInFlight = true

	next, cmd := rv.Update(tickMsg(time.Now()), testEnv(mastermindSource{rt}, rv.pane.report, 140, 40))
	_ = next
	if cmd != nil {
		if hasTabMsg(extractBatch(cmd)) {
			t.Error("tickMsg while tabInFlight must not issue second tab fetch")
		}
	}
}

func TestStatusMsgErrorPreservesReport(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})

	initialReport := view.Report{
		Bindings: []view.BindingStatus{
			{Name: "webshop", Round: 2, State: "active", Display: "ACTIVE"},
		},
	}
	m.report = initialReport
	m.statusInFlight = true

	testErr := errors.New("transient harness failure")
	res, _ := m.Update(statusMsg{err: testErr})
	updated := res.(Model)

	if updated.statusInFlight {
		t.Error("statusMsg should clear statusInFlight")
	}
	if updated.err == nil || updated.err.Error() != testErr.Error() {
		t.Errorf("expected err %v, got %v", testErr, updated.err)
	}
	if !reflect.DeepEqual(updated.report, initialReport) {
		t.Errorf("report was modified on error:\ngot:  %+v\nwant: %+v", updated.report, initialReport)
	}
}

func TestStatusMsgSuccessClearsError(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})

	m.err = errors.New("transient error")
	m.statusInFlight = true

	goodReport := view.Report{
		Bindings: []view.BindingStatus{
			{Name: "webshop", Round: 3, State: "active", Display: "ACTIVE"},
		},
	}
	res, _ := m.Update(statusMsg{report: goodReport})
	updated := res.(Model)

	if updated.statusInFlight {
		t.Error("statusMsg should clear statusInFlight")
	}
	if updated.err != nil {
		t.Errorf("expected err to be cleared, got %v", updated.err)
	}
	if len(updated.report.Bindings) != 1 || updated.report.Bindings[0].Name != "webshop" {
		t.Errorf("unexpected report: %+v", updated.report)
	}
}

// TestTabMsgMismatchedBindingDiscarded ports the pane's name check: a reply
// for another binding is dropped, and the reply still ends the fetch.
func TestTabMsgMismatchedBindingDiscarded(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.active = tabReport
	rv.pane.tabInFlight = true

	rv = roundMsg(rv, tabMsg{
		name:  "other-binding",
		round: rv.pane.detail.round,
		t:     tabReport,
		content: tabContent{
			loaded: true,
			body:   "should be ignored",
		},
	})

	if rv.pane.tabInFlight {
		t.Error("tabMsg should clear tabInFlight")
	}
	if rv.pane.detail.cache[tabReport].loaded {
		t.Error("cache should not be populated with mismatched binding response")
	}
}

func TestWindowSizeMsgSetsReady(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})

	m.ready = false
	res, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	updated := res.(Model)

	if !updated.ready {
		t.Error("WindowSizeMsg should set ready to true")
	}
	if updated.width != 100 || updated.height != 40 {
		t.Errorf("expected 100x40, got %dx%d", updated.width, updated.height)
	}

	// A round view resizes its viewport from the same message (R2.4).
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)
	next, _ := rv.Update(tea.WindowSizeMsg{Width: 100, Height: 40}, Env{Width: 100, Height: 40, Now: railNow, Report: rv.pane.report})
	got := next.(roundView)
	if got.pane.width != 100 {
		t.Errorf("round view width after resize = %d, want 100", got.pane.width)
	}
	if want := bodyHeight(Env{Height: 40}); got.pane.rows != want {
		t.Errorf("round view rows = %d, want %d", got.pane.rows, want)
	}
	if got.pane.detail.vp.Width != got.pane.contentWidth() {
		t.Errorf("expected vp.Width %d (contentWidth), got %d", got.pane.contentWidth(), got.pane.detail.vp.Width)
	}
}

func TestRowHelper(t *testing.T) {
	rep := view.Report{
		Bindings: []view.BindingStatus{
			{Name: "a", Round: 1},
			{Name: "b", Round: 2},
		},
	}

	ra := row(rep, "a")
	if ra == nil || ra.Name != "a" || ra.Round != 1 {
		t.Errorf("expected row 'a', got %+v", ra)
	}

	rc := row(rep, "c")
	if rc != nil {
		t.Errorf("expected nil for 'c', got %+v", rc)
	}
}

func TestStaleRoundReplyDiscardedForDiff(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 5, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.round = 4
	rv.pane.detail.active = tabDiff

	rv = roundMsg(rv, tabMsg{
		name:  "webshop",
		round: 3, // stale round
		t:     tabDiff,
		content: tabContent{
			loaded: true,
			body:   "old diff content",
		},
	})

	if rv.pane.detail.cache[tabDiff].loaded {
		t.Error("stale diff tabMsg must be discarded and not update cache")
	}
}

// TestStaleRoundReplyDiscardedForReport pins #183's generalisation: report
// is round-scoped exactly like diff, so a reply for a round that is no
// longer on screen must be discarded.
func TestStaleRoundReplyDiscardedForReport(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 5, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.round = 4
	rv.pane.detail.active = tabReport

	rv = roundMsg(rv, tabMsg{
		name:  "webshop",
		round: 3, // stale round
		t:     tabReport,
		content: tabContent{
			loaded: true,
			body:   "stale report content",
		},
	})

	if rv.pane.detail.cache[tabReport].loaded {
		t.Error("stale report tabMsg must be discarded and not update cache")
	}
}

func TestMaybeInvalidateBlockedWhenTabInFlight(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	name := "webshop"
	ts := time.Now()

	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: name, Round: 3, Last: &view.LastEvent{TS: ts.Add(5 * time.Second), Round: 3}},
	}}
	rv := newTestRound(t, rt, rep, name, 0)
	rv.pane.detail.active = tabReport
	rv.pane.detail.lastLogTS = ts
	rv.pane.tabInFlight = true

	next, cmd := rv.Update(statusMsg{report: rep}, testEnv(mastermindSource{rt}, rep, 140, 40))
	got := next.(roundView)
	if cmd != nil {
		t.Error("maybeInvalidate must issue no fetch while tabInFlight is set")
	}
	if !got.pane.tabInFlight {
		t.Error("tabInFlight must remain true")
	}
}

// TestRoundPaneIssuesOneFetchAtATime ports TestEnterPressedTwiceIssuesOneFetch
// to the pane's own guard: while a tab fetch is in flight the view issues no
// second one.
func TestRoundPaneIssuesOneFetchAtATime(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.tabInFlight = false

	_, cmd1 := rv.Update(tickMsg(time.Now()), testEnv(mastermindSource{rt}, rv.pane.report, 140, 40))
	if cmd1 == nil {
		t.Fatal("first tick must return non-nil cmd")
	}
	// The tick set tabInFlight; a second tick must issue nothing.
	rv.pane.tabInFlight = true
	_, cmd2 := rv.Update(tickMsg(time.Now()), testEnv(mastermindSource{rt}, rv.pane.report, 140, 40))
	if cmd2 != nil {
		t.Fatal("second tick while tabInFlight is set must return nil cmd")
	}
}

func TestTickBeforeFirstStatusIssuesNoSecondFetch(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})

	if !m.statusInFlight {
		t.Fatal("newModel must initialize statusInFlight = true to guard the Init fetch")
	}

	_, cmd := m.Update(tickMsg(time.Now()))
	batch := extractBatch(cmd)
	if hasStatusMsg(batch) {
		t.Fatal("tickMsg arriving before first statusMsg must not issue second status fetch")
	}
}

// TestLoadingThenEmptyFleet ports TestEmptyIsFalseBeforeLoad: before the
// first status the fleet body is loading prose, and an empty report reads
// as "no bindings".
func TestLoadingThenEmptyFleet(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Millisecond})
	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = res.(Model)

	if m.statusLoaded {
		t.Fatal("newModel must start with statusLoaded = false")
	}
	if !strings.Contains(m.View(), "loading…") {
		t.Fatalf("before the first status the fleet must read loading…:\n%s", m.View())
	}

	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: view.Report{}})
	loaded := res.(Model)
	if !loaded.statusLoaded {
		t.Fatal("statusMsg must set statusLoaded = true")
	}
	if !strings.Contains(loaded.View(), "no bindings") {
		t.Fatalf("an empty report must read no bindings:\n%s", loaded.View())
	}
}

// TestEmptyFleetEnterDoesNothing ports the surviving empty-fleet key rule:
// enter on no rows is a no-op.
func TestEmptyFleetEnterDoesNothing(t *testing.T) {
	m := splitModel(t, 140, 40)
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("enter on an empty fleet must return nil cmd, got %v", cmd)
	}
	if len(res.(Model).stack) != 1 {
		t.Errorf("enter on an empty fleet must not push a view: depth %d", len(res.(Model).stack))
	}
}

// TestStepRoundBackRefetchesEveryTab ports #183's round stepping: "[" and
// "]" move detail.round within [1, detail.rounds], clamping at either edge,
// and invalidate every tab's cache.
func TestStepRoundBackRefetchesEveryTab(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	b.Round = 3
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: name, Round: 3, Display: "ACTIVE"}}}, name, 0)

	if rv.pane.detail.round != 2 || rv.pane.detail.rounds != 3 {
		t.Fatalf("after pointing: round=%d rounds=%d, want round=2 rounds=3", rv.pane.detail.round, rv.pane.detail.rounds)
	}

	for tb := tab(0); tb < tabCount; tb++ {
		rv.pane.detail.cache[tb] = tabContent{loaded: true, body: "stale"}
	}
	rv.pane.tabInFlight = false

	for i := 0; i < 2; i++ {
		rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
		rv.pane.tabInFlight = false
	}
	if rv.pane.detail.round != 1 {
		t.Fatalf("after \"[\" twice: round = %d, want 1", rv.pane.detail.round)
	}
	for tb := tab(0); tb < tabCount; tb++ {
		if rv.pane.detail.cache[tb].loaded {
			t.Errorf("tab %v cache still loaded after stepping back", tb)
		}
	}

	for i := 0; i < 3; i++ {
		rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
		rv.pane.tabInFlight = false
	}
	if rv.pane.detail.round != 3 {
		t.Fatalf("after \"]\" three times: round = %d, want 3 (the open round)", rv.pane.detail.round)
	}

	reportMsg := fetchReport(context.Background(), mastermindSource{rt}, name, rv.pane.detail.round)().(tabMsg)
	if want := "round 3 is open; report arrives when it closes"; reportMsg.content.empty != want {
		t.Errorf("report empty = %q, want %q", reportMsg.content.empty, want)
	}
	diffMsg := fetchDiff(context.Background(), mastermindSource{rt}, name, rv.pane.detail.round)().(tabMsg)
	if want := "diff is captured when round 3 closes"; diffMsg.content.empty != want {
		t.Errorf("diff empty = %q, want %q", diffMsg.content.empty, want)
	}
}

// TestStepRoundEdgesNoop ports #183's clamp: stepping past either edge of
// [1, detail.rounds] changes nothing -- not the round, not the cache.
func TestStepRoundEdgesNoop(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	b := newTestBinding("webshop")
	b.Round = 3
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 3, Display: "ACTIVE"}}}, "webshop", 0)
	rv.pane.detail.round = 1
	rv.pane.detail.rounds = 3
	rv.pane.detail.active = tabReport
	rv.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "round 1"}

	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	if rv.pane.detail.round != 1 {
		t.Errorf("\"[\" at round 1: round = %d, want 1 (no-op)", rv.pane.detail.round)
	}
	if !rv.pane.detail.cache[tabReport].loaded {
		t.Error("\"[\" at round 1: cache must stay untouched (no-op)")
	}

	rv.pane.detail.round = 3
	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if rv.pane.detail.round != 3 {
		t.Errorf("\"]\" at round 3: round = %d, want 3 (no-op)", rv.pane.detail.round)
	}
	if !rv.pane.detail.cache[tabReport].loaded {
		t.Error("\"]\" at round 3: cache must stay untouched (no-op)")
	}
}

// TestPointDetailAtMarksViewed ports #143: opening a live binding's round
// view stamps its viewed mark through the Source.
func TestPointDetailAtMarksViewed(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(store.Binding{Name: "webshop", CWD: "/repo", Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}

	if _, ok := st.ViewedAt("webshop"); ok {
		t.Fatal("ViewedAt before the round view: got ok true, want false")
	}

	rv := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{{Name: "webshop", Round: 2, Display: "ACTIVE"}}}, "webshop", 0)

	if _, ok := st.ViewedAt("webshop"); !ok {
		t.Fatal("opening the round view did not stamp .viewed through the Source")
	}
	if rv.pane.detail.name != "webshop" {
		t.Errorf("the pane must be pointed at the binding: detail.name = %q", rv.pane.detail.name)
	}
}

func TestPaneRound(t *testing.T) {
	tests := []struct {
		r    view.BindingStatus
		want int
	}{
		{r: view.BindingStatus{Round: 1, PlanRound: 1}, want: 1},
		{r: view.BindingStatus{Round: 5, PlanRound: 5}, want: 5},
		{r: view.BindingStatus{Round: 5, PlanRound: 4}, want: 4},
		{r: view.BindingStatus{Round: 1, PlanRound: 0}, want: 0},
		{r: view.BindingStatus{Round: 3, PlanRound: 0}, want: 2},
	}
	for _, tc := range tests {
		if got := paneRound(tc.r); got != tc.want {
			t.Errorf("paneRound(%+v) = %d, want %d", tc.r, got, tc.want)
		}
	}
}

func TestPointDetailAtOpensOnPlanRound(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(store.Binding{Name: "inflight", CWD: "/repo/inflight", Round: 1, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := st.Save(store.Binding{Name: "idle", CWD: "/repo/idle", Round: 3, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}

	rvInflight := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{
		{Name: "inflight", Round: 1, PlanRound: 1, Display: "ACTIVE"},
	}}, "inflight", 0)
	if rvInflight.pane.detail.round != 1 {
		t.Errorf("inflight detail.round = %d, want 1", rvInflight.pane.detail.round)
	}
	if rvInflight.pane.detail.rounds != 1 {
		t.Errorf("inflight detail.rounds = %d, want 1", rvInflight.pane.detail.rounds)
	}

	rvIdle := newTestRound(t, rt, view.Report{Bindings: []view.BindingStatus{
		{Name: "idle", Round: 3, PlanRound: 2, Display: "ACTIVE"},
	}}, "idle", 0)
	if rvIdle.pane.detail.round != 2 {
		t.Errorf("idle detail.round = %d, want 2", rvIdle.pane.detail.round)
	}
	if rvIdle.pane.detail.rounds != 2 {
		t.Errorf("idle detail.rounds = %d, want 2 (§2.2)", rvIdle.pane.detail.rounds)
	}
}
