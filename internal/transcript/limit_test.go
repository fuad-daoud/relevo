package transcript

import (
	"reflect"
	"testing"
)

// limitLinesCases is TestLimitLines's table: one raw line per row and the
// evidence LimitLines must return for it.
var limitLinesCases = map[string]struct {
	kind string
	line string
	want []string
}{
	"claude error event message": {
		"claude",
		`{"type":"error","message":"rate limit reached, resets in 2h"}`,
		[]string{"rate limit reached, resets in 2h"},
	},
	"claude error event nested error message": {
		"claude",
		`{"type":"error","error":{"message":"usage limit reached"}}`,
		[]string{"usage limit reached"},
	},
	"claude failed result takes its result text": {
		"claude",
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"you've hit your limit"}`,
		[]string{"you've hit your limit"},
	},
	"claude failed result falls back to subtype": {
		"claude",
		`{"type":"result","subtype":"rate_limit_error","is_error":true,"result":""}`,
		[]string{"rate_limit_error"},
	},
	"claude successful result is not evidence": {
		"claude",
		`{"type":"result","subtype":"success","is_error":false,"result":"the quota is exhausted, resets in 1h"}`,
		nil,
	},
	"claude assistant text is not evidence": {
		"claude",
		`{"type":"assistant","message":{"content":[{"type":"text","text":"you've hit your limit, resets in 1h"}]}}`,
		nil,
	},
	"claude tool result is not evidence": {
		"claude",
		`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"rate limit reached"}]}}`,
		nil,
	},
	"claude thinking is not evidence": {
		"claude",
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"I may have hit the rate limit"}]}}`,
		nil,
	},
	"claude rate_limit_event is not evidence": {
		"claude",
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}`,
		nil,
	},
	"agy failed result error string": {
		"agy",
		`{"event":"result","result":{"status":"ERROR","error":"Individual quota reached. Resets in 2h48m52s."}}`,
		[]string{"Individual quota reached. Resets in 2h48m52s."},
	},
	"agy failed result error object": {
		"agy",
		`{"event":"result","result":{"status":"ERROR","error":{"message":"quota exceeded"}}}`,
		[]string{"quota exceeded"},
	},
	"agy successful result is not evidence": {
		"agy",
		`{"event":"result","result":{"status":"SUCCESS","response":"quota exceeded, resets in 1h","error":"quota exceeded"}}`,
		nil,
	},
	"agy result without a status is not evidence": {
		"agy",
		`{"event":"result","result":{"error":"quota exceeded"}}`,
		nil,
	},
	"agy successful step output is not evidence": {
		"agy",
		`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_info":{"output":"quota exceeded, resets in 1h"}}}`,
		nil,
	},
	"agy failed step error is not evidence": {
		"agy",
		`{"event":"step_update","step_update":{"step_type":"tool","state":"ERROR","tool_info":{"error":{"message":"quota exceeded"}}}}`,
		nil,
	},
	"agy init is not evidence": {
		"agy",
		`{"event":"init","init":{"model":"m"}}`,
		nil,
	},
	"opencode error message": {
		"opencode",
		`{"type":"error","error":{"message":"Error 429: rate limit reached, resets in 23m"}}`,
		[]string{"Error 429: rate limit reached, resets in 23m"},
	},
	"opencode text is not evidence": {
		"opencode",
		`{"type":"text","part":{"text":"quota exceeded, resets in 1h"}}`,
		nil,
	},
	"opencode tool result is not evidence": {
		"opencode",
		`{"type":"tool_use","part":{"tool":"bash","state":{"status":"error","error":"rate limit reached"}}}`,
		nil,
	},
	"opencode reasoning is not evidence": {
		"opencode",
		`{"type":"reasoning","part":{"text":"maybe the quota is exhausted"}}`,
		nil,
	},
	"codex error message": {
		"codex",
		`{"type":"error","message":"usage limit reached"}`,
		[]string{"usage limit reached"},
	},
	"codex turn.failed error message": {
		"codex",
		`{"type":"turn.failed","error":{"message":"rate limit reached"}}`,
		[]string{"rate limit reached"},
	},
	"codex agent message is not evidence": {
		"codex",
		`{"type":"item.completed","item":{"type":"agent_message","text":"quota exceeded, resets in 1h"}}`,
		nil,
	},
	"codex item error is not evidence": {
		"codex",
		`{"type":"item.completed","item":{"type":"error","message":"quota exceeded"}}`,
		nil,
	},
	"codex thread event is not evidence": {
		"codex",
		`{"type":"thread.started","thread_id":"t1"}`,
		nil,
	},
	"plain stderr is evidence": {
		"agy",
		"error: Individual quota reached. Resets in 1h0m0s.",
		[]string{"error: Individual quota reached. Resets in 1h0m0s."},
	},
	"blank line is nothing": {
		"claude",
		"   \n",
		nil,
	},
	"exit trailer is nothing": {
		"claude",
		"relevo-exit:1\n",
		nil,
	},
	"rusage trailer is nothing": {
		"claude",
		"relevo-rusage:{\"cpu_ms\":1}\n",
		nil,
	},
	"unknown kind is nothing": {
		"mystery",
		`{"type":"error","message":"usage limit reached"}`,
		nil,
	},
}

// TestLimitLines pins the channel table: which raw stream line is limit
// evidence per kind, and which is not.
func TestLimitLines(t *testing.T) {
	for name, c := range limitLinesCases {
		t.Run(name, func(t *testing.T) {
			if got := LimitLines(c.kind, []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("LimitLines(%q, %q) = %q, want %q", c.kind, c.line, got, c.want)
			}
		})
	}
}

// TestLimitLinesByKind pins that every kind drops model-authored text even when
// it is shaped exactly like a limit, and keeps its own structured failure.
func TestLimitLinesByKind(t *testing.T) {
	modelText := map[string]string{
		"claude":   `{"type":"assistant","message":{"content":[{"type":"text","text":"Individual quota reached. Resets in 2h."}]}}`,
		"agy":      `{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_info":{"output":"Individual quota reached. Resets in 2h."}}}`,
		"opencode": `{"type":"text","part":{"text":"Individual quota reached. Resets in 2h."}}`,
		"codex":    `{"type":"item.completed","item":{"type":"agent_message","text":"Individual quota reached. Resets in 2h."}}`,
	}
	structured := map[string]string{
		"claude":   `{"type":"error","message":"Individual quota reached. Resets in 2h."}`,
		"agy":      `{"event":"result","result":{"status":"ERROR","error":"Individual quota reached. Resets in 2h."}}`,
		"opencode": `{"type":"error","error":{"message":"Individual quota reached. Resets in 2h."}}`,
		"codex":    `{"type":"error","message":"Individual quota reached. Resets in 2h."}`,
	}
	for kind := range structured {
		if got := LimitLines(kind, []byte(modelText[kind])); got != nil {
			t.Errorf("LimitLines(%s, model text) = %q, want nothing", kind, got)
		}
		if got := LimitLines(kind, []byte(structured[kind])); len(got) != 1 {
			t.Errorf("LimitLines(%s, structured failure) = %q, want one line", kind, got)
		}
	}
}
