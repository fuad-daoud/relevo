package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

func actorsFixtureList(t *testing.T, fa *fakeActions) actorsView {
	t.Helper()
	env := candActionEnv(fa, candGatedReport())
	v, cmd := newActorsView(env)
	next, _ := v.Update(cmd(), env)
	return next.(actorsView)
}

func actorFixtureView(t *testing.T, fa *fakeActions, name string) actorView {
	t.Helper()
	v, _ := newActorView(candActionEnv(fa, candGatedReport()), fa.doc, name)
	return v.(actorView)
}

func actorEditEntries(t *testing.T, e relevo.ConfigEdit, actor string) []roles.Entry {
	t.Helper()
	body, ok := e.Sections[config.Actors]
	if !ok {
		t.Fatal("the edit changes no actors section")
	}
	acts, _, err := roles.ParseActors(body)
	if err != nil {
		t.Fatalf("ParseActors: %v", err)
	}
	return acts[actor].Candidates
}

func actorEntryRefs(entries []roles.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Candidate)
	}
	return out
}

func actorEditPlacement(t *testing.T, e relevo.ConfigEdit, actor string) []string {
	t.Helper()
	body, ok := e.Sections[config.Actors]
	if !ok {
		t.Fatal("the edit changes no actors section")
	}
	acts, _, err := roles.ParseActors(body)
	if err != nil {
		t.Fatalf("ParseActors: %v", err)
	}
	return acts[actor].Placement
}

// The placement tests need a list of their own, and the fixture places the
// builder on one server only.
func actorPlaced(fa *fakeActions, name string, entries []string) {
	a := fa.doc.Actors[name]
	a.Placement = entries
	fa.doc.Actors[name] = a
}

func actorPlacementView(t *testing.T, fa *fakeActions, name string) actorView {
	t.Helper()
	v := actorFixtureView(t, fa, name)
	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}, candActionEnv(fa, candGatedReport()))
	return next.(actorView)
}

func keyHelpHas(keys []widget.KeyHelp, k, help string) bool {
	for _, kh := range keys {
		if kh.Key == k && kh.Help == help {
			return true
		}
	}
	return false
}

func actorFormKeys(f actorForm, ks ...tea.KeyMsg) actorForm {
	for _, k := range ks {
		next, _, _ := f.update(k)
		f = next.(actorForm)
	}
	return f
}

func addActorText(f addActorForm, s string) addActorForm {
	for _, r := range s {
		next, _, _ := f.update(key(r))
		f = next.(addActorForm)
	}
	return f
}

func TestActorOrder(t *testing.T) {
	got := actorOrder(map[string]roles.Actor{
		"zebra": {}, "reviewer": {}, "alpha": {}, "builder": {}, "researcher": {},
	})
	want := "builder,reviewer,researcher,alpha,zebra"
	if strings.Join(got, ",") != want {
		t.Errorf("actorOrder = %v, want %s", got, want)
	}
}

func TestActorNextPickSkipsOffAndGated(t *testing.T) {
	doc := candFixtureDoc(t)
	gated := candGatedReport().Gated

	if got := nextPick(doc, "builder", gated); got != "sonnet" {
		t.Errorf("nextPick with four gates = %q, want sonnet", got)
	}
	if got := nextPick(doc, "builder", nil); got != "gemini-3.8-flash-high" {
		t.Errorf("nextPick with no gate = %q, want gemini-3.8-flash-high", got)
	}

	b := doc.Actors["builder"]
	for i := range b.Candidates {
		if b.Candidates[i].Candidate == "sonnet" {
			b.Candidates[i].Off = true
		}
	}
	doc.Actors["builder"] = b
	if got := nextPick(doc, "builder", gated); got != "glm-5.3-flash" {
		t.Errorf("nextPick with sonnet off = %q, want glm-5.3-flash", got)
	}
}

func TestActorViewShiftDownReorders(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())

	next, cmd := v.Update(tea.KeyMsg{Type: tea.KeyShiftDown}, env)
	v = next.(actorView)
	runCmd(t, cmd)

	if v.cur != 1 {
		t.Errorf("cursor = %d, want 1 (gemini-3.8-flash-high is now 2nd)", v.cur)
	}
	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	want := "claude-sonnet-4-6,gemini-3.8-flash-high,deepseek-v4.1-flash,gpt-5.6-terra,sonnet,glm-5.3-flash"
	if got := strings.Join(actorEntryRefs(actorEditEntries(t, fa.configEdits[0], "builder")), ","); got != want {
		t.Errorf("edit entries = %s, want %s", got, want)
	}
	if got := strings.Join(actorEntryRefs(v.doc.Actors["builder"].Candidates), ","); got != want {
		t.Errorf("local doc = %s, want %s (the next key must build on this list)", got, want)
	}
}

