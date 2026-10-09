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

// ShowArgs is show's input: Name is required, Round 0 reads the newest
// completed round, and an empty Section reads the prompt.
type ShowArgs struct {
	Name    string `json:"name"`
	Round   int    `json:"round,omitempty"`
	Section string `json:"section,omitempty"`
}

// GateArgs is gate's input: Token is required, Clear lifts the gate instead of
// setting one, and For is how long a set gate holds.
type GateArgs struct {
	Token  string `json:"token"`
	For    string `json:"for,omitempty"`
	Reason string `json:"reason,omitempty"`
	Clear  bool   `json:"clear,omitempty"`
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

// WaitPointer points at the wait tool carrying the round budget.
func WaitPointer(name, budget string) string {
	return "wait tool:\n  wait(name: \"" + name + "\", timeout: \"" + budget + "\")"
}

// appendWaitPointer appends the wait tool pointer, unless there is no text or no budget.
func appendWaitPointer(r ToolResult, name, budget string) ToolResult {
	if budget == "" || len(r.Content) == 0 {
		return r
	}
	r.Content[0].Text += "\n\n" + WaitPointer(name, budget)
	return r
}

func waitToolSpec() ToolSpec {
	return ToolSpec{
		Name:        "wait",
		Description: "Block until a round closes, needs attention, or times out. Omit name to wait on all active bindings this MasterMind owns.",
		InputSchema: schemaObject(nil, map[string]any{
			"name":    map[string]any{"type": "string", "description": "binding name; omit to wait on all active bindings this MasterMind owns"},
			"round":   map[string]any{"type": "integer", "description": "the round to wait for; omit or 0 for the newest planned round"},
			"timeout": map[string]any{"type": "string", "description": "maximum time to wait as a duration (e.g. 10m, 2h); defaults to the round budget"},
		}),
	}
}

// ToolsFor returns the tool list for kind: a Claude Code server includes wait,
// while an opencode server lists the five base verbs.
func ToolsFor(mode Mode, kind string) []ToolSpec {
	base := Tools()
	if kind != "opencode" {
		return []ToolSpec{
			base[0], // status
			base[1], // send
			waitToolSpec(),
			base[2], // done
			base[3], // show
			base[4], // gate
		}
	}
	return base
}

// Tools is the base tools/list document: status, send, done, show, gate, in that order.
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
				"candidate": map[string]any{"type": "string", "description": "candidate name or token to run this round and later ones on (persists); refused while a live round is open (a halted served round may be re-pointed)"},
				"verify":    map[string]any{"type": "boolean", "description": "run a read-only reviewer when the round closes"},
				"regate":    map[string]any{"type": "integer", "description": "automatic repair rounds after a failing gate; 0 disables"},
				"dry_run":   map[string]any{"type": "boolean", "description": "check preconditions and report what send would do, without sending"},
			}),
		},
		{
			Name:        "done",
			Description: "Mark a binding done once its round is verified; relaying stops. A name that is a chain releases every member instead. Calls relevo.Done.",
			InputSchema: schemaObject([]string{"name"}, map[string]any{
				"name": map[string]any{"type": "string", "description": "binding name"},
			}),
		},
		// show's sections are the round's own files; findings and artifact are
		// left to the CLI because their ids have no argument in this schema.
		{
			Name:        "show",
			Description: "One round of a binding: its prompt (the default), report, diff, drift, log, transcript, gate log, output or artifact list. Calls relevo.Show.",
			InputSchema: schemaObject([]string{"name"}, map[string]any{
				"name":    map[string]any{"type": "string", "description": "binding name"},
				"round":   map[string]any{"type": "integer", "description": "the round to read; omit for the newest completed round"},
				"section": map[string]any{"type": "string", "description": "prompt (the default) | report | diff | drift | log | transcript | gate | output | artifacts"},
			}),
		},
		{
			Name:        "gate",
			Description: "Record that a provider hit a usage limit -- relevo switches and resends -- or clear the gate again. Calls relevo's gate ledger.",
			InputSchema: schemaObject([]string{"token"}, map[string]any{
				"token":  map[string]any{"type": "string", "description": "candidate name or token to gate, or the subject to clear"},
				"for":    map[string]any{"type": "string", "description": "how long the gate holds, as a Go duration (e.g. 2h); omit to hold until cleared"},
				"reason": map[string]any{"type": "string", "description": "why, for the record"},
				"clear":  map[string]any{"type": "boolean", "description": "clear the gate instead of setting one"},
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

func validateShowArgs(a ShowArgs) error {
	if a.Name == "" {
		return errors.New("show requires name")
	}
	return nil
}

func validateGateArgs(a GateArgs) error {
	if a.Token == "" {
		return errors.New("gate requires token")
	}
	return nil
}
