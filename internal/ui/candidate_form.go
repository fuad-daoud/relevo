package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// candidateForm is the one overlay the `:candidates` view uses to add and to
// edit a candidate (§3, round 3): a harness chip row, a provider row (chips on
// a harness with a provider list, a text field with suggestions on one
// without), a model field and a read-only name row. It validates on every
// render: modal rebuilds the edit with relevo.AddCandidate/EditCandidate over
// its doc.
type candidateForm struct {
	ctx     context.Context
	actions Actions
	doc     relevo.ConfigDoc
	gates   []availability.Gate // env.Report.Gated at open
	now     time.Time
	editing string   // "" = add; else the candidate's current name
	slots   string   // SERVES text of the edited row ("" when adding)
	kinds   []string // harness kinds, harness.All() order
	hsel    int      // selected kind index
	psel    int      // selected provider index on a listed harness; -1 = none
	provIn  textinput.Model
	modelIn textinput.Model
	focus   int // 0 harness, 1 provider, 2 model
	touched [3]bool
	tried   bool // enter pressed at least once
	sugg    int  // position in the provider suggestion cycle; -1 = none
}

// newCandidateForm builds the add/edit candidate overlay (§4). harnessKind seeds
// an add's harness: the cursor row's kind, or the first kind when "".
func newCandidateForm(env Env, doc relevo.ConfigDoc, editing, slots, harnessKind string) candidateForm {
	all := harness.All()
	kinds := make([]string, 0, len(all))
	for _, h := range all {
		kinds = append(kinds, h.Kind)
	}

	f := candidateForm{
		ctx:     env.Ctx,
		actions: env.Actions,
		doc:     doc,
		gates:   env.Report.Gated,
		now:     env.Now,
		editing: editing,
		slots:   slots,
		kinds:   kinds,
		psel:    -1,
		sugg:    -1,
	}
	f.provIn = newFormInput(false)
	f.modelIn = newFormInput(false)

	if editing != "" {
		// Editing: prefill the harness, provider and model from the entry,
		// then start on the model with the cursor at its end.
		if c, ok := candByName(doc, editing); ok {
			f.hsel = candIndex(f.kinds, c.Harness)
			if f.hsel < 0 {
				f.hsel = 0
			}
			f.provIn.SetValue(c.Provider)
			f.provIn.CursorEnd()
			f.modelIn.SetValue(c.Model)
			f.modelIn.CursorEnd()
		}
		f = f.setFocus(2)
	} else {
		f.hsel = candIndex(f.kinds, harnessKind)
		if f.hsel < 0 {
			f.hsel = 0
		}
		f = f.setFocus(0)
	}
	if h, ok := harness.Lookup(f.kinds[f.hsel]); ok && h.Providers != nil {
		f.psel = pickProvider(h.Providers, f.provIn.Value())
	}
	return f
}

// candByName returns the candidate named name, ok false when none has it.
func candByName(doc relevo.ConfigDoc, name string) (candidate.Candidate, bool) {
	for _, c := range doc.Candidates {
		if c.Name == name {
			return c, true
		}
	}
	return candidate.Candidate{}, false
}

// candIndex is slices.Index for a string slice, kept local so the form reads
// the same as the rest of the package.
func candIndex(items []string, s string) int { return slices.Index(items, s) }

// pickProvider is the provider chip a harness with a list starts on: the only
// entry when the list holds exactly one -- there is nothing else to pick -- else
// the current provider's index when the list holds it, else -1 (none).
func pickProvider(list []string, current string) int {
	if len(list) == 1 {
		return 0
	}
	return candIndex(list, current)
}

// setFocus moves focus to field i (0 harness, 1 provider, 2 model), wrapping,
// focusing that field's input while blurring the others.
func (f candidateForm) setFocus(i int) candidateForm {
	i = ((i % 3) + 3) % 3
	f.focus = i
	if i == 1 {
		f.provIn.Focus()
	} else {
		f.provIn.Blur()
	}
	if i == 2 {
		f.modelIn.Focus()
	} else {
		f.modelIn.Blur()
	}
	return f
}

