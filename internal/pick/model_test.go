package pick

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// wantQuit asserts the command is tea.Quit and the model carries outcome.
func wantQuit(t *testing.T, m Model, cmd func() interface{}, outcome error) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected tea.Quit, got no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected tea.Quit")
	}
	if !errors.Is(m.outcome, outcome) {
		t.Fatalf("outcome = %v, want %v", m.outcome, outcome)
	}
}

func TestEmptyListEndsOnResultScreenWithNothingToPick(t *testing.T) {
	m := newModel(context.Background(), relevo.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg())
	if m.screen != screenResult || m.result.text != "no bindings to mark done" {
		t.Fatalf("screen=%v text=%q", m.screen, m.result.text)
	}
	m, cmd := update(t, m, key("x"))
	wantQuit(t, m, cmd, ErrNothingToPick)
}

func TestStatusErrorEndsOnResultScreen(t *testing.T) {
	m := newModel(context.Background(), relevo.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, statusMsg{err: errors.New("harness: connection refused")})
	if m.screen != screenResult || m.result.err == nil {
		t.Fatalf("screen=%v err=%v", m.screen, m.result.err)
	}
	m, cmd := update(t, m, key("enter"))
	wantQuit(t, m, cmd, ErrVerbFailed)
}

func TestListCursorStaysInBounds(t *testing.T) {
	m := newModel(context.Background(), relevo.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working"), row("b", "ACTIVE", "working")))
	m, _ = update(t, m, key("up"))
	if m.cursor != 0 {
		t.Fatalf("cursor above the top: %d", m.cursor)
	}
	m, _ = update(t, m, key("j"))
	m, _ = update(t, m, key("down"))
	if m.cursor != 1 {
		t.Fatalf("cursor past the end: %d", m.cursor)
	}
	m, _ = update(t, m, key("k"))
	if m.cursor != 0 {
		t.Fatalf("k did not move up: %d", m.cursor)
	}
}

func TestEscCancelsFromTheList(t *testing.T) {
	m := newModel(context.Background(), relevo.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working")))
	m, cmd := update(t, m, key("esc"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestCtrlCCancelsFromAnyScreen(t *testing.T) {
	m := newModel(context.Background(), relevo.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg())
	m, cmd := update(t, m, key("ctrl+c"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestEnterRunsDoneAndShowsItsText(t *testing.T) {
	rt := testRuntime(t, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("y"))
	if m.screen != screenResult || !m.result.pending {
		t.Fatalf("enter should move to a pending result screen: screen=%v pending=%v", m.screen, m.result.pending)
	}
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
	m, _ = update(t, m, cmd())
	if m.result.pending || m.result.err != nil {
		t.Fatalf("result: pending=%v err=%v", m.result.pending, m.result.err)
	}
	if m.result.text != relevo.DoneText("webshop", relevo.DoneResult{}) {
		t.Fatalf("text = %q, want DoneText", m.result.text)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil || b.State != store.StateDone {
		t.Fatalf("binding after done: state=%v err=%v", b.State, err)
	}
	m, quit := update(t, m, key("enter"))
	wantQuit(t, m, quit, nil)
}

func TestEnterRunsUnbindWithArchive(t *testing.T) {
	rt := testRuntime(t, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbUnbind, Archive: true})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("y"))
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("unbind: %v", m.result.err)
	}
	if !contains(m.result.text, "archived webshop") {
		t.Fatalf("text = %q, want the archive line", m.result.text)
	}
	if _, err := rt.Store.Load("webshop"); err == nil {
		t.Fatal("binding still loads after unbind")
	}
}

func TestVerbErrorShowsAndExitsOne(t *testing.T) {
	rt := testRuntime(t) // no binding named webshop
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("y"))
	m, _ = update(t, m, cmd())
	if m.result.err == nil {
		t.Fatal("done on a missing binding should fail")
	}
	m, quit := update(t, m, key("x"))
	wantQuit(t, m, quit, ErrVerbFailed)
}

func TestKeysAreIgnoredWhileTheVerbRuns(t *testing.T) {
	rt := testRuntime(t, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	m, _ = update(t, m, key("y"))
	m, cmd := update(t, m, key("enter"))
	if cmd != nil {
		t.Fatal("a key during a pending verb must not quit or re-run")
	}
	if m.outcome != nil {
		t.Fatalf("outcome set early: %v", m.outcome)
	}
}

func TestEnterOnLiveRowAsksBeforeDone(t *testing.T) {
	rt := testRuntime(t, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, cmd := update(t, m, key("enter"))
	if m.screen != screenConfirm {
		t.Fatalf("enter on an ACTIVE row: screen=%v, want screenConfirm", m.screen)
	}
	if cmd != nil {
		t.Fatal("moving to the confirm screen must not run the verb")
	}
	if m.confirm.Name != "webshop" {
		t.Fatalf("confirm holds %q, want webshop", m.confirm.Name)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil || b.State != store.StateActive {
		t.Fatalf("binding touched before confirm: state=%v err=%v", b.State, err)
	}
}

func TestAnyKeyButYReturnsToTheList(t *testing.T) {
	rt := testRuntime(t, testBinding("a"), testBinding("b"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working"), row("b", "ACTIVE", "working")))
	m, _ = update(t, m, key("down"))
	m, _ = update(t, m, key("enter"))
	if m.screen != screenConfirm {
		t.Fatalf("screen=%v, want screenConfirm", m.screen)
	}
	for _, k := range []string{"enter", "n", "esc", "Y"} {
		mm, cmd := update(t, m, key(k))
		if mm.screen != screenList {
			t.Errorf("%q on confirm: screen=%v, want screenList", k, mm.screen)
		}
		if cmd != nil {
			t.Errorf("%q on confirm returned a command; want none", k)
		}
		if mm.cursor != 1 {
			t.Errorf("%q on confirm moved the cursor to %d, want 1", k, mm.cursor)
		}
		if mm.outcome != nil {
			t.Errorf("%q on confirm set outcome %v; the picker must stay open", k, mm.outcome)
		}
	}
	for _, name := range []string{"a", "b"} {
		b, err := rt.Store.Load(name)
		if err != nil || b.State != store.StateActive {
			t.Fatalf("%s touched by a cancelled confirm: state=%v err=%v", name, b.State, err)
		}
	}
}

func TestUnbindOnDoneRowRunsWithoutConfirm(t *testing.T) {
	done := testBinding("old")
	done.State = store.StateDone
	rt := testRuntime(t, done)
	m := newModel(context.Background(), rt, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("old", "DONE", "idle")))
	m, cmd := update(t, m, key("enter"))
	if m.screen != screenResult || !m.result.pending {
		t.Fatalf("enter on a DONE row should move straight to a pending result: screen=%v pending=%v", m.screen, m.result.pending)
	}
	if cmd == nil {
		t.Fatal("enter on a DONE row returned no command")
	}
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("unbind: %v", m.result.err)
	}
	if _, err := rt.Store.Load("old"); err == nil {
		t.Fatal("binding still loads after unbind")
	}
}

func TestConfirmViewNamesTheBindingItsStateAndRound(t *testing.T) {
	m := newModel(context.Background(), relevo.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	v := m.confirmView()
	for _, want := range []string{"mark webshop done?", "ACTIVE", "round 2", "y", "any other key cancels"} {
		if !contains(v, want) {
			t.Errorf("confirm view lacks %q:\n%s", want, v)
		}
	}
	m = newModel(context.Background(), relevo.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("webshop", "NEEDS YOU", "blocked")))
	m, _ = update(t, m, key("enter"))
	v = m.confirmView()
	for _, want := range []string{"unbind webshop?", "NEEDS YOU", "round 2"} {
		if !contains(v, want) {
			t.Errorf("unbind confirm view lacks %q:\n%s", want, v)
		}
	}
}

func TestCtrlCCancelsFromConfirm(t *testing.T) {
	m := newModel(context.Background(), relevo.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("ctrl+c"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestPickUnbindOfAKeptWorktreeShowsTheHint(t *testing.T) {
	b := testBinding("webshop")
	b.Worktree = t.TempDir()
	b.Builder = store.Endpoint{Mode: store.ModeHeadless}
	rt := testRuntime(t, b)
	m := newModel(context.Background(), rt, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("y"))
	m, _ = update(t, m, cmd())
	if !errors.Is(m.result.err, relevo.ErrWorktreeKept) {
		t.Fatalf("err = %v, want ErrWorktreeKept", m.result.err)
	}
	t.Logf("pick message: %s", m.result.err)
	if !contains(m.result.err.Error(), relevo.KeptHint("webshop")) {
		t.Fatalf("err = %q, want the kept hint", m.result.err)
	}
}
