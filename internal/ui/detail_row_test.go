package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/git"
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

// figureGit is the one git call a live diff makes: a stat with a shape, for any
// dir. The embedded interface is nil on purpose -- liveStat only ever reaches
// DiffWorktreeStat.
type figureGit struct{ relevo.Git }

func (figureGit) DiffWorktreeStat(context.Context, string, string) (git.Stat, error) {
	return git.Stat{FilesChanged: 3, Insertions: 7, Deletions: 2}, nil
}

// figureUsage is the one usage read a live row makes: one sample to fold.
type figureUsage struct{ usage.Reader }

func (figureUsage) Peek(context.Context, usage.Source) ([]usage.Sample, string) {
	return []usage.Sample{{Tokens: usage.Tokens{In: 11, Out: 5}}}, ""
}

// figureBinding is a headless binding whose row carries all three detail
// figures: a round is open with a baseline to diff, a builder kind to price,
// and a log to tail. It is the fixture the fleet gate's test needs: without
// figures to lose, the gate would be invisible and a fleet row's nils would
// prove nothing.
func figureBinding(t *testing.T, st *store.Store, name string) store.Binding {
	t.Helper()
	b := newTestBinding(name)
	b.Builder.Mode = store.ModeHeadless
	b.Builder.Kind = "opencode"
	b.RoundStartedAt = railNow.Add(-time.Minute)
	b.RoundBaselineTree = "baseline-" + name
	b.CWD = t.TempDir()
	b.Builder.LogPath = st.BuilderLogPath(name, b.Round)
	if err := os.MkdirAll(filepath.Dir(b.Builder.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Builder.LogPath, []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

// assertHasDetailFigures is assertNoDetailFigures' counterpart: it pins that the
// fixture really does carry all three, so a fleet row's nils are the gate's
// doing and not the fixture's emptiness.
func assertHasDetailFigures(t *testing.T, where string, row view.BindingStatus) {
	t.Helper()
	if row.Live == nil {
		t.Errorf("%s: Live = nil, want the fixture's diff", where)
	}
	if row.LiveUsage == nil {
		t.Errorf("%s: LiveUsage = nil, want the fixture's figure", where)
	}
	if row.Headless == nil || len(row.Headless.Tail) == 0 {
		t.Errorf("%s: Headless = %+v, want the fixture's tail", where, row.Headless)
	}
}

// TestFleetReportCarriesNoDetailFigures: every fleet entry point builds its
// report with the detail figures off, so a refresh pays for no git diff, no
// usage peek and no log tail. The same runtime with the figures on really does
// carry all three, so the fleet's nils are the gate's doing, not the fixture's.
func TestFleetReportCarriesNoDetailFigures(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(figureBinding(t, st, "fleet-figures")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st, Git: figureGit{}, Usage: figureUsage{}}

	on, err := relevo.Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status with detail on: %v", err)
	}
	if len(on.Bindings) != 1 {
		t.Fatalf("detail-on rows = %d, want 1", len(on.Bindings))
	}
	assertHasDetailFigures(t, "detail on", on.Bindings[0])

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
		if rep.Bindings[0].Name != "fleet-figures" || rep.Bindings[0].Round != 2 {
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

// TestDetailPaneRestoresAllThreeFigures: the pane's own row fetch -- the real
// fetchStatusRow over relevo.StatusRow -- lands with the figures the fleet gate
// dropped, and the round head shows the live usage while the renderers show the
// diff and the tail -- while the fleet row itself still carries none of the
// three.
func TestDetailPaneRestoresAllThreeFigures(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(figureBinding(t, st, "pane-figures")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st, Git: figureGit{}, Usage: figureUsage{}}
	src := mastermindSource{rt}

	fleet := view.Report{Bindings: []view.BindingStatus{
		{Name: "pane-figures", Round: 2, Display: "ACTIVE", BuilderKind: "opencode", BuilderStatus: "working"},
	}}
	assertNoDetailFigures(t, "fleet row", fleet.Bindings[0])

	rv := newTestRound(t, rt, fleet, "pane-figures", 0)

	// The fetch the pane issues for itself, driven for real: mutation "StatusRow
	// builds its row with the option off as well" must break this test, so the
	// reply is the fetch's own, not a hand-built stand-in.
	msg, ok := fetchStatusRow(context.Background(), src, "pane-figures")().(detailRowMsg)
	if !ok {
		t.Fatal("fetchStatusRow must answer a detailRowMsg")
	}
	if msg.err != nil || msg.row == nil {
		t.Fatalf("msg = %+v, want a full-detail row", msg)
	}
	assertHasDetailFigures(t, "the fetched row", *msg.row)

	next, _ := rv.Update(msg, testEnv(src, fleet, 140, 40))
	rv = next.(roundView)

	b := row(rv.pane.report, "pane-figures")
	if b == nil {
		t.Fatal("the pane lost the row it is pointed at")
	}
	if b.Live == nil || b.LiveUsage == nil {
		t.Fatalf("Live %+v LiveUsage %+v, want the fetched figures", b.Live, b.LiveUsage)
	}
	if b.Headless == nil || len(b.Headless.Tail) != 3 {
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
