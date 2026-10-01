package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	yaml "go.yaml.in/yaml/v3"
)

// Parse reads a workflow as JSON when the first non-space byte is '{', and as
// YAML otherwise.
func Parse(data []byte) (Definition, error) {
	if firstNonSpace(data) == '{' {
		return ParseJSON(data)
	}
	return ParseYAML(data)
}

// firstNonSpace returns the first byte that is not whitespace, or 0 when the
// document is empty or all whitespace.
func firstNonSpace(data []byte) byte {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return b
	}
	return 0
}

// ParseJSON reads a workflow from JSON. An unknown field or a wrong type is an
// error that names the path at which it was found.
func ParseJSON(data []byte) (Definition, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Definition{}, fmt.Errorf("workflow: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Definition{}, fmt.Errorf("workflow: trailing data after the workflow")
	}
	return decodeDefinition(v, "")
}

// ParseYAML reads a workflow from YAML. YAML is decoded to generic values, its
// map keys are made strings, and the result is read by the JSON parser, so both
// formats accept exactly the same documents.
func ParseYAML(data []byte) (Definition, error) {
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return Definition{}, fmt.Errorf("workflow: %w", err)
	}
	encoded, err := json.Marshal(stringKeys(v))
	if err != nil {
		return Definition{}, fmt.Errorf("workflow: %w", err)
	}
	return ParseJSON(encoded)
}

// stringKeys rewrites every map in a decoded YAML value to a string-keyed map.
// A YAML bool key, as under a `when` step's on map, is not a string, so the
// JSON encoder cannot take it before this conversion.
func stringKeys(v any) any {
	switch t := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[keyString(k)] = stringKeys(val)
		}
		return m
	case map[string]any:
		for k, val := range t {
			t[k] = stringKeys(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = stringKeys(val)
		}
		return t
	default:
		return v
	}
}

// keyString renders a YAML map key as the string the JSON form uses.
func keyString(k any) string {
	switch t := k.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case nil:
		return "null"
	default:
		return fmt.Sprint(t)
	}
}

func decodeDefinition(v any, path string) (Definition, error) {
	obj, err := objectAt(v, path)
	if err != nil {
		return Definition{}, err
	}
	if err := onlyKeys(obj, path, "name", "description", "inputs", "params", "start", "steps"); err != nil {
		return Definition{}, err
	}
	var def Definition
	if def.Name, err = stringAt(obj, "name", path); err != nil {
		return Definition{}, err
	}
	if def.Description, err = stringAt(obj, "description", path); err != nil {
		return Definition{}, err
	}
	if def.Start, err = stringAt(obj, "start", path); err != nil {
		return Definition{}, err
	}
	if def.Inputs, err = decodeInputs(obj["inputs"], fieldPath(path, "inputs")); err != nil {
		return Definition{}, err
	}
	if def.Params, err = decodeParams(obj["params"], fieldPath(path, "params")); err != nil {
		return Definition{}, err
	}
	if def.Steps, err = decodeSteps(obj["steps"], fieldPath(path, "steps")); err != nil {
		return Definition{}, err
	}
	return def, nil
}

func decodeInputs(v any, path string) (Inputs, error) {
	in := Inputs{Plans: InputNone, Task: InputNone}
	if v == nil {
		return in, nil
	}
	obj, err := objectAt(v, path)
	if err != nil {
		return Inputs{}, err
	}
	if err := onlyKeys(obj, path, "plans", "task"); err != nil {
		return Inputs{}, err
	}
	if in.Plans, err = inputAt(obj, "plans", path); err != nil {
		return Inputs{}, err
	}
	if in.Task, err = inputAt(obj, "task", path); err != nil {
		return Inputs{}, err
	}
	return in, nil
}

func inputAt(obj map[string]any, key, path string) (InputMode, error) {
	s, present, err := optionalString(obj, key, path)
	if err != nil {
		return InputNone, err
	}
	if !present {
		return InputNone, nil
	}
	switch InputMode(s) {
	case InputNone, InputOptional, InputRequired:
		return InputMode(s), nil
	default:
		return InputNone, fmt.Errorf("%s: want none, optional or required, got %q", fieldPath(path, key), s)
	}
}

func decodeParams(v any, path string) (map[string]Param, error) {
	if v == nil {
		return nil, nil
	}
	obj, err := objectAt(v, path)
	if err != nil {
		return nil, err
	}
	params := make(map[string]Param, len(obj))
	for _, key := range sortedKeys(obj) {
		p, err := decodeParam(obj[key], entryPath(path, key))
		if err != nil {
			return nil, err
		}
		params[key] = p
	}
	return params, nil
}

