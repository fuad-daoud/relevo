package transcript

import "time"

// claude is the table for claude -p --output-format stream-json --verbose: an
// assistant message's blocks render in order; tool results are user events.
// Every element carries its event's clock; a tool result also carries the span
// from the tool_use event that made the call, when this pass saw that call.
func (r *Renderer) claude(obj map[string]any) []string {
	at := claudeAt(obj)
	s := r.at(at)
	switch str(obj["type"]) {
	case "assistant":
		return r.claudeAssistant(obj, at, s)
	case "user":
		return r.claudeUser(obj, at)
	case "result":
		return claudeResult(obj, s)
	case "error":
		return []string{s.line(errLine(firstNonEmpty(str(obj["message"]), str(asMap(obj["error"])["message"]))))}
	case "system", "rate_limit_event":
		return nil
	}
	return []string{s.line(unknown(obj))}
}

// claudeAssistant renders an assistant message's blocks in order: each tool_use
// block registers its id for the result that answers it; text and thinking
// blocks render as the stream renders them.
func (r *Renderer) claudeAssistant(obj map[string]any, at time.Time, s stamp) []string {
	var out []string
	for _, blk := range contentBlocks(obj) {
		switch str(blk["type"]) {
		case "tool_use":
			if id := str(blk["id"]); id != "" && !at.IsZero() {
				r.pending[id] = at
			}
			out = append(out, s.line(toolLine(str(blk["name"]), asMap(blk["input"]))))
		case "text":
			if t := str(blk["text"]); t != "" {
				out = append(out, s.line(t))
			}
		case "thinking":
			out = append(out, s.thinking(str(blk["thinking"]))...)
		}
	}
	return out
}

// claudeUser renders a user event's tool_result blocks, each consuming the call
// this pass recorded under its id.
func (r *Renderer) claudeUser(obj map[string]any, at time.Time) []string {
	var out []string
	for _, blk := range contentBlocks(obj) {
		if str(blk["type"]) != "tool_result" {
			continue
		}
		line := okLine(resultText(blk["content"]))
		if isErr, _ := blk["is_error"].(bool); isErr {
			line = errLine(resultText(blk["content"]))
		}
		out = append(out, r.callSpan(str(blk["tool_use_id"]), at).line(line))
	}
	return out
}

// claudeResult renders the final result event: denied permissions, a failure
// subtype, then the result text.
func claudeResult(obj map[string]any, s stamp) []string {
	var out []string
	for _, d := range asList(obj["permission_denials"]) {
		if name := str(asMap(d)["tool_name"]); name != "" {
			out = append(out, s.line("denied: "+name))
		}
	}
	if isErr, _ := obj["is_error"].(bool); isErr {
		out = append(out, s.line("result: "+str(obj["subtype"])))
	}
	if res := str(obj["result"]); res != "" {
		out = append(out, s.line(res))
	}
	return out
}

func contentBlocks(obj map[string]any) []map[string]any {
	var out []map[string]any
	for _, b := range asList(asMap(obj["message"])["content"]) {
		if m := asMap(b); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func resultText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	for _, b := range asList(v) {
		if t := str(asMap(b)["text"]); t != "" {
			return t
		}
	}
	return ""
}