// input is what the form would submit: the selected kind, and the provider the
// selected chips or the typed field hold.
func (f candidateForm) input() relevo.CandidateInput {
	provider := f.provIn.Value()
	if h, ok := harness.Lookup(f.kinds[f.hsel]); ok && h.Providers != nil && f.psel >= 0 && f.psel < len(h.Providers) {
		provider = h.Providers[f.psel]
	}
	return relevo.CandidateInput{
		Harness:  f.kinds[f.hsel],
		Provider: provider,
		Model:    f.modelIn.Value(),
	}
}

// check rebuilds this form's edit: AddCandidate when adding, EditCandidate when
// editing.
func (f candidateForm) check() (relevo.ConfigEdit, error) {
	if f.editing == "" {
		return relevo.AddCandidate(f.doc, f.input())
	}
	return relevo.EditCandidate(f.doc, f.editing, f.input())
}

// preview is the name the current input would take ("" while it has no model).
func (f candidateForm) preview() string {
	return relevo.PreviewCandidateName(f.doc, f.editing, f.input())
}

// suggestions is the distinct providers doc.Candidates already use with the
// selected kind, in stored order.
func (f candidateForm) suggestions() []string {
	kind := f.kinds[f.hsel]
	seen := make(map[string]bool, len(f.doc.Candidates))
	var out []string
	for _, c := range f.doc.Candidates {
		if c.Harness != kind || seen[c.Provider] {
			continue
		}
		seen[c.Provider] = true
		out = append(out, c.Provider)
	}
	return out
}

// gateFor is the gate on the provider named provider, if any: a Gated entry
// whose token's provider segment equals it.
func (f candidateForm) gateFor(provider string) (availability.Gate, bool) {
	if provider == "" {
		return availability.Gate{}, false
	}
	for _, g := range f.gates {
		ref, err := candidate.ParseRef(g.Token)
		if err != nil {
			continue
		}
		if ref.Provider == provider {
			return g, true
		}
	}
	return availability.Gate{}, false
}

// modelPlaceholder is the model field's hint when it is empty (§1).
func (f candidateForm) modelPlaceholder() string {
	if f.kinds[f.hsel] == "opencode" && f.input().Provider != "" {
		return "the model id after " + f.input().Provider + "/"
	}
	return "the model id"
}

// keys are the form's own keys, shown in the footer through overlayKeyer (§1).
func (f candidateForm) keys() []KeyHelp {
	submit := "add"
	if f.editing != "" {
		submit = "save"
	}
	return []KeyHelp{{"enter", submit}, {"tab", "next field"}, {"esc", "cancel"}}
}

// update handles one key (§4): esc closes, tab moves focus, the harness row
// takes left/right, the provider row takes left/right (listed) or up/down and
// typing (open), the model row takes every key, and enter submits.
func (f candidateForm) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return f, nil, true
	case "tab":
		return f.setFocus(f.focus + 1), nil, false
	case "shift+tab", "back_tab":
		return f.setFocus(f.focus - 1), nil, false
	case "enter":
		return f.submit()
	}

	switch f.focus {
	case 0:
		return f.updateHarness(k), nil, false
	case 1:
		return f.updateProvider(k)
	default:
		var cmd tea.Cmd
		f.modelIn, cmd = f.modelIn.Update(k)
		f.touched[2] = true
		return f, cmd, false
	}
}

// updateHarness is left/right on the harness row. Landing on a listed harness
// selects the provider it already holds (or none); leaving one for an open
// harness copies the selected provider into the text field.
func (f candidateForm) updateHarness(k tea.KeyMsg) candidateForm {
	var delta int
	switch k.String() {
	case "left":
		delta = -1
	case "right":
		delta = 1
	default:
		return f
	}

	cur := f.input().Provider
	old := f.kinds[f.hsel]
	f.hsel = ((f.hsel+delta)%len(f.kinds) + len(f.kinds)) % len(f.kinds)
	f.sugg = -1
	oldH, oldOK := harness.Lookup(old)

	if h, ok := harness.Lookup(f.kinds[f.hsel]); ok && h.Providers != nil {
		f.psel = pickProvider(h.Providers, cur)
		f.touched[1] = true
		return f
	}

	if oldOK && oldH.Providers != nil && f.psel >= 0 && f.psel < len(oldH.Providers) {
		f.provIn.SetValue(oldH.Providers[f.psel])
		f.provIn.CursorEnd()
	}
	f.psel = -1
	return f
}

