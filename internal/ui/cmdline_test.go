package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
	"github.com/fuad-daoud/relevo/internal/view"
)

// dashJump builds a dashboard jump message.
func dashJump(name, id string, round int) dash.JumpMsg {
	return dash.JumpMsg{BindingName: name, BindingID: id, Round: round}
}

func cmdEnv(rows ...string) Env {
	bs := make([]view.BindingStatus, len(rows))
	for i, r := range rows {
		bs[i] = view.BindingStatus{Name: r, Display: "ACTIVE"}
	}
	return Env{Report: view.Report{Bindings: bs}, Loaded: true, Now: railNow}
}

// TestCmdLineMatchesRanks pins §4.7's matching and ranking: a
// case-insensitive subsequence, prefix first, then shorter name, then
// alphabetical, capped at 8.
func TestCmdLineMatchesRanks(t *testing.T) {
	env := cmdEnv("api", "atlas")

	c := newCmdLine()
	c.input.SetValue("ro")
	ms := c.matches(env)
	if len(ms) == 0 || ms[0].name != "round" {
		t.Fatalf("matches(\"ro\") = %+v, want round first", ms)
	}
	if len(ms) < 2 || ms[1].name != "rounds" {
		t.Errorf("matches(\"ro\")[1] = %q, want rounds", ms[1].name)
	}
	found := false
	for _, m := range ms {
		if m.name == "round api" {
			found = true
		}
	}
	if !found {
		t.Errorf("matches must include round <key> candidates: %+v", ms)
	}

	c.input.SetValue("ZZZ")
	if got := c.matches(env); len(got) != 0 {
		t.Errorf("no subsequence must match nothing, got %+v", got)
	}

	// The cap.
	c.input.SetValue("")
	if got := c.matches(env); len(got) > 8 {
		t.Errorf("%d matches, want at most 8", len(got))
	}
}

// TestCommandMatchesLiveBeforeDone: 7 done bindings with short names and 1
// live binding with a long name, typed `r`. The live entry survives the cap
// of 8 because live bindings sort before done ones (§2.3, §5).
func TestCommandMatchesLiveBeforeDone(t *testing.T) {
	var bindings []view.BindingStatus
	for i := 1; i <= 7; i++ {
		bindings = append(bindings, view.BindingStatus{
			Name: fmt.Sprintf("d%d", i), Round: 1, Display: "DONE",
		})
	}
	bindings = append(bindings, view.BindingStatus{
		Name: "live-binding-long", Round: 2, Display: "ACTIVE",
	})
	env := Env{Report: view.Report{Bindings: bindings}}

	c := newCmdLine()
	c.input.SetValue("r")
	ms := c.matches(env)
	if len(ms) != 8 {
		t.Fatalf("matches = %d, want the cap of 8: %+v", len(ms), ms)
	}
	found := false
	for _, m := range ms {
		if m.name == "round live-binding-long" {
			found = true
		}
	}
	if !found {
		t.Errorf("the live binding must be within the first 8: %+v", ms)
	}
}

// TestCmdLineTabCompletes: tab replaces the input with the selected
// candidate's name plus a space.
func TestCmdLineTabCompletes(t *testing.T) {
	c := newCmdLine().opened()
	c.input.SetValue("ro")
	next, _ := c.update(tea.KeyMsg{Type: tea.KeyTab}, cmdEnv("api"), prefs{})
	if got := next.typed(); got != "round" {
		t.Errorf("tab completion = %q, want %q", got, "round")
	}
}

