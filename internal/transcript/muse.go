package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Every muse line is an envelope naming the event in payload_type and carrying it
// in payload. The session id rides on the envelope's stream, not the payload.

func musePayloadType(obj map[string]any) string { return str(obj["payload_type"]) }

func musePayload(obj map[string]any) map[string]any { return asMap(obj["payload"]) }

func museIsTerminal(obj map[string]any) bool {
	return strings.HasPrefix(musePayloadType(obj), "run.terminal.")
}

func museSessionID(obj map[string]any) string {
	stream := asMap(obj["stream"])
	if str(stream["kind"]) != "session" {
		return ""
	}
	return str(stream["id"])
}

// museFailure is the message of a failed run: the error object's message, else a
// reason, else the terminal text, else the payload itself, so a limit pattern
// still sees the quota fields.
func museFailure(obj map[string]any) string {
	if musePayloadType(obj) != "run.terminal.failed" {
		return ""
	}
	p := musePayload(obj)
	if msg := limitErrorText(p["error"]); msg != "" {
		return msg
	}
	if msg := limitErrorText(p["reason"]); msg != "" {
		return msg
	}
	if msg := str(p["text"]); msg != "" {
		return msg
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(raw)
}

// museFirstOutput is true for the first event only the model can have caused --
// streamed text or a tool result; the lifecycle events before them are the
// harness's own.
func museFirstOutput(obj map[string]any) bool {
	switch musePayloadType(obj) {
	case "run.output.delta", "tool.result":
		return true
	}
	return false
}

// museFinal is the terminal's own answer, of which the streamed deltas are chunks.
func museFinal(obj map[string]any) string {
	if museIsTerminal(obj) {
		return str(musePayload(obj)["text"])
	}
	return ""
}

// muse renders `muse exec --json`. Streamed deltas are fragments of words, so
// they buffer into one block written before the next non-delta event; a stream
// cut first keeps its tail unwritten.
func (r *Renderer) muse(obj map[string]any) []string {
	t := musePayloadType(obj)
	p := musePayload(obj)
	if t == "run.output.delta" {
		r.museText.WriteString(str(p["text"]))
		return nil
	}
	out := r.museFlush()
	switch {
	case t == "tool.result":
		return append(out, museToolLines(p)...)
	case t == "run.usage":
		return append(out, museUsageLine(p))
	case t == "run.terminal.failed":
		return append(out, errLine(museFailure(obj)))
	case museIsTerminal(obj):
		return append(out, "["+strings.TrimPrefix(t, "run.")+"]")
	case museQuiet(t):
		return out
	}
	return append(out, "["+t+"]")
}

func (r *Renderer) museFlush() []string {
	text := strings.TrimSpace(r.museText.String())
	r.museText.Reset()
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// museQuiet marks the bookkeeping events no reader needs.
func museQuiet(t string) bool {
	switch t {
	case "runtime.command.accepted", "session.run.linked", "run.model.configured",
		"turn.input.user", "run.lifecycle.started", "todo.snapshot.updated":
		return true
	}
	return strings.HasPrefix(t, "task.")
}

func museToolLines(p map[string]any) []string {
	facts := asMap(p["correlation_facts"])
	name := str(facts["tool_name"])
	if name == "" {
		name = "tool"
	}
	text := str(p["text"])
	ok := str(facts["outcome"]) == "success"
	head := "● " + name
	// The bash tool reports a JSON object, whose command is what a reader wants.
	var bash struct {
		Command  *string `json:"command"`
		ExitCode *int    `json:"exit_code"`
		Output   string  `json:"output"`
	}
	if json.Unmarshal([]byte(text), &bash) == nil && bash.Command != nil && bash.ExitCode != nil {
		head = "$ " + oneLine(*bash.Command)
		text = fmt.Sprintf("exit %d", *bash.ExitCode)
		if first := museFirstLine(bash.Output); first != "" {
			text += ": " + first
		}
	}
	if ok {
		return []string{head, okLine(text)}
	}
	return []string{head, errLine(text)}
}

func museFirstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func museUsageLine(p map[string]any) string {
	var in, cached, out, reasoning int64
	for _, m := range asList(p["models"]) {
		mm := asMap(m)
		in += museInt(mm["input_tokens"])
		cached += museInt(mm["cached_tokens"])
		out += museInt(mm["output_tokens"])
		reasoning += museInt(mm["reasoning_tokens"])
	}
	line := fmt.Sprintf("[usage] in %d  cache %d  out %d  reasoning %d", in-cached, cached, out, reasoning)
	if a := asMap(p["allowance"]); a != nil {
		line += fmt.Sprintf("  window %d%%  weekly %d%%",
			museInt(asMap(a["window"])["used_percent"]), museInt(asMap(a["weekly"])["used_percent"]))
	}
	return line
}

func museInt(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}