// updateProvider is the provider row's keys: left/right over a listed harness's
// chips, or up/down through the suggestions and typing into the field on one
// without.
func (f candidateForm) updateProvider(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	if h, ok := harness.Lookup(f.kinds[f.hsel]); ok && h.Providers != nil {
		n := len(h.Providers)
		switch k.String() {
		case "left":
			f.psel = f.movePSel(-1, n)
			f.touched[1] = true
		case "right":
			f.psel = f.movePSel(1, n)
			f.touched[1] = true
		}
		return f, nil, false
	}

	switch k.String() {
	case "up":
		f = f.cycleSuggestion(-1)
		f.touched[1] = true
	case "down":
		f = f.cycleSuggestion(1)
		f.touched[1] = true
	default:
		var cmd tea.Cmd
		f.provIn, cmd = f.provIn.Update(k)
		f.touched[1] = true
		return f, cmd, false
	}
	return f, nil, false
}

// movePSel is one step of a listed harness's provider selection: 0 from -1 in
// either direction, else delta with wrapping.
func (f candidateForm) movePSel(delta, n int) int {
	if n == 0 {
		return -1
	}
	if f.psel < 0 {
		return 0
	}
	return ((f.psel+delta)%n + n) % n
}

// cycleSuggestion moves the suggestion cursor by delta and fills the provider
// field with the provider it lands on.
func (f candidateForm) cycleSuggestion(delta int) candidateForm {
	s := f.suggestions()
	if len(s) == 0 {
		return f
	}
	switch {
	case f.sugg < 0 && delta > 0:
		f.sugg = 0
	case f.sugg < 0:
		f.sugg = len(s) - 1
	default:
		f.sugg = ((f.sugg+delta)%len(s) + len(s)) % len(s)
	}
	f.provIn.SetValue(s[f.sugg])
	f.provIn.CursorEnd()
	return f
}

// submit is enter: a valid edit closes and applies it, an unchanged one closes
// with a notice, and anything else keeps the form open on the failing field.
func (f candidateForm) submit() (overlay, tea.Cmd, bool) {
	edit, err := f.check()
	switch {
	case err == nil:
		verb := "add candidate"
		if f.editing != "" {
			verb = "edit candidate"
		}
		name := edit.Name
		return f, runAction(f.ctx, verb, name, func(ctx context.Context) Result {
			return f.actions.ApplyConfig(ctx, edit)
		}), true
	case errors.Is(err, relevo.ErrNoChange):
		return f, notice("nothing changed"), true
	}

	f.tried = true
	var fe *relevo.FieldError
	if errors.As(err, &fe) {
		switch fe.Field {
		case "harness":
			return f.setFocus(0), nil, false
		case "provider":
			return f.setFocus(1), nil, false
		case "model":
			return f.setFocus(2), nil, false
		}
	}
	return f, nil, false
}

// view is the overlay interface's body: the modal's rows.
func (f candidateForm) view(width int) []string {
	_, rows, _, _ := f.modal(width)
	return rows
}