func TestActorViewSecondEditBuildsOnLocalDoc(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())

	for range 2 {
		next, cmd := v.Update(tea.KeyMsg{Type: tea.KeyShiftDown}, env)
		v = next.(actorView)
		runCmd(t, cmd)
	}

	if len(fa.configEdits) != 2 {
		t.Fatalf("configEdits = %d, want two", len(fa.configEdits))
	}
	want := "claude-sonnet-4-6,deepseek-v4.1-flash,gemini-3.8-flash-high,gpt-5.6-terra,sonnet,glm-5.3-flash"
	if got := strings.Join(actorEntryRefs(actorEditEntries(t, fa.configEdits[1], "builder")), ","); got != want {
		t.Errorf("second edit entries = %s, want %s", got, want)
	}
	if v.cur != 2 {
		t.Errorf("cursor = %d, want 2", v.cur)
	}
}

func TestActorViewIgnoresChangeKeysWhileRunning(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())

	next, cmd := v.Update(tea.KeyMsg{Type: tea.KeyShiftDown}, env)
	v = next.(actorView)
	runCmd(t, cmd)

	env.Running = map[string]string{"actor:builder": "edit actor"}
	next, cmd = v.Update(tea.KeyMsg{Type: tea.KeyShiftDown}, env)
	_ = next.(actorView)
	if cmd != nil {
		t.Errorf("a change key with the edit running returned a command: %T", cmd())
	}
	if len(fa.configEdits) != 1 {
		t.Errorf("configEdits = %d, want one", len(fa.configEdits))
	}
}

func TestActorViewSpaceTogglesOff(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "builder")

	next, cmd := v.Update(tea.KeyMsg{Type: tea.KeySpace}, candActionEnv(fa, candGatedReport()))
	v = next.(actorView)
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	entries := actorEditEntries(t, fa.configEdits[0], "builder")
	if !entries[0].Off || entries[0].Candidate != "gemini-3.8-flash-high" {
		t.Errorf("entry 1 = %+v, want gemini-3.8-flash-high off", entries[0])
	}
	if !v.doc.Actors["builder"].Candidates[0].Off {
		t.Error("the local doc must hold the toggled entry")
	}
}

func TestActorViewRemoveLastNotices(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "researcher")

	_, cmd := v.Update(key('d'), candActionEnv(fa, candGatedReport()))
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	msg := cmd()
	notice, ok := msg.(widget.NoticeMsg)
	if !ok {
		t.Fatalf("d on the only candidate gave %T, want a notice", msg)
	}
	if notice.Text != "researcher needs at least one candidate" {
		t.Errorf("notice = %q", notice.Text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused remove must not write an edit")
	}
}

func TestActorViewPickerAddsHaiku(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())

	_, cmd := v.Update(key('a'), env)
	if cmd == nil {
		t.Fatal("a must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("a gave %T, want an overlay", cmd())
	}
	box, ok := open.ov.(listBox)
	if !ok {
		t.Fatalf("a opened %T, want a listBox", open.ov)
	}
	if box.kind != "add to builder" || box.submit != "add as 7th" {
		t.Errorf("picker = kind %q submit %q, want add to builder / add as 7th", box.kind, box.submit)
	}
	if len(box.items) != 1 || box.items[0].name != "haiku" {
		t.Fatalf("picker items = %+v, want only haiku", box.items)
	}
	if box.items[0].note != "claude · anthropic" || box.items[0].status != "ready" {
		t.Errorf("picker item = note %q status %q", box.items[0].note, box.items[0].status)
	}

	runCmd(t, box.onPick("haiku"))
	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	entries := actorEditEntries(t, fa.configEdits[0], "builder")
	if n := len(entries); n != 7 || entries[n-1].Candidate != "haiku" || entries[n-1].Off {
		t.Errorf("builder list = %v, want haiku last and on", actorEntryRefs(entries))
	}
}

