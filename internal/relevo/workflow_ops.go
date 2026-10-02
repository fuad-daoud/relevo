package relevo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// The origins a workflow reaches a caller from: the one that ships with the
// binary, or one the user saved on this machine.
const (
	WorkflowOriginShipped = "shipped"
	WorkflowOriginSaved   = "saved"
)

// workflowActorCLI is the actor a CLI write is recorded under. A cockpit write
// is recorded under "ui" by WriteConfigEdit, so the two never share a label.
const workflowActorCLI = "cli"

// The classes a workflow operation fails with. Each is a distinct class rather
// than one "workflow failed" error because the callers differ: the CLI maps a
// class to an exit code and the cockpit maps it to a line of text, and only that
// mapping differs -- the check itself lives here, once.
var (
	// ErrWorkflowShipped says the name is the shipped workflow's, so saving
	// over it shadows a workflow that cannot be replaced.
	ErrWorkflowShipped = errors.New("workflow is shipped")
	// ErrWorkflowSaved says a saved workflow already holds the name, so saving
	// again would replace one the user still has.
	ErrWorkflowSaved = errors.New("workflow is already saved")
	// ErrWorkflowNotSaved says nothing is saved under that name.
	ErrWorkflowNotSaved = errors.New("workflow is not saved")
	// ErrWorkflowSource says the source could not be read, parsed, or carried a
	// file: seed that could not be embedded.
	ErrWorkflowSource = errors.New("workflow source is unusable")
	// ErrWorkflowInvalid says the definition breaks a rule of the format.
	ErrWorkflowInvalid = errors.New("workflow is not valid")
	// ErrWorkflowStore says the config store could not be read or refused the
	// write.
	ErrWorkflowStore = errors.New("workflows section is unavailable")
	// ErrWorkflowEncode says the workflows section could not be rendered.
	ErrWorkflowEncode = errors.New("workflows section cannot be encoded")
)

// WorkflowError is the one failure the workflow operations return: the class a
// caller maps to its own vocabulary, the workflow it happened to, and the whole
// message to show. Text is complete, so a caller renders it verbatim rather than
// formatting a class's own wording a second time.
type WorkflowError struct {
	class error
	name  string
	text  string
}

// Error returns the message, so the failure reads as the sentence it was
// written as.
func (e *WorkflowError) Error() string { return e.text }

// Unwrap yields the class, so errors.Is reaches it through the message.
func (e *WorkflowError) Unwrap() error { return e.class }

// Workflow is the name the failure happened to. It is "" when the failure is
// about a source file that carries no usable name yet.
func (e *WorkflowError) Workflow() string { return e.name }

// workflowErrorf builds a classed failure with a complete message.
func workflowErrorf(class error, name, format string, args ...any) error {
	return &WorkflowError{class: class, name: name, text: fmt.Sprintf(format, args...)}
}

// WorkflowParam is one param a workflow declares: its name, the kind it
// declares, and the value it ships with, so a view renders all three without
// reading the definition's own map.
type WorkflowParam struct {
	Name  string
	Kind  workflow.ParamKind
	Value string
}

// WorkflowSummary is one workflow as a list shows it: where it came from, what
// it says about itself, and what it takes. It carries no steps, because a list
// is a list: WorkflowGraph is where a definition's shape lives.
type WorkflowSummary struct {
	Name        string
	Origin      string
	Description string
	Inputs      workflow.Inputs
	Params      []WorkflowParam
}

// WorkflowList returns every workflow this machine can start a chain with: the
// shipped one first, then the saved ones by name. A runtime with no config store
// still lists the shipped one, which is the case a runtime with no database has.
func WorkflowList(rt Runtime) ([]WorkflowSummary, error) {
	shipped := workflow.Default()
	out := []WorkflowSummary{workflowSummary(shipped.Name, WorkflowOriginShipped, shipped)}

	saved, err := loadWorkflows(rt)
	if err != nil {
		return nil, err
	}
	for _, name := range sortedWorkflowNames(saved) {
		out = append(out, workflowSummary(name, WorkflowOriginSaved, saved[name].Definition))
	}
	return out, nil
}

