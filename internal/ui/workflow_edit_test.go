package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// workflowEditPasses is how many editor passes a loop test runs before it calls
// the loop endless. A loop that never ends fails here rather than hanging the
// suite.
const workflowEditPasses = 5

// workflowEditBuffer is what the fake editor leaves in the file: the body, then
// the newline a here-document always ends with.
func workflowEditBuffer(body string) string { return body + "\n" }

// workflowEditScript is a fake editor: a shell script that writes body into the
// file it was handed, as an editor that saved would. It is the whole editor
// these tests use. No terminal, no harness, no network.
func workflowEditScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor.sh")
	script := "#!/bin/sh\ncat > \"$1\" <<'WORKFLOW_EOF'\n" + body + "\nWORKFLOW_EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write the editor script: %v", err)
	}
	return path
}

// workflowEditFailScript is a fake editor that writes nothing and exits
// non-zero, the way an editor that hit an error leaves.
func workflowEditFailScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor-fail.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatalf("write the editor script: %v", err)
	}
	return path
}

// workflowEditStepFor starts a real loop over the scripted Actions: the temp
// file, the stored source and definition and the actors, exactly what the e key
// builds.
func workflowEditStepFor(t *testing.T, m Model, name string) workflowEditStep {
	t.Helper()
	step, err := workflowEditStart(m.env(), name)
	if err != nil {
		t.Fatalf("start the edit loop: %v", err)
	}
	t.Cleanup(func() { os.Remove(step.path) })
	return step
}

// runWorkflowEditor runs the fake editor over the loop's temp file, through the
// editor command production builds, and answers with what the editor left: nil
// when it exited cleanly, the exit error when it did not.
func runWorkflowEditor(t *testing.T, step workflowEditStep) error {
	t.Helper()
	cmd, err := (&mastermindActions{}).AgentEditor(step.path)
	if err != nil {
		t.Fatalf("build the editor command: %v", err)
	}
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return err
	}
	if err != nil {
		t.Fatalf("run the fake editor: %v", err)
	}
	return nil
}

// workflowEditPass runs one pass of the loop over m: the fake editor writes the
// buffer, then the view is handed the editor's exit the way tea.ExecProcess
// hands it over. It reports the decision the transition made, so a loop test
// knows when to stop without running the command the pass returned.
func workflowEditPass(t *testing.T, m Model, step workflowEditStep) (Model, tea.Cmd, workflowEditResult) {
	t.Helper()
	editorErr := runWorkflowEditor(t, step)
	edited, err := os.ReadFile(step.path)
	if err != nil {
		t.Fatalf("read the edited buffer: %v", err)
	}
	res, cmd := m.Update(workflowEditAfterMsg{step: step, err: editorErr})
	return res.(Model), cmd, step.transition(edited)
}

// workflowEditLoop runs passes until the loop no longer wants another, and fails
// if it still does after workflowEditPasses, so a loop that never ends cannot
// hang the test.
func workflowEditLoop(t *testing.T, m Model, step workflowEditStep, want workflowEditEnd) (Model, tea.Cmd) {
	t.Helper()
	for i := 0; i < workflowEditPasses; i++ {
		var cmd tea.Cmd
		m, cmd, res := workflowEditPass(t, m, step)
		if res.end != want {
			t.Fatalf("pass %d ended %v, want %v", i+1, res.end, want)
		}
		if res.end != workflowEditReopen {
			return m, cmd
		}
		// The next pass opens on the buffer the reopen wrote, which is the
		// production loop's own step forward.
		step.prev = res.buffer
	}
	t.Fatalf("the edit loop wanted another pass after %d", workflowEditPasses)
	return m, nil
}

// workflowEditNoticeOf returns the notice a pass's command carries. A reopen
// batches its notice with the command that runs the editor, which needs a
// terminal, so only the notice is asked for.
func workflowEditNoticeOf(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	for _, c := range []tea.Cmd{cmd} {
		if c == nil {
			continue
		}
		batch, ok := c().(tea.BatchMsg)
		if !ok {
			continue
		}
		for _, inner := range batch {
			if msg, ok := inner().(noticeMsg); ok {
				return msg.text
			}
		}
	}
	t.Fatalf("the pass carried no notice")
	return ""
}

