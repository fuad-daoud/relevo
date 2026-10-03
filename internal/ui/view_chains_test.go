package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

func TestChainsViewSortsAndNests(t *testing.T) {
	fa := &fakeActions{
		chainsDoc: relevo.ChainsDoc{
			Chains: []relevo.ChainEntry{
				{Name: "beta-running", Status: "running", Step: "build", PlanPos: 1, PlanTotal: 1},
				{Name: "gamma-parent", Status: "running", Step: "build", PlanPos: 1, PlanTotal: 1, Depth: 0},
				{Name: "gamma-child", Parent: "gamma-parent", Status: "running", Step: "build", PlanPos: 1, PlanTotal: 1, Depth: 1},
				{Name: "alpha-done", Status: "done", Step: "finish", PlanPos: 1, PlanTotal: 1, Depth: 0},
			},
		},
	}
	m := goldenActionModel(t, 132, 34, fa, view.Report{})
	m = drain(t, m, execLine("chains", m.env(), m.prefs))
	body := stripANSI(m.View())

	betaIdx := strings.Index(body, "beta-running")
	alphaIdx := strings.Index(body, "alpha-done")
	gammaParentIdx := strings.Index(body, "gamma-parent")
	gammaChildIdx := strings.Index(body, "  gamma-child")

	if betaIdx == -1 || alphaIdx == -1 || gammaParentIdx == -1 || gammaChildIdx == -1 {
		t.Fatalf("missing expected chain rows in body:\n%s", body)
	}

	if betaIdx > alphaIdx {
		t.Errorf("beta-running (index %d) must appear before alpha-done (index %d)", betaIdx, alphaIdx)
	}
	if gammaParentIdx > gammaChildIdx {
		t.Errorf("gamma-parent (index %d) must appear before nested gamma-child (index %d)", gammaParentIdx, gammaChildIdx)
	}
}

func TestChainsViewWithoutActionsRefused(t *testing.T) {
	env := Env{Actions: nil}
	cmd := execLine("chains", env, prefs{})
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("chains without Actions must return noticeMsg, got %T", cmd())
	}
	if msg.text != "the config views need relevo ui on this machine" {
		t.Errorf("notice = %q, want %q", msg.text, "the config views need relevo ui on this machine")
	}
}

func TestChainsViewServerChainShowsStale(t *testing.T) {
	fa := &fakeActions{
		chainsDoc: relevo.ChainsDoc{
			Chains: []relevo.ChainEntry{
				{
					Name:      "remote-chain",
					Status:    "running",
					Where:     "server us-east",
					Stale:     "connection refused",
					PlanPos:   1,
					PlanTotal: 1,
				},
			},
		},
	}
	m := goldenActionModel(t, 132, 34, fa, view.Report{})
	m = drain(t, m, execLine("chains", m.env(), m.prefs))
	body := stripANSI(m.View())
	if !strings.Contains(body, "stale") {
		t.Errorf("chains view for server chain with Stale set must show stale marker, got:\n%s", body)
	}
}

// chainsModelForTest is the shell with the read model loaded and the chains
// steps view on top, so a key test starts from a chain's own step list.
func chainsModelForTest(t *testing.T, st *store.Store, doc relevo.ChainsDoc, chain string) Model {
	t.Helper()
	fa := &fakeActions{chainsDoc: doc}
	m := goldenActionModelWithStore(t, 132, 34, fa, view.Report{}, st)
	m = drain(t, m, execLine("chains", m.env(), m.prefs))
	return chainsStepsModelOn(t, m, chain)
}

