package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
)

// formLabel is one form row's label: padded to 11 cells, accent when its field
// is focused and faint otherwise. 11 matches formBox's 10-column label plus its
// one separating space, so the two forms line up.
func formLabel(label string, focused bool) string {
	style := faintStyle
	if focused {
		style = accentStyle
	}
	return style.Render(pad(label, 11))
}

// formChips renders one row of chips ("agy  claude  codex"): sel is the
// selected chip's index, or -1 for none. The selected chip wears the accent
// style, the rest the kbd style, and two spaces join them. disabled draws every
// chip faint, for a row the user cannot change.
func formChips(items []string, sel int, disabled bool) string {
	parts := make([]string, 0, len(items))
	for i, item := range items {
		switch {
		case disabled:
			parts = append(parts, chip(faintStyle, item))
		case i == sel:
			parts = append(parts, chip(chipAccentStyle, item))
		default:
			parts = append(parts, chip(kbdStyle, item))
		}
	}
	return strings.Join(parts, "  ")
}

// formInput renders one text field padded to width. A focused field is drawn
// on the selection band; an empty, unfocused field shows its placeholder in
// faint instead of its (empty) value.
func formInput(in textinput.Model, focused bool, placeholder string, width int) string {
	if in.Value() == "" && placeholder != "" && !focused {
		return faintStyle.Render(pad(placeholder, width))
	}
	view := fit(in.View(), width)
	if focused {
		return selBandStyle.Render(view)
	}
	return view
}

// formSubRowIndent is a form row's value column: formLabel's 11 cells, the
// 10-column label plus its one separating space.
const formSubRowIndent = 11

// formSubRow puts a row that sits under a form field -- its chips, its `new
// provider` chip or its error -- in the value column, so it lines up with the
// field's value above it (§1, round 6). An empty row stays empty, so the form
// adds no blank row for a field with nothing to show.
func formSubRow(s string) string {
	if s == "" {
		return ""
	}
	return strings.Repeat(" ", formSubRowIndent) + s
}

// formError is one field's error: red, indented to the value column.
func formError(msg string) string {
	return redStyle.Render(formSubRow(msg))
}

// formKeys renders a form's keys: kbd chips with muted labels, six spaces
// apart. A key named in disabled is faint, its chip and its label both.
func formKeys(keys []KeyHelp, disabled map[string]bool) string {
	parts := make([]string, 0, len(keys))
	for _, kh := range keys {
		chipStyle, labelStyle := kbdStyle, mutedStyle
		if disabled[kh.Key] {
			chipStyle, labelStyle = faintStyle, faintStyle
		}
		parts = append(parts, chip(chipStyle, kh.Key)+" "+labelStyle.Render(kh.Help))
	}
	return strings.Join(parts, "      ")
}
