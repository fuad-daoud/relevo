package harness

import "strings"

// frontmatterModel returns the value of a `model:` key in the leading `---`
// fenced block, or "". It is deliberately shallow: a malformed file yields ""
// rather than an error, and a parse failure must never mask that the file exists.
func frontmatterModel(raw []byte) string {
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	// A `model:` line inside frontmatter that never terminates is not a pin.
	end := -1
	for i, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return ""
	}
	for _, line := range lines[1 : end+1] {
		rest, ok := strings.CutPrefix(line, "model:")
		if !ok {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

// PinnedModel returns the model pin in an installed definition's raw bytes,
// dispatching on the kind's shipped format: TOML or Markdown frontmatter.
func PinnedModel(kind string, raw []byte) string {
	if h, ok := Lookup(kind); ok && h.DocExt == "toml" {
		return tomlTopLevelModel(raw)
	}
	return frontmatterModel(raw)
}

// tomlTopLevelModel returns the value of a top-level `model = "..."` key, or
// "". The first `[table]` header ends the top level, so a model key inside a
// table is not a pin; `model_reasoning_effort` does not match because after
// "model" the remainder does not begin with "=".
func tomlTopLevelModel(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			return ""
		}
		rest, ok := strings.CutPrefix(line, "model")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		rest, ok = strings.CutPrefix(rest, "=")
		if !ok {
			continue
		}
		v := strings.TrimSpace(rest)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			return v[1 : len(v)-1]
		}
	}
	return ""
}
