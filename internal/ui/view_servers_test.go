package ui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/view"
)

// serversSectionJSON is the servers fixture as the store holds it: two
// machines, one pinned by fingerprint and one trusting the system CAs.
const serversSectionJSON = `{
  "backup": {
    "url": "https://backup:7777",
    "fingerprint": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  },
  "zen": {
    "url": "https://zen:7777",
    "fingerprint": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }
}
`

// serversFixtureDoc seeds a store with the servers fixture and loads the doc
// the view reads, so these tests pin the config layer's servers read end to
// end.
func serversFixtureDoc(t *testing.T) relevo.ConfigDoc {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	st := config.Open(d)
	if _, err := st.Put(config.Servers, []byte(serversSectionJSON)); err != nil {
		t.Fatalf("Put(servers): %v", err)
	}
	doc, err := relevo.LoadConfigDoc(st)
	if err != nil {
		t.Fatalf("LoadConfigDoc: %v", err)
	}
	return doc
}

// serversCannedProbes is a probe reply for the fixture: zen answered as
// laptop, backup has no client key.
func serversCannedProbes() []relevo.ServerProbe {
	return []relevo.ServerProbe{
		{Name: "backup", URL: "https://backup:7777", State: "no key", Detail: "run relevo config server key"},
		{Name: "zen", URL: "https://zen:7777", State: "enrolled", Label: "laptop"},
	}
}

// serversFixtureView loads a serversView over fa's doc and drains the doc load
// and the probe it returns, as the ':servers' command's load would.
func serversFixtureView(t *testing.T, fa *fakeActions) serversView {
	t.Helper()
	env := candActionEnv(fa, view.Report{})
	v, cmd := newServersView(env)
	next, probe := v.Update(cmd(), env)
	out := next.(serversView)
	if probe == nil {
		t.Fatal("the doc load must return the probe command")
	}
	next, _ = out.Update(probe(), env)
	return next.(serversView)
}

// The rows render each server's name, url and probe state, with the stored
// fingerprint abbreviated.
func TestServersRowsRenderNameURLStateAndPin(t *testing.T) {
	fa := &fakeActions{doc: serversFixtureDoc(t), serverProbes: serversCannedProbes()}
	v := serversFixtureView(t, fa)
	env := candActionEnv(fa, view.Report{})

	rows := v.rows()
	if len(rows) != 2 || rows[0].name != "backup" || rows[1].name != "zen" {
		t.Fatalf("rows = %+v, want backup then zen", rows)
	}
	if rows[1].url != "https://zen:7777" || rows[1].state != "enrolled as laptop" {
		t.Errorf("zen row = %+v", rows[1])
	}
	if want := "sha256:01234567…"; rows[0].fingerprint != want {
		t.Errorf("backup fingerprint = %q, want %q", rows[0].fingerprint, want)
	}

	body := stripANSI(v.Body(env, 132, 34))
	for _, want := range []string{"SERVER", "URL", "FINGERPRINT", "STATE", "backup", "https://backup:7777", "no key", "zen", "enrolled as laptop"} {
		if !strings.Contains(body, want) {
			t.Errorf("the table must contain %q:\n%s", want, body)
		}
	}
}

// The doc load leaves the rows on the placeholder and hands back the probe as
// a command: nothing is probed while the frame is drawn.
func TestServersPlaceholderUntilTheProbeAnswers(t *testing.T) {
	fa := &fakeActions{doc: serversFixtureDoc(t), serverProbes: serversCannedProbes()}
	env := candActionEnv(fa, view.Report{})

	v, cmd := newServersView(env)
	if cmd == nil {
		t.Fatal("newServersView must return the doc load command")
	}
	next, probe := v.Update(cmd(), env)
	before := next.(serversView)
	if probe == nil {
		t.Fatal("the doc message must return the probe command")
	}
	if fa.serverProbeCalls != 0 {
		t.Fatalf("the doc message probed inline, calls = %d", fa.serverProbeCalls)
	}
	body := stripANSI(before.Body(env, 132, 34))
	if !strings.Contains(body, "—") {
		t.Errorf("a row with no probe yet must show the placeholder:\n%s", body)
	}
	if strings.Contains(body, "enrolled") {
		t.Errorf("no state may be filled in before the probe answers:\n%s", body)
	}

	next, _ = before.Update(probe(), env)
	after := next.(serversView)
	if fa.serverProbeCalls != 1 {
		t.Fatalf("serverProbeCalls = %d, want 1", fa.serverProbeCalls)
	}
	if got := stripANSI(after.Body(env, 132, 34)); !strings.Contains(got, "enrolled as laptop") {
		t.Errorf("the probe's state must render after its message:\n%s", got)
	}
}

