package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ToolSpec is one entry of the tools/list document: additionalProperties is
// always false so an unexpected argument is a visible mistake, not ignored.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// StatusArgs is status's input: {name?, all?}.
type StatusArgs struct {
	Name string `json:"name,omitempty"`
	All  bool   `json:"all,omitempty"`
}

// SendArgs is send's input; Name and File are required.
type SendArgs struct {
	Name      string `json:"name"`
	File      string `json:"file"`
	Tier      string `json:"tier,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	Verify    *bool  `json:"verify,omitempty"`
	Regate    *int   `json:"regate,omitempty"`
	DryRun    bool   `json:"dry_run,omitempty"`
}

type DoneArgs struct {
	Name string `json:"name"`
}

type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Content is one block of a ToolResult; every tool here emits exactly one, of type "text".
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textResult(s string, isError bool) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: s}}, IsError: isError}
}

// jsonResult marshals v, indented for a human skimming the transcript.
func jsonResult(v any) (ToolResult, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ToolResult{}, err
	}
	return textResult(string(raw), false), nil
}

func schemaObject(required []string, props map[string]any) map[string]any {
	s := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// WaitCommand is the tools-mode background wait, run with run_in_background
// while the round runs; budget is the round timeout for `relevo wait --timeout`.
func WaitCommand(name, budget string) string {
	return "background wait (run with run_in_background, then end your turn):\n" +
		"  relevo wait --name " + name + " --timeout " + budget
}

// appendWaitCommand appends the background-wait block, unless there is no text or no budget.
func appendWaitCommand(r ToolResult, name, budget string) ToolResult {
	if budget == "" || len(r.Content) == 0 {
		return r
	}
	r.Content[0].Text += "\n\n" + WaitCommand(name, budget)
	return r
}

// Tools is the tools/list document: status, send, done, in that order.
func Tools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "status",
			Description: "One binding, or every binding on this MasterMind, or (all: true) every binding relevo knows about. Calls relevo.Status.",
			InputSchema: schemaObject(nil, map[string]any{
				"name": map[string]any{"type": "string", "description": "show only this binding"},
				"all":  map[string]any{"type": "boolean", "description": "include every binding relevo knows about, not just this MasterMind's"},
			}),
		},
		{
			Name:        "send",
			Description: "Hand a binding's runner a new round: stage file as the round's plan and prompt the runner. Calls relevo.Send, or relevo.SendDryRun when dry_run is true.",
			InputSchema: schemaObject([]string{"name", "file"}, map[string]any{
				"name":      map[string]any{"type": "string", "description": "binding name"},
				"file":      map[string]any{"type": "string", "description": "path to the plan file"},
				"tier":      map[string]any{"type": "string", "description": "permission tier override: harness|read|edit|yolo"},
				"candidate": map[string]any{"type": "string", "description": "candidate name or token to run this round and later ones on (persists); refused while a round is open"},
				"verify":    map[string]any{"type": "boolean", "description": "run a read-only reviewer when the round closes"},
				"regate":    map[string]any{"type": "integer", "description": "automatic repair rounds after a failing gate; 0 disables"},
				"dry_run":   map[string]any{"type": "boolean", "description": "check preconditions and report what send would do, without sending"},
			}),
		},
		{
			Name:        "done",
			Description: "Mark a binding done once its round is verified; relaying stops. Calls relevo.Done.",
			InputSchema: schemaObject([]string{"name"}, map[string]any{
				"name": map[string]any{"type": "string", "description": "binding name"},
			}),
		},
	}
}

// decodeArgs strictly decodes raw (empty treated as {}) into dst, so a typo in a tool call is visible.
func decodeArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing data after arguments")
	}
	return nil
}

func validateSendArgs(a SendArgs) error {
	if a.Name == "" {
		return errors.New("send requires name")
	}
	if a.File == "" {
		return errors.New("send requires file")
	}
	return nil
}

func validateDoneArgs(a DoneArgs) error {
	if a.Name == "" {
		return errors.New("done requires name")
	}
	return nil
}
