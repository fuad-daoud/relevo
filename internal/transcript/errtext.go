package transcript

import (
	"encoding/json"
	"strings"
)

// ErrorText reports whether one raw line of a harness's stream is a fatal error
// event, and returns the harness's own message, trimmed of surrounding
// whitespace. It is the probe's view of failure: a probe reports the harness's
// own reason instead of the stderr tail, because the real reason (a usage
// limit, say) arrives as a JSON event on stdout. A non-JSON line, an unknown
// kind, an empty message, and a non-fatal mid-run warning are all ("", false).
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
		// item.completed of an item type "error" is a warning the model
		// sees (e.g. "Exceeded skills context budget"), not a fatal error.
		switch str(obj["type"]) {
		case "error":
			return str(obj["message"])
		case "turn.failed":
			return str(asMap(obj["error"])["message"])
		}

	case "opencode":
		// A tool_use part in an error state is a tool failure the model
		// sees, not a run failure.
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

	case "agy":
		return agyErrorText(obj)
	}
	return ""
}

// agyErrorText reads agy's result event: a status other than SUCCESS is fatal,
// and its error is a string or an object.
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
