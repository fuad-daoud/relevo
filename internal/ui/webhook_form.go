package ui

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// webhookFormats are the format chip row's values, in order. json is the
// default and, like the stored policy, is written as "".
var webhookFormats = []string{"json", "slack", "discord"}

// webhookForm is the add/edit webhook modal: the url, the format chips, the
// events filter, and the two dim reference rows under them.
type webhookForm struct {
	title   string
	editing int // the stored index an edit replaces, -1 when adding
	fields  []settingField
	focus   int
	touched []bool
	tried   bool
	doc     relevo.ConfigDoc
	env     Env
	formErr string // a FieldError from SetWebhooks, kept while the form stays open
}

// newWebhookForm builds the add (editing < 0) or edit form over doc, prefilled
// from the hook an edit replaces.
func newWebhookForm(env Env, doc relevo.ConfigDoc, editing int) webhookForm {
	hooks := webhooksOf(doc)
	var hook policy.Webhook
	if editing >= 0 && editing < len(hooks) {
		hook = hooks[editing]
	}

	urlIn := newFormInput(false)
	urlIn.SetValue(hook.URL)
	urlIn.CursorEnd()

	formatSel := candIndex(webhookFormats, webhookFormatText(hook.Format))
	if formatSel < 0 {
		formatSel = 0
	}

	eventsText := strings.Join(hook.Events, ", ")
	eventsIn := newFormInput(false)
	eventsIn.SetValue(eventsText)
	eventsIn.CursorEnd()

	title := "add webhook"
	if editing >= 0 {
		title = "edit webhook"
	}
	f := webhookForm{
		title:   title,
		editing: editing,
		fields: []settingField{
			{label: "url", input: urlIn, orig: hook.URL, parse: parseWebhookURL},
			{
				label: "format", chips: webhookFormats, sel: formatSel, origSel: formatSel,
				value: func(sel int) any { return webhookFormat(sel) },
			},
			{label: "events", input: eventsIn, orig: eventsText, hint: "every event when empty", parse: parseWebhookEvents},
		},
		touched: make([]bool, 3),
		doc:     doc,
		env:     env,
	}
	return f.setFocus(0)
}

// webhookFormat is the stored value for a format chip: json's slot is the
// omitted default "".
func webhookFormat(sel int) string {
	if sel <= 0 || sel >= len(webhookFormats) {
		return ""
	}
	return webhookFormats[sel]
}

// parseWebhookURL is the url field's parser: the trimmed value must be an http
// or https URL with a host.
func parseWebhookURL(s string) (any, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("an http or https URL")
	}
	return s, nil
}

// parseWebhookEvents is the events field's parser: split on commas and spaces,
// empties dropped, so an empty value means every event. Each event names a
// known event, and only state_changed may carry a :<state> suffix.
func parseWebhookEvents(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string(nil), nil
	}
	tokens := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	var out []string
	for _, ev := range tokens {
		name, _, hasState := strings.Cut(ev, ":")
		if !slices.Contains(policy.WebhookEvents, name) || (hasState && name != "state_changed") {
			return nil, errors.New("unknown event " + ev)
		}
		out = append(out, ev)
	}
	return out, nil
}

