package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/view"
)

// TestCommandModalGroupsSections: a report with 2 live, 1 done binding and 1
// gate, typed `r`. The stripped box shows VIEWS, then BINDINGS with the done
// binding last, and the selected row is the first match (§3.1, §5).
func TestCommandModalGroupsSections(t *testing.T) {
	rep := view.Report{
		Bindings: []view.BindingStatus{
			{Name: "runtime", Round: 1, Display: "ACTIVE", BuilderStatus: "working"},
			{Name: "serve", Round: 1, Display: "ACTIVE", BuilderStatus: "idle"},
			{Name: "render", Round: 1, Display: "DONE"},
		},
		Gated: []availability.Gate{{
			Token: "agy/antigravity/gemini-3", Name: "gemini",
			Kind: availability.RateLimited, Until: railNow.Add(time.Hour),
		}},
	}
	m := goldenActionModel(t, 140, 40, &fakeActions{}, rep)
	res, _ := m.Update(key(':'))
	m = res.(Model)
	res, _ = m.Update(key('r'))
	m = res.(Model)

	_, rows, _, _ := m.cmdModal(m.env())
	box := stripANSI(strings.Join(rows, "\n"))

	for _, want := range []string{"VIEWS", "BINDINGS"} {
		if !strings.Contains(box, want) {
			t.Errorf("the command modal must show %q:\n%s", want, box)
		}
	}

	serve := strings.Index(box, "round serve")
	runtime := strings.Index(box, "round runtime")
	render := strings.Index(box, "round render")
	if serve < 0 || runtime < 0 || render < 0 {
		t.Fatalf("the box must list all three round candidates:\n%s", box)
	}
	if render < serve || render < runtime {
		t.Errorf("the done binding must sort last:\n%s", box)
	}

	// The selected row is the first match: "round" leads the VIEWS section.
	views := strings.Index(box, "VIEWS")
	first := ""
	for _, line := range strings.Split(box[views:], "\n")[1:] {
		if line != "" {
			first = line
			break
		}
	}
	if !strings.HasPrefix(first, "▸ round") {
		t.Errorf("the selected row must be the first match, got %q in:\n%s", first, box)
	}
}

// TestCommandModalFoldsOverflow: 12 bindings, empty input. It shows 8 rows and
// `+ N more match` (§3.1, §5).
func TestCommandModalFoldsOverflow(t *testing.T) {
	var bindings []view.BindingStatus
	for i := 1; i <= 12; i++ {
		bindings = append(bindings, view.BindingStatus{
			Name:    "b" + string(rune('a'+i-1)),
			Round:   1,
			Display: "ACTIVE", BuilderStatus: "idle",
		})
	}
	m := goldenActionModel(t, 140, 40, &fakeActions{}, view.Report{Bindings: bindings})
	res, _ := m.Update(key(':'))
	m = res.(Model)

	_, rows, _, _ := m.cmdModal(m.env())
	box := stripANSI(strings.Join(rows, "\n"))

	ms := m.cmd.matches(m.env())
	if len(ms) != 8 {
		t.Fatalf("empty input must cap matches at 8, got %d", len(ms))
	}
	shown := 0
	for _, line := range strings.Split(box, "\n") {
		for _, c := range ms {
			if strings.Contains(line, c.name) {
				shown++
				break
			}
		}
	}
	if shown != 8 {
		t.Errorf("the box shows %d match rows, want 8:\n%s", shown, box)
	}
	if !strings.Contains(box, "+ 17 more match; keep typing") {
		t.Errorf("the box must fold the overflow, got:\n%s", box)
	}
	if strings.Contains(box, "round b") {
		t.Errorf("no binding row fits in the cap of 8:\n%s", box)
	}
}

