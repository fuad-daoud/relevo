package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Change is one difference between two config documents, or a secret change.
type Change struct {
	Path   string          `json:"path"`
	Op     string          `json:"op"`               // "add" | "remove" | "change" | "set" (secrets only)
	Before json.RawMessage `json:"before,omitempty"` // absent for add/set
	After  json.RawMessage `json:"after,omitempty"`  // absent for remove/set
}

type Doc = map[Section]json.RawMessage

const maxValue = 60

// DiffDocs returns the changes that turn a into b, in a deterministic order:
// sections in Sections order, object keys sorted, array indices ascending.
func DiffDocs(a, b Doc) []Change {
	changes := []Change{}
	for _, sec := range Sections {
		ab, aok := a[sec]
		bb, bok := b[sec]
		switch {
		case aok && !bok:
			changes = append(changes, Change{Path: string(sec), Op: "remove", Before: compactCopy(ab)})
		case !aok && bok:
			changes = append(changes, Change{Path: string(sec), Op: "add", After: compactCopy(bb)})
		case aok && bok:
			changes = append(changes, diffRaw(string(sec), ab, bb)...)
		}
	}
	return changes
}

// Describe renders one change as the one line config log prints.
func Describe(c Change) string {
	switch c.Op {
	case "change":
		return fmt.Sprintf("~ %s  %s → %s", c.Path, cutValue(string(c.Before)), cutValue(string(c.After)))
	case "add":
		return fmt.Sprintf("+ %s  %s", c.Path, cutValue(string(c.After)))
	case "remove":
		return fmt.Sprintf("- %s  %s", c.Path, cutValue(string(c.Before)))
	case "set":
		return fmt.Sprintf("* %s", c.Path)
	default:
		return fmt.Sprintf("%s %s", c.Op, c.Path)
	}
}

// EncodeDoc renders doc as an indented JSON object with one key per section in
// Sections order, assembled by hand because a map's keys sort alphabetically.
func EncodeDoc(doc Doc) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, sec := range Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, body); err != nil {
			return nil, fmt.Errorf("%s: %w", sec, err)
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		key, err := json.Marshal(string(sec))
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(compact.Bytes())
	}
	buf.WriteByte('}')

	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// diffRaw diffs one section body by decoding both sides with UseNumber and
// walking them; a side that does not decode is compared compactly as bytes.
func diffRaw(path string, a, b json.RawMessage) []Change {
	out := []Change{}
	av, aok := decodeValue(a)
	bv, bok := decodeValue(b)
	if !aok || !bok {
		ca, cb := compactCopy(a), compactCopy(b)
		if !bytes.Equal(ca, cb) {
			out = append(out, Change{Path: path, Op: "change", Before: ca, After: cb})
		}
		return out
	}
	diffValue(path, av, bv, &out)
	return out
}

func diffValue(path string, a, b any, out *[]Change) {
	aobj, aIsObj := a.(map[string]any)
	bobj, bIsObj := b.(map[string]any)
	if aIsObj && bIsObj {
		keys := make(map[string]bool, len(aobj)+len(bobj))
		for k := range aobj {
			keys[k] = true
		}
		for k := range bobj {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)

		for _, k := range sorted {
			av, aok := aobj[k]
			bv, bok := bobj[k]
			child := joinPath(path, k)
			switch {
			case aok && !bok:
				*out = append(*out, Change{Path: child, Op: "remove", Before: mustCompact(av)})
			case !aok && bok:
				*out = append(*out, Change{Path: child, Op: "add", After: mustCompact(bv)})
			default:
				diffValue(child, av, bv, out)
			}
		}
		return
	}

	aarr, aIsArr := a.([]any)
	barr, bIsArr := b.([]any)
	if aIsArr && bIsArr {
		shorter := len(aarr)
		if len(barr) < shorter {
			shorter = len(barr)
		}
		for i := 0; i < shorter; i++ {
			diffValue(fmt.Sprintf("%s[%d]", path, i), aarr[i], barr[i], out)
		}
		for i := shorter; i < len(aarr); i++ {
			*out = append(*out, Change{Path: fmt.Sprintf("%s[%d]", path, i), Op: "remove", Before: mustCompact(aarr[i])})
		}
		for i := shorter; i < len(barr); i++ {
			*out = append(*out, Change{Path: fmt.Sprintf("%s[%d]", path, i), Op: "add", After: mustCompact(barr[i])})
		}
		return
	}

	// Scalars, or values of different kinds.
	ca, cb := mustCompact(a), mustCompact(b)
	if !bytes.Equal(ca, cb) {
		*out = append(*out, Change{Path: path, Op: "change", Before: ca, After: cb})
	}
}

// decodeValue decodes one JSON value, keeping numbers as their literal text so
// 2 and 2.0 are not conflated.
func decodeValue(raw json.RawMessage) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func mustCompact(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return compactCopy(b)
}

// compactCopy strips insignificant whitespace, or copies raw when invalid.
func compactCopy(raw json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return append(json.RawMessage(nil), buf.Bytes()...)
}

// joinPath appends an object key, quoting a key that is not a bare identifier
// so a dot in it cannot be mistaken for a nested path.
func joinPath(path, key string) string {
	if isIdent(key) {
		return path + "." + key
	}
	quoted, err := json.Marshal(key)
	if err != nil {
		quoted = []byte(`"?"`)
	}
	return path + "[" + string(quoted) + "]"
}

func isIdent(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func cutValue(s string) string {
	runes := []rune(s)
	if len(runes) <= maxValue {
		return s
	}
	return string(runes[:maxValue-1]) + "…"
}