func TestActorFormBuilderAgents(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	f := newActorForm(candActionEnv(fa, candGatedReport()), fa.doc, "builder")
	if got := strings.Join(f.agents, ","); got != "plan-executor,librarian,designer" {
		t.Errorf("builder agents = %q, want plan-executor,librarian,designer", got)
	}
	if f.asel != 0 || f.tsel != 2 || !f.check || !f.builtin {
		t.Errorf("form = asel %d tsel %d check %v builtin %v, want 0, 2, true, true", f.asel, f.tsel, f.check, f.builtin)
	}
}

func TestActorFormCheckDisabledForReader(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	env := candActionEnv(fa, candGatedReport())

	r := newActorForm(env, fa.doc, "reviewer")
	if !r.checkDisabled() {
		t.Fatal("reviewer's check must be disabled")
	}
	r = actorFormKeys(r.setFocus(1), tea.KeyMsg{Type: tea.KeyTab})
	if r.focus != 0 {
		t.Errorf("tab from tier on a reader = focus %d, want 0 (check skipped)", r.focus)
	}

	b := newActorForm(env, fa.doc, "builder")
	if b.checkDisabled() {
		t.Fatal("builder's check must not be disabled")
	}
	b = actorFormKeys(b.setFocus(1), tea.KeyMsg{Type: tea.KeyTab})
	if b.focus != 2 {
		t.Errorf("tab from tier on a writer = focus %d, want 2 (check)", b.focus)
	}
}

func TestActorsListDeleteBuiltinNotices(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorsFixtureList(t, fa)

	_, cmd := v.Update(key('d'), candActionEnv(fa, candGatedReport()))
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	msg := cmd()
	notice, ok := msg.(widget.NoticeMsg)
	if !ok {
		t.Fatalf("d on builder gave %T, want a notice", msg)
	}
	if !strings.Contains(notice.Text, "built in") {
		t.Errorf("notice = %q, want it to say built in", notice.Text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused delete must not write an edit")
	}
}

func TestAddActorFormBadNameShowsError(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	f := addActorText(newAddActorForm(candActionEnv(fa, candGatedReport()), fa.doc), "Bad Name")

	next, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if closed {
		t.Fatal("a refused add must keep the form open")
	}
	if cmd != nil {
		t.Errorf("a refused add returned a command: %T", cmd())
	}
	f = next.(addActorForm)
	if f.err == "" {
		t.Fatal("the form must hold the FieldError")
	}
	if !strings.Contains(f.err, "bad actor name") {
		t.Errorf("err = %q, want it to name the bad actor name", f.err)
	}
	if !strings.Contains(stripANSI(strings.Join(f.view(132), "\n")), "bad actor name") {
		t.Error("the form must show the error row")
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused add must not write an edit")
	}
}

func TestActorViewTabTogglesFocus(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())

	if v.placementFocus() {
		t.Fatal("the candidate table must own the keys first")
	}
	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}, env)
	v = next.(actorView)
	if !v.placementFocus() {
		t.Fatalf("after tab: focus = %d, want the placement pane", v.focus)
	}
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}, env)
	v = next.(actorView)
	if v.placementFocus() {
		t.Errorf("after the second tab: focus = %d, want the candidate table", v.focus)
	}
}

func TestActorViewPlacementShiftDownReorders(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	actorPlaced(fa, "builder", []string{"zen", "backup", "local"})
	v := actorPlacementView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())
	candidates := strings.Join(actorEntryRefs(v.doc.Actors["builder"].Candidates), ",")

	next, cmd := v.Update(tea.KeyMsg{Type: tea.KeyShiftDown}, env)
	v = next.(actorView)
	runCmd(t, cmd)

	if v.placeCur != 1 {
		t.Errorf("placeCur = %d, want 1 (zen is now second)", v.placeCur)
	}
	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	edit := fa.configEdits[0]
	if edit.Message != "edit actor builder placement" || edit.Name != "builder" {
		t.Errorf("edit = {message: %q, name: %q}", edit.Message, edit.Name)
	}
	if got := strings.Join(actorEditPlacement(t, edit, "builder"), ","); got != "backup,zen,local" {
		t.Errorf("edit placement = %s, want backup,zen,local", got)
	}
	if got := strings.Join(actorEntryRefs(actorEditEntries(t, edit, "builder")), ","); got != candidates {
		t.Errorf("the placement edit changed the candidates: %s, want %s", got, candidates)
	}
	if got := strings.Join(v.doc.Actors["builder"].Placement, ","); got != "backup,zen,local" {
		t.Errorf("local doc placement = %s, want the swapped list", got)
	}
}