// workflowEditSource is a saved workflow the fixtures can edit: two steps, both
// running an actor the fixture registry carries.
func workflowEditSource() string {
	return "name: fix-first\nstart: build\nsteps:\n" +
		"  build: { run: builder, on: { done: check } }\n" +
		"  check: { check: gate, on: { green: done, red: done } }\n"
}

// TestWorkflowEditLoopSavesValid: a buffer that parses and validates is stored
// through the write the loop was handed, and the buffer saved is the workflow.
// Mutation: skip WorkflowSave.
func TestWorkflowEditLoopSavesValid(t *testing.T) {
	body := "# one more gate\n" + workflowEditSource()
	t.Setenv("VISUAL", workflowEditScript(t, body))
	edited := workflowEditBuffer(body)

	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "fix-first")
	step := workflowEditStepFor(t, m, "fix-first")

	m, cmd := workflowEditLoop(t, m, step, workflowEditSaved)
	if cmd == nil {
		t.Fatal("a valid workflow must produce a save")
	}
	m = drain(t, m, cmd)

	if len(fa.saves) != 1 {
		t.Fatalf("saves = %d, want the one valid workflow stored", len(fa.saves))
	}
	got := fa.saves[0]
	if got.name != "fix-first" {
		t.Errorf("stored as %q, want fix-first", got.name)
	}
	if got.source != edited {
		t.Errorf("stored source = %q, want the edited source", got.source)
	}
	if got.def.Name != "fix-first" {
		t.Errorf("stored definition names %q, want fix-first", got.def.Name)
	}
	if m.notice != fa.saveResult.Text {
		t.Errorf("notice = %q, want the save's text with the config version", m.notice)
	}
	if _, err := os.Stat(step.path); !os.IsNotExist(err) {
		t.Error("the temp file must be gone once the workflow is stored")
	}
}

// TestWorkflowEditLoopReopensWithProblems: an invalid buffer comes back with the
// marker and the problem lines on top, so the next pass opens on something
// fixable, and the notice says what is waiting. Mutation: reopen with the bare
// file, no problems.
func TestWorkflowEditLoopReopensWithProblems(t *testing.T) {
	const bad = "name: fix-first\nstart: ghost\nsteps:\n" +
		"  build: { run: builder, on: { done: done } }\n"
	t.Setenv("VISUAL", workflowEditScript(t, bad))

	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "fix-first")
	step := workflowEditStepFor(t, m, "fix-first")

	m, cmd, res := workflowEditPass(t, m, step)
	if res.end != workflowEditReopen {
		t.Fatalf("an invalid workflow must ask for the editor again, ended %v", res.end)
	}
	buf, err := os.ReadFile(step.path)
	if err != nil {
		t.Fatalf("read the reopened buffer: %v", err)
	}
	got := string(buf)
	if !strings.HasPrefix(got, relevo.WorkflowEditProblemMarker+"\n") {
		t.Errorf("the reopened buffer must start with the marker:\n%s", got)
	}
	if !strings.Contains(got, "ghost") {
		t.Errorf("the reopened buffer must carry the problems:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), strings.TrimSpace(bad)) {
		t.Errorf("the reopened buffer must end with the user's own text:\n%s", got)
	}
	if len(fa.saves) != 0 {
		t.Errorf("nothing may be stored on a reopen, saves = %d", len(fa.saves))
	}
	if notice := workflowEditNoticeOf(t, cmd); !strings.Contains(notice, "ghost") {
		t.Errorf("notice = %q, want the problem the editor is waiting on", notice)
	}
	if string(res.buffer) != got {
		t.Error("the next pass must open on the buffer the reopen wrote")
	}
}

