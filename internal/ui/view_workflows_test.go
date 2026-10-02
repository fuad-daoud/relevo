package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

// workflowTestStore is a config store over a fresh database, so a test can run
// the real workflow operations without a harness, a network or a real config.
func workflowTestStore(t *testing.T) *config.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return config.Open(d)
}

// workflowInvalidErr is what the real WorkflowAdd returns for a workflow whose
// start names no step. It comes from the operation rather than from a literal,
// so the goldens show the message the rules actually write.
func workflowInvalidErr(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "broken.yaml")
	body := "name: broken\nstart: ghost\nsteps:\n  build: { run: nobody, on: { done: done } }\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	_, err := relevo.WorkflowAdd(relevo.Runtime{Config: workflowTestStore(t)}, path, false, false)
	if err == nil {
		t.Fatal("an invalid workflow must not be stored")
	}
	return err
}

// workflowSavedErr is what the real WorkflowAdd returns for a second add of a
// name already saved.
func workflowSavedErr(t *testing.T, source string) error {
	t.Helper()
	rt := relevo.Runtime{Config: workflowTestStore(t)}
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if _, err := relevo.WorkflowAdd(rt, path, false, false); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err := relevo.WorkflowAdd(rt, path, false, false)
	if !errors.Is(err, relevo.ErrWorkflowSaved) {
		t.Fatalf("second add = %v, want ErrWorkflowSaved", err)
	}
	return err
}

// workflowCustomSource is a workflow under a name of its own, so a test can
// rename it to the shipped name without touching the fixtures.
const workflowCustomSource = `name: custom
start: gate
steps:
  gate: { when: "true", on: { true: done } }
`

// workflowShippedErr is what the real WorkflowAdd returns for the shipped name
// with no force, which the cockpit never passes.
func workflowShippedErr(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	body := strings.Replace(workflowCustomSource, "name: custom", "name: default", 1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	_, err := relevo.WorkflowAdd(relevo.Runtime{Config: workflowTestStore(t)}, path, false, false)
	if !errors.Is(err, relevo.ErrWorkflowShipped) {
		t.Fatalf("add of the shipped name = %v, want ErrWorkflowShipped", err)
	}
	return err
}

// workflowsFormModel is the add form opened over the list, with path typed into
// it and enter pressed, so a test starts from whatever the form then shows.
func workflowsFormModel(t *testing.T, fa *fakeActions, path string) Model {
	t.Helper()
	m := goldenActionModel(t, 132, 34, fa, view.Report{})
	m = drain(t, m, execLine("workflows", m.env(), m.prefs))
	m = candKeys(t, m, key('a'))
	m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = candType(t, m, path)
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return drain(t, res.(Model), cmd)
}

// addWorkflowFormOverlay is the add form a model has open, or a failure when it
// has something else.
func addWorkflowFormOverlay(t *testing.T, m Model) addWorkflowForm {
	t.Helper()
	f, ok := m.overlay.(addWorkflowForm)
	if !ok {
		t.Fatalf("the add form must stay open, overlay is %T", m.overlay)
	}
	return f
}

// TestWorkflowsRemoveRefusesShipped: d on the shipped workflow is a notice and
// no confirm. Mutation: open the confirm anyway, as a saved one does.
func TestWorkflowsRemoveRefusesShipped(t *testing.T) {
	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "default")

	res, cmd := m.Update(key('d'))
	m = res.(Model)
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("d on a shipped workflow must return a notice, got %T", cmd())
	}
	if msg.text != "shipped workflows cannot be removed" {
		t.Errorf("notice = %q, want the shipped refusal", msg.text)
	}
	if m.overlay != nil {
		t.Errorf("no confirm may open for a shipped workflow, got %T", m.overlay)
	}
	if len(fa.removes) != 0 {
		t.Errorf("WorkflowRemove must not be called, removes = %v", fa.removes)
	}
}

