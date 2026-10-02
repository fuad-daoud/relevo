package ui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// workflowEditShippedNotice is what e says on a shipped workflow. There is no
// saved source under that name to edit, and copying it out is a CLI step, so
// the notice names the command rather than offering an unbuilt key.
const workflowEditShippedNotice = "shipped workflows cannot be edited. " +
	"Copy it with `relevo config workflow show`, then add it under a new name."

// workflowEditEnd is where one pass of the editor loop leaves it: the workflow
// is ready to store, the user is done, or the editor opens again.
type workflowEditEnd int

const (
	workflowEditSaved workflowEditEnd = iota
	workflowEditDone
	workflowEditReopen
)

// workflowEditStep is the state one edit loop carries between editor passes: the
// workflow being edited as it is stored, the buffer last handed to the editor,
// and the temp file that buffer lives in. Everything here is data, so a pass can
// be decided without an editor, a terminal or a runtime.
type workflowEditStep struct {
	name   string
	saved  config.StoredWorkflow
	prev   []byte
	path   string
	actors map[string]workflow.ActorInfo
}

// workflowEditResult is what one pass decided. buffer and prev are the next
// pass's buffer on a reopen; source and def are what to store on a save.
type workflowEditResult struct {
	end      workflowEditEnd
	buffer   []byte
	source   string
	def      workflow.Definition
	prev     []byte
	problems []string
	aborted  bool
}

// transition is one pass of the loop, and it decides only what the CLI's loop
// decides: WorkflowEditRound answers whether the buffer is a workflow to store,
// a no-op, or a buffer to reopen with problems on top, and a source that has
// been renamed since it was opened is a problem like any other. It holds no
// validation of its own, so the two loops cannot disagree about what is valid.
func (e workflowEditStep) transition(edited []byte) workflowEditResult {
	stored, def, problems, done := relevo.WorkflowEditRound(e.prev, edited, e.saved, e.actors)
	if !done {
		return workflowEditReopened(problems, edited)
	}
	if stored == nil {
		return workflowEditResult{end: workflowEditDone, aborted: strings.TrimSpace(string(edited)) == ""}
	}
	if def.Name != e.name {
		return workflowEditReopened([]string{relevo.WorkflowNameProblem(def.Name, e.name)}, stored)
	}
	return workflowEditResult{end: workflowEditSaved, source: string(stored), def: def, prev: stored}
}

// workflowEditReopened is the pass that ends with the editor opening again: the
// problem lines as comments at the top, then the text the user last wrote with
// any earlier block stripped.
func workflowEditReopened(problems []string, edited []byte) workflowEditResult {
	buf := relevo.WorkflowEditReopen(problems, edited)
	return workflowEditResult{
		end:      workflowEditReopen,
		buffer:   buf,
		prev:     buf,
		problems: problems,
	}
}

// workflowEditReopenNotice says what is waiting in the buffer the editor has
// reopened on, so the cockpit says why it is handing the terminal back. The
// problems arrive already marked as comments, so the first one loses its mark.
func workflowEditReopenNotice(problems []string) string {
	first := strings.TrimSpace(strings.TrimPrefix(problems[0], "#"))
	if len(problems) == 1 {
		return first + "; the editor is open again"
	}
	return fmt.Sprintf("%s, and %d more; the editor is open again", first, len(problems)-1)
}

// workflowEditAfterMsg is one editor pass handed back: the loop's state as it
// was when the editor ran, and whether the editor left without running cleanly.
type workflowEditAfterMsg struct {
	step workflowEditStep
	err  error
}

// workflowEditStart reads what the loop needs for name and opens the temp file
// the editor works on. The stored source and definition come with it, so an edit
// can keep the file: seeds the stored copy embedded and can check the name
// against the workflow it was opened on.
func workflowEditStart(env Env, name string) (workflowEditStep, error) {
	source, shipped, err := env.Actions.WorkflowSource(name)
	if err != nil {
		return workflowEditStep{}, err
	}
	if shipped {
		return workflowEditStep{}, errShippedWorkflow
	}
	def, err := env.Actions.WorkflowDefinition(name)
	if err != nil {
		return workflowEditStep{}, err
	}
	f, err := os.CreateTemp("", "relevo-workflow-*.yaml")
	if err != nil {
		return workflowEditStep{}, err
	}
	step := workflowEditStep{
		name:   name,
		saved:  config.StoredWorkflow{Source: source, Definition: def},
		prev:   []byte(source),
		path:   f.Name(),
		actors: env.Actions.WorkflowActors(),
	}
	if err := f.Close(); err != nil {
		os.Remove(step.path)
		return workflowEditStep{}, err
	}
	if err := workflowEditWrite(step, step.prev); err != nil {
		os.Remove(step.path)
		return workflowEditStep{}, err
	}
	return step, nil
}

