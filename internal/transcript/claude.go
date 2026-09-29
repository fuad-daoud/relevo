package transcript

// renderClaude is the table for claude -p --output-format stream-json --verbose:
// an assistant message's blocks render in order; tool results are user events.
func renderClaude(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "assistant":
		var out []string
		for _, blk := range contentBlocks(obj) {
			switch str(blk["type"]) {
			case "tool_use":
				out = append(out, toolLine(str(blk["name"]), asMap(blk["input"])))
			case "text":
				if t := str(blk["text"]); t != "" {
					out = append(out, t)
				}
			case "thinking":
				out = append(out, thinkingLines(str(blk["thinking"]))...)
			}
		}
		return out
	case "user":
		var out []string
		for _, blk := range contentBlocks(obj) {
			if str(blk["type"]) != "tool_result" {
				continue
			}
			if isErr, _ := blk["is_error"].(bool); isErr {
				out = append(out, errLine(resultText(blk["content"])))
			} else {
				out = append(out, okLine(resultText(blk["content"])))
			}
		}
		return out
	case "result":
		var out []string
		for _, d := range asList(obj["permission_denials"]) {
			if name := str(asMap(d)["tool_name"]); name != "" {
				out = append(out, "denied: "+name)
			}
		}
		if isErr, _ := obj["is_error"].(bool); isErr {
			out = append(out, "result: "+str(obj["subtype"]))
		}
		if r := str(obj["result"]); r != "" {
			out = append(out, r)
		}
		return out
	case "error":
		return []string{errLine(firstNonEmpty(str(obj["message"]), str(asMap(obj["error"])["message"])))}
	case "system", "rate_limit_event":
		return nil
	}
	return []string{unknown(obj)}
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