// modal draws the form box (§1, §4): title `add candidate` or `edit <name>`,
// want 90, rows in the order §4 lists.
func (f candidateForm) modal(width int) (string, []string, int, bool) {
	const want = 90
	kind := "add candidate"
	if f.editing != "" {
		kind = "edit " + f.editing
	}

	innerW := modalInnerW(want, width)
	fieldW := innerW - 11
	if fieldW < 1 {
		fieldW = 1
	}

	_, err := f.check()
	fe := asFieldError(err)
	show := func(field string) bool {
		if fe == nil || fe.Field != field {
			return false
		}
		i := fieldIndex(field)
		return i >= 0 && (f.touched[i] || f.tried)
	}
	h, _ := harness.Lookup(f.kinds[f.hsel])

	rows := []string{""}
	rows = append(rows, formLabel("harness", f.focus == 0)+formChips(f.kinds, f.hsel, false))
	if show("harness") {
		rows = append(rows, formError(fe.Msg))
	}
	rows = append(rows, "")

	if h.Providers != nil {
		rows = append(rows, formLabel("provider", f.focus == 1)+formChips(h.Providers, f.psel, false))
	} else {
		rows = append(rows, formLabel("provider", f.focus == 1)+
			formInput(f.provIn, f.focus == 1, "", fieldW))
	}
	if row := f.providerNote(fe, show("provider")); row != "" {
		rows = append(rows, row)
	}
	rows = append(rows, "")

	rows = append(rows, formLabel("model", f.focus == 2)+
		formInput(f.modelIn, f.focus == 2, f.modelPlaceholder(), fieldW))
	if show("model") {
		rows = append(rows, formError(fe.Msg))
	}
	rows = append(rows, "")

	rows = append(rows, f.nameRow())
	rows = append(rows, "")
	rows = append(rows, f.infoLines()...)
	if fe != nil && fe.Field == "" {
		rows = append(rows, formError(fe.Msg))
	}
	rows = append(rows, "")
	rows = append(rows, formKeys(f.keys(), map[string]bool{
		"enter": err != nil && !errors.Is(err, relevo.ErrNoChange),
	}))
	return kind, rows, want, false
}

// providerNote is the row under the provider field (§4 row 6): on a listed
// harness the provider error, else the suggestion chips (or `new provider` in
// accent when the typed value matches none of them).
func (f candidateForm) providerNote(fe *relevo.FieldError, showErr bool) string {
	if showErr {
		return formError(fe.Msg)
	}
	if h, ok := harness.Lookup(f.kinds[f.hsel]); ok && h.Providers != nil {
		return ""
	}
	s := f.suggestions()
	value := strings.TrimSpace(f.provIn.Value())
	if value != "" && candIndex(s, value) < 0 {
		return formSubRow(chip(chipAccentStyle, "new provider"))
	}
	if len(s) == 0 {
		return ""
	}
	sel := candIndex(s, value)
	if sel < 0 {
		sel = f.sugg
	}
	return formSubRow(formChips(s, sel, false))
}

// nameRow is the read-only name row (§1): the name the model would give, the
// `was <old> · still <slots>` note when an edit changes it, and
// `follows the model` while an add has no model yet.
func (f candidateForm) nameRow() string {
	label := formLabel("name", false)
	name := f.preview()

	if f.editing == "" {
		if strings.TrimSpace(f.modelIn.Value()) == "" {
			return label + mutedStyle.Render("follows the model")
		}
		return label + textStyle.Render(name)
	}

	row := label + textStyle.Render(name)
	if name != f.editing {
		suffix := "   was " + f.editing
		if f.slots != "" {
			suffix += " · still " + f.slots
		}
		row += faintStyle.Render(suffix)
	}
	return row
}

// infoLines are the muted notes under the name (§1): a gate on the provider,
// the note that an edit keeping a gated provider stays gated, and the note that
// nothing picks a candidate until an actor lists it.
func (f candidateForm) infoLines() []string {
	var out []string
	input := f.input()
	provider := strings.TrimSpace(input.Provider)
	g, gated := f.gateFor(provider)

	if gated {
		suffix := " until " + statsGateUntil(g, f.now)
		if reason := statsGateReasonText(g.Note); reason != "" {
			suffix += " · " + reason
		}
		out = append(out, mutedStyle.Render(provider+" is ")+redStyle.Render("gated")+mutedStyle.Render(suffix))
	}
	if f.editing != "" && gated {
		if c, ok := candByName(f.doc, f.editing); ok && c.Provider == provider {
			out = append(out, mutedStyle.Render("the gate is the provider's, so the new model stays gated"))
		}
	}
	if f.editing == "" {
		out = append(out, mutedStyle.Render("no actor picks it until you add it to one in ")+
			textStyle.Render(":actors"))
	}
	return out
}

// asFieldError is err as a *relevo.FieldError, nil when it is not one.
func asFieldError(err error) *relevo.FieldError {
	var fe *relevo.FieldError
	if errors.As(err, &fe) {
		return fe
	}
	return nil
}

// fieldIndex is a FieldError field's index in the form's touched array.
func fieldIndex(field string) int {
	switch field {
	case "harness":
		return 0
	case "provider":
		return 1
	case "model":
		return 2
	}
	return -1
}