// TestCmdLineExecuteCommands walks §6's command table.
func TestCmdLineExecuteCommands(t *testing.T) {
	env := cmdEnv("api")
	p := prefs{Sort: "attention"}

	t.Run("fleet", func(t *testing.T) {
		cmd := execLine("fleet", env, p)
		msg, ok := cmd().(rootMsg)
		if !ok {
			t.Fatalf("fleet must return rootMsg, got %T", cmd())
		}
		if len(msg.vs) != 1 {
			t.Fatalf("fleet must root one view, got %d", len(msg.vs))
		}
		if _, ok := msg.vs[0].(fleetView); !ok {
			t.Errorf("fleet must root a fleetView, got %T", msg.vs[0])
		}
	})

	t.Run("help", func(t *testing.T) {
		if _, ok := execLine("help", env, p)().(helpMsg); !ok {
			t.Error("help must return helpMsg")
		}
	})

	t.Run("quit", func(t *testing.T) {
		if _, ok := execLine("quit", env, p)().(tea.QuitMsg); !ok {
			t.Error("quit must return tea.Quit")
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		msg, ok := execLine("bogus", env, p)().(noticeMsg)
		if !ok {
			t.Fatalf("unknown command must notice, got %T", execLine("bogus", env, p)())
		}
		if !strings.Contains(msg.text, "unknown command") {
			t.Errorf("notice = %q", msg.text)
		}
	})

	t.Run("unknown binding", func(t *testing.T) {
		msg, ok := execLine("round nope", env, p)().(noticeMsg)
		if !ok {
			t.Fatalf("unknown binding must notice, got %T", execLine("round nope", env, p)())
		}
		if !strings.Contains(msg.text, `unknown binding "nope"`) {
			t.Errorf("notice = %q", msg.text)
		}
	})

	t.Run("rounds without a DB", func(t *testing.T) {
		st := store.New(t.TempDir())
		noDB := testEnv(mastermindSource{relevo.Runtime{Store: st}}, view.Report{}, 140, 40)
		msg, ok := execLine("rounds", noDB, p)().(noticeMsg)
		if !ok {
			t.Fatalf("rounds without a DB must notice, got %T", execLine("rounds", noDB, p)())
		}
		if !strings.Contains(msg.text, "no database") {
			t.Errorf("notice = %q", msg.text)
		}
	})

	t.Run("round live opens a round view", func(t *testing.T) {
		m := splitModel(t, 140, 40, view.BindingStatus{Name: "api", Round: 4, Display: "ACTIVE"})
		cmd := execLine("round api 2", m.env(), m.prefs)
		m = drain(t, m, cmd)
		rv, ok := m.top().(roundView)
		if !ok {
			t.Fatalf("round must push a round view, got %T", m.top())
		}
		if rv.pane.detail.name != "api" || rv.pane.detail.round != 2 {
			t.Errorf("detail = %s r%d, want api r2", rv.pane.detail.name, rv.pane.detail.round)
		}
	})
}

// TestOpenRoundNotFound: a jump whose name is in neither the fleet nor the
// database is a notice (§5.4).
func TestOpenRoundNotFound(t *testing.T) {
	st := store.New(t.TempDir())
	env := testEnv(mastermindSource{relevo.Runtime{Store: st}}, view.Report{}, 140, 40)
	msg, ok := openRound(env, dashJump("ghost", "h1", 1))().(noticeMsg)
	if !ok {
		t.Fatalf("openRound must notice, got %T", openRound(env, dashJump("ghost", "h1", 1))())
	}
	if !strings.Contains(msg.text, "ghost not found") {
		t.Errorf("notice = %q, want %q", msg.text, "ghost not found")
	}
}

// TestOpenRoundLive: a live row resolves to a roundOpenMsg with its key.
func TestOpenRoundLive(t *testing.T) {
	st := store.New(t.TempDir())
	rep := view.Report{Bindings: []view.BindingStatus{{Name: "persist", Round: 3, Display: "ACTIVE"}}}
	env := testEnv(mastermindSource{relevo.Runtime{Store: st}}, rep, 140, 40)
	msg, ok := openRound(env, dashJump("persist", "b1", 2))().(roundOpenMsg)
	if !ok {
		t.Fatalf("openRound must return roundOpenMsg, got %T", openRound(env, dashJump("persist", "b1", 2))())
	}
	if msg.key != "persist" || msg.round != 2 || msg.hist != nil {
		t.Errorf("roundOpenMsg = %+v, want key persist round 2", msg)
	}
}

// :servers completes from the command table and dispatches to the view.
func TestCmdLineServersCompletesAndDispatches(t *testing.T) {
	c := newCmdLine()
	c.input.SetValue("ser")
	ms := c.matches(cmdEnv())
	if len(ms) == 0 || ms[0].name != "servers" {
		t.Fatalf("matches(\"ser\") = %+v, want servers first", ms)
	}

	env := candActionEnv(&fakeActions{}, view.Report{})
	cmd := execLine("servers", env, prefs{})
	if cmd == nil {
		t.Fatal(":servers must return a command")
	}
	msg, ok := cmd().(rootMsg)
	if !ok {
		t.Fatalf(":servers gave %T, want a root message", msg)
	}
	if len(msg.vs) != 1 {
		t.Fatalf(":servers rooted %d views, want one", len(msg.vs))
	}
	if _, ok := msg.vs[0].(serversView); !ok {
		t.Errorf(":servers rooted %T, want a serversView", msg.vs[0])
	}
}