// errShippedWorkflow says the name is the shipped workflow's. It is not a
// failure to report: the key answers with its own notice instead.
var errShippedWorkflow = fmt.Errorf("workflow is shipped")

// workflowEditWrite puts the buffer the editor opens on into the temp file. The
// file holds whatever the user is working on, so it is never group or world
// readable, and it is rewritten on a reopen with the problem block on top.
func workflowEditWrite(step workflowEditStep, buf []byte) error {
	return os.WriteFile(step.path, buf, 0o600)
}

// workflowEditRunCmd runs the editor on the buffer and hands the loop's state
// back to the view when it exits. The editor never runs, so a command that
// cannot be built ends the loop here rather than leaving a temp file behind.
func workflowEditRunCmd(env Env, step workflowEditStep) tea.Cmd {
	cmd, err := env.Actions.AgentEditor(step.path)
	if err != nil {
		return workflowEditStop(step, err.Error())
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return workflowEditAfterMsg{step: step, err: err}
	})
}

// workflowEditStop ends the loop: the temp file goes, and the notice says why.
func workflowEditStop(step workflowEditStep, text string) tea.Cmd {
	os.Remove(step.path)
	return notice(text)
}

// editCmd is the e key: the user's editor on the cursor workflow's source, with
// the terminal released while it runs. A shipped workflow has no saved source,
// so the key is a notice and no editor opens.
func (v workflowsView) editCmd(env Env, w relevo.WorkflowSummary) tea.Cmd {
	if w.Origin == relevo.WorkflowOriginShipped {
		return notice(workflowEditShippedNotice)
	}
	step, err := workflowEditStart(env, w.Name)
	if err != nil {
		if err == errShippedWorkflow {
			return notice(workflowEditShippedNotice)
		}
		return notice(err.Error())
	}
	return workflowEditRunCmd(env, step)
}

// editAfter is one editor pass finished. An editor that did not exit cleanly
// changed nothing, and says so rather than reading a half-written buffer.
// Otherwise the pass decides: a workflow to store goes through the same audited
// config edit every other cockpit write makes, a no-op ends the loop, and any
// other result rewrites the temp file and opens the editor again on it.
func (v workflowsView) editAfter(env Env, msg workflowEditAfterMsg) tea.Cmd {
	step := msg.step
	if msg.err != nil {
		return workflowEditStop(step, "editor exited; nothing changed")
	}
	edited, err := os.ReadFile(step.path)
	if err != nil {
		return workflowEditStop(step, err.Error())
	}
	return v.editPass(env, step, step.transition(edited))
}

// editPass carries out one transition. It is its own step so a test can drive the
// loop's decisions without an editor.
func (v workflowsView) editPass(env Env, step workflowEditStep, res workflowEditResult) tea.Cmd {
	switch res.end {
	case workflowEditSaved:
		step.prev = res.prev
		os.Remove(step.path)
		return runAction(env.Ctx, "edit workflow", workflowWriteKey("edit", step.name),
			func(ctx context.Context) Result {
				return env.Actions.WorkflowSave(ctx, step.name, res.source, res.def)
			})
	case workflowEditDone:
		text := "no changes"
		if res.aborted {
			text = "aborted; nothing changed"
		}
		return workflowEditStop(step, text)
	default:
		step.prev = res.prev
		if err := workflowEditWrite(step, res.buffer); err != nil {
			return workflowEditStop(step, err.Error())
		}
		return tea.Batch(
			notice(workflowEditReopenNotice(res.problems)),
			workflowEditRunCmd(env, step),
		)
	}
}
