package transcript

import (
	"bytes"
	"cmp"
	"encoding/json"
)

// LimitResetMS reports the structured reset one raw muse stream line carries:
// a failed run's quota resets_at_ms, else a usage event's allowance window
// resets_at_ms. quota is true for the failed run's own field, false for the
// allowance window, so a caller scanning a whole stream prefers the failure's
// instant over the window's. Zero or absent reads as no reset, and any other
// kind carries none, so only muse lines ever report one. Never errors, never
// panics.
func LimitResetMS(kind string, line []byte) (ms int64, quota bool) {
	if kind != "muse" {
		return 0, false
	}
	trimmed := bytes.TrimSpace(line)
	var obj map[string]any
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		return 0, false
	}
	switch musePayloadType(obj) {
	case "run.terminal.failed":
		p := musePayload(obj)
		if ms := quotaResetMS(p["error"]); ms != 0 {
			return ms, true
		}
		if ms := quotaResetMS(p["reason"]); ms != 0 {
			return ms, true
		}
	case "run.usage":
		window := asMap(asMap(musePayload(obj)["allowance"])["window"])
		return museMillis(window["resets_at_ms"]), false
	}
	return 0, false
}

// quotaResetMS reads resets_at_ms out of a failed run's error or reason
// field, which carries the quota object under "quota". A bare string, a
// missing object and a zero instant all read as no reset.
func quotaResetMS(v any) int64 {
	return museMillis(asMap(asMap(v)["quota"])["resets_at_ms"])
}

// museMillis reads a millisecond instant out of a decoded JSON number.
// Missing, zero and negative read as no reset.
func museMillis(v any) int64 {
	f, _ := v.(float64)
	if f <= 0 {
		return 0
	}
	return int64(f)
}

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
	case "muse":
		return nonEmptyLine(museFailure(obj))
	}
	return nil
}

// TransportLines returns the text of one raw stream line that a transport scan
// may read. Only opencode's error event qualifies, as "<error.type>:
// <error.message>" (just the message when the type is empty), plus a non-JSON
// line that is not a supervisor trailer, because the harness writes some
// transport failures on stderr. Model text and tool output never classify.
func TransportLines(kind string, line []byte) []string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || isTrailerLine(trimmed) {
		return nil
	}
	var obj map[string]any
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		return []string{string(line)}
	}
	if kind != "opencode" || str(obj["type"]) != "error" {
		return nil
	}
	e := asMap(obj["error"])
	msg := str(e["message"])
	if msg == "" {
		return nil
	}
	if t := str(e["type"]); t != "" {
		return []string{t + ": " + msg}
	}
	return []string{msg}
}

// claudeLimitLines is the claude row of the channel table: an error event's
// message, or a result with is_error true -- its result text, else its subtype.
func claudeLimitLines(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "error":
		return nonEmptyLine(cmp.Or(str(obj["message"]), str(asMap(obj["error"])["message"])))
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
