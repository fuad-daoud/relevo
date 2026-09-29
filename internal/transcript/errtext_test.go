package transcript

import "testing"

func TestErrorText(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		line   string
		want   string
		wantOK bool
	}{
		{"codex error", "codex", `{"type":"error","message":"You've hit your usage limit."}`, "You've hit your usage limit.", true},
		{"codex turn.failed", "codex", `{"type":"turn.failed","error":{"message":"quota"}}`, "quota", true},
		{"codex item.completed error", "codex", `{"type":"item.completed","item":{"type":"error","message":"Exceeded skills context budget"}}`, "", false},

		{"opencode error", "opencode", `{"type":"error","error":{"message":"opencode gave up"}}`, "opencode gave up", true},
		{"opencode tool_use error", "opencode", `{"type":"tool_use","part":{"tool":"bash","state":{"status":"error","error":"exit 1"}}}`, "", false},

		{"claude result is_error", "claude", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`, "boom", true},
		{"claude result ok", "claude", `{"type":"result","subtype":"success","is_error":false,"result":""}`, "", false},

		{"agy result error object", "agy", `{"event":"result","result":{"status":"ERROR","error":{"message":"quota"}}}`, "quota", true},
		{"agy result success", "agy", `{"event":"result","result":{"status":"SUCCESS","response":"ok"}}`, "", false},

		{"non-JSON claude", "claude", `relevo-exit:1`, "", false},
		{"non-JSON opencode", "opencode", `relevo-exit:1`, "", false},
		{"non-JSON agy", "agy", `relevo-exit:1`, "", false},
		{"non-JSON codex", "codex", `relevo-exit:1`, "", false},
		{"unknown kind", "nope", `{"type":"error","message":"boom"}`, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ErrorText(tt.kind, []byte(tt.line))
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ErrorText(%q, %s) = (%q, %v), want (%q, %v)", tt.kind, tt.line, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
