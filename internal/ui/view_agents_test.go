package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/view"
)

// agentsFixtureList loads an agentsView over fa's doc and files, as the
// ':agents' command's load would.
func agentsFixtureList(t *testing.T, fa *fakeActions) agentsView {
	t.Helper()
	env := candActionEnv(fa, view.Report{})
	v, cmd := newAgentsView(env)
	next, _ := v.Update(cmd(), env)
	return next.(agentsView)
}

// agentRowIndex is the table index of the agent named name.
func agentRowIndex(t *testing.T, rows []agentRow, name string) int {
	t.Helper()
	for i, r := range rows {
		if r.name == name {
			return i
		}
	}
	t.Fatalf("no agent row %q", name)
	return -1
}

// agentFixtureView is the pushed detail view over fa's named agent.
func agentFixtureView(t *testing.T, fa *fakeActions, name string) agentView {
	t.Helper()
	rows := agentRows(fa.doc, fa.files)
	for _, r := range rows {
		if r.name == name {
			v, _ := newAgentView(candActionEnv(fa, view.Report{}), fa.doc, r)
			return v.(agentView)
		}
	}
	t.Fatalf("no agent row %q", name)
	return agentView{}
}

// agentDocCustom is the fixture doc with one custom agent added.
func agentDocCustom(t *testing.T, name, shape string, native map[string]roles.DefRow) relevo.ConfigDoc {
	t.Helper()
	doc := candFixtureDoc(t)
	doc.Agents[name] = roles.AgentEntry{Shape: shape, Native: native}
	return doc
}

// securityReviewerSource is a valid reader source with no kind list, so it
// renders to every kind relevo knows.
const securityReviewerSource = "---\n" +
	"name: security-reviewer\n" +
	"description: Reviews a change for security problems.\n" +
	"shape: reader\n" +
	"output: review\n" +
	"requires: []\n" +
	"kinds: []\n" +
	"---\n\n" +
	"Review the change for security problems.\n"

// agentDocSource is the fixture doc with security-reviewer added as a source
// agent, so the source-custom cases have one relevo renders files for.
func agentDocSource(t *testing.T) relevo.ConfigDoc {
	t.Helper()
	doc := candFixtureDoc(t)
	doc.Agents["security-reviewer"] = roles.AgentEntry{Shape: "reader", Source: securityReviewerSource}
	return doc
}

// sourceAgentFiles is security-reviewer's rendered files, in harness.All()
// order, at each kind's convention path under the fixture's home and in state.
func sourceAgentFiles(t *testing.T, state harness.FileState) []harness.AgentFile {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	var files []harness.AgentFile
	for _, h := range harness.All() {
		rel, ok := harness.DefinitionPath(h.Kind, "security-reviewer")
		if !ok {
			t.Fatalf("%s has no security-reviewer definition path", h.Kind)
		}
		files = append(files, harness.AgentFile{Kind: h.Kind, Path: filepath.Join(home, rel), State: state})
	}
	return files
}

// sourceAgentFixture is the agents goldens' Actions: the installed shipped
// agents plus security-reviewer, whose rendered files are all up to date.
func sourceAgentFixture(t *testing.T) *fakeActions {
	t.Helper()
	fa := &fakeActions{doc: agentDocSource(t), files: agentFileFixtures(t)}
	fa.files["security-reviewer"] = sourceAgentFiles(t, harness.FileUpToDate)
	return fa
}

// usedBy for researcher is builder (plan-executor requires it) and researcher
// (its own actor), sorted (§3, §5).
func TestAgentsUsedByResearcher(t *testing.T) {
	v := agentsFixtureList(t, &fakeActions{doc: candFixtureDoc(t)})
	rows := v.rows()
	r := rows[agentRowIndex(t, rows, "researcher")]
	if got := strings.Join(r.usedBy, " "); got != "builder researcher" {
		t.Errorf("researcher usedBy = %q, want builder researcher", got)
	}
}

