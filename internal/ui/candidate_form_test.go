package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// formDoc is the round 2 fixture doc, the one the goldens and these tests use.
func formDoc(t *testing.T) relevo.ConfigDoc { return candFixtureDoc(t) }

// formEnv is the form's Env: the fake under test, the fixture gates and a
// 132x34 shell.
func formEnv(fa *fakeActions) Env {
	return candActionEnv(fa, candGatedReport())
}

// formPress sends one key through the form and returns the next value.
func formPress(f candidateForm, k tea.KeyMsg) candidateForm {
	next, _, _ := f.update(k)
	return next.(candidateForm)
}

// formType sends s's runes, one key at a time, through the form.
func formType(f candidateForm, s string) candidateForm {
	for _, r := range s {
		f = formPress(f, key(r))
	}
	return f
}

// formArrow is one arrow key press.
func formArrow(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// formText is the form's rendered rows, ANSI stripped.
func formText(f candidateForm, width int) string {
	return stripANSI(strings.Join(f.view(width), "\n"))
}

// Enter on a valid edit applies it: the message names the rename.
func TestCandidateFormValidEditSubmits(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}
	f := newCandidateForm(formEnv(fa), fa.doc, "deepseek-v4.1-flash", "builder 3", "")
	f = formPress(f, tea.KeyMsg{Type: tea.KeyCtrlU})
	f = formType(f, "cline-pass/deepseek-v4.2-flash#high")

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("enter on a valid edit must close the form")
	}
	runCmd(t, cmd)

	if len(fa.configEdits) != 1 {
		t.Fatalf("configEdits = %d, want one", len(fa.configEdits))
	}
	want := "edit candidate deepseek-v4.1-flash → deepseek-v4.2-flash"
	if got := fa.configEdits[0].Message; got != want {
		t.Errorf("edit message = %q, want %q", got, want)
	}
}

// Enter on an unchanged edit closes with a notice and writes nothing.
func TestCandidateFormUnchangedEditNotices(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}
	f := newCandidateForm(formEnv(fa), fa.doc, "deepseek-v4.1-flash", "builder 3", "")

	_, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !closed {
		t.Fatal("enter on an unchanged edit must close the form")
	}
	if cmd == nil {
		t.Fatal("enter must return the notice")
	}
	msg := cmd()
	notice, ok := msg.(noticeMsg)
	if !ok {
		t.Fatalf("enter gave %T, want a notice", msg)
	}
	if notice.text != "nothing changed" {
		t.Errorf("notice = %q, want nothing changed", notice.text)
	}
	if len(fa.configEdits) != 0 {
		t.Error("an unchanged edit must not write one")
	}
}

// Enter on an invalid form stays open, marks the form tried and focuses the
// failing field.
func TestCandidateFormInvalidStaysOpen(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}
	f := newCandidateForm(formEnv(fa), fa.doc, "", "", "opencode")

	next, cmd, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if closed {
		t.Fatal("enter on an invalid form must keep it open")
	}
	if cmd != nil {
		t.Errorf("enter on an invalid form gave a command: %T", cmd())
	}
	f = next.(candidateForm)
	if !f.tried {
		t.Error("enter must mark the form tried")
	}
	if f.focus != 1 {
		t.Errorf("focus = %d, want the provider row", f.focus)
	}
	if f.touched[1] {
		t.Error("enter must not mark the provider touched")
	}
}

// Down on an open harness's provider cycles its suggestions in order.
func TestCandidateFormProviderSuggestionCycle(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}
	f := newCandidateForm(formEnv(fa), fa.doc, "", "", "opencode")
	f = formPress(f, tea.KeyMsg{Type: tea.KeyTab}) // harness -> provider

	got := f.suggestions()
	if strings.Join(got, ",") != "openrouter,cline-pass" {
		t.Fatalf("suggestions = %v, want openrouter,cline-pass", got)
	}

	f = formPress(f, formArrow(tea.KeyDown))
	if f.provIn.Value() != "openrouter" {
		t.Errorf("after down = %q, want openrouter", f.provIn.Value())
	}
	f = formPress(f, formArrow(tea.KeyDown))
	if f.provIn.Value() != "cline-pass" {
		t.Errorf("after second down = %q, want cline-pass", f.provIn.Value())
	}
	f = formPress(f, formArrow(tea.KeyDown))
	if f.provIn.Value() != "openrouter" {
		t.Errorf("cycle wrapped to %q, want openrouter", f.provIn.Value())
	}
	if !f.touched[1] {
		t.Error("cycling must mark the provider touched")
	}
}

// A listed harness with two or more providers keeps the current provider only
// when the list holds it, and takes none when it does not (agy).
func TestCandidateFormHarnessChangeSelectsProvider(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}

	f := newCandidateForm(formEnv(fa), fa.doc, "", "", "opencode")
	f.provIn.SetValue("google")
	f.provIn.CursorEnd()
	f = formPress(f, formArrow(tea.KeyRight)) // opencode -> agy
	if f.kinds[f.hsel] != "agy" {
		t.Fatalf("harness = %q, want agy", f.kinds[f.hsel])
	}
	if f.psel != 0 || f.input().Provider != "google" {
		t.Errorf("psel = %d provider = %q, want 0 and google", f.psel, f.input().Provider)
	}

	f = newCandidateForm(formEnv(fa), fa.doc, "", "", "opencode")
	f.provIn.SetValue("openrouter")
	f.provIn.CursorEnd()
	f = formPress(f, formArrow(tea.KeyRight)) // opencode -> agy
	if f.psel != -1 {
		t.Errorf("psel = %d, want -1 for a provider agy does not list", f.psel)
	}
}

