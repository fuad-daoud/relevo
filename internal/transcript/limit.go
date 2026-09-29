package transcript

import (
	"bytes"
	"encoding/json"
)

// LimitLines returns the text of one raw stream line that a limit scan may
// read: a line the harness itself wrote. The channel each kind keeps is the
// harness's own report of a failure -- claude's error event and a failed
// result, agy's failed result, opencode's error event, codex's error and
// turn.failed events -- plus a non-JSON line that is not a supervisor trailer
// (the harness's stderr). Everything a model or a tool produced yields nothing:
// assistant text, tool output, a successful result's text, a successful
// step's output, thinking and housekeeping. A limit-shaped sentence there is
// the model talking about a limit, not hitting one.
//
// A line that is limit evidence is returned as its own text, and the reset time
// is parsed from that same text, so a reset phrase on another line never
// supplies an Until. Empty text yields nothing. Never errors, never panics.
func LimitLines(kind string, line []byte) []string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if isTrailerLine(trimmed) {
		return nil
	}
	var obj map[string]any
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		// A non-JSON line that is not a supervisor trailer is the harness's
		// own stderr, so it is limit evidence.
		return []string{string(line)}
	}
	switch kind {
	case "claude":
		return claudeLimitLines(obj)
	case "agy":
		return agyLimitLines(obj)
	case "opencode":
		return opencodeLimitLines(obj)
	case "codex":
		return codexLimitLines(obj)
	}
	return nil
}

// claudeLimitLines is the claude row of the channel table: an error event's
// message, or a result with is_error true -- its result text, else its subtype.
func claudeLimitLines(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "error":
		return nonEmptyLine(firstNonEmpty(str(obj["message"]), str(asMap(obj["error"])["message"])))
	case "result":
		if isErr, _ := obj["is_error"].(bool); isErr {
			if r := str(obj["result"]); r != "" {
				return []string{r}
			}
			return nonEmptyLine(str(obj["subtype"]))
		}
	}
	return nil
}

// agyLimitLines is the agy row: a result with a status set and not SUCCESS,
// taking its error field.
func agyLimitLines(obj map[string]any) []string {
	if str(obj["event"]) != "result" {
		return nil
	}
	r := asMap(obj["result"])
	if st := str(r["status"]); st == "" || st == "SUCCESS" {
		return nil
	}
	return nonEmptyLine(limitErrorText(r["error"]))
}

// opencodeLimitLines is the opencode row: an error event's message.
func opencodeLimitLines(obj map[string]any) []string {
	if str(obj["type"]) != "error" {
		return nil
	}
	return nonEmptyLine(str(asMap(obj["error"])["message"]))
}

// codexLimitLines is the codex row: an error event's message, or a turn.failed
// event's error message.
func codexLimitLines(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "error":
		return nonEmptyLine(str(obj["message"]))
	case "turn.failed":
		return nonEmptyLine(str(asMap(obj["error"])["message"]))
	}
	return nil
}

// limitErrorText reads an error field that may be a bare string or an object
// carrying its text under "message".
func limitErrorText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return str(asMap(v)["message"])
}

// nonEmptyLine wraps one evidence line, or nothing when there is no text.
func nonEmptyLine(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