// WorkflowSource returns a workflow's source text as the user wrote it. The
// saved workflows are consulted first, so a workflow that shadows the shipped
// name serves its own source the way the CLI did before --force existed as a
// special case here. The shipped workflow has no source to show, because it
// ships as a parsed definition and ResolveWorkflow has no text for it, so an
// unsaved shipped name reports shipped with empty text and the caller renders
// the definition instead.
func WorkflowSource(rt Runtime, name string) (string, bool, error) {
	saved, err := loadWorkflows(rt)
	if err != nil {
		return "", false, err
	}
	if w, ok := saved[name]; ok {
		return w.Source, false, nil
	}
	if name == workflow.Default().Name {
		return "", true, nil
	}
	return "", false, workflowErrorf(ErrWorkflowNotSaved, name, "workflow %q is not saved", name)
}

// WorkflowAdd reads path, validates the workflow it holds against the actors
// this machine has, and saves its source beside the definition. file: seeds are
// embedded relative to the file, so the file itself is not needed again. It
// returns the name it stored.
//
// replace allows a saved workflow of the same name to be overwritten, and force
// allows the name to shadow the shipped one. Neither implies the other: a name
// can be saved over without shadowing anything, and a shipped name can be
// shadowed without replacing anything.
func WorkflowAdd(rt Runtime, path string, replace, force bool) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", workflowErrorf(ErrWorkflowSource, "", "workflow add: %v", err)
	}
	def, err := workflow.Parse(raw)
	if err != nil {
		return "", workflowErrorf(ErrWorkflowSource, "", "%v", err)
	}
	if err := checkWorkflowName(rt, def.Name, replace, force); err != nil {
		return "", err
	}
	embedded, err := EmbedFileSeeds(def, filepath.Dir(path))
	if err != nil {
		return "", workflowErrorf(ErrWorkflowSource, def.Name, "%v", err)
	}
	if problems := WorkflowValidateProblems(rt, embedded); len(problems) > 0 {
		return "", workflowErrorf(ErrWorkflowInvalid, embedded.Name, "%s", joinProblems(problems))
	}
	if err := WorkflowSave(rt, embedded.Name, raw, embedded, "config workflow add "+embedded.Name); err != nil {
		return "", err
	}
	return embedded.Name, nil
}

// WorkflowRemove drops a saved workflow, leaving the rest of the section in
// place. A shipped workflow is refused along with an unknown one: it is not
// saved, so there is nothing here that could remove it. A saved workflow that
// shadows the shipped one is not refused -- it is a saved workflow, and removing
// it is how the shadow goes away.
func WorkflowRemove(rt Runtime, name string) error {
	saved, err := loadWorkflows(rt)
	if err != nil {
		return err
	}
	if _, ok := saved[name]; !ok {
		return workflowErrorf(ErrWorkflowNotSaved, name, "workflow %q is not saved", name)
	}

	next := make(map[string]config.StoredWorkflow, len(saved)-1)
	for key, w := range saved {
		if key != name {
			next[key] = w
		}
	}
	return writeWorkflows(rt, workflowActorCLI, "config workflow rm "+name, next)
}

// WorkflowSave stores name's source and definition, leaving every other saved
// workflow in place, as a CLI write recorded under the cli actor.
func WorkflowSave(rt Runtime, name string, source []byte, def workflow.Definition, message string) error {
	body, err := workflowSection(rt, name, source, def)
	if err != nil {
		return err
	}
	return putWorkflows(rt, workflowActorCLI, message, body)
}

