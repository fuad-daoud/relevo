package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

// FinalText returns the last assistant text a harness stream carries, trimmed,
// "" when it carries none: claude's last assistant text blocks, falling back to
// the result string; opencode's last text event; agy's result.response, falling
// back to the last step_update text; codex's last agent_message; muse's terminal
// text. Non-JSON lines and the relevo-exit trailer are ignored.
func FinalText(kind string, stream []byte) string {
	var last, fallback string

	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
			continue
		}
		l, f := finalCandidates(kind, obj)
		if l != "" {
			last = l
		}
		if f != "" {
			fallback = f
		}
	}

	if last == "" {
		last = fallback
	}
	return strings.TrimSpace(last)
}

// Texts returns every message text FinalText's decoding recognises, in stream
// order, the last preferred over the fallback when non-empty. It lets a caller
// rescan a stream for something earlier than FinalText's last message.
func Texts(kind string, stream []byte) []string {
	var out []string
	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
			continue
		}
		l, f := finalCandidates(kind, obj)
		if l == "" {
			l = f
		}
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// LastWithBlock returns the last stream text carrying a fenced `relevo` block --
// the message a chain's verdict or finding-count parse reads, which a recapping
// reader would otherwise hide from the round's saved artifact. "" when none does.
func LastWithBlock(kind string, stream []byte) string {
	texts := Texts(kind, stream)
	for i := len(texts) - 1; i >= 0; i-- {
		if strings.Contains(texts[i], "```relevo") {
			return texts[i]
		}
	}
	return ""
}

func finalCandidates(kind string, obj map[string]any) (last, fallback string) {
	switch kind {
	case "claude":
		switch str(obj["type"]) {
		case "assistant":
			return strings.Join(claudeTextBlocks(obj), ""), ""
		case "result":
			return "", str(obj["result"])
		}
	case "opencode":
		if str(obj["type"]) == "text" {
			return str(asMap(obj["part"])["text"]), ""
		}
	case "agy":
		switch str(obj["event"]) {
		case "result":
			return str(asMap(obj["result"])["response"]), ""
		case "step_update":
			return "", str(asMap(obj["step_update"])["text"])
		}
	case "muse":
		return museFinal(obj), ""
	case "codex":
		if str(obj["type"]) == "item.completed" {
			item := asMap(obj["item"])
			if str(item["type"]) == "agent_message" {
				return str(item["text"]), ""
			}
		}
	}
	return "", ""
}

// claudeTextBlocks is an assistant event's message.content text blocks, in order.
// It reads the renderer's shape on its own, so a renderer change does not
// silently change what FinalText returns.
func claudeTextBlocks(obj map[string]any) []string {
	var out []string
	for _, b := range asList(asMap(obj["message"])["content"]) {
		blk := asMap(b)
		if str(blk["type"]) != "text" {
			continue
		}
		if t := str(blk["text"]); t != "" {
			out = append(out, t)
		}
	}
	return out
}
