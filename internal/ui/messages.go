package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// push returns a command that pushes v and then runs init.
func push(v widget.View, init tea.Cmd) tea.Cmd {
	return func() tea.Msg { return widget.PushMsg{View: v, Init: init} }
}

// pop returns a command that pops the top view.
func pop() tea.Cmd { return func() tea.Msg { return widget.PopMsg{} } }

// root returns a command that replaces the whole stack.
func root(vs ...widget.View) tea.Cmd { return rootThen(nil, vs...) }

// rootThen returns a command that replaces the whole stack and then runs init.
func rootThen(init tea.Cmd, vs ...widget.View) tea.Cmd {
	return func() tea.Msg { return widget.RootMsg{Views: vs, Init: init} }
}

// notice returns a command that sets the sticky footer notice.
func notice(text string) tea.Cmd { return func() tea.Msg { return widget.NoticeMsg{Text: text} } }