func TestChainDrillOpensStepRound(t *testing.T) {
	st := store.New(t.TempDir())
	seedPlanFixture(t, st, "feature-auth", 2, railNow.Add(-6*time.Minute), "# Round 2 plan\n")
	// The member's own row stands on a later round than the step closed on,
	// so opening round 0 (the row's default) cannot pass for opening the
	// step's round.
	doc := chainsFixtureDoc()
	for i := range doc.Chains {
		if doc.Chains[i].Name != "feature-auth" {
			continue
		}
		for j := range doc.Chains[i].Members {
			if doc.Chains[i].Members[j].Name == "feature-auth" {
				doc.Chains[i].Members[j].Round = 7
				doc.Chains[i].Members[j].PlanRound = 7
			}
		}
	}
	m := chainsModelForTest(t, st, doc, "feature-auth")
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)

	rv, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("enter on a step did not push a round view: %T", m.top())
	}
	if rv.pane.detail.name != "feature-auth" {
		t.Errorf("drill opened %q, want the step's member feature-auth", rv.pane.detail.name)
	}
	if rv.pane.detail.round != 2 {
		t.Errorf("drill opened round %d, want the step's round 2", rv.pane.detail.round)
	}
	if got := rv.Crumbs(); len(got) != 1 || got[0] != "build" {
		t.Errorf("crumbs = %v, want the step id alone (the chain is already a crumb)", got)
	}
}

func TestChainDrillSurvivesStatusRefresh(t *testing.T) {
	st := store.New(t.TempDir())
	seedPlanFixture(t, st, "feature-auth", 2, railNow.Add(-6*time.Minute), "# Round 2 plan\n")
	m := chainsModelForTest(t, st, chainsFixtureDoc(), "feature-auth")
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)

	// A status refresh whose report does not carry the member row at all: the
	// cockpit folds a chain's members into its own row, so the shell's report
	// never has them.
	res, cmd = m.Update(statusMsg{report: view.Report{Bindings: []view.BindingStatus{{Name: "some-other-binding", Round: 1}}}})
	m = drain(t, res.(Model), cmd)

	rv, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("a status refresh replaced the round view: %T", m.top())
	}
	if row(rv.pane.report, "feature-auth") == nil {
		t.Fatalf("the member row vanished after a status refresh: %d rows", len(rv.pane.report.Bindings))
	}
	left, _ := rv.Context(m.env())
	if !strings.Contains(stripANSI(left), "feature-auth") {
		t.Errorf("the context row no longer names the member: %q", stripANSI(left))
	}
}

func TestChainDrillNoRoundYetNotices(t *testing.T) {
	st := store.New(t.TempDir())
	m := chainsModelForTest(t, st, chainsFixtureDoc(), "feature-auth")
	sv := m.top().(chainStepsView)
	sv.cur = 3 // the repair step, which never ran
	m.stack[len(m.stack)-1] = sv

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("enter on a step that never ran returned %T, want a notice", cmd())
	}
	if msg.text != "no round yet: repair has not run" {
		t.Errorf("notice = %q, want the no-round-yet notice", msg.text)
	}
	if len(m.stack) != 2 {
		t.Errorf("stack depth = %d, want the steps view still on top", len(m.stack))
	}
}

func TestChainEscReturnsToChain(t *testing.T) {
	st := store.New(t.TempDir())
	m := chainsModelForTest(t, st, chainsFixtureDoc(), "feature-auth")
	if _, ok := m.top().(chainStepsView); !ok {
		t.Fatalf("setup did not reach the steps view: %T", m.top())
	}
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(chainsView); !ok {
		t.Fatalf("esc from the steps view left %T on top, want the chains list", m.top())
	}
	if len(m.stack) != 1 {
		t.Errorf("stack depth = %d, want esc to pop exactly one level", len(m.stack))
	}
}

// TestChainTraceFromStepsLoads pins the steps view's own t: the trace it
// pushes must arrive already read, or the view sits on "loading…" forever
// because nothing else fills it. The chains list's t already dispatches the
// read, so the golden for the trace never exercises this second entry point.
func TestChainTraceFromStepsLoads(t *testing.T) {
	fa := &fakeActions{
		chainsDoc:   chainsFixtureDoc(),
		chainTraces: map[string]relevo.ChainTraceDoc{"feature-auth": chainTraceFixture()},
	}
	m := chainsStepsModel(t, 132, 34, fa, "feature-auth")
	if _, ok := m.top().(chainStepsView); !ok {
		t.Fatalf("setup did not reach the steps view: %T", m.top())
	}
	res, cmd := m.Update(key('t'))
	m = drain(t, res.(Model), cmd)

	tv, ok := m.top().(chainTraceView)
	if !ok {
		t.Fatalf("t from the steps view left %T on top, want the trace view", m.top())
	}
	if !tv.loaded {
		t.Fatalf("trace view opened unloaded: the steps view's t pushed it without the read that fills it")
	}
	if len(tv.lines) == 0 {
		t.Fatalf("trace view loaded with no lines: the read never landed")
	}
	if body := stripANSI(tv.Body(m.env(), 132, 20)); strings.Contains(body, "loading") {
		t.Errorf("trace body still reads loading after the read landed:\n%s", body)
	}
}