// Adding on a claude row starts on its only provider: psel is 0, the provider
// is anthropic, and enter raises no provider error even after it is tried
// (§1, round 7).
func TestCandidateFormAddClaudePicksSoleProvider(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}
	f := newCandidateForm(formEnv(fa), fa.doc, "", "", "claude")
	if f.psel != 0 || f.input().Provider != "anthropic" {
		t.Fatalf("psel = %d provider = %q, want 0 and anthropic", f.psel, f.input().Provider)
	}

	next, _, closed := f.update(tea.KeyMsg{Type: tea.KeyEnter})
	if closed {
		t.Fatal("enter on an add with no model must keep the form open")
	}
	f = next.(candidateForm)
	if !f.tried {
		t.Error("enter must mark the form tried")
	}
	if _, err := f.check(); err != nil {
		if fe := asFieldError(err); fe == nil || fe.Field != "model" {
			t.Errorf("check() = %v, want a model error, not a provider one", err)
		}
	}
	if text := formText(f, 132); strings.Contains(text, "a provider is required") {
		t.Errorf("the claude add form shows a provider error:\n%s", text)
	}
}

// Switching to claude from opencode auto-picks anthropic: claude lists exactly
// one provider, so there is nothing else to pick (§1, round 7).
func TestCandidateFormHarnessChangePicksSoleProvider(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}

	f := newCandidateForm(formEnv(fa), fa.doc, "", "", "opencode")
	f = formPress(f, formArrow(tea.KeyLeft)) // opencode -> codex
	f = formPress(f, formArrow(tea.KeyLeft)) // codex -> claude
	if f.kinds[f.hsel] != "claude" {
		t.Fatalf("harness = %q, want claude", f.kinds[f.hsel])
	}
	if f.psel != 0 || f.input().Provider != "anthropic" {
		t.Errorf("psel = %d provider = %q, want 0 and anthropic", f.psel, f.input().Provider)
	}
}

// A gated provider shows the gate line among the form's info lines.
func TestCandidateFormGatedProviderLine(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}
	f := newCandidateForm(formEnv(fa), fa.doc, "deepseek-v4.1-flash", "builder 3", "")

	var gate availability.Gate
	for _, g := range candFixtureGates() {
		if g.Token == "opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high" {
			gate = g
		}
	}
	if gate.Token == "" {
		t.Fatal("the fixture has no gate on deepseek's provider")
	}

	text := formText(f, 132)
	want := "cline-pass is gated until " + statsGateUntil(gate, railNow)
	if !strings.Contains(text, want) {
		t.Errorf("form does not show %q:\n%s", want, text)
	}
	// The fixture's note renders as "·", so the line must omit the reason
	// rather than carry an empty one (§2, round 6).
	if reason := statsGateReason(gate.Note); strings.Contains(text, " · "+reason) {
		t.Errorf("form shows an empty gate reason %q:\n%s", reason, text)
	}
}

// Adding carries the note that no actor picks the candidate yet.
func TestCandidateFormAddActorsNote(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}
	f := newCandidateForm(formEnv(fa), fa.doc, "", "", "opencode")

	text := formText(f, 132)
	if !strings.Contains(text, "no actor picks it until you add it to one in :actors") {
		t.Errorf("add form does not show the :actors note:\n%s", text)
	}
	if strings.Contains(text, "was ") {
		t.Errorf("add form shows an edit's was-note:\n%s", text)
	}
}

// The name row previews the model's name and, on an edit that renames, the old
// name and the row's SERVES text.
func TestCandidateFormNameRow(t *testing.T) {
	fa := &fakeActions{doc: formDoc(t)}

	add := newCandidateForm(formEnv(fa), fa.doc, "", "", "opencode")
	if !strings.Contains(formText(add, 132), "follows the model") {
		t.Error("an add with no model must read follows the model")
	}
	add = formPress(add, tea.KeyMsg{Type: tea.KeyTab}) // harness -> provider
	add = formPress(add, tea.KeyMsg{Type: tea.KeyTab}) // provider -> model
	add = formType(add, "z-ai/glm-5.4-flash")
	if strings.Contains(formText(add, 132), "follows the model") {
		t.Error("an add with a model must show the derived name, not the placeholder")
	}

	edit := newCandidateForm(formEnv(fa), fa.doc, "deepseek-v4.1-flash", "builder 3", "")
	edit = formPress(edit, tea.KeyMsg{Type: tea.KeyCtrlU})
	edit = formType(edit, "cline-pass/deepseek-v4.2-flash#high")
	text := formText(edit, 132)
	if !strings.Contains(text, "deepseek-v4.2-flash   was deepseek-v4.1-flash · still builder 3") {
		t.Errorf("name row does not show the rename and slots:\n%s", text)
	}
}