func decodeParam(v any, path string) (Param, error) {
	switch t := v.(type) {
	case bool:
		return Param{Kind: ParamBool, Bool: t}, nil
	case string:
		return Param{Kind: ParamString, Str: t}, nil
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return Param{}, fmt.Errorf("%s: want bool, int or string, got %s", orRoot(path), t.String())
		}
		return Param{Kind: ParamInt, Int: int(i)}, nil
	default:
		return Param{}, fmt.Errorf("%s: want bool, int or string, got %s", orRoot(path), typeName(v))
	}
}

func decodeSteps(v any, path string) (map[string]Step, error) {
	if v == nil {
		return nil, nil
	}
	obj, err := objectAt(v, path)
	if err != nil {
		return nil, err
	}
	steps := make(map[string]Step, len(obj))
	for _, key := range sortedKeys(obj) {
		step, err := decodeStep(obj[key], entryPath(path, key))
		if err != nil {
			return nil, err
		}
		steps[key] = step
	}
	return steps, nil
}

func decodeStep(v any, path string) (Step, error) {
	obj, err := objectAt(v, path)
	if err != nil {
		return Step{}, err
	}
	if err := onlyKeys(obj, path, "run", "check", "for-each", "when", "fork", "seed", "budget", "on"); err != nil {
		return Step{}, err
	}
	var s Step
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"run", &s.Run},
		{"check", &s.Check},
		{"for-each", &s.ForEach},
		{"when", &s.When},
		{"seed", &s.Seed},
	} {
		val, err := stringAt(obj, f.key, path)
		if err != nil {
			return Step{}, err
		}
		*f.dst = val
	}
	if fv, ok := obj["fork"]; ok {
		fork, err := decodeFork(fv, fieldPath(path, "fork"))
		if err != nil {
			return Step{}, err
		}
		s.Fork = &fork
	}
	if bv, ok := obj["budget"]; ok {
		budget, err := decodeBudget(bv, fieldPath(path, "budget"))
		if err != nil {
			return Step{}, err
		}
		s.Budget = &budget
	}
	if ov, ok := obj["on"]; ok {
		on, err := decodeOn(ov, fieldPath(path, "on"))
		if err != nil {
			return Step{}, err
		}
		s.On = on
	}
	return s, nil
}

func decodeOn(v any, path string) (map[string]Target, error) {
	obj, err := objectAt(v, path)
	if err != nil {
		return nil, err
	}
	on := make(map[string]Target, len(obj))
	for _, key := range sortedKeys(obj) {
		target, err := decodeTarget(obj[key], entryPath(path, key))
		if err != nil {
			return nil, err
		}
		on[key] = target
	}
	return on, nil
}

func decodeTarget(v any, path string) (Target, error) {
	switch t := v.(type) {
	case string:
		if t == "done" {
			return DoneTarget(), nil
		}
		return StepTarget(t), nil
	case map[string]any:
		if err := onlyKeys(t, path, "halt"); err != nil {
			return Target{}, err
		}
		reason, present, err := optionalString(t, "halt", path)
		if err != nil {
			return Target{}, err
		}
		if !present {
			return Target{}, fmt.Errorf("%s: want a step id, done, or {halt: \"...\"}", orRoot(path))
		}
		return HaltTarget(reason), nil
	default:
		return Target{}, fmt.Errorf("%s: want a step id, done, or {halt: \"...\"}, got %s", orRoot(path), typeName(v))
	}
}

func decodeBudget(v any, path string) (Budget, error) {
	obj, err := objectAt(v, path)
	if err != nil {
		return Budget{}, err
	}
	if err := onlyKeys(obj, path, "max", "per", "then"); err != nil {
		return Budget{}, err
	}
	var b Budget
	mv, ok := obj["max"]
	if !ok {
		return Budget{}, fmt.Errorf("%s: max is required", orRoot(path))
	}
	if b.Max, err = decodeLimit(mv, fieldPath(path, "max")); err != nil {
		return Budget{}, err
	}
	pv, ok := obj["per"]
	if !ok {
		return Budget{}, fmt.Errorf("%s: per is required", orRoot(path))
	}
	if b.Per, err = decodePer(pv, fieldPath(path, "per")); err != nil {
		return Budget{}, err
	}
	tv, ok := obj["then"]
	if !ok {
		return Budget{}, fmt.Errorf("%s: then is required", orRoot(path))
	}
	if b.Then, err = decodeTarget(tv, fieldPath(path, "then")); err != nil {
		return Budget{}, err
	}
	return b, nil
}

func decodeLimit(v any, path string) (Limit, error) {
	switch t := v.(type) {
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return Limit{}, fmt.Errorf("%s: want an int or a params reference, got %s", orRoot(path), t.String())
		}
		return Limit{Count: int(i)}, nil
	case string:
		return Limit{Ref: t}, nil
	default:
		return Limit{}, fmt.Errorf("%s: want an int or a params reference, got %s", orRoot(path), typeName(v))
	}
}

