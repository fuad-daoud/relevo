package transcript

// opencode is the table for `opencode run --format json`: a tool_use event
// carries the call and its result in one event. Both lines carry the event's
// clock; the call line also carries the tool's own measured span.
func (r *Renderer) opencode(obj map[string]any) []string {
	s := r.at(opencodeAt(obj))
	switch str(obj["type"]) {
	case "step_start", "step_finish":
		return nil
	case "text":
		if t := str(asMap(obj["part"])["text"]); t != "" {
			return []string{s.line(t)}
		}
		return nil
	case "tool_use":
		part := asMap(obj["part"])
		state := asMap(part["state"])
		call := s
		call.dur = r.timeSpan(state["time"])
		line := call.line(toolLine(str(part["tool"]), asMap(state["input"])))
		switch str(state["status"]) {
		case "completed":
			return []string{line, s.line(okLine(str(state["output"])))}
		case "error":
			return []string{line, s.line(errLine(str(state["error"])))}
		default:
			return []string{s.line(unknown(obj))}
		}
	case "error":
		return []string{s.line(errLine(str(asMap(obj["error"])["message"])))}
	case "reasoning":
		return s.thinking(str(asMap(obj["part"])["text"]))
	default:
		return []string{s.line(unknown(obj))}
	}
}
