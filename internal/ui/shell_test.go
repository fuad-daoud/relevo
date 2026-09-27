package ui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// fakeView is a minimal View the routing tests can push and interrogate.
type fakeView struct {
	crumbs  []string
	capture bool
	last    tea.KeyMsg
	seen    []string
	mice    int
}

func (f fakeView) Crumbs() []string {
	if len(f.crumbs) == 0 {
		return []string{"fake"}
	}
	return f.crumbs
}
func (f fakeView) Context(Env) (string, string) { return "fake", "" }
func (f fakeView) Keys() []KeyHelp              { return nil }
func (f fakeView) Capturing() bool              { return f.capture }
func (f fakeView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		f.last = k
		f.seen = append(f.seen, k.String())
	}
	if _, ok := msg.(tea.MouseMsg); ok {
		f.mice++
	}
	return f, nil
}
func (f fakeView) Body(env Env, w, h int) string {
	return strings.Repeat("\n", h-1)
}

// TestKeyRoutingRules is §5.2's key routing, one case per rule.
func TestKeyRoutingRules(t *testing.T) {
	newShell := func() Model {
		return splitModel(t, 140, 40,
			view.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"},
			view.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE"},
		)
	}
	key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

	t.Run("1 ctrl+c quits anywhere", func(t *testing.T) {
		m := newShell()
		m.stack = append(m.stack, fakeView{capture: true})
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil {
			t.Fatal("ctrl+c must quit")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Error("ctrl+c must return tea.Quit")
		}
	})

	t.Run("2 cmd.open owns the key", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key(':'))
		m = res.(Model)
		if !m.cmd.open {
			t.Fatal(": must open the command line")
		}
		res, _ = m.Update(key('x'))
		m = res.(Model)
		if !m.cmd.open || !strings.Contains(m.cmd.typed(), "x") {
			t.Errorf("while open the cmdline owns the key: open=%v typed=%q", m.cmd.open, m.cmd.typed())
		}
	})

	t.Run("3 help closes on esc, ?, q and ignores others", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key('?'))
		m = res.(Model)
		if !m.help {
			t.Fatal("? must open the help overlay")
		}
		res, _ = m.Update(key('j'))
		m = res.(Model)
		if !m.help {
			t.Error("another key must be ignored while help is up")
		}
		res, _ = m.Update(key('q'))
		m = res.(Model)
		if m.help {
			t.Error("q must close the help overlay")
		}
	})

	t.Run("4 a capturing top view owns every key", func(t *testing.T) {
		m := newShell()
		fv := fakeView{capture: true}
		m.stack = append(m.stack, fv)
		res, _ := m.Update(key('?'))
		m = res.(Model)
		if m.help {
			t.Error("? must be forwarded to a capturing view, not open help")
		}
		if m.cmd.open {
			t.Error("the cmdline must stay closed")
		}
		if got := m.top().(fakeView).last.String(); got != "?" {
			t.Errorf("the capturing view must receive the key, got %q", got)
		}
	})

	t.Run("5 colon opens the command line", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key(':'))
		if !res.(Model).cmd.open {
			t.Error(": must open the command line")
		}
	})

	t.Run("6 question opens help", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key('?'))
		if !res.(Model).help {
			t.Error("? must open help")
		}
	})

	t.Run("7 esc pops only above the root", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if len(res.(Model).stack) != 1 {
			t.Error("esc at depth 1 must do nothing")
		}
		m.stack = append(m.stack, fakeView{})
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = drain(t, res.(Model), cmd)
		if len(m.stack) != 1 {
			t.Errorf("esc above the root must pop, depth = %d", len(m.stack))
		}
	})

	t.Run("8 q quits at the root and pops above it", func(t *testing.T) {
		m := newShell()
		_, cmd := m.Update(key('q'))
		if cmd == nil {
			t.Fatal("q at depth 1 must quit")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Error("q at depth 1 must return tea.Quit")
		}
		m.stack = append(m.stack, fakeView{})
		res, cmd := m.Update(key('q'))
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				t.Error("q above the root must not quit")
			}
		}
		m = drain(t, res.(Model), cmd)
		if len(m.stack) != 1 {
			t.Errorf("q above the root must pop, depth = %d", len(m.stack))
		}
	})

	t.Run("9 other keys reach the top view", func(t *testing.T) {
		m := newShell()
		res, _ := m.Update(key('j'))
		m = res.(Model)
		if fleet(m).cursor != 1 || fleet(m).sticky != "webshop" {
			t.Errorf("j must move the fleet cursor, got cursor %d sticky %q", fleet(m).cursor, fleet(m).sticky)
		}
	})
}