// The architect row is faint and its USED BY reads no actor: no actor runs it
// (§4).
func TestAgentsArchitectRowFaint(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := agentsFixtureList(t, fa)
	env := candActionEnv(fa, view.Report{})
	rows := v.rows()
	architect := rows[agentRowIndex(t, rows, "architect")]
	if len(architect.usedBy) != 0 {
		t.Fatalf("architect usedBy = %v, want none", architect.usedBy)
	}

	nameW, cols := agentListLayout(env.Width, agentKinds(fa.files))
	cw := env.Width - 6
	line := agentDataLine(architect, false, nameW, cols, cw)
	if !strings.Contains(line, faintStyle.Render(pad("architect", nameW))) {
		t.Errorf("the architect row must be faint: %q", line)
	}
	if !strings.Contains(stripANSI(line), "no actor") {
		t.Errorf("USED BY = %q, want no actor", stripANSI(line))
	}

	// A used row's name is not faint, so the assertion is about the row.
	builder := rows[agentRowIndex(t, rows, "plan-executor")]
	used := agentDataLine(builder, false, nameW, cols, cw)
	if !strings.Contains(used, textStyle.Render(pad("plan-executor", nameW))) {
		t.Errorf("a used row must not be faint: %q", used)
	}
}

// A used row's USED BY names its actor, and a custom agent's kinds are all `·`
// (§4).
func TestAgentsRowCells(t *testing.T) {
	fa := &fakeActions{
		doc:   agentDocCustom(t, "scout", "reader", map[string]roles.DefRow{"opencode": {Agent: "scout"}}),
		files: agentFileFixtures(t),
	}
	v := agentsFixtureList(t, fa)
	rows := v.rows()

	researcher := rows[agentRowIndex(t, rows, "researcher")]
	if got := agentUsedCell(researcher); got != "builder, researcher" {
		t.Errorf("researcher USED BY = %q", got)
	}
	if text, style := agentStateCell(researcher, "claude"); text != "ok" || style.GetForeground() != greenStyle.GetForeground() {
		t.Errorf("researcher on claude = %q %v, want ok green", text, style)
	}

	scout := rows[agentRowIndex(t, rows, "scout")]
	if scout.source != "custom" || scout.shape != "reader" {
		t.Errorf("scout row = %+v, want a custom reader", scout)
	}
	for _, kind := range agentKinds(fa.files) {
		if text, style := agentStateCell(scout, kind); text != "·" || style.GetForeground() != faintStyle.GetForeground() {
			t.Errorf("scout on %s = %q %v, want a faint ·", kind, text, style)
		}
	}
}

// d on a shipped agent is a notice: it is greyed on the list because it
// cannot be deleted (§4, §5).
func TestAgentsDeleteShippedNotices(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t)}
	v := agentsFixtureList(t, fa)
	v.cur = agentRowIndex(t, v.rows(), "researcher")

	_, cmd := v.Update(key('d'), candActionEnv(fa, view.Report{}))
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	msg := cmd()
	notice, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("d on researcher gave %T, want a notice", msg)
	}
	if notice.text != "researcher ships with relevo; it can't be deleted" {
		t.Errorf("notice = %q", notice.text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused delete must not write an edit")
	}
}

// d on an unused custom agent confirms, then deletes it from the config alone
// (§4, §5).
func TestAgentsDeleteCustomAgentConfirms(t *testing.T) {
	fa := &fakeActions{doc: agentDocCustom(t, "scout", "reader", map[string]roles.DefRow{"opencode": {Agent: "scout"}})}
	v := agentsFixtureList(t, fa)
	v.cur = agentRowIndex(t, v.rows(), "scout")

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

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	if got := fa.configEdits[0].Message; got != "delete agent scout" {
		t.Errorf("edit message = %q", got)
	}
}

// d on a custom agent an actor runs is relevo.DeleteAgent's refusal, shown as
// a notice (§4, §5).
func TestAgentsDeleteUsedCustomAgentNotices(t *testing.T) {
	doc := agentDocCustom(t, "scout", "reader", map[string]roles.DefRow{"opencode": {Agent: "scout"}})
	doc.Actors["tinkerer"] = roles.Actor{Agent: "scout"}
	fa := &fakeActions{doc: doc}
	v := agentsFixtureList(t, fa)
	v.cur = agentRowIndex(t, v.rows(), "scout")

	_, cmd := v.Update(key('d'), candActionEnv(fa, view.Report{}))
	if cmd == nil {
		t.Fatal("d must return a command")
	}
	msg := cmd()
	notice, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("d on a used custom agent gave %T, want a notice", msg)
	}
	if !strings.Contains(notice.text, "used by ") {
		t.Errorf("notice = %q, want it to name the actor", notice.text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("a refused delete must not write an edit")
	}
}

