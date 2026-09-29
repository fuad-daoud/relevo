package ui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// settingsLabelWidth is a settings form field's label column: wider
// than formLabel's 11 cells, because these rows carry longer labels
// ("repair rounds", "progress_interval").
const settingsLabelWidth = 17

// settingsFieldWidth is a text field's own width, left of its faint hint:
// fixed and narrow, since every value here is short (a
// command, a duration, a small integer), leaving room on the row for the
// hint instead of stretching the input to the modal's edge.
const settingsFieldWidth = 24

// settingField is one field of a settingsForm: a chip row when chips
// is non-nil, a text input otherwise.
type settingField struct {
	label        string                    // left column, e.g. "command", "max_switches"
	path         string                    // the PolicySet path this field writes
	chips        []string                  // non-nil: a chip row; nil: a text input
	sel, origSel int                       // chip rows
	input        textinput.Model           // text rows; prefilled with the stored value, "" when unset
	orig         string                    // the prefill, to detect a change
	hint         string                    // "default 10m": shown right of the input, faint
	parse        func(string) (any, error) // text rows: "" -> (nil, nil) means delete; else the JSON value or an error
	value        func(sel int) any         // chip rows: the JSON value for a selection
	disabled     func(f settingsForm) bool // nil = never; true = drawn faint, skipped by focus, validation and sets()
}

// settingsForm is the group form overlay: one form per group of plain
// settings (rounds, check, timing, max_builders).
type settingsForm struct {
	title   string
	fields  []settingField
	focus   int
	touched []bool
	tried   bool
	doc     relevo.ConfigDoc
	env     Env
	note    string // one dim sentence row, may be ""
}

// settingByKey is the row named key from Settings(doc, cpus), or a zero
// Setting when key is unknown.
func settingByKey(doc relevo.ConfigDoc, cpus int, key string) relevo.Setting {
	for _, s := range relevo.Settings(doc, cpus) {
		if s.Key == key {
			return s
		}
	}
	return relevo.Setting{}
}

// textSettingField builds one text field, prefilled with s.Value when s.Set,
// "" otherwise, with the hint "default <s.Default>". path defaults to
// s.Key; a caller whose PolicySet path differs (the "_ms" duration fields)
// overwrites it.
func textSettingField(label string, s relevo.Setting, parse func(string) (any, error)) settingField {
	orig := ""
	if s.Set {
		orig = s.Value
	}
	in := newFormInput(false)
	in.SetValue(orig)
	in.CursorEnd()
	return settingField{
		label: label,
		path:  s.Key,
		input: in,
		orig:  orig,
		hint:  "default " + s.Default,
		parse: parse,
	}
}

// parseNonNegInt is the int parser the rounds and check forms use: ""
// deletes, else a non-negative integer or msg.
func parseNonNegInt(msg string) func(string) (any, error) {
	return func(s string) (any, error) {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, errors.New(msg)
		}
		return n, nil
	}
}

// parseIntMin is max_builders' int parser: "" deletes, else an integer
// >= min or msg.
func parseIntMin(min int, msg string) func(string) (any, error) {
	return func(s string) (any, error) {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < min {
			return nil, errors.New(msg)
		}
		return n, nil
	}
}

// parseDuration is every duration field's parser: "" deletes, else a
// positive duration stored as milliseconds.
func parseDuration(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return nil, errors.New("a duration like 15m or 1h30m")
	}
	return int(d.Milliseconds()), nil
}

// parseCommand is gate.default's parser: "" deletes, else the trimmed
// command.
func parseCommand(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return s, nil
}

