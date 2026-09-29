package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// actorsFixtureList loads an actorsView over fa's doc, as the ':actors'
// command's doc load would.
func actorsFixtureList(t *testing.T, fa *fakeActions) actorsView {
	t.Helper()
	env := candActionEnv(fa, candGatedReport())
	v, cmd := newActorsView(env)
	next, _ := v.Update(cmd(), env)
	return next.(actorsView)
}

// actorFixtureView is the pushed detail view over fa's builder.
func actorFixtureView(t *testing.T, fa *fakeActions, name string) actorView {
	t.Helper()
	v, _ := newActorView(candActionEnv(fa, candGatedReport()), fa.doc, name)
	return v.(actorView)
}

// actorEditEntries decodes one edit's actor's entry list.
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

// actorEntryRefs is the entry list's candidate references, in order.
func actorEntryRefs(entries []roles.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Candidate)
	}
	return out
}

// actorFormKeys sends keys through the actor form, one at a time.
func actorFormKeys(f actorForm, ks ...tea.KeyMsg) actorForm {
	for _, k := range ks {
		next, _, _ := f.update(k)
		f = next.(actorForm)
	}
	return f
}

// addActorText types s's runes through the add form's name input.
func addActorText(f addActorForm, s string) addActorForm {
	for _, r := range s {
		next, _, _ := f.update(key(r))
		f = next.(addActorForm)
	}
	return f
}

// actorOrder puts builder, reviewer and researcher first, then the rest by
// name (§3).
func TestActorOrder(t *testing.T) {
	got := actorOrder(map[string]roles.Actor{
		"zebra": {}, "reviewer": {}, "alpha": {}, "builder": {}, "researcher": {},
	})
	want := "builder,reviewer,researcher,alpha,zebra"
	if strings.Join(got, ",") != want {
		t.Errorf("actorOrder = %v, want %s", got, want)
	}
}

// nextPick skips an off entry and a gated one (§3).
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

// shift+down swaps the cursor entry with its neighbour, moves the cursor with
// it, writes one edit and updates the local doc (§4, §5).
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

// A second shift+down builds on the list the first one wrote, not on the doc
// the view loaded: two edits, and the second one's list carries both swaps.
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

// While this actor's edit is in flight every change key is ignored, so two
// fast presses write one edit (§4).
func TestActorViewIgnoresChangeKeysWhileRunning(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "builder")
	env := candActionEnv(fa, candGatedReport())

	next, cmd := v.Update(tea.KeyMsg{Type: tea.KeyShiftDown}, env)
	v = next.(actorView)
	runCmd(t, cmd)

	env.Running = map[string]string{"actor:builder": "edit actor"}
	next, cmd = v.Update(tea.KeyMsg{Type: tea.KeyShiftDown}, env)
	v = next.(actorView)
	if cmd != nil {
		t.Errorf("a change key with the edit running returned a command: %T", cmd())
	}
	if len(fa.configEdits) != 1 {
		t.Errorf("configEdits = %d, want one", len(fa.configEdits))
	}
}

// space toggles Off on the cursor entry and writes it (§4).
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

// d on an actor's last candidate is a notice, not an edit (§4).
func TestActorViewRemoveLastNotices(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorFixtureView(t, fa, "researcher")

	_, cmd := v.Update(key('d'), candActionEnv(fa, candGatedReport()))
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	msg := cmd()
	notice, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("d on the only candidate gave %T, want a notice", msg)
	}
	if notice.text != "researcher needs at least one candidate" {
		t.Errorf("notice = %q", notice.text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused remove must not write an edit")
	}
}

// a opens the picker on the candidates the actor does not list yet; picking
// one appends it, on (§4).
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

// The actor form's agent chips for the builtin builder offer only the writer
// agents, in shipped order (§4).
func TestActorFormBuilderAgents(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	f := newActorForm(candActionEnv(fa, candGatedReport()), fa.doc, "builder")
	if got := strings.Join(f.agents, ","); got != "plan-executor" {
		t.Errorf("builder agents = %q, want plan-executor", got)
	}
	if f.asel != 0 || f.tsel != 2 || !f.check || !f.builtin {
		t.Errorf("form = asel %d tsel %d check %v builtin %v, want 0, 2, true, true", f.asel, f.tsel, f.check, f.builtin)
	}
}

// A reader agent disables the check row, and tab skips it (§4).
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

// d on a builtin actor in the list is a notice: the actor cannot be deleted
// (§4).
func TestActorsListDeleteBuiltinNotices(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := actorsFixtureList(t, fa)

	_, cmd := v.Update(key('d'), candActionEnv(fa, candGatedReport()))
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	msg := cmd()
	notice, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("d on builder gave %T, want a notice", msg)
	}
	if !strings.Contains(notice.text, "built in") {
		t.Errorf("notice = %q, want it to say built in", notice.text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused delete must not write an edit")
	}
}

// A bad name in the add form shows its FieldError and keeps the form open
// (§4).
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