// r on an up-to-date file is a notice, and asks for no reset (§4, §5).
func TestAgentResetUpToDateNotices(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t), files: agentFileFixtures(t)}
	v := agentFixtureView(t, fa, "researcher")
	v.cur = 1 // claude

	_, cmd := v.Update(key('r'), candActionEnv(fa, view.Report{}))
	if cmd == nil {
		t.Fatal("r must return a command")
	}
	msg := cmd()
	notice, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("r on an up-to-date file gave %T, want a notice", msg)
	}
	if notice.text != "claude's researcher is up to date" {
		t.Errorf("notice = %q", notice.text)
	}
	if len(fa.resets) != 0 {
		t.Errorf("resets = %v, want none", fa.resets)
	}
}

// r on a file the user edited confirms, then resets exactly that (kind,
// agent) pair (§4, §5).
func TestAgentResetEditedConfirms(t *testing.T) {
	files := agentFileFixtures(t)
	for i := range files["researcher"] {
		if files["researcher"][i].Kind == "claude" {
			files["researcher"][i].State = harness.FileEdited
		}
	}
	fa := &fakeActions{doc: candFixtureDoc(t), files: files, result: Result{Text: "reset claude's researcher", Refresh: true}}
	v := agentFixtureView(t, fa, "researcher")
	v.cur = 1 // claude

	_, cmd := v.Update(key('r'), candActionEnv(fa, view.Report{}))
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
	if !box.danger || box.kind != "reset" {
		t.Errorf("reset confirm = kind %q danger %v, want reset danger", box.kind, box.danger)
	}
	if want := "Reset claude's " + accentStyle.Bold(true).Render("researcher") + "?"; box.title != want {
		t.Errorf("reset title = %q, want %q", box.title, want)
	}
	if got := strings.Join(box.lines, "\n"); !strings.Contains(got, "your edit is lost") {
		t.Errorf("reset confirm must say the edit is lost:\n%s", got)
	}

	_, yes, closed := box.update(key('y'))
	if !closed {
		t.Fatal("y must close the confirm")
	}
	runCmd(t, yes)

	if len(fa.resets) != 1 || fa.resets[0] != [2]string{"claude", "researcher"} {
		t.Fatalf("resets = %v, want one [claude researcher]", fa.resets)
	}
}

// r on a file whose edit sits on an older shipped copy confirms, and its note
// says the newer copy is what lands (§4).
func TestAgentResetOnEditNewerSaysNewerCopy(t *testing.T) {
	files := agentFileFixtures(t)
	for i := range files["researcher"] {
		if files["researcher"][i].Kind == "claude" {
			files["researcher"][i].State = harness.FileEditedNewer
		}
	}
	fa := &fakeActions{doc: candFixtureDoc(t), files: files, result: Result{Text: "reset claude's researcher", Refresh: true}}
	v := agentFixtureView(t, fa, "researcher")
	v.cur = 1 // claude

	_, cmd := v.Update(key('r'), candActionEnv(fa, view.Report{}))
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
	if got := strings.Join(box.lines, "\n"); !strings.Contains(got, "with the newer copy this relevo ships; your edit is lost") {
		t.Errorf("reset confirm must say the newer copy lands:\n%s", got)
	}
}

// A shipped agent's four reset notes keep their exact wording, whether the file
// is edited, stale, edit + newer, or missing (§4).
func TestAgentViewShippedResetWordingUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		state harness.FileState
		want  string
	}{
		{"edited", harness.FileEdited, "with the copy this relevo ships; your edit is lost"},
		{"stale", harness.FileStale, "with the copy this relevo ships; relevo has a newer copy"},
		{"edit + newer", harness.FileEditedNewer, "with the newer copy this relevo ships; your edit is lost"},
		{"missing", harness.FileMissing, "the copy this relevo ships"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := agentFileFixtures(t)
			for i := range files["researcher"] {
				if files["researcher"][i].Kind == "claude" {
					files["researcher"][i].State = tc.state
				}
			}
			fa := &fakeActions{doc: candFixtureDoc(t), files: files}
			v := agentFixtureView(t, fa, "researcher")
			v.cur = 1 // claude

			_, cmd := v.Update(key('r'), candActionEnv(fa, view.Report{}))
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
			if want := pad("", 11) + tc.want; box.lines[1] != want {
				t.Errorf("reset note = %q, want %q", box.lines[1], want)
			}
		})
	}
}