// WorkflowEdit is the audited change that stores name's source and definition.
// The cockpit hands it to ApplyConfig, so a workflow write takes the same path
// as every other config edit it makes and is recorded under the ui actor.
func WorkflowEdit(rt Runtime, name string, source []byte, def workflow.Definition, message string) (ConfigEdit, error) {
	body, err := workflowSection(rt, name, source, def)
	if err != nil {
		return ConfigEdit{}, err
	}
	return ConfigEdit{
		Sections: map[config.Section]json.RawMessage{config.Workflows: body},
		Message:  message,
		Name:     name,
	}, nil
}

// WorkflowDefinition returns the parsed definition of a saved workflow: the
// source with its file: seeds already replaced by the contents they named when
// the workflow was saved. A shipped workflow is refused with ErrWorkflowNotSaved,
// because a caller asking for a saved one wants the saved one and nothing else;
// a caller that wants the shipped definition asks for it by WorkflowList or by
// workflow.Default.
func WorkflowDefinition(rt Runtime, name string) (workflow.Definition, error) {
	saved, err := loadWorkflows(rt)
	if err != nil {
		return workflow.Definition{}, err
	}
	w, ok := saved[name]
	if !ok {
		return workflow.Definition{}, workflowErrorf(ErrWorkflowNotSaved, name, "workflow %q is not saved", name)
	}
	return w.Definition, nil
}

// WorkflowValidateProblems runs the full format and rule checks against the
// actors this machine has, and returns one rendered line per failure in the
// order the rules report them. It returns the problems rather than a joined
// error so a caller can show them inline; joinProblems joins them for a caller
// that has one place to put them.
//
// Given is nil, so a rule about the chain's inputs is left to chain start.
func WorkflowValidateProblems(rt Runtime, def workflow.Definition) []string {
	env := workflow.Env{
		Actors: rt.RoleRegistry().WorkflowActors(),
		Given:  nil,
		Seeds:  workflow.ShippedSeeds(),
		Workflow: func(name string) (workflow.Definition, bool) {
			d, _, err := ResolveWorkflow(rt, name)
			return d, err == nil
		},
	}
	problems := workflow.Validate(def, env)
	if len(problems) == 0 {
		return nil
	}
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.String())
	}
	return out
}

// GraphRow is one step of a workflow's graph, in the order a reader walks it:
// the definition's own order, start first. Kind is the single kind the step
// declares, empty when it declares more than one; Actors names the run actor,
// the check actor and any fork's workflows. Edges are the step's step-targets:
// its on targets and its budget then. done and halt carry no edge, because
// neither names a step to go to, so a reader sees them as an end of a path
// rather than as a missing row.
type GraphRow struct {
	ID     string
	Kind   string
	Actors string
	Edges  []string
}

// WorkflowGraph returns def's steps as graph rows, in the same order the chain
// view walks them: breadth-first from the definition's start, then every step no
// path from the start reaches, sorted. A definition with no start, or a start
// that is not a step, lists its steps sorted.
func WorkflowGraph(def workflow.Definition) []GraphRow {
	order := orderWorkflowSteps(def)
	edges := workflow.StepEdges(def)

	rows := make([]GraphRow, 0, len(order))
	for _, id := range order {
		rows = append(rows, GraphRow{
			ID:     id,
			Kind:   workflow.StepKind(def, id),
			Actors: workflow.StepActors(def, id),
			Edges:  append([]string(nil), edges[id]...),
		})
	}
	return rows
}

// checkWorkflowName refuses a name that is shipped, or already saved, unless the
// caller asked for the one flag that allows it.
func checkWorkflowName(rt Runtime, name string, replace, force bool) error {
	if name == workflow.Default().Name && !force {
		return workflowErrorf(ErrWorkflowShipped, name, "workflow %q is shipped; pass --force to shadow it", name)
	}
	saved, err := loadWorkflows(rt)
	if err != nil {
		return err
	}
	if _, ok := saved[name]; ok && !replace {
		return workflowErrorf(ErrWorkflowSaved, name, "workflow %q is already saved; pass --replace to overwrite it", name)
	}
	return nil
}

