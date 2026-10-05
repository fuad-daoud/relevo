package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// unresolvableSource resolves no key at all: the way a server source answers a
// key whose owner it does not hold.
type unresolvableSource struct{ Source }

func (unresolvableSource) Runtime(string) (relevo.Runtime, string, bool) {
	return relevo.Runtime{}, "", false
}

// detailRow is the row a single-row fetch brings back: the three figures a
// fleet row leaves nil, all at once.
func detailRow(name string) view.BindingStatus {
	return view.BindingStatus{
		Name: name, Round: 2, Display: "ACTIVE", BuilderKind: "opencode", BuilderStatus: "working",
		Live:      &view.LiveDiff{Files: 3, Added: 7, Removed: 2},
		LiveUsage: &usage.Usage{Harness: "opencode", Tokens: usage.Tokens{In: 120, Out: 30}},
		Headless:  &view.HeadlessInfo{PID: 4242, LogPath: "/logs/builder.log", Tail: []string{"l2", "l3"}},
	}
}

// assertNoDetailFigures fails on a row that carries any of the three.
func assertNoDetailFigures(t *testing.T, where string, row view.BindingStatus) {
	t.Helper()
	if row.Live != nil {
		t.Errorf("%s: Live = %+v, want nil", where, row.Live)
	}
	if row.LiveUsage != nil {
		t.Errorf("%s: LiveUsage = %+v, want nil", where, row.LiveUsage)
	}
	if row.Headless != nil && len(row.Headless.Tail) > 0 {
		t.Errorf("%s: Headless.Tail = %q, want empty", where, row.Headless.Tail)
	}
}

// TestFleetReportCarriesNoDetailFigures: every fleet entry point builds its
// report with the detail figures off, so a refresh pays for no git diff, no
// usage peek and no log tail.
func TestFleetReportCarriesNoDetailFigures(t *testing.T) {
	st := store.New(t.TempDir())
	b := newTestBinding("webshop")
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}

	for _, tc := range []struct {
		name string
		src  Source
	}{
		{"mastermindSource", mastermindSource{rt}},
		{"liveSource", liveSource{newLiveRuntime(rt)}},
	} {
		rep, err := tc.src.Status(context.Background())
		if err != nil {
			t.Fatalf("%s Status: %v", tc.name, err)
		}
		if len(rep.Bindings) != 1 {
			t.Fatalf("%s: rows = %d, want 1", tc.name, len(rep.Bindings))
		}
		assertNoDetailFigures(t, tc.name, rep.Bindings[0])
		// The non-live row is untouched: it is still the row the store holds.
		if rep.Bindings[0].Name != "webshop" || rep.Bindings[0].Round != 2 {
			t.Errorf("%s: row = %+v, want the store's own row", tc.name, rep.Bindings[0])
		}
	}
}

// TestDetailFetchReturnsTheRowsOwnRow: fetchStatusRow resolves the key and
// builds that one binding's row; an unresolvable key is the same prose an
// unknown owner's tab reads.
func TestDetailFetchReturnsTheRowsOwnRow(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	src := mastermindSource{rt}

	msg, ok := fetchStatusRow(context.Background(), src, "webshop")().(detailRowMsg)
	if !ok {
		t.Fatal("fetchStatusRow must answer a detailRowMsg")
	}
	if msg.err != nil || msg.row == nil {
		t.Fatalf("msg = %+v, want a row", msg)
	}
	if msg.key != "webshop" || msg.row.Name != "webshop" {
		t.Errorf("msg = key %q row %q, want the fetched binding", msg.key, msg.row.Name)
	}

	// A source that cannot resolve the key reports it, never a blank row.
	got, ok := fetchStatusRow(context.Background(), unresolvableSource{}, "webshop")().(detailRowMsg)
	if !ok {
		t.Fatal("fetchStatusRow must answer a detailRowMsg")
	}
	if got.err == nil || got.row != nil {
		t.Errorf("an unresolved key = %+v, want an error and no row", got)
	}
	if !strings.Contains(got.err.Error(), "webshop") {
		t.Errorf("err = %q, want it to name the key", got.err)
	}
}

