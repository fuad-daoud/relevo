package transcript

// renderOpencode is the table for `opencode run --format json`: a tool_use
// event carries the call and its result in one event.
func renderOpencode(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "step_start", "step_finish":
		return nil
	case "text":
		if t := str(asMap(obj["part"])["text"]); t != "" {
			return []string{t}
		}
		return nil
	case "tool_use":
		part := asMap(obj["part"])
		state := asMap(part["state"])
		call := toolLine(str(part["tool"]), asMap(state["input"]))
		switch str(state["status"]) {
		case "completed":
			return []string{call, okLine(str(state["output"]))}
		case "error":
			return []string{call, errLine(str(state["error"]))}
		default:
			return []string{unknown(obj)}
		}
	case "error":
		return []string{errLine(str(asMap(obj["error"])["message"]))}
	case "reasoning":
		return thinkingLines(str(asMap(obj["part"])["text"]))
	default:
		return []string{unknown(obj)}
	}
}
