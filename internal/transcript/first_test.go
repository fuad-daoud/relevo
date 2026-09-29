package transcript

import "testing"

func TestFirstOutput(t *testing.T) {
	tests := []struct {
		name string
		kind string
		line string
		want bool
	}{
		{"claude assistant", "claude", `{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`, true},
		{"claude system init", "claude", `{"type":"system","subtype":"init"}`, false},
		{"claude result", "claude", `{"type":"result","subtype":"success"}`, false},

		{"opencode text", "opencode", `{"type":"text","part":{"text":"ok"}}`, true},
		{"opencode reasoning", "opencode", `{"type":"reasoning","part":{"text":"thinking"}}`, true},
		{"opencode tool_use", "opencode", `{"type":"tool_use","part":{"tool":"bash","state":{"status":"completed","output":"ok"}}}`, true},
		{"opencode step_start", "opencode", `{"type":"step_start","part":{}}`, false},
		{"opencode step_finish", "opencode", `{"type":"step_finish","part":{}}`, false},

		{"agy agent_response", "agy", `{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE"}}`, true},
		{"agy result", "agy", `{"event":"result","result":{"status":"SUCCESS"}}`, true},
		{"agy init", "agy", `{"event":"init","session_id":"abc"}`, false},
		{"agy tool step", "agy", `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE"}}`, false},

		{"codex item.started agent_message", "codex", `{"type":"item.started","item":{"type":"agent_message"}}`, true},
		{"codex item.completed agent_message", "codex", `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`, true},
		{"codex item.completed reasoning", "codex", `{"type":"item.completed","item":{"type":"reasoning"}}`, true},
		{"codex item.completed command_execution", "codex", `{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}`, true},
		{"codex item.completed file_change", "codex", `{"type":"item.completed","item":{"type":"file_change","changes":[]}}`, true},
		{"codex item.completed error", "codex", `{"type":"item.completed","item":{"type":"error","message":"boom"}}`, false},
		{"codex thread.started", "codex", `{"type":"thread.started","thread_id":"t"}`, false},
		{"codex turn.started", "codex", `{"type":"turn.started"}`, false},

		{"non-JSON claude", "claude", `relevo-exit:0`, false},
		{"non-JSON opencode", "opencode", `relevo-exit:0`, false},
		{"non-JSON agy", "agy", `relevo-exit:0`, false},
		{"non-JSON codex", "codex", `relevo-exit:0`, false},
		{"unknown kind", "nope", `{"type":"assistant","event":"result"}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstOutput(tt.kind, []byte(tt.line)); got != tt.want {
				t.Errorf("FirstOutput(%q, %s) = %v, want %v", tt.kind, tt.line, got, tt.want)
			}
		})
	}
}