func TestActorViewPlacementRemove(t *testing.T) {
	t.Run("the last entry clears", func(t *testing.T) {
		fa := &fakeActions{doc: candFixtureDoc(t)}
		v := actorPlacementView(t, fa, "builder")

		next, cmd := v.Update(key('d'), candActionEnv(fa, candGatedReport()))
		v = next.(actorView)
		runCmd(t, cmd)

		if len(fa.configEdits) != 1 {
			t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
		}
		if got := actorEditPlacement(t, fa.configEdits[0], "builder"); got != nil {
			t.Errorf("edit placement = %v, want nil (the default)", got)
		}
		if got := v.doc.Actors["builder"].Placement; got != nil {
			t.Errorf("local doc placement = %v, want nil", got)
		}
		if v.placeCur != 0 {
			t.Errorf("placeCur = %d, want 0", v.placeCur)
		}
	})

	t.Run("a middle entry is removed", func(t *testing.T) {
		fa := &fakeActions{doc: candFixtureDoc(t)}
		actorPlaced(fa, "builder", []string{"zen", "backup", "local"})
		v := actorPlacementView(t, fa, "builder")
		v.placeCur = 1

		next, cmd := v.Update(key('d'), candActionEnv(fa, candGatedReport()))
		v = next.(actorView)
		runCmd(t, cmd)

		if len(fa.configEdits) != 1 {
			t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
		}
		if got := strings.Join(actorEditPlacement(t, fa.configEdits[0], "builder"), ","); got != "zen,local" {
			t.Errorf("edit placement = %s, want zen,local", got)
		}
	})
}

func TestActorViewPlacementPicker(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorPlacementView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())

	_, cmd := v.Update(key('a'), env)
	if cmd == nil {
		t.Fatal("a must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("a gave %T, want an overlay", cmd())
	}
	box, ok := open.ov.(listBox)
	if !ok {
		t.Fatalf("a opened %T, want a listBox", open.ov)
	}
	if box.kind != "placement" || box.submit != "add" {
		t.Errorf("picker = kind %q submit %q, want placement / add", box.kind, box.submit)
	}
	if len(box.items) != 2 || box.items[0].name != "local" || box.items[1].name != "backup" {
		t.Fatalf("picker items = %+v, want local then backup", box.items)
	}
	if box.items[0].note != "this machine" || box.items[1].note != "https://backup:7777" {
		t.Errorf("picker notes = %q / %q", box.items[0].note, box.items[1].note)
	}

	runCmd(t, box.onPick("local"))
	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	if got := strings.Join(actorEditPlacement(t, fa.configEdits[0], "builder"), ","); got != "zen,local" {
		t.Errorf("edit placement = %s, want zen,local (the pick appends)", got)
	}

	everything := &fakeActions{doc: candFixtureDoc(t)}
	actorPlaced(everything, "builder", []string{"zen", "backup", "local"})
	full := actorPlacementView(t, everything, "builder")
	_, cmd = full.Update(key('a'), candActionEnv(everything, candGatedReport()))
	if cmd == nil {
		t.Fatal("a with everything listed must return a command")
	}
	msg := cmd()
	notice, ok := msg.(widget.NoticeMsg)
	if !ok {
		t.Fatalf("a with everything listed gave %T, want a notice", msg)
	}
	if notice.Text != "every server is already in builder's placement" {
		t.Errorf("notice = %q", notice.Text)
	}
	if len(everything.configEdits) != 0 {
		t.Error("the notice must not write an edit")
	}
}

func TestActorViewPlacementSpaceIsInert(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	actorPlaced(fa, "builder", []string{"zen", "backup", "local"})
	v := actorPlacementView(t, fa, "builder")
	v.placeCur = 1

	next, cmd := v.Update(tea.KeyMsg{Type: tea.KeySpace}, candActionEnv(fa, candGatedReport()))
	v = next.(actorView)
	if cmd != nil {
		t.Errorf("space in the placement pane returned a command: %T", cmd())
	}
	if len(fa.configEdits) != 0 {
		t.Errorf("configEdits = %d, want none", len(fa.configEdits))
	}
	if v.placeCur != 1 {
		t.Errorf("placeCur = %d, want 1", v.placeCur)
	}
}