// newSettingsForm builds the form named form, focused on focusKey's field.
// form is one of the closed list: rounds, check, timing,
// max_builders, scope, serve.scope, classify.
func newSettingsForm(env Env, doc relevo.ConfigDoc, form, focusKey string) settingsForm {
	cpus := numCPU()
	byKey := func(key string) relevo.Setting { return settingByKey(doc, cpus, key) }

	var fields []settingField
	var note string
	var title string
	focus := 0

	switch form {
	case "rounds":
		title = "rounds"
		msField := textSettingField("max_switches", byKey("max_switches"), parseNonNegInt("a whole number, 0 or more"))

		tierChips := []string{"read", "edit", "yolo"}
		tierSel := candIndex(tierChips, byKey("max_tier").Value)
		if tierSel < 0 {
			tierSel = 0
		}
		tierField := settingField{
			label: "max_tier", path: "max_tier",
			chips: tierChips, sel: tierSel, origSel: tierSel,
			value: func(sel int) any {
				if sel < 0 || sel >= len(tierChips) {
					return nil
				}
				return tierChips[sel]
			},
		}

		verifyChips := []string{"on", "off"}
		verifySel := 1
		if byKey("verify.default").Value == "on" {
			verifySel = 0
		}
		verifyField := settingField{
			label: "verify.default", path: "verify.default",
			chips: verifyChips, sel: verifySel, origSel: verifySel,
			value: func(sel int) any { return sel == 0 },
		}

		amsField := textSettingField("artifact_max_mb", byKey("artifact_max_mb"), parseNonNegInt("a whole number, 0 or more"))

		fields = []settingField{msField, tierField, verifyField, amsField}
		switch focusKey {
		case "max_switches":
			focus = 0
		case "max_tier":
			focus = 1
		case "verify.default":
			focus = 2
		case "artifact_max_mb":
			focus = 3
		}

	case "check":
		title = "check"
		cmdField := textSettingField("command", byKey("gate.default"), parseCommand)
		cmdField.path = "gate.default"

		toField := textSettingField("timeout", byKey("gate.timeout"), parseDuration)
		toField.path = "gate.timeout_ms"

		rrField := textSettingField("repair rounds", byKey("gate.regate"), parseNonNegInt("a whole number, 0 or more"))
		rrField.path = "gate.regate"

		fields = []settingField{cmdField, toField, rrField}
		note = checkSentence(doc)
		switch focusKey {
		case "gate.default":
			focus = 0
		case "gate.timeout":
			focus = 1
		case "gate.regate":
			focus = 2
		}

	case "timing":
		title = "timing"
		defs := []struct{ label, key, path string }{
			{"limit_gate_default", "limit_gate_default", "limit_gate_default_ms"},
			{"stall_after", "stall_after", "stall_after_ms"},
			{"progress_interval", "progress_interval", "progress_interval_ms"},
			{"explore_after", "explore_after", "explore_after_ms"},
			{"stale_after", "stale_after", "stale_after_ms"},
		}
		for i, d := range defs {
			f := textSettingField(d.label, byKey(d.key), parseDuration)
			f.path = d.path
			fields = append(fields, f)
			if d.key == focusKey {
				focus = i
			}
		}

	case "max_builders":
		title = "serve.max_builders"
		f := textSettingField("max_builders", byKey("serve.max_builders"), parseIntMin(1, "a whole number, 1 or more"))
		f.path = "serve.max_builders"
		fields = []settingField{f}
		focus = 0

	case "scope", "serve.scope":
		fields, note, title = newScopeFields(doc, form)

	case "classify":
		fields, note, title = newClassifyFields(doc)
	}

	f := settingsForm{
		title:   title,
		fields:  fields,
		touched: make([]bool, len(fields)),
		doc:     doc,
		env:     env,
		note:    note,
	}
	return f.setFocus(focus)
}

// setFocus moves focus to field i, wrapping, focusing that field's input
// (chip rows have none to focus).
func (f settingsForm) setFocus(i int) settingsForm {
	n := len(f.fields)
	if n == 0 {
		return f
	}
	i = ((i % n) + n) % n
	for j := range f.fields {
		fld := f.fields[j]
		if fld.chips == nil {
			if j == i {
				fld.input.Focus()
			} else {
				fld.input.Blur()
			}
			f.fields[j] = fld
		}
	}
	f.focus = i
	return f
}

// footerKeys is the form's keys: enter, tab, esc, plus ←→ choose when
// any field is a chip row.
func (f settingsForm) footerKeys() []KeyHelp {
	keys := []KeyHelp{{"enter", "save"}, {"tab", "next field"}, {"esc", "cancel"}}
	for _, fld := range f.fields {
		if fld.chips != nil {
			return append(keys, KeyHelp{"←→", "choose"})
		}
	}
	return keys
}

// keys is the form's own keys, shown in the footer through overlayKeyer.
func (f settingsForm) keys() []KeyHelp { return f.footerKeys() }

// moveChip is left/right on a chip row.
func (f settingsForm) moveChip(delta int) settingsForm {
	fld := f.fields[f.focus]
	if len(fld.chips) == 0 {
		return f
	}
	fld.sel = ((fld.sel+delta)%len(fld.chips) + len(fld.chips)) % len(fld.chips)
	f.fields[f.focus] = fld
	f.touched[f.focus] = true
	return f
}

