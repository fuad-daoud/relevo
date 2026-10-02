package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// Workflows is every workflow this machine can start a chain with: the shipped
// one first, then the saved ones by name.
func (a *mastermindActions) Workflows() ([]relevo.WorkflowSummary, error) {
	return relevo.WorkflowList(a.runtime())
}

// WorkflowSource is one workflow's source text and whether it is the shipped
// one. A shipped workflow has no source to show -- it ships as a parsed
// definition, and ResolveWorkflow has no text for it -- so it answers with the
// definition rendered as JSON in place of the source, which is what the view
// shows the s key.
func (a *mastermindActions) WorkflowSource(name string) (string, bool, error) {
	source, shipped, err := relevo.WorkflowSource(a.runtime(), name)
	if err != nil || !shipped {
		return source, shipped, err
	}
	def, _, derr := relevo.ResolveWorkflow(a.runtime(), name)
	if derr != nil {
		return "", true, derr
	}
	raw, jerr := json.MarshalIndent(def, "", "  ")
	if jerr != nil {
		return "", true, jerr
	}
	return string(raw) + "\n", true, nil
}

// WorkflowGraph is one workflow's steps as graph rows, read from its definition
// the same way whether it is saved or shipped.
func (a *mastermindActions) WorkflowGraph(name string) ([]relevo.GraphRow, error) {
	def, _, err := relevo.ResolveWorkflow(a.runtime(), name)
	if err != nil {
		return nil, err
	}
	return relevo.WorkflowGraph(def), nil
}

// WorkflowAdd validates and stores the workflow a file holds. force is never
// passed: the cockpit offers no way to shadow a shipped workflow, so the name
// is refused rather than shadowed. The write goes through the same reload
// ApplyConfig's does, so the next read sees what was stored.
func (a *mastermindActions) WorkflowAdd(_ context.Context, path string, replace bool) Result {
	name, err := relevo.WorkflowAdd(a.runtime(), expandHome(path), replace, false)
	if err != nil {
		return Result{Err: err, Refresh: true}
	}
	if err := a.live.Refresh(); err != nil {
		return Result{Text: "stored " + name + "; reload failed: " + err.Error(), Refresh: true}
	}
	return Result{Text: "stored workflow " + name, Refresh: true}
}

// WorkflowRemove drops a saved workflow. A shipped one is refused by
// WorkflowRemove itself: it is not saved, so there is nothing here to remove.
func (a *mastermindActions) WorkflowRemove(_ context.Context, name string) Result {
	if err := relevo.WorkflowRemove(a.runtime(), name); err != nil {
		return Result{Err: err, Refresh: true}
	}
	if err := a.live.Refresh(); err != nil {
		return Result{Text: "removed " + name + "; reload failed: " + err.Error(), Refresh: true}
	}
	return Result{Text: "removed workflow " + name, Refresh: true}
}

// workflowWriteKey is the action key a workflow write runs under, so a second
// write on the same workflow is refused the way every other action is.
func workflowWriteKey(verb, name string) string { return verb + " workflow:" + name }

// expandHome resolves a leading ~ in a path the way a shell does, so the form
// takes the path a user would type at a prompt. A path with no ~ is unchanged,
// and a home that cannot be read leaves the ~ in place rather than guessing.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// workflowProblems returns the validation problems a workflow failure carries,
// one per line for a caller with room for them, and the message alone for every
// other class, which has no problems of its own.
func workflowProblems(err error) []string {
	var we *relevo.WorkflowError
	if errors.As(err, &we) && len(we.Problems()) > 0 {
		return we.Problems()
	}
	return []string{err.Error()}
}