func decodePer(v any, path string) (Per, error) {
	switch t := v.(type) {
	case string:
		if t == "chain" {
			return Per{Chain: true}, nil
		}
		return Per{Steps: []string{t}}, nil
	case []any:
		steps := make([]string, 0, len(t))
		for i, item := range t {
			s, err := asString(item, fmt.Sprintf("%s[%d]", orRoot(path), i))
			if err != nil {
				return Per{}, err
			}
			steps = append(steps, s)
		}
		return Per{Steps: steps}, nil
	default:
		return Per{}, fmt.Errorf("%s: want a step id, a list of step ids, or chain, got %s", orRoot(path), typeName(v))
	}
}

func decodeFork(v any, path string) (Fork, error) {
	obj, err := objectAt(v, path)
	if err != nil {
		return Fork{}, err
	}
	_, hasEach := obj["each"]
	_, hasChildren := obj["children"]
	switch {
	case hasEach && hasChildren:
		return Fork{}, fmt.Errorf("%s: each and children are mutually exclusive", orRoot(path))
	case hasEach:
		if err := onlyKeys(obj, path, "each", "workflow"); err != nil {
			return Fork{}, err
		}
		each, err := stringAt(obj, "each", path)
		if err != nil {
			return Fork{}, err
		}
		workflow, err := stringAt(obj, "workflow", path)
		if err != nil {
			return Fork{}, err
		}
		return Fork{Each: each, Workflow: workflow}, nil
	case hasChildren:
		if err := onlyKeys(obj, path, "children"); err != nil {
			return Fork{}, err
		}
		children, err := decodeChildren(obj["children"], fieldPath(path, "children"))
		if err != nil {
			return Fork{}, err
		}
		return Fork{Children: children}, nil
	default:
		return Fork{}, fmt.Errorf("%s: want each or children", orRoot(path))
	}
}

func decodeChildren(v any, path string) ([]ForkChild, error) {
	list, err := listAt(v, path)
	if err != nil {
		return nil, err
	}
	children := make([]ForkChild, 0, len(list))
	for i, item := range list {
		childPath := fmt.Sprintf("%s[%d]", orRoot(path), i)
		obj, err := objectAt(item, childPath)
		if err != nil {
			return nil, err
		}
		if err := onlyKeys(obj, childPath, "workflow", "plans", "task"); err != nil {
			return nil, err
		}
		var child ForkChild
		if child.Workflow, err = stringAt(obj, "workflow", childPath); err != nil {
			return nil, err
		}
		if child.Plans, err = stringsAt(obj, "plans", childPath); err != nil {
			return nil, err
		}
		if child.Task, err = stringAt(obj, "task", childPath); err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	return children, nil
}

func objectAt(v any, path string) (map[string]any, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: want an object, got %s", orRoot(path), typeName(v))
	}
	return obj, nil
}

func listAt(v any, path string) ([]any, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: want a list, got %s", orRoot(path), typeName(v))
	}
	return list, nil
}

func asString(v any, path string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: want a string, got %s", orRoot(path), typeName(v))
	}
	return s, nil
}

func optionalString(obj map[string]any, key, path string) (string, bool, error) {
	v, ok := obj[key]
	if !ok {
		return "", false, nil
	}
	s, err := asString(v, fieldPath(path, key))
	return s, true, err
}

func stringAt(obj map[string]any, key, path string) (string, error) {
	s, _, err := optionalString(obj, key, path)
	return s, err
}

func stringsAt(obj map[string]any, key, path string) ([]string, error) {
	v, ok := obj[key]
	if !ok {
		return nil, nil
	}
	list, err := listAt(v, fieldPath(path, key))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		s, err := asString(item, fmt.Sprintf("%s[%d]", fieldPath(path, key), i))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func onlyKeys(obj map[string]any, path string, allowed ...string) error {
	known := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		known[key] = true
	}
	for _, key := range sortedKeys(obj) {
		if !known[key] {
			return fmt.Errorf("%s: unknown field %q", orRoot(path), key)
		}
	}
	return nil
}

// typeName names a decoded value's shape for an error message.
func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case json.Number:
		return "number"
	case []any:
		return "list"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// orRoot names the whole document when a path is empty, so a top-level error
// still says where it was found.
func orRoot(path string) string {
	if path == "" {
		return "workflow"
	}
	return path
}

// fieldPath extends a path with an object field.
func fieldPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

// entryPath extends a path with a map entry key.
func entryPath(base, key string) string {
	return base + "[" + strconv.Quote(key) + "]"
}

// sortedKeys returns a string-keyed map's keys, sorted, so decoding and error
// reporting are deterministic.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