// loadWorkflows reads the saved workflows. A runtime with no config store has
// none, which is the same answer as a machine that has saved nothing.
func loadWorkflows(rt Runtime) (map[string]config.StoredWorkflow, error) {
	if rt.Config == nil {
		return map[string]config.StoredWorkflow{}, nil
	}
	L, err := rt.Config.Load()
	if err != nil {
		return nil, workflowErrorf(ErrWorkflowStore, "", "%v", err)
	}
	if L.Workflows == nil {
		return map[string]config.StoredWorkflow{}, nil
	}
	return L.Workflows, nil
}

// workflowSection renders the whole workflows section with name's source and
// definition stored under it, keeping every other saved workflow in place. The
// section is rendered whole rather than patched, so a removal and an overwrite
// are the same write.
func workflowSection(rt Runtime, name string, source []byte, def workflow.Definition) ([]byte, error) {
	saved, err := loadWorkflows(rt)
	if err != nil {
		return nil, err
	}
	next := make(map[string]config.StoredWorkflow, len(saved)+1)
	for key, w := range saved {
		next[key] = w
	}
	next[name] = config.StoredWorkflow{Source: string(source), Definition: def}
	body, err := config.EncodeWorkflows(next)
	if err != nil {
		return nil, workflowErrorf(ErrWorkflowEncode, name, "%v", err)
	}
	return body, nil
}

// putWorkflows stores body as the workflows section, recorded under actor.
func putWorkflows(rt Runtime, actor, message string, body []byte) error {
	if rt.Config == nil {
		return workflowErrorf(ErrWorkflowStore, "", "no config store")
	}
	if _, err := rt.Config.As(actor, message).Put(config.Workflows, body); err != nil {
		return workflowErrorf(ErrWorkflowStore, "", "%v", err)
	}
	return nil
}

// writeWorkflows renders next as the whole section and stores it.
func writeWorkflows(rt Runtime, actor, message string, next map[string]config.StoredWorkflow) error {
	body, err := config.EncodeWorkflows(next)
	if err != nil {
		return workflowErrorf(ErrWorkflowEncode, "", "%v", err)
	}
	return putWorkflows(rt, actor, message, body)
}

// workflowSummary is one row of the list, built from the definition it lists.
func workflowSummary(name, origin string, def workflow.Definition) WorkflowSummary {
	return WorkflowSummary{
		Name:        name,
		Origin:      origin,
		Description: def.Description,
		Inputs:      def.Inputs,
		Params:      workflowParams(def),
	}
}

// workflowParams returns a definition's params by name, each with its kind and
// the value it ships with. A definition with no params yields none rather than
// an empty slice, so a caller can tell "declares none" from "not read".
func workflowParams(def workflow.Definition) []WorkflowParam {
	if len(def.Params) == 0 {
		return nil
	}
	names := make([]string, 0, len(def.Params))
	for name := range def.Params {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]WorkflowParam, 0, len(names))
	for _, name := range names {
		p := def.Params[name]
		out = append(out, WorkflowParam{Name: name, Kind: p.Kind, Value: paramValue(p)})
	}
	return out
}

// paramValue renders a param's shipped value as the one word a listing shows.
func paramValue(p workflow.Param) string {
	switch p.Kind {
	case workflow.ParamBool:
		return strconv.FormatBool(p.Bool)
	case workflow.ParamInt:
		return strconv.Itoa(p.Int)
	case workflow.ParamString:
		return p.Str
	default:
		return ""
	}
}

// sortedWorkflowNames returns the saved workflows' names, sorted.
func sortedWorkflowNames(m map[string]config.StoredWorkflow) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// joinProblems is how a caller with one place to put the problems renders them.
func joinProblems(problems []string) string {
	return strings.Join(problems, "; ")
}
