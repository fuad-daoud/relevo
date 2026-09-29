package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/fuad-daoud/relevo/internal/view"
)

func TestConfirmAnyKeyCancels(t *testing.T) {
	called := false
	onYes := func() tea.Msg {
		called = true
		return nil
	}
	c := confirmBox{
		title: "test",
		onYes: onYes,
	}

	for _, k := range []string{"q", "x", "enter"} {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd, closed := c.update(msg)
		if !closed {
			t.Errorf("key %q did not close confirmBox", k)
		}
		if cmd != nil {
			t.Errorf("key %q returned non-nil cmd: %v", k, cmd)
		}
	}

	_, cmd, closed := c.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if !closed {
		t.Errorf("y did not close confirmBox")
	}
	if cmd == nil {
		t.Fatalf("y returned nil cmd, want onYes")
	}
	cmd()
	if !called {
		t.Errorf("cmd was not onYes")
	}
}

func TestConfirmModalOneButtonRow(t *testing.T) {
	env := Env{Now: time.Now()}
	b := view.BindingStatus{
		Name:    "webshop",
		Round:   1,
		Branch:  "main",
		Display: "ACTIVE",
	}

	nCancelRE := regexp.MustCompile(`\bn\b\s+cancel`)

	cases := []struct {
		name string
		cmd  tea.Cmd
	}{
		{"stop", stopCmd(env, b)},
		{"done", doneCmd(env, b)},
		{"unbind", unbindCmd(env, b)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.cmd()
			box, ok := msg.(openOverlayMsg).ov.(confirmBox)
			if !ok {
				t.Fatalf("overlay is not confirmBox: %T", msg)
			}
			title, rows, want, danger := box.modal(140)
			modalLines := renderModal(title, rows, want, 140, danger)
			stripped := ansi.Strip(strings.Join(modalLines, "\n"))

			if strings.Contains(stripped, "· n cancel") {
				t.Errorf("%s confirm contains legacy '· n cancel':\n%s", tc.name, stripped)
			}
			matches := nCancelRE.FindAllString(stripped, -1)
			if len(matches) != 1 {
				t.Errorf("%s confirm modal has %d matches for n + cancel, want 1:\n%s", tc.name, len(matches), stripped)
			}
		})
	}
}
