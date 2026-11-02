package reporttail

import (
	"testing"
)

// tokenTrailedFenceBody is the leaked-token shape: the closing fence carries
// chat-template tokens on its own line, so the block ends there and the lines
// after it are none of the block's concern.
func tokenTrailedFenceBody() string {
	return "```relevo\n" +
		"status: done\n" +
		"halted_at: \"\"\n" +
		"changed_paths: [\"internal/serve/guard.go\", \"internal/serve/guard_test.go\"]\n" +
		"commands_run: [\"git status\", \"go vet ./...\", \"go test -count=1 ./internal/serve/\"]\n" +
		"not_done: []\n" +
		"```<|end|><|start|><|start|>assistant<|channel|>analysis<|message|>Now create done marker file.<|end|><|start|> QPushButton to=shell<|channel|>analysis <|constrain|>xml<|message|><rfl:parameter name=\"command\">\n" +
		"touch /x/state/relevo/serve-hardening/out/004-done && ls -la /x/state/relevo/serve-hardening/out/\n" +
		"```\n"
}

// TestParseWithReasonClosesOnTokenTrailedFence pins the token-fence rule: a
// closing fence trailed directly by chat-template tokens still ends the
// block, the parse reports the block's status, the ignored trailer is flagged
// for the report note, and the body stays whole past the fence.
func TestParseWithReasonClosesOnTokenTrailedFence(t *testing.T) {
	t.Parallel()
	body := tokenTrailedFenceBody()
	tail, ok, reason := ParseWithReason([]byte(body))
	if !ok {
		t.Fatalf("ParseWithReason ok = false, reason %q", reason)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
	if tail.Status != OutcomeDone {
		t.Errorf("Status = %q, want %q", tail.Status, OutcomeDone)
	}
	if !tail.TrailerIgnored {
		t.Errorf("TrailerIgnored = false, want true: the fence carried token trailer")
	}
	if got := StripTail([]byte(body)); string(got) != body {
		t.Errorf("StripTail = %q, want the body byte-identical", got)
	}
}

// TestParseWithReasonFenceWithOtherTextDoesNotClose pins that only tokens
// trail a fence: a closing fence followed by anything else is no fence, so
// the block runs to end of file and the stray line rejects as before.
func TestParseWithReasonFenceWithOtherTextDoesNotClose(t *testing.T) {
	t.Parallel()
	body := "```relevo\nstatus: done\n``` trailing prose\n"
	_, ok, reason := ParseWithReason([]byte(body))
	if ok {
		t.Fatalf("ParseWithReason ok = true, want false")
	}
	if want := "tail: line 3 has no ':'"; reason != want {
		t.Fatalf("reason = %q, want %q", reason, want)
	}
}

// TestParseWithReasonRejectableBodyWithTokenFenceStillRejects pins that a
// token-trailed fence does not rescue a malformed body: the block's own
// reject wins over the trailer tolerance.
func TestParseWithReasonRejectableBodyWithTokenFenceStillRejects(t *testing.T) {
	t.Parallel()
	body := "```relevo\nstatus: done\nno colon on this line\n```<|end|><|start|>\n"
	_, ok, reason := ParseWithReason([]byte(body))
	if ok {
		t.Fatalf("ParseWithReason ok = true, want false")
	}
	if want := "tail: line 3 has no ':'"; reason != want {
		t.Fatalf("reason = %q, want %q", reason, want)
	}
}
