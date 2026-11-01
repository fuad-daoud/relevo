package transcript

import (
	"encoding/json"
	"strings"
)

// ErrorText reports whether one raw stream line is a fatal error event, returning
// the harness's own trimmed message. It is the probe's view of failure: a probe
// reports the harness's own reason, not the stderr tail, since the real reason
// arrives as a JSON event on stdout. A non-JSON line, an unknown kind, an empty
// message and a non-fatal mid-run warning are all ("", false).
func ErrorText(kind string, line []byte) (string, bool) {
	var obj map[string]any
	if json.Unmarshal(line, &obj) != nil || obj == nil {
		return "", false
	}

	msg := strings.TrimSpace(errorText(kind, obj))
	if msg == "" {
		return "", false
	}
	return msg, true
}

func errorText(kind string, obj map[string]any) string {
	switch kind {
	case "codex":
		// An item.completed of type "error" is a warning the model sees, not a
		// fatal error.
		switch str(obj["type"]) {
		case "error":
			return str(obj["message"])
		case "turn.failed":
			return str(asMap(obj["error"])["message"])
		}

	case "opencode":
		// A tool_use part in an error state is a tool failure the model sees,
		// not a run failure.
		if str(obj["type"]) == "error" {
			return str(asMap(obj["error"])["message"])
		}

	case "claude":
		switch str(obj["type"]) {
		case "result":
			if isErr, _ := obj["is_error"].(bool); isErr {
				if msg := str(obj["result"]); msg != "" {
					return msg
				}
				return "error result"
			}
		case "error":
			if msg := str(obj["message"]); msg != "" {
				return msg
			}
			return str(asMap(obj["error"])["message"])
		}

	case "muse":
		return museFailure(obj)

	case "agy":
		return agyErrorText(obj)
	}
	return ""
}

// agyErrorText reads agy's result event: a non-SUCCESS status is fatal, its error
// a string or an object.
func agyErrorText(obj map[string]any) string {
	if str(obj["event"]) != "result" {
		return ""
	}
	r := asMap(obj["result"])
	st := str(r["status"])
	if st == "" || st == "SUCCESS" {
		return ""
	}
	var msg string
	if e, ok := r["error"].(string); ok {
		msg = e
	} else {
		msg = str(asMap(r["error"])["message"])
	}
	if msg == "" {
		msg = "result status " + st
	}
	return msg
}
