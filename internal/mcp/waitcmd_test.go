package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// serveOnce drives a fully-built Server, so a test can set Mode as well as Verbs.
func serveOnce(t *testing.T, srv *Server, requests []string) []byte {
	t.Helper()
	pr, pw := io.Pipe()
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), pr, &out) }()

	for _, line := range requests {
		if _, err := pw.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return out.Bytes()
}

// callSend drives one tools/call send and returns the tool result's text.
func callSend(t *testing.T, mode Mode, res any) string {
	t.Helper()
	verbs := &fakeVerbs{sendFn: func(context.Context, string, SendArgs) (any, error) { return res, nil }}
	srv := &Server{Verbs: verbs, Version: "test", Mode: mode}
	out := serveOnce(t, srv, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send","arguments":{"name":"webshop","file":"/tmp/plan.md"}}}`,
	})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("want one response line, got %d: %q", len(lines), string(out))
	}
	resp := decodeResponse(t, lines[0])
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	return result.Content[0].Text
}

// TestMCPSendResultPointsAtWait covers the one send result shape: a Claude
// server has no push, so the result points at the wait tool.
func TestMCPSendResultPointsAtWait(t *testing.T) {
	res := sendResult{SendResult: relevo.SendResult{Round: 1}, WaitBudget: "24h0m0s"}
	text := callSend(t, ModeTools, res)
	want := "wait tool:\n  wait(name: \"webshop\", timeout: \"24h0m0s\")"
	if !strings.HasSuffix(text, want) {
		t.Fatalf("send result text = %q, want it to end with:\n%s", text, want)
	}
	if strings.Contains(text, "run_in_background") || strings.Contains(text, "relevo wait") {
		t.Fatalf("send result must carry no background shell block, got %q", text)
	}
}

func TestMCPInstructionsTools(t *testing.T) {
	tools := InstructionsFor(ModeTools, "")

	for _, want := range []string{"wait tool", "still-open", "needs-you", "status(name)", "delivered by the mod"} {
		if !strings.Contains(tools, want) {
			t.Errorf("tools instructions must mention %q", want)
		}
	}
	if strings.Contains(tools, "run_in_background") {
		t.Error("tools instructions must not teach the background shell wait")
	}
	if strings.Contains(tools, "this pane") {
		t.Error(`instructions must say "this mastermind", not "this pane"`)
	}
	if !strings.HasSuffix(tools, mastermind.Guide()) {
		t.Error("tools instructions do not end with the guide")
	}

	// initialize serves the tools text when no override is set.
	srv := &Server{Verbs: &fakeVerbs{}, Version: "test", Mode: ModeTools}
	if res := srv.initializeResult(); res["instructions"] != tools {
		t.Error("initialize must serve the tools text")
	}
}

// TestMCPSendResultOpencodeHasNoWaitLine: an opencode tools server appends no
// background wait; its report arrives as a new turn.
func TestMCPSendResultOpencodeHasNoWaitLine(t *testing.T) {
	res := sendResult{SendResult: relevo.SendResult{Round: 1}, WaitBudget: "24h0m0s"}
	verbs := &fakeVerbs{sendFn: func(context.Context, string, SendArgs) (any, error) { return res, nil }}
	srv := &Server{Verbs: verbs, Version: "test", Mode: ModeTools, Kind: "opencode"}
	out := serveOnce(t, srv, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send","arguments":{"name":"webshop","file":"/tmp/plan.md"}}}`,
	})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("want one response line, got %d: %q", len(lines), string(out))
	}
	resp := decodeResponse(t, lines[0])
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	if text := result.Content[0].Text; strings.Contains(text, "background wait") || strings.Contains(text, "relevo wait") {
		t.Errorf("opencode send result must carry no wait line, got %q", text)
	}
}

// TestMCPInstructionsOpencode pins the opencode prelude: reports arrive as new
// turns, no wait to start, and initialize serves it for an opencode server.
func TestMCPInstructionsOpencode(t *testing.T) {
	text := InstructionsFor(ModeTools, "opencode")
	if !strings.Contains(text, "new turns") {
		t.Error("opencode instructions must say reports arrive as new turns")
	}
	for _, word := range []string{"run_in_background", "background wait"} {
		if strings.Contains(text, word) {
			t.Errorf("opencode instructions must not mention %q", word)
		}
	}
	if !strings.HasSuffix(text, mastermind.Guide()) {
		t.Error("opencode instructions do not end with the guide")
	}

	srv := &Server{Verbs: &fakeVerbs{}, Version: "test", Mode: ModeTools, Kind: "opencode"}
	if res := srv.initializeResult(); res["instructions"] != text {
		t.Error("initialize must serve the opencode text for an opencode server")
	}
}