// TestDetailPaneRestoresAllThreeFigures: the pane's own row fetch lands, and the
// round head shows the live usage and the renderers show the diff and the tail
// -- while the fleet row itself still carries none of the three.
func TestDetailPaneRestoresAllThreeFigures(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}

	fleet := view.Report{Bindings: []view.BindingStatus{
		{Name: "webshop", Round: 2, Display: "ACTIVE", BuilderKind: "opencode", BuilderStatus: "working"},
	}}
	assertNoDetailFigures(t, "fleet row", fleet.Bindings[0])

	rv := newTestRound(t, rt, fleet, "webshop", 0)

	// The reply a fetch issues against a runtime with git and a usage reader
	// wired: internal/relevo's TestStatusRowCarriesTheDetailFigures pins that
	// the single-row entry point really does build all three. Here the row the
	// fetch answers with stands in for it.
	next, _ := rv.Update(detailRowMsg{key: "webshop", row: ptrRow(detailRow("webshop"))},
		testEnv(mastermindSource{rt}, fleet, 140, 40))
	rv = next.(roundView)

	b := row(rv.pane.report, "webshop")
	if b == nil {
		t.Fatal("the pane lost the row it is pointed at")
	}
	if b.Live == nil || b.LiveUsage == nil {
		t.Fatalf("Live %+v LiveUsage %+v, want the fetched figures", b.Live, b.LiveUsage)
	}
	if b.Headless == nil || len(b.Headless.Tail) != 2 {
		t.Fatalf("Headless = %+v, want the fetched tail", b.Headless)
	}

	// round_head draws the live usage off the row the pane holds.
	head := stripANSI(rv.pane.tokensLine(b))
	if !strings.Contains(head, "tokens") || strings.Contains(head, "no usage yet") {
		t.Errorf("the tokens line must show the live usage: %q", head)
	}

	// view.RenderStatus draws the diff and the tail off the same row.
	text := view.RenderStatus(view.Report{Bindings: []view.BindingStatus{*b}})
	for _, want := range []string{"+7/-2 in 3", "usage", "  log      l2", "  log      l3"} {
		if !strings.Contains(text, want) {
			t.Errorf("view.RenderStatus lacks %q:\n%s", want, text)
		}
	}

	// And the fleet row the shell still holds is unchanged by all of it.
	assertNoDetailFigures(t, "fleet row after the fetch", fleet.Bindings[0])
}

// TestDetailFetchFailureKeepsTheFleetRow: a failed fetch leaves the fleet's own
// nil figures. There is no placeholder figure, and no stale full row from an
// earlier fetch either.
func TestDetailFetchFailureKeepsTheFleetRow(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	fleet := view.Report{Bindings: []view.BindingStatus{
		{Name: "webshop", Round: 2, Display: "ACTIVE", BuilderKind: "opencode", BuilderStatus: "working"},
	}}

	rv := newTestRound(t, rt, fleet, "webshop", 0)

	// A fetch that worked, then one that failed: the failure must not leave the
	// earlier row painted, and must not invent a figure.
	rv = roundDetailRow(rv, detailRowMsg{key: "webshop", row: ptrRow(detailRow("webshop"))}, rt, fleet)
	if b := row(rv.pane.report, "webshop"); b == nil || b.Live == nil {
		t.Fatalf("the first fetch must land: %+v", b)
	}

	failed := detailRowMsg{key: "webshop", err: context.DeadlineExceeded}
	rv = roundDetailRow(rv, failed, rt, fleet)
	if rv.detailRow != nil || rv.detailKey != "" {
		t.Errorf("a failed fetch must leave no row: key %q row %+v", rv.detailKey, rv.detailRow)
	}
	b := row(rv.pane.report, "webshop")
	if b == nil {
		t.Fatal("the pane lost the fleet row itself")
	}
	assertNoDetailFigures(t, "after a failed fetch", *b)
	if stripANSI(rv.pane.tokensLine(b)) == "" {
		t.Error("the pane must still draw its head from the fleet row")
	}

	// A reply for a row the view has left changes nothing.
	rv = roundDetailRow(rv, detailRowMsg{key: "somewhere-else", row: ptrRow(detailRow("somewhere-else"))}, rt, fleet)
	if rv.detailRow != nil {
		t.Errorf("a stale reply must be dropped, got %+v", rv.detailRow)
	}
}

// roundDetailRow sends one detailRowMsg through the round view's Update.
func roundDetailRow(rv roundView, msg tea.Msg, rt relevo.Runtime, rep view.Report) roundView {
	next, _ := rv.Update(msg, testEnv(mastermindSource{rt}, rep, 140, 40))
	return next.(roundView)
}

func ptrRow(b view.BindingStatus) *view.BindingStatus { return &b }

// TestDetailFetchDoesNotOutliveTheTick: the detail fetch is issued once, on the
// open, so a refresh tick never pays for a second one.
func TestDetailFetchDoesNotOutliveTheTick(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	fleet := view.Report{Bindings: []view.BindingStatus{
		{Name: "webshop", Round: 2, Display: "ACTIVE", BuilderKind: "opencode", BuilderStatus: "working"},
	}}

	rv := newTestRound(t, rt, fleet, "webshop", 0)
	if rv.pane.tabInFlight {
		rv.pane.tabInFlight = false
	}
	_, cmd := rv.Update(tickMsg(railNow.Add(time.Second)), testEnv(mastermindSource{rt}, fleet, 140, 40))
	for _, msg := range batchMsgs(cmd) {
		if _, ok := msg.(detailRowMsg); ok {
			t.Fatal("a tick must not issue a second detail-row fetch")
		}
	}
}
