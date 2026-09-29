package ui

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/muesli/termenv"
)

// candFixtureJSON is this machine's seven candidates, with the names the store
// derives for them: the same data as round 1's configedit_test.go fixture.
const candFixtureJSON = `[
  {"name": "gemini-3.8-flash-high", "harness": "agy", "provider": "google", "model": "gemini-3.8-flash-high"},
  {"name": "claude-sonnet-4-6", "harness": "agy", "provider": "agy-extra", "model": "claude-sonnet-4-6"},
  {"name": "sonnet", "harness": "claude", "provider": "anthropic", "model": "sonnet"},
  {"name": "haiku", "harness": "claude", "provider": "anthropic", "model": "haiku"},
  {"name": "glm-5.3-flash", "harness": "opencode", "provider": "openrouter", "model": "z-ai/glm-5.3-flash"},
  {"name": "deepseek-v4.1-flash", "harness": "opencode", "provider": "cline-pass", "model": "cline-pass/deepseek-v4.1-flash#high"},
  {"name": "gpt-5.6-terra", "harness": "codex", "provider": "openai", "model": "gpt-5.6-terra:high"}
]`

// candFixtureDoc is round 1's fixture doc: the seven candidates above, the
// three actors, no custom agents, and a policy that allows the yolo tiers the
// actors carry.
func candFixtureDoc(t *testing.T) relevo.ConfigDoc {
	t.Helper()
	var doc relevo.ConfigDoc
	if err := json.Unmarshal([]byte(candFixtureJSON), &doc.Candidates); err != nil {
		t.Fatalf("candidates JSON: %v", err)
	}
	if len(doc.Candidates) != 7 {
		t.Fatalf("fixture has %d candidates, want 7", len(doc.Candidates))
	}
	doc.Policy = policy.Policy{MaxTier: "yolo"}
	doc.Actors = map[string]roles.Actor{
		"builder": {
			Agent: "plan-executor",
			Candidates: []roles.Entry{
				{Candidate: "gemini-3.8-flash-high"},
				{Candidate: "claude-sonnet-4-6"},
				{Candidate: "deepseek-v4.1-flash"},
				{Candidate: "gpt-5.6-terra"},
				{Candidate: "sonnet"},
				{Candidate: "glm-5.3-flash"},
			},
			Tier: "yolo",
		},
		"reviewer": {
			Agent: "reviewer",
			Candidates: []roles.Entry{
				{Candidate: "sonnet"},
				{Candidate: "gpt-5.6-terra"},
			},
			Tier: "yolo",
		},
		"researcher": {
			Agent:      "researcher",
			Candidates: []roles.Entry{{Candidate: "haiku"}},
		},
	}
	doc.Agents = map[string]roles.AgentEntry{}
	return doc
}

// candFixtureGates is §8's gate fixture, hung on railNow: gemini until
// cleared, claude-sonnet-4-6 in 20h, deepseek in 1d and gpt in 24d.
func candFixtureGates() []availability.Gate {
	return []availability.Gate{
		{
			Token: "agy/google/gemini-3.8-flash-high", Kind: availability.RateLimited,
			Since: railNow.Add(-45 * time.Minute),
			Note:  "RESOURCE_EXHAUSTED (code 429): Individual quota reached",
		},
		{Token: "agy/agy-extra/claude-sonnet-4-6", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(20 * time.Hour)},
		{Token: "opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(24 * time.Hour)},
		{Token: "codex/openai/gpt-5.6-terra:high", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(24 * 24 * time.Hour)},
	}
}

// candGatedReport is the report the goldens and unit tests hang their gates
// on.
func candGatedReport() view.Report { return view.Report{Gated: candFixtureGates()} }

// candUnusedReport is candGatedReport plus one live rate limit on antigravity,
// a provider no candidate in the fixture uses.
func candUnusedReport() view.Report {
	rep := candGatedReport()
	rep.Unused = []view.ProviderGate{{
		Provider: "antigravity",
		Since:    railNow.Add(-40 * time.Hour),
		Until:    railNow.Add(3 * time.Hour),
		Note:     "RESOURCE_EXHAUSTED (code 429): Individual quota reached",
		Source:   "relevo",
		Binding:  "oc-tui-a",
	}}
	return rep
}

// candEnv is the Env the view's pure helpers are called with.
func candEnv(rep view.Report) Env {
	return Env{Loaded: true, Now: railNow, Report: rep, Width: 132, Height: 34}
}

// goldenCandidatesModel is the candidates goldens' builder: a loaded shell, the
// `:candidates` command, and its doc load drained.
func goldenCandidatesModel(t *testing.T, width, height int, fa *fakeActions, rep view.Report) Model {
	t.Helper()
	m := goldenActionModel(t, width, height, fa, rep)
	return drain(t, m, execLine("candidates", m.env(), m.prefs))
}

// candActionEnv is the view's Env with an Actions seam: the key tests pass an
// env whose Actions is the fake under test.
func candActionEnv(a Actions, rep view.Report) Env {
	return Env{Ctx: context.Background(), Actions: a, Loaded: true, Now: railNow,
		Report: rep, Width: 132, Height: 34}
}

// candView loads a candidates view over fa's doc at width x height.
func candView(t *testing.T, fa *fakeActions, rep view.Report, width, height int) candidatesView {
	t.Helper()
	env := Env{Ctx: context.Background(), Actions: fa, Report: rep, Loaded: true,
		Now: railNow, Width: width, Height: height}
	v, cmd := newCandidatesView(env)
	next, _ := v.Update(cmd(), env)
	return next.(candidatesView)
}

// rowNames is the table's candidate names in order.
func rowNames(rows []candRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.c.Name)
	}
	return out
}