// nextFocusable is the next field index in direction delta (+1 for tab, -1
// for shift+tab) that is not disabled, wrapping around at most once through
// every field; it returns from unchanged when every field is disabled.
func (f settingsForm) nextFocusable(from, delta int) int {
	n := len(f.fields)
	if n == 0 {
		return from
	}
	i := from
	for range f.fields {
		i = ((i+delta)%n + n) % n
		fld := f.fields[i]
		if fld.disabled == nil || !fld.disabled(f) {
			return i
		}
	}
	return from
}

// update handles one key: esc cancels, tab moves focus, left/right
// move a chip row's selection, typing edits a text field, enter submits.
func (f settingsForm) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return f, nil, true
	case "tab":
		return f.setFocus(f.nextFocusable(f.focus, 1)), nil, false
	case "shift+tab", "back_tab":
		return f.setFocus(f.nextFocusable(f.focus, -1)), nil, false
	case "enter":
		return f.submit()
	case "left":
		return f.moveChip(-1), nil, false
	case "right":
		return f.moveChip(1), nil, false
	}
	if len(f.fields) == 0 {
		return f, nil, false
	}
	fld := f.fields[f.focus]
	if fld.chips != nil {
		return f, nil, false
	}
	var cmd tea.Cmd
	fld.input, cmd = fld.input.Update(k)
	f.fields[f.focus] = fld
	f.touched[f.focus] = true
	return f, cmd, false
}

// fieldError is field i's live parse error, nil for a chip row or a field
// with nothing to parse.
func (f settingsForm) fieldError(i int) error {
	fld := f.fields[i]
	if fld.chips != nil || fld.parse == nil {
		return nil
	}
	if fld.disabled != nil && fld.disabled(f) {
		return nil
	}
	_, err := fld.parse(fld.input.Value())
	return err
}

// validSets is every changed field's PolicySet: a text field
// is changed when its value differs from its prefill, a chip row when its
// selection differs from its start. A disabled field is skipped entirely, so
// a pending edit made before its field became disabled cannot slip through.
// Turning classify's provider chip off writes classify itself, not
// classify.provider, so a stored model or threshold does not linger behind
// it. ok is false when a text field's value fails to parse.
func (f settingsForm) validSets() ([]relevo.PolicySet, bool) {
	var out []relevo.PolicySet
	ok := true
	for _, fld := range f.fields {
		if fld.disabled != nil && fld.disabled(f) {
			continue
		}
		if fld.chips != nil {
			if fld.sel != fld.origSel {
				path, val := fld.path, fld.value(fld.sel)
				if path == "classify.provider" && val == nil {
					path = "classify"
				}
				out = append(out, relevo.PolicySet{Path: path, Value: val})
			}
			continue
		}
		if fld.input.Value() == fld.orig {
			continue
		}
		val, err := fld.parse(fld.input.Value())
		if err != nil {
			ok = false
			continue
		}
		out = append(out, relevo.PolicySet{Path: fld.path, Value: val})
	}
	if ok {
		out = f.scopeBlockPrune(out)
	}
	return out, ok
}

// scopeBlockPrune collapses the scope and serve.scope forms' per-field deltas
// into one delete of the whole block when every field but enabled ends empty
// and enabled ends on: the row then returns to its plain default instead of
// leaving an explicit {"enabled":true} behind that EditPolicy's per-key
// pruning, which only removes a parent once every one of its keys is gone,
// cannot see past.
func (f settingsForm) scopeBlockPrune(out []relevo.PolicySet) []relevo.PolicySet {
	if len(out) == 0 || (f.title != "scope" && f.title != "serve.scope") {
		return out
	}
	var enabled *settingField
	allEmpty := true
	for i := range f.fields {
		fld := &f.fields[i]
		if fld.chips != nil {
			enabled = fld
			continue
		}
		if strings.TrimSpace(fld.input.Value()) != "" {
			allEmpty = false
		}
	}
	if enabled == nil || !allEmpty || enabled.value(enabled.sel) != true {
		return out
	}
	return []relevo.PolicySet{{Path: strings.TrimSuffix(enabled.path, ".enabled"), Value: nil}}
}

// settingsMessageKey is a PolicySet path's setting key for the submit
// message: every path is its key already, except the duration fields'
// "_ms" suffix.
func settingsMessageKey(path string) string { return strings.TrimSuffix(path, "_ms") }

