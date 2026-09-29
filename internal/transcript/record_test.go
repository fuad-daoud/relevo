package transcript

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderRecord(t *testing.T) {
	assistantLine := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"x"}}]}}`)
	userToolResultLine := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":"ok text"}]}}`)

	cases := map[string]struct {
		kind string
		line []byte
		want []string
	}{
		"assistant matches the stream renderer": {
			"claude", assistantLine, Render("claude", assistantLine),
		},
		"user tool_result matches the stream renderer": {
			"claude", userToolResultLine, Render("claude", userToolResultLine),
		},
		"user prompt string, first line only": {
			"claude",
			[]byte(`{"type":"user","message":{"content":"please do X\nsecond line"}}`),
			[]string{"> please do X"},
		},
		"user prompt as a text block list": {
			"claude",
			[]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}`),
			[]string{"> hi"},
		},
		"long prompt truncates with an ellipsis": {
			"claude",
			[]byte(`{"type":"user","message":{"content":"` + strings.Repeat("x", 300) + `"}}`),
			[]string{"> " + strings.Repeat("x", 200) + "…"},
		},
		"attachment is housekeeping": {
			"claude", []byte(`{"type":"attachment"}`), nil,
		},
		"file-history-snapshot is housekeeping": {
			"claude", []byte(`{"type":"file-history-snapshot"}`), nil,
		},
		"system is housekeeping": {
			"claude", []byte(`{"type":"system"}`), nil,
		},
		"summary is housekeeping": {
			"claude", []byte(`{"type":"summary"}`), nil,
		},
		"non-JSON line is nothing, unlike Render": {
			"claude", []byte("relevo-exit: 0"), nil,
		},
		"a non-claude kind renders nothing": {
			"opencode", assistantLine, nil,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := RenderRecord(c.kind, c.line); !reflect.DeepEqual(got, c.want) {
				t.Errorf("RenderRecord(%q, %q) = %q, want %q", c.kind, c.line, got, c.want)
			}
		})
	}
}
