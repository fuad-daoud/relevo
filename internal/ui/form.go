package ui

import (
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// formField is one field of a formBox: its label, its input, an optional faint
// hint line under it, and an optional validator (nil means anything goes).
type formField struct {
	label    string
	input    textinput.Model
	hint     string             // faint line under the field; "" for none
	validate func(string) error // nil = anything goes
}

// formBox is a multi-field form overlay (§3.3): one field per row, tab moves
// focus, enter validates every field and submits, esc cancels.
type formBox struct {
	kind     string // box title
	header   []string
	note     []string // faint lines under the fields
	fields   []formField
	focus    int
	err      string
	submit   string // label for enter
	onSubmit func(values []string) tea.Cmd
}

// keys is the form's footer: enter submits, tab moves, esc cancels (§3.3).
func (f formBox) keys() []KeyHelp {
	submit := f.submit
	if submit == "" {
		submit = "submit"
	}
	return []KeyHelp{{"enter", submit}, {"tab", "next field"}, {"esc", "cancel"}}
}

func (f formBox) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return f, nil, true
	case "tab":
		return f.setFocus(f.focus + 1), nil, false
	case "shift+tab", "back_tab":
		return f.setFocus(f.focus - 1), nil, false
	case "enter":
		return f.submitForm()
	}
	if len(f.fields) == 0 {
		return f, nil, false
	}
	var cmd tea.Cmd
	f.fields[f.focus].input, cmd = f.fields[f.focus].input.Update(k)
	return f, cmd, false
}

// setFocus moves focus to field i, wrapping, and focuses its input while
// blurring every other.
func (f formBox) setFocus(i int) formBox {
	n := len(f.fields)
	if n == 0 {
		return f
	}
	i = ((i % n) + n) % n
	for j := range f.fields {
		if j == i {
			f.fields[j].input.Focus()
		} else {
			f.fields[j].input.Blur()
		}
	}
	f.focus = i
	return f
}

// submitForm validates every field in order; the first error keeps the box
// open with that field focused. With every field valid it closes and returns
// onSubmit(values) (§3.3).
func (f formBox) submitForm() (overlay, tea.Cmd, bool) {
	for i, field := range f.fields {
		if field.validate == nil {
			continue
		}
		if err := field.validate(field.input.Value()); err != nil {
			f.err = err.Error()
			return f.setFocus(i), nil, false
		}
	}
	f.err = ""
	values := make([]string, len(f.fields))
	for i, field := range f.fields {
		values[i] = field.input.Value()
	}
	if f.onSubmit == nil {
		return f, nil, true
	}
	return f, f.onSubmit(values), true
}

func (f formBox) view(width int) []string {
	_, rows, _, _ := f.modal(width)
	return rows
}

// modal renders the form box (§3.3): the header, then per field its label
// padded to 10 (accent when focused, faint otherwise), the input (focused on
// the selection band), its hint, then the note, the error and the buttons.
func (f formBox) modal(width int) (string, []string, int, bool) {
	const want = 72
	rows := append([]string(nil), f.header...)
	rows = append(rows, "")
	for i, field := range f.fields {
		labelStyle := faintStyle
		inputView := field.input.View()
		if i == f.focus {
			labelStyle = accentStyle
			inputView = selBandStyle.Render(inputView)
		}
		rows = append(rows, labelStyle.Render(pad(field.label, 10))+inputView)
		if field.hint != "" {
			rows = append(rows, faintStyle.Render(field.hint))
		}
		rows = append(rows, "")
	}
	for _, line := range f.note {
		rows = append(rows, faintStyle.Render(line))
	}
	if f.err != "" {
		rows = append(rows, errorStyle.Render(f.err))
	}
	rows = append(rows, "")
	submit := f.submit
	if submit == "" {
		submit = "submit"
	}
	rows = append(rows, chip(kbdStyle, "enter")+" "+mutedStyle.Render(submit)+
		"   "+chip(kbdStyle, "tab")+mutedStyle.Render(" next field")+
		"   "+chip(kbdStyle, "esc")+mutedStyle.Render(" cancel"))
	return f.kind, rows, want, false
}

// staticCursor is set by TestMain, because a blinking cursor's timer command
// blocks about half a second per keystroke when a test runs the returned
// commands synchronously.
var staticCursor bool

// newTextInput builds a text input, with the cursor's blink suppressed while
// staticCursor is set.
func newTextInput() textinput.Model {
	in := textinput.New()
	if staticCursor {
		in.Cursor.SetMode(cursor.CursorStatic)
	}
	return in
}

// newFormInput builds one field input, focused only when it is the first.
func newFormInput(focused bool) textinput.Model {
	in := newTextInput()
	in.Prompt = ""
	if focused {
		in.Focus()
	}
	return in
}
