package ui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/view"
)

// settingsFixtureJSON is 3a's real policy: the store's raw policy body this
// round's Settings/EditPolicy tests and the settings goldens share.
const settingsFixtureJSON = `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}}}`

// settingsFixtureDoc is candFixtureDoc(t) with its Policy replaced by
// settingsFixtureJSON, parsed, and PolicyRaw set to its bytes, so EditPolicy
// and ResetSetting have a real stored body to edit.
func settingsFixtureDoc(t *testing.T) relevo.ConfigDoc {
	t.Helper()
	doc := candFixtureDoc(t)
	p, _, err := policy.Parse(config.FileName(config.Policy), []byte(settingsFixtureJSON))
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	doc.Policy = p
	doc.PolicyRaw = json.RawMessage(settingsFixtureJSON)
	return doc
}

// settingsFixtureDocNoTiers is settingsFixtureDoc with every actor's own
// Tier cleared, so lowering or resetting max_tier down to "edit" does not
// collide with a builtin actor's fixed "yolo" tier in roles.Build's cap
// check (used where a reset or a rounds-form edit moves max_tier down).
func settingsFixtureDocNoTiers(t *testing.T) relevo.ConfigDoc {
	t.Helper()
	doc := settingsFixtureDoc(t)
	cleared := make(map[string]roles.Actor, len(doc.Actors))
	for name, a := range doc.Actors {
		a.Tier = ""
		cleared[name] = a
	}
	doc.Actors = cleared
	return doc
}

// settingsFixtureView loads a settingsView over fa's doc, as the
// ':settings' command's doc load would.
func settingsFixtureView(t *testing.T, fa *fakeActions) settingsView {
	t.Helper()
	env := candActionEnv(fa, view.Report{})
	v, cmd := newSettingsView(env)
	next, _ := v.Update(cmd(), env)
	return next.(settingsView)
}

