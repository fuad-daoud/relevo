package transcript

import "testing"

func TestFinalText(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		stream func(t *testing.T) []byte
		want   string
	}{
		{"claude fixture's last assistant text", "claude", fixture("claude.jsonl"), "done"},
		{"opencode fixture's last text event", "opencode", fixture("opencode.jsonl"), "done"},
		{"codex fixture's last agent_message", "codex", fixture("codex.jsonl"), "LUNA-RESEARCHER PONG"},
		{"agy fixture carries no text", "agy", fixture("agy.jsonl"), ""},
		{
			"agy result.response",
			"agy",
			raw(`{"event":"result","result":{"status":"SUCCESS","response":"AGY FINDINGS","denied_actions":[]}}` + "\n"),
			"AGY FINDINGS",
		},
		{
			"agy empty response falls back to the last step_update text",
			"agy",
			raw(`{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":[]}}` + "\n" +
				`{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE","text":"STEP FINDINGS"}}` + "\n"),
			"STEP FINDINGS",
		},
		{
			"claude falls back to the result string with no assistant text",
			"claude",
			raw(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"","signature":"sig"}]}}` + "\n" +
				`{"type":"result","subtype":"success","is_error":false,"result":"RESULT STRING"}` + "\n"),
			"RESULT STRING",
		},
		{"claude trailer-only stream is empty", "claude", raw("relevo-exit:0\n"), ""},
		{"opencode trailer-only stream is empty", "opencode", raw("relevo-exit:0\n"), ""},
		{"agy trailer-only stream is empty", "agy", raw("relevo-exit:0\n"), ""},
		{"codex trailer-only stream is empty", "codex", raw("relevo-exit:0\n"), ""},
		{
			"opencode trims text and ignores a non-JSON line",
			"opencode",
			raw("not json at all\n" + `{"type":"text","part":{"text":"  PADDED  "}}` + "\n"),
			"PADDED",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FinalText(tc.kind, tc.stream(t)); got != tc.want {
				t.Errorf("FinalText(%q) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}