// TestHelpModalColumns: on :fleet with actions, ACT ON THE ROW holds x and D,
// MOVE & VIEW holds . and /, and ANYWHERE holds : (§3.2, §5).
func TestHelpModalColumns(t *testing.T) {
	m := goldenActionModel(t, 140, 40, &fakeActions{},
		view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
	res, _ := m.Update(key('?'))
	m = res.(Model)

	move, act, anywhere := m.helpColumns(m.env())
	has := func(keys []KeyHelp, k string) bool {
		for _, kh := range keys {
			if kh.Key == k {
				return true
			}
		}
		return false
	}
	if !has(act, "x") || !has(act, "D") {
		t.Errorf("ACT ON THE ROW must hold x and D, got %+v", act)
	}
	if !has(move, ".") || !has(move, "/") {
		t.Errorf("MOVE & VIEW must hold . and /, got %+v", move)
	}
	if !has(anywhere, ":") {
		t.Errorf("ANYWHERE must hold :, got %+v", anywhere)
	}

	title, rows, _, _ := m.helpModal(m.env())
	if title != "keys · fleet" {
		t.Errorf("help title = %q, want %q", title, "keys · fleet")
	}
	box := stripANSI(strings.Join(rows, "\n"))
	for _, want := range []string{"MOVE & VIEW", "ACT ON THE ROW", "ANYWHERE"} {
		if !strings.Contains(box, want) {
			t.Errorf("the help modal must show %q:\n%s", want, box)
		}
	}
}

// TestHelpNoDuplicateKeys: on :fleet the view's own "/ filter" is not repeated
// in ANYWHERE (§2.6, §5).
func TestHelpNoDuplicateKeys(t *testing.T) {
	m := goldenActionModel(t, 140, 40, &fakeActions{},
		view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
	res, _ := m.Update(key('?'))
	m = res.(Model)

	_, rows, _, _ := m.helpModal(m.env())
	box := stripANSI(strings.Join(rows, "\n"))
	if n := strings.Count(box, "filter"); n != 1 {
		t.Errorf("the help modal shows `filter` %d times, want once:\n%s", n, box)
	}
}

// TestGateFormOneSubmitCallsGateOnce: a fake Actions. Type 2h, tab, type
// quota, enter. Gate is called once with 2h and quota. An invalid 2x keeps the
// box open with an error and focus on for (§3.3, §5).
func TestGateFormOneSubmitCallsGateOnce(t *testing.T) {
	fa := &fakeActions{result: Result{Text: "gated", Refresh: true}}
	m := actionModel(t, fa, view.BindingStatus{
		Name: "atlas", Round: 4, Display: "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm",
	})

	res, cmd0 := m.Update(key('g'))
	m = drain(t, res.(Model), cmd0)
	if _, ok := m.overlay.(formBox); !ok {
		t.Fatalf("g must open a formBox, got %T", m.overlay)
	}

	for _, r := range "2h" {
		res, _ = m.Update(key(r))
		m = res.(Model)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = res.(Model)
	for _, r := range "quota" {
		res, _ = m.Update(key(r))
		m = res.(Model)
	}
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)

	if len(fa.gates) != 1 {
		t.Fatalf("gates = %+v, want one call", fa.gates)
	}
	got := fa.gates[0]
	if got.subject != "opencode/cline-pass/glm" || got.forDur != 2*time.Hour || got.reason != "quota" {
		t.Errorf("gate call = %+v", got)
	}

	// An invalid duration keeps the box open, on `for`.
	res, cmd0 = m.Update(key('g'))
	m = drain(t, res.(Model), cmd0)
	for _, r := range "2x" {
		res, _ = m.Update(key(r))
		m = res.(Model)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	kept, ok := m.overlay.(formBox)
	if !ok {
		t.Fatalf("an invalid duration must keep the form open, got %T", m.overlay)
	}
	if kept.err == "" {
		t.Error("an invalid duration must show the validation error")
	}
	if kept.focus != 0 {
		t.Errorf("an invalid duration must focus `for`, got focus %d", kept.focus)
	}
}

// TestGateFormEmptyDurationIsUntilCleared: an empty for field calls Gate with 0
// (§3.3, §5).
func TestGateFormEmptyDurationIsUntilCleared(t *testing.T) {
	fa := &fakeActions{result: Result{Text: "gated", Refresh: true}}
	m := actionModel(t, fa, view.BindingStatus{
		Name: "atlas", Round: 4, Display: "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm",
	})

	res, cmd0 := m.Update(key('g'))
	m = drain(t, res.(Model), cmd0)
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)

	if len(fa.gates) != 1 {
		t.Fatalf("gates = %+v, want one call", fa.gates)
	}
	if got := fa.gates[0]; got.forDur != 0 || got.reason != "" {
		t.Errorf("gate call = %+v, want forDur 0 and no reason", got)
	}
}

// TestGateFormNoPrompt: the stripped gate form carries no `> ` prompt before
// its fields; the label already names each field (§2.4, §5).
func TestGateFormNoPrompt(t *testing.T) {
	m := actionModel(t, &fakeActions{}, view.BindingStatus{
		Name: "atlas", Round: 4, Display: "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm",
	})
	res, cmd0 := m.Update(key('g'))
	m = drain(t, res.(Model), cmd0)

	box := stripANSI(strings.Join(m.overlay.view(140), "\n"))
	if strings.Contains(box, "> ") {
		t.Errorf("the gate form must not show a `> ` prompt:\n%s", box)
	}
}

// TestModalFooterShowsOverlayKeys: with the gate form open, the footer contains
// `next field` and not `? all keys` (§2.1, §5).
func TestModalFooterShowsOverlayKeys(t *testing.T) {
	m := actionModel(t, &fakeActions{}, view.BindingStatus{
		Name: "atlas", Round: 4, Display: "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm",
	})
	res, cmd0 := m.Update(key('g'))
	m = drain(t, res.(Model), cmd0)

	footer := stripANSI(m.keysView(m.env()))
	if !strings.Contains(footer, "next field") {
		t.Errorf("the footer must show the form's keys, got %q", footer)
	}
	if strings.Contains(footer, "? all keys") {
		t.Errorf("the footer must drop the global tail for a modal, got %q", footer)
	}
}

// TestRetryListDisablesCurrentAndGated: three candidates, one current and one
// gated. sel starts on the ready one, ↑/↓ never land on a disabled one, and
// enter opens the retry confirm naming it (§3.5, §5).
func TestRetryListDisablesCurrentAndGated(t *testing.T) {
	fa := &fakeActions{candidates: []string{"current-c", "gated-g", "ready-r"}}
	b := view.BindingStatus{
		Name: "atlas", Round: 4, Display: "ACTIVE", BuilderName: "current-c",
	}
	rep := view.Report{
		Bindings: []view.BindingStatus{b},
		Gated: []availability.Gate{{
			Token: "agy/provider/gated-g", Name: "gated-g",
			Kind: availability.RateLimited, Until: railNow.Add(time.Hour),
		}},
	}
	m := goldenActionModel(t, 140, 40, fa, rep)

	res, cmd := m.Update(key('r'))
	m = drain(t, res.(Model), cmd)
	lb, ok := m.overlay.(listBox)
	if !ok {
		t.Fatalf("r must open the retry list, got %T", m.overlay)
	}
	if lb.sel != 2 {
		t.Fatalf("sel must start on the ready candidate, got %d (%+v)", lb.sel, lb.items)
	}
	if !lb.items[0].disabled || !lb.items[1].disabled || lb.items[2].disabled {
		t.Errorf("current and gated must be disabled, ready must not: %+v", lb.items)
	}

	keys := []tea.KeyMsg{
		{Type: tea.KeyUp}, {Type: tea.KeyUp}, {Type: tea.KeyDown},
		{Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyUp},
	}
	for _, k := range keys {
		res, _ = m.Update(k)
		m = res.(Model)
		lb = m.overlay.(listBox)
		if lb.items[lb.sel].disabled {
			t.Fatalf("↑/↓ landed on a disabled item: %+v", lb.items[lb.sel])
		}
	}

	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("enter must open the retry confirm, got %T", m.overlay)
	}
	if !strings.Contains(box.title, "ready-r") {
		t.Errorf("the confirm must name the picked candidate, title = %q", box.title)
	}

	res, cmd = m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.retries) != 1 || fa.retries[0].candidate != "ready-r" {
		t.Errorf("retries = %+v, want one on ready-r", fa.retries)
	}
}