// longAgeDoc is a read model whose ages are the hour-and-minute form
// view.AgeText renders at seven and six cells -- "124h 5m", "14h 5m" -- the
// shapes a chain that has run for days reaches. The fixed six-cell AGE column
// the table carried cut the first into "124h 5…", so at 100 cells both must
// show whole.
func longAgeDoc() relevo.ChainsDoc {
	return relevo.ChainsDoc{
		Chains: []relevo.ChainEntry{
			{Name: "long-running-job", Status: "running", Step: "building", PlanPos: 1, PlanTotal: 2, Elapsed: 124*time.Hour + 5*time.Minute, Where: "server zen"},
			{Name: "another-long-one", Status: "running", Step: "review", PlanPos: 2, PlanTotal: 2, Elapsed: 14*time.Hour + 5*time.Minute, Where: "server contabo"},
		},
	}
}

// TestChainsViewShowsLongAgesWholeAt100 pins the AGE column's width: an age the
// human reads is worth more than a WHERE column, so at 100 cells every age and
// every placement shows whole.
// Mutation: restore the fixed six-cell AGE width in chainsLayout, or restore the
// greedy remainder NAME that starves the columns after it.
func TestChainsViewShowsLongAgesWholeAt100(t *testing.T) {
	m := goldenActionModel(t, 100, 30, &fakeActions{chainsDoc: longAgeDoc()}, view.Report{})
	m = drain(t, m, execLine("chains", m.env(), m.prefs))
	body := stripANSI(m.View())

	if !strings.Contains(body, "124h 5m") {
		t.Errorf("the seven-cell age was cut at width 100:\n%s", body)
	}
	if !strings.Contains(body, "14h 5m") {
		t.Errorf("the six-cell age was cut at width 100:\n%s", body)
	}
	for _, want := range []string{"server zen", "server contabo"} {
		if !strings.Contains(body, want) {
			t.Errorf("WHERE lost %q at width 100:\n%s", want, body)
		}
	}
	if strings.Contains(body, "…") {
		t.Errorf("a chains cell was ellipsised at width 100:\n%s", body)
	}
}

// TestChainsViewNameIsNotTheGreedyRemainder pins NAME's width: it is the longest
// name the read model holds, not the width the other columns happen to leave.
// A greedy NAME pushes WHERE off the row entirely at 100.
// Mutation: make chainsLayout give NAME the columns' leftover width again.
func TestChainsViewNameIsNotTheGreedyRemainder(t *testing.T) {
	m := goldenActionModel(t, 100, 30, &fakeActions{chainsDoc: longAgeDoc()}, view.Report{})
	m = drain(t, m, execLine("chains", m.env(), m.prefs))
	nameW, cols := chainsLayout(100, longAgeDoc())

	if nameW < lipgloss.Width("another-long-one") {
		t.Errorf("NAME width = %d, want at least the longest name (%d)", nameW, lipgloss.Width("another-long-one"))
	}
	if nameW > chainsNameCap {
		t.Errorf("NAME width = %d, want at most the cap %d", nameW, chainsNameCap)
	}
	if !chainColsHave(cols, "where") {
		t.Errorf("WHERE dropped at width 100 with %d cells for the columns: %+v", nameW, cols)
	}
	_ = m
}

