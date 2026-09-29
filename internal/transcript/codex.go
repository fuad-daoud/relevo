package transcript

import (
	"fmt"
	"sort"
)

// renderCodex is the table for `codex exec --json`: a spawned [agents.*] role
// is a thread inside the same process, reported as collab_tool_call items.
func renderCodex(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "thread.started", "turn.started", "turn.completed", "item.started":
		return nil
	case "turn.failed":
		return []string{errLine(str(asMap(obj["error"])["message"]))}
	case "error":
		return []string{errLine(str(obj["message"]))}
	case "item.completed":
		return renderCodexItem(asMap(obj["item"]))
	}
	return []string{unknown(obj)}
}

func renderCodexItem(item map[string]any) []string {
	switch str(item["type"]) {
	case "agent_message":
		if text := str(item["text"]); text != "" {
			return []string{text}
		}
		return nil
	case "reasoning":
		return thinkingLines(str(item["text"]))
	case "error":
		return []string{errLine(str(item["message"]))}
	case "command_execution":
		return renderCodexCommand(item)
	case "file_change":
		return renderCodexFileChange(item)
	case "collab_tool_call":
		return renderCodexCollab(item)
	}
	return []string{unknown(item)}
}

func renderCodexCommand(item map[string]any) []string {
	call := toolLine("bash", map[string]any{"command": item["command"]})
	out := str(item["aggregated_output"])
	code, _ := item["exit_code"].(float64)
	if code == 0 {
		return []string{call, okLine(out)}
	}
	return []string{call, errLine(fmt.Sprintf("exit %d: %s", int(code), out))}
}

// renderCodexFileChange names one edit line per changed file; a change with no
// path list degrades to its type rather than vanishing.
func renderCodexFileChange(item map[string]any) []string {
	changes := asList(item["changes"])
	if len(changes) == 0 {
		return []string{"[file_change]"}
	}
	var lines []string
	for _, c := range changes {
		cm := asMap(c)
		if cm == nil {
			continue
		}
		lines = append(lines, toolLine("edit", map[string]any{"path": cm["path"]}))
	}
	return lines
}

func renderCodexCollab(item map[string]any) []string {
	switch tool := str(item["tool"]); tool {
	case "spawn_agent":
		return []string{toolLine("spawn_agent", map[string]any{"prompt": item["prompt"]})}
	case "wait":
		var lines []string
		states := asMap(item["agents_states"])
		keys := make([]string, 0, len(states))
		for k := range states {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			st := asMap(states[k])
			if str(st["status"]) == "completed" {
				lines = append(lines, okLine(str(st["message"])))
			}
		}
		if len(lines) == 0 {
			return []string{"[collab_tool_call wait]"}
		}
		return lines
	default:
		return []string{"[collab_tool_call " + tool + "]"}
	}
}
