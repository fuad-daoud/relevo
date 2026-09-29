package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// listItem is one selectable row of a listBox.
type listItem struct {
	name, status, note string
	statusStyle        lipgloss.Style
	disabled           bool
}

// listBox is a single-column list overlay (§3.5): ↑↓ move to the next enabled
// item, enter picks it, esc cancels.
type listBox struct {
	kind, submit string
	header, note []string
	items        []listItem
	sel          int // index of a selectable item, or -1 when none is selectable
	onPick       func(name string) tea.Cmd
}

// keys is the list's footer (§3.5).
func (l listBox) keys() []KeyHelp {
	submit := l.submit
	if submit == "" {
		submit = "choose"
	}
	return []KeyHelp{{"↑↓", "choose"}, {"enter", submit}, {"esc", "cancel"}}
}

func (l listBox) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return l, nil, true
	case "up", "ctrl+p":
		l.sel = l.step(-1)
		return l, nil, false
	case "down", "ctrl+n":
		l.sel = l.step(1)
		return l, nil, false
	case "enter":
		item, ok := l.current()
		if !ok {
			return l, nil, false
		}
		return l, l.onPick(item.name), true
	}
	return l, nil, false
}

// current is the selected item, when it is enabled.
func (l listBox) current() (listItem, bool) {
	if l.sel < 0 || l.sel >= len(l.items) || l.items[l.sel].disabled {
		return listItem{}, false
	}
	return l.items[l.sel], true
}

// step moves the selection by dir through the enabled items, wrapping. It
// stays put when no item is enabled.
func (l listBox) step(dir int) int {
	n := len(l.items)
	if n == 0 {
		return -1
	}
	i := l.sel
	for c := 0; c < n; c++ {
		i = ((i+dir)%n + n) % n
		if !l.items[i].disabled {
			return i
		}
	}
	return l.sel
}

func (l listBox) view(width int) []string {
	_, rows, _, _ := l.modal(width)
	return rows
}

// modal renders the list (§3.5): the header, one row per item, the note and
// the buttons. A row is the name (26 cells), the status (16 cells, in its
// style) and the note in faint; a disabled row is faint throughout.
func (l listBox) modal(width int) (string, []string, int, bool) {
	const want = 72
	innerW := modalInnerW(want, width)
	rows := append([]string(nil), l.header...)
	rows = append(rows, "")

	enabled := false
	for _, item := range l.items {
		if !item.disabled {
			enabled = true
			break
		}
	}
	if !enabled {
		rows = append(rows, faintStyle.Render("no other candidate is ready"))
	}
	for i, item := range l.items {
		name := pad(item.name, 26)
		status := pad(item.status, 16)
		var row string
		switch {
		case item.disabled:
			row = faintStyle.Render(name + " " + status + " " + item.note)
		case i == l.sel:
			// The band covers the whole row: build the selected row from plain
			// text and render it once, as modalRow does, so no inner reset ends
			// the band (§2.2).
			row = selBandStyle.Foreground(textStyle.GetForeground()).Bold(true).
				Render(fit(name+" "+status+" "+item.note, innerW))
		default:
			row = name + " " + item.statusStyle.Render(status) + " " + faintStyle.Render(item.note)
		}
		rows = append(rows, row)
	}
	rows = append(rows, "")
	rows = append(rows, l.note...)
	rows = append(rows, "")
	submit := l.submit
	if submit == "" {
		submit = "choose"
	}
	rows = append(rows, chip(kbdStyle, "enter")+" "+mutedStyle.Render(submit)+
		"   "+chip(kbdStyle, "esc")+mutedStyle.Render(" cancel"))
	return l.kind, rows, want, false
}

// gateProvider is a gate's provider: the second '/'-segment of its token
// (§3.5).
func gateProvider(token string) string {
	parts := strings.Split(token, "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return token
}