// TestWorkflowEditLoopQuitUnchangedEnds: a buffer the editor left as it was ends
// the loop with nothing stored, and the temp file goes. Mutation: loop forever,
// caught by the pass bound.
func TestWorkflowEditLoopQuitUnchangedEnds(t *testing.T) {
	t.Setenv("VISUAL", "true")

	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "fix-first")
	step := workflowEditStepFor(t, m, "fix-first")

	m, cmd := workflowEditLoop(t, m, step, workflowEditDone)
	if msg, ok := cmd().(noticeMsg); !ok {
		t.Fatalf("an unchanged buffer must end with a notice, got %T", cmd())
	} else if msg.text != "no changes" {
		t.Errorf("notice = %q, want no changes", msg.text)
	}
	if len(fa.saves) != 0 {
		t.Errorf("nothing may be stored, saves = %d", len(fa.saves))
	}
	if _, err := os.Stat(step.path); !os.IsNotExist(err) {
		t.Error("the temp file must be gone once the loop ends")
	}
}

// TestWorkflowEditLoopAbortedOnEmpty: an emptied buffer is the abort, which is a
// different notice from leaving the workflow as it was.
func TestWorkflowEditLoopAbortedOnEmpty(t *testing.T) {
	t.Setenv("VISUAL", workflowEditScript(t, ""))

	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "fix-first")
	step := workflowEditStepFor(t, m, "fix-first")

	_, cmd, _ := workflowEditPass(t, m, step)
	if msg, ok := cmd().(noticeMsg); !ok {
		t.Fatalf("an emptied buffer must end with a notice, got %T", cmd())
	} else if msg.text != "aborted; nothing changed" {
		t.Errorf("notice = %q, want the abort", msg.text)
	}
	if len(fa.saves) != 0 {
		t.Errorf("nothing may be stored, saves = %d", len(fa.saves))
	}
}

// TestWorkflowEditLoopRefusesRename: a source renamed under the key it was
// opened on is refused with the CLI's own problem line and reopened, because
// storing it would file a workflow under a name it does not carry. Mutation:
// allow the rename.
func TestWorkflowEditLoopRefusesRename(t *testing.T) {
	const renamed = "name: other\nstart: build\nsteps:\n" +
		"  build: { run: builder, on: { done: done } }\n"
	t.Setenv("VISUAL", workflowEditScript(t, renamed))

	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "fix-first")
	step := workflowEditStepFor(t, m, "fix-first")

	_, cmd, res := workflowEditPass(t, m, step)
	if res.end != workflowEditReopen {
		t.Fatalf("a renamed source must ask for the editor again, ended %v", res.end)
	}
	buf, err := os.ReadFile(step.path)
	if err != nil {
		t.Fatalf("read the reopened buffer: %v", err)
	}
	want := relevo.WorkflowNameProblem("other", "fix-first")
	if !strings.Contains(string(buf), want) {
		t.Errorf("the reopened buffer must carry %q:\n%s", want, string(buf))
	}
	if len(fa.saves) != 0 {
		t.Errorf("a renamed workflow must not be stored, saves = %d", len(fa.saves))
	}
	if notice := workflowEditNoticeOf(t, cmd); !strings.Contains(notice, "other") {
		t.Errorf("notice = %q, want the name problem", notice)
	}
}

// TestWorkflowEditLoopNeverStoresProblemBlock: a workflow fixed after a reopen is
// stored without the comments the reopen put there, so what is saved is the
// workflow and not a comment block. Mutation: skip StripWorkflowEditProblems.
func TestWorkflowEditLoopNeverStoresProblemBlock(t *testing.T) {
	body := workflowEditSource()
	t.Setenv("VISUAL", workflowEditScript(t, body))
	fixed := workflowEditBuffer(body)

	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "fix-first")
	step := workflowEditStepFor(t, m, "fix-first")

	// The first pass reopens on the problem block, so the second is the one that
	// must strip it before storing.
	reopened := []byte(relevo.WorkflowEditReopen([]string{`# start "ghost" is not a step`}, []byte(fixed)))
	step.prev = reopened
	if err := workflowEditWrite(step, reopened); err != nil {
		t.Fatalf("write the reopened buffer: %v", err)
	}
	m, cmd := workflowEditLoop(t, m, step, workflowEditSaved)
	m = drain(t, m, cmd)

	if len(fa.saves) != 1 {
		t.Fatalf("saves = %d, want the fixed workflow stored", len(fa.saves))
	}
	if got := fa.saves[0].source; got != fixed {
		t.Errorf("stored source = %q, want the problem block stripped", got)
	}
	if strings.Contains(fa.saves[0].source, relevo.WorkflowEditProblemMarker) {
		t.Error("the stored source must carry no marker line")
	}
}