func TestActorViewPlacementEditOpensTheForm(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorPlacementView(t, fa, "builder")

	_, cmd := v.Update(key('e'), candActionEnv(fa, candGatedReport()))
	if cmd == nil {
		t.Fatal("e must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("e gave %T, want an overlay", cmd())
	}
	form, ok := open.ov.(actorForm)
	if !ok {
		t.Fatalf("e opened %T, want an actorForm", open.ov)
	}
	if form.actor != "builder" {
		t.Errorf("the form is for %q, want builder", form.actor)
	}
}

func TestActorViewEmptyPlacementRendersTheDefault(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	actorPlaced(fa, "builder", nil)
	v := actorPlacementView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())

	pane := actorPlacementLines(v.doc, v.name, true, 0, env.Width)
	if len(pane) != 2 {
		t.Fatalf("the empty placement pane has %d lines, want the header and the default line", len(pane))
	}
	if !strings.Contains(stripANSI(pane[0]), "PLACEMENT") {
		t.Errorf("pane line 1 = %q, want the header", stripANSI(pane[0]))
	}
	line := stripANSI(pane[1])
	if !strings.Contains(line, "local") || !strings.Contains(line, "default") {
		t.Errorf("pane line 2 = %q, want the local default line", line)
	}

	body := strings.Join(v.bodyLines(env, env.Width), "\n")
	if !strings.Contains(stripANSI(body), "default") {
		t.Errorf("the body does not carry the default line:\n%s", stripANSI(body))
	}
	if v.placeCur != 0 {
		t.Errorf("placeCur = %d, want 0", v.placeCur)
	}
}

func TestActorViewPlacementIgnoresChangeKeysWhileRunning(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	actorPlaced(fa, "builder", []string{"zen", "backup"})
	v := actorPlacementView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())
	env.Running = map[string]string{"actor:builder": "edit actor"}

	for _, k := range []tea.KeyMsg{{Type: tea.KeyShiftDown}, {Type: tea.KeyShiftUp}, key('d'), key('a')} {
		next, cmd := v.Update(k, env)
		v = next.(actorView)
		if cmd != nil {
			t.Errorf("%q with the edit running returned a command: %T", k.String(), cmd())
		}
	}
	if len(fa.configEdits) != 0 {
		t.Errorf("configEdits = %d, want none", len(fa.configEdits))
	}
	if got := strings.Join(v.doc.Actors["builder"].Placement, ","); got != "zen,backup" {
		t.Errorf("placement = %s, want it untouched", got)
	}

	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}, env)
	focused := v.placementFocus()
	if v = next.(actorView); v.placementFocus() == focused {
		t.Error("tab must still toggle the focus while an edit is running")
	}
}

func TestActorViewPlacementCursorClampsOnDocMsg(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	actorPlaced(fa, "builder", []string{"zen", "backup", "local"})
	v := actorPlacementView(t, fa, "builder")
	v.placeCur = 2

	short := candFixtureDoc(t)
	a := short.Actors["builder"]
	a.Placement = nil
	short.Actors["builder"] = a
	next, _ := v.Update(candDocMsg{doc: short}, candActionEnv(fa, candGatedReport()))
	if got := next.(actorView).placeCur; got != 0 {
		t.Errorf("placeCur = %d, want 0 after the list shrank to nothing", got)
	}
}

func TestActorViewKeysFollowThePane(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "builder")

	if !keyHelpHas(v.Keys(), "tab", "placement") || !keyHelpHas(v.Keys(), "space", "on / off") {
		t.Errorf("candidate pane Keys = %+v, want tab placement and space on / off", v.Keys())
	}
	if !keyHelpHas(v.HelpKeys(), "space", "on / off") || !keyHelpHas(v.HelpKeys(), "tab", "switch list") {
		t.Errorf("HelpKeys = %+v, want both panes' keys", v.HelpKeys())
	}

	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}, candActionEnv(fa, candGatedReport()))
	v = next.(actorView)
	if !keyHelpHas(v.Keys(), "tab", "candidates") {
		t.Errorf("placement pane Keys = %+v, want tab candidates", v.Keys())
	}
	for _, kh := range v.Keys() {
		if kh.Key == "space" {
			t.Errorf("placement pane Keys = %+v, must not offer space on / off", v.Keys())
		}
	}
	if !keyHelpHas(v.HelpKeys(), "space", "on / off") {
		t.Errorf("HelpKeys in the placement pane = %+v, want the candidates' space key kept", v.HelpKeys())
	}
}
