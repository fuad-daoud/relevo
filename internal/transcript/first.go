package transcript

import "encoding/json"

func FirstOutput(kind string, line []byte) bool {
	var obj map[string]any
	if json.Unmarshal(line, &obj) != nil || obj == nil {
		return false
	}

	switch kind {
	case "claude":
		return str(obj["type"]) == "assistant"
	case "opencode":
		switch str(obj["type"]) {
		case "text", "reasoning", "tool_use":
			return true
		}
		return false
	case "agy":
		switch str(obj["event"]) {
		case "result":
			return true
		case "step_update":
			return str(asMap(obj["step_update"])["step_type"]) == "agent_response"
		}
		return false
	case "codex":
		switch str(obj["type"]) {
		case "item.started", "item.completed":
			switch str(asMap(obj["item"])["type"]) {
			case "agent_message", "reasoning", "command_execution", "file_change":
				return true
			}
		}
		return false
	}

	return false
}