// setFocus moves focus to field i, wrapping, focusing that field's input (chip
// rows have none to focus).
func (f webhookForm) setFocus(i int) webhookForm {
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

// nextFocusable is the next field index in direction delta, wrapping once
// through every field.
func (f webhookForm) nextFocusable(from, delta int) int {
	n := len(f.fields)
	if n == 0 {
		return from
	}
	i := from
	for range f.fields {
		i = ((i+delta)%n + n) % n
		if f.fields[i].chips == nil {
			return i
		}
	}
	return from
}

// moveChip is left/right on the format row.
func (f webhookForm) moveChip(delta int) webhookForm {
	fld := f.fields[f.focus]
	if len(fld.chips) == 0 {
		return f
	}
	fld.sel = ((fld.sel+delta)%len(fld.chips) + len(fld.chips)) % len(fld.chips)
	f.fields[f.focus] = fld
	f.touched[f.focus] = true
	return f
}

// fieldError is field i's live parse error, nil for the chip row.
func (f webhookForm) fieldError(i int) error {
	fld := f.fields[i]
	if fld.chips != nil || fld.parse == nil {
		return nil
	}
	_, err := fld.parse(fld.input.Value())
	return err
}

// hook is the form's webhook, or ok false while a field fails to parse.
func (f webhookForm) hook() (policy.Webhook, bool) {
	for i := range f.fields {
		if f.fieldError(i) != nil {
			return policy.Webhook{}, false
		}
	}
	uv, _ := f.fields[0].parse(f.fields[0].input.Value())
	evs, _ := f.fields[2].parse(f.fields[2].input.Value())
	h := policy.Webhook{URL: uv.(string), Format: webhookFormat(f.fields[1].sel)}
	if evs != nil {
		h.Events = evs.([]string)
	}
	return h, true
}

// hooks is the whole list a submit would store: the form's hook appended when
// adding, or replacing the edited index.
func (f webhookForm) hooks() ([]policy.Webhook, bool) {
	h, ok := f.hook()
	if !ok {
		return nil, false
	}
	base := append([]policy.Webhook(nil), webhooksOf(f.doc)...)
	if f.editing < 0 {
		return append(base, h), true
	}
	if f.editing >= len(base) {
		return base, true
	}
	next := append([]policy.Webhook(nil), base...)
	next[f.editing] = h
	return next, true
}

// footerKeys is the form's keys: enter, tab, the format row's ←→, esc.
func (f webhookForm) footerKeys() []KeyHelp {
	return []KeyHelp{{"enter", "save"}, {"tab", "next field"}, {"←→", "choose"}, {"esc", "cancel"}}
}

// keys is the form's own keys, shown in the footer through overlayKeyer.
func (f webhookForm) keys() []KeyHelp { return f.footerKeys() }

// update handles one key: esc cancels, tab moves focus, left/right move the
// format row, typing edits a text field, enter submits.
func (f webhookForm) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
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

// submit is enter: SetWebhooks validates the edited list. ErrNoChange closes
// with "nothing changed"; a FieldError keeps the form open; a success applies
// the edit and closes.
func (f webhookForm) submit() (overlay, tea.Cmd, bool) {
	hooks, ok := f.hooks()
	if !ok {
		f.tried = true
		return f, nil, false
	}
	hook, _ := f.hook()
	edit, err := relevo.SetWebhooks(f.doc, hooks, f.message(hook))
	switch {
	case err == nil:
		return f, runAction(f.env.Ctx, "set webhook", f.title, func(ctx context.Context) Result {
			return f.env.Actions.ApplyConfig(ctx, edit)
		}), true
	case errors.Is(err, relevo.ErrNoChange):
		return f, notice("nothing changed"), true
	}
	f.tried = true
	f.formErr = relevo.HumanPolicyError(err)
	return f, nil, false
}

// message is the submit message: "add webhook <url>" or "edit webhook <url>".
func (f webhookForm) message(h policy.Webhook) string {
	verb := "add"
	if f.editing >= 0 {
		verb = "edit"
	}
	return verb + " webhook " + h.URL
}

func (f webhookForm) view(width int) []string {
	_, rows, _, _ := f.modal(width)
	return rows
}

// modal draws the form box in the settings forms' spacing: a blank, then per
// field the label, the input or chips and the hint, a per-field error when
// touched or tried, and a blank; then the two reference rows, a form-level
// error, and the key row. The enter chip is faint while an error stands.
func (f webhookForm) modal(width int) (string, []string, int, bool) {
	const want = 90
	innerW := modalInnerW(want, width)
	fieldW := settingsFieldWidth
	if roomW := innerW - settingsLabelWidth; fieldW > roomW {
		fieldW = roomW
	}
	if fieldW < 1 {
		fieldW = 1
	}

	hooks, fieldsOK := f.hooks()
	var formErr string
	if fieldsOK {
		if _, err := relevo.SetWebhooks(f.doc, hooks, "x"); err != nil && !errors.Is(err, relevo.ErrNoChange) {
			var fe *relevo.FieldError
			if errors.As(err, &fe) {
				formErr = relevo.HumanPolicyError(fe)
			}
		}
	}
	if formErr == "" {
		formErr = f.formErr
	}

	rows := []string{""}
	for i, fld := range f.fields {
		focused := i == f.focus
		var valuePart string
		if fld.chips != nil {
			valuePart = formChips(fld.chips, fld.sel, false)
		} else {
			valuePart = formInput(fld.input, focused, "", fieldW)
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
	for _, ref := range webhookRefRows() {
		rows = append(rows, mutedStyle.Render(ref))
	}
	rows = append(rows, "")
	if formErr != "" {
		rows = append(rows, formError(formErr))
		rows = append(rows, "")
	}
	rows = append(rows, formKeys(f.footerKeys(), map[string]bool{"enter": !fieldsOK || formErr != ""}))
	return f.title, rows, want, false
}

// webhookRefRows are the two dim reference rows under the fields: the known
// event names, and the state_changed suffix that narrows it.
func webhookRefRows() []string {
	return []string{
		"   events: " + strings.Join(policy.WebhookEvents, "  "),
		"   state_changed:needs_you narrows state_changed to one state",
	}
}
