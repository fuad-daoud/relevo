package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// StoredWorkflow is one saved workflow: the source text as the user wrote it,
// beside the definition it parses to. The definition differs from a plain
// re-parse of the source in one place: a file: seed is replaced by the contents
// of the file it names when the workflow is saved, because that file is not
// kept.
type StoredWorkflow struct {
	Source     string
	Definition workflow.Definition
}

// workflowBody is the wire shape of one stored workflow.
type workflowBody struct {
	Source     string          `json:"source"`
	Definition json.RawMessage `json:"definition"`
}

// loadWorkflows fills L.Workflows from the stored body. An absent section is an
// empty map, so a machine with no saved workflow lists only the shipped ones.
func loadWorkflows(doc Doc, L *Loaded) error {
	body, ok := doc[Workflows]
	if !ok {
		L.Workflows = map[string]StoredWorkflow{}
		return nil
	}
	w, err := parseWorkflows(body)
	if err != nil {
		return err
	}
	L.Workflows = w
	return nil
}

// parseWorkflows reads and checks the workflows body: every key is a valid
// workflow name, every source parses, every definition declares its own key as
// its name, and every definition is the parse of its source. It never consults
// the actor registry, so removing an actor a saved workflow names stays
// allowed.
func parseWorkflows(body []byte) (map[string]StoredWorkflow, error) {
	var raw map[string]workflowBody
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", Workflows, err)
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make(map[string]StoredWorkflow, len(raw))
	for _, name := range names {
		w, err := parseWorkflowEntry(name, raw[name])
		if err != nil {
			return nil, err
		}
		out[name] = w
	}
	return out, nil
}

func parseWorkflowEntry(name string, entry workflowBody) (StoredWorkflow, error) {
	if err := workflow.ValidName(name); err != nil {
		return StoredWorkflow{}, fmt.Errorf("%s: %w", Workflows, err)
	}
	def, err := workflow.Parse([]byte(entry.Source))
	if err != nil {
		return StoredWorkflow{}, fmt.Errorf("%s: workflow %q: %w", Workflows, name, err)
	}
	if def.Name != name {
		return StoredWorkflow{}, fmt.Errorf("%s: workflow %q declares name %q", Workflows, name, def.Name)
	}
	if len(entry.Definition) == 0 {
		return StoredWorkflow{}, fmt.Errorf("%s: workflow %q has no definition", Workflows, name)
	}
	matches, err := definitionMatches(def, entry.Definition)
	if err != nil {
		return StoredWorkflow{}, fmt.Errorf("%s: workflow %q: %w", Workflows, name, err)
	}
	if !matches {
		return StoredWorkflow{}, fmt.Errorf("%s: workflow %q: the stored definition is not the parse of its source", Workflows, name)
	}
	return StoredWorkflow{Source: entry.Source, Definition: def}, nil
}

// definitionMatches reports whether the stored definition is the parse of def.
// A step whose source seed is a file: reference is the one exception: the saved
// definition carries that file's contents and the file is not re-read here, so
// the stored seed is taken as given for that step. Every other field must agree,
// which is what catches a hand-edited body whose two halves drifted apart.
func definitionMatches(def workflow.Definition, stored json.RawMessage) (bool, error) {
	marshalled, err := json.Marshal(def)
	if err != nil {
		return false, err
	}
	want, err := decodeJSONValue(marshalled)
	if err != nil {
		return false, err
	}
	got, err := decodeJSONValue(stored)
	if err != nil {
		return false, err
	}
	syncFileSeeds(want, got)
	return reflect.DeepEqual(got, want), nil
}

// syncFileSeeds copies each stored seed back onto the parse of a source step
// that names a file:, so the comparison ignores the one field the stored
// definition had to change when it embedded the file.
func syncFileSeeds(want, got any) {
	wantObj, ok := want.(map[string]any)
	if !ok {
		return
	}
	gotObj, ok := got.(map[string]any)
	if !ok {
		return
	}
	wantSteps, ok := wantObj["steps"].(map[string]any)
	if !ok {
		return
	}
	gotSteps, _ := gotObj["steps"].(map[string]any)
	for id, w := range wantSteps {
		step, ok := w.(map[string]any)
		if !ok {
			continue
		}
		seed, _ := step["seed"].(string)
		if !strings.HasPrefix(seed, "file:") {
			continue
		}
		if g, ok := gotSteps[id].(map[string]any); ok {
			step["seed"] = g["seed"]
		}
	}
}

// decodeJSONValue decodes one JSON value, keeping numbers as their literal text
// so an int and a float never compare equal by accident.
func decodeJSONValue(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// EncodeWorkflows renders the workflows section body: one entry per saved
// workflow, its source beside the definition's JSON. The map's keys sort, so
// the same set always encodes to the same bytes.
func EncodeWorkflows(m map[string]StoredWorkflow) ([]byte, error) {
	out := make(map[string]workflowBody, len(m))
	for name, w := range m {
		raw, err := json.Marshal(w.Definition)
		if err != nil {
			return nil, fmt.Errorf("%s: workflow %q: %w", Workflows, name, err)
		}
		out[name] = workflowBody{Source: w.Source, Definition: raw}
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Workflows, err)
	}
	return body, nil
}