// runCmd runs one tea.Cmd, unwrapping a batch's own commands so an action's
// workingMsg and actionMsg both fire.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	return msg
}

func TestCandidatesPickOrder(t *testing.T) {
	v := candView(t, &fakeActions{doc: candFixtureDoc(t)}, candGatedReport(), 132, 34)
	got := rowNames(v.rows(candEnv(candGatedReport())))
	want := []string{
		"gemini-3.8-flash-high", "claude-sonnet-4-6", "deepseek-v4.1-flash",
		"gpt-5.6-terra", "sonnet", "glm-5.3-flash", "haiku",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pick order = %v, want %v", got, want)
	}
}

func TestCandidatesServes(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := candView(t, fa, candGatedReport(), 132, 34)
	env := candActionEnv(fa, candGatedReport())
	got := map[string]string{}
	for _, r := range v.rows(env) {
		got[r.c.Name] = servesText(r)
	}
	want := map[string]string{
		"gemini-3.8-flash-high": "builder 1",
		"claude-sonnet-4-6":     "builder 2",
		"deepseek-v4.1-flash":   "builder 3",
		"gpt-5.6-terra":         "builder 4 · reviewer 2",
		"sonnet":                "builder 5 · reviewer 1",
		"glm-5.3-flash":         "builder 6",
		"haiku":                 "researcher 1",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("SERVES %s = %q, want %q", name, got[name], w)
		}
	}
}

func TestCandidatesOffRow(t *testing.T) {
	doc := candFixtureDoc(t)
	b := doc.Actors["builder"]
	for i := range b.Candidates {
		if b.Candidates[i].Candidate == "glm-5.3-flash" {
			b.Candidates[i].Off = true
		}
	}
	doc.Actors["builder"] = b
	v := candView(t, &fakeActions{doc: doc}, candGatedReport(), 132, 34)

	var row candRow
	for _, r := range v.rows(candEnv(candGatedReport())) {
		if r.c.Name == "glm-5.3-flash" {
			row = r
		}
	}
	if row.c.Name == "" {
		t.Fatal("glm-5.3-flash is not in the table")
	}
	if row.on {
		t.Error("an off entry must not count as on")
	}
	if got := servesText(row); got != "" {
		t.Errorf("SERVES for an off row = %q, want empty", got)
	}
	text, style := candStatus(row, railNow)
	if text != "off" {
		t.Errorf("STATUS = %q, want off", text)
	}
	if style.GetForeground() != faintStyle.GetForeground() {
		t.Error("an off STATUS must be faint")
	}
}

func TestCandidatesGatedPickNamesSonnet(t *testing.T) {
	doc := candFixtureDoc(t)
	fa := &fakeActions{doc: doc}
	v := candView(t, fa, candGatedReport(), 132, 34)
	env := candActionEnv(fa, candGatedReport())
	var gemini candRow
	for _, r := range v.rows(env) {
		if r.c.Name == "gemini-3.8-flash-high" {
			gemini = r
		}
	}
	got := stripANSI(candPickSentence(doc, env, gemini))
	for _, want := range []string{"the builder's first pick", "takes sonnet"} {
		if !strings.Contains(got, want) {
			t.Errorf("pick sentence %q, want it to contain %q", got, want)
		}
	}
}

func TestCandidatesDeleteRefusedIsNotice(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := candView(t, fa, candGatedReport(), 132, 34)
	v.cur = 6 // haiku, the researcher's only candidate
	env := candActionEnv(fa, candGatedReport())

	_, cmd := v.Update(key('d'), env)
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	msg := cmd()
	notice, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("d on the researcher's only candidate gave %T, want a notice", msg)
	}
	if !strings.Contains(notice.text, "researcher has no other candidate") {
		t.Errorf("notice = %q, want it to name the researcher", notice.text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused delete must not write an edit")
	}
}