// TestWorkflowsAddExistingAsksReplace: an existing name is a conflict, not a
// silent overwrite. Mutation: re-call with replace set without asking.
func TestWorkflowsAddExistingAsksReplace(t *testing.T) {
	const path = "~/workflows/fix-first.yaml"
	fa := workflowsFake()
	fa.addResults = map[string]Result{path: {Err: workflowSavedErr(t, workflowsFixtureSources()["fix-first"].text)}}

	m := goldenActionModel(t, 132, 34, fa, view.Report{})
	m = drain(t, m, execLine("workflows", m.env(), m.prefs))
	m = candKeys(t, m, key('a'))
	m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = candType(t, m, path)

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	m = drain(t, res.(Model), cmd)

	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("an existing name must open the replace confirm, got %T", m.overlay)
	}
	if !strings.Contains(box.title, "replace") || !strings.Contains(box.title, "fix-first") {
		t.Errorf("replace confirm title = %q, want the workflow's name", box.title)
	}
	if len(fa.adds) != 1 || fa.adds[0].replace {
		t.Fatalf("adds = %+v, want one add without replace", fa.adds)
	}

	// y is the second call, and only that one sets replace.
	res, cmd = m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.adds) != 2 {
		t.Fatalf("adds = %+v, want the replace call too", fa.adds)
	}
	if !fa.adds[1].replace || fa.adds[1].path != path {
		t.Errorf("the replace call = %+v, want replace set on %q", fa.adds[1], path)
	}
	if m.overlay != nil {
		t.Errorf("y must close the confirm, got %T", m.overlay)
	}
}

// TestWorkflowsAddInvalidKeepsFormOpen: a workflow the rules reject shows its
// problems under the field and the form stays open, one per line. Mutation:
// close the form on the first problem.
func TestWorkflowsAddInvalidKeepsFormOpen(t *testing.T) {
	const path = "~/workflows/broken.yaml"
	fa := workflowsFake()
	fa.addResults = map[string]Result{path: {Err: workflowInvalidErr(t)}}

	m := workflowsFormModel(t, fa, path)
	f := addWorkflowFormOverlay(t, m)

	if len(f.problems) < 2 {
		t.Fatalf("problems = %v, want one per validation failure", f.problems)
	}
	if !strings.Contains(strings.Join(f.problems, "\n"), "start \"ghost\" is not a step") {
		t.Errorf("problems = %v, want the rules' own wording", f.problems)
	}
	if !strings.Contains(strings.Join(f.view(132), "\n"), "is not a step") {
		t.Errorf("the form must show the problems under the field:\n%s", strings.Join(f.view(132), "\n"))
	}
	if len(fa.adds) != 1 {
		t.Fatalf("adds = %+v, want the one rejected add", fa.adds)
	}
}

// TestWorkflowsAddShippedRefused: the shipped name is refused in the form, and
// --force is never offered, so there is no way past it from the cockpit.
func TestWorkflowsAddShippedRefused(t *testing.T) {
	const path = "~/workflows/default.yaml"
	fa := workflowsFake()
	fa.addResults = map[string]Result{path: {Err: workflowShippedErr(t)}}

	m := workflowsFormModel(t, fa, path)
	f := addWorkflowFormOverlay(t, m)

	if len(f.problems) != 1 || !strings.Contains(f.problems[0], "shipped") {
		t.Errorf("problems = %v, want the shipped refusal", f.problems)
	}
	if strings.Contains(strings.Join(f.view(132), "\n"), "--force") {
		t.Errorf("the cockpit must not offer --force:\n%s", strings.Join(f.view(132), "\n"))
	}
}

