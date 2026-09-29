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
			"claude", assistantLine, NewRenderer().Render("claude", assistantLine),
		},
		"user tool_result matches the stream renderer": {
			"claude", userToolResultLine, NewRenderer().Render("claude", userToolResultLine),
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

// TestRenderRecordSanitizesControlBytes pins that a session record's own text
// is sanitised exactly as a stream line's is.
func TestRenderRecordSanitizesControlBytes(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"boom \u001b[2J\u0007"}]}}`)
	got := RenderRecord("claude", line)
	if len(got) != 1 {
		t.Fatalf("RenderRecord = %q, want one line", got)
	}
	if strings.ContainsAny(got[0], "\x1b\x07") {
		t.Errorf("RenderRecord = %q, want no control bytes", got[0])
	}
	if !strings.Contains(got[0], "\uFFFD[2J\uFFFD") {
		t.Errorf("RenderRecord = %q, want the control bytes replaced with U+FFFD", got[0])
	}
}

// TestRenderRecordIsNeverStamped: the session-record path (RenderRecord, behind
// show --owner) is out of the stamp's scope. A record that carries a stream
// timestamp still renders today's bytes.
func TestRenderRecordIsNeverStamped(t *testing.T) {
	utc(t)
	line := []byte(`{"type":"assistant","timestamp":"2026-09-26T20:16:21.719Z","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./..."}}]}}`)
	want := []string{"● Bash go test ./..."}
	if got := RenderRecord("claude", line); !reflect.DeepEqual(got, want) {
		t.Errorf("RenderRecord = %q, want %q", got, want)
	}
}
