package ui

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// addWorkflowForm is the a key's overlay: one field for the path of a workflow
// file, the problems the file's workflow breaks under it, one per line, and the
// replace confirm an existing name earns.
type addWorkflowForm struct {
	ctx      context.Context
	actions  Actions
	pathIn   textinput.Model
	tried    bool
	problems []string
}

// newAddWorkflowForm builds the add workflow overlay.
func newAddWorkflowForm(env Env) addWorkflowForm {
	in := newFormInput(true)
	in.Placeholder = "~/workflows/review.yaml"
	return addWorkflowForm{ctx: env.Ctx, actions: env.Actions, pathIn: in}
}

// keys is the form's own keys, shown in the footer through overlayKeyer.
func (f addWorkflowForm) keys() []KeyHelp {
	return []KeyHelp{{"enter", "add"}, {"esc", "cancel"}}
}

// update handles one key: esc cancels, enter submits, every other key types.
func (f addWorkflowForm) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return f, nil, true
	case "enter":
		return f.submit()
	}
	var cmd tea.Cmd
	f.pathIn, cmd = f.pathIn.Update(k)
	return f, cmd, false
}

// submit is enter: the path is stored through the same WorkflowAdd the CLI
// calls. A file that cannot be read, parsed or validated keeps the form open
// with the problems under the field. A name that is already saved is not a
// problem but a conflict, so it opens the replace confirm and closes the form:
// the path is already in the confirm's own title, so nothing is lost by letting
// go of it.
func (f addWorkflowForm) submit() (overlay, tea.Cmd, bool) {
	f.tried = true
	path := strings.TrimSpace(f.pathIn.Value())
	if path == "" {
		f.problems = []string{"a path is required"}
		return f, nil, false
	}

	res := f.actions.WorkflowAdd(f.ctx, path, false)
	switch {
	case res.Err == nil:
		return f, addWorkflowDone(f.ctx, path, res), true
	case errors.Is(res.Err, relevo.ErrWorkflowShipped):
		f.problems = []string{"shipped workflows cannot be saved over from here"}
		return f, nil, false
	case errors.Is(res.Err, relevo.ErrWorkflowSaved):
		name := workflowName(res.Err)
		return f, openOverlay(replaceWorkflowConfirm(f.ctx, f.actions, path, name)), true
	}
	f.problems = workflowProblems(res.Err)
	return f, nil, false
}

// addWorkflowDone reports a stored workflow the way runAction would: the text
// becomes the notice and the log entry, and Refresh refetches the list. The
// write already happened, so nothing runs off the update loop here.
func addWorkflowDone(ctx context.Context, path string, res Result) tea.Cmd {
	return func() tea.Msg {
		return actionMsg{verb: "add workflow", key: workflowWriteKey("add", path), res: res}
	}
}

// workflowName is the workflow a classed failure happened to, "" when it names
// none yet.
func workflowName(err error) string {
	var we *relevo.WorkflowError
	if errors.As(err, &we) {
		return we.Workflow()
	}
	return ""
}

// replaceWorkflowConfirm is the confirm an already-saved name earns. The path
// is named, and the yes re-calls WorkflowAdd with replace set, so the only
// thing the second call adds is the overwrite the first refused.
func replaceWorkflowConfirm(ctx context.Context, a Actions, path, name string) confirmBox {
	return confirmBox{
		kind:   "replace workflow",
		yes:    "replace",
		danger: true,
		title:  "replace " + accentStyle.Bold(true).Render(name) + "?",
		lines: []string{
			pad("from", 11) + path,
			pad("kept", 11) + "the other saved workflows",
		},
		onYes: runAction(ctx, "add workflow", workflowWriteKey("add", name), func(ctx context.Context) Result {
			return a.WorkflowAdd(ctx, path, true)
		}),
	}
}

func (f addWorkflowForm) view(width int) []string {
	_, rows, _, _ := f.modal(width)
	return rows
}

// modal draws the form box: a blank, the path row, one problem per line under
// it once the form has been tried, a blank, and the keys.
func (f addWorkflowForm) modal(width int) (string, []string, int, bool) {
	const want = 90
	innerW := modalInnerW(want, width)
	fieldW := innerW - formSubRowIndent
	if fieldW < 1 {
		fieldW = 1
	}

	rows := []string{""}
	rows = append(rows, formLabel("path", true)+formInput(f.pathIn, true, "", fieldW))
	if f.tried {
		for _, p := range f.problems {
			rows = append(rows, formError(p))
		}
	}
	rows = append(rows, "")
	rows = append(rows, formKeys(f.keys(), nil))
	return "add workflow", rows, want, false
}