// TestWorkflowsViewToggleSource: s switches between the graph and the source
// and back. Mutation: ignore s.
func TestWorkflowsViewToggleSource(t *testing.T) {
	m := workflowGraphModel(t, 132, 34, "fix-first")

	wv, ok := m.top().(workflowView)
	if !ok {
		t.Fatalf("enter did not push a workflow view: %T", m.top())
	}
	if !wv.sourceLoaded || !wv.graphLoaded {
		t.Fatalf("the graph view must open loaded: source=%v graph=%v", wv.sourceLoaded, wv.graphLoaded)
	}
	if wv.src {
		t.Fatal("the view opens on the graph")
	}
	if body := stripANSI(wv.Body(m.env(), 132, 20)); !strings.Contains(body, "on done → check") {
		t.Errorf("the graph body must show the edges:\n%s", body)
	}

	m = candKeys(t, m, key('s'))
	wv = m.top().(workflowView)
	if !wv.src {
		t.Fatal("s must switch to the source")
	}
	if body := stripANSI(wv.Body(m.env(), 132, 20)); !strings.Contains(body, "start: build") {
		t.Errorf("the source body must show the workflow's own text:\n%s", body)
	}

	m = candKeys(t, m, key('s'))
	if m.top().(workflowView).src {
		t.Error("s must switch back to the graph")
	}
}

// TestWorkflowsShippedSourceIsItsDefinition: a shipped workflow has no source
// to show, so the source pane renders its definition as JSON and says so.
func TestWorkflowsShippedSourceIsItsDefinition(t *testing.T) {
	m := workflowGraphModel(t, 132, 34, "default")
	m = candKeys(t, m, key('s'))

	wv := m.top().(workflowView)
	if !wv.shipped {
		t.Fatal("the shipped workflow's source must report shipped")
	}
	left, right := wv.Context(m.env())
	if !strings.Contains(stripANSI(left), "definition (json)") {
		t.Errorf("context = %q, want the definition named as what is shown", stripANSI(left))
	}
	if stripANSI(right) != "" {
		t.Errorf("right = %q, want empty", stripANSI(right))
	}
	if body := stripANSI(wv.Body(m.env(), 132, 20)); !strings.Contains(body, `"name": "default"`) {
		t.Errorf("the shipped source must be its definition as JSON:\n%s", body)
	}
}

// TestWorkflowsRemoveCallsRemoveOnYes: the confirm's y is the one call, and n
// calls nothing.
func TestWorkflowsRemoveCallsRemoveOnYes(t *testing.T) {
	fa := workflowsFake()
	fa.removeResult = Result{Text: "removed workflow audit-only", Refresh: true}

	m := workflowsViewModelOn(t, 132, 34, fa, "audit-only")
	res, cmd := m.Update(key('d'))
	m = drain(t, res.(Model), cmd)
	if _, ok := m.overlay.(confirmBox); !ok {
		t.Fatalf("d on a saved workflow must open the confirm, got %T", m.overlay)
	}

	// n cancels: nothing is called.
	res, cmd = m.Update(key('n'))
	m = res.(Model)
	if m.overlay != nil {
		t.Error("n must close the confirm")
	}
	if cmd != nil {
		m = drain(t, m, cmd)
	}
	if len(fa.removes) != 0 {
		t.Fatalf("n must call nothing, removes = %v", fa.removes)
	}

	res, cmd = m.Update(key('d'))
	m = drain(t, res.(Model), cmd)
	res, cmd = m.Update(key('y'))
	m = res.(Model)
	m = drain(t, m, cmd)
	if len(fa.removes) != 1 || fa.removes[0] != "audit-only" {
		t.Fatalf("removes = %v, want one audit-only", fa.removes)
	}
	if m.notice != "removed workflow audit-only" {
		t.Errorf("notice = %q, want the removal's text", m.notice)
	}
}

// TestWorkflowsWithoutActionsRefused: the command needs the write seam, since
// every one of its keys writes or reads config. Mutation: drop the guard.
func TestWorkflowsWithoutActionsRefused(t *testing.T) {
	cmd := execLine("workflows", Env{Actions: nil}, prefs{})
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf(":workflows without Actions must return noticeMsg, got %T", cmd())
	}
	if msg.text != "the config views need relevo ui on this machine" {
		t.Errorf("notice = %q, want the config-views refusal", msg.text)
	}
}