func TestCandidatesDeleteConfirmApplies(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := candView(t, fa, candGatedReport(), 132, 34)
	v.cur = 3 // gpt-5.6-terra
	env := candActionEnv(fa, candGatedReport())

	_, cmd := v.Update(key('d'), env)
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("d gave %T, want an overlay", cmd())
	}
	box, ok := open.ov.(confirmBox)
	if !ok {
		t.Fatalf("d opened %T, want a confirmBox", open.ov)
	}
	if !box.danger || box.kind != "delete" {
		t.Errorf("delete confirm = kind %q danger %v, want delete danger", box.kind, box.danger)
	}

	_, yes, closed := box.update(key('y'))
	if !closed {
		t.Fatal("y must close the confirm")
	}
	runCmd(t, yes)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	if got := fa.configEdits[0].Message; got != "delete candidate gpt-5.6-terra" {
		t.Errorf("edit message = %q", got)
	}
}

func TestCandidatesUngateKey(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := candView(t, fa, candGatedReport(), 132, 34)
	env := candActionEnv(fa, candGatedReport())

	_, cmd := v.Update(key('u'), env)
	runCmd(t, cmd)
	if len(fa.ungates) != 1 || fa.ungates[0] != "gemini-3.8-flash-high" {
		t.Errorf("ungates = %v, want [gemini-3.8-flash-high]", fa.ungates)
	}
}

func TestCandidatesProbeKey(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := candView(t, fa, candGatedReport(), 132, 34)
	env := candActionEnv(fa, candGatedReport())

	_, cmd := v.Update(key('p'), env)
	runCmd(t, cmd)
	if len(fa.probes) != 1 || fa.probes[0] != "gemini-3.8-flash-high" {
		t.Errorf("probes = %v, want [gemini-3.8-flash-high]", fa.probes)
	}
}

func TestCandidatesEnterAndAddOpenForm(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := candView(t, fa, candGatedReport(), 132, 34)
	env := candActionEnv(fa, candGatedReport())

	// a opens the add form on the cursor row's harness, on the harness row.
	_, cmd := v.Update(key('a'), env)
	if cmd == nil {
		t.Fatal("a must return a command")
	}
	msg, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("a gave %T, want an overlay", cmd())
	}
	add, ok := msg.ov.(candidateForm)
	if !ok {
		t.Fatalf("a opened %T, want a candidateForm", msg.ov)
	}
	if add.editing != "" || add.kinds[add.hsel] != "agy" || add.focus != 0 {
		t.Errorf("add form = editing %q harness %q focus %d, want an agy add on the harness row",
			add.editing, add.kinds[add.hsel], add.focus)
	}

	// enter opens the edit form on the cursor row, on the model field.
	v.cur = 2 // deepseek-v4.1-flash
	_, cmd = v.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	if cmd == nil {
		t.Fatal("enter must return a command")
	}
	msg, ok = cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("enter gave %T, want an overlay", cmd())
	}
	edit, ok := msg.ov.(candidateForm)
	if !ok {
		t.Fatalf("enter opened %T, want a candidateForm", msg.ov)
	}
	if edit.editing != "deepseek-v4.1-flash" || edit.slots != "builder 3" || edit.focus != 2 {
		t.Errorf("edit form = editing %q slots %q focus %d", edit.editing, edit.slots, edit.focus)
	}
	if got := edit.modelIn.Value(); got != "cline-pass/deepseek-v4.1-flash#high" {
		t.Errorf("edit model = %q, want the entry's model", got)
	}
}

// Keys list the footer set and HelpKeys the full set: `↑↓ move` lives only in
// the help overlay at 132 columns (§10).
func TestCandidatesKeysAndHelpKeys(t *testing.T) {
	v := candView(t, &fakeActions{doc: candFixtureDoc(t)}, candGatedReport(), 132, 34)

	keys := make([]string, 0, len(v.Keys()))
	for _, kh := range v.Keys() {
		keys = append(keys, kh.Key)
	}
	help := make([]string, 0, len(v.HelpKeys()))
	for _, kh := range v.HelpKeys() {
		help = append(help, kh.Key)
	}

	if got := strings.Join(keys, ","); got != "enter,a,d,g,u,p" {
		t.Errorf("Keys = %v, want enter,a,d,g,u,p", got)
	}
	if got := strings.Join(help, ","); got != "↑↓,enter,a,d,g,u,p" {
		t.Errorf("HelpKeys = %v, want ↑↓,enter,a,d,g,u,p", got)
	}
}

func TestCandidatesNeedsActions(t *testing.T) {
	env := Env{Ctx: context.Background(), Loaded: true, Now: railNow, Width: 132, Height: 34}
	cmd := execLine("candidates", env, prefs{})
	if cmd == nil {
		t.Fatal(":candidates with no Actions must return a notice")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf(":candidates with no Actions gave %T, want a notice", cmd())
	}
	if !strings.Contains(msg.text, "need relevo ui") {
		t.Errorf("notice = %q", msg.text)
	}
}

