package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

// RenderRecord turns one line of a harness's own session record into the lines
// to append to the log. It differs from Render in what "unknown" means: a
// record file is a superset of the stream with housekeeping records, so an
// unknown type, a non-JSON line, or a kind with no record table renders as
// nothing. Never errors, never panics.
func RenderRecord(kind string, line []byte) []string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	var obj map[string]any
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		return nil
	}
	if kind != "claude" {
		return nil
	}
	return renderClaudeRecord(obj)
}

// renderClaudeRecord is RenderRecord's table for kind "claude": an assistant,
// or a user record carrying a tool_result, renders as the stream renders it; a
// typed prompt renders as "> " plus its first line; every housekeeping record
// type and anything else renders as nothing.
func renderClaudeRecord(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "assistant":
		return renderClaude(obj)
	case "user":
		if hasToolResult(obj) {
			return renderClaude(obj)
		}
		return renderClaudePrompt(obj)
	}
	return nil
}

func hasToolResult(obj map[string]any) bool {
	for _, blk := range contentBlocks(obj) {
		if str(blk["type"]) == "tool_result" {
			return true
		}
	}
	return false
}

// renderClaudePrompt renders a typed prompt as "> " plus the first non-empty
// line of its text, truncated to maxArg runes with "…"; any other shape
// renders as nothing.
func renderClaudePrompt(obj map[string]any) []string {
	text, ok := claudePromptText(obj)
	if !ok {
		return nil
	}
	line := firstNonEmptyLine(text)
	if line == "" {
		return nil
	}
	return []string{"> " + truncateRunes(line, maxArg)}
}

func claudePromptText(obj map[string]any) (string, bool) {
	content := asMap(obj["message"])["content"]
	if s, ok := content.(string); ok {
		return s, true
	}
	list := asList(content)
	if list == nil {
		return "", false
	}
	var parts []string
	for _, b := range list {
		blk := asMap(b)
		if str(blk["type"]) != "text" {
			return "", false
		}
		parts = append(parts, str(blk["text"]))
	}
	return strings.Join(parts, "\n"), true
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