// TestWorkflowsListColumns pins the four columns the plan fixes: NAME, ORIGIN,
// DESCRIPTION, INPUTS and PARAMS, with the inputs and params rendered as the
// modes and values they carry.
func TestWorkflowsListColumns(t *testing.T) {
	m := goldenWorkflowsModel(t, 132, 34, workflowsFake())
	body := stripANSI(m.View())

	for _, want := range []string{"NAME", "ORIGIN", "DESCRIPTION", "INPUTS", "PARAMS"} {
		if !strings.Contains(body, want) {
			t.Errorf("the header must carry %s:\n%s", want, body)
		}
	}
	// The table truncates its fixed cells, so the full text each row carries is
	// pinned against the layout's own renderers rather than against the screen.
	for _, want := range []string{
		"plans required · task optional",
		"task required",
		"builder=builder, reviewer=reviewer, scan=true",
		"gate=make check",
	} {
		if strings.Contains(body, want) {
			continue
		}
		if !strings.Contains(strings.Join(workflowsFixtureCells(), "\n"), want) {
			t.Errorf("a workflow row must carry %q", want)
		}
	}
	for _, want := range []string{"shipped", "saved", "plans built, checked and reviewed"} {
		if !strings.Contains(body, want) {
			t.Errorf("the list must carry %q:\n%s", want, body)
		}
	}

	// The 100-column golden drops PARAMS before DESCRIPTION, so the fixed
	// columns have to fit with a NAME wide enough to read.
	nameW, cols := workflowsLayout(100)
	if nameW < 14 {
		t.Errorf("NAME column at width 100 = %d, want at least 14", nameW)
	}
	for _, c := range cols {
		if c.key == "params" {
			t.Error("PARAMS must be the first column to leave at width 100")
		}
	}
}

// TestWorkflowGraphEdgeTextFromDefinition: the EDGES column is the definition's
// own edges as text, read through WorkflowGraph rather than re-derived here, so
// a rule change cannot make the view and the engine disagree about a path.
func TestWorkflowGraphEdgeTextFromDefinition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	body := "name: edges\nstart: work\nsteps:\n" +
		"  work: { run: builder, budget: { max: 2, per: chain, then: review }, on: { done: fix } }\n" +
		"  fix: { run: builder, on: { done: work } }\n" +
		"  review: { run: reviewer, on: { done: done, blocked: { halt: reviewer asked } } }\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	def, _, err := relevo.ResolveWorkflow(relevo.Runtime{Config: workflowTestStore(t)}, path)
	if err != nil {
		t.Fatalf("ResolveWorkflow: %v", err)
	}

	byID := map[string]relevo.GraphRow{}
	for _, row := range relevo.WorkflowGraph(def) {
		byID[row.ID] = row
	}
	want := map[string]string{
		"work":   "on done → fix · budget → review",
		"fix":    "on done → work",
		"review": "on blocked → halt: reviewer asked · on done → done",
	}
	for id, edgeText := range want {
		if got := byID[id].EdgeText; got != edgeText {
			t.Errorf("%s edges = %q, want %q", id, got, edgeText)
		}
	}
}

// workflowsFixtureCells is every fixture row rendered at the width the layout
// gives the column wide enough for, so a test can pin the full text a row
// carries rather than what the screen truncates it to.
func workflowsFixtureCells() []string {
	var out []string
	for _, list := range [][]relevo.WorkflowSummary{workflowsFixtureList()} {
		for _, w := range list {
			out = append(out,
				workflowsInputsText(w.Inputs),
				workflowsParamsText(w.Params),
			)
		}
	}
	return out
}

// TestExpandHome pins the one path transformation the form applies before it
// calls WorkflowAdd.
func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to expand against")
	}
	cases := map[string]string{
		"~/workflows/a.yaml": filepath.Join(home, "workflows", "a.yaml"),
		"~":                  home,
		"/abs/a.yaml":        "/abs/a.yaml",
		"rel/a.yaml":         "rel/a.yaml",
		"":                   "",
	}
	for in, want := range cases {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