// TestFooterGreysAViewsOffKeys pins the footer's off-aware rendering: a key the
// top view reports in OffKeys draws in the grey chip and label, and a key it
// does not keeps the normal chip. Not parallel: it moves the global colour
// profile so the strings it compares carry escapes.
func TestFooterGreysAViewsOffKeys(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	fa := &fakeActions{doc: candFixtureDoc(t)}
	m := goldenCandidatesModel(t, 132, 34, fa, candUnusedReport())
	m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnd})

	got := m.keysView(m.env())
	wantOff := chip(offKbdStyle, "d") + " " + offStyle.Render("delete")
	if !strings.Contains(got, wantOff) {
		t.Errorf("footer does not grey d: want %q in %q", wantOff, got)
	}
	wantOn := chip(kbdStyle, "u") + " " + mutedStyle.Render("ungate")
	if !strings.Contains(got, wantOn) {
		t.Errorf("footer does not keep u normal: want %q in %q", wantOn, got)
	}
}

func TestCandidatesCursorReachesTheUnusedRow(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	rep := candUnusedReport()
	v := candView(t, fa, rep, 132, 34)
	env := candActionEnv(fa, rep)

	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnd}, env)
	v = next.(candidatesView)
	n := len(v.rows(env))
	if v.cur != n {
		t.Fatalf("cur = %d, want %d (the first unused row)", v.cur, n)
	}
	if got := v.OffKeys(env); !reflect.DeepEqual(got, []string{"enter", "d", "g", "p"}) {
		t.Errorf("OffKeys on an unused row = %v, want [enter d g p]", got)
	}

	home, _ := v.Update(tea.KeyMsg{Type: tea.KeyHome}, env)
	if got := home.(candidatesView).OffKeys(env); got != nil {
		t.Errorf("OffKeys on a candidate row = %v, want nil", got)
	}
}

func TestCandidatesUngateOnAnUnusedRowClearsItsProvider(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	rep := candUnusedReport()
	v := candView(t, fa, rep, 132, 34)
	env := candActionEnv(fa, rep)

	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnd}, env)
	v = next.(candidatesView)

	_, cmd := v.Update(key('u'), env)
	runCmd(t, cmd)
	if len(fa.ungates) != 1 || fa.ungates[0] != "antigravity" {
		t.Errorf("ungates = %v, want [antigravity]", fa.ungates)
	}
}

func TestCandidatesCandidateKeysDoNothingOnAnUnusedRow(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
	}{
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}},
		{"d", key('d')},
		{"g", key('g')},
		{"p", key('p')},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa := &fakeActions{doc: candFixtureDoc(t)}
			rep := candUnusedReport()
			v := candView(t, fa, rep, 132, 34)
			env := candActionEnv(fa, rep)

			next, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnd}, env)
			v = next.(candidatesView)
			n := len(v.rows(env))

			res, cmd := v.Update(tc.key, env)
			if cmd != nil {
				t.Errorf("%s on an unused row returned a command", tc.name)
			}
			if got := res.(candidatesView); got.cur != n {
				t.Errorf("%s moved the cursor to %d, want %d", tc.name, got.cur, n)
			}
			if len(fa.gates)+len(fa.ungates)+len(fa.probes)+len(fa.configEdits) != 0 {
				t.Errorf("%s on an unused row ran an action: gates=%v ungates=%v probes=%v edits=%v",
					tc.name, fa.gates, fa.ungates, fa.probes, fa.configEdits)
			}
		})
	}
}

func TestCandidatesContextCountsUnusedProviderGates(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}

	none := candView(t, fa, candGatedReport(), 132, 34)
	left, _ := none.Context(candActionEnv(fa, candGatedReport()))
	if strings.Contains(stripANSI(left), "unused") {
		t.Errorf("context without unused gates = %q, must not name them", stripANSI(left))
	}

	one := candView(t, fa, candUnusedReport(), 132, 34)
	left, _ = one.Context(candActionEnv(fa, candUnusedReport()))
	if !strings.Contains(stripANSI(left), "1 gate on an unused provider") {
		t.Errorf("context = %q, want it to count one gate on an unused provider", stripANSI(left))
	}

	rep := candUnusedReport()
	rep.Unused = append(rep.Unused, view.ProviderGate{Provider: "other", Since: railNow, Source: "planner"})
	two := candView(t, fa, rep, 132, 34)
	left, _ = two.Context(candActionEnv(fa, rep))
	if !strings.Contains(stripANSI(left), "2 gates on unused providers") {
		t.Errorf("context = %q, want it to count two gates on unused providers", stripANSI(left))
	}
}
