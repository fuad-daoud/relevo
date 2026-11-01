package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

func push(v widget.View, init tea.Cmd) tea.Cmd {
	return func() tea.Msg { return widget.PushMsg{View: v, Init: init} }
}

func pop() tea.Cmd { return func() tea.Msg { return widget.PopMsg{} } }

func root(vs ...widget.View) tea.Cmd { return rootThen(nil, vs...) }

func rootThen(init tea.Cmd, vs ...widget.View) tea.Cmd {
	return func() tea.Msg { return widget.RootMsg{Views: vs, Init: init} }
}

func notice(text string) tea.Cmd { return func() tea.Msg { return widget.NoticeMsg{Text: text} } }