// r on a file relevo has never written confirms a write, then writes exactly
// that (kind, agent) pair (§3, round 6).
func TestAgentResetMissingConfirmsWrite(t *testing.T) {
	files := agentFileFixtures(t)
	for i := range files["researcher"] {
		if files["researcher"][i].Kind == "claude" {
			files["researcher"][i].State = harness.FileMissing
		}
	}
	fa := &fakeActions{doc: candFixtureDoc(t), files: files, result: Result{Text: "write claude's researcher", Refresh: true}}
	v := agentFixtureView(t, fa, "researcher")
	v.cur = 1 // claude

	_, cmd := v.Update(key('r'), candActionEnv(fa, view.Report{}))
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
	if want := "Write claude's " + accentStyle.Bold(true).Render("researcher") + "?"; box.title != want {
		t.Errorf("write title = %q, want %q", box.title, want)
	}
	got := strings.Join(box.lines, "\n")
	if !strings.Contains(got, "writes") {
		t.Errorf("write confirm must label the action writes:\n%s", got)
	}
	if strings.Contains(got, "overwrites") {
		t.Errorf("a write must not say overwrites:\n%s", got)
	}
	if !strings.Contains(got, "the copy this relevo ships") {
		t.Errorf("write confirm must name the copy:\n%s", got)
	}

	_, yes, closed := box.update(key('y'))
	if !closed {
		t.Fatal("y must close the confirm")
	}
	runCmd(t, yes)

	if len(fa.resets) != 1 || fa.resets[0] != [2]string{"claude", "researcher"} {
		t.Fatalf("resets = %v, want one [claude researcher]", fa.resets)
	}
}

// A native custom agent's view has no r, and its rows come from the native
// entry: relevo writes no file for it, so there is nothing to reset.
func TestAgentViewCustomHasNoReset(t *testing.T) {
	fa := &fakeActions{doc: agentDocCustom(t, "scout", "reader", map[string]roles.DefRow{"opencode": {Agent: "my-scout"}})}
	v := agentFixtureView(t, fa, "scout")

	for _, kh := range v.Keys() {
		if kh.Key == "r" {
			t.Error("a custom agent's view must not offer r")
		}
	}
	rows := v.rows()
	if len(rows) != 1 || rows[0].kind != "opencode" {
		t.Fatalf("custom rows = %+v, want one opencode row", rows)
	}
	if rel, ok := harness.DefinitionPath("opencode", "my-scout"); !ok || !strings.HasSuffix(rows[0].path, rel) {
		t.Errorf("custom row path = %q, want it to end in %q", rows[0].path, rel)
	}
}

// A source custom agent's view offers r, and r on an edited file confirms a
// reset that names the copy relevo renders from the user's config. Confirming
// resets exactly that (kind, agent) pair.
func TestAgentViewSourceCustomAgentResets(t *testing.T) {
	fa := sourceAgentFixture(t)
	for i := range fa.files["security-reviewer"] {
		if fa.files["security-reviewer"][i].Kind == "claude" {
			fa.files["security-reviewer"][i].State = harness.FileEdited
		}
	}
	v := agentFixtureView(t, fa, "security-reviewer")
	v.cur = 1 // claude

	hasReset := false
	for _, kh := range v.Keys() {
		if kh.Key == "r" {
			hasReset = true
		}
	}
	if !hasReset {
		t.Error("a source custom agent's view must offer r")
	}

	_, cmd := v.Update(key('r'), candActionEnv(fa, view.Report{}))
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
	if !box.danger || box.kind != "reset" {
		t.Errorf("reset confirm = kind %q danger %v, want reset danger", box.kind, box.danger)
	}
	if want := pad("", 11) + "with the copy relevo renders from your config; your edit is lost"; box.lines[1] != want {
		t.Errorf("a source agent's reset note = %q, want %q", box.lines[1], want)
	}

	_, yes, closed := box.update(key('y'))
	if !closed {
		t.Fatal("y must close the confirm")
	}
	runCmd(t, yes)

	if len(fa.resets) != 1 || fa.resets[0] != [2]string{"claude", "security-reviewer"} {
		t.Fatalf("resets = %v, want one [claude security-reviewer]", fa.resets)
	}
}