// TestChainStepsViewGivesSlackToLastOutcome pins the steps table the same way:
// STEP is the longest step id and LAST OUTCOME takes the rest, so a full outcome
// shows at 100.
// Mutation: make chainStepsLayout return the remainder as STEP's width.
func TestChainStepsViewGivesSlackToLastOutcome(t *testing.T) {
	m := chainsStepsModel(t, 100, 30, &fakeActions{chainsDoc: chainsFixtureDoc()}, "feature-auth")
	stepW, cols := chainStepsLayout(100, m.top().(chainStepsView).doc.Chains[2])

	if stepW < lipgloss.Width("review") {
		t.Errorf("STEP width = %d, want at least the longest step id (%d)", stepW, lipgloss.Width("review"))
	}
	outcome, ok := chainColBy(cols, "outcome")
	if !ok {
		t.Fatalf("LAST OUTCOME dropped at width 100: %+v", cols)
	}
	if outcome < lipgloss.Width("review r1 · verdict=changes") {
		t.Errorf("LAST OUTCOME width = %d, want the longest outcome (%d)", outcome, lipgloss.Width("review r1 · verdict=changes"))
	}
	body := stripANSI(m.View())
	if !strings.Contains(body, "review r1 · verdict=changes") {
		t.Errorf("a step outcome was cut at width 100:\n%s", body)
	}
}

// chainColBy reads a laid-out column's width by key.
func chainColBy(cols []chainCol, key string) (int, bool) {
	for _, c := range cols {
		if c.key == key {
			return c.w, true
		}
	}
	return 0, false
}

// chainColsHave reports whether a laid-out table shows the named column.
func chainColsHave(cols []chainCol, key string) bool {
	_, ok := chainColBy(cols, key)
	return ok
}

// TestChainTraceCrumbsNameTheChainOnce pins the breadcrumb from both entry
// points: the stack contributes chains, then the chain, then the trace. Pushed
// from the step list, the trace's own segment is trace alone, because the step
// list already contributes the chain's name.
// Mutation: restore the unconditional two-segment [name, trace] Crumbs, or drop
// the caller's crumbs in the steps view's t.
func TestChainTraceCrumbsNameTheChainOnce(t *testing.T) {
	fa := &fakeActions{
		chainsDoc:   chainsFixtureDoc(),
		chainTraces: map[string]relevo.ChainTraceDoc{"feature-auth": chainTraceFixture()},
	}

	// From the chains list: chains, then the chain and its trace.
	m := chainTraceModel(t, 132, 34)
	assertTraceHeader(t, m, "chains › feature-auth › trace")

	// From the step list: chains, then the chain (the step list's own crumb),
	// then the trace.
	m = chainsStepsModel(t, 132, 34, fa, "feature-auth")
	res, cmd := m.Update(key('t'))
	m = drain(t, res.(Model), cmd)
	assertTraceHeader(t, m, "chains › feature-auth › trace")
}

// assertTraceHeader fails when the rendered breadcrumb does not read want: the
// chain named twice is the defect, so the whole trail is compared.
func assertTraceHeader(t *testing.T, m Model, want string) {
	t.Helper()
	// The header draws each crumb and its separator through lipgloss, so the
	// line is read with the runs stripped and the spacing collapsed before the
	// segments are compared.
	got := strings.Join(strings.Fields(stripANSI(strings.Split(stripANSI(m.View()), "\n")[0])), " ")
	if !strings.Contains(got, want) {
		t.Errorf("breadcrumb line = %q, want it to read %q", got, want)
	}
	if n := strings.Count(got, "feature-auth"); n != 1 {
		t.Errorf("the chain is named %d times in %q, want once", n, got)
	}
}

// assertNoControlBytes fails when rendered carries a control byte sanitize.Text
// is supposed to have replaced. lipgloss emits SGR runs of its own, so the
// check is for the raw C0 bytes rather than for ESC, and the replacement rune
// is checked separately so dropping the text cannot pass for making it inert.
func assertNoControlBytes(t *testing.T, what, rendered string) {
	t.Helper()
	for _, r := range []rune{'\r', '\a', '\x1b', '\x0b', '\x0c'} {
		if strings.ContainsRune(rendered, r) {
			t.Errorf("%s still carries %q:\n%q", what, r, rendered)
		}
	}
}

