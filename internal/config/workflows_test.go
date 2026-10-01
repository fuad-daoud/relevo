package config

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// wfValidSource is a workflow that needs no actor, so a section test never has
// to build a registry.
const wfValidSource = `name: custom
params: { scan: true }
start: gate
steps:
  gate: { when: "{{params.scan}}", on: { true: done, false: done } }
`

// wfOtherSource parses to a different definition under the same name, which is
// what a source/definition disagreement looks like.
const wfOtherSource = `name: custom
params: { scan: true }
start: gate
steps:
  gate: { when: "{{params.scan}}", on: { true: done, false: { halt: "no" } } }
`

func wfParse(t *testing.T, source string) workflow.Definition {
	t.Helper()
	def, err := workflow.Parse([]byte(source))
	if err != nil {
		t.Fatalf("workflow.Parse: %v", err)
	}
	return def
}

func wfRaw(t *testing.T, def workflow.Definition) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("json.Marshal(definition): %v", err)
	}
	return raw
}

func wfBody(t *testing.T, name, source string, definition json.RawMessage) []byte {
	t.Helper()
	entry, err := json.Marshal(map[string]json.RawMessage{
		"source":     json.RawMessage(strconv.Quote(source)),
		"definition": definition,
	})
	if err != nil {
		t.Fatalf("json.Marshal(entry): %v", err)
	}
	body, err := json.Marshal(map[string]json.RawMessage{name: entry})
	if err != nil {
		t.Fatalf("json.Marshal(body): %v", err)
	}
	return body
}

func TestWorkflowsSectionValidate(t *testing.T) {
	t.Parallel()

	def := wfRaw(t, wfParse(t, wfValidSource))
	if _, err := Validate(Workflows, wfBody(t, "custom", wfValidSource, def)); err != nil {
		t.Fatalf("a valid workflows section: %v", err)
	}

	// A file: seed is stored embedded, so its source and definition differ in
	// exactly that one field; the validator must take the stored seed as given.
	seedSource := `name: custom
start: build
steps:
  build: { run: builder, seed: "file:prompt.txt", on: { done: done } }
`
	embedded := wfParse(t, seedSource)
	step := embedded.Steps["build"]
	step.Seed = "hello seed\n"
	embedded.Steps["build"] = step
	if _, err := Validate(Workflows, wfBody(t, "custom", seedSource, wfRaw(t, embedded))); err != nil {
		t.Fatalf("an embedded file: seed: %v", err)
	}

	cases := []struct {
		name string
		body []byte
	}{
		{"bad name", wfBody(t, "Bad", wfValidSource, def)},
		{"key mismatch", wfBody(t, "other", wfValidSource, def)},
		{"source/definition disagreement", wfBody(t, "custom", wfOtherSource, def)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Validate(Workflows, c.body); err == nil {
				t.Fatalf("%s: Validate accepted the body", c.name)
			}
		})
	}
}

func TestExportImportCarriesWorkflows(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	body, err := EncodeWorkflows(map[string]StoredWorkflow{
		"custom": {Source: wfValidSource, Definition: wfParse(t, wfValidSource)},
	})
	if err != nil {
		t.Fatalf("EncodeWorkflows: %v", err)
	}
	if _, err := s.Put(Workflows, body); err != nil {
		t.Fatalf("Put(workflows): %v", err)
	}

	doc, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	exported, err := EncodeDoc(doc)
	if err != nil {
		t.Fatalf("EncodeDoc: %v", err)
	}
	var imported Doc
	if err := json.Unmarshal(exported, &imported); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	s2 := openStore(t)
	if _, err := s2.PutDoc(imported); err != nil {
		t.Fatalf("PutDoc: %v", err)
	}
	L, err := s2.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w, ok := L.Workflows["custom"]
	if !ok {
		t.Fatal("custom missing after import")
	}
	if w.Source != wfValidSource {
		t.Errorf("imported source = %q, want %q", w.Source, wfValidSource)
	}
	if !reflect.DeepEqual(w.Definition, wfParse(t, wfValidSource)) {
		t.Errorf("imported definition = %+v, want the parsed source", w.Definition)
	}
}