// An empty servers section renders its text, not an error.
func TestServersNoServersRendersAsText(t *testing.T) {
	fa := &fakeActions{}
	env := candActionEnv(fa, view.Report{})
	v := serversFixtureView(t, fa)
	body := stripANSI(v.Body(env, 132, 34))
	if !strings.Contains(body, "no servers configured") {
		t.Errorf("an empty section must render its text:\n%s", body)
	}
}

// A server with no client key renders as text, and the context line counts it
// not enrolled.
func TestServersNoKeyRendersAsText(t *testing.T) {
	fa := &fakeActions{doc: serversFixtureDoc(t)}
	fa.serverProbes = []relevo.ServerProbe{
		{Name: "backup", URL: "https://backup:7777", State: "no key", Detail: "run relevo config server key"},
		{Name: "zen", URL: "https://zen:7777", State: "no key", Detail: "run relevo config server key"},
	}
	v := serversFixtureView(t, fa)
	env := candActionEnv(fa, view.Report{})

	body := stripANSI(v.Body(env, 132, 34))
	if !strings.Contains(body, "no key") {
		t.Errorf("a no-key probe must render as text:\n%s", body)
	}
	left, _ := v.Context(env)
	if got := stripANSI(left); !strings.Contains(got, "2 servers") || !strings.Contains(got, "0 enrolled") {
		t.Errorf("context = %q, want two servers, none enrolled", got)
	}
}

// r re-probes on demand, off the update loop.
func TestServersReprobeKeyAsksAgain(t *testing.T) {
	fa := &fakeActions{doc: serversFixtureDoc(t), serverProbes: serversCannedProbes()}
	v := serversFixtureView(t, fa)
	if fa.serverProbeCalls != 1 {
		t.Fatalf("the load probed %d times, want 1", fa.serverProbeCalls)
	}

	env := candActionEnv(fa, view.Report{})
	_, cmd := v.Update(key('r'), env)
	if cmd == nil {
		t.Fatal("r must return a probe command")
	}
	if fa.serverProbeCalls != 1 {
		t.Fatalf("r must probe off the update loop, calls = %d", fa.serverProbeCalls)
	}
	if _, ok := cmd().(serversProbeMsg); !ok {
		t.Fatalf("r must return a probe message")
	}
	if fa.serverProbeCalls != 2 {
		t.Errorf("serverProbeCalls = %d, want 2 after r", fa.serverProbeCalls)
	}
}

// a opens the add form, whose valid submit applies one edit carrying the
// servers section alone.
func TestServersAddSubmitsTheServersSectionAlone(t *testing.T) {
	fa := &fakeActions{doc: serversFixtureDoc(t)}
	v := serversFixtureView(t, fa)

	_, cmd := v.Update(key('a'), candActionEnv(fa, view.Report{}))
	if cmd == nil {
		t.Fatal("a must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("a gave %T, want an overlay", cmd())
	}
	box, ok := open.ov.(formBox)
	if !ok {
		t.Fatalf("a opened %T, want a formBox", open.ov)
	}
	if box.kind != "add server" {
		t.Fatalf("form = %q, want add server", box.kind)
	}

	box.fields[0].input.SetValue("laptop2")
	box.fields[1].input.SetValue("https://laptop2:7777")
	box.fields[2].input.SetValue("sha256:cccccccccccccccc")
	_, submit, closed := box.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatalf("a valid add must close the form, err = %q", box.err)
	}
	runCmd(t, submit)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	got := fa.configEdits[0]
	if got.Message != "add server laptop2" || got.Name != "laptop2" {
		t.Errorf("edit = {message: %q, name: %q}", got.Message, got.Name)
	}
	if len(got.Sections) != 1 {
		t.Fatalf("sections = %v, want only servers", got.Sections)
	}
	var served map[string]remote.ServerEntry
	if err := json.Unmarshal(got.Sections[config.Servers], &served); err != nil {
		t.Fatalf("decode servers: %v", err)
	}
	if served["laptop2"].URL != "https://laptop2:7777" {
		t.Errorf("stored entry = %+v", served["laptop2"])
	}
}

