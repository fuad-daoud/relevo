package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// OutputKind is the kind of an actor output declaration.
type OutputKind string

// The kinds an output may have.
const (
	OutputOneOf    OutputKind = "one-of"
	OutputCount    OutputKind = "count"
	OutputArtifact OutputKind = "artifact"
)

// Output is one declared actor output: an enumerated outcome with its values, a
// non-negative count, or an artifact file a later step reads.
type Output struct {
	Kind   OutputKind
	Values []string
}

// Outputs maps an output key to its declaration.
type Outputs map[string]Output

// MarshalJSON renders the outputs in wire form.
func (o Outputs) MarshalJSON() ([]byte, error) {
	if o == nil {
		return []byte("null"), nil
	}
	m := make(map[string]any, len(o))
	for _, key := range sortedKeys(o) {
		out := o[key]
		switch out.Kind {
		case OutputOneOf:
			if len(out.Values) == 0 {
				return nil, fmt.Errorf("workflow: output %q: one-of needs at least one value", key)
			}
			m[key] = map[string]any{"one-of": out.Values}
		case OutputCount:
			m[key] = string(OutputCount)
		case OutputArtifact:
			m[key] = string(OutputArtifact)
		default:
			return nil, fmt.Errorf("workflow: output %q: unknown kind %q", key, out.Kind)
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON reads outputs from JSON using decodeOutputs.
func (o *Outputs) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*o = nil
		return nil
	}
	v, err := parseOutputJSON(data)
	if err != nil {
		return err
	}
	decoded, err := decodeOutputs(v, "")
	if err != nil {
		return err
	}
	*o = decoded
	return nil
}

// Outcomes returns the non-artifact output keys, sorted.
func (o Outputs) Outcomes() []string {
	keys := make([]string, 0, len(o))
	for key, out := range o {
		if out.Kind != OutputArtifact {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// Artifacts returns the artifact output keys, sorted.
func (o Outputs) Artifacts() []string {
	keys := make([]string, 0, len(o))
	for key, out := range o {
		if out.Kind == OutputArtifact {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// ParseOutputs reads an outputs map from JSON when the first non-space byte is
// '{', and from YAML otherwise. The two formats are read with the same generic
// decode and key-to-string conversion the definition parser uses, so they
// produce the same Outputs by construction.
func ParseOutputs(data []byte) (Outputs, error) {
	v, err := parseOutputValue(data)
	if err != nil {
		return nil, err
	}
	return decodeOutputs(v, "")
}

// parseOutputValue decodes YAML or JSON into the generic value the output
// decoder reads, turning YAML map keys into strings.
func parseOutputValue(data []byte) (any, error) {
	if firstNonSpace(data) == '{' {
		return parseOutputJSON(data)
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}
	return stringKeys(v), nil
}

// parseOutputJSON decodes one JSON document and rejects trailing data, the same
// strictness ParseJSON applies.
func parseOutputJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("workflow: trailing data after the outputs")
	}
	return v, nil
}

// decodeOutputs reads a map of output declarations. The keys are actor-chosen,
// so only the declaration under each is strict.
func decodeOutputs(v any, path string) (Outputs, error) {
	obj, err := objectAt(v, path)
	if err != nil {
		return nil, err
	}
	outputs := make(Outputs, len(obj))
	for _, key := range sortedKeys(obj) {
		out, err := decodeOutput(obj[key], entryPath(path, key))
		if err != nil {
			return nil, err
		}
		outputs[key] = out
	}
	return outputs, nil
}

// decodeOutput reads one output declaration: the scalar count or artifact, or a
// {one-of: [...]} with at least one value and no duplicates.
func decodeOutput(v any, path string) (Output, error) {
	switch t := v.(type) {
	case string:
		switch OutputKind(t) {
		case OutputCount:
			return Output{Kind: OutputCount}, nil
		case OutputArtifact:
			return Output{Kind: OutputArtifact}, nil
		default:
			return Output{}, fmt.Errorf("%s: want one-of, count or artifact, got %q", orRoot(path), t)
		}
	case map[string]any:
		if err := onlyKeys(t, path, "one-of"); err != nil {
			return Output{}, err
		}
		raw, ok := t["one-of"]
		if !ok {
			return Output{}, fmt.Errorf("%s: want one-of, count or artifact", orRoot(path))
		}
		list, err := listAt(raw, fieldPath(path, "one-of"))
		if err != nil {
			return Output{}, err
		}
		values := make([]string, 0, len(list))
		seen := make(map[string]bool, len(list))
		for i, item := range list {
			value, err := asString(item, fmt.Sprintf("%s[%d]", fieldPath(path, "one-of"), i))
			if err != nil {
				return Output{}, err
			}
			if seen[value] {
				return Output{}, fmt.Errorf("%s: duplicate value %q", fieldPath(path, "one-of"), value)
			}
			seen[value] = true
			values = append(values, value)
		}
		if len(values) == 0 {
			return Output{}, fmt.Errorf("%s: one-of needs at least one value", fieldPath(path, "one-of"))
		}
		return Output{Kind: OutputOneOf, Values: values}, nil
	default:
		return Output{}, fmt.Errorf("%s: want one-of, count or artifact, got %s", orRoot(path), typeName(v))
	}
}

// Footer returns the prompt text a runner must end its output with: one line
// per declared artifact naming the file to write, then one fenced relevo block
// with one line per outcome. With no outcomes there is no block.
func Footer(o Outputs, artifactPaths map[string]string) string {
	var b strings.Builder
	for _, name := range o.Artifacts() {
		path := artifactPaths[name]
		if path == "" {
			path = name + ".md"
		}
		fmt.Fprintf(&b, "Write %s to %s.\n", name, path)
	}

	outcomes := o.Outcomes()
	if len(outcomes) == 0 {
		return b.String()
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString("```relevo\n")
	for _, key := range outcomes {
		b.WriteString(outcomeLine(key, o[key]))
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}

// OutcomeLine renders one outcome's placeholder line: a count shows 0, and a
// one-of names its first value and lists the rest after it.
func OutcomeLine(key string, out Output) string {
	if out.Kind == OutputCount {
		return key + ": 0   # a count"
	}
	line := key + ": " + out.Values[0]
	if len(out.Values) > 1 {
		line += "   # or: " + strings.Join(out.Values[1:], ", ")
	}
	return line
}

func outcomeLine(key string, out Output) string {
	return OutcomeLine(key, out)
}