// TestSendPickerListsNewestFirst: a temp dir with three .md files at distinct
// mtimes and one .txt. The list shows the three .md files, newest first.
// Typing a filter narrows it. ↓ then enter opens the send confirm with that
// path (§3.4, §5).
func TestSendPickerListsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	files := []struct {
		name string
		age  time.Duration
	}{
		{"a.md", 3 * time.Hour},
		{"b.md", 2 * time.Hour},
		{"c.md", 1 * time.Hour},
		{"note.txt", 30 * time.Minute},
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, []byte("# "+f.name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
		at := railNow.Add(-f.age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("chtimes %s: %v", f.name, err)
		}
	}

	m := actionModel(t, &fakeActions{}, view.BindingStatus{
		Name: "atlas", Round: 4, Display: "ACTIVE", CWD: dir,
	})
	res, cmd := m.Update(key('s'))
	m = drain(t, res.(Model), cmd)
	p, ok := m.overlay.(sendPicker)
	if !ok {
		t.Fatalf("s must open the send picker, got %T", m.overlay)
	}

	sep := string(filepath.Separator)
	p.input.SetValue(dir + sep)
	m.overlay = p

	list := p.plans()
	if len(list) != 3 {
		t.Fatalf("list = %+v, want the three .md files", list)
	}
	got := []string{filepath.Base(list[0].path), filepath.Base(list[1].path), filepath.Base(list[2].path)}
	want := []string{"c.md", "b.md", "a.md"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("newest-first order = %v, want %v", got, want)
		}
	}

	// Typing a filter narrows the list.
	p.input.SetValue(filepath.Join(dir, "b"))
	m.overlay = p
	if narrowed := p.plans(); len(narrowed) != 1 || filepath.Base(narrowed[0].path) != "b.md" {
		t.Fatalf("filtered list = %+v, want only b.md", narrowed)
	}

	// ↓ selects the newest, and enter opens the send confirm with that path.
	p.input.SetValue(dir + sep)
	p.pick = -1
	m.overlay = p
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = res.(Model)
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("enter on a row must open the send confirm, got %T", m.overlay)
	}
	newest := filepath.Join(dir, "c.md")
	if box.title != "Send "+newest+" to atlas as round 4?" {
		t.Errorf("send confirm title = %q", box.title)
	}
}