// fakeOverlay is a test-only overlay (confirm.go:36) that never closes and
// never draws anything, so TestWheelRouting can pin "the wheel never reaches
// a modal" without depending on a real overlay's behavior.
type fakeOverlay struct{}

func (fakeOverlay) update(tea.KeyMsg) (overlay, tea.Cmd, bool) { return fakeOverlay{}, nil, false }
func (fakeOverlay) view(width int) []string                    { return nil }

// TestWheelRouting is §4's wheel routing: a wheel event becomes wheelStep
// arrow keys for the top view, and every other mouse event is dropped before
// it ever reaches updateStack.
func TestWheelRouting(t *testing.T) {
	newShell := func() Model {
		return splitModel(t, 140, 40,
			view.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"},
			view.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE"},
		)
	}
	key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
	wheelDown := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
	wheelUp := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}

	t.Run("wheel down sends three downs", func(t *testing.T) {
		m := newShell()
		m.stack = append(m.stack, fakeView{})
		res, _ := m.Update(wheelDown)
		m = res.(Model)
		got := m.top().(fakeView).seen
		want := []string{"down", "down", "down"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("seen = %v, want %v", got, want)
		}
		if mice := m.top().(fakeView).mice; mice != 0 {
			t.Errorf("mice = %d, want 0", mice)
		}
	})

	t.Run("wheel up sends three ups", func(t *testing.T) {
		m := newShell()
		m.stack = append(m.stack, fakeView{})
		res, _ := m.Update(wheelUp)
		m = res.(Model)
		got := m.top().(fakeView).seen
		want := []string{"up", "up", "up"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("seen = %v, want %v", got, want)
		}
	})

	t.Run("wheel keeps the notice", func(t *testing.T) {
		m := newShell()
		m.stack = append(m.stack, fakeView{})
		m.notice = "x"
		res, _ := m.Update(wheelDown)
		m = res.(Model)
		if m.notice != "x" {
			t.Errorf("notice = %q, want %q", m.notice, "x")
		}
	})

	t.Run("a capturing view gets no wheel", func(t *testing.T) {
		m := newShell()
		m.stack = append(m.stack, fakeView{capture: true})
		res, _ := m.Update(wheelDown)
		m = res.(Model)
		if got := m.top().(fakeView).seen; len(got) != 0 {
			t.Errorf("seen = %v, want empty", got)
		}
		if mice := m.top().(fakeView).mice; mice != 0 {
			t.Errorf("mice = %d, want 0", mice)
		}
	})

	t.Run("help, overlay and command line get no wheel", func(t *testing.T) {
		t.Run("help", func(t *testing.T) {
			m := newShell()
			m.stack = append(m.stack, fakeView{})
			res, _ := m.Update(key('?'))
			m = res.(Model)
			res, _ = m.Update(wheelDown)
			m = res.(Model)
			if !m.help {
				t.Error("help must stay open")
			}
			if got := m.top().(fakeView).seen; len(got) != 0 {
				t.Errorf("seen = %v, want empty", got)
			}
			if mice := m.top().(fakeView).mice; mice != 0 {
				t.Errorf("mice = %d, want 0", mice)
			}
		})

		t.Run("command line", func(t *testing.T) {
			m := newShell()
			m.stack = append(m.stack, fakeView{})
			res, _ := m.Update(key(':'))
			m = res.(Model)
			res, _ = m.Update(wheelDown)
			m = res.(Model)
			if !m.cmd.open || m.cmd.typed() != "" {
				t.Errorf("cmd.open = %v, typed = %q, want open with nothing typed", m.cmd.open, m.cmd.typed())
			}
			if got := m.top().(fakeView).seen; len(got) != 0 {
				t.Errorf("seen = %v, want empty", got)
			}
			if mice := m.top().(fakeView).mice; mice != 0 {
				t.Errorf("mice = %d, want 0", mice)
			}
		})

		t.Run("overlay", func(t *testing.T) {
			m := newShell()
			m.stack = append(m.stack, fakeView{})
			m.overlay = fakeOverlay{}
			res, _ := m.Update(wheelDown)
			m = res.(Model)
			if m.overlay == nil {
				t.Error("overlay must stay set")
			}
			if got := m.top().(fakeView).seen; len(got) != 0 {
				t.Errorf("seen = %v, want empty", got)
			}
			if mice := m.top().(fakeView).mice; mice != 0 {
				t.Errorf("mice = %d, want 0", mice)
			}
		})
	})

	t.Run("other mouse events are dropped", func(t *testing.T) {
		m := newShell()
		m.stack = append(m.stack, fakeView{}, fakeView{})
		events := []tea.MouseMsg{
			{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft},
			{Action: tea.MouseActionRelease, Button: tea.MouseButtonWheelDown},
			{Action: tea.MouseActionMotion},
			{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelLeft},
		}
		for _, ev := range events {
			res, _ := m.Update(ev)
			m = res.(Model)
			for i := 1; i < len(m.stack); i++ {
				fv := m.stack[i].(fakeView)
				if len(fv.seen) != 0 {
					t.Errorf("stack[%d].seen = %v, want empty", i, fv.seen)
				}
				if fv.mice != 0 {
					t.Errorf("stack[%d].mice = %d, want 0", i, fv.mice)
				}
			}
		}
	})
}

