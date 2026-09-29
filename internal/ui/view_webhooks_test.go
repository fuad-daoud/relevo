package ui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

// webhooksFixtureJSON is settingsFixtureJSON plus two stored webhooks: the
// first slack with two events, the second the json default with none.
const webhooksFixtureJSON = `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}},"notify":{"webhooks":[{"url":"https://hooks.example.com/relevo","format":"slack","events":["state_changed:needs_you","binding_stale"]},{"url":"https://discord.example.com/hook","format":"json"}]}}`

// webhookSingleJSON is a policy whose only webhook is one json hook.
const webhookSingleJSON = `{"max_switches":2,"max_tier":"yolo","notify":{"webhooks":[{"url":"https://example.com/hook"}]}}`

// webhooksDocFrom is candFixtureDoc with its policy replaced by raw, parsed,
// and PolicyRaw set to its bytes, so EditPolicy has a real body to edit.
func webhooksDocFrom(t *testing.T, raw string) relevo.ConfigDoc {
	t.Helper()
	doc := candFixtureDoc(t)
	p, _, err := policy.Parse(config.FileName(config.Policy), []byte(raw))
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	doc.Policy = p
	doc.PolicyRaw = json.RawMessage(raw)
	return doc
}

// webhookEditBody is a ConfigEdit's policy body decoded for assertions.
func webhookEditBody(t *testing.T, edit relevo.ConfigEdit) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(edit.Sections[config.Policy], &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// webhookStoredHooks is edit's stored notify.webhooks decoded, or nil when the
// notify block is gone.
func webhookStoredHooks(t *testing.T, edit relevo.ConfigEdit) []any {
	t.Helper()
	m := webhookEditBody(t, edit)
	notify, ok := m["notify"].(map[string]any)
	if !ok {
		return nil
	}
	hooks, _ := notify["webhooks"].([]any)
	return hooks
}

// webhooksViewFrom enters notify.webhooks on a loaded settings view and drains
// the pushed view's doc load.
func webhooksViewFrom(t *testing.T, fa *fakeActions, env Env) webhooksView {
	t.Helper()
	v := settingsFixtureView(t, fa)
	v.cur = 17 // notify.webhooks
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	if cmd == nil {
		t.Fatal("enter must return a command")
	}
	pushed, ok := cmd().(pushMsg)
	if !ok {
		t.Fatalf("enter gave %T, want a push", cmd())
	}
	wv, ok := pushed.v.(webhooksView)
	if !ok {
		t.Fatalf("enter pushed %T, want a webhooksView", pushed.v)
	}
	next, _ := wv.Update(pushed.init(), env)
	return next.(webhooksView)
}

// openWebhookForm runs an opening key on the list and returns the form it
// raised.
func openWebhookForm(t *testing.T, wv webhooksView, k tea.KeyMsg, env Env) webhookForm {
	t.Helper()
	_, cmd := wv.Update(k, env)
	if cmd == nil {
		t.Fatal("the key must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("the key gave %T, want an overlay", cmd())
	}
	f, ok := open.ov.(webhookForm)
	if !ok {
		t.Fatalf("the key opened %T, want a webhookForm", open.ov)
	}
	return f
}

// webhookFormType types s's runes through f, one key at a time.
func webhookFormType(f webhookForm, s string) webhookForm {
	for _, r := range s {
		next, _, _ := f.update(key(r))
		f = next.(webhookForm)
	}
	return f
}

// a. enter on notify.webhooks, a, type a url, enter: one edit whose body has
// the hook.
func TestWebhooksAddWritesHook(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	wv := webhooksViewFrom(t, fa, env)

	f := openWebhookForm(t, wv, key('a'), env)
	if f.title != "add webhook" {
		t.Fatalf("title = %q, want add webhook", f.title)
	}
	f = webhookFormType(f, "https://example.com/hook")

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	edit := fa.configEdits[0]
	if edit.Message != "add webhook https://example.com/hook" {
		t.Errorf("message = %q, want add webhook https://example.com/hook", edit.Message)
	}
	hooks := webhookStoredHooks(t, edit)
	if len(hooks) != 1 {
		t.Fatalf("webhooks = %v, want one", hooks)
	}
	if got := hooks[0].(map[string]any)["url"]; got != "https://example.com/hook" {
		t.Errorf("webhooks[0].url = %v, want the typed url", got)
	}
}

// b. Edit with two fixture hooks: changing the second's format stores discord
// there and leaves the first as it was.
func TestWebhooksEditChangesSecondFormat(t *testing.T) {
	fa := &fakeActions{doc: webhooksDocFrom(t, webhooksFixtureJSON)}
	env := candActionEnv(fa, view.Report{})
	wv := webhooksViewFrom(t, fa, env)
	next, _ := wv.Update(tea.KeyMsg{Type: tea.KeyDown}, env)
	wv = next.(webhooksView)

	f := openWebhookForm(t, wv, key('e'), env)
	if f.title != "edit webhook" || f.editing != 1 {
		t.Fatalf("form = %q editing %d, want edit webhook on the second", f.title, f.editing)
	}
	f = f.setFocus(1) // format
	for range 2 {
		nextF, _, _ := f.update(tea.KeyMsg{Type: tea.KeyRight}) // json -> slack -> discord
		f = nextF.(webhookForm)
	}

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	hooks := webhookStoredHooks(t, fa.configEdits[0])
	if len(hooks) != 2 {
		t.Fatalf("webhooks = %v, want two", hooks)
	}
	second := hooks[1].(map[string]any)
	if second["format"] != "discord" {
		t.Errorf("webhooks[1].format = %v, want discord", second["format"])
	}
	first := hooks[0].(map[string]any)
	if first["format"] != "slack" || first["url"] != "https://hooks.example.com/relevo" {
		t.Errorf("webhooks[0] = %v, want the first hook unchanged", first)
	}
}

// c. d, then y, on the only hook: the body has no notify key.
func TestWebhooksDeleteRemovesNotify(t *testing.T) {
	fa := &fakeActions{doc: webhooksDocFrom(t, webhookSingleJSON)}
	env := candActionEnv(fa, view.Report{})
	wv := webhooksViewFrom(t, fa, env)

	_, cmd := wv.Update(key('d'), env)
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
	if !box.danger {
		t.Error("the delete confirm must be a danger box")
	}

	_, yes, closed := box.update(key('y'))
	if !closed {
		t.Fatal("y must close the confirm")
	}
	runCmd(t, yes)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	m := webhookEditBody(t, fa.configEdits[0])
	if _, exists := m["notify"]; exists {
		t.Errorf("notify still present: %v", m["notify"])
	}
}

// d. Type nope into events: the form shows the unknown-event error and enter
// records no edit.
func TestWebhookFormUnknownEventStaysOpen(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	wv := webhooksViewFrom(t, fa, env)

	f := openWebhookForm(t, wv, key('a'), env)
	f = webhookFormType(f, "https://example.com/hook")
	f = f.setFocus(2) // events
	f = webhookFormType(f, "nope")

	view := stripANSI(strings.Join(f.view(90), "\n"))
	if !strings.Contains(view, "unknown event nope") {
		t.Errorf("view = %s, want the unknown-event error", view)
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

// e. state_changed:needs_you, builder_stalled is accepted and stored as two
// events.
func TestWebhookFormStoresStateSuffixAndEvent(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	wv := webhooksViewFrom(t, fa, env)

	f := openWebhookForm(t, wv, key('a'), env)
	f = webhookFormType(f, "https://example.com/hook")
	f = f.setFocus(2) // events
	f = webhookFormType(f, "state_changed:needs_you, builder_stalled")

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("a valid submit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	hooks := webhookStoredHooks(t, fa.configEdits[0])
	evs, ok := hooks[0].(map[string]any)["events"].([]any)
	if !ok || len(evs) != 2 {
		t.Fatalf("events = %v, want two", hooks[0])
	}
	if evs[0] != "state_changed:needs_you" || evs[1] != "builder_stalled" {
		t.Errorf("events = %v, want the two typed", evs)
	}
}

// f. enter on scan_patterns shows the reworded notice.
func TestSettingsScanPatternsEditorNotice(t *testing.T) {
	fa := &fakeActions{doc: settingsFixtureDoc(t)}
	env := candActionEnv(fa, view.Report{})
	v := settingsFixtureView(t, fa)
	v.cur = 15 // scan_patterns

	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	if cmd == nil {
		t.Fatal("enter must return a command")
	}
	n, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("enter gave %T, want a notice", cmd())
	}
	if want := "scan_patterns is edited with relevo config set policy"; n.text != want {
		t.Errorf("notice = %q, want %q", n.text, want)
	}
}
