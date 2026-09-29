package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// auditFixtureShell is a loaded shell with ':audit' open and the cursor on
// revision 4 (the list opens on revision 5, so one down), every load drained.
func auditFixtureShell(t *testing.T, fa *fakeActions) Model {
	t.Helper()
	m := goldenAuditModel(t, 132, 34, fa)
	return candKeys(t, m, tea.KeyMsg{Type: tea.KeyDown})
}

// auditViewOf is the shell's top view as an auditView.
func auditViewOf(t *testing.T, m Model) auditView {
	t.Helper()
	v, ok := m.top().(auditView)
	if !ok {
		t.Fatalf("top view is %T, want an auditView", m.top())
	}
	return v
}

// R confirms with the changes it undoes, then y rolls back to exactly the
// cursor revision.
func TestAuditRollbackConfirmsThenCalls(t *testing.T) {
	fa := auditFixtureFake()
	fa.preview = []relevo.ChangeLine{{Op: "-", Subject: "actor planner", Before: "agent architect"}}
	fa.result = Result{Text: "rolled back to #4 as #6", Refresh: true}
	m := auditFixtureShell(t, fa)

	res, cmd := m.Update(key('R'))
	m = drain(t, res.(Model), cmd)
	if m.overlay == nil {
		t.Fatal("R must open the roll back confirm")
	}
	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("R must open a confirmBox, got %T", m.overlay)
	}
	if !box.danger || box.kind != "roll back" {
		t.Errorf("confirm = kind %q danger %v, want roll back danger", box.kind, box.danger)
	}
	if want := "Roll back to " + accentStyle.Bold(true).Render("#4") + "?"; box.title != want {
		t.Errorf("title = %q, want %q", box.title, want)
	}
	got := stripANSI(strings.Join(box.lines, "\n"))
	for _, want := range []string{
		"undoes     - actor planner   agent architect",
		"saves as   #6 · rollback",
		"kept       #5 and every revision before it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("confirm lines must contain %q:\n%s", want, got)
		}
	}
	if len(fa.rollbacks) != 0 {
		t.Errorf("opening the confirm must not roll back, got %v", fa.rollbacks)
	}

	res, cmd = m.Update(key('y'))
	m = res.(Model)
	if m.overlay != nil {
		t.Error("y must close the confirm")
	}
	m = drain(t, m, cmd)
	if len(fa.rollbacks) != 1 || fa.rollbacks[0] != 4 {
		t.Fatalf("rollbacks = %v, want [4]", fa.rollbacks)
	}
}

// A refuted roll back is a notice: no overlay opens and nothing is written.
func TestAuditRollbackRefusedNotices(t *testing.T) {
	fa := auditFixtureFake()
	fa.previewErr = &relevo.RollbackRefused{Rev: 4, Reason: "pick one of agy's providers"}
	m := auditFixtureShell(t, fa)

	res, cmd := m.Update(key('R'))
	m = drain(t, res.(Model), cmd)
	if m.overlay != nil {
		t.Errorf("a refused roll back must open no overlay, got %T", m.overlay)
	}
	if want := "can't roll back to #4: pick one of agy's providers"; m.notice != want {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}
	if len(fa.rollbacks) != 0 {
		t.Errorf("a refused roll back must write nothing, got %v", fa.rollbacks)
	}
}

// Rolling back to a revision the config already equals is a notice, not a
// confirm.
func TestAuditRollbackNoChangeNotices(t *testing.T) {
	fa := auditFixtureFake()
	fa.previewErr = config.ErrNoChange
	m := auditFixtureShell(t, fa)

	res, cmd := m.Update(key('R'))
	m = drain(t, res.(Model), cmd)
	if m.overlay != nil {
		t.Errorf("a no-change roll back must open no overlay, got %T", m.overlay)
	}
	if want := "config already equals #4"; m.notice != want {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}
	if len(fa.rollbacks) != 0 {
		t.Errorf("a no-change roll back must write nothing, got %v", fa.rollbacks)
	}
}

// The cursor is an index over revisions: it steps from revision 5 to revision
// 4, past the day rule between them, and never lands on one.
func TestAuditCursorStepsOverDayRules(t *testing.T) {
	m := goldenAuditModel(t, 132, 34, auditFixtureFake())
	v := auditViewOf(t, m)
	if v.cur != 0 || v.revs[v.cur].Rev != 5 {
		t.Fatalf("cursor starts at %d (rev %d), want index 0 (#5)", v.cur, v.revs[v.cur].Rev)
	}

	m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyDown})
	v = auditViewOf(t, m)
	if v.cur != 1 || v.revs[v.cur].Rev != 4 {
		t.Errorf("down = index %d (rev %d), want index 1 (#4)", v.cur, v.revs[v.cur].Rev)
	}

	m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyDown})
	v = auditViewOf(t, m)
	if v.cur != 2 || v.revs[v.cur].Rev != 3 {
		t.Errorf("second down = index %d (rev %d), want index 2 (#3)", v.cur, v.revs[v.cur].Rev)
	}
}

// A cursor move asks for the revision's changes once, and the reply is cached:
// moving over a revision again asks for nothing.
func TestAuditChangesRequestedOncePerRevision(t *testing.T) {
	fa := auditFixtureFake()
	m := goldenAuditModel(t, 132, 34, fa)

	// The load cached revision 5; revision 4 still needs a request.
	if _, ok := auditViewOf(t, m).changes[5]; !ok {
		t.Fatal("the load must cache the cursor revision's changes")
	}

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = res.(Model)
	if cmd == nil {
		t.Fatal("moving to an uncached revision must request its changes")
	}
	m = drain(t, m, cmd)
	if _, ok := auditViewOf(t, m).changes[4]; !ok {
		t.Fatal("the reply must be cached for #4")
	}

	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = drain(t, res.(Model), cmd)
	if cmd != nil {
		t.Error("moving to a cached revision must request nothing")
	}
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = drain(t, res.(Model), cmd)
	if cmd != nil {
		t.Error("moving back to a cached revision must request nothing")
	}
}

// :audit without an Actions seam is a notice, as :candidates, :actors and
// :agents are.
func TestAuditNeedsActions(t *testing.T) {
	env := Env{Ctx: context.Background(), Loaded: true, Now: railNow, Width: 132, Height: 34}
	cmd := execLine("audit", env, prefs{})
	if cmd == nil {
		t.Fatal(":audit with no Actions must return a notice")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf(":audit with no Actions gave %T, want a notice", cmd())
	}
	if !strings.Contains(msg.text, "need relevo ui") {
		t.Errorf("notice = %q", msg.text)
	}
}