// message is the submit message: "set " then each changed field's
// "<key> <new display value>", comma-joined; a cleared text field reads
// "<key> default".
func (f settingsForm) message() string {
	var parts []string
	for _, fld := range f.fields {
		if fld.disabled != nil && fld.disabled(f) {
			continue
		}
		key := settingsMessageKey(fld.path)
		if fld.chips != nil {
			if fld.sel == fld.origSel {
				continue
			}
			parts = append(parts, key+" "+fld.chips[fld.sel])
			continue
		}
		if fld.input.Value() == fld.orig {
			continue
		}
		v := strings.TrimSpace(fld.input.Value())
		if v == "" {
			parts = append(parts, key+" default")
		} else {
			parts = append(parts, key+" "+v)
		}
	}
	return "set " + strings.Join(parts, ", ")
}

// submit is enter: no sets, or EditPolicy's ErrNoChange, closes with
// "nothing changed"; a FieldError (or a field that fails to parse) keeps the
// form open; otherwise it applies the edit and closes.
func (f settingsForm) submit() (overlay, tea.Cmd, bool) {
	sets, ok := f.validSets()
	if !ok {
		f.tried = true
		return f, nil, false
	}
	if len(sets) == 0 {
		return f, notice("nothing changed"), true
	}
	edit, err := relevo.EditPolicy(f.doc, sets, f.message())
	switch {
	case err == nil:
		return f, runAction(f.env.Ctx, "set "+f.title, f.title, func(ctx context.Context) Result {
			return f.env.Actions.ApplyConfig(ctx, edit)
		}), true
	case errors.Is(err, relevo.ErrNoChange):
		return f, notice("nothing changed"), true
	}
	f.tried = true
	return f, nil, false
}

func (f settingsForm) view(width int) []string {
	_, rows, _, _ := f.modal(width)
	return rows
}

// modal draws the form box: title is the form's identifier
// (rounds, check, timing or serve.max_builders); rows are a blank, then per
// field the label, the input or chips and the hint, a per-field error when
// touched or tried, and a blank; then the note, a form-level error from
// EditPolicy, and the key row. The enter chip is faint while an error stands.
func (f settingsForm) modal(width int) (string, []string, int, bool) {
	const want = 90
	innerW := modalInnerW(want, width)
	fieldW := settingsFieldWidth
	if roomW := innerW - settingsLabelWidth; fieldW > roomW {
		fieldW = roomW
	}
	if fieldW < 1 {
		fieldW = 1
	}

	sets, fieldsOK := f.validSets()
	var formErr string
	if fieldsOK && len(sets) > 0 {
		if _, err := relevo.EditPolicy(f.doc, sets, "x"); err != nil && !errors.Is(err, relevo.ErrNoChange) {
			var fe *relevo.FieldError
			if errors.As(err, &fe) {
				formErr = relevo.HumanPolicyError(fe)
			}
		}
	}

	rows := []string{""}
	for i, fld := range f.fields {
		focused := i == f.focus
		disabled := fld.disabled != nil && fld.disabled(f)
		var valuePart string
		if fld.chips != nil {
			valuePart = formChips(fld.chips, fld.sel, disabled)
		} else {
			valuePart = formInput(fld.input, focused, "", fieldW)
			if disabled {
				valuePart = faintStyle.Render(valuePart)
			}
			if fld.hint != "" {
				valuePart += "   " + faintStyle.Render(fld.hint)
			}
		}
		rows = append(rows, settingsFormLabel(fld.label, focused)+valuePart)
		if err := f.fieldError(i); err != nil && (f.touched[i] || f.tried) {
			rows = append(rows, formError(err.Error()))
		}
		rows = append(rows, "")
	}
	if f.note != "" {
		rows = append(rows, mutedStyle.Render(f.note))
		rows = append(rows, "")
	}
	if formErr != "" {
		rows = append(rows, formError(formErr))
		rows = append(rows, "")
	}
	rows = append(rows, formKeys(f.footerKeys(), map[string]bool{"enter": !fieldsOK || formErr != ""}))
	return f.title, rows, want, false
}

// settingsFormLabel is a settings form row's label, padded wider than
// formLabel's (17 cells, not 11) because these labels run longer.
func settingsFormLabel(label string, focused bool) string {
	style := faintStyle
	if focused {
		style = accentStyle
	}
	return style.Render(pad(label, settingsLabelWidth))
}