// An invalid url keeps the add form open on its field.
func TestServersAddRefusalKeepsTheFormOpen(t *testing.T) {
	fa := &fakeActions{doc: serversFixtureDoc(t)}
	v := serversFixtureView(t, fa)

	_, cmd := v.Update(key('a'), candActionEnv(fa, view.Report{}))
	box := cmd().(openOverlayMsg).ov.(formBox)
	box.fields[0].input.SetValue("laptop2")
	box.fields[1].input.SetValue("http://laptop2:7777")
	next, _, closed := box.update(tea.KeyMsg{Type: tea.KeyEnter})
	if closed {
		t.Fatal("an invalid url must keep the form open")
	}
	kept, ok := next.(formBox)
	if !ok || kept.err == "" {
		t.Errorf("an invalid url must show the error, got %q", box.err)
	}
	if len(fa.configEdits) != 0 {
		t.Errorf("a refused add must write nothing, edits = %v", fa.configEdits)
	}
}

// e opens the edit form prefilled from the cursor row, and a valid submit
// applies an edit for that server.
func TestServersEditPrefillsAndSaves(t *testing.T) {
	fa := &fakeActions{doc: serversFixtureDoc(t)}
	v := serversFixtureView(t, fa)

	_, cmd := v.Update(key('e'), candActionEnv(fa, view.Report{}))
	if cmd == nil {
		t.Fatal("e must return a command")
	}
	open, ok := cmd().(openOverlayMsg)
	if !ok {
		t.Fatalf("e gave %T, want an overlay", cmd())
	}
	box, ok := open.ov.(formBox)
	if !ok {
		t.Fatalf("e opened %T, want a formBox", open.ov)
	}
	if len(box.fields) != 4 {
		t.Fatalf("edit fields = %d, want url, fingerprint, ca, insecure", len(box.fields))
	}
	if got := box.fields[0].input.Value(); got != "https://backup:7777" {
		t.Errorf("url prefill = %q", got)
	}
	if got := box.fields[3].input.Value(); got != "no" {
		t.Errorf("insecure prefill = %q", got)
	}

	box.fields[0].input.SetValue("https://backup:8888")
	_, submit, closed := box.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatalf("a valid edit must close the form, err = %q", box.err)
	}
	runCmd(t, submit)

	if len(fa.configEdits) != 1 || fa.configEdits[0].Message != "edit server backup" {
		t.Fatalf("configEdits = %+v, want one edit of backup", fa.configEdits)
	}
}

// d confirms before it deletes, then applies the delete of the cursor row.
func TestServersDeleteConfirms(t *testing.T) {
	fa := &fakeActions{doc: serversFixtureDoc(t)}
	v := serversFixtureView(t, fa)
	v.cur = 1 // zen

	_, cmd := v.Update(key('d'), candActionEnv(fa, view.Report{}))
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

	if len(fa.configEdits) != 1 || fa.configEdits[0].Message != "delete server zen" {
		t.Fatalf("configEdits = %+v, want one delete of zen", fa.configEdits)
	}
}

// d on a server an actor's placement names is a notice naming that actor, and
// writes nothing.
func TestServersDeleteRefusedNamesTheActor(t *testing.T) {
	doc := serversFixtureDoc(t)
	doc.Actors = map[string]roles.Actor{"builder": {Agent: "plan-executor", Placement: []string{"zen"}}}
	fa := &fakeActions{doc: doc}
	v := serversFixtureView(t, fa)
	v.cur = 1 // zen

	_, cmd := v.Update(key('d'), candActionEnv(fa, view.Report{}))
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	notice, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("d on a placed server gave %T, want a notice", cmd())
	}
	if !strings.Contains(notice.text, "builder") {
		t.Errorf("notice = %q, want it to name the actor", notice.text)
	}
	if len(fa.configEdits) != 0 {
		t.Errorf("a refused delete must write nothing, edits = %v", fa.configEdits)
	}
}

// Without an Actions seam the edit and probe keys are hidden and inert.
func TestServersKeysNeedActions(t *testing.T) {
	v := serversView{}
	if keys := v.Keys(); len(keys) != 0 {
		t.Errorf("Keys = %v, want none without Actions", keys)
	}
	for _, r := range []rune{'r', 'a', 'e', 'd'} {
		_, cmd := v.Update(key(r), Env{})
		if cmd != nil {
			t.Errorf("%q must do nothing without Actions", r)
		}
	}
}

// :servers without an Actions seam is a notice, as the other config views are.
func TestServersNeedsActions(t *testing.T) {
	env := Env{Ctx: context.Background(), Loaded: true, Now: railNow, Width: 132, Height: 34}
	cmd := execLine("servers", env, prefs{})
	if cmd == nil {
		t.Fatal(":servers with no Actions must return a notice")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf(":servers with no Actions gave %T, want a notice", cmd())
	}
	if !strings.Contains(msg.text, "need relevo ui") {
		t.Errorf("notice = %q", msg.text)
	}
}
