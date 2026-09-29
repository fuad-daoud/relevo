package transcript

import (
	"bytes"
	"encoding/json"
)

// SessionID is the harness's own session id as one line of its stream announces
// it, "" when the line announces none. Each harness names it differently and not
// always on the first line, so the caller feeds the lines in order and takes the
// first that answers: claude's session_id, opencode's sessionID, agy's
// conversation_id (top-level or nested in the event's own object), and codex's
// thread_id on thread.started only.
func SessionID(kind string, line []byte) string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return ""
	}
	switch kind {
	case "claude":
		return str(obj["session_id"])
	case "opencode":
		return str(obj["sessionID"])
	case "agy":
		if id := str(obj["conversation_id"]); id != "" {
			return id
		}
		if ev := str(obj["event"]); ev != "" {
			return str(asMap(obj[ev])["conversation_id"])
		}
		return ""
	case "codex":
		if str(obj["type"]) == "thread.started" {
			return str(obj["thread_id"])
		}
		return ""
	}
	return ""
}