// The detail block of a source custom agent lists one line per rendered file
// with its state, and says nothing about relevo not installing custom agents.
func TestAgentsDetailListsASourceCustomAgentsFiles(t *testing.T) {
	fa := sourceAgentFixture(t)
	v := agentsFixtureList(t, fa)
	rows := v.rows()
	r := rows[agentRowIndex(t, rows, "security-reviewer")]
	if len(r.files) != len(harness.All()) {
		t.Fatalf("security-reviewer files = %d, want one per kind", len(r.files))
	}

	lines := agentDetailLines(fa.doc, r, 132)
	got := strings.Join(lines, "\n")
	if strings.Contains(got, "does not install") {
		t.Errorf("a source agent must not say relevo cannot install it:\n%s", got)
	}
	if len(lines) != 1+len(r.files) {
		t.Errorf("detail has %d lines, want one per file plus the header", len(lines))
	}
	for _, f := range r.files {
		if !strings.Contains(got, tildePath(f.Path)) {
			t.Errorf("the detail must list %s's file %s:\n%s", f.Kind, tildePath(f.Path), got)
		}
		if !strings.Contains(stripANSI(got), string(f.State)) {
			t.Errorf("the detail must show %s's state %q:\n%s", f.Kind, f.State, got)
		}
	}
}

// e opens the user's editor on the cursor file's path (§4, §5).
func TestAgentEditOpensEditor(t *testing.T) {
	fa := &fakeActions{doc: candFixtureDoc(t), files: agentFileFixtures(t)}
	v := agentFixtureView(t, fa, "researcher")
	v.cur = 1 // claude

	_, cmd := v.Update(key('e'), candActionEnv(fa, view.Report{}))
	if cmd == nil {
		t.Fatal("e must return a command")
	}
	if len(fa.edited) != 1 {
		t.Fatalf("edited = %v, want one path", fa.edited)
	}
	rows := v.rows()
	if fa.edited[0] != rows[1].path {
		t.Errorf("edited = %q, want the claude file %q", fa.edited[0], rows[1].path)
	}
	_ = cmd()
}

// e on a file that is not on disk is a notice: there is nothing to open (§4).
func TestAgentEditMissingNotices(t *testing.T) {
	files := agentFileFixtures(t)
	for i := range files["researcher"] {
		if files["researcher"][i].Kind == "claude" {
			files["researcher"][i].State = harness.FileMissing
		}
	}
	fa := &fakeActions{doc: candFixtureDoc(t), files: files}
	v := agentFixtureView(t, fa, "researcher")
	v.cur = 1 // claude

	_, cmd := v.Update(key('e'), candActionEnv(fa, view.Report{}))
	if cmd == nil {
		t.Fatal("e must return a command")
	}
	msg := cmd()
	notice, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("e on a missing file gave %T, want a notice", msg)
	}
	if notice.text != "claude has no researcher file yet" {
		t.Errorf("notice = %q", notice.text)
	}
	if len(fa.edited) != 0 {
		t.Errorf("a missing file must open nothing, edited = %v", fa.edited)
	}
}

// :agents without an Actions seam is a notice, as :candidates and :actors are
// (§4).
func TestAgentsNeedsActions(t *testing.T) {
	env := Env{Ctx: context.Background(), Loaded: true, Now: railNow, Width: 132, Height: 34}
	cmd := execLine("agents", env, prefs{})
	if cmd == nil {
		t.Fatal(":agents with no Actions must return a notice")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf(":agents with no Actions gave %T, want a notice", cmd())
	}
	if !strings.Contains(msg.text, "need relevo ui") {
		t.Errorf("notice = %q", msg.text)
	}
}
