package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
)

// FinalText returns the last assistant text a harness stream carries -- the
// message a one-shot process leaves behind that reads as its answer -- trimmed,
// or "" when the stream carries none. Per kind: claude's last assistant text
// blocks, falling back to the result string; opencode's last text event; agy's
// result.response, falling back to the last step_update text; codex's last
// agent_message. A non-JSON line and the relevo-exit trailer are ignored.
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
// order: per JSON line, the same finalCandidates values, the last preferred
// over the fallback when it is non-empty, skipping non-JSON lines and empty
// texts. It lets a caller rescan a stream for something earlier than the last
// message, where FinalText stops at the last one.
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

// LastWithBlock returns the last text in the stream that carries a fenced
// `relevo` block: the message a chain's verdict or finding-count parse reads,
// which a reader that recaps afterwards would otherwise hide from the round's
// saved artifact. "" when no message carries one.
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

// claudeTextBlocks is an assistant event's message.content text blocks, in
// order. It is final.go's own reader of the shape renderClaude walks, so a
// change to the renderer does not silently change what FinalText returns.
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