// settingsFormFrom presses enter on the view's cursor row and returns the
// settingsForm it opens.
func settingsFormFrom(t *testing.T, v settingsView, env Env) settingsForm {
	t.Helper()
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	if cmd == nil {
		t.Fatal("enter must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("enter gave %T, want an overlay", cmd())
	}
	f, ok := open.ov.(settingsForm)
	if !ok {
		t.Fatalf("enter opened %T, want a settingsForm", open.ov)
	}
	return f
}

// settingsFormType types s's runes through f, one key at a time.
func settingsFormType(f settingsForm, s string) settingsForm {
	for _, r := range s {
		next, _, _ := f.update(key(r))
		f = next.(settingsForm)
	}
	return f
}

// a. The rounds form: max_tier moves from yolo to edit, one edit with the
// message "set max_tier edit".
func TestSettingsRoundsFormChangesMaxTier(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDocNoTiers(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 1 // max_tier

	f := settingsFormFrom(t, v, env)
	if f.title != "rounds" {
		t.Fatalf("title = %q, want rounds", f.title)
	}
	if f.focus != 1 {
		t.Fatalf("focus = %d, want 1 (max_tier)", f.focus)
	}

	next, _, _ := f.update(tea.KeyMsg{Type: tea.KeyLeft}) // yolo -> edit
	f = next.(settingsForm)

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	edit := fa.configEdits[0]
	if edit.Message != "set max_tier edit" {
		t.Errorf("message = %q, want %q", edit.Message, "set max_tier edit")
	}
	if body := string(edit.Sections[config.Policy]); !strings.Contains(body, `"max_tier": "edit"`) {
		t.Errorf("body = %s, want max_tier edit", body)
	}
}

// b. The check form: typing "make check" into command and enter writes
// gate.default.
func TestSettingsCheckFormSetsCommand(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 4 // gate.default

	f := settingsFormFrom(t, v, env)
	if f.title != "check" || f.focus != 0 {
		t.Fatalf("form = title %q focus %d, want check / 0 (command)", f.title, f.focus)
	}
	f = settingsFormType(f, "make check")

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	body := string(fa.configEdits[0].Sections[config.Policy])
	if !strings.Contains(body, `"default": "make check"`) {
		t.Errorf("body = %s, want gate.default make check", body)
	}
}

// c. The timing form: "15" into stall_after shows the duration error, and
// enter records no edit.
func TestSettingsTimingFormBadDuration(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 8 // stall_after

	f := settingsFormFrom(t, v, env)
	if f.title != "timing" || f.focus != 1 {
		t.Fatalf("form = title %q focus %d, want timing / 1 (stall_after)", f.title, f.focus)
	}
	f = settingsFormType(f, "15")

	view := stripANSI(strings.Join(f.view(90), "\n"))
	if !strings.Contains(view, "a duration like 15m or 1h30m") {
		t.Errorf("view = %s, want the duration error shown", view)
	}

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if closed {
		t.Fatal("an invalid submit must keep the form open")
	}
	if cmd != nil {
		t.Errorf("an invalid submit must return no command: %T", cmd())
	}
	if len(fa.configEdits) != 0 {
		t.Error("an invalid submit must not write an edit")
	}
}

// d. r on max_tier, then y: one edit, message "reset max_tier", max_tier gone
// from the body.
func TestSettingsResetMaxTier(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDocNoTiers(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 1 // max_tier

	_, cmd := v.Update(key('r'), env)
	if cmd == nil {
		t.Fatal("r must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("r gave %T, want an overlay", cmd())
	}
	box, ok := open.ov.(confirmBox)
	if !ok {
		t.Fatalf("r opened %T, want a confirmBox", open.ov)
	}

	_, yes, closed := box.update(key('y'))
	if !closed {
		t.Fatal("y must close the confirm")
	}
	runCmd(t, yes)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	if got := fa.configEdits[0].Message; got != "reset max_tier" {
		t.Errorf("message = %q, want reset max_tier", got)
	}
	if body := string(fa.configEdits[0].Sections[config.Policy]); strings.Contains(body, "max_tier") {
		t.Errorf("body = %s, must not contain max_tier", body)
	}
}

// g. r on max_tier is refused before any confirm when the reset itself would
// fail: builder and reviewer run at yolo, so resetting max_tier to its edit
// default is refused by roles.Build's cap check.
func TestSettingsResetRefusedBeforeConfirm(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 1 // max_tier

	_, cmd := v.Update(key('r'), env)
	if cmd == nil {
		t.Fatal("r must return a command")
	}
	msg := cmd()
	n, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("r on a refused reset gave %T, want a notice", msg)
	}
	if !strings.HasPrefix(n.text, "can't reset max_tier:") {
		t.Errorf("notice = %q, want prefix %q", n.text, "can't reset max_tier:")
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused reset must not write an edit")
	}
}

// e. r on gate.default (unset) is a notice, with no confirm.
func TestSettingsResetUnsetNotices(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 4 // gate.default, unset in the fixture

	_, cmd := v.Update(key('r'), env)
	if cmd == nil {
		t.Fatal("r must return a command")
	}
	msg := cmd()
	n, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("r on an unset row gave %T, want a notice", msg)
	}
	if n.text != "gate.default is already the default" {
		t.Errorf("notice = %q", n.text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("an unset reset must not write an edit")
	}
}

// a. serve.scope form: prefilled slice = relevo.slice. Typing 200% into
// cpu_quota and enter writes serve.scope.cpu_quota, keeping serve.scope.slice.
func TestSettingsServeScopeFormSetsCPUQuota(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 14 // serve.scope

	f := settingsFormFrom(t, v, env)
	if f.title != "serve.scope" {
		t.Fatalf("title = %q, want serve.scope", f.title)
	}
	if f.fields[1].label != "slice" || f.fields[1].orig != "relevo.slice" {
		t.Fatalf("slice field = %+v, want prefilled relevo.slice", f.fields[1])
	}
	f = f.setFocus(4) // cpu_quota
	f = settingsFormType(f, "200%")

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	body := string(fa.configEdits[0].Sections[config.Policy])
	if !strings.Contains(body, `"cpu_quota": "200%"`) {
		t.Errorf("body = %s, want serve.scope.cpu_quota 200%%", body)
	}
	if !strings.Contains(body, `"slice": "relevo.slice"`) {
		t.Errorf("body = %s, want serve.scope.slice kept", body)
	}
}

// b. serve.scope form: clearing slice, its only stored field, and pressing
// enter removes serve entirely -- the empty scope block and its now-empty
// parent both prune away.
func TestSettingsServeScopeFormClearingSliceRemovesServe(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 14 // serve.scope

	f := settingsFormFrom(t, v, env)
	f = f.setFocus(1) // slice
	orig := f.fields[1].orig
	for range orig {
		next, _, _ := f.update(tea.KeyMsg{Type: tea.KeyBackspace})
		f = next.(settingsForm)
	}
	if f.fields[1].input.Value() != "" {
		t.Fatalf("slice = %q, want cleared", f.fields[1].input.Value())
	}

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	body := string(fa.configEdits[0].Sections[config.Policy])
	if strings.Contains(body, "serve") {
		t.Errorf("body = %s, must not contain serve", body)
	}
}

// c. scope form: 8 into cpu_weight is valid, abc into memory_max is not; the
// form shows the memory_max error and enter records no edit.
func TestSettingsScopeFormBadMemoryMax(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 12 // scope

	f := settingsFormFrom(t, v, env)
	if f.title != "scope" {
		t.Fatalf("title = %q, want scope", f.title)
	}
	f = f.setFocus(2) // cpu_weight
	f = settingsFormType(f, "8")
	f = f.setFocus(3) // memory_max
	f = settingsFormType(f, "abc")

	view := stripANSI(strings.Join(f.view(90), "\n"))
	if !strings.Contains(view, "a size like 8G or 512M") {
		t.Errorf("view = %s, want the memory_max error shown", view)
	}

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if closed {
		t.Fatal("an invalid submit must keep the form open")
	}
	if cmd != nil {
		t.Errorf("an invalid submit must return no command: %T", cmd())
	}
	if len(fa.configEdits) != 0 {
		t.Error("an invalid submit must not write an edit")
	}
}

// d. classify form on a policy without classify: moving provider to jev and
// pressing enter writes classify.provider = "jev" and nothing else under
// classify.
func TestSettingsClassifyFormTurnsOn(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 16 // classify

	f := settingsFormFrom(t, v, env)
	if f.title != "classify" {
		t.Fatalf("title = %q, want classify", f.title)
	}
	next, _, _ := f.update(tea.KeyMsg{Type: tea.KeyRight}) // off -> jev
	f = next.(settingsForm)

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	body := string(fa.configEdits[0].Sections[config.Policy])
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cl, ok := m["classify"].(map[string]any)
	if !ok {
		t.Fatalf("classify is not a map: %v", m["classify"])
	}
	if len(cl) != 1 || cl["provider"] != "jev" {
		t.Errorf("classify = %v, want only provider jev", cl)
	}
}

// e. classify form on a policy with classify.provider = "jev": a pending edit
// to model, made while it is enabled, is dropped once provider moves to off,
// and the whole classify block is deleted. M1: skip the disabled-fields
// exclusion in validSets and the model edit survives, so classify reappears
// (EditPolicy's own set-after-delete recreates it) and this test fails.
func TestSettingsClassifyFormTurnsOffIgnoresPendingFields(t *testing.T) {
	doc := settingsFixtureDoc(t)
	raw := `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}},"classify":{"provider":"jev"}}`
	p, _, err := policy.Parse(config.FileName(config.Policy), []byte(raw))
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	doc.Policy = p
	doc.PolicyRaw = json.RawMessage(raw)

	fa := &fakeActions{doc: doc}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 16 // classify

	f := settingsFormFrom(t, v, env)
	if f.fields[0].sel != 1 {
		t.Fatalf("provider sel = %d, want 1 (jev)", f.fields[0].sel)
	}
	f = f.setFocus(1) // model, enabled while provider is jev
	f = settingsFormType(f, "should-be-ignored")
	f = f.setFocus(0)
	next, _, _ := f.update(tea.KeyMsg{Type: tea.KeyLeft}) // jev -> off
	f = next.(settingsForm)

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	body := string(fa.configEdits[0].Sections[config.Policy])
	if strings.Contains(body, "classify") {
		t.Errorf("body = %s, must not contain classify", body)
	}
	if strings.Contains(body, "should-be-ignored") {
		t.Errorf("body = %s, must not contain the pending model edit", body)
	}
}

// f. classify form, provider off: tab never focuses model, threshold or
// timeout, all disabled while provider stays off.
func TestSettingsClassifyFormTabSkipsDisabledFields(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 16 // classify

	f := settingsFormFrom(t, v, env)
	for i := 0; i < 5; i++ {
		next, _, _ := f.update(tea.KeyMsg{Type: tea.KeyTab})
		f = next.(settingsForm)
		if f.focus != 0 {
			t.Fatalf("tab %d landed on focus %d (%s), want to stay on provider (0)", i, f.focus, f.fields[f.focus].label)
		}
	}
}

// g. r on max_tier is refused in plain words: builder and reviewer run at
// yolo, above the edit default a reset would restore. M2: remove
// HumanPolicyError's tier rule and this test fails.
func TestSettingsResetRefusalIsPlainWords(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 1 // max_tier

	_, cmd := v.Update(key('r'), env)
	if cmd == nil {
		t.Fatal("r must return a command")
	}
	msg := cmd()
	n, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("r on a refused reset gave %T, want a notice", msg)
	}
	want := "can't reset max_tier: builder runs at yolo, above edit; lower its tier in :actors first"
	if n.text != want {
		t.Errorf("notice = %q, want %q", n.text, want)
	}
}

// f. down from the last rounds row lands on gate.default, never on a rule line.
func TestSettingsDownSkipsRule(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 3 // artifact_max_mb, the last rounds row

	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}, env)
	v = next.(settingsView)

	settings := relevo.Settings(v.doc, numCPU())
	if v.cur < 0 || v.cur >= len(settings) {
		t.Fatalf("cursor = %d, out of range", v.cur)
	}
	if settings[v.cur].Key != "gate.default" {
		t.Errorf("cursor key = %q, want gate.default", settings[v.cur].Key)
	}
}