// TestSnapshotRoutingForwardsToEveryView: a tabMsg reaches a round view
// that is under a pushed view (§5.2's bottom-to-top forwarding).
func TestSnapshotRoutingForwardsToEveryView(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	m := splitModel(t, 140, 40, view.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"})
	v, _ := newRoundView(m.env(), "webshop", 0)
	rv := v.(roundView)
	m.stack = []View{rv, fakeView{}}

	res, _ := m.Update(tabMsg{name: "webshop", round: rv.pane.detail.round, t: tabReport, content: tabContent{loaded: true, body: "snap"}})
	m = res.(Model)

	got := m.stack[0].(roundView)
	if !got.pane.detail.cache[tabReport].loaded || got.pane.detail.cache[tabReport].body != "snap" {
		t.Errorf("the round view under the pushed view must receive the tabMsg: %+v", got.pane.detail.cache[tabReport])
	}
}

// TestHelpListsGlobalAndViewKeys: the help modal names the three columns and
// the top view's own keys. Ported for O2 (the full-screen help list): the
// labels and the chip-padded entries replaced the old "global"/"view"
// sections.
func TestHelpListsGlobalAndViewKeys(t *testing.T) {
	m := splitModel(t, 140, 40, view.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"})
	rv, _ := newRoundView(m.env(), "webshop", 0)
	m.stack = append(m.stack, rv)

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = res.(Model)
	if !m.help {
		t.Fatal("? must open help")
	}

	body := plain(m.helpBody(m.env(), bodyHeight(m.env())))
	for _, want := range []string{"MOVE & VIEW", "ACT ON THE ROW", "ANYWHERE", "command", "filter", "help", "back", "quit / back", "round", "next tab"} {
		if !strings.Contains(body, want) {
			t.Errorf("help must list %q:\n%s", want, body)
		}
	}
}

type sentinelMsg struct{}

// TestPushDeliversInitAfterPush verifies that push returns a pushMsg, not a
// tea.BatchMsg, and that Model.Update grows the stack and returns the init cmd.
func TestPushDeliversInitAfterPush(t *testing.T) {
	v := fakeView{}
	init := func() tea.Msg { return sentinelMsg{} }
	cmd := push(v, init)
	msg := cmd()
	pushM, ok := msg.(pushMsg)
	if !ok {
		t.Fatalf("push(v, init) result must be pushMsg, got %T", msg)
	}
	if _, ok := msg.(tea.BatchMsg); ok {
		t.Fatalf("push(v, init) must not return tea.BatchMsg")
	}

	m := splitModel(t, 140, 40, view.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"})
	origDepth := len(m.stack)
	res, returnedCmd := m.Update(pushM)
	m = res.(Model)
	if len(m.stack) != origDepth+1 {
		t.Errorf("stack depth = %d, want %d", len(m.stack), origDepth+1)
	}
	if returnedCmd == nil {
		t.Fatal("expected non-nil cmd from pushMsg")
	}
	gotSentinel := returnedCmd()
	if _, ok := gotSentinel.(sentinelMsg); !ok {
		t.Errorf("cmd produced %T, want sentinelMsg", gotSentinel)
	}
}

// TestRootThenDeliversInitAfterRoot verifies that rootThen returns a rootMsg,
// not a tea.BatchMsg, and that Model.Update replaces the stack and returns the init cmd.
func TestRootThenDeliversInitAfterRoot(t *testing.T) {
	v := fakeView{}
	init := func() tea.Msg { return sentinelMsg{} }
	cmd := rootThen(init, v)
	msg := cmd()
	rootM, ok := msg.(rootMsg)
	if !ok {
		t.Fatalf("rootThen(init, v) result must be rootMsg, got %T", msg)
	}
	if _, ok := msg.(tea.BatchMsg); ok {
		t.Fatalf("rootThen(init, v) must not return tea.BatchMsg")
	}

	m := splitModel(t, 140, 40, view.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"})
	m.stack = append(m.stack, fakeView{}, fakeView{})
	res, returnedCmd := m.Update(rootM)
	m = res.(Model)
	if len(m.stack) != 1 {
		t.Errorf("stack depth = %d, want 1", len(m.stack))
	}
	if returnedCmd == nil {
		t.Fatal("expected non-nil cmd from rootMsg")
	}
	gotSentinel := returnedCmd()
	if _, ok := gotSentinel.(sentinelMsg); !ok {
		t.Errorf("cmd produced %T, want sentinelMsg", gotSentinel)
	}
}

// TestOpenRoundReplyNeverBeatsPush:
// - From :fleet with a working row and a plan file on a temp store, press enter. Execute the returned cmd once.
// - The message must be a pushMsg, with no tabMsg among the messages that cmd produces.
// - Feed it to Update, run the returned init, and feed its tabMsg to Update. The plan tab is loaded and tabInFlight is false.
func TestOpenRoundReplyNeverBeatsPush(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"
	b := newTestBinding(name)
	b.Round = 1
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	planPath := st.PromptPath(name, 1)
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("# Round 1 plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := view.Report{
		Bindings: []view.BindingStatus{
			{Name: name, Round: 1, Display: "ACTIVE", BuilderStatus: "working"},
		},
	}
	m := newModel(context.Background(), mastermindSource{rt}, Options{Interval: time.Second})
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	res, _ = m.Update(statusMsg{report: rep})
	m = res.(Model)

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	if cmd == nil {
		t.Fatal("expected cmd from enter")
	}
	msg := cmd()
	pushM, ok := msg.(pushMsg)
	if !ok {
		t.Fatalf("expected pushMsg from enter cmd, got %T", msg)
	}
	if _, ok := msg.(tea.BatchMsg); ok {
		t.Fatalf("expected pushMsg, not tea.BatchMsg")
	}
	if _, ok := msg.(tabMsg); ok {
		t.Fatalf("cmd must not produce tabMsg")
	}

	res, initCmd := m.Update(pushM)
	m = res.(Model)
	if len(m.stack) != 2 {
		t.Fatalf("stack depth = %d, want 2", len(m.stack))
	}
	if initCmd == nil {
		t.Fatal("expected init cmd from pushMsg")
	}
	initMsg := initCmd()
	tMsg, ok := initMsg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg from init cmd, got %T", initMsg)
	}

	res, _ = m.Update(tMsg)
	m = res.(Model)

	rv, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("top view must be roundView, got %T", m.top())
	}
	if !rv.pane.detail.cache[tabPrompt].loaded {
		t.Error("plan tab must be loaded")
	}
	if rv.pane.tabInFlight {
		t.Error("tabInFlight must be false")
	}
}

func TestFrameBlankRowUnderHeader(t *testing.T) {
	m := splitModel(t, 132, 34,
		view.BindingStatus{Name: "webshop", Round: 2, Display: "ACTIVE"},
	)
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) < 3 {
		t.Fatalf("Model.View() produced %d lines, want at least 3", len(lines))
	}
	// line index 1 is blank (spaces only)
	if strings.Trim(lines[1], " ") != "" || len(lines[1]) == 0 {
		t.Errorf("line index 1 must be blank (spaces only), got %q", lines[1])
	}
	// line index 2 is the context row
	ctxLeft, ctxRight := m.top().Context(m.env())
	expectedContext := fit(spread(ctxLeft, ctxRight, 132), 132)
	if lines[2] != expectedContext {
		t.Errorf("line index 2 must be context row:\n got  %q\n want %q", lines[2], expectedContext)
	}
}