// TestWorkflowEditShippedRefused: e on a shipped workflow is a notice and opens
// no editor. Mutation: open the editor.
func TestWorkflowEditShippedRefused(t *testing.T) {
	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "default")

	res, cmd := m.Update(key('e'))
	m = res.(Model)
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("e on a shipped workflow must return a notice, got %T", cmd())
	}
	m = drain(t, m, cmd)
	if msg.text != workflowEditShippedNotice {
		t.Errorf("notice = %q, want the shipped refusal", msg.text)
	}
	if len(fa.edited) != 0 {
		t.Errorf("no editor may open for a shipped workflow, opened %v", fa.edited)
	}
	if len(fa.saves) != 0 {
		t.Errorf("a shipped workflow must not be stored, saves = %d", len(fa.saves))
	}
	if m.notice != workflowEditShippedNotice {
		t.Errorf("the notice must reach the shell, notice = %q", m.notice)
	}
}

// TestWorkflowEditEditorNonZeroExitIsNoChange: an editor that did not exit
// cleanly changed nothing, so the loop ends without reading a buffer the user
// may not have finished writing.
func TestWorkflowEditEditorNonZeroExitIsNoChange(t *testing.T) {
	t.Setenv("VISUAL", workflowEditFailScript(t))

	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "fix-first")
	step := workflowEditStepFor(t, m, "fix-first")

	_, cmd, _ := workflowEditPass(t, m, step)
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("a failed editor must end with a notice, got %T", cmd())
	}
	if msg.text != "editor exited; nothing changed" {
		t.Errorf("notice = %q, want nothing changed", msg.text)
	}
	if len(fa.saves) != 0 {
		t.Errorf("a failed editor must store nothing, saves = %d", len(fa.saves))
	}
	if _, err := os.Stat(step.path); !os.IsNotExist(err) {
		t.Error("the temp file must be gone once the loop ends")
	}
}