// TestChainsViewSanitizesPeerFields pins Finding 1: a peer server's own
// status, step and halt reason reach the chains table and its reason line, so
// a status word the engine does not recognise (and therefore falls through the
// chainStatusText switch) must render inert like any other.
// Mutation: drop the three sanitizeText calls in view_chains.go.
func TestChainsViewSanitizesPeerFields(t *testing.T) {
	fa := &fakeActions{
		chainsDoc: relevo.ChainsDoc{
			Chains: []relevo.ChainEntry{
				{
					// A status word outside the four the switch knows, so the
					// default branch draws it rather than a fixed word.
					Name:      "hostile-chain",
					Status:    "run\ning\x1b[2J",
					Step:      "build\rstep",
					Reason:    "reviewer said nobell",
					PlanPos:   1,
					PlanTotal: 1,
					Where:     "server us-east",
				},
			},
		},
	}
	m := goldenActionModel(t, 132, 34, fa, view.Report{})
	m = drain(t, m, execLine("chains", m.env(), m.prefs))
	body := stripANSI(m.View())

	if !strings.Contains(body, "hostile-chain") {
		t.Fatalf("the hostile chain is not on screen, so nothing was pinned:\n%s", body)
	}
	assertNoControlBytes(t, "the chains view", body)
}

// TestChainStepsViewSanitizesPeerFields pins the rest of Finding 1: a chain's
// steps, their actor and outcome text and the steps context row are peer- and
// model-influenced too.
// Mutation: drop the sanitizeText calls in view_chain_steps.go.
func TestChainStepsViewSanitizesPeerFields(t *testing.T) {
	doc := chainsFixtureDoc()
	for i := range doc.Chains {
		if doc.Chains[i].Name != "feature-auth" {
			continue
		}
		c := &doc.Chains[i]
		c.Step = "review\rstep"
		c.Reason = "haltedbell"
		for j := range c.Steps {
			s := &c.Steps[j]
			s.ID = "build\x1b[2J"
			s.Kind = "run\ving"
			s.Actor = "builder\ractor"
			s.LastOutcome = "done\x0bout"
		}
	}
	m := chainsStepsModel(t, 132, 34, &fakeActions{chainsDoc: doc}, "feature-auth")
	body := stripANSI(m.View())
	assertNoControlBytes(t, "the chain steps view", body)

	left, right := m.top().(chainStepsView).Context(m.env())
	for _, part := range []string{stripANSI(left), stripANSI(right)} {
		assertNoControlBytes(t, "the chain steps context row", part)
	}
}

// TestChainTraceErrorBodySanitizes pins Finding 2: the trace body drew the raw
// error while its context row sanitized the same error two lines above it.
// Mutation: draw v.err.Error() raw in chainTraceView.Body.
func TestChainTraceErrorBodySanitizes(t *testing.T) {
	tv := chainTraceView{err: errors.New("pull failed: 502\r\a\033[2J")}
	body := stripANSI(tv.Body(Env{}, 132, 20))

	if !strings.Contains(body, "pull failed") {
		t.Fatalf("the error body is not on screen, so nothing was pinned:\n%q", body)
	}
	assertNoControlBytes(t, "the chain trace error body", body)
}

// TestChainEscFromDrillStopsAtSteps pins the drill's own esc: one level back
// to the step list, never past it to the chains list.
func TestChainEscFromDrillStopsAtSteps(t *testing.T) {
	st := store.New(t.TempDir())
	seedPlanFixture(t, st, "feature-auth", 2, railNow.Add(-6*time.Minute), "# Round 2 plan\n")
	m := chainsModelForTest(t, st, chainsFixtureDoc(), "feature-auth")
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(roundView); !ok {
		t.Fatalf("the drill did not open: %T", m.top())
	}
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(chainStepsView); !ok {
		t.Fatalf("esc from the drill left %T on top, want the step list", m.top())
	}
}