// TestSendPickerRelativePath: a binding tree with docs/plans/ pre-fills the
// relative "docs/plans/"; typing a.md resolves against the tree and the send
// confirm's Actions.Send receives the absolute path. An absolute path typed by
// the human also works, and the confirm title shows the value as typed
// (§2.5, §5).
func TestSendPickerRelativePath(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "docs", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	abs := filepath.Join(planDir, "a.md")
	if err := os.WriteFile(abs, []byte("# plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	rel := filepath.Join("docs", "plans") + string(filepath.Separator)

	fa := &fakeActions{}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE", CWD: dir})

	res, cmd := m.Update(key('s'))
	m = drain(t, res.(Model), cmd)
	p, ok := m.overlay.(sendPicker)
	if !ok {
		t.Fatalf("s must open the send picker, got %T", m.overlay)
	}
	if p.input.Value() != rel {
		t.Fatalf("pre-fill = %q, want %q", p.input.Value(), rel)
	}

	// Typing a.md appends to the pre-fill.
	for _, r := range "a.md" {
		res, _ = m.Update(key(r))
		m = res.(Model)
	}
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("enter must open the send confirm, got %T", m.overlay)
	}
	typed := filepath.Join("docs", "plans", "a.md")
	if want := "Send " + typed + " to atlas as round 4?"; box.title != want {
		t.Errorf("send confirm title = %q, want the value as typed %q", box.title, want)
	}
	res, cmd = m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.sends) != 1 || fa.sends[0].file != abs {
		t.Fatalf("sends = %+v, want one send of the absolute %q", fa.sends, abs)
	}

	// An absolute path typed by the human keeps working.
	res, cmd = m.Update(key('s'))
	m = drain(t, res.(Model), cmd)
	p, ok = m.overlay.(sendPicker)
	if !ok {
		t.Fatalf("s must reopen the send picker, got %T", m.overlay)
	}
	p.input.SetValue(abs)
	m.overlay = p
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	box, ok = m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("an absolute path must open the send confirm, got %T", m.overlay)
	}
	if want := "Send " + abs + " to atlas as round 4?"; box.title != want {
		t.Errorf("send confirm title = %q, want %q", box.title, want)
	}
	res, cmd = m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.sends) != 2 || fa.sends[1].file != abs {
		t.Fatalf("sends = %+v, want a second send of %q", fa.sends, abs)
	}
}

// TestSendPickerCtrlEOpensEditorFlow: ctrl+e returns a non-nil cmd and closes
// the picker. Assert via the returned overlay state (§3.4, §5).
func TestSendPickerCtrlEOpensEditorFlow(t *testing.T) {
	m := actionModel(t, &fakeActions{}, view.BindingStatus{
		Name: "atlas", Round: 4, Display: "ACTIVE", CWD: t.TempDir(),
	})
	res, cmd := m.Update(key('s'))
	m = drain(t, res.(Model), cmd)
	if _, ok := m.overlay.(sendPicker); !ok {
		t.Fatalf("s must open the send picker, got %T", m.overlay)
	}

	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = res.(Model)
	if m.overlay != nil {
		t.Errorf("ctrl+e must close the picker, overlay = %T", m.overlay)
	}
	if cmd == nil {
		t.Error("ctrl+e must return the editor flow's cmd")
	}
}