// TestWorkflowEditKeyOpensEditorOnSource: the e key hands the editor a temp file
// that already holds the workflow's own source, readable by its owner alone, and
// returns the command that runs it. The command is not run here, since
// tea.ExecProcess needs a terminal.
func TestWorkflowEditKeyOpensEditorOnSource(t *testing.T) {
	t.Setenv("VISUAL", workflowEditScript(t, ""))

	fa := workflowsFake()
	m := workflowsViewModelOn(t, 132, 34, fa, "fix-first")

	_, cmd := m.Update(key('e'))
	if cmd == nil {
		t.Fatal("e must return the command that runs the editor")
	}
	if len(fa.edited) != 1 {
		t.Fatalf("the editor was opened %d times, want once", len(fa.edited))
	}
	path := fa.edited[0]
	t.Cleanup(func() { os.Remove(path) })
	if !strings.HasSuffix(path, ".yaml") {
		t.Errorf("the editor path = %q, want the workflow's own suffix", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the temp file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("temp file mode = %v, want 0600", info.Mode().Perm())
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the temp file: %v", err)
	}
	if string(buf) != workflowsFixtureSources()["fix-first"].text {
		t.Errorf("the editor must open on the stored source, got:\n%s", string(buf))
	}
}

// TestWorkflowEditKeysHiddenWithoutActions: the view with no write seam hides
// its keys, and the shipped refusal still answers, since it is decided by the
// row rather than by a read.
func TestWorkflowEditKeysHiddenWithoutActions(t *testing.T) {
	v := workflowsView{}
	if keys := v.Keys(); keys != nil {
		t.Errorf("without Actions the keys must be hidden, got %v", keys)
	}
	cmd := v.editCmd(Env{}, workflowsFixtureList()[0])
	if msg, ok := cmd().(noticeMsg); !ok {
		t.Fatalf("a shipped row must answer with a notice, got %T", cmd())
	} else if msg.text != workflowEditShippedNotice {
		t.Errorf("notice = %q, want the shipped refusal", msg.text)
	}
}

// TestWorkflowEditTransitionTable drives the transition directly, with no editor
// and no view, over every way one pass can end. It is the pure half of the loop.
func TestWorkflowEditTransitionTable(t *testing.T) {
	source := workflowEditSource()
	def, err := workflow.Parse([]byte(source))
	if err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}
	step := workflowEditStep{
		name:   "fix-first",
		saved:  config.StoredWorkflow{Source: source, Definition: def},
		prev:   []byte(source),
		actors: workflowsFixtureActors(),
	}
	cases := []struct {
		name    string
		edited  string
		want    workflowEditEnd
		aborted bool
		wantIn  string
	}{
		{name: "unchanged", edited: source, want: workflowEditDone},
		{name: "emptied", edited: "  \n", want: workflowEditDone, aborted: true},
		{
			name:   "renamed",
			edited: strings.Replace(source, "fix-first", "other", 1),
			want:   workflowEditReopen,
			wantIn: relevo.WorkflowNameProblem("other", "fix-first"),
		},
		{
			name:   "unknown actor",
			edited: strings.Replace(source, "run: builder", "run: ghost", 1),
			want:   workflowEditReopen,
			wantIn: "ghost",
		},
		{name: "edited and valid", edited: "# tuned\n" + source, want: workflowEditSaved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := step.transition([]byte(tc.edited))
			if got.end != tc.want {
				t.Fatalf("end = %v, want %v", got.end, tc.want)
			}
			if got.aborted != tc.aborted {
				t.Errorf("aborted = %v, want %v", got.aborted, tc.aborted)
			}
			if tc.wantIn == "" {
				return
			}
			if !strings.Contains(string(got.buffer), tc.wantIn) {
				t.Errorf("reopen buffer =\n%s\nwant it to carry %q", string(got.buffer), tc.wantIn)
			}
		})
	}
}

// TestWorkflowEditTransitionSavesNothingWithoutActors: an empty actor registry
// makes every step's actor a problem, so a workflow no role can run is reopened
// rather than stored.
func TestWorkflowEditTransitionSavesNothingWithoutActors(t *testing.T) {
	source := workflowEditSource()
	def, err := workflow.Parse([]byte(source))
	if err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}
	step := workflowEditStep{
		name:  "fix-first",
		saved: config.StoredWorkflow{Source: source, Definition: def},
		prev:  []byte(source),
	}
	got := step.transition([]byte("# tuned\n" + source))
	if got.end != workflowEditReopen {
		t.Fatalf("end = %v, want a reopen", got.end)
	}
	if !strings.Contains(string(got.buffer), "builder") {
		t.Errorf("the problem must name the actor:\n%s", string(got.buffer))
	}
}

// workflowEditReopenModel is the workflows list after a pass the rules rejected:
// the editor has the buffer with its problems on top, and the notice says what
// is waiting in it. Only the notice is fed to the shell, since the command that
// reopens the editor needs a terminal to run in.
func workflowEditReopenModel(t *testing.T, width, height int) Model {
	t.Helper()
	t.Setenv("VISUAL", workflowEditScript(t, "name: fix-first\nstart: ghost\nsteps:\n"+
		"  build: { run: builder, on: { done: done } }\n"))

	m := workflowsViewModelOn(t, width, height, workflowsFake(), "fix-first")
	step := workflowEditStepFor(t, m, "fix-first")
	_, cmd, res := workflowEditPass(t, m, step)
	if res.end != workflowEditReopen {
		t.Fatalf("the golden's pass must reopen, ended %v", res.end)
	}
	for _, inner := range cmd().(tea.BatchMsg) {
		if msg, ok := inner().(noticeMsg); ok {
			res, _ := m.Update(msg)
			return res.(Model)
		}
	}
	t.Fatal("a reopen must say what the editor is waiting on")
	return m
}
